package skills

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testResponse() *SkillsResponse {
	return &SkillsResponse{
		Prompt: "## Skills\n\nRouting rules here.",
		Skills: []Skill{
			{
				Path:        "fly-api",
				Name:        "Fly (UI) API Reference",
				Description: "Complete endpoint reference.",
				Content:     "# Fly API\n\nMain content.",
				Files: map[string]string{
					"campaigns.md": "# Campaigns\n\nSub-file content.",
				},
			},
			{
				Path:        "liquid-syntax",
				Name:        "Liquid Templating Syntax",
				Description: "Complete guide to Liquid.",
				Content:     "# Liquid\n\nMain content.",
				Files:       map[string]string{},
			},
		},
	}
}

func serveSkills(t *testing.T, resp *SkillsResponse) *httptest.Server {
	t.Helper()
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	etag := ComputeETag(data)

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != skillsEndpointPath {
			http.NotFound(w, r)
			return
		}
		if match := r.Header.Get("If-None-Match"); match == etag {
			w.Header().Set("ETag", etag)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ETag", etag)
		w.Write(data)
	}))
}

func TestEnsureSkills_Fresh(t *testing.T) {
	resp := testResponse()
	srv := serveSkills(t, resp)
	defer srv.Close()

	dir := t.TempDir()
	got, err := EnsureSkills(context.Background(), LoadOptions{
		BaseURL:  srv.URL,
		CacheDir: dir,
	})
	if err != nil {
		t.Fatal(err)
	}

	if got.Prompt != resp.Prompt {
		t.Errorf("prompt = %q, want %q", got.Prompt, resp.Prompt)
	}
	if len(got.Skills) != 2 {
		t.Fatalf("got %d skills, want 2", len(got.Skills))
	}
	if got.Skills[0].Path != "fly-api" {
		t.Errorf("skills[0].path = %q, want %q", got.Skills[0].Path, "fly-api")
	}
	if got.Skills[0].Files["campaigns.md"] != "# Campaigns\n\nSub-file content." {
		t.Errorf("unexpected sub-file content: %q", got.Skills[0].Files["campaigns.md"])
	}

	// Verify cache files exist.
	if _, err := os.Stat(filepath.Join(dir, skillsCacheFile)); err != nil {
		t.Errorf("cache file not created: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, metaFileName)); err != nil {
		t.Errorf("meta file not created: %v", err)
	}
}

func TestEnsureSkills_CacheHit(t *testing.T) {
	resp := testResponse()
	srv := serveSkills(t, resp)
	defer srv.Close()

	dir := t.TempDir()
	opts := LoadOptions{
		BaseURL:  srv.URL,
		CacheDir: dir,
	}

	// First call populates cache.
	_, err := EnsureSkills(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}

	// Stop server — second call should use cache.
	srv.Close()

	got, err := EnsureSkills(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if got.Prompt != resp.Prompt {
		t.Errorf("prompt = %q, want %q", got.Prompt, resp.Prompt)
	}
}

func TestEnsureSkills_304NotModified(t *testing.T) {
	resp := testResponse()
	requestCount := 0
	data, _ := json.Marshal(resp)
	etag := ComputeETag(data)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		if match := r.Header.Get("If-None-Match"); match == etag {
			w.Header().Set("ETag", etag)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ETag", etag)
		w.Write(data)
	}))
	defer srv.Close()

	dir := t.TempDir()
	opts := LoadOptions{
		BaseURL:  srv.URL,
		CacheDir: dir,
		TTL:      1 * time.Millisecond, // expire immediately
	}

	// First call: full download.
	_, err := EnsureSkills(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if requestCount != 1 {
		t.Fatalf("expected 1 request, got %d", requestCount)
	}

	// Wait for TTL to expire.
	time.Sleep(5 * time.Millisecond)

	// Second call: should get 304.
	got, err := EnsureSkills(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if requestCount != 2 {
		t.Fatalf("expected 2 requests, got %d", requestCount)
	}
	if got.Prompt != resp.Prompt {
		t.Errorf("prompt = %q, want %q", got.Prompt, resp.Prompt)
	}
}

func TestEnsureSkills_ForceRefresh(t *testing.T) {
	resp := testResponse()
	requestCount := 0
	data, _ := json.Marshal(resp)
	etag := ComputeETag(data)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		if match := r.Header.Get("If-None-Match"); match == etag {
			w.Header().Set("ETag", etag)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ETag", etag)
		w.Write(data)
	}))
	defer srv.Close()

	dir := t.TempDir()
	opts := LoadOptions{
		BaseURL:  srv.URL,
		CacheDir: dir,
	}

	// First call populates cache.
	_, err := EnsureSkills(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}

	// Force refresh — should still make a request (gets 304).
	opts.ForceRefresh = true
	_, err = EnsureSkills(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if requestCount != 2 {
		t.Fatalf("expected 2 requests after force refresh, got %d", requestCount)
	}
}

func TestEnsureSkills_StaleOnFailure(t *testing.T) {
	resp := testResponse()
	data, _ := json.Marshal(resp)
	etag := ComputeETag(data)

	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 1 {
			// First call succeeds.
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("ETag", etag)
			w.Write(data)
			return
		}
		// Subsequent calls fail.
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	dir := t.TempDir()
	opts := LoadOptions{
		BaseURL:      srv.URL,
		CacheDir:     dir,
		TTL:          1 * time.Millisecond,
		ForceRefresh: false,
	}

	// First call populates cache.
	_, err := EnsureSkills(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}

	// Wait for TTL to expire.
	time.Sleep(5 * time.Millisecond)

	// Second call — server returns 500 but we should get stale cache.
	got, err := EnsureSkills(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if got.Prompt != resp.Prompt {
		t.Errorf("prompt = %q, want %q", got.Prompt, resp.Prompt)
	}
}
