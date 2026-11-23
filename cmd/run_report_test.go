package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

type cliRun struct {
	stdout, stderr string
	exitCode       int
}

func runCLIForReport(t *testing.T, args ...string) cliRun {
	t.Helper()
	cmd := exec.Command(os.Args[0], append([]string{"test-run-report"}, args...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	exitCode := 0
	if err != nil {
		exit, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatal(err)
		}
		exitCode = exit.ExitCode()
	}
	return cliRun{stdout.String(), stderr.String(), exitCode}
}

func TestRunReport(t *testing.T) {
	const (
		path         = "/v1/environments/456/campaigns"
		template     = "/v1/environments/{environment_id}/campaigns"
		itemTemplate = template + "/{campaign_id}"
		email        = "recipient@example.test"
		name         = "Private Query Name"
		body         = `{"body":"Private Request Body"}`
		jq           = `error("Private Projection Expression")`
	)
	for _, tc := range []struct {
		name                               string
		command                            string
		args                               []string
		reason, code, method, route, match string
		requests, status                   int
		noToken, warmCache                 bool
	}{
		{name: "matched", command: "api", args: []string{path}, method: "GET", route: template, requests: 1, status: 200},
		{name: "absent", command: "api", args: []string{path + "/42/absent"}, method: "GET", reason: "not_in_spec", code: "VALIDATION_ERROR", match: "not_in_spec"},
		{name: "wrong method", command: "api", args: []string{path, "-X", "DELETE"}, method: "DELETE", reason: "wrong_method", code: "VALIDATION_ERROR", match: "not_in_spec"},
		{name: "word matches parameter", command: "api", args: []string{path + "/card"}, method: "GET", route: itemTemplate, reason: "http_error", code: "API_ERROR", requests: 1, status: 404},
		{name: "precondition response", command: "api", args: []string{path + "/42"}, method: "GET", route: itemTemplate, reason: "http_error", code: "API_ERROR", requests: 1, status: 428},
		{name: "truncated error response", command: "api", args: []string{path}, method: "GET", route: template, reason: "http_error", code: "GENERAL_ERROR", requests: 1, status: 404},
		{name: "truncated success response", command: "api", args: []string{path}, method: "GET", route: template, reason: "network", code: "GENERAL_ERROR", requests: 1, status: 200},
		{name: "failed redirect", command: "api", args: []string{path}, method: "GET", route: template, reason: "http_error", code: "GENERAL_ERROR", requests: 1, status: 302},
		{name: "preflight disabled", command: "api", args: []string{path, "--no-preflight"}, method: "GET", requests: 1, status: 200, warmCache: true},
		{name: "spec unavailable", command: "api", args: []string{path}, method: "GET", requests: 1, status: 200},
		{name: "missing path parameter", command: "api", args: []string{itemTemplate}, method: "GET", reason: "missing_path_param", code: "VALIDATION_ERROR"},
		{name: "unknown flag", command: "api", args: []string{path, "--unknown-flag"}, reason: "unknown_flag", code: "GENERAL_ERROR"},
		{name: "wrong argument count", command: "api", reason: "arg_count", code: "GENERAL_ERROR"},
		{name: "nested argument count", command: "domains list", args: []string{"extra"}, reason: "arg_count", code: "GENERAL_ERROR"},
		{name: "unknown command", args: []string{"unknown-command"}, reason: "unknown_command", code: "GENERAL_ERROR"},
		{name: "invalid JSON", command: "api", args: []string{path, "--json", "{"}, reason: "invalid_json", code: "VALIDATION_ERROR"},
		{name: "invalid JSON binding", command: "api", args: []string{path, "--json", "$value", "--argjson", "value={"}, reason: "invalid_json", code: "VALIDATION_ERROR"},
		{name: "invalid bespoke JSON", command: "send email", args: []string{"--identifiers", "{"}, reason: "invalid_json", code: "VALIDATION_ERROR"},
		{name: "invalid params", command: "api", args: []string{path, "--params", `{"name":`}, reason: "invalid_params", code: "VALIDATION_ERROR"},
		{name: "invalid inline query", command: "api", args: []string{path + "?name=%XX"}, method: "GET", reason: "invalid_params", code: "VALIDATION_ERROR"},
		{name: "invalid jq syntax", command: "api", args: []string{path, "--jq", ".["}, method: "GET", route: template, reason: "jq_error", code: "GENERAL_ERROR", requests: 1, status: 200},
		{name: "jq evaluation keeps caller inputs private", command: "api", args: []string{path + "/" + email, "-X", "GET", "--params", `{"name":"` + name + `"}`, "--json", body, "--jq", jq}, method: "GET", route: itemTemplate, reason: "jq_error", code: "GENERAL_ERROR", requests: 1, status: 200},
		{name: "unknown schema resource", command: "schema", args: []string{"absent"}, reason: "schema_not_found", code: "VALIDATION_ERROR"},
		{name: "pagination", command: "api", args: []string{path, "--page-all"}, method: "GET", route: template, requests: 2, status: 201},
		{name: "retry", command: "api", args: []string{path}, method: "GET", route: template, requests: 2, status: 200},
		{name: "missing auth", command: "api", args: []string{path, "--no-preflight"}, method: "GET", reason: "auth", code: "GENERAL_ERROR", noToken: true},
		{name: "invalid login credential", command: "auth login", args: []string{"not-a-token"}, reason: "auth", code: "VALIDATION_ERROR"},
		{name: "rejected token exchange", command: "api", args: []string{path, "--no-preflight"}, method: "GET", reason: "auth", code: "GENERAL_ERROR"},
		{name: "token endpoint outage", command: "api", args: []string{path, "--no-preflight"}, method: "GET", reason: "http_error", code: "GENERAL_ERROR"},
		{name: "unreachable token endpoint", command: "api", args: []string{path, "--no-preflight"}, method: "GET", reason: "network", code: "GENERAL_ERROR"},
		{name: "unauthorized response", command: "api", args: []string{path}, method: "GET", route: template, reason: "auth", code: "AUTH_ERROR", requests: 2, status: 401},
		{name: "network failure", command: "api", args: []string{path, "-X", "POST"}, method: "POST", route: template, reason: "network", code: "GENERAL_ERROR", requests: 1},
		{name: "bespoke with cached match", command: "domains list", args: []string{"--env-id", "456"}, method: "GET", route: "/v1/environments/{environment_id}/domains", requests: 1, status: 200, warmCache: true},
		{name: "bespoke without cached match", command: "domains list", args: []string{"--env-id", "456"}, method: "GET", requests: 1, status: 200},
		{name: "other failure", command: "api", args: []string{path, "-X", "PRIVATE", "--read-only", "--no-preflight"}, reason: "other", code: "GENERAL_ERROR"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CIO_CONFIG", "")
			t.Setenv("CIO_PROFILE", "")
			t.Setenv("CIO_TIMEOUT", "")
			t.Setenv("CIO_MAX_RETRIES", "1")
			var attempts atomic.Int32
			server := newPreflightServerWithAPI(t, func(w http.ResponseWriter, r *http.Request) {
				switch tc.name {
				case "word matches parameter", "precondition response", "unauthorized response":
					w.WriteHeader(tc.status)
					fmt.Fprint(w, `{"error":"private API error detail"}`)
					return
				case "truncated success response":
					w.Header().Set("Content-Length", "10")
					w.WriteHeader(http.StatusOK)
					fmt.Fprint(w, "{")
					return
				case "truncated error response":
					w.Header().Set("Content-Length", "10")
					w.WriteHeader(http.StatusNotFound)
					fmt.Fprint(w, "{")
					return
				case "failed redirect":
					w.Header().Set("Location", "http://[invalid")
					w.WriteHeader(http.StatusFound)
					return
				case "pagination":
					if r.URL.Query().Get("page") == "2" {
						w.WriteHeader(http.StatusCreated)
					}
					fmt.Fprint(w, `{"total_pages":2}`)
					return
				case "retry":
					if attempts.Add(1) == 1 {
						w.WriteHeader(http.StatusServiceUnavailable)
						return
					}
				case "network failure":
					conn, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					_ = conn.Close()
					return
				}
				fmt.Fprint(w, `{"ok":true}`)
			})
			if tc.noToken {
				t.Setenv("CIO_TOKEN", "")
			}
			if tc.name == "rejected token exchange" {
				server.mu.Lock()
				server.tokenStatus = http.StatusUnauthorized
				server.mu.Unlock()
			}
			if tc.name == "token endpoint outage" {
				server.mu.Lock()
				server.tokenStatus = http.StatusServiceUnavailable
				server.mu.Unlock()
			}
			if tc.name == "spec unavailable" {
				server.setSpec("unavailable")
			}
			if tc.name == "unreachable token endpoint" {
				server.Close()
			}
			if tc.warmCache {
				t.Setenv("CIO_RUN_REPORT", "")
				server.setSpec(strings.ReplaceAll(preflightSpec(), "/all_metrics", "/domains"))
				if run := runCLIForReport(t, "schema", "--api-url", server.URL); run.exitCode != 0 {
					t.Fatal(run)
				}
			}

			reportPath := filepath.Join(t.TempDir(), "report.json")
			t.Setenv("CIO_RUN_REPORT", reportPath)
			if tc.name == "matched" {
				if err := os.WriteFile(reportPath, bytes.Repeat([]byte("stale"), 100), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			args := append(strings.Fields(tc.command), tc.args...)
			run := runCLIForReport(t, append(args, "--api-url", server.URL)...)
			data, err := os.ReadFile(reportPath)
			if err != nil {
				t.Fatal(err)
			}

			var got map[string]any
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatal(err)
			}

			want := map[string]any{
				"version": "0.0.39", "command": tc.command, "exit_code": float64(0),
				"error_code": nil, "reason": nil, "method": nil, "route": nil,
				"route_match": "not_checked", "requests": float64(tc.requests), "status": nil,
			}
			if tc.code != "" {
				if run.exitCode == 0 {
					t.Fatalf("command succeeded, want %s", tc.code)
				}
				want["exit_code"], want["error_code"], want["reason"] = float64(run.exitCode), tc.code, tc.reason
				if !strings.Contains(run.stderr, `"code":"`+tc.code+`"`) {
					t.Errorf("command stderr missing %s: %s", tc.code, run.stderr)
				}
			}
			if tc.method != "" {
				want["method"] = tc.method
			}
			if tc.route != "" {
				want["route"], want["route_match"] = tc.route, "matched"
			}
			if tc.match != "" {
				want["route_match"] = tc.match
			}
			if tc.status != 0 {
				want["status"] = float64(tc.status)
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("report = %s, want %v", data, want)
			}
			if run.exitCode != int(want["exit_code"].(float64)) {
				t.Errorf("process exited %d, stderr: %s", run.exitCode, run.stderr)
			}
			if count := len(server.requested()); count != tc.requests {
				t.Errorf("API received %d requests, want %d", count, tc.requests)
			}
			if tc.name == "jq evaluation keeps caller inputs private" {
				for _, secret := range []string{email, name, "Private Request Body", "Private Projection Expression"} {
					if strings.Contains(string(data), secret) {
						t.Error("report contains caller input")
					}
				}
			}
		})
	}
}

func TestRunReportWriteFailurePreservesCLIOutput(t *testing.T) {
	for _, args := range [][]string{{"--version"}, {"api", "/any", "--json", "{"}} {
		t.Setenv("CIO_RUN_REPORT", "")
		without := runCLIForReport(t, args...)
		dir := t.TempDir()
		t.Setenv("CIO_RUN_REPORT", dir)
		if with := runCLIForReport(t, args...); with != without {
			t.Errorf("unwritable report changed invocation: got %+v, want %+v", with, without)
		}
		if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
			t.Errorf("report path %s should stay an empty directory, got %v (%v)", dir, entries, err)
		}
	}
}
