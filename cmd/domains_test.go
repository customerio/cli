package cmd

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// domainServer creates a test server that handles OAuth + domain API routes.
// It echoes back the method, path, query, and body for verification.
// GET /v1/environments/456/domains returns a canned domain list for name resolution.
func domainServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// OAuth token exchange
		if r.URL.Path == "/v1/service_accounts/oauth/token" {
			if err := r.ParseForm(); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if r.PostFormValue("client_secret") != "sa_live_test123" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"access_token":"jwt-test-session","token_type":"Bearer","expires_in":3600}`))
			return
		}

		// Require JWT
		if r.Header.Get("Authorization") != "Bearer jwt-test-session" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
			return
		}

		// Domain list endpoint — returns canned data for domain name resolution.
		if r.Method == "GET" && r.URL.Path == "/v1/environments/456/domains" {
			_, _ = w.Write([]byte(`{"domains":[{"id":"789","domain":"example.com"},{"id":"101","domain":"other.com"}]}`))
			return
		}

		// Entri token endpoint.
		if r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/entri_token") {
			_, _ = w.Write([]byte(`{"token":"test-jwt-token","application_id":"test-app-id","user_id":"1:456:789:domain_auth:example.com"}`))
			return
		}

		// DNS check endpoint — returns canned verification results.
		if r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/check_dns") {
			_, _ = w.Write([]byte(`{"domain_auth":{"records":[{"name":"MX","passing":true,"expected":"10 mxa.mailgun.org","actual":"10 mxa.mailgun.org"},{"name":"SPF","passing":true,"expected":"v=spf1 include:mailgun.org ~all","actual":"v=spf1 include:mailgun.org ~all"},{"name":"DKIM","passing":true,"expected":"k=rsa; p=MIIBIjAN...","actual":"k=rsa; p=MIIBIjAN..."},{"name":"DMARC","passing":false,"expected":"v=DMARC1; p=none","errors":["DMARC record not found"]}]}}`))
			return
		}

		// Handle DELETE → 204
		if r.Method == "DELETE" {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		// Read body if present
		var bodyStr string
		if r.Body != nil {
			data, _ := io.ReadAll(r.Body)
			bodyStr = string(data)
		}

		response := map[string]any{
			"method": r.Method,
			"path":   r.URL.Path,
			"query":  r.URL.Query(),
		}
		if bodyStr != "" {
			response["body"] = json.RawMessage(bodyStr)
		}
		data, _ := json.Marshal(response)
		_, _ = w.Write(data)
	}))
}

func setupDomainTest(t *testing.T) (*httptest.Server, func()) {
	t.Helper()
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	t.Setenv("CIO_TOKEN", "sa_live_test123")
	t.Setenv("CIO_ACCESS_TOKEN", "")
	t.Setenv("CIO_UI_URL", "")
	resetDomainFlags()
	server := domainServer(t)
	return server, func() { server.Close() }
}

// resetDomainFlags clears local flags that persist across cobra test runs.
func resetDomainFlags() {
	for _, cmd := range []*cobra.Command{
		domainsListCmd,
		domainsAddCmd,
		domainsGetCmd,
		domainsDeleteCmd,
		domainsConfigureCmd,
		domainsVerifyCmd,
		domainsFromAddressesListCmd,
		domainsFromAddressesAddCmd,
		domainsFromAddressesUpdateCmd,
		domainsLinkTrackingConfigureCmd,
		domainsLinkTrackingVerifyCmd,
		domainsFromAddressesDeleteCmd,
	} {
		cmd.Flags().Visit(func(f *pflag.Flag) {
			_ = f.Value.Set(f.DefValue)
			f.Changed = false
		})
	}
}

func TestDomains_List(t *testing.T) {
	server, cleanup := setupDomainTest(t)
	defer cleanup()

	stdout, _, err := executeCommand("domains", "list",
		"--env-id", "456",
		"--api-url", server.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The list endpoint returns canned domain data
	var result map[string]any
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("invalid JSON: %v\nstdout: %s", err, stdout)
	}
	domains, ok := result["domains"].([]any)
	if !ok {
		t.Fatalf("expected domains array, got %T", result["domains"])
	}
	if len(domains) != 2 {
		t.Errorf("expected 2 domains, got %d", len(domains))
	}
}

func TestDomains_List_DryRun(t *testing.T) {
	server, cleanup := setupDomainTest(t)
	defer cleanup()

	stdout, _, err := executeCommand("domains", "list",
		"--env-id", "456",
		"--api-url", server.URL,
		"--dry-run")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("invalid JSON: %v\nstdout: %s", err, stdout)
	}
	if result["dry_run"] != true {
		t.Errorf("expected dry_run=true, got %v", result["dry_run"])
	}
	if result["method"] != "GET" {
		t.Errorf("expected GET, got %v", result["method"])
	}
}

func TestDomains_List_MissingEnvID(t *testing.T) {
	server, cleanup := setupDomainTest(t)
	defer cleanup()

	_, _, err := executeCommand("domains", "list",
		"--api-url", server.URL)
	if err == nil {
		t.Fatal("expected error for missing --env-id")
	}
	if !strings.Contains(err.Error(), "--env-id is required") {
		t.Errorf("expected --env-id is required error, got: %v", err)
	}
}

func TestDomains_List_InvalidEnvID(t *testing.T) {
	server, cleanup := setupDomainTest(t)
	defer cleanup()

	_, _, err := executeCommand("domains", "list",
		"--env-id", "abc",
		"--api-url", server.URL)
	if err == nil {
		t.Fatal("expected error for non-numeric --env-id")
	}
	if !strings.Contains(err.Error(), "must be numeric") {
		t.Errorf("expected numeric validation error, got: %v", err)
	}
}

func TestDomains_Add(t *testing.T) {
	server, cleanup := setupDomainTest(t)
	defer cleanup()

	stdout, _, err := executeCommand("domains", "add",
		"--env-id", "456",
		"example.com",
		"--api-url", server.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("invalid JSON: %v\nstdout: %s", err, stdout)
	}
	if result["method"] != "POST" {
		t.Errorf("expected POST, got %v", result["method"])
	}
	if result["path"] != "/v1/environments/456/domains" {
		t.Errorf("expected /v1/environments/456/domains, got %v", result["path"])
	}
	// Verify the body contains the domain
	body, ok := result["body"].(map[string]any)
	if !ok {
		t.Fatalf("expected body map, got %T", result["body"])
	}
	domainObj, _ := body["domain"].(map[string]any)
	if domainObj["domain"] != "example.com" {
		t.Errorf("expected domain=example.com in body, got %v", domainObj["domain"])
	}
}

func TestDomains_Add_WithAutoTLS(t *testing.T) {
	server, cleanup := setupDomainTest(t)
	defer cleanup()

	stdout, _, err := executeCommand("domains", "add",
		"--env-id", "456",
		"example.com",
		"--auto-tls",
		"--api-url", server.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("invalid JSON: %v\nstdout: %s", err, stdout)
	}
	body, ok := result["body"].(map[string]any)
	if !ok {
		t.Fatalf("expected body map, got %T", result["body"])
	}
	if body["auto_tls"] != true {
		t.Errorf("expected auto_tls=true in body, got %v", body["auto_tls"])
	}
}

func TestDomains_Delete_ByName(t *testing.T) {
	server, cleanup := setupDomainTest(t)
	defer cleanup()

	stdout, _, err := executeCommand("domains", "delete",
		"--env-id", "456",
		"example.com",
		"--api-url", server.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("invalid JSON: %v\nstdout: %s", err, stdout)
	}
	if result["deleted"] != true {
		t.Errorf("expected deleted=true, got %v", result["deleted"])
	}
	// Domain name "example.com" should resolve to ID "789"
	if result["domain_id"] != "789" {
		t.Errorf("expected domain_id=789, got %v", result["domain_id"])
	}
}

func TestDomains_Delete_NotFound(t *testing.T) {
	server, cleanup := setupDomainTest(t)
	defer cleanup()

	_, _, err := executeCommand("domains", "delete",
		"--env-id", "456",
		"nonexistent.com",
		"--api-url", server.URL)
	if err == nil {
		t.Fatal("expected error for unknown domain name")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected not found error, got: %v", err)
	}
}

func TestDomains_AuthenticateConfigure_Automatic(t *testing.T) {
	server, cleanup := setupDomainTest(t)
	defer cleanup()

	stdout, stderr, err := executeCommand("domains", "configure",
		"--env-id", "456",
		"example.com",
		"--api-url", server.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(stderr, "/cli/dns-setup#") {
		t.Errorf("expected /cli/dns-setup# in stderr URL, got: %s", stderr)
	}
	if !strings.Contains(stderr, "example.com") {
		t.Errorf("expected domain name in stderr, got: %s", stderr)
	}

	// Verify structured JSON on stdout.
	var result map[string]any
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("invalid JSON on stdout: %v\nstdout: %s", err, stdout)
	}
	if result["domain"] != "example.com" {
		t.Errorf("expected domain=example.com, got %v", result["domain"])
	}
	if result["domain_id"] != "789" {
		t.Errorf("expected domain_id=789, got %v", result["domain_id"])
	}
	url, _ := result["url"].(string)
	if !strings.Contains(url, "/cli/dns-setup#") {
		t.Errorf("expected /cli/dns-setup# in stdout URL, got %v", url)
	}
}

func TestDomains_AuthenticateConfigure_CustomUIURL(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CIO_TOKEN", "sa_live_test123")
	t.Setenv("CIO_ACCESS_TOKEN", "")
	t.Setenv("CIO_UI_URL", "http://localhost:3000")
	resetDomainFlags()

	server := domainServer(t)
	defer server.Close()

	_, stderr, err := executeCommand("domains", "configure",
		"--env-id", "456",
		"example.com",
		"--api-url", server.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(stderr, "http://localhost:3000/cli/dns-setup#") {
		t.Errorf("expected CIO_UI_URL override in stderr URL, got: %s", stderr)
	}
}

func TestDomains_Verify_WithFailures(t *testing.T) {
	server, cleanup := setupDomainTest(t)
	defer cleanup()

	stdout, stderr, err := executeCommand("domains", "verify",
		"--env-id", "456",
		"example.com",
		"--api-url", server.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("invalid JSON: %v\nstdout: %s", err, stdout)
	}
	// DMARC is failing in our canned response, so verified should be false
	if result["verified"] != false {
		t.Errorf("expected verified=false, got %v", result["verified"])
	}
	if result["domain"] != "example.com" {
		t.Errorf("expected domain=example.com, got %v", result["domain"])
	}
	// Should have failing_records with DMARC
	failing, ok := result["failing_records"].([]any)
	if !ok || len(failing) == 0 {
		t.Fatalf("expected failing_records array, got %v", result["failing_records"])
	}
	firstFailing, _ := failing[0].(map[string]any)
	if firstFailing["record"] != "DMARC" {
		t.Errorf("expected DMARC in failing records, got %v", firstFailing["record"])
	}
	// Human-readable summary should be on stderr
	if !strings.Contains(stderr, "FAILING") {
		t.Error("expected human-readable failure summary on stderr")
	}
	if !strings.Contains(stderr, "re-run") {
		t.Error("expected instructions hint on stderr")
	}
}

func TestDomains_FromAddresses_List(t *testing.T) {
	server, cleanup := setupDomainTest(t)
	defer cleanup()

	stdout, _, err := executeCommand("domains", "from_addresses", "list",
		"--env-id", "456",
		"--api-url", server.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("invalid JSON: %v\nstdout: %s", err, stdout)
	}
	if result["method"] != "GET" {
		t.Errorf("expected GET, got %v", result["method"])
	}
	if result["path"] != "/v1/environments/456/identities" {
		t.Errorf("expected identities path, got %v", result["path"])
	}
	query, ok := result["query"].(map[string]any)
	if !ok {
		t.Fatalf("expected query map, got %T", result["query"])
	}
	typeValues, _ := query["type"].([]any)
	if len(typeValues) == 0 || typeValues[0] != "email" {
		t.Errorf("expected type=email, got %v", query["type"])
	}
}

func TestDomains_FromAddresses_List_InUse(t *testing.T) {
	server, cleanup := setupDomainTest(t)
	defer cleanup()

	stdout, _, err := executeCommand("domains", "from_addresses", "list",
		"--env-id", "456",
		"--in-use",
		"--api-url", server.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("invalid JSON: %v\nstdout: %s", err, stdout)
	}
	query, ok := result["query"].(map[string]any)
	if !ok {
		t.Fatalf("expected query map, got %T", result["query"])
	}
	inUseValues, _ := query["in_use"].([]any)
	if len(inUseValues) == 0 || inUseValues[0] != "true" {
		t.Errorf("expected in_use=true, got %v", query["in_use"])
	}
}

func TestDomains_FromAddresses_Add(t *testing.T) {
	server, cleanup := setupDomainTest(t)
	defer cleanup()

	stdout, _, err := executeCommand("domains", "from_addresses", "add",
		"--env-id", "456",
		"--name", "Support",
		"--email", "support@example.com",
		"--api-url", server.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("invalid JSON: %v\nstdout: %s", err, stdout)
	}
	if result["method"] != "POST" {
		t.Errorf("expected POST, got %v", result["method"])
	}
	if result["path"] != "/v1/environments/456/identities" {
		t.Errorf("expected identities path, got %v", result["path"])
	}
	body, ok := result["body"].(map[string]any)
	if !ok {
		t.Fatalf("expected body map, got %T", result["body"])
	}
	identity, _ := body["identity"].(map[string]any)
	if identity["name"] != "Support" {
		t.Errorf("expected name=Support, got %v", identity["name"])
	}
	if identity["email"] != "support@example.com" {
		t.Errorf("expected email=support@example.com, got %v", identity["email"])
	}
	if identity["template_type"] != "email" {
		t.Errorf("expected template_type=email, got %v", identity["template_type"])
	}
}

func TestDomains_FromAddresses_Add_MissingName(t *testing.T) {
	server, cleanup := setupDomainTest(t)
	defer cleanup()

	_, _, err := executeCommand("domains", "from_addresses", "add",
		"--env-id", "456",
		"--email", "support@example.com",
		"--api-url", server.URL)
	if err == nil {
		t.Fatal("expected error for missing --name")
	}
	if !strings.Contains(err.Error(), "--name is required") {
		t.Errorf("expected --name is required error, got: %v", err)
	}
}

func TestDomains_FromAddresses_Update(t *testing.T) {
	server, cleanup := setupDomainTest(t)
	defer cleanup()

	stdout, _, err := executeCommand("domains", "from_addresses", "update",
		"--env-id", "456",
		"123",
		"--name", "New Name",
		"--api-url", server.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("invalid JSON: %v\nstdout: %s", err, stdout)
	}
	if result["method"] != "PUT" {
		t.Errorf("expected PUT, got %v", result["method"])
	}
	if result["path"] != "/v1/environments/456/identities/123" {
		t.Errorf("expected identities/123 path, got %v", result["path"])
	}
	body, ok := result["body"].(map[string]any)
	if !ok {
		t.Fatalf("expected body map, got %T", result["body"])
	}
	identity, _ := body["identity"].(map[string]any)
	if identity["name"] != "New Name" {
		t.Errorf("expected name=New Name, got %v", identity["name"])
	}
	if _, hasEmail := identity["email"]; hasEmail {
		t.Error("expected email to be omitted when not provided")
	}
}

func TestDomains_FromAddresses_Update_MissingFields(t *testing.T) {
	server, cleanup := setupDomainTest(t)
	defer cleanup()

	_, _, err := executeCommand("domains", "from_addresses", "update",
		"--env-id", "456",
		"123",
		"--api-url", server.URL)
	if err == nil {
		t.Fatal("expected error when neither --name nor --email provided")
	}
	if !strings.Contains(err.Error(), "at least one of --name or --email") {
		t.Errorf("expected validation error, got: %v", err)
	}
}

func TestDomains_FromAddresses_Delete(t *testing.T) {
	server, cleanup := setupDomainTest(t)
	defer cleanup()

	stdout, _, err := executeCommand("domains", "from_addresses", "delete",
		"--env-id", "456",
		"123",
		"--api-url", server.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("invalid JSON: %v\nstdout: %s", err, stdout)
	}
	if result["deleted"] != true {
		t.Errorf("expected deleted=true, got %v", result["deleted"])
	}
	if result["identity_id"] != "123" {
		t.Errorf("expected identity_id=123, got %v", result["identity_id"])
	}
}

// --- Dry-run tests for mutating commands ---

func TestDomains_Add_DryRun(t *testing.T) {
	server, cleanup := setupDomainTest(t)
	defer cleanup()

	stdout, _, err := executeCommand("domains", "add",
		"--env-id", "456",
		"example.com",
		"--api-url", server.URL,
		"--dry-run")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if result["dry_run"] != true {
		t.Errorf("expected dry_run=true")
	}
	if result["method"] != "POST" {
		t.Errorf("expected POST, got %v", result["method"])
	}
}

func TestDomains_Delete_DryRun(t *testing.T) {
	server, cleanup := setupDomainTest(t)
	defer cleanup()

	stdout, _, err := executeCommand("domains", "delete",
		"--env-id", "456",
		"example.com",
		"--api-url", server.URL,
		"--dry-run")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if result["dry_run"] != true {
		t.Errorf("expected dry_run=true")
	}
	if result["method"] != "DELETE" {
		t.Errorf("expected DELETE, got %v", result["method"])
	}
}

func TestDomains_FromAddresses_Add_DryRun(t *testing.T) {
	server, cleanup := setupDomainTest(t)
	defer cleanup()

	stdout, _, err := executeCommand("domains", "from_addresses", "add",
		"--env-id", "456",
		"--name", "Support",
		"--email", "support@example.com",
		"--api-url", server.URL,
		"--dry-run")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if result["dry_run"] != true {
		t.Errorf("expected dry_run=true")
	}
	if result["method"] != "POST" {
		t.Errorf("expected POST, got %v", result["method"])
	}
}

func TestDomains_FromAddresses_Update_DryRun(t *testing.T) {
	server, cleanup := setupDomainTest(t)
	defer cleanup()

	stdout, _, err := executeCommand("domains", "from_addresses", "update",
		"--env-id", "456",
		"123",
		"--name", "New Name",
		"--api-url", server.URL,
		"--dry-run")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if result["dry_run"] != true {
		t.Errorf("expected dry_run=true")
	}
	if result["method"] != "PUT" {
		t.Errorf("expected PUT, got %v", result["method"])
	}
}

func TestDomains_FromAddresses_Delete_DryRun(t *testing.T) {
	server, cleanup := setupDomainTest(t)
	defer cleanup()

	stdout, _, err := executeCommand("domains", "from_addresses", "delete",
		"--env-id", "456",
		"123",
		"--api-url", server.URL,
		"--dry-run")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if result["dry_run"] != true {
		t.Errorf("expected dry_run=true")
	}
	if result["method"] != "DELETE" {
		t.Errorf("expected DELETE, got %v", result["method"])
	}
}

func TestDomains_LinkTracking_Configure_DryRun(t *testing.T) {
	server, cleanup := setupDomainTest(t)
	defer cleanup()

	stdout, _, err := executeCommand("domains", "link_tracking", "configure",
		"--env-id", "456",
		"example.com",
		"--subdomain", "email",
		"--api-url", server.URL,
		"--dry-run")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if result["dry_run"] != true {
		t.Errorf("expected dry_run=true")
	}
	if result["method"] != "PUT" {
		t.Errorf("expected PUT, got %v", result["method"])
	}
}

// --- Get test ---

func TestDomains_Get(t *testing.T) {
	server, cleanup := setupDomainTest(t)
	defer cleanup()

	stdout, _, err := executeCommand("domains", "get",
		"--env-id", "456",
		"example.com",
		"--api-url", server.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("invalid JSON: %v\nstdout: %s", err, stdout)
	}
	if result["method"] != "GET" {
		t.Errorf("expected GET, got %v", result["method"])
	}
	// Domain name "example.com" resolves to ID "789"
	if result["path"] != "/v1/environments/456/domains/789" {
		t.Errorf("expected /v1/environments/456/domains/789, got %v", result["path"])
	}
}

// --- Link tracking tests ---

func TestDomains_LinkTracking_Configure(t *testing.T) {
	server, cleanup := setupDomainTest(t)
	defer cleanup()

	stdout, _, err := executeCommand("domains", "link_tracking", "configure",
		"--env-id", "456",
		"example.com",
		"--subdomain", "email",
		"--api-url", server.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("invalid JSON: %v\nstdout: %s", err, stdout)
	}
	if result["method"] != "PUT" {
		t.Errorf("expected PUT, got %v", result["method"])
	}
	if result["path"] != "/v1/environments/456/domains/789" {
		t.Errorf("expected domains/789 path, got %v", result["path"])
	}
	// Bare subdomain "email" + domain "example.com" → "email.example.com"
	body, ok := result["body"].(map[string]any)
	if !ok {
		t.Fatalf("expected body map, got %T", result["body"])
	}
	if body["cname"] != "email.example.com" {
		t.Errorf("expected cname=email.example.com, got %v", body["cname"])
	}
}

func TestDomains_LinkTracking_Configure_ByID(t *testing.T) {
	server, cleanup := setupDomainTest(t)
	defer cleanup()

	// When passing a numeric ID, the domain name should be resolved from the API
	stdout, _, err := executeCommand("domains", "link_tracking", "configure",
		"--env-id", "456",
		"789",
		"--subdomain", "email",
		"--api-url", server.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("invalid JSON: %v\nstdout: %s", err, stdout)
	}
	body, ok := result["body"].(map[string]any)
	if !ok {
		t.Fatalf("expected body map, got %T", result["body"])
	}
	// Should be "email.example.com" (resolved domain name), NOT "email.789"
	if body["cname"] != "email.example.com" {
		t.Errorf("expected cname=email.example.com, got %v", body["cname"])
	}
}

func TestDomains_LinkTracking_Configure_FullSubdomain(t *testing.T) {
	server, cleanup := setupDomainTest(t)
	defer cleanup()

	stdout, _, err := executeCommand("domains", "link_tracking", "configure",
		"--env-id", "456",
		"example.com",
		"--subdomain", "email.example.com",
		"--api-url", server.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("invalid JSON: %v\nstdout: %s", err, stdout)
	}
	body, _ := result["body"].(map[string]any)
	// Full subdomain passed through as-is
	if body["cname"] != "email.example.com" {
		t.Errorf("expected cname=email.example.com, got %v", body["cname"])
	}
}

func TestDomains_LinkTracking_Verify(t *testing.T) {
	server, cleanup := setupDomainTest(t)
	defer cleanup()

	stdout, stderr, err := executeCommand("domains", "link_tracking", "verify",
		"--env-id", "456",
		"example.com",
		"--api-url", server.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// check_dns returns canned response with DMARC failing (domain_auth flow),
	// but link_tracking flow has no records in canned response, so it should pass
	var result map[string]any
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("invalid JSON: %v\nstdout: %s", err, stdout)
	}
	if result["domain"] != "example.com" {
		t.Errorf("expected domain=example.com, got %v", result["domain"])
	}
	// Human summary should be on stderr
	if stderr == "" {
		t.Error("expected human-readable summary on stderr")
	}
}
