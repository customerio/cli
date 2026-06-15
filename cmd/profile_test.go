package cmd

import (
	"encoding/json"
	"testing"

	"github.com/customerio/cli/internal/client"
)

func TestProfileUse_UnknownProfileErrors(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	t.Setenv("CIO_TOKEN", "")
	t.Setenv("CIO_PROFILE", "")

	_, _, err := executeCommand("profile", "use", "nope")
	if err == nil {
		t.Fatal("expected error switching to unknown profile")
	}
}

func TestAuthLoginProfile_CreatesAndSwitches(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	t.Setenv("CIO_TOKEN", "")
	t.Setenv("CIO_PROFILE", "")

	defaultSrv := oauthServer(t, "sa_live_default")
	defer defaultSrv.Close()
	stagingSrv := oauthServer(t, "sa_live_staging")
	defer stagingSrv.Close()

	// Log into the default profile.
	if _, _, err := executeCommand("auth", "login", "sa_live_default", "--api-url", defaultSrv.URL); err != nil {
		t.Fatalf("default login: %v", err)
	}

	// Log into a named "staging" profile with a custom URL — should switch to it.
	stdout, _, err := executeCommand("auth", "login", "sa_live_staging",
		"--profile", "staging", "--api-url", stagingSrv.URL)
	if err != nil {
		t.Fatalf("staging login: %v", err)
	}
	var loginResult map[string]any
	if err := json.Unmarshal([]byte(stdout), &loginResult); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, stdout)
	}
	if loginResult["profile"] != "staging" {
		t.Errorf("expected profile staging in login output, got %v", loginResult["profile"])
	}

	// current_profile should now be staging, with the custom URL persisted.
	if got := client.CurrentProfileName(); got != "staging" {
		t.Errorf("expected current profile staging, got %q", got)
	}
	client.SetActiveProfile("staging")
	creds, err := client.ReadCredentials()
	if err != nil {
		t.Fatalf("read staging: %v", err)
	}
	if creds.ServiceAccountToken != "sa_live_staging" {
		t.Errorf("staging token: got %q", creds.ServiceAccountToken)
	}
	if creds.APIURL != stagingSrv.URL {
		t.Errorf("expected persisted api_url %q, got %q", stagingSrv.URL, creds.APIURL)
	}
	client.SetActiveProfile("")

	// profile list shows both, with staging current.
	stdout, _, err = executeCommand("profile", "list")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var listResult struct {
		Profiles []client.ProfileInfo `json:"profiles"`
	}
	if err := json.Unmarshal([]byte(stdout), &listResult); err != nil {
		t.Fatalf("invalid list JSON: %v\n%s", err, stdout)
	}
	if len(listResult.Profiles) != 2 {
		t.Fatalf("expected 2 profiles, got %+v", listResult.Profiles)
	}
	for _, p := range listResult.Profiles {
		if p.Name == "staging" && !p.Current {
			t.Errorf("expected staging marked current")
		}
		if p.Name == "default" && p.Current {
			t.Errorf("default should not be current")
		}
	}

	// Switch back to default.
	if _, _, err := executeCommand("profile", "use", "default"); err != nil {
		t.Fatalf("use default: %v", err)
	}
	if got := client.CurrentProfileName(); got != "default" {
		t.Errorf("expected current default after use, got %q", got)
	}

	// Remove staging.
	if _, _, err := executeCommand("profile", "remove", "staging"); err != nil {
		t.Fatalf("remove staging: %v", err)
	}
	if client.ProfileExists("staging") {
		t.Errorf("staging should be removed")
	}
}
