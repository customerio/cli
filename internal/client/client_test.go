package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/customerio/cli/internal/useragent"
)

func TestClient_Do_BearerToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth != "Bearer test-jwt" {
			t.Errorf("expected 'Bearer test-jwt', got %q", auth)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	c := New(Config{
		BaseURL:     server.URL,
		AccessToken: "test-jwt", // Pre-exchanged JWT, skip OAuth flow.
		RetryConfig: &RetryConfig{MaxRetries: 0, SleepFn: ContextSleep},
	})

	result, err := c.Do(context.Background(), "GET", "/test", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var resp map[string]any
	if err := json.Unmarshal(result, &resp); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if resp["ok"] != true {
		t.Errorf("expected ok=true, got %v", resp["ok"])
	}
}

func TestClient_Do_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
	}))
	defer server.Close()

	c := New(Config{
		BaseURL:     server.URL,
		AccessToken: "bad-jwt",
		RetryConfig: &RetryConfig{MaxRetries: 0, SleepFn: ContextSleep},
	})

	_, err := c.Do(context.Background(), "GET", "/test", nil, nil)
	if err == nil {
		t.Fatal("expected error")
	}

	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("expected *APIError, got %T", err)
	}
	if apiErr.StatusCode != 401 {
		t.Errorf("expected 401, got %d", apiErr.StatusCode)
	}
}

func TestClient_Do_NonJSONResponse(t *testing.T) {
	// Simulates an upstream (e.g. SPA fallback) returning 200 OK with an HTML
	// body for an unknown API path. Without validation, the HTML would flow
	// through as a json.RawMessage and later fail at marshal time with a
	// cryptic "invalid character '<'" error.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<!DOCTYPE html><html><body>not json</body></html>"))
	}))
	defer server.Close()

	c := New(Config{
		BaseURL:     server.URL,
		AccessToken: "jwt",
		RetryConfig: &RetryConfig{MaxRetries: 0, SleepFn: ContextSleep},
	})

	_, err := c.Do(context.Background(), "GET", "/v1/unknown", nil, nil)
	if err == nil {
		t.Fatal("expected error for non-JSON response")
	}
	njErr, ok := err.(*NonJSONResponseError)
	if !ok {
		t.Fatalf("expected *NonJSONResponseError, got %T: %v", err, err)
	}
	if njErr.StatusCode != 200 {
		t.Errorf("expected status 200, got %d", njErr.StatusCode)
	}
	if !strings.Contains(njErr.ContentType, "text/html") {
		t.Errorf("expected text/html content type, got %q", njErr.ContentType)
	}
	// Error message must not echo raw HTML — it would mislead users into
	// thinking the request actually fetched a page.
	if strings.Contains(njErr.Error(), "<!DOCTYPE") {
		t.Errorf("error message should not include raw HTML, got: %s", njErr.Error())
	}
}

func TestClient_Do_WithParams(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("limit") != "10" {
			t.Errorf("expected limit=10, got %q", r.URL.Query().Get("limit"))
		}
		_, _ = w.Write([]byte(`{"items":[]}`))
	}))
	defer server.Close()

	c := New(Config{
		BaseURL:     server.URL,
		AccessToken: "jwt",
		RetryConfig: &RetryConfig{MaxRetries: 0, SleepFn: ContextSleep},
	})

	_, err := c.Do(context.Background(), "GET", "/test", map[string]string{"limit": "10"}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestClient_Do_PostJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("expected application/json, got %q", ct)
		}
		_, _ = w.Write([]byte(`{"created":true}`))
	}))
	defer server.Close()

	c := New(Config{
		BaseURL:     server.URL,
		AccessToken: "jwt",
		RetryConfig: &RetryConfig{MaxRetries: 0, SleepFn: ContextSleep},
	})

	body := json.RawMessage(`{"name":"test"}`)
	_, err := c.Do(context.Background(), "POST", "/test", nil, body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestClient_OAuthExchange(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/service_accounts/oauth/token" {
			if r.Method != "POST" {
				t.Errorf("expected POST, got %s", r.Method)
			}
			if ct := r.Header.Get("Content-Type"); ct != "application/x-www-form-urlencoded" {
				t.Errorf("expected form content type, got %q", ct)
			}
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			if r.PostFormValue("grant_type") != "client_credentials" {
				t.Errorf("expected client_credentials grant, got %q", r.PostFormValue("grant_type"))
			}
			if r.PostFormValue("client_secret") != "sa_live_test123" {
				t.Errorf("expected sa_live_test123, got %q", r.PostFormValue("client_secret"))
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"jwt-session-abc","token_type":"Bearer","expires_in":3600}`))
			return
		}

		// Regular API call — verify JWT is used.
		auth := r.Header.Get("Authorization")
		if auth != "Bearer jwt-session-abc" {
			t.Errorf("expected 'Bearer jwt-session-abc', got %q", auth)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	// Use a temp home so CachedAccessToken doesn't interfere.
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	c := New(Config{
		BaseURL:             server.URL,
		ServiceAccountToken: "sa_live_test123",
		RetryConfig:         &RetryConfig{MaxRetries: 0, SleepFn: ContextSleep},
	})

	result, err := c.Do(context.Background(), "GET", "/v1/accounts/current", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var resp map[string]any
	if err := json.Unmarshal(result, &resp); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if resp["ok"] != true {
		t.Errorf("expected ok=true, got %v", resp["ok"])
	}
}

func TestClient_OAuthExchange_ReadOnly(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/service_accounts/oauth/token" {
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			if r.PostFormValue("scope") != ScopeReadOnly {
				t.Errorf("expected scope=%q, got %q", ScopeReadOnly, r.PostFormValue("scope"))
			}
			if r.PostFormValue("grant_type") != "client_credentials" {
				t.Errorf("expected client_credentials grant, got %q", r.PostFormValue("grant_type"))
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"jwt-readonly","token_type":"Bearer","expires_in":3600,"scope":"read_only"}`))
			return
		}
		auth := r.Header.Get("Authorization")
		if auth != "Bearer jwt-readonly" {
			t.Errorf("expected 'Bearer jwt-readonly', got %q", auth)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	c := New(Config{
		BaseURL:             server.URL,
		ServiceAccountToken: "sa_live_test123",
		ReadOnly:            true,
		RetryConfig:         &RetryConfig{MaxRetries: 0, SleepFn: ContextSleep},
	})

	result, err := c.Do(context.Background(), "GET", "/v1/test", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var resp map[string]any
	if err := json.Unmarshal(result, &resp); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if resp["ok"] != true {
		t.Errorf("expected ok=true, got %v", resp["ok"])
	}

	if !c.ReadOnly() {
		t.Error("expected ReadOnly() to return true")
	}
}

func TestClient_OAuthExchange_NoScopeWhenNotReadOnly(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/service_accounts/oauth/token" {
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			if scope := r.PostFormValue("scope"); scope != "" {
				t.Errorf("expected no scope param, got %q", scope)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"jwt-full","token_type":"Bearer","expires_in":3600}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	c := New(Config{
		BaseURL:             server.URL,
		ServiceAccountToken: "sa_live_test123",
		ReadOnly:            false,
		RetryConfig:         &RetryConfig{MaxRetries: 0, SleepFn: ContextSleep},
	})

	_, err := c.Do(context.Background(), "GET", "/v1/test", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestClient_ReadOnly_BlocksNonGET(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("request should not reach the server in read-only mode for non-GET")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	c := New(Config{
		BaseURL:     server.URL,
		AccessToken: "jwt-readonly",
		ReadOnly:    true,
		RetryConfig: &RetryConfig{MaxRetries: 0, SleepFn: ContextSleep},
	})

	methods := []string{"POST", "PUT", "PATCH", "DELETE"}
	for _, method := range methods {
		_, err := c.Do(context.Background(), method, "/v1/test", nil, json.RawMessage(`{}`))
		if err == nil {
			t.Errorf("%s: expected error in read-only mode, got nil", method)
			continue
		}
		if !strings.Contains(err.Error(), "read-only mode") {
			t.Errorf("%s: expected read-only error, got %v", method, err)
		}
	}

	// GET should still work.
	server2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server2.Close()

	c2 := New(Config{
		BaseURL:     server2.URL,
		AccessToken: "jwt-readonly",
		ReadOnly:    true,
		RetryConfig: &RetryConfig{MaxRetries: 0, SleepFn: ContextSleep},
	})
	_, err := c2.Do(context.Background(), "GET", "/v1/test", nil, nil)
	if err != nil {
		t.Fatalf("GET should succeed in read-only mode: %v", err)
	}
}

func TestClient_OAuthExchange_ReadOnly_ScopeMismatch(t *testing.T) {
	// Server grants a full-access token (no scope) even though we asked for read_only.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"jwt-full","token_type":"Bearer","expires_in":3600}`))
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	c := New(Config{
		BaseURL:             server.URL,
		ServiceAccountToken: "sa_live_test123",
		ReadOnly:            true,
		RetryConfig:         &RetryConfig{MaxRetries: 0, SleepFn: ContextSleep},
	})

	_, err := c.Do(context.Background(), "GET", "/v1/test", nil, nil)
	if err == nil {
		t.Fatal("expected error when server doesn't grant read_only scope")
	}
	if !strings.Contains(err.Error(), "requested read-only scope") {
		t.Errorf("expected scope mismatch error, got: %v", err)
	}
}

func TestClient_OAuthExchange_Failure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid_client","error_description":"invalid client credentials"}`))
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	c := New(Config{
		BaseURL:             server.URL,
		ServiceAccountToken: "sa_live_bad",
		RetryConfig:         &RetryConfig{MaxRetries: 0, SleepFn: ContextSleep},
	})

	_, err := c.Do(context.Background(), "GET", "/test", nil, nil)
	if err == nil {
		t.Fatal("expected error for bad token")
	}
}

func TestDiscoverRegion_US(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/service_accounts/oauth/token" {
			_, _ = w.Write([]byte(`{"access_token":"jwt-abc","token_type":"Bearer","expires_in":3600}`))
			return
		}
		if r.URL.Path == "/v1/accounts/current" {
			_, _ = w.Write([]byte(`{"account":{"id":1,"name":"Acme","data_center":"us"}}`))
			return
		}
	}))
	defer server.Close()

	result, err := DiscoverRegion(context.Background(), "sa_live_test", server.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Region != "us" {
		t.Errorf("expected us, got %q", result.Region)
	}
	if result.AccessToken != "jwt-abc" {
		t.Errorf("expected jwt-abc, got %q", result.AccessToken)
	}
	if result.AccountID != "1" {
		t.Errorf("expected account_id 1, got %q", result.AccountID)
	}
}

func TestDiscoverRegion_EU(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/service_accounts/oauth/token" {
			_, _ = w.Write([]byte(`{"access_token":"jwt-eu","token_type":"Bearer","expires_in":3600}`))
			return
		}
		if r.URL.Path == "/v1/accounts/current" {
			_, _ = w.Write([]byte(`{"account":{"id":2,"name":"Euro Corp","data_center":"eu"}}`))
			return
		}
	}))
	defer server.Close()

	result, err := DiscoverRegion(context.Background(), "sa_live_test", server.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Region != "eu" {
		t.Errorf("expected eu, got %q", result.Region)
	}
	if result.AccountID != "2" {
		t.Errorf("expected account_id 2, got %q", result.AccountID)
	}
}

func TestDiscoverRegion_BadToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid_client"}`))
	}))
	defer server.Close()

	_, err := DiscoverRegion(context.Background(), "sa_live_bad", server.URL)
	if err == nil {
		t.Fatal("expected error for bad token")
	}
}

func TestClient_OAuthExchange_CustomScopes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/service_accounts/oauth/token" {
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			scope := r.PostFormValue("scope")
			if scope != "admin extra" {
				t.Errorf("expected scope=%q, got %q", "admin extra", scope)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"jwt-scoped","token_type":"Bearer","expires_in":3600,"scope":"admin extra"}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	c := New(Config{
		BaseURL:             server.URL,
		ServiceAccountToken: "sa_live_test123",
		Scopes:              []string{"admin", "extra"},
		RetryConfig:         &RetryConfig{MaxRetries: 0, SleepFn: ContextSleep},
	})

	_, err := c.Do(context.Background(), "GET", "/v1/test", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestClient_OAuthExchange_ReadOnlyWithCustomScopes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/service_accounts/oauth/token" {
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			scope := r.PostFormValue("scope")
			if scope != "read_only admin" {
				t.Errorf("expected scope=%q, got %q", "read_only admin", scope)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"jwt-ro-scoped","token_type":"Bearer","expires_in":3600,"scope":"read_only admin"}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	c := New(Config{
		BaseURL:             server.URL,
		ServiceAccountToken: "sa_live_test123",
		ReadOnly:            true,
		Scopes:              []string{"admin"},
		RetryConfig:         &RetryConfig{MaxRetries: 0, SleepFn: ContextSleep},
	})

	_, err := c.Do(context.Background(), "GET", "/v1/test", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestClient_NoToken(t *testing.T) {
	c := New(Config{
		BaseURL:     "http://localhost:9999",
		RetryConfig: &RetryConfig{MaxRetries: 0, SleepFn: ContextSleep},
	})

	_, err := c.Do(context.Background(), "GET", "/test", nil, nil)
	if err == nil {
		t.Fatal("expected error with no token")
	}
}

func TestClient_TokenExpiryRefresh(t *testing.T) {
	exchangeCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/service_accounts/oauth/token" {
			exchangeCount++
			_, _ = w.Write([]byte(`{"access_token":"jwt-` + fmt.Sprintf("%d", exchangeCount) + `","token_type":"Bearer","expires_in":3600}`))
			return
		}
		if r.Header.Get("Authorization") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	c := New(Config{
		BaseURL:             server.URL,
		ServiceAccountToken: "sa_live_test",
		RetryConfig:         &RetryConfig{MaxRetries: 0, SleepFn: ContextSleep},
	})

	// First call should exchange token.
	_, err := c.Do(context.Background(), "GET", "/test", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if exchangeCount != 1 {
		t.Fatalf("expected 1 exchange, got %d", exchangeCount)
	}

	// Second call should reuse cached token (not expired).
	_, err = c.Do(context.Background(), "GET", "/test", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if exchangeCount != 1 {
		t.Fatalf("expected still 1 exchange, got %d", exchangeCount)
	}

	// Simulate expiry by setting expiresAt to the past.
	c.accessTokenExpiresAt = time.Now().Add(-1 * time.Minute)

	// Third call should re-exchange because token expired.
	_, err = c.Do(context.Background(), "GET", "/test", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if exchangeCount != 2 {
		t.Fatalf("expected 2 exchanges after expiry, got %d", exchangeCount)
	}
}

func TestClient_401RetryWithFreshToken(t *testing.T) {
	exchangeCount := 0
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/service_accounts/oauth/token" {
			exchangeCount++
			_, _ = w.Write([]byte(`{"access_token":"jwt-` + fmt.Sprintf("%d", exchangeCount) + `","token_type":"Bearer","expires_in":3600}`))
			return
		}
		requestCount++
		// First API request returns 401, second succeeds.
		if requestCount == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	c := New(Config{
		BaseURL:             server.URL,
		ServiceAccountToken: "sa_live_test",
		RetryConfig:         &RetryConfig{MaxRetries: 0, SleepFn: ContextSleep},
	})

	_, err := c.Do(context.Background(), "GET", "/test", nil, nil)
	if err != nil {
		t.Fatalf("expected success after 401 retry, got: %v", err)
	}
	if exchangeCount != 2 {
		t.Errorf("expected 2 token exchanges (initial + retry), got %d", exchangeCount)
	}
	if requestCount != 2 {
		t.Errorf("expected 2 API requests (401 + retry), got %d", requestCount)
	}
}

func TestParseJWTExpiry_ValidJWT(t *testing.T) {
	// Create a JWT with exp claim set to 1700000000 (2023-11-14T22:13:20Z)
	// Header: {"alg":"HS256","typ":"JWT"}
	// Payload: {"exp":1700000000,"sub":"test"}
	token := "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJleHAiOjE3MDAwMDAwMDAsInN1YiI6InRlc3QifQ.signature"
	exp, err := parseJWTExpiry(token)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := time.Unix(1700000000, 0)
	if !exp.Equal(expected) {
		t.Errorf("expected %v, got %v", expected, exp)
	}
}

func TestParseJWTExpiry_NotAJWT(t *testing.T) {
	_, err := parseJWTExpiry("not-a-jwt")
	if err == nil {
		t.Fatal("expected error for non-JWT string")
	}
}

func TestParseJWTExpiry_MissingExp(t *testing.T) {
	// Payload: {"sub":"test"} (no exp)
	token := "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ0ZXN0In0.signature"
	_, err := parseJWTExpiry(token)
	if err == nil {
		t.Fatal("expected error for JWT without exp claim")
	}
}

func TestParseJWTExpiry_OpaqueToken(t *testing.T) {
	_, err := parseJWTExpiry("sa_live_abc123")
	if err == nil {
		t.Fatal("expected error for opaque token")
	}
}

func TestClient_Do_ValidateHeader(t *testing.T) {
	// X-Validate: strict must be stamped on every request so the server
	// rejects unknown JSON fields with a 400 instead of silently dropping
	// them. This is the CLI's primary defense against typo'd payloads from
	// agents (e.g. segment_ids vs filters, tag_id vs tags[].name).
	var got string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("X-Validate")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	c := New(Config{
		BaseURL:     server.URL,
		AccessToken: "test-jwt",
		RetryConfig: &RetryConfig{MaxRetries: 0, SleepFn: ContextSleep},
	})
	if _, err := c.Do(context.Background(), "GET", "/test", nil, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "strict" {
		t.Errorf("X-Validate: got %q, want %q", got, "strict")
	}
}

func TestClient_Do_UserAgentHeader(t *testing.T) {
	var got string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("User-Agent")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	c := New(Config{
		BaseURL:     server.URL,
		AccessToken: "test-jwt",
		RetryConfig: &RetryConfig{MaxRetries: 0, SleepFn: ContextSleep},
	})
	if _, err := c.Do(context.Background(), "GET", "/test", nil, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != useragent.Get() {
		t.Errorf("User-Agent: got %q, want %q", got, useragent.Get())
	}
}

func TestDoTrack_StandardHeaders(t *testing.T) {
	var gotUserAgent string
	var gotValidate string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUserAgent = r.Header.Get("User-Agent")
		gotValidate = r.Header.Get("X-Validate")
		_, _ = w.Write([]byte(`{"delivery_id":"abc"}`))
	}))
	defer server.Close()

	if _, err := DoTrack(context.Background(), TrackRequest{
		TrackBaseURL:        server.URL,
		Path:                "/v1/send/email",
		ServiceAccountToken: "sa_live_test",
		WorkspaceID:         "123",
		Body:                json.RawMessage(`{"to":"test@example.com"}`),
		Timeout:             time.Second,
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotUserAgent != useragent.Get() {
		t.Errorf("User-Agent: got %q, want %q", gotUserAgent, useragent.Get())
	}
	if gotValidate != "strict" {
		t.Errorf("X-Validate: got %q, want %q", gotValidate, "strict")
	}
}

func TestClient_Do_AgentHeader(t *testing.T) {
	cases := []struct {
		name     string
		envValue string
		envSet   bool
		want     string // expected X-CIO-Agent header value on the request
	}{
		{"env unset", "", false, ""},
		{"env empty", "", true, ""},
		{"env 1", "1", true, "1"},
		{"env other value", "true", true, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.envSet {
				t.Setenv("CIO_AGENT", tc.envValue)
			} else {
				// Ensure a stray env from the outer process doesn't leak in.
				t.Setenv("CIO_AGENT", "")
				_ = os.Unsetenv("CIO_AGENT")
			}

			var got string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got = r.Header.Get("X-CIO-Agent")
				_, _ = w.Write([]byte(`{"ok":true}`))
			}))
			defer server.Close()

			c := New(Config{
				BaseURL:     server.URL,
				AccessToken: "test-jwt",
				RetryConfig: &RetryConfig{MaxRetries: 0, SleepFn: ContextSleep},
			})
			if _, err := c.Do(context.Background(), "GET", "/test", nil, nil); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("X-CIO-Agent: got %q, want %q", got, tc.want)
			}
		})
	}
}
