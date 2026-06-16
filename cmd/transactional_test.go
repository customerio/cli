package cmd

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func resetTransactionalFlags() {
	if f := transactionalCmd.PersistentFlags().Lookup("environment-id"); f != nil {
		_ = transactionalCmd.PersistentFlags().Set("environment-id", "")
	}
}

func setupTransactionalSendTest(t *testing.T, token, workspaceID string) (*httptest.Server, func()) {
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
// transactional send — flag-based
// ---------------------------------------------------------------------------

func TestTransactionalSend_EmailFlags(t *testing.T) {
	_, cleanup := setupTransactionalSendTest(t, "sa_live_test123", "70310")
	defer cleanup()
	resetSendFlags()
	resetTransactionalFlags()

	stdout, _, err := executeCommand("transactional", "send", "email",
		"--environment-id", "70310",

		"--token", "sa_live_test123",
		"--transactional-message-id", "blah",
		"--to", "user@example.com",
		"--message-data", `{"name":"Alice"}`)
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

func TestTransactionalSend_EmailJSON(t *testing.T) {
	_, cleanup := setupTransactionalSendTest(t, "sa_live_test123", "70310")
	defer cleanup()
	resetSendFlags()
	resetTransactionalFlags()

	stdout, _, err := executeCommand("transactional", "send", "email",
		"--environment-id", "70310",

		"--token", "sa_live_test123",
		"--json", `{"transactional_message_id":"blah","identifiers":{"email":"user@example.com"}}`)
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

func TestTransactionalSend_Push(t *testing.T) {
	_, cleanup := setupTransactionalSendTest(t, "sa_live_test123", "123")
	defer cleanup()
	resetSendFlags()
	resetTransactionalFlags()

	stdout, _, err := executeCommand("transactional", "send", "push",
		"--environment-id", "123",

		"--token", "sa_live_test123",
		"--transactional-message-id", "2",
		"--identifiers", `{"id":"user123"}`)
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

func TestTransactionalSend_MissingTransactionalMessageID(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CIO_TOKEN", "sa_live_test123")
	t.Setenv("CIO_ACCESS_TOKEN", "")
	t.Setenv("CIO_ENVIRONMENT_ID", "")
	resetSendFlags()
	resetTransactionalFlags()

	_, _, err := executeCommand("transactional", "send", "email",
		"--environment-id", "123",

		"--token", "sa_live_test123",
		"--to", "test@example.com")
	if err == nil {
		t.Fatal("expected error for missing transactional_message_id")
	}
	if !strings.Contains(err.Error(), "transactional-message-id") {
		t.Errorf("expected transactional-message-id error, got: %v", err)
	}
}

func TestTransactionalSend_DryRun(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CIO_TOKEN", "sa_live_test123")
	t.Setenv("CIO_ACCESS_TOKEN", "")
	t.Setenv("CIO_ENVIRONMENT_ID", "")
	resetSendFlags()
	resetTransactionalFlags()

	stdout, _, err := executeCommand("transactional", "send", "email",
		"--environment-id", "70310",

		"--token", "sa_live_test123",
		"--transactional-message-id", "1",
		"--to", "test@example.com",
		"--message-data", `{"name":"Alice"}`,
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
	if result["url"] != "https://track.customer.io/v1/send/email" {
		t.Errorf("expected track URL, got %v", result["url"])
	}
}

// TestTransactionalSend_DryRun_APIURLOverridesTrackHost covers SELF-48 for the
// transactional send path: --api-url must direct the send at that host.
func TestTransactionalSend_DryRun_APIURLOverridesTrackHost(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CIO_TOKEN", "sa_live_test123")
	t.Setenv("CIO_ACCESS_TOKEN", "")
	t.Setenv("CIO_ENVIRONMENT_ID", "")
	t.Setenv("CIO_TRACK_URL", "")
	resetSendFlags()
	resetTransactionalFlags()

	stdout, _, err := executeCommand("transactional", "send", "email",
		"--environment-id", "71981",
		"--token", "sa_live_test123",
		"--transactional-message-id", "3",
		"--to", "test@example.com",
		"--api-url", "https://track.example.test",
		"--dry-run")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("invalid JSON: %v\nstdout: %s", err, stdout)
	}
	if result["url"] != "https://track.example.test/v1/send/email" {
		t.Errorf("expected overridden track URL, got %v", result["url"])
	}
}

func TestTransactionalSend_JQFilter(t *testing.T) {
	_, cleanup := setupTransactionalSendTest(t, "sa_live_test123", "123")
	defer cleanup()
	resetSendFlags()
	resetTransactionalFlags()

	stdout, _, err := executeCommand("transactional", "send", "email",
		"--environment-id", "123",

		"--token", "sa_live_test123",
		"--transactional-message-id", "1",
		"--to", "test@example.com",
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
// transactional list
// ---------------------------------------------------------------------------

func TestTransactionalList(t *testing.T) {
	server, cleanup := setupAPITest(t)
	defer cleanup()
	resetSendFlags()
	resetTransactionalFlags()

	stdout, _, err := executeCommand("transactional", "list",
		"--environment-id", "456",
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
	if result["path"] != "/v1/environments/456/transactional_messages" {
		t.Errorf("expected transactional_messages path, got %v", result["path"])
	}
}

func TestTransactionalList_DryRun(t *testing.T) {
	server, cleanup := setupAPITest(t)
	defer cleanup()
	resetSendFlags()
	resetTransactionalFlags()

	stdout, _, err := executeCommand("transactional", "list",
		"--environment-id", "456",
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
		t.Errorf("expected dry_run=true")
	}
}

func TestTransactionalList_MissingEnvironmentID(t *testing.T) {
	server, cleanup := setupAPITest(t)
	defer cleanup()
	resetSendFlags()
	resetTransactionalFlags()

	_, _, err := executeCommand("transactional", "list",
		"--api-url", server.URL)
	if err == nil {
		t.Fatal("expected error for missing environment-id")
	}
}

func TestTransactionalList_JQFilter(t *testing.T) {
	server, cleanup := setupAPITest(t)
	defer cleanup()
	resetSendFlags()
	resetTransactionalFlags()

	stdout, _, err := executeCommand("transactional", "list",
		"--environment-id", "456",
		"--api-url", server.URL,
		"--jq", ".method")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := strings.TrimSpace(stdout)
	if got != `"GET"` {
		t.Errorf("expected \"GET\", got %q", got)
	}
}
