package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/customerio/cli/internal/skills"
)

// runSkillsInstall drives `cio skills install` against a test server with a
// scratch HOME and working directory so on-disk writes are sandboxed.
func runInstallCommand(t *testing.T, srv *httptest.Server, home string, args ...string) (string, error) {
	t.Helper()

	cacheDir := t.TempDir()
	t.Setenv("CIO_SKILLS_CACHE_DIR", cacheDir)
	t.Setenv("HOME", home)

	cmd := rootCmd
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(new(bytes.Buffer))
	cmd.SetIn(new(bytes.Buffer)) // non-TTY: scope defaults to global
	t.Cleanup(func() { cmd.SetIn(nil) })

	// rootCmd is shared across tests and pflag retains parsed values between
	// Execute calls, so reset the flags this command touches to their defaults.
	_ = rootCmd.PersistentFlags().Set("dry-run", "false")
	for _, name := range []string{"global", "project", "force"} {
		_ = skillsInstallCmd.Flags().Set(name, "false")
	}
	_ = skillsInstallCmd.Flags().Set("target", "claude,codex")

	fullArgs := append([]string{"skills", "install", "--api-url", srv.URL}, args...)
	cmd.SetArgs(fullArgs)
	err := cmd.Execute()
	return buf.String(), err
}

func TestSkillsInstallBootstrapOnly(t *testing.T) {
	srv := testSkillsServer(t)
	defer srv.Close()

	home := t.TempDir()
	out, err := runInstallCommand(t, srv, home)
	if err != nil {
		t.Fatalf("install failed: %v\noutput: %s", err, out)
	}

	var result struct {
		Scope     string `json:"scope"`
		Installed []struct {
			Skill  string `json:"skill"`
			Target string `json:"target"`
		} `json:"installed"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("invalid JSON: %v\noutput: %s", err, out)
	}
	if result.Scope != "global" {
		t.Errorf("expected scope global, got %q", result.Scope)
	}
	// Only the bootstrap skill (cio), once per target (claude+codex).
	if len(result.Installed) != 2 {
		t.Fatalf("expected 2 entries (cio x claude+codex), got %d: %+v", len(result.Installed), result.Installed)
	}
	for _, e := range result.Installed {
		if e.Skill != "cio" {
			t.Errorf("expected only the bootstrap skill, got %q", e.Skill)
		}
	}

	// The other skills must NOT be written locally — they are served by the
	// API and read at runtime via `cio skills read <skill>`.
	for _, other := range []string{"fly-api", "liquid-syntax"} {
		if _, err := os.Stat(filepath.Join(home, ".claude", "skills", other)); !os.IsNotExist(err) {
			t.Errorf("non-bootstrap skill %q must not be installed, got err=%v", other, err)
		}
	}

	// Bootstrap SKILL.md exists for both targets: server-tuned frontmatter plus
	// a minimal body that just points the agent at `cio prime`.
	for _, target := range []string{".claude", ".agents"} {
		p := filepath.Join(home, target, "skills", "cio", "SKILL.md")
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("expected bootstrap SKILL.md at %s: %v", p, err)
		}
		if !strings.HasPrefix(string(data), "---\nname: \"cio\"\ndescription: \"Builder onboarding.\"\n---\n") {
			t.Errorf("expected frontmatter on %s, got:\n%s", p, data)
		}
		if !strings.Contains(string(data), "cio prime") {
			t.Errorf("expected bootstrap body to point at `cio prime` in %s, got:\n%s", p, data)
		}
	}
	// Bootstrap sub-files are fetched at runtime, not installed.
	if _, err := os.Stat(filepath.Join(home, ".claude", "skills", "cio", "onboarding.md")); !os.IsNotExist(err) {
		t.Errorf("bootstrap sub-files must not be installed, got err=%v", err)
	}
}

func TestEnsureFrontmatter(t *testing.T) {
	// Missing frontmatter: prepend name + description.
	got := ensureFrontmatter("# Title\n\nbody", "fly-api", "Endpoint reference.")
	want := "---\nname: \"fly-api\"\ndescription: \"Endpoint reference.\"\n---\n\n# Title\n\nbody"
	if got != want {
		t.Errorf("ensureFrontmatter mismatch:\n got: %q\nwant: %q", got, want)
	}

	// Already has frontmatter: leave untouched.
	withFM := "---\nname: x\ndescription: y\n---\n\n# Body"
	if got := ensureFrontmatter(withFM, "ignored", "ignored"); got != withFM {
		t.Errorf("expected content with frontmatter untouched, got: %q", got)
	}

	// Multi-line / quote-bearing descriptions collapse to a safe scalar.
	got = ensureFrontmatter("body", "s", "line one\nline \"two\"")
	if !strings.HasPrefix(got, "---\nname: \"s\"\ndescription: \"line one line \\\"two\\\"\"\n---\n") {
		t.Errorf("unexpected scalar handling:\n%s", got)
	}
}

func TestSkillsInstallDryRun(t *testing.T) {
	srv := testSkillsServer(t)
	defer srv.Close()

	home := t.TempDir()
	out, err := runInstallCommand(t, srv, home, "--dry-run")
	if err != nil {
		t.Fatalf("dry-run failed: %v", err)
	}

	var result struct {
		DryRun bool `json:"dry_run"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if !result.DryRun {
		t.Error("expected dry_run true")
	}
	if _, err := os.Stat(filepath.Join(home, ".claude")); !os.IsNotExist(err) {
		t.Errorf("dry-run must not write to disk, got err=%v", err)
	}
}

func TestSkillsInstallTargetSelection(t *testing.T) {
	srv := testSkillsServer(t)
	defer srv.Close()

	home := t.TempDir()
	if _, err := runInstallCommand(t, srv, home, "--target", "codex"); err != nil {
		t.Fatalf("install failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".agents", "skills", "cio", "SKILL.md")); err != nil {
		t.Errorf("expected codex install: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude")); !os.IsNotExist(err) {
		t.Errorf("--target codex must not write Claude Code dir, got err=%v", err)
	}
}

func TestSkillsInstallUnknownTarget(t *testing.T) {
	srv := testSkillsServer(t)
	defer srv.Close()

	home := t.TempDir()
	if _, err := runInstallCommand(t, srv, home, "--target", "bogus"); err == nil {
		t.Fatal("expected error for unknown target")
	}
}

func TestSkillsInstallProject(t *testing.T) {
	srv := testSkillsServer(t)
	defer srv.Close()

	home := t.TempDir()
	proj := t.TempDir()
	t.Chdir(proj)

	if _, err := runInstallCommand(t, srv, home, "--project", "--target", "claude"); err != nil {
		t.Fatalf("install failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(proj, ".claude", "skills", "cio", "SKILL.md")); err != nil {
		t.Errorf("expected project install under cwd: %v", err)
	}
}

func TestSkillsInstallNoForceSkipsExisting(t *testing.T) {
	srv := testSkillsServer(t)
	defer srv.Close()

	home := t.TempDir()
	skillDir := filepath.Join(home, ".claude", "skills", "cio")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	existing := filepath.Join(skillDir, "SKILL.md")
	if err := os.WriteFile(existing, []byte("KEEP ME"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := runInstallCommand(t, srv, home, "--target", "claude"); err != nil {
		t.Fatalf("install failed: %v", err)
	}
	data, err := os.ReadFile(existing)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "KEEP ME" {
		t.Errorf("expected existing file untouched without --force, got: %s", data)
	}

	// With --force it gets overwritten.
	if _, err := runInstallCommand(t, srv, home, "--target", "claude", "--force"); err != nil {
		t.Fatalf("install --force failed: %v", err)
	}
	data, err = os.ReadFile(existing)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) == "KEEP ME" {
		t.Error("expected --force to overwrite existing file")
	}
}

func TestPrintInstallSummary(t *testing.T) {
	claude := installedFile{Skill: "cio", Target: "claude", Dir: "/home/u/.claude/skills/cio", Files: []string{"SKILL.md"}}
	codex := installedFile{Skill: "cio", Target: "codex", Dir: "/home/u/.agents/skills/cio", Files: []string{"SKILL.md"}}
	claudeSkipped := installedFile{Skill: "cli", Target: "claude", Dir: "/home/u/.claude/skills/cli", Files: nil}
	codexSkipped := installedFile{Skill: "cli", Target: "codex", Dir: "/home/u/.agents/skills/cli", Files: nil}

	cases := []struct {
		name      string
		dryRun    bool
		installed []installedFile
		wantHead  string
		wantFoot  string
		wantNote  bool // expect an "(already present)" marker
	}{
		{
			name:      "fresh install",
			installed: []installedFile{claude, codex},
			wantHead:  "Installed the Customer.io CLI skill:",
			wantFoot:  "cio prime",
		},
		{
			name:      "dry run",
			dryRun:    true,
			installed: []installedFile{claude, codex},
			wantHead:  "Would install the Customer.io CLI skill:",
			wantFoot:  "Run without --dry-run",
		},
		{
			name:      "all already present",
			installed: []installedFile{claudeSkipped, codexSkipped},
			wantHead:  "The Customer.io CLI skill is already installed:",
			wantFoot:  "Re-run with --force",
			wantNote:  true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var b bytes.Buffer
			printInstallSummary(&b, tc.dryRun, tc.installed, nil)
			out := b.String()
			t.Logf("\n%s", out)

			if !strings.Contains(out, tc.wantHead) {
				t.Errorf("missing header %q in:\n%s", tc.wantHead, out)
			}
			if !strings.Contains(out, tc.wantFoot) {
				t.Errorf("missing footer %q in:\n%s", tc.wantFoot, out)
			}
			if !strings.Contains(out, "Claude Code") || !strings.Contains(out, "Codex") {
				t.Errorf("expected friendly target labels, got:\n%s", out)
			}
			if strings.Contains(out, "{") || strings.Contains(out, "\"status\"") {
				t.Errorf("human summary must not contain JSON, got:\n%s", out)
			}
			if got := strings.Contains(out, "(already present)"); got != tc.wantNote {
				t.Errorf("already-present note = %v, want %v in:\n%s", got, tc.wantNote, out)
			}
		})
	}
}

func TestSafeRelPath(t *testing.T) {
	for _, name := range []string{"SKILL.md", "recipes/liquid.md", "a/b/c.md"} {
		if _, err := safeRelPath(name); err != nil {
			t.Errorf("safeRelPath(%q) unexpected error: %v", name, err)
		}
	}
	for _, name := range []string{"", "..", "../escape", "../../etc/passwd", "/abs/path", "a/../../b"} {
		if _, err := safeRelPath(name); err == nil {
			t.Errorf("safeRelPath(%q) expected error", name)
		}
	}
}

func TestPromptScope(t *testing.T) {
	cases := map[string]string{
		"\n":        "global",
		"1\n":       "global",
		"global\n":  "global",
		"2\n":       "project",
		"project\n": "project",
		"  2  \n":   "project",
	}
	for in, want := range cases {
		got, err := promptScope(strings.NewReader(in), new(bytes.Buffer))
		if err != nil {
			t.Errorf("promptScope(%q) error: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("promptScope(%q) = %q, want %q", in, got, want)
		}
	}

	if _, err := promptScope(strings.NewReader("nonsense\n"), new(bytes.Buffer)); err == nil {
		t.Error("expected error for invalid choice")
	}
}

func TestReadLineContextCancel(t *testing.T) {
	// A reader that never yields a line, simulating a stuck terminal read.
	pr, pw := io.Pipe()
	t.Cleanup(func() { _ = pw.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled, as if SIGINT fired

	done := make(chan struct{})
	var err error
	go func() {
		_, err = readLineContext(ctx, pr)
		close(done)
	}()

	select {
	case <-done:
		if err == nil {
			t.Fatal("expected cancellation error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("readLineContext did not return on cancellation (hang)")
	}
}

func TestSelectBootstrap(t *testing.T) {
	served := []skills.Skill{
		{Path: "fly-api", Name: "Fly"},
		{Path: bootstrapSkillName, Name: "Current"},
	}
	got, err := selectBootstrap(served)
	if err != nil {
		t.Fatalf("selectBootstrap: %v", err)
	}
	if len(got) != 1 || got[0].Path != bootstrapSkillName {
		t.Fatalf("expected the bootstrap skill, got %+v", got)
	}

	// The retired name is not a fallback: installing under it is the bug, so a
	// backend still serving it must fail loudly instead.
	legacyOnly := []skills.Skill{{Path: legacyBootstrapDir, Name: "Legacy"}}
	if _, err := selectBootstrap(legacyOnly); err == nil {
		t.Fatal("expected an error when only the retired name is served")
	}
}

func TestRemoveLegacyInstalls(t *testing.T) {
	base := t.TempDir()

	writeSKILL := func(subdir, name, body string) string {
		dir := filepath.Join(base, subdir, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	ourBody := "---\nname: cli\n---\n\n" + bootstrapBodyMarker + ", `cio`.\n"

	// Nothing left over.
	removed, err := removeLegacyInstalls(base, installTargets, false)
	if err != nil {
		t.Fatalf("removeLegacyInstalls: %v", err)
	}
	if len(removed) != 0 {
		t.Fatalf("expected nothing removed, got %v", removed)
	}

	// A dry run reports the directory without touching it.
	ours := writeSKILL(filepath.Join(".claude", "skills"), legacyBootstrapDir, ourBody)
	removed, err = removeLegacyInstalls(base, installTargets, true)
	if err != nil {
		t.Fatalf("removeLegacyInstalls dry run: %v", err)
	}
	if len(removed) != 1 || removed[0] != ours {
		t.Fatalf("expected the dry run to report %q, got %v", ours, removed)
	}
	if _, err := os.Stat(ours); err != nil {
		t.Fatalf("dry run must not delete, got err=%v", err)
	}

	// Somebody else's skill of the same name is left alone; ours is removed.
	theirs := writeSKILL(filepath.Join(".agents", "skills"), legacyBootstrapDir, "---\nname: cli\n---\n\n# My own CLI notes\n")
	removed, err = removeLegacyInstalls(base, installTargets, false)
	if err != nil {
		t.Fatalf("removeLegacyInstalls: %v", err)
	}
	if len(removed) != 1 || removed[0] != ours {
		t.Fatalf("expected only our own leftover removed, got %v", removed)
	}
	if _, err := os.Stat(ours); !os.IsNotExist(err) {
		t.Errorf("expected our leftover gone, got err=%v", err)
	}
	if _, err := os.Stat(theirs); err != nil {
		t.Errorf("expected an unrelated same-named skill untouched, got err=%v", err)
	}
}

func TestPrintInstallSummaryReportsRemoved(t *testing.T) {
	var b bytes.Buffer
	installed := []installedFile{{Skill: "cio", Target: "claude", Dir: "/home/u/.claude/skills/cio", Files: []string{"SKILL.md"}}}
	printInstallSummary(&b, false, installed, []string{"/home/u/.agents/skills/cli"})

	out := b.String()
	if !strings.Contains(out, "Removed an earlier install") || !strings.Contains(out, "/home/u/.agents/skills/cli") {
		t.Errorf("expected the summary to report the removal, got:\n%s", out)
	}
}

// The cleanup recognizes its own leftovers by a phrase from the body install
// writes, so editing that paragraph must not silently stop it matching.
func TestBootstrapBodyCarriesMarker(t *testing.T) {
	if !strings.Contains(bootstrapSkillBody, bootstrapBodyMarker) {
		t.Fatalf("the installed body must contain %q, or removeLegacyInstalls stops recognizing installs this CLI wrote", bootstrapBodyMarker)
	}
}

// A target we cannot clean must not strand the others.
func TestRemoveLegacyInstallsContinuesPastAFailure(t *testing.T) {
	if len(installTargets) < 2 {
		t.Skip("needs at least two targets to show one failure not stranding another")
	}
	// The failure is forced with directory permissions, which root ignores.
	if os.Geteuid() == 0 {
		t.Skip("running as root: a sealed directory is still removable")
	}

	base := t.TempDir()
	body := "---\nname: cli\n---\n\n" + bootstrapBodyMarker + ", `cio`.\n"

	dirs := make([]string, 0, len(installTargets))
	for _, target := range installTargets {
		dir := filepath.Join(base, target.subdir, legacyBootstrapDir)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		dirs = append(dirs, dir)
	}

	// Seal the first target's parent so its removal fails.
	sealed := filepath.Dir(dirs[0])
	if err := os.Chmod(sealed, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(sealed, 0o755) })

	removed, err := removeLegacyInstalls(base, installTargets, false)
	if err == nil {
		t.Fatal("expected the blocked target to surface an error")
	}

	reported := make(map[string]bool, len(removed))
	for _, r := range removed {
		reported[r] = true
	}
	for _, dir := range dirs[1:] {
		if _, statErr := os.Stat(dir); !os.IsNotExist(statErr) {
			t.Errorf("expected %s removed despite the earlier failure, got err=%v", dir, statErr)
		}
		if !reported[dir] {
			t.Errorf("expected %s reported as removed, got %v", dir, removed)
		}
	}
}
