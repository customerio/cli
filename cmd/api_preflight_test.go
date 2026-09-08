package cmd

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// preflightSpec is a Journeys OpenAPI fixture with enough of the campaigns
// resource to exercise the pre-send path check: a collection with two verbs and
// a single-campaign read.
func preflightSpec() string {
	return `{
		"openapi": "3.1.0",
		"info": {"title": "Test", "version": "1.0.0"},
		"paths": {
			"/v1/environments/{environment_id}/campaigns": {
				"get": {"summary": "List campaigns"},
				"post": {"summary": "Create campaign"}
			},
			"/v1/environments/{environment_id}/campaigns/{campaign_id}": {
				"get": {"summary": "Get campaign"}
			},
			"/v1/environments/{environment_id}/campaigns/{campaign_id}/duplicate": {
				"post": {"summary": "Duplicate campaign"}
			}
		}
	}`
}

// preflightServer serves the spec and echoes anything else, recording every
// path it was asked for — API paths so a test can assert a request never left
// the CLI, spec paths so a test can assert the check itself fetches nothing.
type preflightServer struct {
	*httptest.Server
	mu        sync.Mutex
	apiPaths  []string
	specPaths []string
}

func (p *preflightServer) requested() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.apiPaths...)
}

func (p *preflightServer) specFetches() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.specPaths...)
}

func (p *preflightServer) forget() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.apiPaths = nil
	p.specPaths = nil
}

// newPreflightServer starts the server without priming the spec cache, for
// tests that care about a cold cache.
func newPreflightServer(t *testing.T) *preflightServer {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CIO_TOKEN", "sa_live_test123")
	t.Setenv("CIO_ACCESS_TOKEN", "")

	p := &preflightServer{}
	p.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/service_accounts/oauth/token":
			_, _ = w.Write([]byte(`{"access_token":"jwt-test-session","token_type":"Bearer","expires_in":3600}`))
			return
		case "/v1/openapi.json", "/cdp/api/openapi.json":
			p.mu.Lock()
			p.specPaths = append(p.specPaths, r.URL.Path)
			p.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			if r.URL.Path == "/v1/openapi.json" {
				_, _ = w.Write([]byte(preflightSpec()))
			} else {
				_, _ = w.Write([]byte(`{"openapi":"3.1.0","info":{"title":"CDP","version":"1.0.0"},"paths":{}}`))
			}
			return
		}

		p.mu.Lock()
		p.apiPaths = append(p.apiPaths, r.URL.Path)
		p.mu.Unlock()
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(p.Close)
	return p
}

// setupPreflightTest primes the spec cache the way a session does — the check
// reads the cache and never downloads — then forgets the setup traffic.
func setupPreflightTest(t *testing.T) *preflightServer {
	t.Helper()
	p := newPreflightServer(t)
	if _, _, err := executeCommand("schema", "--api-url", p.URL); err != nil {
		t.Fatalf("priming the spec cache via schema: %v", err)
	}
	p.forget()
	return p
}

func TestAPIPreflight_BlocksUnknownSubPathWithoutSending(t *testing.T) {
	server := setupPreflightTest(t)

	_, _, err := executeCommand("api", "/v1/environments/456/campaigns/48/actions",
		"--api-url", server.URL)
	if err == nil {
		t.Fatal("expected an invented sub-path to be rejected before sending")
	}
	if !strings.Contains(err.Error(), "is not an endpoint in the API spec") {
		t.Errorf("error should name the cause, got: %s", err.Error())
	}
	if !strings.Contains(err.Error(), "request was not sent") {
		t.Errorf("error should say the request was not sent, got: %s", err.Error())
	}
	// The prescription matters as much as the diagnosis: a caller that only
	// learns "that failed" tries a variant instead of reading the schema.
	if !strings.Contains(err.Error(), "cio schema campaigns") {
		t.Errorf("error should point at the resource's schema, got: %s", err.Error())
	}
	if got := server.requested(); len(got) != 0 {
		t.Errorf("no API request should have been made, got %v", got)
	}
}

func TestAPIPreflight_AllowsKnownPaths(t *testing.T) {
	server := setupPreflightTest(t)

	for _, path := range []string{
		"/v1/environments/456/campaigns",
		"/v1/environments/456/campaigns/48",
	} {
		if _, _, err := executeCommand("api", path, "--api-url", server.URL); err != nil {
			t.Fatalf("expected %s to be allowed, got: %v", path, err)
		}
	}

	if got := server.requested(); len(got) != 2 {
		t.Errorf("expected both requests to reach the API, got %v", got)
	}
}

// The template form must pass the check too: --params fills it in before the
// path is judged.
func TestAPIPreflight_AllowsTemplateFormWithParams(t *testing.T) {
	server := setupPreflightTest(t)

	_, _, err := executeCommand("api", "/v1/environments/{environment_id}/campaigns",
		"--api-url", server.URL,
		"--params", `{"environment_id": "456"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := server.requested(); len(got) != 1 || got[0] != "/v1/environments/456/campaigns" {
		t.Errorf("expected the resolved path to be requested, got %v", got)
	}
}

func TestAPIPreflight_ReportsAllowedMethodsForWrongVerb(t *testing.T) {
	server := setupPreflightTest(t)

	_, _, err := executeCommand("api", "/v1/environments/456/campaigns",
		"--api-url", server.URL,
		"-X", "DELETE")
	if err == nil {
		t.Fatal("expected DELETE on a GET/POST collection to be rejected")
	}
	if !strings.Contains(err.Error(), "DELETE is not allowed") {
		t.Errorf("error should name the rejected verb, got: %s", err.Error())
	}
	if !strings.Contains(err.Error(), "GET, POST") {
		t.Errorf("error should list the accepted verbs, got: %s", err.Error())
	}
	if got := server.requested(); len(got) != 0 {
		t.Errorf("no API request should have been made, got %v", got)
	}
}

// The method is usually implicit — GET unless a body is passed — so a route
// that exists for one other verb must name the flag that fixes the call.
func TestAPIPreflight_NamesTheMethodFlagForASingleAllowedVerb(t *testing.T) {
	server := setupPreflightTest(t)

	_, _, err := executeCommand("api", "/v1/environments/456/campaigns/48/duplicate",
		"--api-url", server.URL)
	if err == nil {
		t.Fatal("expected an implicit GET on a POST-only route to be rejected")
	}
	if !strings.Contains(err.Error(), "retry with -X POST") {
		t.Errorf("error should name the flag that fixes it, got: %s", err.Error())
	}
	if got := server.requested(); len(got) != 0 {
		t.Errorf("no API request should have been made, got %v", got)
	}

	// And with the method set, the same path goes through.
	if _, _, err := executeCommand("api", "/v1/environments/456/campaigns/48/duplicate",
		"--api-url", server.URL, "-X", "post"); err != nil {
		t.Fatalf("expected -X post (case-insensitive) to be allowed, got: %v", err)
	}
	if got := server.requested(); len(got) != 1 {
		t.Errorf("expected the POST to reach the API, got %v", got)
	}
}

func TestAPIPreflight_NoPreflightFlagSendsAnyway(t *testing.T) {
	server := setupPreflightTest(t)

	_, _, err := executeCommand("api", "/v1/environments/456/campaigns/48/actions",
		"--api-url", server.URL,
		"--no-preflight")
	if err != nil {
		t.Fatalf("--no-preflight should bypass the check, got: %v", err)
	}
	if got := server.requested(); len(got) != 1 || got[0] != "/v1/environments/456/campaigns/48/actions" {
		t.Errorf("expected the request to be sent unchecked, got %v", got)
	}
}

// A dry run must not report a path as valid when the spec does not describe it.
func TestAPIPreflight_DryRunRejectsUnknownPath(t *testing.T) {
	server := setupPreflightTest(t)

	stdout, _, err := executeCommand("api", "/v1/environments/456/campaigns/48/actions",
		"--api-url", server.URL,
		"--dry-run")
	if err == nil {
		t.Fatal("expected a dry run to reject an unknown path")
	}
	if strings.Contains(stdout, `"valid": true`) {
		t.Errorf("dry run must not claim an unknown path is valid, got: %s", stdout)
	}
}

// Paths too shallow to sit inside a documented scope cannot be judged absent.
// "/v1/login" is the case that matters: it is a real endpoint the spec omits,
// so a check that policed everything under "/v1" would block a working call.
func TestAPIPreflight_AllowsPathsOutsideSpecScopes(t *testing.T) {
	for _, path := range []string{"/health", "/v1/login", "/v2/environments/456/campaigns"} {
		t.Run(path, func(t *testing.T) {
			server := setupPreflightTest(t)

			if _, _, err := executeCommand("api", path, "--api-url", server.URL); err != nil {
				t.Fatalf("%s must not be blocked, got: %v", path, err)
			}
			if got := server.requested(); len(got) != 1 || got[0] != path {
				t.Errorf("expected %s to be requested, got %v", path, got)
			}
		})
	}
}

// A resource invented alongside real ones is inside a documented scope, so it
// is judged — this is the other half of the observed failure mode.
func TestAPIPreflight_BlocksInventedSiblingResource(t *testing.T) {
	server := setupPreflightTest(t)

	_, _, err := executeCommand("api", "/v1/environments/456/attribute_names",
		"--api-url", server.URL)
	if err == nil {
		t.Fatal("expected an invented sibling resource to be rejected")
	}
	if got := server.requested(); len(got) != 0 {
		t.Errorf("no API request should have been made, got %v", got)
	}
}

// The check reads the spec cache and must never fetch: EnsureSpecs takes an
// uninterruptible cache lock and can sit through two spec downloads, which is
// not something a per-request check may put in the caller's way.
func TestAPIPreflight_NeverFetchesTheSpecItself(t *testing.T) {
	server := setupPreflightTest(t)

	// Rejects from the primed cache, with no spec request of its own.
	if _, _, err := executeCommand("api", "/v1/environments/456/campaigns/48/actions",
		"--api-url", server.URL); err == nil {
		t.Fatal("expected the cached spec to reject an invented sub-path")
	}
	if got := server.specFetches(); len(got) != 0 {
		t.Errorf("the check must not download specs, got %v", got)
	}
}

// A cold cache is filled, once, so the check can judge — and the call it was
// asked about is judged against what was just fetched.
func TestAPIPreflight_ColdCacheFetchesOnceThenJudges(t *testing.T) {
	server := newPreflightServer(t)

	_, _, err := executeCommand("api", "/v1/environments/456/campaigns/48/actions",
		"--api-url", server.URL)
	if err == nil {
		t.Fatal("expected the freshly fetched spec to reject the invented path")
	}
	if got := server.requested(); len(got) != 0 {
		t.Errorf("no API request should have been made, got %v", got)
	}
	if got := server.specFetches(); len(got) != 2 {
		t.Errorf("expected exactly one download of each spec, got %v", got)
	}

	// Warm now: the next check reads the cache and fetches nothing.
	server.forget()
	if _, _, err := executeCommand("api", "/v1/environments/456/campaigns/48/actions",
		"--api-url", server.URL); err == nil {
		t.Fatal("expected the cached spec to reject the invented path")
	}
	if got := server.specFetches(); len(got) != 0 {
		t.Errorf("a warm cache must not fetch, got %v", got)
	}
}

// When the spec cannot be loaded the check has nothing to judge against, and
// must not stand between the caller and an endpoint that does exist.
func TestAPIPreflight_FailsOpenWhenSpecUnavailable(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CIO_TOKEN", "sa_live_test123")
	t.Setenv("CIO_ACCESS_TOKEN", "")

	var mu sync.Mutex
	var apiPaths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/service_accounts/oauth/token":
			_, _ = w.Write([]byte(`{"access_token":"jwt-test-session","token_type":"Bearer","expires_in":3600}`))
		case "/v1/openapi.json", "/cdp/api/openapi.json":
			http.Error(w, "spec unavailable", http.StatusInternalServerError)
		default:
			mu.Lock()
			apiPaths = append(apiPaths, r.URL.Path)
			mu.Unlock()
			_, _ = w.Write([]byte(`{"ok":true}`))
		}
	}))
	defer server.Close()

	if _, _, err := executeCommand("api", "/v1/environments/456/campaigns", "--api-url", server.URL); err != nil {
		t.Fatalf("expected the call to proceed without a spec, got: %v", err)
	}

	mu.Lock()
	got := append([]string(nil), apiPaths...)
	mu.Unlock()
	if len(got) != 1 || got[0] != "/v1/environments/456/campaigns" {
		t.Errorf("expected the request to be sent, got %v", got)
	}
}
