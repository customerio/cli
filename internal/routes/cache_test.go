package routes

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/customerio/cli/internal/useragent"
)

func testSpec() string {
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

func testCDPSpec() string {
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

func specServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/openapi.json":
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("ETag", `"test-etag-journeys"`)
			w.Write([]byte(testSpec()))
		case "/cdp/api/openapi.json":
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("ETag", `"test-etag-cdp"`)
			w.Write([]byte(testCDPSpec()))
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestEnsureSpecs_FirstDownload(t *testing.T) {
	server := specServer(t)
	defer server.Close()

	cacheDir := t.TempDir()
	journeys, cdp, err := EnsureSpecs(context.Background(), LoadRegistryOptions{
		BaseURL:  server.URL,
		CacheDir: cacheDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(journeys) == 0 {
		t.Error("expected journeys data")
	}
	if len(cdp) == 0 {
		t.Error("expected cdp data")
	}

	// Verify cache files exist.
	if _, err := os.Stat(filepath.Join(cacheDir, "openapi.json")); err != nil {
		t.Errorf("cache file openapi.json not created: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cacheDir, "cdp_openapi.json")); err != nil {
		t.Errorf("cache file cdp_openapi.json not created: %v", err)
	}

	// Verify metadata.
	meta := readMeta(cacheDir)
	if meta.Specs["openapi"] == nil {
		t.Fatal("missing openapi metadata")
	}
	if meta.Specs["openapi"].ETag != `"test-etag-journeys"` {
		t.Errorf("expected etag, got %q", meta.Specs["openapi"].ETag)
	}
	if meta.Specs["cdp_openapi"] == nil {
		t.Fatal("missing cdp_openapi metadata")
	}
}

func TestEnsureSpecs_FreshCache(t *testing.T) {
	server := specServer(t)
	defer server.Close()

	cacheDir := t.TempDir()

	// First download to populate cache.
	_, _, err := EnsureSpecs(context.Background(), LoadRegistryOptions{
		BaseURL:  server.URL,
		CacheDir: cacheDir,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Track requests to verify no download on second call.
	var requests atomic.Int32
	server2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.NotFound(w, r)
	}))
	defer server2.Close()

	// Second call with fresh cache should not make any HTTP requests.
	journeys, cdp, err := EnsureSpecs(context.Background(), LoadRegistryOptions{
		BaseURL:  server2.URL,
		CacheDir: cacheDir,
		TTL:      1 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(journeys) == 0 || len(cdp) == 0 {
		t.Error("expected data from cache")
	}
	if requests.Load() != 0 {
		t.Errorf("expected 0 HTTP requests, got %d", requests.Load())
	}
}

func TestEnsureSpecs_StaleCache(t *testing.T) {
	server := specServer(t)
	defer server.Close()

	cacheDir := t.TempDir()

	// Populate cache.
	_, _, err := EnsureSpecs(context.Background(), LoadRegistryOptions{
		BaseURL:  server.URL,
		CacheDir: cacheDir,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Make cache stale by backdating fetched_at.
	meta := readMeta(cacheDir)
	for _, s := range meta.Specs {
		s.FetchedAt = time.Now().Add(-48 * time.Hour)
	}
	writeMeta(cacheDir, meta)

	// Track requests.
	var requests atomic.Int32
	server2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		switch r.URL.Path {
		case "/v1/openapi.json":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(testSpec()))
		case "/cdp/api/openapi.json":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(testCDPSpec()))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server2.Close()

	journeys, cdp, err := EnsureSpecs(context.Background(), LoadRegistryOptions{
		BaseURL:  server2.URL,
		CacheDir: cacheDir,
		TTL:      24 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(journeys) == 0 || len(cdp) == 0 {
		t.Error("expected data")
	}
	if requests.Load() != 2 {
		t.Errorf("expected 2 HTTP requests for stale cache, got %d", requests.Load())
	}
}

func TestEnsureSpecs_ETagConditionalRequest(t *testing.T) {
	var receivedETags []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		etag := r.Header.Get("If-None-Match")
		receivedETags = append(receivedETags, etag)

		if etag == `"test-etag"` {
			w.Header().Set("ETag", `"test-etag"`)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ETag", `"test-etag"`)
		switch r.URL.Path {
		case "/v1/openapi.json":
			w.Write([]byte(testSpec()))
		case "/cdp/api/openapi.json":
			w.Write([]byte(testCDPSpec()))
		}
	}))
	defer server.Close()

	cacheDir := t.TempDir()

	// First download — no ETag sent.
	_, _, err := EnsureSpecs(context.Background(), LoadRegistryOptions{
		BaseURL:  server.URL,
		CacheDir: cacheDir,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Expire cache.
	meta := readMeta(cacheDir)
	for _, s := range meta.Specs {
		s.FetchedAt = time.Now().Add(-48 * time.Hour)
	}
	writeMeta(cacheDir, meta)

	receivedETags = nil

	// Second request — should send If-None-Match and get 304.
	journeys, cdp, err := EnsureSpecs(context.Background(), LoadRegistryOptions{
		BaseURL:  server.URL,
		CacheDir: cacheDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(journeys) == 0 || len(cdp) == 0 {
		t.Error("expected data from cache after 304")
	}

	// Verify If-None-Match was sent.
	etagSent := false
	for _, e := range receivedETags {
		if e == `"test-etag"` {
			etagSent = true
			break
		}
	}
	if !etagSent {
		t.Errorf("expected If-None-Match with etag, got %v", receivedETags)
	}
}

func TestEnsureSpecs_DownloadFailsWithCache(t *testing.T) {
	server := specServer(t)
	defer server.Close()

	cacheDir := t.TempDir()

	// Populate cache.
	_, _, err := EnsureSpecs(context.Background(), LoadRegistryOptions{
		BaseURL:  server.URL,
		CacheDir: cacheDir,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Expire cache and point to a failing server.
	meta := readMeta(cacheDir)
	for _, s := range meta.Specs {
		s.FetchedAt = time.Now().Add(-48 * time.Hour)
	}
	writeMeta(cacheDir, meta)

	failServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer failServer.Close()

	// Should fall back to stale cache.
	journeys, cdp, err := EnsureSpecs(context.Background(), LoadRegistryOptions{
		BaseURL:  failServer.URL,
		CacheDir: cacheDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(journeys) == 0 || len(cdp) == 0 {
		t.Error("expected stale data from cache")
	}
}

func TestEnsureSpecs_ForceRefreshFailsHard(t *testing.T) {
	server := specServer(t)
	defer server.Close()

	cacheDir := t.TempDir()

	// Populate cache from a working server.
	_, _, err := EnsureSpecs(context.Background(), LoadRegistryOptions{
		BaseURL:  server.URL,
		CacheDir: cacheDir,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Expire cache and point to a failing server.
	meta := readMeta(cacheDir)
	for _, s := range meta.Specs {
		s.FetchedAt = time.Now().Add(-48 * time.Hour)
	}
	writeMeta(cacheDir, meta)

	failServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer failServer.Close()

	// With ForceRefresh, should NOT fall back to stale cache — should error.
	_, _, err = EnsureSpecs(context.Background(), LoadRegistryOptions{
		BaseURL:      failServer.URL,
		CacheDir:     cacheDir,
		ForceRefresh: true,
	})
	if err == nil {
		t.Error("expected error when force refresh fails, not stale cache fallback")
	}
}

func TestEnsureSpecs_DownloadFailsNoCache(t *testing.T) {
	failServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer failServer.Close()

	_, _, err := EnsureSpecs(context.Background(), LoadRegistryOptions{
		BaseURL:  failServer.URL,
		CacheDir: t.TempDir(),
	})
	if err == nil {
		t.Error("expected error when download fails with no cache")
	}
}

func TestEnsureSpecs_ForceRefresh(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/openapi.json":
			w.Write([]byte(testSpec()))
		case "/cdp/api/openapi.json":
			w.Write([]byte(testCDPSpec()))
		}
	}))
	defer server.Close()

	cacheDir := t.TempDir()

	// Populate cache.
	_, _, err := EnsureSpecs(context.Background(), LoadRegistryOptions{
		BaseURL:  server.URL,
		CacheDir: cacheDir,
	})
	if err != nil {
		t.Fatal(err)
	}

	requests.Store(0)

	// ForceRefresh should re-download even though cache is fresh.
	_, _, err = EnsureSpecs(context.Background(), LoadRegistryOptions{
		BaseURL:      server.URL,
		CacheDir:     cacheDir,
		ForceRefresh: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 2 {
		t.Errorf("expected 2 requests on force refresh, got %d", requests.Load())
	}
}

func TestEnsureSpecs_AuthenticatedBearerToken(t *testing.T) {
	var receivedAuth []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = append(receivedAuth, r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/openapi.json":
			w.Write([]byte(testSpec()))
		case "/cdp/api/openapi.json":
			w.Write([]byte(testCDPSpec()))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	cacheDir := t.TempDir()
	token := "test-service-account-token-a"

	_, _, err := EnsureSpecs(context.Background(), LoadRegistryOptions{
		BaseURL:     server.URL,
		CacheDir:    cacheDir,
		AccessToken: token,
		CacheKey:    token,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Every request should have carried the Bearer token.
	for i, auth := range receivedAuth {
		if auth != "Bearer "+token {
			t.Errorf("request %d: expected Authorization 'Bearer %s', got %q", i, token, auth)
		}
	}
}

func TestEnsureSpecs_AuthenticatedCacheIsolation(t *testing.T) {
	server := specServer(t)
	defer server.Close()

	cacheDir := t.TempDir()

	// Download unauthenticated.
	_, _, err := EnsureSpecs(context.Background(), LoadRegistryOptions{
		BaseURL:  server.URL,
		CacheDir: cacheDir,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Download authenticated with a token.
	token := "test-service-account-token-a"
	_, _, err = EnsureSpecs(context.Background(), LoadRegistryOptions{
		BaseURL:     server.URL,
		CacheDir:    cacheDir,
		AccessToken: token,
		CacheKey:    token,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Unauthenticated specs should be in the base dir.
	if _, err := os.Stat(filepath.Join(cacheDir, "openapi.json")); err != nil {
		t.Errorf("unauthenticated cache missing: %v", err)
	}

	// Authenticated specs should be in a token-specific subdir.
	authDir := filepath.Join(cacheDir, tokenCacheKey(token))
	if _, err := os.Stat(filepath.Join(authDir, "openapi.json")); err != nil {
		t.Errorf("authenticated cache missing: %v", err)
	}

	// Different token should use a different subdir.
	token2 := "test-service-account-token-b"
	if tokenCacheKey(token) == tokenCacheKey(token2) {
		t.Error("different tokens should produce different cache keys")
	}
}

func TestEnsureSpecs_UnauthenticatedNoAuthHeader(t *testing.T) {
	var receivedAuth []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = append(receivedAuth, r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/openapi.json":
			w.Write([]byte(testSpec()))
		case "/cdp/api/openapi.json":
			w.Write([]byte(testCDPSpec()))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	_, _, err := EnsureSpecs(context.Background(), LoadRegistryOptions{
		BaseURL:  server.URL,
		CacheDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}

	for i, auth := range receivedAuth {
		if auth != "" {
			t.Errorf("request %d: expected no Authorization header, got %q", i, auth)
		}
	}
}

func TestEnsureSpecs_UserAgentHeader(t *testing.T) {
	var received []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received = append(received, r.Header.Get("User-Agent"))
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/openapi.json":
			w.Write([]byte(testSpec()))
		case "/cdp/api/openapi.json":
			w.Write([]byte(testCDPSpec()))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	_, _, err := EnsureSpecs(context.Background(), LoadRegistryOptions{
		BaseURL:  server.URL,
		CacheDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(received) != len(defaultSpecSources) {
		t.Fatalf("got %d requests, want %d", len(received), len(defaultSpecSources))
	}
	for i, got := range received {
		if got != useragent.Get() {
			t.Errorf("request %d User-Agent: got %q, want %q", i, got, useragent.Get())
		}
	}
}

func TestEnsureSpecs_ConcurrentAccess(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/openapi.json":
			w.Write([]byte(testSpec()))
		case "/cdp/api/openapi.json":
			w.Write([]byte(testCDPSpec()))
		}
	}))
	defer server.Close()

	cacheDir := t.TempDir()
	opts := LoadRegistryOptions{
		BaseURL:  server.URL,
		CacheDir: cacheDir,
	}

	// Run multiple concurrent EnsureSpecs calls.
	errs := make(chan error, 5)
	for i := 0; i < 5; i++ {
		go func() {
			_, _, err := EnsureSpecs(context.Background(), opts)
			errs <- err
		}()
	}

	for i := 0; i < 5; i++ {
		if err := <-errs; err != nil {
			t.Errorf("concurrent EnsureSpecs failed: %v", err)
		}
	}

	// Verify meta.json is valid after concurrent writes.
	meta := readMeta(cacheDir)
	if meta.Specs["openapi"] == nil {
		t.Error("missing openapi metadata after concurrent access")
	}
	if meta.Specs["cdp_openapi"] == nil {
		t.Error("missing cdp_openapi metadata after concurrent access")
	}
}
