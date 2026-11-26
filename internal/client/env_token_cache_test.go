package client

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestEnvTokenCache(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CIO_TOKEN", "sa_live_env")
	if err := WriteCredentials(&Credentials{ServiceAccountToken: "sa_live_env", AccessToken: "saved-jwt", AccessTokenExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	configPath, _ := configFilePath()
	before, _ := os.ReadFile(configPath)
	var exchanges atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := exchanges.Add(1)
		if n > 5 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		fmt.Fprint(w, `{"access_token":"env-jwt","expires_in":3600}`)
	}))
	defer server.Close()
	for range 6 {
		c := New(Config{BaseURL: server.URL, ServiceAccountToken: ResolveServiceAccountToken("")})
		if token, err := c.EnsureAccessToken(context.Background()); err != nil || token != "env-jwt" {
			t.Fatalf("token=%q err=%v", token, err)
		}
	}
	if got := exchanges.Load(); got != 1 {
		t.Fatalf("expected one exchange across clients, got %d", got)
	}
	after, _ := os.ReadFile(configPath)
	if string(before) != string(after) {
		t.Fatal("environment-token use changed stored credentials")
	}
	files, _ := filepath.Glob(filepath.Join(os.Getenv("HOME"), ".cio", "cache", "tokens", "*.json"))
	if len(files) != 1 || strings.Contains(files[0], "sa_live_env") {
		t.Fatalf("expected one cache file with no raw credential in its name: %v", files)
	}
	info, err := os.Stat(files[0])
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("expected private cache file: %v, %v", info, err)
	}
	if err := os.WriteFile(files[0], []byte("corrupt cache"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(Config{BaseURL: server.URL, ServiceAccountToken: "sa_live_env"}).EnsureAccessToken(context.Background()); err != nil {
		t.Fatal(err)
	}
	if exchanges.Load() != 2 {
		t.Fatal("corrupt cache did not trigger a fresh exchange")
	}
	dir := filepath.Dir(files[0])
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir, []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(Config{BaseURL: server.URL, ServiceAccountToken: "sa_live_env"}).EnsureAccessToken(context.Background()); err != nil {
		t.Fatalf("unavailable cache blocked authentication: %v", err)
	}
	after, _ = os.ReadFile(configPath)
	if string(before) != string(after) {
		t.Fatal("unavailable cache changed stored credentials")
	}
}

func TestEnvTokenCacheIsolation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var exchanges atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		fmt.Fprintf(w, `{"access_token":"jwt-%d","expires_in":3600,"scope":%q}`, exchanges.Add(1), r.Form.Get("scope"))
	}))
	defer server.Close()
	configs := []Config{
		{BaseURL: server.URL, ServiceAccountToken: "sa_live_first"},
		{BaseURL: server.URL, ServiceAccountToken: "sa_live_second"},
		{BaseURL: server.URL + "/other", ServiceAccountToken: "sa_live_first"},
		{BaseURL: server.URL, ServiceAccountToken: "sa_live_first", ReadOnly: true},
		{BaseURL: server.URL, ServiceAccountToken: "sa_live_first", Scopes: []string{"extra"}},
	}
	for i, cfg := range configs {
		t.Setenv("CIO_TOKEN", cfg.ServiceAccountToken)
		for range 2 {
			token, err := New(cfg).EnsureAccessToken(context.Background())
			if err != nil || token != fmt.Sprintf("jwt-%d", i+1) {
				t.Fatalf("config %d: token=%q err=%v", i, token, err)
			}
		}
	}
	if exchanges.Load() != int32(len(configs)) {
		t.Fatalf("expected one exchange per auth context, got %d", exchanges.Load())
	}
}

func TestEnvTokenCacheExpiry(t *testing.T) {
	for _, jwtExpiry := range []bool{false, true} {
		t.Run(fmt.Sprintf("jwt-expiry-%t", jwtExpiry), func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			t.Setenv("CIO_TOKEN", "sa_live_env")
			var exchanges atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				exchanges.Add(1)
				token, expiresIn := "opaque-jwt", 30
				if jwtExpiry {
					payload := fmt.Sprintf(`{"exp":%d}`, time.Now().Add(30*time.Second).Unix())
					token = "header." + base64.RawURLEncoding.EncodeToString([]byte(payload)) + ".signature"
					expiresIn = 3600
				}
				fmt.Fprintf(w, `{"access_token":%q,"expires_in":%d}`, token, expiresIn)
			}))
			defer server.Close()
			for range 2 {
				if _, err := New(Config{BaseURL: server.URL, ServiceAccountToken: "sa_live_env"}).EnsureAccessToken(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			if exchanges.Load() != 2 {
				t.Fatal("token within 60 seconds of expiry was reused")
			}
		})
	}
}

func TestEnvTokenCacheConcurrentClients(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CIO_TOKEN", "sa_live_env")
	var exchanges atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if exchanges.Add(1) == 1 {
			close(started)
		}
		<-release
		fmt.Fprint(w, `{"access_token":"env-jwt","expires_in":3600}`)
	}))
	defer server.Close()
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if _, err := New(Config{BaseURL: server.URL, ServiceAccountToken: "sa_live_env"}).EnsureAccessToken(context.Background()); err != nil {
				t.Error(err)
			}
		})
	}
	<-started
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := New(Config{BaseURL: server.URL, ServiceAccountToken: "sa_live_env"}).EnsureAccessToken(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("expected cancellation while waiting for cache lock, got %v", err)
	}
	close(release)
	wg.Wait()
	if exchanges.Load() != 1 {
		t.Fatalf("expected one concurrent exchange, got %d", exchanges.Load())
	}
}

func TestEnvTokenCacheRefreshAfter401(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CIO_TOKEN", "sa_live_env")
	var exchanges atomic.Int32
	var rejections atomic.Int32
	rejectedAll := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/service_accounts/oauth/token" {
			fmt.Fprintf(w, `{"access_token":"jwt-%d","expires_in":3600}`, exchanges.Add(1))
			return
		}
		if r.Header.Get("Authorization") == "Bearer jwt-1" {
			if rejections.Add(1) == 8 {
				close(rejectedAll)
			}
			<-rejectedAll
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, `{"ok":true}`)
	}))
	defer server.Close()
	cfg := Config{BaseURL: server.URL, ServiceAccountToken: "sa_live_env"}
	clients := make([]*Client, 8)
	for i := range clients {
		clients[i] = New(cfg)
		if _, err := clients[i].EnsureAccessToken(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	for _, c := range clients {
		wg.Go(func() {
			if _, err := c.Do(context.Background(), http.MethodGet, "/test", nil, nil); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if _, err := New(cfg).Do(context.Background(), http.MethodGet, "/test", nil, nil); err != nil {
		t.Fatal(err)
	}
	if exchanges.Load() != 2 {
		t.Fatalf("expected rejected token to be replaced for later clients, got %d exchanges", exchanges.Load())
	}
}
