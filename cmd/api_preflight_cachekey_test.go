package cmd

import (
	"encoding/base64"
	"encoding/json"
	"testing"
)

// unsignedJWT builds a syntactically valid JWT with the given payload and a
// placeholder signature. The CLI never verifies signatures — it forwards the
// token and reads claims as hints — so the test server accepts it as-is.
func unsignedJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"EdDSA","typ":"JWT"}`))
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}
	return header + "." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}

// farFuture keeps the fixture tokens unexpired so the client never tries to
// refresh one.
const farFuture = 4102444800 // 2100-01-01T00:00:00Z

// A caller authenticating with a pre-exchanged JWT gets a freshly signed token
// from its issuer on every command: same session, new iat, different bytes.
// The cache written by one command must be found by the next, or the path
// check never has a spec to judge against.
func TestAPIPreflight_CacheSurvivesTokenResign(t *testing.T) {
	server := newPreflightServer(t)
	t.Setenv("CIO_TOKEN", "")

	// First command: warm the cache under this session's token.
	t.Setenv("CIO_ACCESS_TOKEN", unsignedJWT(t, map[string]any{
		"jti": "session-A", "sub": "sa:1", "iat": 1700000000, "exp": farFuture,
	}))
	if _, _, err := executeCommand("schema", "--api-url", server.URL); err != nil {
		t.Fatalf("warming the cache via schema: %v", err)
	}
	server.forget()

	// Second command: same session, re-signed one second later.
	t.Setenv("CIO_ACCESS_TOKEN", unsignedJWT(t, map[string]any{
		"jti": "session-A", "sub": "sa:1", "iat": 1700000001, "exp": farFuture,
	}))
	_, _, err := executeCommand("api", "/v1/environments/456/campaigns/48/actions",
		"--api-url", server.URL)
	if err == nil {
		t.Fatal("expected the re-signed token to find the cache and reject the invented path")
	}
	if got := server.requested(); len(got) != 0 {
		t.Errorf("no API request should have been made, got %v", got)
	}
	if got := server.specFetches(); len(got) != 0 {
		t.Errorf("the second command must reuse the cache, not fetch; got %v", got)
	}
}

// The same keying serves `cio schema`: a second call under a re-signed token
// must read the spec it cached moments ago, not download it again.
func TestSchema_ReusesCacheAcrossTokenResign(t *testing.T) {
	server := newPreflightServer(t)
	t.Setenv("CIO_TOKEN", "")

	t.Setenv("CIO_ACCESS_TOKEN", unsignedJWT(t, map[string]any{
		"jti": "session-A", "sub": "sa:1", "iat": 1700000000, "exp": farFuture,
	}))
	if _, _, err := executeCommand("schema", "--api-url", server.URL); err != nil {
		t.Fatalf("first schema: %v", err)
	}
	if got := server.specFetches(); len(got) != 2 {
		t.Fatalf("first schema should download both specs, got %v", got)
	}
	server.forget()

	t.Setenv("CIO_ACCESS_TOKEN", unsignedJWT(t, map[string]any{
		"jti": "session-A", "sub": "sa:1", "iat": 1700000001, "exp": farFuture,
	}))
	if _, _, err := executeCommand("schema", "--api-url", server.URL); err != nil {
		t.Fatalf("second schema: %v", err)
	}
	if got := server.specFetches(); len(got) != 0 {
		t.Errorf("second schema under a re-signed token must reuse the cache, got fetches %v", got)
	}
}

// Partitioning by session must still hold: a different session does not see
// another session's cache, because the served spec may differ per identity.
func TestAPIPreflight_CacheIsolatedPerSession(t *testing.T) {
	server := newPreflightServer(t)
	t.Setenv("CIO_TOKEN", "")

	t.Setenv("CIO_ACCESS_TOKEN", unsignedJWT(t, map[string]any{
		"jti": "session-A", "sub": "sa:1", "iat": 1700000000, "exp": farFuture,
	}))
	if _, _, err := executeCommand("schema", "--api-url", server.URL); err != nil {
		t.Fatalf("warming the cache via schema: %v", err)
	}
	server.forget()

	// A different session: its cache is cold, so it must fetch its own spec —
	// never read session A's, which may be filtered for a different plan. The
	// tell is the download: A's spec is already on disk, and B fetches anyway.
	t.Setenv("CIO_ACCESS_TOKEN", unsignedJWT(t, map[string]any{
		"jti": "session-B", "sub": "sa:2", "iat": 1700000001, "exp": farFuture,
	}))
	if _, _, err := executeCommand("api", "/v1/environments/456/campaigns/48/actions",
		"--api-url", server.URL); err == nil {
		t.Fatal("expected session B's own freshly fetched spec to reject the invented path")
	}
	if got := server.specFetches(); len(got) != 2 {
		t.Errorf("session B must download its own spec rather than read session A's, got fetches %v", got)
	}
	if got := server.requested(); len(got) != 0 {
		t.Errorf("no API request should have been made, got %v", got)
	}
}

// A token that is not a JWT, or a JWT without jti, keys on its raw bytes —
// the behaviour every caller had before, unchanged.
func TestSpecCacheKey_FallsBackToRawToken(t *testing.T) {
	for _, token := range []string{
		"sa_live_abc123",
		"opaque-token",
		unsignedJWT(t, map[string]any{"sub": "sa:1", "exp": farFuture}), // JWT, no jti
	} {
		if got := specCacheKey(token); got != token {
			t.Errorf("specCacheKey(%q) = %q, want the raw token", token, got)
		}
	}
}

func TestSpecCacheKey_UsesJTIAcrossResigns(t *testing.T) {
	a := specCacheKey(unsignedJWT(t, map[string]any{"jti": "session-A", "iat": 1, "exp": farFuture}))
	b := specCacheKey(unsignedJWT(t, map[string]any{"jti": "session-A", "iat": 2, "exp": farFuture}))
	c := specCacheKey(unsignedJWT(t, map[string]any{"jti": "session-B", "iat": 2, "exp": farFuture}))

	if a != "session-A" || a != b {
		t.Errorf("same session must yield the same key across re-signs; got %q and %q", a, b)
	}
	if c == a {
		t.Errorf("different sessions must yield different keys; both %q", a)
	}
}
