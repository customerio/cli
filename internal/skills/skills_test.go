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

	"github.com/customerio/cli/internal/useragent"
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

func TestEnsureSkills_UserAgentHeader(t *testing.T) {
	resp := testResponse()
	data, _ := json.Marshal(resp)

	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/json")
		w.Write(data)
	}))
	defer srv.Close()

	_, err := EnsureSkills(context.Background(), LoadOptions{
		BaseURL:  srv.URL,
		CacheDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}

	if got != useragent.Get() {
		t.Errorf("User-Agent: got %q, want %q", got, useragent.Get())
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

func TestFrontmatterDescription(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"single line", "---\nname: x\ndescription: A one liner.\n---\nbody", "A one liner."},
		{"folded block", "---\ndescription: >\n  first part\n  second part\nname: x\n---\nbody", "first part second part"},
		{"no frontmatter", "# Just a heading\n", ""},
		{"no description key", "---\nname: x\n---\nbody", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := FrontmatterDescription(tc.raw); got != tc.want {
				t.Errorf("FrontmatterDescription() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSkillIndex(t *testing.T) {
	// Authored content is returned verbatim.
	authored := Skill{Path: "fly-api", Name: "Fly", Description: "d", Content: "# Authored"}
	if got := authored.Index(); got != "# Authored" {
		t.Errorf("expected authored content, got %q", got)
	}

	// Entrypoint-less skill synthesizes a sorted index from frontmatter.
	s := Skill{
		Path:        "cli",
		Name:        "CLI Onboarding",
		Description: "Builder onboarding.",
		Files: map[string]string{
			"onboarding.md": "---\ndescription: Entry point.\n---\n",
			"auth.md":       "---\ndescription: Auth reference.\n---\n",
		},
	}
	want := "# CLI Onboarding\n\nBuilder onboarding.\n\n" +
		"Read the file matching the task (`cio skills read cli/<file>`):\n\n" +
		"- **auth.md** - Auth reference.\n" +
		"- **onboarding.md** - Entry point.\n"
	if got := s.Index(); got != want {
		t.Errorf("synthesized index mismatch\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestEnsureSkills_UserAgentChangeInvalidatesCache(t *testing.T) {
	// Restore the package version after fiddling with it.
	defer useragent.SetVersion("dev")

	requestCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		// Vary the body by User-Agent, like the real server.
		body, _ := json.Marshal(&SkillsResponse{Prompt: "UA=" + r.Header.Get("User-Agent")})
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ETag", ComputeETag(body))
		w.Write(body)
	}))
	defer srv.Close()

	opts := LoadOptions{
		BaseURL:  srv.URL,
		CacheDir: t.TempDir(),
		TTL:      time.Hour, // long: only a UA change should force a refetch
	}

	useragent.SetVersion("1.0.0")
	got, err := EnsureSkills(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if got.Prompt != "UA="+useragent.Get() || requestCount != 1 {
		t.Fatalf("v1: prompt=%q requests=%d", got.Prompt, requestCount)
	}

	// Different version (e.g. after an upgrade) within TTL must refetch, not
	// reuse the v1 variant.
	useragent.SetVersion("2.0.0")
	got, err = EnsureSkills(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if got.Prompt != "UA="+useragent.Get() {
		t.Fatalf("v2: expected refetched variant, got prompt=%q", got.Prompt)
	}
	if requestCount != 2 {
		t.Fatalf("v2: expected a refetch on UA change, got %d requests", requestCount)
	}

	// Same version again within TTL: cache hit, no new request.
	if _, err = EnsureSkills(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if requestCount != 2 {
		t.Fatalf("expected cache hit for same UA, got %d requests", requestCount)
	}
}
