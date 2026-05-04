package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// trackServer creates a test server that mimics the track API's /v1/send/* endpoints.
func trackServer(t *testing.T, wantToken, wantWorkspaceID string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		auth := r.Header.Get("Authorization")
		if auth != "Bearer "+wantToken {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"meta":{"error":"Unauthorized"}}`))
			return
		}
		wsID := r.Header.Get("X-Workspace-Id")
		if wantWorkspaceID != "" && wsID != wantWorkspaceID {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"meta":{"error":"workspace_id is required"}}`))
			return
		}
		resp := map[string]any{
			"delivery_id": "RKalBAUAAZ21_test==",
			"queued_at":   1776874924,
		}
		data, _ := json.Marshal(resp)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(data)
	}))
}

func resetSendFlags() {
	if f := sendCmd.PersistentFlags().Lookup("environment-id"); f != nil {
		_ = sendCmd.PersistentFlags().Set("environment-id", "")
	}
}

func setupSendTest(t *testing.T, token, workspaceID string) (*httptest.Server, func()) {
	t.Helper()
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	t.Setenv("CIO_TOKEN", token)
	t.Setenv("CIO_ACCESS_TOKEN", "")
	t.Setenv("CIO_ENVIRONMENT_ID", "")
	server := trackServer(t, token, workspaceID)
	t.Setenv("CIO_TRACK_URL", server.URL)
	return server, func() { server.Close() }
}

// ---------------------------------------------------------------------------
// Flag-based sends
// ---------------------------------------------------------------------------

func TestSend_EmailWithFlags(t *testing.T) {
	_, cleanup := setupSendTest(t, "sa_live_test123", "123")
	defer cleanup()

	stdout, _, err := executeCommand("send", "email",
		"--environment-id", "123",
		"--token", "sa_live_test123",
		"--to", "user@example.com",
		"--from", "Acme <noreply@example.com>",
		"--subject", "Hello World",
		"--body", "<h1>Hi</h1>")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("invalid JSON: %v\nstdout: %s", err, stdout)
	}
	if result["delivery_id"] != "RKalBAUAAZ21_test==" {
		t.Errorf("expected delivery_id, got %v", result["delivery_id"])
	}
}

func TestSend_EmailPlaintext(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CIO_TOKEN", "sa_live_test123")
	t.Setenv("CIO_ACCESS_TOKEN", "")

	stdout, _, err := executeCommand("send", "email",
		"--environment-id", "123",
		"--token", "sa_live_test123",
		"--to", "user@example.com",
		"--from", "noreply@example.com",
		"--subject", "Hello",
		"--text", "It works!",
		"--dry-run")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("invalid JSON: %v\nstdout: %s", err, stdout)
	}
	body := result["body"].(map[string]any)
	if body["body_plain"] != "It works!" {
		t.Errorf("expected body_plain, got %v", body["body_plain"])
	}
	if body["to"] != "user@example.com" {
		t.Errorf("expected to, got %v", body["to"])
	}
}

func TestSend_EmailAutoInfersIdentifiers(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CIO_TOKEN", "sa_live_test123")
	t.Setenv("CIO_ACCESS_TOKEN", "")

	stdout, _, err := executeCommand("send", "email",
		"--environment-id", "123",
		"--token", "sa_live_test123",
		"--to", "alice@example.com",
		"--from", "noreply@example.com",
		"--subject", "Hi",
		"--body", "hello",
		"--dry-run")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	body := result["body"].(map[string]any)
	idents := body["identifiers"].(map[string]any)
	if idents["email"] != "alice@example.com" {
		t.Errorf("expected identifiers.email=alice@example.com, got %v", idents["email"])
	}
}

func TestSend_EmailExplicitIdentifiers(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CIO_TOKEN", "sa_live_test123")
	t.Setenv("CIO_ACCESS_TOKEN", "")

	stdout, _, err := executeCommand("send", "email",
		"--environment-id", "123",
		"--token", "sa_live_test123",
		"--to", "user@example.com",
		"--from", "noreply@example.com",
		"--subject", "Hi",
		"--body", "hello",
		"--identifiers", `{"id":"user123"}`,
		"--dry-run")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	body := result["body"].(map[string]any)
	idents := body["identifiers"].(map[string]any)
	if idents["id"] != "user123" {
		t.Errorf("expected explicit identifiers.id=user123, got %v", idents)
	}
}

func TestSend_FlagsOverrideJSON(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CIO_TOKEN", "sa_live_test123")
	t.Setenv("CIO_ACCESS_TOKEN", "")

	stdout, _, err := executeCommand("send", "email",
		"--environment-id", "123",
		"--token", "sa_live_test123",
		"--json", `{"identifiers":{"id":"user123"},"to":"old@example.com","subject":"old"}`,
		"--to", "new@example.com",
		"--subject", "New subject",
		"--dry-run")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	body := result["body"].(map[string]any)
	if body["to"] != "new@example.com" {
		t.Errorf("expected --to to override, got %v", body["to"])
	}
	if body["subject"] != "New subject" {
		t.Errorf("expected --subject to override, got %v", body["subject"])
	}
	// Identifiers from JSON should be preserved (not auto-inferred).
	idents := body["identifiers"].(map[string]any)
	if idents["id"] != "user123" {
		t.Errorf("expected JSON identifiers preserved, got %v", idents)
	}
}

// ---------------------------------------------------------------------------
// JSON-based sends (existing behavior)
// ---------------------------------------------------------------------------

func TestSend_EmailJSON(t *testing.T) {
	_, cleanup := setupSendTest(t, "sa_live_test123", "70310")
	defer cleanup()

	stdout, _, err := executeCommand("send", "email",
		"--environment-id", "70310",
		"--token", "sa_live_test123",
		"--json", `{"transactional_message_id":"blah","identifiers":{"email":"user@example.com"},"to":"user@example.com","subject":"Test","body":"hello"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("invalid JSON: %v\nstdout: %s", err, stdout)
	}
	if result["delivery_id"] != "RKalBAUAAZ21_test==" {
		t.Errorf("expected delivery_id, got %v", result["delivery_id"])
	}
}

// ---------------------------------------------------------------------------
// Dry run
// ---------------------------------------------------------------------------

func TestSend_DryRun(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CIO_TOKEN", "sa_live_test123")
	t.Setenv("CIO_ACCESS_TOKEN", "")

	stdout, _, err := executeCommand("send", "email",
		"--environment-id", "70310",
		"--token", "sa_live_test123",
		"--to", "test@example.com",
		"--from", "noreply@example.com",
		"--subject", "Test",
		"--body", "hello",
		"--dry-run")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("invalid JSON: %v\nstdout: %s", err, stdout)
	}
	if result["dry_run"] != true {
		t.Errorf("expected dry_run=true")
	}
	if result["method"] != "POST" {
		t.Errorf("expected method=POST, got %v", result["method"])
	}
	if result["url"] != "https://track.customer.io/v1/send/email" {
		t.Errorf("expected track URL, got %v", result["url"])
	}
	headers := result["headers"].(map[string]any)
	if headers["X-Workspace-Id"] != "70310" {
		t.Errorf("expected X-Workspace-Id=70310, got %v", headers["X-Workspace-Id"])
	}
}

// ---------------------------------------------------------------------------
// Validation errors
// ---------------------------------------------------------------------------

func TestSend_MissingEnvironmentID(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CIO_TOKEN", "sa_live_test123")
	t.Setenv("CIO_ACCESS_TOKEN", "")
	t.Setenv("CIO_ENVIRONMENT_ID", "")

	_, _, err := executeCommand("send", "email",
		"--token", "sa_live_test123",
		"--to", "user@example.com",
		"--from", "noreply@example.com",
		"--subject", "Hi",
		"--body", "hello")
	if err == nil {
		t.Fatal("expected error for missing environment-id")
	}
}

func TestSend_MissingToken(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CIO_TOKEN", "")
	t.Setenv("CIO_ACCESS_TOKEN", "")

	_, _, err := executeCommand("send", "email",
		"--environment-id", "123",
		"--to", "user@example.com",
		"--from", "noreply@example.com",
		"--subject", "Hi",
		"--body", "hello")
	if err == nil {
		t.Fatal("expected error for missing token")
	}
}

func TestSend_MissingIdentifiers(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CIO_TOKEN", "sa_live_test123")
	t.Setenv("CIO_ACCESS_TOKEN", "")

	// JSON with no identifiers and no --to — should fail
	_, _, err := executeCommand("send", "email",
		"--environment-id", "123",
		"--token", "sa_live_test123",
		"--json", `{"from":"noreply@example.com","subject":"Hi","body":"hello"}`,
		"--dry-run")
	if err == nil {
		t.Fatal("expected error for missing identifiers")
	}
}

func TestSend_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"meta":{"error":"Missing required field: \"to\""}}`))
	}))
	defer server.Close()

	t.Setenv("HOME", t.TempDir())
	t.Setenv("CIO_TOKEN", "sa_live_test123")
	t.Setenv("CIO_ACCESS_TOKEN", "")
	t.Setenv("CIO_TRACK_URL", server.URL)

	_, _, err := executeCommand("send", "email",
		"--environment-id", "123",
		"--token", "sa_live_test123",
		"--json", `{"transactional_message_id":1,"identifiers":{"email":"test@example.com"}}`)
	if err == nil {
		t.Fatal("expected error for 400 response")
	}
}

func TestSend_InvalidEnvironmentID(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CIO_TOKEN", "sa_live_test123")
	t.Setenv("CIO_ACCESS_TOKEN", "")

	_, _, err := executeCommand("send", "email",
		"--environment-id", "abc?xyz",
		"--token", "sa_live_test123",
		"--to", "user@example.com",
		"--from", "noreply@example.com",
		"--subject", "Hi",
		"--body", "hello")
	if err == nil {
		t.Fatal("expected error for invalid environment-id")
	}
}

// ---------------------------------------------------------------------------
// JQ filter
// ---------------------------------------------------------------------------

func TestSend_JQFilter(t *testing.T) {
	_, cleanup := setupSendTest(t, "sa_live_test123", "123")
	defer cleanup()

	stdout, _, err := executeCommand("send", "email",
		"--environment-id", "123",
		"--token", "sa_live_test123",
		"--json", `{"transactional_message_id":1,"identifiers":{"email":"test@example.com"}}`,
		"--jq", ".delivery_id")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := strings.TrimSpace(stdout)
	if got != `"RKalBAUAAZ21_test=="` {
		t.Errorf("expected filtered delivery_id, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// Env var fallbacks
// ---------------------------------------------------------------------------

func TestSend_EnvironmentIDFromEnvVar(t *testing.T) {
	_, cleanup := setupSendTest(t, "sa_live_test123", "999")
	defer cleanup()
	t.Setenv("CIO_ENVIRONMENT_ID", "999")

	stdout, _, err := executeCommand("send", "email",
		"--token", "sa_live_test123",
		"--json", `{"transactional_message_id":1,"identifiers":{"email":"test@example.com"}}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("invalid JSON: %v\nstdout: %s", err, stdout)
	}
	if result["delivery_id"] != "RKalBAUAAZ21_test==" {
		t.Errorf("expected delivery_id, got %v", result["delivery_id"])
	}
}

func TestSend_TrackURLFromEnvVar(t *testing.T) {
	_, cleanup := setupSendTest(t, "sa_live_test123", "123")
	defer cleanup()
	// CIO_TRACK_URL is already set by setupSendTest — this test verifies it works.

	stdout, _, err := executeCommand("send", "email",
		"--environment-id", "123",
		"--token", "sa_live_test123",
		"--json", `{"transactional_message_id":1,"identifiers":{"email":"test@example.com"}}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("invalid JSON: %v\nstdout: %s", err, stdout)
	}
	if result["delivery_id"] != "RKalBAUAAZ21_test==" {
		t.Errorf("expected delivery_id, got %v", result["delivery_id"])
	}
}

// ---------------------------------------------------------------------------
// --watch flag
// ---------------------------------------------------------------------------

// deliveryAPIServer creates a test server that mimics the REST API delivery
// status endpoint. firstResponses are returned in order before the final
// terminal response.
func deliveryAPIServer(t *testing.T, envID, deliveryID string, firstResponses []map[string]any, finalResp map[string]any) *httptest.Server {
	t.Helper()
	call := 0
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		want := "/v1/environments/" + envID + "/deliveries/" + deliveryID
		if r.URL.Path != want {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var resp map[string]any
		if call < len(firstResponses) {
			resp = firstResponses[call]
		} else {
			resp = finalResp
		}
		call++
		data, _ := json.Marshal(resp)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(data)
	}))
}

func TestSend_Watch_PrintsDeliveryStatus(t *testing.T) {
	_, cleanup := setupSendTest(t, "sa_live_test123", "123")
	defer cleanup()

	deliveryResp := map[string]any{
		"delivery_id": "RKalBAUAAZ21_test==",
		"state":       "sent",
		"type":        "email",
	}
	apiServer := deliveryAPIServer(t, "123", "RKalBAUAAZ21_test==", nil, deliveryResp)
	defer apiServer.Close()
	t.Setenv("CIO_API_URL", apiServer.URL)
	t.Setenv("CIO_ACCESS_TOKEN", "fake-access-token-for-test")

	stdout, _, err := executeCommand("send", "email",
		"--environment-id", "123",
		"--token", "sa_live_test123",
		"--json", `{"transactional_message_id":1,"identifiers":{"email":"test@example.com"}}`,
		"--watch")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("invalid JSON output: %v\nstdout: %s", err, stdout)
	}
	if result["state"] != "sent" {
		t.Errorf("expected state=sent, got %v", result["state"])
	}
}

func TestSend_Watch_RetriesToGetTerminalStatus(t *testing.T) {
	_, cleanup := setupSendTest(t, "sa_live_test123", "123")
	defer cleanup()

	pending := map[string]any{"delivery_id": "RKalBAUAAZ21_test=="}
	terminal := map[string]any{"delivery_id": "RKalBAUAAZ21_test==", "state": "failed", "failure_message": "invalid address"}
	apiServer := deliveryAPIServer(t, "123", "RKalBAUAAZ21_test==", []map[string]any{pending, pending}, terminal)
	defer apiServer.Close()
	t.Setenv("CIO_API_URL", apiServer.URL)
	t.Setenv("CIO_ACCESS_TOKEN", "fake-access-token-for-test")

	stdout, _, err := executeCommand("send", "email",
		"--environment-id", "123",
		"--token", "sa_live_test123",
		"--json", `{"transactional_message_id":1,"identifiers":{"email":"test@example.com"}}`,
		"-w")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("invalid JSON output: %v\nstdout: %s", err, stdout)
	}
	if result["state"] != "failed" {
		t.Errorf("expected state=failed, got %v", result["state"])
	}
}

func TestSend_Watch_Retries404(t *testing.T) {
	_, cleanup := setupSendTest(t, "sa_live_test123", "123")
	defer cleanup()

	calls := 0
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls < 3 {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		data, _ := json.Marshal(map[string]any{"delivery_id": "RKalBAUAAZ21_test==", "state": "sent"})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(data)
	}))
	defer apiServer.Close()
	t.Setenv("CIO_API_URL", apiServer.URL)
	t.Setenv("CIO_ACCESS_TOKEN", "fake-access-token-for-test")

	stdout, _, err := executeCommand("send", "email",
		"--environment-id", "123",
		"--token", "sa_live_test123",
		"--json", `{"transactional_message_id":1,"identifiers":{"email":"test@example.com"}}`,
		"--watch")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls < 3 {
		t.Errorf("expected at least 3 calls (2 x 404 + final), got %d", calls)
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("invalid JSON output: %v\nstdout: %s", err, stdout)
	}
	if result["state"] != "sent" {
		t.Errorf("expected state=sent, got %v", result["state"])
	}
}

// ---------------------------------------------------------------------------
// isTerminalDelivery unit tests
// ---------------------------------------------------------------------------

func TestIsTerminalDelivery(t *testing.T) {
	cases := []struct {
		name     string
		json     string
		terminal bool
	}{
		{"top-level sent", `{"state":"sent"}`, true},
		{"top-level opened", `{"state":"opened"}`, true},
		{"top-level failed", `{"state":"failed"}`, true},
		{"top-level bounced", `{"state":"bounced"}`, true},
		{"top-level suppressed", `{"state":"suppressed"}`, true},
		{"top-level undeliverable", `{"state":"undeliverable"}`, true},
		{"top-level deferred", `{"state":"deferred"}`, true},
		{"in-progress queued", `{"state":"queued"}`, false},
		{"in-progress drafted", `{"state":"drafted"}`, false},
		{"in-progress attempted", `{"state":"attempted"}`, false},
		{"no state field", `{"delivery_id":"abc"}`, false},
		{"empty state", `{"state":""}`, false},
		{"nested sent", `{"delivery":{"state":"sent"}}`, true},
		{"nested queued", `{"delivery":{"state":"queued"}}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := isTerminalDelivery(json.RawMessage(tc.json))
			if got != tc.terminal {
				t.Errorf("isTerminalDelivery(%s) = %v, want %v", tc.json, got, tc.terminal)
			}
		})
	}
}
