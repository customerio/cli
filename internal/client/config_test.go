package client

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// resetActiveProfile clears the package-level profile selection so tests don't
// leak state into each other.
func resetActiveProfile(t *testing.T) {
	t.Helper()
	SetActiveProfile("")
	t.Cleanup(func() { SetActiveProfile("") })
}

func TestReadConfig_MigratesLegacyFlatFile(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	t.Setenv("CIO_PROFILE", "")
	resetActiveProfile(t)

	// Write a legacy flat credentials file (no "profiles" key).
	dir := filepath.Join(tmpDir, ".cio")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	legacy := Credentials{ServiceAccountToken: "sa_live_legacy", Region: "eu", AccountID: "7"}
	data, _ := json.Marshal(legacy)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), data, 0600); err != nil {
		t.Fatal(err)
	}

	creds, err := ReadCredentials()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if creds.ServiceAccountToken != "sa_live_legacy" || creds.Region != "eu" {
		t.Errorf("legacy file not migrated: %+v", creds)
	}

	profiles, err := ListProfiles()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(profiles) != 1 || profiles[0].Name != DefaultProfileName || !profiles[0].Current {
		t.Errorf("expected single current default profile, got %+v", profiles)
	}
}

func TestWriteCredentials_MigratesFileToNewFormat(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	t.Setenv("CIO_PROFILE", "")
	resetActiveProfile(t)

	if err := WriteCredentials(&Credentials{ServiceAccountToken: "sa_live_x"}); err != nil {
		t.Fatalf("write: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(tmpDir, ".cio", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var onDisk map[string]json.RawMessage
	if err := json.Unmarshal(raw, &onDisk); err != nil {
		t.Fatal(err)
	}
	if _, ok := onDisk["profiles"]; !ok {
		t.Errorf("expected new format with profiles key, got %s", raw)
	}
	if _, ok := onDisk["current_profile"]; !ok {
		t.Errorf("expected current_profile to be initialized, got %s", raw)
	}
}

func TestProfiles_IsolatedByName(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	t.Setenv("CIO_PROFILE", "")
	resetActiveProfile(t)

	SetActiveProfile(DefaultProfileName)
	if err := WriteCredentials(&Credentials{ServiceAccountToken: "sa_live_default", Region: "us"}); err != nil {
		t.Fatalf("write default: %v", err)
	}

	SetActiveProfile("staging")
	if err := WriteCredentials(&Credentials{ServiceAccountToken: "sa_live_staging", APIURL: "https://staging.example"}); err != nil {
		t.Fatalf("write staging: %v", err)
	}

	// Each profile resolves its own token and base URL.
	SetActiveProfile("staging")
	if got := ResolveServiceAccountToken(""); got != "sa_live_staging" {
		t.Errorf("staging token: got %q", got)
	}
	if got := ResolveBaseURL("", false); got != "https://staging.example" {
		t.Errorf("staging base URL: got %q", got)
	}

	SetActiveProfile(DefaultProfileName)
	if got := ResolveServiceAccountToken(""); got != "sa_live_default" {
		t.Errorf("default token: got %q", got)
	}
	if got := ResolveBaseURL("", false); got != BaseURLForRegion("us") {
		t.Errorf("default base URL: got %q", got)
	}
}

func TestActiveProfileName_Precedence(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	resetActiveProfile(t)

	// Seed two profiles with current_profile=default.
	SetActiveProfile(DefaultProfileName)
	if err := WriteCredentials(&Credentials{ServiceAccountToken: "sa_live_default"}); err != nil {
		t.Fatal(err)
	}
	SetActiveProfile("alt")
	if err := WriteCredentials(&Credentials{ServiceAccountToken: "sa_live_alt"}); err != nil {
		t.Fatal(err)
	}
	if err := SetCurrentProfile(DefaultProfileName); err != nil {
		t.Fatal(err)
	}

	// current_profile wins when no flag/env.
	SetActiveProfile("")
	t.Setenv("CIO_PROFILE", "")
	if got := ActiveProfileName(); got != DefaultProfileName {
		t.Errorf("expected current_profile default, got %q", got)
	}

	// CIO_PROFILE overrides current_profile.
	t.Setenv("CIO_PROFILE", "alt")
	if got := ActiveProfileName(); got != "alt" {
		t.Errorf("expected env alt, got %q", got)
	}

	// Explicit --profile (SetActiveProfile) wins over env.
	SetActiveProfile("default")
	if got := ActiveProfileName(); got != "default" {
		t.Errorf("expected explicit default, got %q", got)
	}
}

func TestRemoveProfile_RepointsCurrent(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	t.Setenv("CIO_PROFILE", "")
	resetActiveProfile(t)

	SetActiveProfile("a")
	if err := WriteCredentials(&Credentials{ServiceAccountToken: "sa_live_a"}); err != nil {
		t.Fatal(err)
	}
	SetActiveProfile("b")
	if err := WriteCredentials(&Credentials{ServiceAccountToken: "sa_live_b"}); err != nil {
		t.Fatal(err)
	}
	if err := SetCurrentProfile("b"); err != nil {
		t.Fatal(err)
	}

	// Removing the current profile repoints current_profile to the survivor.
	SetActiveProfile("")
	if err := RemoveProfile("b"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if got := CurrentProfileName(); got != "a" {
		t.Errorf("expected current repointed to a, got %q", got)
	}
	if ProfileExists("b") {
		t.Errorf("profile b should be gone")
	}
}

func TestRemoveProfile_LastDeletesFile(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	t.Setenv("CIO_PROFILE", "")
	resetActiveProfile(t)

	SetActiveProfile("only")
	if err := WriteCredentials(&Credentials{ServiceAccountToken: "sa_live_only"}); err != nil {
		t.Fatal(err)
	}
	if err := RemoveProfile("only"); err != nil {
		t.Fatalf("remove: %v", err)
	}

	if _, err := os.Stat(filepath.Join(tmpDir, ".cio", "config.json")); !os.IsNotExist(err) {
		t.Errorf("expected config file deleted after last profile removed, got err=%v", err)
	}
}

func TestValidateProfileName(t *testing.T) {
	valid := []string{"default", "staging", "account-47", "acct_49", "us.prod", "A1"}
	for _, name := range valid {
		if err := ValidateProfileName(name); err != nil {
			t.Errorf("expected %q to be valid, got %v", name, err)
		}
	}

	invalid := []string{"", "has space", "../escape", "name/slash", "emoji😀", strings.Repeat("a", maxProfileNameLen+1)}
	for _, name := range invalid {
		if err := ValidateProfileName(name); err == nil {
			t.Errorf("expected %q to be rejected", name)
		}
	}
}

func TestWriteCredentials_RejectsInvalidProfileName(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	t.Setenv("CIO_PROFILE", "")
	resetActiveProfile(t)

	SetActiveProfile("bad name")
	if err := WriteCredentials(&Credentials{ServiceAccountToken: "sa_live_x"}); err == nil {
		t.Fatal("expected write to reject an invalid active profile name")
	}

	// Nothing should have been persisted for the rejected name.
	if _, err := os.Stat(filepath.Join(tmpDir, ".cio", "config.json")); !os.IsNotExist(err) {
		t.Errorf("expected no config file after rejected write, got err=%v", err)
	}
}

func TestListProfiles_CurrentTracksStoredNotOverride(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	t.Setenv("CIO_PROFILE", "")
	resetActiveProfile(t)

	SetActiveProfile(DefaultProfileName)
	if err := WriteCredentials(&Credentials{ServiceAccountToken: "sa_live_default"}); err != nil {
		t.Fatal(err)
	}
	SetActiveProfile("staging")
	if err := WriteCredentials(&Credentials{ServiceAccountToken: "sa_live_staging"}); err != nil {
		t.Fatal(err)
	}
	if err := SetCurrentProfile(DefaultProfileName); err != nil {
		t.Fatal(err)
	}

	// A per-invocation override (flag or env) must not change which profile the
	// listing marks as current — that reflects the persisted current_profile.
	SetActiveProfile("staging")
	t.Setenv("CIO_PROFILE", "staging")
	profiles, err := ListProfiles()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, p := range profiles {
		if p.Name == DefaultProfileName && !p.Current {
			t.Errorf("expected stored current %q to be marked current", DefaultProfileName)
		}
		if p.Name == "staging" && p.Current {
			t.Errorf("session override %q should not be marked current", "staging")
		}
	}
}
