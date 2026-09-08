package cmd

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// A spec host that never answers must cost the caller the fetch budget at most,
// and then the request must go out as if there were no check at all.
func TestAPIPreflight_StalledSpecHostFailsOpenWithinBudget(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CIO_TOKEN", "sa_live_test123")
	t.Setenv("CIO_ACCESS_TOKEN", "")

	release := make(chan struct{})

	var mu sync.Mutex
	var apiPaths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/service_accounts/oauth/token":
			_, _ = w.Write([]byte(`{"access_token":"jwt-test-session","token_type":"Bearer","expires_in":3600}`))
		case "/v1/openapi.json", "/cdp/api/openapi.json":
			// Hold the response until the test ends; the client must give up first.
			select {
			case <-release:
			case <-r.Context().Done():
			}
		default:
			mu.Lock()
			apiPaths = append(apiPaths, r.URL.Path)
			mu.Unlock()
			_, _ = w.Write([]byte(`{"ok":true}`))
		}
	}))
	// Deferred LIFO: release the stalled handler before Close waits on it.
	defer server.Close()
	defer close(release)

	start := time.Now()
	_, _, err := executeCommand("api", "/v1/environments/456/campaigns/48/actions", "--api-url", server.URL)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("a stalled spec host must not fail the call, got: %v", err)
	}
	mu.Lock()
	got := append([]string(nil), apiPaths...)
	mu.Unlock()
	if len(got) != 1 {
		t.Errorf("expected the request to be sent after giving up on the spec, got %v", got)
	}
	// Anything near the 30s HTTP client timeout means the deadline was not
	// honoured. Generous headroom over the budget keeps this off the flake list.
	if elapsed > preflightSpecFetchBudget+5*time.Second {
		t.Errorf("call took %v; the spec fetch must be bounded by the %v budget", elapsed, preflightSpecFetchBudget)
	}
}

// A service-account token that cannot be exchanged must not make the check
// fetch anonymously: that spec is the wrong one to judge the identity against,
// and it would be written to a partition the identity's own reads never open.
// The check steps aside and the request proceeds to fail — or not — on its own.
func TestAPIPreflight_UnexchangeableTokenSkipsCheckWithoutAnonymousFetch(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CIO_TOKEN", "sa_live_revoked")
	t.Setenv("CIO_ACCESS_TOKEN", "")

	var mu sync.Mutex
	var specFetches, apiPaths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/service_accounts/oauth/token":
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"invalid_client"}`))
		case "/v1/openapi.json", "/cdp/api/openapi.json":
			mu.Lock()
			specFetches = append(specFetches, r.URL.Path)
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(preflightSpec()))
		default:
			mu.Lock()
			apiPaths = append(apiPaths, r.URL.Path)
			mu.Unlock()
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
		}
	}))
	defer server.Close()

	// The call fails on auth, as it should — at the token exchange, which the
	// client performs before it can send anything — and not on the check.
	_, _, err := executeCommand("api", "/v1/environments/456/campaigns/48/actions", "--api-url", server.URL)
	if err == nil {
		t.Fatal("expected the call to fail authentication")
	}
	if strings.Contains(err.Error(), "not an endpoint in the API spec") {
		t.Fatalf("the check must step aside when the token cannot be exchanged, got: %v", err)
	}
	if !strings.Contains(err.Error(), "token exchange failed") {
		t.Fatalf("expected the failure to be the token exchange, got: %v", err)
	}

	mu.Lock()
	fetched := append([]string(nil), specFetches...)
	sent := append([]string(nil), apiPaths...)
	mu.Unlock()
	if len(fetched) != 0 {
		t.Errorf("must not fetch a spec anonymously, got %v", fetched)
	}
	// No Bearer could be minted, so nothing reaches the API; documented here so
	// a future client that sends anyway shows up as a deliberate change.
	if len(sent) != 0 {
		t.Errorf("expected no API request without a token, got %v", sent)
	}
	if _, statErr := os.Stat(filepath.Join(home, ".cio", "cache", "specs", "openapi.json")); statErr == nil {
		t.Error("an anonymous spec must not be written to the shared base partition")
	}
}

// The budget covers the token exchange too. A token endpoint that stalls on
// the check's exchange must cost at most the budget; the check then steps
// aside, and the request's own exchange (which is answered) goes through.
func TestAPIPreflight_StalledTokenExchangeIsBoundedByBudget(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CIO_TOKEN", "sa_live_test123")
	t.Setenv("CIO_ACCESS_TOKEN", "")

	// Deferred LIFO: the server must not be closed while its stalled handler
	// still waits, so release is deferred after Close and therefore runs first.
	release := make(chan struct{})

	var mu sync.Mutex
	var exchanges int
	var specFetches, apiPaths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/service_accounts/oauth/token":
			mu.Lock()
			exchanges++
			first := exchanges == 1
			mu.Unlock()
			if first {
				// The check's exchange: never answered.
				select {
				case <-release:
				case <-r.Context().Done():
				}
				return
			}
			_, _ = w.Write([]byte(`{"access_token":"jwt-test-session","token_type":"Bearer","expires_in":3600}`))
		case "/v1/openapi.json", "/cdp/api/openapi.json":
			mu.Lock()
			specFetches = append(specFetches, r.URL.Path)
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(preflightSpec()))
		default:
			mu.Lock()
			apiPaths = append(apiPaths, r.URL.Path)
			mu.Unlock()
			_, _ = w.Write([]byte(`{"ok":true}`))
		}
	}))
	defer server.Close()
	defer close(release)

	start := time.Now()
	_, _, err := executeCommand("api", "/v1/environments/456/campaigns/48/actions", "--api-url", server.URL)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("a stalled exchange on the check must not fail the call, got: %v", err)
	}
	mu.Lock()
	fetched := append([]string(nil), specFetches...)
	sent := append([]string(nil), apiPaths...)
	mu.Unlock()
	if len(fetched) != 0 {
		t.Errorf("without a token the check must not fetch, got %v", fetched)
	}
	if len(sent) != 1 {
		t.Errorf("expected the request to be sent once the check stepped aside, got %v", sent)
	}
	// Without the budget on the exchange this would sit for the 30s client
	// timeout before the check even gave up.
	if elapsed > preflightSpecFetchBudget+5*time.Second {
		t.Errorf("call took %v; the exchange must be bounded by the %v budget", elapsed, preflightSpecFetchBudget)
	}
}
