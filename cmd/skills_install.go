package cmd

import (
	"bufio"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/customerio/cli/internal/output"
	"github.com/customerio/cli/internal/skills"
	"github.com/customerio/cli/internal/tui"
	"github.com/spf13/cobra"
)

// bootstrapSkillBody is the SKILL.md body written on install: a thin pointer to
// `cio prime`, which holds the full, current CLI instructions. Keeping the
// installed file minimal avoids shipping a stale copy of guidance that lives
// behind `cio prime` and `cio skills read`.
//
//go:embed bootstrap_skill.md
var bootstrapSkillBody string

// installTarget describes one agent's on-disk skills layout. The directory is
// resolved relative to the install base (home dir for --global, cwd for
// --project).
type installTarget struct {
	// name is the value accepted by --target.
	name string
	// label is the display name shown in the human-readable summary.
	label string
	// subdir is the path under the install base where skill folders live.
	subdir string
}

// installTargets maps --target names to their on-disk layout.
//
//   - claude — Claude Code reads ~/.claude/skills/<name>/SKILL.md (global) or
//     ./.claude/skills/<name>/SKILL.md (project).
//   - codex — Codex, Cursor, Windsurf, and other agents that support the open
//     agent skills convention read .agents/skills/<name>/SKILL.md.
var installTargets = []installTarget{
	{name: "claude", label: "Claude Code", subdir: filepath.Join(".claude", "skills")},
	{name: "codex", label: "Codex", subdir: filepath.Join(".agents", "skills")},
}

// installedFile is one target's entry in the install result. A nil Files means
// the SKILL.md was already there and left untouched (no --force).
type installedFile struct {
	Skill  string   `json:"skill"`
	Target string   `json:"target"`
	Dir    string   `json:"dir"`
	Files  []string `json:"files"`
}

var skillsInstallCmd = &cobra.Command{
	Use:   "install",
	Short: "Install the Customer.io bootstrap skill into Claude Code and Codex",
	Long: `Write the Customer.io bootstrap skill to disk so Claude Code, Codex, and
other agents discover the CLI:

  Claude Code   ~/.claude/skills/<skill>/SKILL.md  (or ./.claude with --project)
  Codex/agents  ~/.agents/skills/<skill>/SKILL.md  (or ./.agents with --project)

Only the bootstrap skill is installed. Its SKILL.md routing index tells the
agent to pull every other reference (Journeys, CDP, Design Studio, recipes)
on demand from the backend via 'cio skills read <skill>', so nothing else is
copied locally and the served content is always current.

  cio skills install                  — install the bootstrap skill (prompts for scope)
  cio skills install --global         — install into your home directory
  cio skills install --project        — install into the current directory
  cio skills install --target claude  — install for Claude Code only
  cio skills install --force          — overwrite an existing SKILL.md`,
	Args: cobra.NoArgs,
	RunE: runSkillsInstall,
}

func init() {
	skillsInstallCmd.Flags().Bool("global", false, "Install into your home directory for use across all projects")
	skillsInstallCmd.Flags().Bool("project", false, "Install into the current directory only")
	skillsInstallCmd.Flags().String("target", "claude,codex", "Comma-separated agents to install for: claude, codex")
	skillsInstallCmd.Flags().Bool("force", false, "Overwrite skill files that already exist")
	skillsCmd.AddCommand(skillsInstallCmd)
}

func runSkillsInstall(cmd *cobra.Command, args []string) error {
	targets, err := resolveInstallTargets(cmd)
	if err != nil {
		return err
	}

	scope, err := resolveInstallScope(cmd)
	if err != nil {
		return err
	}

	base, err := resolveInstallBase(scope)
	if err != nil {
		output.PrintError(output.CodeGeneralError, err.Error(), nil)
		return err
	}

	resp, err := loadSkills(cmd)
	if err != nil {
		return err
	}

	selected, err := selectBootstrap(resp.Skills)
	if err != nil {
		return err
	}

	dryRun := GetDryRun(cmd)
	force, _ := cmd.Flags().GetBool("force")

	installed := make([]installedFile, 0, len(selected)*len(targets))
	for _, s := range selected {
		// The skill path and file names come from the server; never let them
		// escape the install directory.
		if _, err := safeRelPath(s.Path); err != nil {
			output.PrintError(output.CodeValidationError, err.Error(), map[string]any{"skill": s.Path})
			return err
		}
		for _, t := range targets {
			dir := filepath.Join(base, t.subdir, s.Path)
			files, err := writeSkill(dir, s, dryRun, force)
			if err != nil {
				output.PrintError(output.CodeGeneralError, err.Error(), map[string]any{
					"skill":  s.Path,
					"target": t.name,
					"dir":    dir,
				})
				return err
			}
			installed = append(installed, installedFile{
				Skill:  s.Path,
				Target: t.name,
				Dir:    dir,
				Files:  files,
			})
		}
	}

	removed, err := removeLegacyInstalls(base, targets, dryRun)
	if err != nil {
		// The skill itself is installed; a leftover we couldn't clear is worth
		// saying out loud but not worth failing the command over.
		fmt.Fprintf(cmd.ErrOrStderr(), "cio: %v\n", err)
	}

	// This CLI speaks JSON for agents and pipes, but a person who ran install at
	// a terminal gets a readable summary instead.
	if installWantsHumanOutput(cmd) {
		printInstallSummary(cmd.OutOrStdout(), dryRun, installed, removed)
		return nil
	}

	action := "installed"
	if dryRun {
		action = "would install"
	}
	return skillsOutput(cmd, map[string]any{
		"status":    "ok",
		"scope":     scope,
		"base":      base,
		"dry_run":   dryRun,
		"action":    action,
		"installed": installed,
		"removed":   removed,
	})
}

// legacyBootstrapDir is the directory an earlier install wrote the bootstrap
// skill into. Left in place it keeps claiming the generic `cli` name in the
// agent's skill list, which is how it ends up loaded for unrelated CLI work.
const legacyBootstrapDir = "cli"

// bootstrapBodyMarker is a phrase unique to the SKILL.md body this CLI writes.
// Only a directory whose SKILL.md carries it is ours to replace; a skill
// somebody else happens to have named `cli` is left alone.
const bootstrapBodyMarker = "Customer.io ships an agent-first CLI"

// removeLegacyInstalls deletes the pre-rename bootstrap directory for each
// target and returns the paths removed. One target failing does not strand the
// others: each is attempted, and the failures come back joined.
func removeLegacyInstalls(base string, targets []installTarget, dryRun bool) ([]string, error) {
	var removed []string
	var failures error
	for _, t := range targets {
		dir := filepath.Join(base, t.subdir, legacyBootstrapDir)
		body, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
		if err != nil || !strings.Contains(string(body), bootstrapBodyMarker) {
			continue
		}
		if !dryRun {
			if err := os.RemoveAll(dir); err != nil {
				failures = errors.Join(failures, fmt.Errorf("could not remove the earlier install at %s: %w", dir, err))
				continue
			}
		}
		removed = append(removed, dir)
	}
	return removed, failures
}

// installWantsHumanOutput is true only at an interactive terminal with no
// machine-output flag — --jq and --raw-output both mean the caller wants JSON.
func installWantsHumanOutput(cmd *cobra.Command) bool {
	if GetJQFlag(cmd) != "" || GetRawFlag(cmd) {
		return false
	}
	return writerIsTerminal(cmd.OutOrStdout())
}

// printInstallSummary renders the install result for a human, in place of the
// JSON that agents get.
func printInstallSummary(w io.Writer, dryRun bool, installed []installedFile, removed []string) {
	wrote := false
	width := 0
	for _, f := range installed {
		if len(f.Files) > 0 {
			wrote = true
		}
		if l := len(targetLabel(f.Target)); l > width {
			width = l
		}
	}

	switch {
	case dryRun:
		fmt.Fprintln(w, "Would install the Customer.io CLI skill:")
	case wrote:
		fmt.Fprintln(w, "Installed the Customer.io CLI skill:")
	default:
		fmt.Fprintln(w, "The Customer.io CLI skill is already installed:")
	}
	fmt.Fprintln(w)

	for _, f := range installed {
		path := tildeAbbrev(filepath.Join(f.Dir, "SKILL.md"))
		note := ""
		if !dryRun && len(f.Files) == 0 {
			note = "  (already present)"
		}
		fmt.Fprintf(w, "  %-*s%s%s\n", width+2, targetLabel(f.Target), path, note)
	}
	fmt.Fprintln(w)

	switch {
	case dryRun:
		fmt.Fprintln(w, "Run without --dry-run to write these files.")
	case !wrote:
		fmt.Fprintln(w, "Re-run with --force to overwrite.")
	default:
		fmt.Fprintln(w, "Your agent picks it up on its next run. Try: cio prime")
	}

	printRemovedLegacy(w, dryRun, removed)
}

// printRemovedLegacy tells a human which earlier installs were cleaned up.
func printRemovedLegacy(w io.Writer, dryRun bool, removed []string) {
	if len(removed) == 0 {
		return
	}

	verb := "Removed"
	if dryRun {
		verb = "Would remove"
	}

	fmt.Fprintln(w)
	fmt.Fprintf(w, "%s an earlier install that claimed the generic \"cli\" name:\n", verb)
	fmt.Fprintln(w)
	for _, dir := range removed {
		fmt.Fprintf(w, "  %s\n", tildeAbbrev(dir))
	}
}

func targetLabel(name string) string {
	for _, t := range installTargets {
		if t.name == name {
			return t.label
		}
	}
	return name
}

func tildeAbbrev(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if path == home {
		return "~"
	}
	if rest, ok := strings.CutPrefix(path, home+string(filepath.Separator)); ok {
		return "~" + string(filepath.Separator) + rest
	}
	return path
}

// writeSkill writes the bootstrap SKILL.md (a thin pointer to `cio prime`)
// into dir, carrying the skill's server-tuned name and description as
// frontmatter. It returns the names of the files written. When dryRun is set
// it reports what would be written without touching disk. When force is false,
// an existing SKILL.md is left untouched.
func writeSkill(dir string, s skills.Skill, dryRun, force bool) ([]string, error) {
	const name = "SKILL.md"

	if dryRun {
		return []string{name}, nil
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create %s: %w", dir, err)
	}

	path := filepath.Join(dir, name)
	if !force {
		if _, err := os.Stat(path); err == nil {
			// File exists and --force not set; leave it untouched.
			return nil, nil
		}
	}
	// Write a minimal bootstrap body that points the agent at `cio prime`,
	// keeping the server-tuned description for activation. The full guidance is
	// served by `cio prime` / `cio skills read`, not copied here.
	body := ensureFrontmatter(bootstrapSkillBody, s.Path, s.Description)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return nil, fmt.Errorf("write %s: %w", path, err)
	}
	return []string{name}, nil
}

// ensureFrontmatter guarantees a SKILL.md begins with YAML frontmatter. Agent
// runtimes (Codex / open agent skills, Claude Code) require a `---`-delimited
// block with at least name and description; the server's authored content and
// the synthesized index for entrypoint-less skills don't carry one, so we
// prepend it from the skill's metadata when missing.
func ensureFrontmatter(content, name, description string) string {
	if strings.HasPrefix(content, "---\n") || strings.HasPrefix(content, "---\r\n") {
		return content
	}
	var b strings.Builder
	fmt.Fprintf(&b, "---\nname: %s\ndescription: %s\n---\n\n", yamlScalar(name), yamlScalar(description))
	b.WriteString(content)
	return b.String()
}

// yamlScalar renders s as a safe single-line double-quoted YAML scalar,
// collapsing internal whitespace (including newlines) so the frontmatter stays
// a valid one-line value.
func yamlScalar(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}

// safeRelPath validates a server-supplied skill path or file name before it is
// used to build a filesystem path. It rejects absolute paths and any name that
// would escape its install directory via "..".
func safeRelPath(name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("empty path")
	}
	clean := filepath.Clean(name)
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("unsafe path %q", name)
	}
	return clean, nil
}

// bootstrapSkillName is the path the bootstrap skill is served under, and so the
// directory and frontmatter name it installs as. The bootstrap is the only skill
// installed locally; its routing index points the agent at every other
// reference, fetched from the backend on demand via `cio skills read <skill>`.
const bootstrapSkillName = "cio"

// selectBootstrap returns the single bootstrap skill to install. The other
// skills are intentionally not written to disk — they are served by the API
// and read at runtime so their content stays current.
//
// A backend that serves the bootstrap under the retired name is an error rather
// than something to fall back on: installing under that name is what makes an
// agent load this skill for unrelated CLI work, so failing here is better than
// quietly writing it.
func selectBootstrap(all []skills.Skill) ([]skills.Skill, error) {
	available := make([]string, 0, len(all))
	for _, s := range all {
		if s.Path == bootstrapSkillName {
			return []skills.Skill{s}, nil
		}
		available = append(available, s.Path)
	}

	err := fmt.Errorf("could not find the bootstrap skill %q", bootstrapSkillName)
	output.PrintError(output.CodeGeneralError, err.Error(), map[string]any{
		"available_skills": available,
	})
	return nil, err
}

// resolveInstallTargets validates --target and returns the matching layouts.
func resolveInstallTargets(cmd *cobra.Command) ([]installTarget, error) {
	raw, _ := cmd.Flags().GetString("target")
	requested := strings.Split(raw, ",")
	out := make([]installTarget, 0, len(requested))
	seen := make(map[string]bool, len(requested))
	for _, name := range requested {
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		var match *installTarget
		for i := range installTargets {
			if installTargets[i].name == name {
				match = &installTargets[i]
				break
			}
		}
		if match == nil {
			valid := make([]string, len(installTargets))
			for i, t := range installTargets {
				valid[i] = t.name
			}
			err := fmt.Errorf("unknown target %q", name)
			output.PrintError(output.CodeValidationError, err.Error(), map[string]any{
				"valid_targets": valid,
			})
			return nil, err
		}
		out = append(out, *match)
	}
	if len(out) == 0 {
		err := fmt.Errorf("no install targets selected")
		output.PrintError(output.CodeValidationError, err.Error(), nil)
		return nil, err
	}
	return out, nil
}

// resolveInstallScope decides between "global" and "project". An explicit
// --global/--project flag wins; otherwise it prompts on an interactive
// terminal, and falls back to "global" when input is not a TTY.
func resolveInstallScope(cmd *cobra.Command) (string, error) {
	global, _ := cmd.Flags().GetBool("global")
	project, _ := cmd.Flags().GetBool("project")
	switch {
	case global && project:
		err := fmt.Errorf("--global and --project are mutually exclusive")
		output.PrintError(output.CodeValidationError, err.Error(), nil)
		return "", err
	case global:
		return "global", nil
	case project:
		return "project", nil
	}

	in := cmd.InOrStdin()
	stderr := cmd.ErrOrStderr()
	if !inputIsTerminal(in) {
		// Non-interactive (CI, agent, piped): default to global.
		return "global", nil
	}

	if writerIsTerminal(stderr) {
		fmt.Fprintln(stderr, tui.Logo(stderr))
		fmt.Fprintln(stderr)
	}
	return promptScope(in, stderr)
}

// promptScope asks the user where to install and returns "global" or "project".
//
// It installs its own SIGINT/SIGTERM handler for the duration of the read.
// When the CLI is launched by a parent that already ignores SIGINT (agents,
// IDEs, some job-control setups), Go inherits that disposition and Ctrl+C would
// otherwise be swallowed, leaving the blocking read — and the whole app — hung.
// Notifying here overrides the inherited disposition so Ctrl+C aborts cleanly.
func promptScope(in io.Reader, out io.Writer) (string, error) {
	fmt.Fprintln(out, "Where should the skills be installed?")
	fmt.Fprintln(out, "  [1] Global  — your home directory (~/.claude, ~/.agents), available in every project")
	fmt.Fprintln(out, "  [2] Project — the current directory (./.claude, ./.agents) only")
	fmt.Fprint(out, "Choose [1/2] (default 1): ")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	line, err := readLineContext(ctx, in)
	if err != nil {
		if ctx.Err() != nil {
			// Interrupted: end the prompt line and abort without a stack of noise.
			fmt.Fprintln(out)
			return "", fmt.Errorf("installation cancelled")
		}
		return "", fmt.Errorf("failed to read choice: %w", err)
	}

	switch strings.TrimSpace(line) {
	case "", "1", "g", "global":
		return "global", nil
	case "2", "p", "project":
		return "project", nil
	default:
		err := fmt.Errorf("invalid choice; expected 1 or 2")
		output.PrintError(output.CodeValidationError, err.Error(), nil)
		return "", err
	}
}

// readLineContext reads a single line from in, returning early with ctx.Err()
// if ctx is cancelled (e.g. by SIGINT) before input arrives.
//
// Note: on cancellation the reader goroutine is abandoned, still blocked on
// Scan(). That is only safe because the sole caller (the install scope prompt)
// treats cancellation as a fatal abort and the process exits immediately. Keep
// this function internal; do not reuse it on a path that continues running, or
// the leaked goroutine will accumulate.
func readLineContext(ctx context.Context, in io.Reader) (string, error) {
	lines := make(chan string, 1)
	errs := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(in)
		if scanner.Scan() {
			lines <- scanner.Text()
			return
		}
		if err := scanner.Err(); err != nil {
			errs <- err
			return
		}
		lines <- "" // EOF with no input
	}()

	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case err := <-errs:
		return "", err
	case line := <-lines:
		return line, nil
	}
}

// resolveInstallBase returns the directory under which target subdirs are
// created: the home dir for "global", the working directory for "project".
func resolveInstallBase(scope string) (string, error) {
	if scope == "project" {
		return os.Getwd()
	}
	return os.UserHomeDir()
}

// inputIsTerminal reports whether the reader is an interactive terminal.
func inputIsTerminal(in io.Reader) bool {
	f, ok := in.(*os.File)
	if !ok {
		return false
	}
	return isTerminalInput(f.Fd())
}

// writerIsTerminal reports whether the writer is an interactive terminal, so
// the decorative banner is only printed for humans. (term.IsTerminal works on
// any FD, so reusing isTerminalInput here is fine.)
func writerIsTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	return isTerminalInput(f.Fd())
}
