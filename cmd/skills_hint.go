package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

// maybeHintSkillsInstall prints a one-time hint to stderr when the bootstrap
// skill hasn't been installed yet. It only fires on an interactive terminal and
// skips commands where the hint would be noise (skills install itself, prime,
// help, completion).
func maybeHintSkillsInstall(cmd *cobra.Command) {
	if !shouldHintSkillsInstall(cmd) {
		return
	}
	fmt.Fprintln(cmd.ErrOrStderr(), "Tip: run \"cio skills install\" to set up AI agent skills for Claude Code and Codex.")
}

func shouldHintSkillsInstall(cmd *cobra.Command) bool {
	path := cmd.CommandPath()
	if strings.HasPrefix(path, "cio skills") ||
		strings.HasPrefix(path, "cio prime") ||
		strings.HasPrefix(path, "cio help") ||
		strings.HasPrefix(path, "cio completion") {
		return false
	}

	stderr, ok := cmd.ErrOrStderr().(*os.File)
	if !ok || !isTerminalInput(stderr.Fd()) {
		return false
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}

	skillPath := filepath.Join(home, ".claude", "skills", "cli", "SKILL.md")
	if _, err := os.Stat(skillPath); err == nil {
		return false
	}

	return true
}
