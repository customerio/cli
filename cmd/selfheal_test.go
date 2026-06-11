package cmd

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/customerio/cli/internal/client"
)

// newClientFor builds a client pointed at srv with a pre-set access token so
// Do() skips the OAuth exchange (the bogus JWT gets a far-future expiry).
func newClientFor(url, saToken string) *client.Client {
	return client.New(client.Config{
		BaseURL:             url,
		ServiceAccountToken: saToken,
		AccessToken:         "test-jwt",
	})
}

func TestMaybePromoteSandboxToken_PromotesWhenLive(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	var gotPath, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"token":"sa_live_promoted999","id":1,"name":"live-bootstrap"}`))
	}))
	defer srv.Close()

	saToken := "sa_sandbox_bootstrap123"
	if err := client.WriteCredentials(&client.Credentials{
		ServiceAccountToken: saToken,
		AccountID:           "42",
		Region:              "us",
	}); err != nil {
		t.Fatal(err)
	}

	c := newClientFor(srv.URL, saToken)
	maybePromoteSandboxToken(context.Background(), c, saToken, false)

	if gotMethod != http.MethodPost || gotPath != "/v1/accounts/42/promote_sandbox_token" {
		t.Fatalf("unexpected request: %s %s", gotMethod, gotPath)
	}
	if c.ServiceAccountToken() != "sa_live_promoted999" {
		t.Fatalf("in-memory client token not swapped: %q", c.ServiceAccountToken())
	}
	creds, err := client.ReadCredentials()
	if err != nil {
		t.Fatal(err)
	}
	if creds.ServiceAccountToken != "sa_live_promoted999" {
		t.Fatalf("stored token not swapped: %q", creds.ServiceAccountToken)
	}
	if creds.AccessToken != "" {
		t.Fatal("cached access token should be cleared after promotion")
	}
}

func TestMaybePromoteSandboxToken_Throttled(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer srv.Close()

	saToken := "sa_sandbox_x"
	if err := client.WriteCredentials(&client.Credentials{
		ServiceAccountToken:     saToken,
		AccountID:               "1",
		SandboxPromoteCheckedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	maybePromoteSandboxToken(context.Background(), newClientFor(srv.URL, saToken), saToken, false)
	if called {
		t.Fatal("should not probe promote within the throttle window")
	}
}

func TestMaybePromoteSandboxToken_SkipsLiveAndReadOnly(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer srv.Close()

	// Live token: nothing to promote.
	maybePromoteSandboxToken(context.Background(), newClientFor(srv.URL, "sa_live_x"), "sa_live_x", false)
	// Sandbox token but read-only: the POST would be blocked, so skip.
	maybePromoteSandboxToken(context.Background(), newClientFor(srv.URL, "sa_sandbox_x"), "sa_sandbox_x", true)
	if called {
		t.Fatal("should not call promote for a live token or in read-only mode")
	}
}

func TestMaybePromoteSandboxToken_SwallowsStillSandbox(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"still in sandbox"}]}`))
	}))
	defer srv.Close()

	saToken := "sa_sandbox_y"
	if err := client.WriteCredentials(&client.Credentials{
		ServiceAccountToken: saToken,
		AccountID:           "7",
	}); err != nil {
		t.Fatal(err)
	}

	maybePromoteSandboxToken(context.Background(), newClientFor(srv.URL, saToken), saToken, false)

	creds, err := client.ReadCredentials()
	if err != nil {
		t.Fatal(err)
	}
	if creds.ServiceAccountToken != saToken {
		t.Fatalf("token must be unchanged on 403, got %q", creds.ServiceAccountToken)
	}
	if creds.SandboxPromoteCheckedAt.IsZero() {
		t.Fatal("throttle timestamp should be recorded after a failed attempt")
	}
}
