package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/customerio/cli/internal/output"
	"github.com/customerio/cli/internal/useragent"
)

func TestRootHelpListsAllCommands(t *testing.T) {
	stdout, _, err := executeCommand("--help")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The branded help screen must enumerate every registered command, not a
	// curated subset, so newly added commands can't silently go missing.
	if !strings.Contains(stdout, "All commands:") {
		t.Fatalf("help missing 'All commands:' section:\n%s", stdout)
	}
	for _, name := range []string{"api", "auth", "domains", "prime", "schema", "send", "skills", "transactional"} {
		if !strings.Contains(stdout, name) {
			t.Errorf("help output missing command %q", name)
		}
	}
}

func TestSetVersionIgnoresEmptyVersion(t *testing.T) {
	oldRootVersion := rootCmd.Version
	t.Cleanup(func() {
		rootCmd.Version = oldRootVersion
		useragent.SetVersion("dev")
	})

	SetVersion("v1.2.3")
	SetVersion("")

	if got := rootCmd.Version; got != "v1.2.3" {
		t.Fatalf("rootCmd.Version = %q, want %q", got, "v1.2.3")
	}
	if got, want := useragent.Get(), "Customer.io-CLI/v1.2.3 (+https://github.com/customerio/cli)"; got != want {
		t.Fatalf("useragent.Get() = %q, want %q", got, want)
	}
}

func TestResolveArgFileRefs(t *testing.T) {
	dir := t.TempDir()
	body := filepath.Join(dir, "body.txt")
	if err := os.WriteFile(body, []byte(`<x>Hi "there"`), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := resolveArgFileRefs("arg", []string{"name=literal", "html=@" + body})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"name=literal", `html=<x>Hi "there"`}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("mismatch:\n want: %q\n got:  %q", want, got)
	}

	if _, err := resolveArgFileRefs("arg", []string{"x=@" + filepath.Join(dir, "missing")}); err == nil {
		t.Error("expected error for missing file")
	}
	if _, err := resolveArgFileRefs("arg", []string{"x=@"}); err == nil {
		t.Error("expected error for empty @ path")
	}
}

func TestExecuteHelperProcess(t *testing.T) {
	if os.Getenv("CIO_EXECUTE_HELPER") != "1" {
		return
	}
	args := os.Args
	for i, arg := range args {
		if arg == "--" {
			args = args[i+1:]
			break
		}
	}
	rootCmd.SetArgs(args)
	Execute()
}

func TestExecute_PrintsAPIErrorOnce(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/service_accounts/oauth/token":
			_, _ = w.Write([]byte(`{"access_token":"jwt-test-session","token_type":"Bearer","expires_in":3600}`))
		case "/v1/forbidden":
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"errors":[{"code":"update_requires_edit_permission","detail":"Updating this one-time send requires the Edit permission.","status":"403"}]}`))
		default:
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"errors":[{"detail":"boom"}]}`))
		}
	}))
	defer server.Close()

	cases := []struct {
		desc     string
		path     string
		wantCode string
		wantExit int
	}{
		{
			desc:     "given a coded 403, cio api should print one AUTHZ_ERROR and exit 4",
			path:     "/v1/forbidden",
			wantCode: output.CodeAuthzError,
			wantExit: output.ExitAuthz,
		},
		{
			desc:     "given a 500, cio api should print one API_ERROR and exit 5",
			path:     "/v1/broken",
			wantCode: output.CodeAPIError,
			wantExit: output.ExitAPI,
		},
	}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestExecuteHelperProcess$", "--",
				"api", tc.path, "--no-preflight", "--api-url", server.URL, "--token", "sa_live_test123")
			cmd.Env = append(os.Environ(), "CIO_EXECUTE_HELPER=1", "HOME="+t.TempDir())
			var stderr bytes.Buffer
			cmd.Stderr = &stderr

			err := cmd.Run()

			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) {
				t.Fatalf("expected a non-zero exit, got %v", err)
			}
			if exitErr.ExitCode() != tc.wantExit {
				t.Errorf("exit code = %d, want %d", exitErr.ExitCode(), tc.wantExit)
			}
			lines := strings.Split(strings.TrimSpace(stderr.String()), "\n")
			if len(lines) != 1 {
				t.Fatalf("expected one stderr line, got %d:\n%s", len(lines), stderr.String())
			}
			var got output.CLIError
			if err := json.Unmarshal([]byte(lines[0]), &got); err != nil {
				t.Fatalf("stderr should be a JSON error: %v\n%s", err, stderr.String())
			}
			if got.Code != tc.wantCode {
				t.Errorf("code = %q, want %q", got.Code, tc.wantCode)
			}
		})
	}
}
