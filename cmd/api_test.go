package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/customerio/cli/internal/client"
)

// apiServer creates a test server that handles OAuth + any API route.
func apiServer(t *testing.T) *httptest.Server {
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

		// Echo back what was received for verification
		response := map[string]any{
			"method":      r.Method,
			"path":        r.URL.Path,
			"query":       r.URL.Query(),
			"request_uri": r.RequestURI,
		}
		data, _ := json.Marshal(response)
		_, _ = w.Write(data)
	}))
}

func setupAPITest(t *testing.T) (*httptest.Server, func()) {
	t.Helper()
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	t.Setenv("CIO_TOKEN", "sa_live_test123")
	t.Setenv("CIO_ACCESS_TOKEN", "")
	server := apiServer(t)
	return server, func() { server.Close() }
}

// schemaSpec is a minimal Journeys OpenAPI fixture for schema tests. It must
// include a campaigns resource so the schema assertions (campaigns.list -> GET)
// resolve without reaching the live API.
func schemaSpec() string {
	return `{
		"openapi": "3.1.0",
		"info": {"title": "Test", "version": "1.0.0"},
		"paths": {
			"/v1/environments/{environment_id}/campaigns": {
				"get": {
					"summary": "List campaigns",
					"parameters": [
						{"name": "environment_id", "in": "path", "required": true, "schema": {"type": "string"}}
					]
				}
			}
		}
	}`
}

// schemaCDPSpec is the CDP companion fixture; LoadRegistry loads both the
// Journeys and CDP specs, so both endpoints must respond.
func schemaCDPSpec() string {
	return `{
		"openapi": "3.1.0",
		"info": {"title": "CDP", "version": "1.0.0"},
		"paths": {
			"/cdp/api/workspaces/{workspace_id}/sources": {
				"get": {
					"summary": "List sources",
					"parameters": [
						{"name": "workspace_id", "in": "path", "required": true, "schema": {"type": "string"}}
					]
				}
			}
		}
	}`
}

// setupSchemaTest points the route registry at a local spec server with an
// isolated, empty cache (temp HOME) so `cio schema` tests never reach the live
// API. Returns a cleanup func; call it with defer.
func setupSchemaTest(t *testing.T) func() {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CIO_TOKEN", "")
	t.Setenv("CIO_ACCESS_TOKEN", "")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/openapi.json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(schemaSpec()))
		case "/cdp/api/openapi.json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(schemaCDPSpec()))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Setenv("CIO_API_URL", server.URL)
	return server.Close
}

func TestAPI_GetCampaigns(t *testing.T) {
	server, cleanup := setupAPITest(t)
	defer cleanup()

	stdout, _, err := executeCommand("api", "/v1/environments/{environment_id}/campaigns",
		"--api-url", server.URL,
		"--params", `{"environment_id": "456"}`)
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
	if result["path"] != "/v1/environments/456/campaigns" {
		t.Errorf("expected /v1/environments/456/campaigns, got %v", result["path"])
	}
}

func TestAPI_GetCampaignByID(t *testing.T) {
	server, cleanup := setupAPITest(t)
	defer cleanup()

	stdout, _, err := executeCommand("api", "/v1/environments/{environment_id}/campaigns/{campaign_id}",
		"--api-url", server.URL,
		"--params", `{"environment_id": "456", "campaign_id": "789"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if result["path"] != "/v1/environments/456/campaigns/789" {
		t.Errorf("expected resolved path, got %v", result["path"])
	}
}

func TestAPI_DryRun(t *testing.T) {
	server, cleanup := setupAPITest(t)
	defer cleanup()

	stdout, _, err := executeCommand("api", "/v1/environments/{environment_id}/campaigns",
		"--api-url", server.URL,
		"--params", `{"environment_id": "123"}`,
		"--dry-run")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if result["dry_run"] != true {
		t.Error("expected dry_run=true")
	}
	if result["method"] != "GET" {
		t.Errorf("expected GET, got %v", result["method"])
	}
}

func TestAPI_MissingPathParam(t *testing.T) {
	server, cleanup := setupAPITest(t)
	defer cleanup()

	_, _, err := executeCommand("api", "/v1/environments/{environment_id}/campaigns",
		"--api-url", server.URL)
	if err == nil {
		t.Fatal("expected error for missing environment_id")
	}
}

func TestAPI_PathParamsAllowStringSegments(t *testing.T) {
	server, cleanup := setupAPITest(t)
	defer cleanup()

	stdout, _, err := executeCommand("api", "/v1/environments/{environment_id}/test_users/{test_user_id}",
		"--api-url", server.URL,
		"--params", `{"environment_id": "217838", "test_user_id": "profile_abc-123"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("invalid JSON: %v\nstdout: %s", err, stdout)
	}
	if result["path"] != "/v1/environments/217838/test_users/profile_abc-123" {
		t.Errorf("expected resolved string path segment, got %v", result["path"])
	}
}

func TestAPI_CustomerIDAllowsCIOID(t *testing.T) {
	server, cleanup := setupAPITest(t)
	defer cleanup()

	params := `{"environment_id": "217838", "customer_id": "eea50d000102"}`
	wantPath := "/v1/environments/217838/customers/eea50d000102"

	tests := []struct {
		name      string
		args      []string
		wantKey   string
		wantValue string
	}{
		{
			name: "get",
			args: []string{"api", "/v1/environments/{environment_id}/customers/{customer_id}",
				"--api-url", server.URL,
				"--params", params},
			wantKey:   "path",
			wantValue: wantPath,
		},
		{
			name: "put",
			args: []string{"api", "/v1/environments/{environment_id}/customers/{customer_id}",
				"--api-url", server.URL,
				"--params", params,
				"-X", "PUT",
				"--json", `{"customer":{"email":"test@example.com"}}`},
			wantKey:   "path",
			wantValue: wantPath,
		},
		{
			name: "dry-run",
			args: []string{"api", "/v1/environments/{environment_id}/customers/{customer_id}",
				"--api-url", server.URL,
				"--params", params,
				"--dry-run"},
			wantKey:   "url",
			wantValue: server.URL + wantPath,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stdout, _, err := executeCommand(tt.args...)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			var result map[string]any
			if err := json.Unmarshal([]byte(stdout), &result); err != nil {
				t.Fatalf("invalid JSON: %v\nstdout: %s", err, stdout)
			}
			if result[tt.wantKey] != tt.wantValue {
				t.Errorf("expected %s=%s, got %v", tt.wantKey, tt.wantValue, result[tt.wantKey])
			}
		})
	}
}

func TestAPI_CustomerIDAllowsEmailIdentifier(t *testing.T) {
	server, cleanup := setupAPITest(t)
	defer cleanup()

	stdout, _, err := executeCommand("api", "/v1/environments/{environment_id}/customers/{customer_id}",
		"--api-url", server.URL,
		"--params", `{"environment_id": "217838", "customer_id": "user@example.com"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("invalid JSON: %v\nstdout: %s", err, stdout)
	}
	if result["path"] != "/v1/environments/217838/customers/user@example.com" {
		t.Errorf("expected email identifier in path, got %v", result["path"])
	}
	if result["request_uri"] != "/v1/environments/217838/customers/user@example.com" {
		t.Errorf("expected email identifier to remain in request path, got request_uri=%v", result["request_uri"])
	}
}

func TestAPI_PathParamRejectsReservedPathCharacters(t *testing.T) {
	server, cleanup := setupAPITest(t)
	defer cleanup()

	tests := []struct {
		name   string
		params string
	}{
		{
			name:   "slash",
			params: `{"environment_id": "217838", "test_user_id": "eea50d000102/../deliveries"}`,
		},
		{
			name:   "pre-encoded",
			params: `{"environment_id": "217838", "test_user_id": "user%40example.com"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := executeCommand("api", "/v1/environments/{environment_id}/test_users/{test_user_id}",
				"--api-url", server.URL,
				"--params", tt.params)
			if err == nil {
				t.Fatal("expected error for reserved path character")
			}
			if !strings.Contains(err.Error(), "reserved path character") {
				t.Errorf("expected reserved path character error, got: %v", err)
			}
		})
	}
}

func TestAPI_PathParamRejectsDotSegments(t *testing.T) {
	server, cleanup := setupAPITest(t)
	defer cleanup()

	for _, value := range []string{".", ".."} {
		t.Run(value, func(t *testing.T) {
			_, _, err := executeCommand("api", "/v1/environments/{environment_id}/test_users/{test_user_id}",
				"--api-url", server.URL,
				"--params", `{"environment_id": "217838", "test_user_id": "`+value+`"}`)
			if err == nil {
				t.Fatal("expected error for dot path segment")
			}
			if !strings.Contains(err.Error(), "dot path segment") {
				t.Errorf("expected dot path segment error, got: %v", err)
			}
		})
	}
}

func TestAPI_RejectsQueryOrFragmentInPathTemplate(t *testing.T) {
	server, cleanup := setupAPITest(t)
	defer cleanup()

	tests := []string{
		"/v1/environments/{environment_id}/campaigns?include_archived=true",
		"/v1/environments/{environment_id}/campaigns#section",
	}

	for _, path := range tests {
		t.Run(path, func(t *testing.T) {
			_, _, err := executeCommand("api", path,
				"--api-url", server.URL,
				"--params", `{"environment_id": "217838"}`)
			if err == nil {
				t.Fatal("expected error for query or fragment in path")
			}
			if !strings.Contains(err.Error(), "query or fragment") {
				t.Errorf("expected query or fragment error, got: %v", err)
			}
		})
	}
}

func TestAPI_PathTraversal(t *testing.T) {
	server, cleanup := setupAPITest(t)
	defer cleanup()

	_, _, err := executeCommand("api", "/v1/environments/{environment_id}/campaigns",
		"--api-url", server.URL,
		"--params", `{"environment_id": "123/../../etc"}`)
	if err == nil {
		t.Fatal("expected error for path traversal")
	}
}

func TestAPI_RejectsControlCharInQueryValue(t *testing.T) {
	server, cleanup := setupAPITest(t)
	defer cleanup()

	_, _, err := executeCommand("api", "/v1/environments/{environment_id}/campaigns",
		"--api-url", server.URL,
		"--params", `{"environment_id":"456","q":"foo\u0000bar"}`)
	if err == nil {
		t.Fatal("expected error for control character in query value")
	}
	if !strings.Contains(err.Error(), "control character") {
		t.Errorf("expected 'control character' in error, got: %v", err)
	}
}

func TestAPI_RejectsNewlineInQueryValue(t *testing.T) {
	server, cleanup := setupAPITest(t)
	defer cleanup()

	_, _, err := executeCommand("api", "/v1/environments/{environment_id}/campaigns",
		"--api-url", server.URL,
		"--params", `{"environment_id":"456","name":"foo\nbar"}`)
	if err == nil {
		t.Fatal("expected error for newline in query value")
	}
	if !strings.Contains(err.Error(), "control character") {
		t.Errorf("expected 'control character' in error, got: %v", err)
	}
}

func TestAPI_RejectsOverlongQueryValue(t *testing.T) {
	server, cleanup := setupAPITest(t)
	defer cleanup()

	long := strings.Repeat("a", 2000)
	_, _, err := executeCommand("api", "/v1/environments/{environment_id}/campaigns",
		"--api-url", server.URL,
		"--params", `{"environment_id":"456","q":"`+long+`"}`)
	if err == nil {
		t.Fatal("expected error for overlong query value")
	}
	if !strings.Contains(err.Error(), "maximum length") {
		t.Errorf("expected 'maximum length' in error, got: %v", err)
	}
}

func TestAPI_AllowsUnicodeInQueryValue(t *testing.T) {
	server, cleanup := setupAPITest(t)
	defer cleanup()

	stdout, _, err := executeCommand("api", "/v1/environments/{environment_id}/campaigns",
		"--api-url", server.URL,
		"--params", `{"environment_id":"456","name":"café"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	query := result["query"].(map[string]any)
	name := query["name"].([]any)[0].(string)
	if name != "café" {
		t.Errorf("want name=café, got %q", name)
	}
}

func TestAPI_QueryParams(t *testing.T) {
	server, cleanup := setupAPITest(t)
	defer cleanup()

	stdout, _, err := executeCommand("api", "/v1/environments/{environment_id}/campaigns",
		"--api-url", server.URL,
		"--params", `{"environment_id": "456", "page": "2", "limit": "10"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	// page and limit should be query params since they're not path params
	query := result["query"].(map[string]any)
	if query["page"] == nil || query["limit"] == nil {
		t.Errorf("expected page and limit as query params, got %v", query)
	}
}

func TestAPI_PostWithBody(t *testing.T) {
	server, cleanup := setupAPITest(t)
	defer cleanup()

	stdout, _, err := executeCommand("api", "/v1/environments/{environment_id}/campaigns",
		"--api-url", server.URL,
		"--params", `{"environment_id": "456"}`,
		"--json", `{"campaign": {"name": "Test"}}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if result["method"] != "POST" {
		t.Errorf("expected POST (auto-detected from --json), got %v", result["method"])
	}
}

func TestAPI_ExplicitMethod(t *testing.T) {
	server, cleanup := setupAPITest(t)
	defer cleanup()

	stdout, _, err := executeCommand("api", "/v1/environments/{environment_id}/campaigns/{campaign_id}",
		"--api-url", server.URL,
		"-X", "PUT",
		"--params", `{"environment_id": "456", "campaign_id": "789"}`,
		"--json", `{"campaign": {"name": "Updated"}}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if result["method"] != "PUT" {
		t.Errorf("expected PUT, got %v", result["method"])
	}
}

func TestAPI_DeleteMethod(t *testing.T) {
	server, cleanup := setupAPITest(t)
	defer cleanup()

	stdout, _, err := executeCommand("api", "/v1/environments/{environment_id}/campaigns/{campaign_id}",
		"--api-url", server.URL,
		"-X", "DELETE",
		"--params", `{"environment_id": "456", "campaign_id": "789"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if result["method"] != "DELETE" {
		t.Errorf("expected DELETE, got %v", result["method"])
	}
}

func TestAPI_PostDryRunWithBody(t *testing.T) {
	server, cleanup := setupAPITest(t)
	defer cleanup()

	stdout, _, err := executeCommand("api", "/v1/environments/{environment_id}/campaigns",
		"--api-url", server.URL,
		"--params", `{"environment_id": "456"}`,
		"--json", `{"campaign": {"name": "Test"}}`,
		"--dry-run")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if result["dry_run"] != true {
		t.Error("expected dry_run=true")
	}
	if result["body"] == nil {
		t.Error("expected body in dry-run output")
	}
}

func TestAPI_AutoPrefixSlash(t *testing.T) {
	server, cleanup := setupAPITest(t)
	defer cleanup()

	stdout, _, err := executeCommand("api", "v1/environments/{environment_id}/campaigns",
		"--api-url", server.URL,
		"--params", `{"environment_id": "456"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("invalid JSON: %v\nstdout: %s", err, stdout)
	}
	if result["path"] != "/v1/environments/456/campaigns" {
		t.Errorf("expected auto-prefixed path, got %v", result["path"])
	}
}

func TestAPI_IgnoresStoredAccountIDWithAccessToken(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	t.Setenv("CIO_ACCESS_TOKEN", "some-jwt-token")

	if err := client.WriteCredentials(&client.Credentials{
		ServiceAccountToken: "sa_live_test123",
		AccountID:           "123",
		Region:              "us",
	}); err != nil {
		t.Fatalf("failed to write credentials: %v", err)
	}

	// With CIO_ACCESS_TOKEN set, stored account_id should NOT be auto-filled.
	_, _, err := executeCommand("api", "/v1/accounts/{account_id}")
	if err == nil {
		t.Fatal("expected error for missing account_id when CIO_ACCESS_TOKEN is set")
	}
}

func TestSchema_ListResources(t *testing.T) {
	defer setupSchemaTest(t)()
	stdout, _, err := executeCommand("schema")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result []map[string]any
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("invalid JSON: %v\nstdout: %s", err, stdout)
	}
	if len(result) == 0 {
		t.Fatal("expected non-empty resource list")
	}
	// Should have resource, endpoints, count fields.
	first := result[0]
	if first["resource"] == nil || first["endpoints"] == nil || first["count"] == nil {
		t.Errorf("expected resource/endpoints/count fields, got %v", first)
	}
}

func TestSchema_ResourceMethods(t *testing.T) {
	defer setupSchemaTest(t)()
	stdout, _, err := executeCommand("schema", "campaigns")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result []map[string]any
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("invalid JSON: %v\nstdout: %s", err, stdout)
	}
	if len(result) == 0 {
		t.Fatal("expected non-empty endpoint list for campaigns")
	}
}

func TestSchema_ResourceMethod(t *testing.T) {
	defer setupSchemaTest(t)()
	stdout, _, err := executeCommand("schema", "campaigns.list")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if result["resource"] != "campaigns" {
		t.Errorf("expected resource=campaigns, got %v", result["resource"])
	}
	if result["method"] != "list" {
		t.Errorf("expected method=list, got %v", result["method"])
	}
	if result["http_method"] != "GET" {
		t.Errorf("expected http_method=GET, got %v", result["http_method"])
	}
	if result["example"] == nil {
		t.Error("expected example field in schema output")
	}
}

func TestSchema_UnknownMethod(t *testing.T) {
	defer setupSchemaTest(t)()
	_, _, err := executeCommand("schema", "campaigns.nonexistent")
	if err == nil {
		t.Fatal("expected error for unknown method")
	}
}

func TestSchema_UnknownResource(t *testing.T) {
	defer setupSchemaTest(t)()
	_, _, err := executeCommand("schema", "nonexistent")
	if err == nil {
		t.Fatal("expected error for unknown resource")
	}
}

func TestAPI_JSONFromFile(t *testing.T) {
	server, cleanup := setupAPITest(t)
	defer cleanup()

	dir := t.TempDir()
	f := filepath.Join(dir, "body.json")
	_ = os.WriteFile(f, []byte(`{"campaign": {"name": "Test"}}`), 0644)

	stdout, _, err := executeCommand("api", "/v1/environments/{environment_id}/campaigns",
		"--api-url", server.URL,
		"--params", `{"environment_id": "456"}`,
		"--json", "@"+f)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if result["method"] != "POST" {
		t.Errorf("method = %v, want POST", result["method"])
	}
}

func TestAPI_JSONFromFile_DryRun(t *testing.T) {
	server, cleanup := setupAPITest(t)
	defer cleanup()

	dir := t.TempDir()
	f := filepath.Join(dir, "body.json")
	_ = os.WriteFile(f, []byte(`{"campaign": {"name": "Test"}}`), 0644)

	stdout, _, err := executeCommand("api", "/v1/environments/{environment_id}/campaigns",
		"--api-url", server.URL,
		"--params", `{"environment_id": "456"}`,
		"--json", "@"+f, "--dry-run")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if result["method"] != "POST" {
		t.Errorf("method = %v, want POST", result["method"])
	}
	if result["body"] == nil {
		t.Error("expected body in dry run output")
	}
}

func TestAPI_JSONFromFile_NotFound(t *testing.T) {
	server, cleanup := setupAPITest(t)
	defer cleanup()

	_, _, err := executeCommand("api", "/v1/environments/{environment_id}/campaigns",
		"--api-url", server.URL,
		"--params", `{"environment_id": "456"}`,
		"--json", "@/nonexistent/file.json")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
	if !strings.Contains(err.Error(), "@/nonexistent/file.json") {
		t.Errorf("expected error to mention file path, got: %s", err.Error())
	}
}

func TestAPI_JSONFromFile_InvalidJSON(t *testing.T) {
	server, cleanup := setupAPITest(t)
	defer cleanup()

	dir := t.TempDir()
	f := filepath.Join(dir, "bad.json")
	_ = os.WriteFile(f, []byte(`{not json`), 0644)

	_, _, err := executeCommand("api", "/v1/environments/{environment_id}/campaigns",
		"--api-url", server.URL,
		"--params", `{"environment_id": "456"}`,
		"--json", "@"+f)
	if err == nil {
		t.Fatal("expected error for invalid JSON in file")
	}
	if !strings.Contains(err.Error(), "from file") {
		t.Errorf("expected error to mention 'from file', got: %s", err.Error())
	}
	if !strings.Contains(err.Error(), "bad.json") {
		t.Errorf("expected error to mention filename, got: %s", err.Error())
	}
}

func TestAPI_JSONFromStdin_DryRun(t *testing.T) {
	server, cleanup := setupAPITest(t)
	defer cleanup()

	body := `{"campaign":{"name":"Test"}}`
	rootCmd.SetIn(strings.NewReader(body))
	t.Cleanup(func() { rootCmd.SetIn(nil) })

	stdout, _, err := executeCommand("api", "/v1/environments/{environment_id}/campaigns",
		"--api-url", server.URL,
		"--params", `{"environment_id": "456"}`,
		"--json", "-", "--dry-run")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("invalid JSON: %v\nstdout: %s", err, stdout)
	}
	if result["dry_run"] != true {
		t.Error("expected dry_run=true")
	}
	got, err := json.Marshal(result["body"])
	if err != nil {
		t.Fatalf("re-marshal body: %v", err)
	}
	if string(got) != body {
		t.Errorf("body = %s, want %s", got, body)
	}
}

func TestAPI_JSONFromStdin_Empty(t *testing.T) {
	server, cleanup := setupAPITest(t)
	defer cleanup()

	rootCmd.SetIn(strings.NewReader(""))
	t.Cleanup(func() { rootCmd.SetIn(nil) })

	_, _, err := executeCommand("api", "/v1/environments/{environment_id}/campaigns",
		"--api-url", server.URL,
		"--params", `{"environment_id": "456"}`,
		"--json", "-")
	if err == nil {
		t.Fatal("expected error for empty stdin")
	}
	if !strings.Contains(err.Error(), "must not be empty") {
		t.Errorf("expected 'must not be empty' error, got: %v", err)
	}
}

func TestAPI_JSONFromFile_EmptyFilename(t *testing.T) {
	server, cleanup := setupAPITest(t)
	defer cleanup()

	_, _, err := executeCommand("api", "/v1/environments/{environment_id}/campaigns",
		"--api-url", server.URL,
		"--params", `{"environment_id": "456"}`,
		"--json", "@")
	if err == nil {
		t.Fatal("expected error for empty filename")
	}
	if !strings.Contains(err.Error(), "missing filename") {
		t.Errorf("expected 'missing filename' error, got: %s", err.Error())
	}
}
