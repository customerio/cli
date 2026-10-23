package cmd

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const flexAccount = `{"id":1,"billing_vendor":"modern_stripe","sso_setting":"off"}`

// addFundsServer answers the token exchange, the account read and the link
// mint, and records every request so a test can show nothing was minted.
type addFundsServer struct {
	*httptest.Server
	requests int
	mints    int
	mintAuth string
	mintBody string
	// commands maps each requested path to the X-CIO-Command header it carried.
	commands map[string]string
}

func newAddFundsServer(t *testing.T, account string, mintStatus int, mintResponse string) *addFundsServer {
	t.Helper()
	s := &addFundsServer{commands: map[string]string{}}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.requests++
		s.commands[r.URL.Path] = r.Header.Get("X-CIO-Command")
		switch r.URL.Path {
		case "/v1/service_accounts/oauth/token":
			_, _ = w.Write([]byte(`{"access_token":"jwt-test-session","token_type":"Bearer","expires_in":3600}`))
		case "/v1/accounts/current":
			if r.Header.Get("Authorization") != "Bearer jwt-test-session" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"account":` + account + `}`))
		case "/v1/login_cli/link":
			s.mints++
			s.mintAuth = r.Header.Get("Authorization")
			body, _ := io.ReadAll(r.Body)
			s.mintBody = string(body)
			w.WriteHeader(mintStatus)
			_, _ = w.Write([]byte(mintResponse))
		default:
			t.Errorf("unexpected request to %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func setupAddFunds(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CIO_TOKEN", "sa_live_flex")
	t.Setenv("CIO_ACCESS_TOKEN", "")
	t.Setenv("CIO_API_URL", "")
	t.Setenv("CIO_UI_URL", "https://fly.example.test")
}

func TestBillingAddFunds_PrintsAddFundsLink(t *testing.T) {
	setupAddFunds(t)
	server := newAddFundsServer(t, flexAccount, http.StatusOK, `{"handoff_token":"handoff-abc","expires_in":60}`)

	stdout, stderr, err := executeCommand("billing", "add-funds", "--api-url", server.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v\nstderr: %s", err, stderr)
	}

	if server.mints != 1 {
		t.Fatalf("expected 1 mint request, got %d", server.mints)
	}
	if server.mintAuth != "Bearer sa_live_flex" {
		t.Errorf("expected the service account token as the Bearer, got %q", server.mintAuth)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(server.mintBody), &body); err != nil {
		t.Fatalf("mint body is not JSON: %v (%q)", err, server.mintBody)
	}
	if body["destination"] != "add_funds" {
		t.Errorf("expected destination add_funds, got %v", body["destination"])
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("invalid JSON output: %v\nstdout: %s", err, stdout)
	}
	wantURL := "https://fly.example.test/cli?token=handoff-abc"
	if result["url"] != wantURL {
		t.Errorf("expected url %q, got %v", wantURL, result["url"])
	}
	if result["status"] != "ok" || result["expires_in"] != float64(60) {
		t.Errorf("expected status ok and expires_in 60, got %v", result)
	}
	if _, ok := result["sign_in_required"]; ok {
		t.Errorf("a sign-in link should not report sign_in_required, got %v", result)
	}
	if !strings.Contains(stderr, wantURL) {
		t.Errorf("expected stderr to print the URL, got: %s", stderr)
	}
}

func TestBillingAddFunds_JQReadsTheURL(t *testing.T) {
	setupAddFunds(t)
	server := newAddFundsServer(t, flexAccount, http.StatusOK, `{"handoff_token":"handoff-abc","expires_in":60}`)

	stdout, stderr, err := executeCommand("billing", "add-funds", "--api-url", server.URL, "--jq", ".url", "-r")
	if err != nil {
		t.Fatalf("unexpected error: %v\nstderr: %s", err, stderr)
	}
	if want := "https://fly.example.test/cli?token=handoff-abc\n"; stdout != want {
		t.Errorf("expected stdout %q, got %q", want, stdout)
	}
}

func TestBillingAddFunds_NamesTheCommandOnEveryRequest(t *testing.T) {
	setupAddFunds(t)
	server := newAddFundsServer(t, flexAccount, http.StatusOK, `{"handoff_token":"handoff-abc","expires_in":60}`)

	// Flag values must stay off the header: only the command name is sent.
	if _, stderr, err := executeCommand("billing", "add-funds", "--api-url", server.URL, "--jq", ".url", "-r"); err != nil {
		t.Fatalf("unexpected error: %v\nstderr: %s", err, stderr)
	}

	for _, path := range []string{"/v1/service_accounts/oauth/token", "/v1/accounts/current", "/v1/login_cli/link"} {
		got, ok := server.commands[path]
		if !ok {
			t.Errorf("expected a request to %s", path)
			continue
		}
		if got != "billing add-funds" {
			t.Errorf("X-CIO-Command on %s: got %q, want %q", path, got, "billing add-funds")
		}
	}
}

func TestBillingAddFunds_RefusesOtherPlans(t *testing.T) {
	cases := map[string]string{
		"another vendor":         `{"id":1,"billing_vendor":"ordway","sso_setting":"off"}`,
		"no vendor":              `{"id":1,"sso_setting":"off"}`,
		"another vendor and SSO": `{"id":1,"billing_vendor":"ordway","sso_setting":"required"}`,
	}
	for name, account := range cases {
		t.Run(name, func(t *testing.T) {
			setupAddFunds(t)
			server := newAddFundsServer(t, account, http.StatusOK, `{"handoff_token":"handoff-abc","expires_in":60}`)

			stdout, _, err := executeCommand("billing", "add-funds", "--api-url", server.URL)
			if err == nil {
				t.Fatalf("expected an error, got stdout: %s", stdout)
			}
			if !strings.Contains(err.Error(), "Flex") {
				t.Errorf("expected the error to name the Flex plan, got %q", err)
			}
			if server.mints != 0 {
				t.Errorf("expected no mint request, got %d", server.mints)
			}
			if stdout != "" {
				t.Errorf("expected no stdout, got %q", stdout)
			}
		})
	}
}

func TestBillingAddFunds_SSOPrintsTheBillingPage(t *testing.T) {
	setupAddFunds(t)
	server := newAddFundsServer(t, `{"id":1,"billing_vendor":"modern_stripe","sso_setting":"required"}`, http.StatusOK, `{"handoff_token":"handoff-abc","expires_in":60}`)

	stdout, stderr, err := executeCommand("billing", "add-funds", "--api-url", server.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v\nstderr: %s", err, stderr)
	}
	if server.mints != 0 {
		t.Errorf("expected no mint request for an SSO account, got %d", server.mints)
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("invalid JSON output: %v\nstdout: %s", err, stdout)
	}
	wantURL := "https://fly.example.test/settings/billing/your-plan?purchaseModal=account-funds&from=cli"
	if result["url"] != wantURL {
		t.Errorf("expected url %q, got %v", wantURL, result["url"])
	}
	if result["sign_in_required"] != true {
		t.Errorf("expected sign_in_required true, got %v", result)
	}
	if _, ok := result["expires_in"]; ok {
		t.Errorf("the billing page URL does not expire, got %v", result)
	}
	if !strings.Contains(stderr, "SSO") {
		t.Errorf("expected stderr to explain SSO, got: %s", stderr)
	}
}

func TestBillingAddFunds_DryRunMintsNothing(t *testing.T) {
	setupAddFunds(t)
	server := newAddFundsServer(t, flexAccount, http.StatusOK, `{"handoff_token":"handoff-abc","expires_in":60}`)

	stdout, stderr, err := executeCommand("billing", "add-funds", "--api-url", server.URL, "--dry-run")
	if err != nil {
		t.Fatalf("unexpected error: %v\nstderr: %s", err, stderr)
	}
	if server.mints != 0 {
		t.Errorf("expected no mint request on a dry run, got %d", server.mints)
	}
	if strings.Contains(stdout, "sa_live_flex") {
		t.Errorf("dry run must not print the token, got: %s", stdout)
	}

	var result struct {
		DryRun  bool              `json:"dry_run"`
		Method  string            `json:"method"`
		URL     string            `json:"url"`
		Headers map[string]string `json:"headers"`
		Body    map[string]string `json:"body"`
	}
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("invalid JSON output: %v\nstdout: %s", err, stdout)
	}
	if !result.DryRun || result.Method != "POST" || result.URL != server.URL+"/v1/login_cli/link" {
		t.Errorf("expected a POST to /v1/login_cli/link, got %+v", result)
	}
	if result.Headers["Authorization"] != "Bearer [REDACTED]" {
		t.Errorf("expected a redacted Authorization header, got %q", result.Headers["Authorization"])
	}
	if result.Body["destination"] != "add_funds" {
		t.Errorf("expected destination add_funds, got %v", result.Body)
	}
}

func TestBillingAddFunds_ReadOnlyMintsNothing(t *testing.T) {
	setupAddFunds(t)
	server := newAddFundsServer(t, flexAccount, http.StatusOK, `{"handoff_token":"handoff-abc","expires_in":60}`)

	_, _, err := executeCommand("billing", "add-funds", "--api-url", server.URL, "--read-only")
	if err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("expected a read-only error, got %v", err)
	}
	if server.requests != 0 {
		t.Errorf("expected no requests under --read-only, got %d", server.requests)
	}
}

func TestBillingAddFunds_RefusesAnAccessToken(t *testing.T) {
	setupAddFunds(t)
	t.Setenv("CIO_ACCESS_TOKEN", "jwt-from-elsewhere")
	server := newAddFundsServer(t, flexAccount, http.StatusOK, `{"handoff_token":"handoff-abc","expires_in":60}`)

	_, _, err := executeCommand("billing", "add-funds", "--api-url", server.URL)
	if err == nil || !strings.Contains(err.Error(), "CIO_ACCESS_TOKEN") {
		t.Fatalf("expected an error naming CIO_ACCESS_TOKEN, got %v", err)
	}
	if server.requests != 0 {
		t.Errorf("expected no requests with an access token, got %d", server.requests)
	}
}

func TestBillingAddFunds_RequiresAToken(t *testing.T) {
	setupAddFunds(t)
	t.Setenv("CIO_TOKEN", "")
	server := newAddFundsServer(t, flexAccount, http.StatusOK, `{"handoff_token":"handoff-abc","expires_in":60}`)

	_, _, err := executeCommand("billing", "add-funds", "--api-url", server.URL)
	if err == nil || !strings.Contains(err.Error(), "not authenticated") {
		t.Fatalf("expected a not-authenticated error, got %v", err)
	}
	if server.requests != 0 {
		t.Errorf("expected no requests without a token, got %d", server.requests)
	}
}

func TestBillingAddFunds_PassesOnTheServerRefusal(t *testing.T) {
	cases := map[string]struct {
		status int
		detail string
	}{
		"no billing permission": {http.StatusForbidden, "Adding funds needs permission to manage billing on this account. Ask an account admin to add funds or grant that permission."},
		"not on Flex":           {http.StatusBadRequest, "Add funds is only available on the Flex plan."},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			setupAddFunds(t)
			refusal, _ := json.Marshal(map[string]any{"errors": []map[string]any{{"detail": tc.detail, "status": tc.status}}})
			server := newAddFundsServer(t, flexAccount, tc.status, string(refusal))

			stdout, _, err := executeCommand("billing", "add-funds", "--api-url", server.URL)
			if err == nil {
				t.Fatalf("expected an error, got stdout: %s", stdout)
			}
			if err.Error() != tc.detail {
				t.Errorf("expected the server's message %q, got %q", tc.detail, err)
			}
			if stdout != "" {
				t.Errorf("expected no stdout, got %q", stdout)
			}
		})
	}
}
