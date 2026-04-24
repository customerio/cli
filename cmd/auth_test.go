package cmd

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/customerio/cli/internal/client"
)

func executeCommand(args ...string) (stdout, stderr string, err error) {
	stdoutBuf := new(bytes.Buffer)
	stderrBuf := new(bytes.Buffer)

	rootCmd.SetOut(stdoutBuf)
	rootCmd.SetErr(stderrBuf)
	rootCmd.SetArgs(args)

	// Reset persistent flags before each test.
	_ = rootCmd.PersistentFlags().Set("jq", "")
	_ = rootCmd.PersistentFlags().Set("params", "")
	_ = rootCmd.PersistentFlags().Set("json", "")
	_ = rootCmd.PersistentFlags().Set("dry-run", "false")
	_ = rootCmd.PersistentFlags().Set("api-url", "")
	_ = rootCmd.PersistentFlags().Set("token", "")
	_ = rootCmd.PersistentFlags().Set("scope", "")

	// Reset local flags on subcommands that persist across test runs.
	if f := apiCmd.Flags().Lookup("method"); f != nil {
		_ = apiCmd.Flags().Set("method", "")
	}

	// Reset send/transactional persistent flags.
	for _, name := range []string{"environment-id"} {
		if f := sendCmd.PersistentFlags().Lookup(name); f != nil {
			_ = sendCmd.PersistentFlags().Set(name, "")
		}
		if f := transactionalCmd.PersistentFlags().Lookup(name); f != nil {
			_ = transactionalCmd.PersistentFlags().Set(name, "")
		}
	}

	// Reset channel-specific local flags on send and transactional send subcommands.
	channelFlags := []string{"to", "from", "subject", "body", "text", "reply-to", "bcc",
		"identifiers", "message-data", "transactional-message-id",
		"title", "message", "image-url", "link"}
	for _, sub := range sendCmd.Commands() {
		for _, name := range channelFlags {
			if f := sub.Flags().Lookup(name); f != nil {
				_ = sub.Flags().Set(name, "")
			}
		}
	}
	for _, sub := range transactionalSendCmd.Commands() {
		for _, name := range channelFlags {
			if f := sub.Flags().Lookup(name); f != nil {
				_ = sub.Flags().Set(name, "")
			}
		}
	}

	err = rootCmd.Execute()

	return stdoutBuf.String(), stderrBuf.String(), err
}

// oauthServer creates a test server that handles the OAuth token exchange,
// account discovery (GET /v1/accounts/current), and regular API requests.
func oauthServer(t *testing.T, wantSecret string) *httptest.Server {
	return oauthServerWithRegion(t, wantSecret, "us")
}

func oauthServerWithRegion(t *testing.T, wantSecret, region string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/service_accounts/oauth/token" {
			if err := r.ParseForm(); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			secret := r.PostFormValue("client_secret")
			if secret != wantSecret {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":"invalid_client","error_description":"invalid client credentials"}`))
				return
			}
			_, _ = w.Write([]byte(`{"access_token":"jwt-test-session","token_type":"Bearer","expires_in":3600}`))
			return
		}

		// Account discovery — returns data_center.
		if r.URL.Path == "/v1/accounts/current" {
			auth := r.Header.Get("Authorization")
			if auth != "Bearer jwt-test-session" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"account":{"id":1,"name":"Test Account","data_center":"` + region + `"}}`))
			return
		}

		// Regular API — check JWT.
		auth := r.Header.Get("Authorization")
		if auth != "Bearer jwt-test-session" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
			return
		}
		_, _ = w.Write([]byte(`{"campaigns":[]}`))
	}))
}

func TestAuthLogin_SavesToken(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	t.Setenv("CIO_TOKEN", "")

	server := oauthServer(t, "sa_live_test123")
	defer server.Close()

	stdout, _, err := executeCommand("auth", "login", "sa_live_test123", "--api-url", server.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("invalid JSON output: %v\nstdout: %s", err, stdout)
	}
	if result["status"] != "ok" {
		t.Errorf("expected status ok, got %v", result["status"])
	}

	// Verify file was written.
	data, err := os.ReadFile(filepath.Join(tmpDir, ".cio", "config.json"))
	if err != nil {
		t.Fatalf("failed to read config file: %v", err)
	}

	var creds map[string]any
	if err := json.Unmarshal(data, &creds); err != nil {
		t.Fatalf("invalid JSON in config file: %v", err)
	}
	if creds["service_account_token"] != "sa_live_test123" {
		t.Errorf("expected sa_live_test123, got %v", creds["service_account_token"])
	}
	// Should have cached the JWT.
	if creds["access_token"] != "jwt-test-session" {
		t.Errorf("expected cached JWT, got %v", creds["access_token"])
	}

	// Verify file permissions.
	info, err := os.Stat(filepath.Join(tmpDir, ".cio", "config.json"))
	if err != nil {
		t.Fatalf("failed to stat config file: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("expected 0600 permissions, got %o", perm)
	}
}

func TestAuthLogin_UsesCIOAPIURLForTokenExchange(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	t.Setenv("CIO_TOKEN", "")

	server := oauthServer(t, "sa_live_env123")
	defer server.Close()
	t.Setenv("CIO_API_URL", server.URL)

	stdout, _, err := executeCommand("auth", "login", "sa_live_env123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("invalid JSON output: %v\nstdout: %s", err, stdout)
	}
	if result["status"] != "ok" {
		t.Errorf("expected status ok, got %v", result["status"])
	}
	if result["base_url"] != server.URL {
		t.Errorf("expected base_url %q, got %v", server.URL, result["base_url"])
	}
}

func TestAuthLogin_EmptyToken(t *testing.T) {
	_, _, err := executeCommand("auth", "login", "")
	if err == nil {
		t.Fatal("expected error for empty token")
	}
}

func TestAuthLogin_BadPrefix(t *testing.T) {
	_, _, err := executeCommand("auth", "login", "not_a_service_account_token")
	if err == nil {
		t.Fatal("expected error for non-sa_live_ token")
	}
}

func TestAuthLogin_InvalidToken(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	t.Setenv("CIO_TOKEN", "")

	server := oauthServer(t, "sa_live_valid") // Server only accepts "sa_live_valid"
	defer server.Close()

	_, _, err := executeCommand("auth", "login", "sa_live_wrong", "--api-url", server.URL)
	if err == nil {
		t.Fatal("expected error for invalid token exchange")
	}

	// A failed exchange must not persist the rejected token — otherwise
	// every subsequent command would reuse a credential the server
	// already refused.
	path := filepath.Join(tmpDir, ".cio", "config.json")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("config file should not exist after failed exchange, got err=%v", err)
	}
}

func TestAuthLogin_InvalidTokenPreservesExistingCredentials(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	t.Setenv("CIO_TOKEN", "")

	// Seed a valid prior login.
	if err := client.WriteCredentials(&client.Credentials{
		ServiceAccountToken: "sa_live_previous",
		AccountID:           "42",
		Region:              "us",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	server := oauthServer(t, "sa_live_valid")
	defer server.Close()

	_, _, err := executeCommand("auth", "login", "sa_live_wrong", "--api-url", server.URL)
	if err == nil {
		t.Fatal("expected error for invalid token exchange")
	}

	// Previous working credentials must remain intact.
	data, err := os.ReadFile(filepath.Join(tmpDir, ".cio", "config.json"))
	if err != nil {
		t.Fatalf("prior config file should survive failed login: %v", err)
	}
	var creds map[string]any
	if err := json.Unmarshal(data, &creds); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if creds["service_account_token"] != "sa_live_previous" {
		t.Errorf("prior token clobbered by failed login, got %v", creds["service_account_token"])
	}
}

func TestAuthLogin_DiscoverEURegion(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	t.Setenv("CIO_TOKEN", "")

	server := oauthServerWithRegion(t, "sa_live_eu123", "eu")
	defer server.Close()

	stdout, _, err := executeCommand("auth", "login", "sa_live_eu123", "--api-url", server.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if result["region"] != "eu" {
		t.Errorf("expected auto-discovered region 'eu', got %v", result["region"])
	}
	if result["data_center"] != "eu" {
		t.Errorf("expected data_center 'eu', got %v", result["data_center"])
	}

	// Verify config file has the discovered region.
	data, err := os.ReadFile(filepath.Join(tmpDir, ".cio", "config.json"))
	if err != nil {
		t.Fatalf("failed to read config: %v", err)
	}
	var creds map[string]any
	if err := json.Unmarshal(data, &creds); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if creds["region"] != "eu" {
		t.Errorf("expected region 'eu' in config, got %v", creds["region"])
	}
}

func TestResolveCLILoginURL(t *testing.T) {
	t.Setenv("CIO_UI_URL", "")
	t.Setenv("CIO_REGION", "")
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	cases := []struct {
		name     string
		uiURLEnv string
		region   string
		want     string
	}{
		{
			name: "default when nothing set is US fly-login",
			want: "https://fly.customer.io/cli",
		},
		{
			name:   "CIO_REGION=eu still uses the shared frontend host",
			region: "eu",
			want:   "https://fly.customer.io/cli",
		},
		{
			name:     "CIO_UI_URL overrides everything",
			uiURLEnv: "http://fly.test:4200/",
			want:     "http://fly.test:4200/cli",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CIO_REGION", tc.region)
			t.Setenv("CIO_UI_URL", tc.uiURLEnv)

			got := resolveCLILoginURL()
			if got != tc.want {
				t.Errorf("resolveCLILoginURL() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestReadInteractiveTokenWithTTY_HidesTerminalInput(t *testing.T) {
	origIsTerminal := isTerminalInput
	origReadPassword := readPasswordInput
	t.Cleanup(func() {
		isTerminalInput = origIsTerminal
		readPasswordInput = origReadPassword
	})

	isTerminalInput = func(fd uintptr) bool {
		return true
	}
	readPasswordInput = func(fd uintptr) ([]byte, error) {
		if fd != 123 {
			t.Fatalf("unexpected fd: %d", fd)
		}
		return []byte("sa_live_secret123\n"), nil
	}

	var stderr bytes.Buffer
	token, err := readInteractiveTokenWithTTY(strings.NewReader("ignored"), &stderr, 123, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if token != "sa_live_secret123" {
		t.Fatalf("token = %q, want %q", token, "sa_live_secret123")
	}
	if stderr.String() != "Paste token: \n" {
		t.Fatalf("stderr = %q, want %q", stderr.String(), "Paste token: \n")
	}
}

func TestReadInteractiveTokenWithTTY_FallsBackForNonTerminalInput(t *testing.T) {
	origIsTerminal := isTerminalInput
	t.Cleanup(func() {
		isTerminalInput = origIsTerminal
	})

	isTerminalInput = func(fd uintptr) bool {
		t.Fatalf("unexpected terminal check for non-terminal path")
		return false
	}

	var stderr bytes.Buffer
	token, err := readInteractiveTokenWithTTY(strings.NewReader("sa_live_scanned\n"), &stderr, 0, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if token != "sa_live_scanned" {
		t.Fatalf("token = %q, want %q", token, "sa_live_scanned")
	}
	if stderr.String() != "Paste token: " {
		t.Fatalf("stderr = %q, want %q", stderr.String(), "Paste token: ")
	}
}

func TestAuthLogin_HelpMentionsBrowserFlow(t *testing.T) {
	// Inspect `Long` directly instead of calling `--help`: cobra's help path
	// mutates shared command state on the global rootCmd, which leaks into
	// the next test in the package.
	help := authLoginCmd.Long
	if !strings.Contains(help, "browser") && !strings.Contains(help, "URL") {
		t.Errorf("expected login help to describe the browser/URL flow, got:\n%s", help)
	}
}

func TestAuthLogout_RemovesCredentials(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	t.Setenv("CIO_TOKEN", "")

	server := oauthServer(t, "sa_live_logout")
	defer server.Close()

	// First login.
	_, _, err := executeCommand("auth", "login", "sa_live_logout", "--api-url", server.URL)
	if err != nil {
		t.Fatalf("login failed: %v", err)
	}

	// Verify file exists.
	configPath := filepath.Join(tmpDir, ".cio", "config.json")
	if _, err := os.Stat(configPath); err != nil {
		t.Fatalf("config file should exist after login: %v", err)
	}

	// Logout.
	stdout, _, err := executeCommand("auth", "logout")
	if err != nil {
		t.Fatalf("logout failed: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if result["status"] != "ok" {
		t.Errorf("expected status ok, got %v", result["status"])
	}

	// Verify file is gone.
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Error("config file should be removed after logout")
	}
}

func TestAuthStatus_NotAuthenticated(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	t.Setenv("CIO_TOKEN", "")

	_, _, err := executeCommand("auth", "status")
	if err == nil {
		t.Fatal("expected error when not authenticated")
	}
}

func TestAuthStatus_WithEnvToken(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	t.Setenv("CIO_TOKEN", "sa_live_envtoken")

	server := oauthServer(t, "sa_live_envtoken")
	defer server.Close()

	stdout, _, err := executeCommand("auth", "status", "--api-url", server.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("invalid JSON: %v\nstdout: %s", err, stdout)
	}
	if result["token_source"] != "environment" {
		t.Errorf("expected token_source 'environment', got %v", result["token_source"])
	}
	if result["verified"] != true {
		t.Errorf("expected verified=true, got %v (error: %v)", result["verified"], result["verify_error"])
	}
}

func TestAuthStatus_InvalidToken(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	t.Setenv("CIO_TOKEN", "sa_live_bad")
	t.Setenv("CIO_ACCESS_TOKEN", "")

	server := oauthServer(t, "sa_live_good") // Server rejects "sa_live_bad"
	defer server.Close()

	stdout, _, err := executeCommand("auth", "status", "--api-url", server.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if result["verified"] != false {
		t.Errorf("expected verified=false, got %v", result["verified"])
	}
}

func TestAuthToken_PrintsToken(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	t.Setenv("CIO_TOKEN", "sa_live_secret123")

	stdout, _, err := executeCommand("auth", "token")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := stdout; got != "sa_live_secret123\n" {
		t.Errorf("expected raw token output, got %q", got)
	}
}

func TestAuthToken_NoToken(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	t.Setenv("CIO_TOKEN", "")

	_, _, err := executeCommand("auth", "token")
	if err == nil {
		t.Fatal("expected error when no token available")
	}
}

// signupServer returns a test server for the agentic signup endpoints.
func signupServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Errorf("signup endpoint received Authorization header: %q", r.Header.Get("Authorization"))
		}
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		body, _ := io.ReadAll(r.Body)

		switch r.URL.Path {
		case "/v1/account_signup":
			if !bytes.Contains(body, []byte(`"email"`)) {
				w.WriteHeader(http.StatusUnprocessableEntity)
				_, _ = w.Write([]byte(`{"errors":{"email":["is required"]}}`))
				return
			}
			_, _ = w.Write([]byte(`{"message":"check your email"}`))
		case "/v1/account_signup/code":
			_, _ = w.Write([]byte(`{"account_id":1,"environment_id":2,"user_id":3,"service_account_id":4,"token_id":5,"token":"sa_live_bootstrap","token_hint":"trap","expires_at":0}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func TestAuthSignupStart_Success(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	t.Setenv("CIO_TOKEN", "")

	server := signupServer(t)
	defer server.Close()

	stdout, _, err := executeCommand("auth", "signup", "start",
		"--api-url", server.URL,
		"--json", `{"email":"agent+demo@example.com"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("invalid JSON: %v\nstdout: %s", err, stdout)
	}
	if result["message"] != "check your email" {
		t.Errorf("unexpected response: %v", result)
	}
}

func TestAuthSignupStart_MissingJSON(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	t.Setenv("CIO_TOKEN", "")

	_, _, err := executeCommand("auth", "signup", "start", "--api-url", "https://example.invalid")
	if err == nil {
		t.Fatal("expected error when --json missing")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("--json is required")) {
		t.Errorf("expected --json validation error, got %v", err)
	}
}

func TestAuthSignupStart_IgnoresCredentials(t *testing.T) {
	// Even if CIO_TOKEN is set, the signup endpoint must be called anonymously
	// (no Authorization header). The test server fails the test otherwise.
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	t.Setenv("CIO_TOKEN", "sa_live_should_not_be_used")

	server := signupServer(t)
	defer server.Close()

	_, _, err := executeCommand("auth", "signup", "start",
		"--api-url", server.URL,
		"--json", `{"email":"agent+demo@example.com"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAuthSignupVerify_ReturnsBootstrapToken(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	t.Setenv("CIO_TOKEN", "")

	server := signupServer(t)
	defer server.Close()

	stdout, _, err := executeCommand("auth", "signup", "verify",
		"--api-url", server.URL,
		"--json", `{"email":"agent+demo@example.com","code":"123456","company_name":"Acme","first_name":"Ada","last_name":"Lovelace","data_center":"eu"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("invalid JSON: %v\nstdout: %s", err, stdout)
	}
	if result["token"] != "sa_live_bootstrap" {
		t.Errorf("expected bootstrap token, got %v", result["token"])
	}

	// verify should persist the bootstrap token + account_id to ~/.cio/config.json.
	data, err := os.ReadFile(filepath.Join(tmpDir, ".cio", "config.json"))
	if err != nil {
		t.Fatalf("expected config written, got: %v", err)
	}
	var creds map[string]any
	if err := json.Unmarshal(data, &creds); err != nil {
		t.Fatalf("invalid config JSON: %v", err)
	}
	if creds["service_account_token"] != "sa_live_bootstrap" {
		t.Errorf("expected service_account_token saved, got %v", creds["service_account_token"])
	}
	if creds["account_id"] != "1" {
		t.Errorf("expected account_id=1, got %v", creds["account_id"])
	}
	// data_center=eu in the request body, and server URL has no eu/us hint —
	// so region should fall back to the request body's data_center.
	if creds["region"] != "eu" {
		t.Errorf("expected region=eu (from request body data_center), got %v", creds["region"])
	}
}

func TestAuthSignupStart_DryRun(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	t.Setenv("CIO_TOKEN", "")

	stdout, _, err := executeCommand("auth", "signup", "start",
		"--api-url", "https://example.invalid",
		"--json", `{"email":"agent+demo@example.com"}`,
		"--dry-run")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("invalid JSON: %v\nstdout: %s", err, stdout)
	}
	if result["dry_run"] != true {
		t.Errorf("expected dry_run=true, got %v", result)
	}
	if result["url"] != "https://example.invalid/v1/account_signup" {
		t.Errorf("unexpected url: %v", result["url"])
	}
}
