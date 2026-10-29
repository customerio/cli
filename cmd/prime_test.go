package cmd

import (
	"strings"
	"testing"
)

func TestPrime_UsesDefaultTokenPageURL(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CIO_API_URL", "")
	t.Setenv("CIO_UI_URL", "")

	stdout, _, err := executeCommand("prime")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(stdout, "Open https://fly.customer.io/cli and sign in.") {
		t.Errorf("expected the production token page in prime output")
	}
}

func TestPrime_NamesSignupForNewAccounts(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	stdout, _, err := executeCommand("prime")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, want := range []string{"cio auth signup start", "cio auth signup verify", "ask the user which email address"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("expected prime output to name %q", want)
		}
	}
}

func TestPrime_UsesResolvedTokenPageURL(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CIO_API_URL", "")
	t.Setenv("CIO_UI_URL", "https://ui.example.com")

	stdout, _, err := executeCommand("prime")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(stdout, "Open https://ui.example.com/cli and sign in.") {
		t.Errorf("expected the CIO_UI_URL token page in prime output")
	}
	if strings.Contains(stdout, defaultCLILoginURL) {
		t.Errorf("prime output still names the production token page")
	}
}
