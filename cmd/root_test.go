package cmd

import (
	"strings"
	"testing"

	"github.com/customerio/cli/internal/useragent"
)

func TestRootHelpListsAllCommands(t *testing.T) {
	stdout, _, err := executeCommand("--help")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The branded help screen must enumerate every registered command, not a
	// curated subset, so newly added commands can't silently go missing.
	if !strings.Contains(stdout, "All commands:") {
		t.Fatalf("help missing 'All commands:' section:\n%s", stdout)
	}
	for _, name := range []string{"api", "auth", "domains", "prime", "schema", "send", "skills", "transactional"} {
		if !strings.Contains(stdout, name) {
			t.Errorf("help output missing command %q", name)
		}
	}
}

func TestSetVersionIgnoresEmptyVersion(t *testing.T) {
	oldRootVersion := rootCmd.Version
	t.Cleanup(func() {
		rootCmd.Version = oldRootVersion
		useragent.SetVersion("dev")
	})

	SetVersion("v1.2.3")
	SetVersion("")

	if got := rootCmd.Version; got != "v1.2.3" {
		t.Fatalf("rootCmd.Version = %q, want %q", got, "v1.2.3")
	}
	if got, want := useragent.Get(), "Customer.io-CLI/v1.2.3 (+https://github.com/customerio/cli)"; got != want {
		t.Fatalf("useragent.Get() = %q, want %q", got, want)
	}
}
