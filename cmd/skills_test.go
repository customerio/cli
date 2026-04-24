package cmd

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/customerio/cli/internal/skills"
)

func testSkillsServer(t *testing.T) *httptest.Server {
	t.Helper()
	resp := &skills.SkillsResponse{
		Prompt: "## Skills\n\nRouting rules here.\n\n<available_skills>\n</available_skills>",
		Skills: []skills.Skill{
			{
				Path:        "fly-api",
				Name:        "Fly (UI) API Reference",
				Description: "Complete endpoint reference.",
				Content:     "# Fly API\n\nMain content.",
				Files: map[string]string{
					"campaigns.md": "# Campaigns\n\nCampaign details.",
				},
			},
			{
				Path:        "liquid-syntax",
				Name:        "Liquid Templating Syntax",
				Description: "Complete guide to Liquid.",
				Content:     "# Liquid\n\nLiquid content.",
				Files:       map[string]string{},
			},
		},
	}
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	etag := skills.ComputeETag(data)

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/agent/skills" {
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

func runSkillsCommand(t *testing.T, srv *httptest.Server, args ...string) (string, error) {
	t.Helper()

	dir := t.TempDir()
	cmd := rootCmd
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(new(bytes.Buffer))

	fullArgs := append([]string{"skills", "--api-url", srv.URL}, args...)
	// Set env to use test cache dir.
	t.Setenv("CIO_SKILLS_CACHE_DIR", dir)
	cmd.SetArgs(fullArgs)
	err := cmd.Execute()
	return buf.String(), err
}

func TestSkillsList(t *testing.T) {
	srv := testSkillsServer(t)
	defer srv.Close()

	out, err := runSkillsCommand(t, srv)
	if err != nil {
		t.Fatal(err)
	}

	var result []map[string]any
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("invalid JSON: %v\noutput: %s", err, out)
	}

	if len(result) != 2 {
		t.Fatalf("expected 2 skills, got %d", len(result))
	}
	if result[0]["path"] != "fly-api" {
		t.Errorf("expected first skill path 'fly-api', got %v", result[0]["path"])
	}
}

func TestSkillsRead(t *testing.T) {
	srv := testSkillsServer(t)
	defer srv.Close()

	out, err := runSkillsCommand(t, srv, "read", "fly-api")
	if err != nil {
		t.Fatal(err)
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("invalid JSON: %v\noutput: %s", err, out)
	}

	if result["path"] != "fly-api" {
		t.Errorf("expected path 'fly-api', got %v", result["path"])
	}
	if result["content"] != "# Fly API\n\nMain content." {
		t.Errorf("unexpected content: %v", result["content"])
	}
}

func TestSkillsReadSubFile(t *testing.T) {
	srv := testSkillsServer(t)
	defer srv.Close()

	out, err := runSkillsCommand(t, srv, "read", "fly-api/campaigns.md")
	if err != nil {
		t.Fatal(err)
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("invalid JSON: %v\noutput: %s", err, out)
	}

	if result["content"] != "# Campaigns\n\nCampaign details." {
		t.Errorf("unexpected content: %v", result["content"])
	}
}

func TestSkillsPrompt(t *testing.T) {
	srv := testSkillsServer(t)
	defer srv.Close()

	out, err := runSkillsCommand(t, srv, "prompt")
	if err != nil {
		t.Fatal(err)
	}

	var result map[string]string
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("invalid JSON: %v\noutput: %s", err, out)
	}

	if result["prompt"] == "" {
		t.Error("expected non-empty prompt")
	}
}

func TestSkillsReadUnknown(t *testing.T) {
	srv := testSkillsServer(t)
	defer srv.Close()

	_, err := runSkillsCommand(t, srv, "read", "nonexistent")
	if err == nil {
		t.Fatal("expected error for unknown skill")
	}
}
