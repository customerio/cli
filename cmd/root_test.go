package cmd

import (
	"testing"

	"github.com/customerio/cli/internal/useragent"
)

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
