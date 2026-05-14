package client

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestResolveServiceAccountToken_ExplicitFirst(t *testing.T) {
	t.Setenv("CIO_TOKEN", "sa_live_env")
	got := ResolveServiceAccountToken("sa_live_explicit")
	if got != "sa_live_explicit" {
		t.Errorf("expected sa_live_explicit, got %q", got)
	}
}

func TestResolveServiceAccountToken_EnvFallback(t *testing.T) {
	t.Setenv("CIO_TOKEN", "sa_live_env")
	got := ResolveServiceAccountToken("")
	if got != "sa_live_env" {
		t.Errorf("expected sa_live_env, got %q", got)
	}
}

func TestResolveServiceAccountToken_ConfigFileFallback(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	t.Setenv("CIO_TOKEN", "")

	dir := filepath.Join(tmpDir, ".cio")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	creds := Credentials{ServiceAccountToken: "sa_live_file"}
	data, _ := json.Marshal(creds)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), data, 0600); err != nil {
		t.Fatal(err)
	}

	got := ResolveServiceAccountToken("")
	if got != "sa_live_file" {
		t.Errorf("expected sa_live_file, got %q", got)
	}
}

func TestResolveServiceAccountToken_Empty(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	t.Setenv("CIO_TOKEN", "")

	got := ResolveServiceAccountToken("")
	if got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestWriteAndReadCredentials(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	creds := &Credentials{
		ServiceAccountToken: "sa_live_test123",
		Region:              "eu",
	}

	if err := WriteCredentials(creds); err != nil {
		t.Fatalf("write failed: %v", err)
	}

	got, err := ReadCredentials()
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}
	if got.ServiceAccountToken != "sa_live_test123" {
		t.Errorf("expected token 'sa_live_test123', got %q", got.ServiceAccountToken)
	}
	if got.Region != "eu" {
		t.Errorf("expected region 'eu', got %q", got.Region)
	}

	// Verify permissions.
	path := filepath.Join(tmpDir, ".cio", "config.json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("expected 0600, got %o", perm)
	}
}

func TestCacheAccessToken_ConcurrentWritesProduceValidConfig(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	if err := WriteCredentials(&Credentials{ServiceAccountToken: "sa_live_seed"}); err != nil {
		t.Fatalf("seed write: %v", err)
	}

	const workers = 16
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := range workers {
		go func(i int) {
			defer wg.Done()
			// Alternate readOnly so writes actually differ; exercise the
			// read-modify-write path under contention.
			if err := CacheAccessToken("jwt", 3600, i%2 == 0, []string{"scope"}); err != nil {
				t.Errorf("cache %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	got, err := ReadCredentials()
	if err != nil {
		t.Fatalf("read after concurrent writes: %v", err)
	}
	if got.ServiceAccountToken != "sa_live_seed" {
		t.Errorf("seed token clobbered: got %q", got.ServiceAccountToken)
	}
	if got.AccessToken != "jwt" {
		t.Errorf("access token not persisted: got %q", got.AccessToken)
	}
	if got.AccessTokenExpiresAt.Before(time.Now()) {
		t.Errorf("expiry not persisted: got %v", got.AccessTokenExpiresAt)
	}
}

func TestCachedAccessTokenRequiresMatchingServiceAccountToken(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	if err := WriteCredentials(&Credentials{
		ServiceAccountToken:  "sa_live_file",
		AccessToken:          "jwt-file",
		AccessTokenExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("seed write: %v", err)
	}

	if got := CachedAccessTokenForServiceAccount("sa_live_env", false, nil); got != "" {
		t.Fatalf("expected mismatched token cache miss, got %q", got)
	}
	if got := CachedAccessTokenForServiceAccount("sa_live_file", false, nil); got != "jwt-file" {
		t.Fatalf("expected matching token cache hit, got %q", got)
	}
}

func TestCacheAccessTokenSkipsMismatchedServiceAccountToken(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	if err := WriteCredentials(&Credentials{
		ServiceAccountToken:  "sa_live_file",
		AccessToken:          "jwt-file",
		AccessTokenExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("seed write: %v", err)
	}

	if err := CacheAccessTokenForServiceAccount("sa_live_env", "jwt-env", 3600, false, nil); err != nil {
		t.Fatalf("cache write: %v", err)
	}

	got, err := ReadCredentials()
	if err != nil {
		t.Fatalf("read credentials: %v", err)
	}
	if got.AccessToken != "jwt-file" {
		t.Fatalf("expected cached access token to stay unchanged, got %q", got.AccessToken)
	}
}

func TestWriteCredentials_AtomicNoPartialFile(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	const writers = 8
	var wg sync.WaitGroup
	wg.Add(writers)
	for i := range writers {
		go func(i int) {
			defer wg.Done()
			creds := &Credentials{
				ServiceAccountToken: "sa_live_writer",
				AccountID:           "12345",
				Region:              "us",
			}
			if err := WriteCredentials(creds); err != nil {
				t.Errorf("write %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	// No orphan temp files after concurrent writes.
	entries, err := os.ReadDir(filepath.Join(tmpDir, ".cio"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := e.Name()
		if name == "config.json" || name == "config.lock" {
			continue
		}
		t.Errorf("unexpected leftover file in config dir: %s", name)
	}

	// Final file parses cleanly (no torn writes).
	got, err := ReadCredentials()
	if err != nil {
		t.Fatalf("read after concurrent writes: %v", err)
	}
	if got.ServiceAccountToken != "sa_live_writer" {
		t.Errorf("expected sa_live_writer, got %q", got.ServiceAccountToken)
	}
}

func TestDeleteCredentials(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	creds := &Credentials{ServiceAccountToken: "sa_live_delete"}
	if err := WriteCredentials(creds); err != nil {
		t.Fatal(err)
	}

	if err := DeleteCredentials(); err != nil {
		t.Fatalf("delete failed: %v", err)
	}

	path := filepath.Join(tmpDir, ".cio", "config.json")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("expected file to be deleted")
	}
}

func TestDeleteCredentials_NoFile(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	if err := DeleteCredentials(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMaskToken(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"", ""},
		{"ab", "**"},
		{"sa_live_abcdef1234567890", "sa_live_abcd********7890"},
		{"sa_live_ab", "sa_live_**"},
		{"short", "*****"},
		{"abcdefghijklmnop", "abcd********mnop"},
	}
	for _, tc := range tests {
		got := MaskToken(tc.input)
		if got != tc.expected {
			t.Errorf("MaskToken(%q) = %q, want %q", tc.input, got, tc.expected)
		}
	}
}

func TestIsServiceAccountToken(t *testing.T) {
	if !IsServiceAccountToken("sa_live_abc123") {
		t.Error("expected true for sa_live_ prefix")
	}
	if IsServiceAccountToken("not_a_token") {
		t.Error("expected false for non-sa_live_ prefix")
	}
	if IsServiceAccountToken("") {
		t.Error("expected false for empty string")
	}
}

func TestBaseURLForRegion(t *testing.T) {
	tests := []struct {
		region   string
		expected string
	}{
		{"us", "https://us.fly.customer.io"},
		{"eu", "https://eu.fly.customer.io"},
		{"EU", "https://eu.fly.customer.io"},
		{"", "https://us.fly.customer.io"},
		{"garbage", "https://us.fly.customer.io"},
	}
	for _, tc := range tests {
		got := BaseURLForRegion(tc.region)
		if got != tc.expected {
			t.Errorf("BaseURLForRegion(%q) = %q, want %q", tc.region, got, tc.expected)
		}
	}
}

func TestRegionFromBaseURL(t *testing.T) {
	tests := []struct {
		url      string
		expected string
	}{
		{"https://us.fly.customer.io", "us"},
		{"https://eu.fly.customer.io", "eu"},
		{"https://something.else.com", ""},
	}
	for _, tc := range tests {
		got := RegionFromBaseURL(tc.url)
		if got != tc.expected {
			t.Errorf("RegionFromBaseURL(%q) = %q, want %q", tc.url, got, tc.expected)
		}
	}
}

func TestResolveRegion(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	t.Setenv("CIO_REGION", "")

	// Default is "us"
	got := ResolveRegion("", false)
	if got != "us" {
		t.Errorf("expected 'us', got %q", got)
	}

	// Env var
	t.Setenv("CIO_REGION", "eu")
	got = ResolveRegion("", false)
	if got != "eu" {
		t.Errorf("expected 'eu', got %q", got)
	}

	// Explicit URL overrides
	t.Setenv("CIO_REGION", "")
	got = ResolveRegion("https://eu.fly.customer.io", true)
	if got != "eu" {
		t.Errorf("expected 'eu', got %q", got)
	}
}
