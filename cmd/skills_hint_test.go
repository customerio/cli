package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBootstrapSkillInstalled(t *testing.T) {
	write := func(home, name string) {
		dir := filepath.Join(home, ".claude", "skills", name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\n---\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("nothing installed", func(t *testing.T) {
		if bootstrapSkillInstalled(t.TempDir()) {
			t.Error("expected the hint to fire on a home with no skills")
		}
	})

	t.Run("current name installed", func(t *testing.T) {
		home := t.TempDir()
		write(home, bootstrapSkillName)
		if !bootstrapSkillInstalled(home) {
			t.Error("expected the hint to be suppressed once the bootstrap is installed")
		}
	})

	// A leftover under the old name is the thing we want replaced, so it must
	// not pass as installed and silence the nudge to re-run install.
	t.Run("only the legacy name installed", func(t *testing.T) {
		home := t.TempDir()
		write(home, legacyBootstrapDir)
		if bootstrapSkillInstalled(home) {
			t.Error("expected a legacy leftover not to count as installed")
		}
	})
}
