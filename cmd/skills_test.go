package cmd

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/customerio/cli/internal/client"
	"github.com/customerio/cli/internal/skills"
)

// skillsFixture returns the bundle the test server serves. Plan scoping is
// resolved server-side (out-of-scope files are omitted before the bundle is
// sent), so the fixture is the already-scoped response the CLI receives.
// accountPlan, when non-empty, is appended to the prompt as the real server
// does for authenticated callers.
func skillsFixture(accountPlan string) *skills.SkillsResponse {
	prompt := "## Skills\n\nRouting rules here.\n\n<available_skills>\n</available_skills>"
	if accountPlan != "" {
		prompt += "\n\nAccount plan: " + accountPlan + "."
	}
	resp := &skills.SkillsResponse{
		Prompt: prompt,
		Skills: []skills.Skill{
			{
				Path:        "fly-api",
				Name:        "Fly (UI) API Reference",
				Description: "Complete endpoint reference.",
				Content:     "# Fly API\n\nMain content.",
				Files: map[string]string{
					"campaigns.md":  "# Campaigns\n\nCampaign details.",
					"broadcasts.md": "# Broadcasts\n\nBroadcast details.",
					"segments.md":   "# Segments\n\nSegment details.",
				},
			},
			{
				Path:        "liquid-syntax",
				Name:        "Liquid Templating Syntax",
				Description: "Complete guide to Liquid.",
				Content:     "# Liquid\n\nLiquid content.",
				Files:       map[string]string{},
			},
			{
				// Entrypoint-less skill: empty Content, routing index is
				// synthesized client-side from each sub-file's frontmatter.
				Path:        "cli",
				Name:        "Customer.io CLI Onboarding",
				Description: "Builder onboarding.",
				Content:     "",
				Files: map[string]string{
					"onboarding.md": "---\nname: onboarding\ndescription: Builder onboarding entry point.\n---\n\n# Onboarding\n",
					"auth.md":       "---\nname: auth\ndescription: cio CLI authentication reference.\n---\n\n# Auth\n",
					"anonymous.md":  "---\nname: anonymous\ndescription: Anonymous broadcasts.\n---\n\n# Anonymous\n",
				},
			},
		},
	}
	return resp
}

// testSkillsServer serves the full, unscoped bundle (an unauthenticated fetch).
func testSkillsServer(t *testing.T) *httptest.Server {
	t.Helper()
	return skillsServerWithResponse(t, skillsFixture(""), nil)
}

// skillsServerWithResponse serves the given bundle from /v1/agent/skills and
// also answers the OAuth token exchange, so authenticated fetches work. When
// gotAuth is non-nil, the Authorization header sent to the skills endpoint is
// recorded into it.
func skillsServerWithResponse(t *testing.T, resp *skills.SkillsResponse, gotAuth *string) *httptest.Server {
	t.Helper()
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	etag := skills.ComputeETag(data)

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/service_accounts/oauth/token":
			_, _ = w.Write([]byte(`{"access_token":"jwt-test-session","token_type":"Bearer","expires_in":3600}`))
		case "/v1/agent/skills":
			if gotAuth != nil {
				*gotAuth = r.Header.Get("Authorization")
			}
			if match := r.Header.Get("If-None-Match"); match == etag {
				w.Header().Set("ETag", etag)
				w.WriteHeader(http.StatusNotModified)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("ETag", etag)
			_, _ = w.Write(data)
		default:
			http.NotFound(w, r)
		}
	}))
}

// isolateSkillsHome points HOME at a temp dir (so plan resolution never sees
// or touches the developer's real ~/.cio/config.json) and clears CIO_TOKEN.
// Returns the temp home dir so tests can seed credentials into it.
func isolateSkillsHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CIO_TOKEN", "")
	return home
}

func runSkillsCommand(t *testing.T, srv *httptest.Server, args ...string) (string, error) {
	t.Helper()
	isolateSkillsHome(t)
	return execSkillsCommand(t, srv, args...)
}

// execSkillsCommand runs `cio skills` against srv without touching HOME, so
// tests that seed ~/.cio/config.json keep their credentials.
func execSkillsCommand(t *testing.T, srv *httptest.Server, args ...string) (string, error) {
	t.Helper()

	dir := t.TempDir()
	cmd := rootCmd
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(new(bytes.Buffer))

	// Reset --token explicitly: rootCmd is shared package state, so a value
	// set by an earlier test would otherwise leak into plan resolution.
	fullArgs := append([]string{"skills", "--api-url", srv.URL, "--token", ""}, args...)
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

	if len(result) != 3 {
		t.Fatalf("expected 3 skills, got %d", len(result))
	}
	if result[0]["path"] != "fly-api" {
		t.Errorf("expected first skill path 'fly-api', got %v", result[0]["path"])
	}
}

func TestSkillsListIncludesFileDescriptions(t *testing.T) {
	srv := testSkillsServer(t)
	defer srv.Close()

	out, err := runSkillsCommand(t, srv)
	if err != nil {
		t.Fatal(err)
	}

	var result []struct {
		Path  string `json:"path"`
		Files []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		} `json:"files"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("invalid JSON: %v\noutput: %s", err, out)
	}

	var cli *struct {
		Path  string `json:"path"`
		Files []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		} `json:"files"`
	}
	for i := range result {
		if result[i].Path == "cli" {
			cli = &result[i]
		}
	}
	if cli == nil {
		t.Fatal("expected cli skill in list")
	}
	// Files come back sorted, each carrying its frontmatter description.
	// With no stored plan info, nothing is filtered.
	if len(cli.Files) != 3 || cli.Files[0].Name != "anonymous.md" || cli.Files[1].Name != "auth.md" || cli.Files[2].Name != "onboarding.md" {
		t.Fatalf("expected files sorted [anonymous.md auth.md onboarding.md], got %+v", cli.Files)
	}
	if cli.Files[2].Description != "Builder onboarding entry point." {
		t.Errorf("expected onboarding.md description from frontmatter, got %q", cli.Files[2].Description)
	}
}

func TestSkillsReadSynthesizesIndex(t *testing.T) {
	srv := testSkillsServer(t)
	defer srv.Close()

	out, err := runSkillsCommand(t, srv, "read", "cli")
	if err != nil {
		t.Fatal(err)
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("invalid JSON: %v\noutput: %s", err, out)
	}

	content, _ := result["content"].(string)
	for _, want := range []string{
		"# Customer.io CLI Onboarding",
		"cio skills read cli/<file>",
		"- **auth.md** - cio CLI authentication reference.",
		"- **onboarding.md** - Builder onboarding entry point.",
	} {
		if !bytes.Contains([]byte(content), []byte(want)) {
			t.Errorf("synthesized index missing %q\ngot:\n%s", want, content)
		}
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
	// No stored plan info — the prompt must not state a plan.
	if strings.Contains(result["prompt"], "Account plan:") {
		t.Errorf("expected no account plan line, got prompt: %q", result["prompt"])
	}
}

func TestSkillsPromptIncludesAccountPlan(t *testing.T) {
	// The plan line is resolved and appended server-side; the CLI passes the
	// prompt through verbatim.
	srv := skillsServerWithResponse(t, skillsFixture("premium"), nil)
	defer srv.Close()

	out, err := runSkillsCommand(t, srv, "prompt")
	if err != nil {
		t.Fatal(err)
	}

	var result map[string]string
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("invalid JSON: %v\noutput: %s", err, out)
	}

	if !strings.Contains(result["prompt"], "Account plan: premium.") {
		t.Errorf("expected prompt to state the account plan, got: %q", result["prompt"])
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

// TestSkillsSendsBearerTokenWhenAuthenticated verifies the CLI exchanges its
// stored credential and sends the resulting Bearer token, so the server can
// scope the bundle to the account.
func TestSkillsSendsBearerTokenWhenAuthenticated(t *testing.T) {
	var gotAuth string
	srv := skillsServerWithResponse(t, skillsFixture(""), &gotAuth)
	defer srv.Close()

	isolateSkillsHome(t)
	if err := client.WriteCredentials(&client.Credentials{
		ServiceAccountToken: "sa_live_authtest",
		AccountID:           "1",
		Region:              "us",
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := execSkillsCommand(t, srv); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer jwt-test-session" {
		t.Errorf("expected Bearer token on the skills request, got %q", gotAuth)
	}
}

// TestSkillsUnauthenticatedSendsNoToken verifies that without credentials the
// CLI fetches the bundle unauthenticated (no Authorization header).
func TestSkillsUnauthenticatedSendsNoToken(t *testing.T) {
	var gotAuth string
	srv := skillsServerWithResponse(t, skillsFixture(""), &gotAuth)
	defer srv.Close()

	if _, err := runSkillsCommand(t, srv); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "" {
		t.Errorf("expected no Authorization header, got %q", gotAuth)
	}
}
