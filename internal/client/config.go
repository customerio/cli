package client

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// DefaultProfileName is the profile used when none is selected. A legacy flat
// config.json is migrated under this name on first read.
const DefaultProfileName = "default"

// maxProfileNameLen bounds a profile name. Names are config-map keys and appear
// in JSON and CLI output, so they're kept short and to a conservative charset.
const maxProfileNameLen = 64

// validProfileName matches an allowed profile name: letters, digits, dot, dash,
// and underscore. This keeps names safe to use as map keys, JSON keys, and shell
// arguments without surprising quoting or lookalike characters.
var validProfileName = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// ValidateProfileName reports whether name is an acceptable profile name. It is
// enforced wherever a profile can be created (the active profile on write) so an
// adversarial --profile or CIO_PROFILE value can't land odd keys in the config.
func ValidateProfileName(name string) error {
	if name == "" {
		return fmt.Errorf("profile name must not be empty")
	}
	if len(name) > maxProfileNameLen {
		return fmt.Errorf("profile name must be at most %d characters", maxProfileNameLen)
	}
	if !validProfileName.MatchString(name) {
		return fmt.Errorf("profile name %q is invalid: use only letters, digits, '.', '-', and '_'", name)
	}
	return nil
}

// storedConfig is the on-disk shape of ~/.cio/config.json. It holds one or more
// named profiles, each with its own credentials, region, and optional base URL.
type storedConfig struct {
	// CurrentProfile is the profile used when --profile / CIO_PROFILE are unset.
	CurrentProfile string `json:"current_profile,omitempty"`
	// Profiles maps a profile name to its credentials.
	Profiles map[string]*Credentials `json:"profiles"`
}

// activeProfile is the profile name selected for this invocation (from the
// --profile flag). Empty means "fall back to CIO_PROFILE, then current_profile,
// then default". Guarded because tests and concurrent goroutines may touch it.
var (
	activeProfileMu sync.RWMutex
	activeProfile   string
)

// SetActiveProfile records the profile selected by the --profile flag. Called
// once early in the root PersistentPreRunE, before any credential access.
func SetActiveProfile(name string) {
	activeProfileMu.Lock()
	activeProfile = strings.TrimSpace(name)
	activeProfileMu.Unlock()
}

func explicitActiveProfile() string {
	activeProfileMu.RLock()
	defer activeProfileMu.RUnlock()
	return activeProfile
}

// ActiveProfileName resolves the active profile name in priority order:
// --profile flag > CIO_PROFILE env > stored current_profile > default.
func ActiveProfileName() string {
	cfg, _ := readConfig()
	return resolveProfileName(cfg)
}

func resolveProfileName(cfg *storedConfig) string {
	if v := explicitActiveProfile(); v != "" {
		return v
	}

	if v := strings.TrimSpace(os.Getenv("CIO_PROFILE")); v != "" {
		return v
	}

	return storedCurrentProfile(cfg)
}

// storedCurrentProfile returns the profile persisted as current_profile, ignoring
// any session override from --profile / CIO_PROFILE. Used where the persisted
// default matters (e.g. 'cio profile list'), not the per-invocation selection.
func storedCurrentProfile(cfg *storedConfig) string {
	if cfg != nil && cfg.CurrentProfile != "" {
		return cfg.CurrentProfile
	}
	return DefaultProfileName
}

// readConfig reads ~/.cio/config.json, migrating a legacy flat credentials file
// into a single "default" profile. Returns the underlying read error (including
// os.ErrNotExist) when the file is absent.
func readConfig() (*storedConfig, error) {
	path, err := configFilePath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config file: %w", err)
	}
	return parseConfig(data)
}

// parseConfig turns raw config bytes into a Config, handling both the new
// multi-profile shape and the legacy flat Credentials object.
func parseConfig(data []byte) (*storedConfig, error) {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(data, &probe); err != nil {
		return nil, fmt.Errorf("parse config file: %w", err)
	}

	// New format carries a "profiles" key. Anything else is a legacy flat file.
	if _, ok := probe["profiles"]; ok {
		var cfg storedConfig
		if err := json.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("parse config file: %w", err)
		}
		if cfg.Profiles == nil {
			cfg.Profiles = map[string]*Credentials{}
		}
		return &cfg, nil
	}

	var legacy Credentials
	if err := json.Unmarshal(data, &legacy); err != nil {
		return nil, fmt.Errorf("parse config file: %w", err)
	}
	return &storedConfig{
		CurrentProfile: DefaultProfileName,
		Profiles:       map[string]*Credentials{DefaultProfileName: &legacy},
	}, nil
}

// readConfigOrEmpty is the write-path counterpart to readConfig: a missing or
// unreadable file yields an empty Config rather than an error, so the first
// write bootstraps the file.
func readConfigOrEmpty() *storedConfig {
	cfg, err := readConfig()
	if err != nil {
		return &storedConfig{Profiles: map[string]*Credentials{}}
	}
	if cfg.Profiles == nil {
		cfg.Profiles = map[string]*Credentials{}
	}
	return cfg
}

// writeConfigLocked performs an atomic write (temp file + rename) of the config
// file. Callers must already hold the config-dir lock.
func writeConfigLocked(cfg *storedConfig) error {
	dir, err := configDirPath()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(dir, configDirMode); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(dir, configFileName+".tmp.*")
	if err != nil {
		return fmt.Errorf("create temp config: %w", err)
	}
	tmpName := tmp.Name()
	// Cleans up on any error path; a no-op after a successful Rename.
	defer os.Remove(tmpName)

	if err := tmp.Chmod(configFileMode); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod temp config: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp config: %w", err)
	}

	if err := os.Rename(tmpName, filepath.Join(dir, configFileName)); err != nil {
		return fmt.Errorf("rename temp config: %w", err)
	}

	return nil
}

// ProfileInfo is a redacted view of a stored profile for display.
type ProfileInfo struct {
	Name      string `json:"name"`
	Current   bool   `json:"current"`
	Token     string `json:"token,omitempty"`
	Region    string `json:"region,omitempty"`
	APIURL    string `json:"api_url,omitempty"`
	AccountID string `json:"account_id,omitempty"`
}

// ListProfiles returns all stored profiles, sorted by name, with tokens masked.
func ListProfiles() ([]ProfileInfo, error) {
	cfg, err := readConfig()
	if err != nil {
		return nil, err
	}

	// Mark the persisted default, not the per-invocation selection, so the list
	// reflects what 'cio profile use' set regardless of any --profile/CIO_PROFILE
	// override active for this command.
	current := storedCurrentProfile(cfg)
	out := make([]ProfileInfo, 0, len(cfg.Profiles))
	for name, creds := range cfg.Profiles {
		info := ProfileInfo{Name: name, Current: name == current}
		if creds != nil {
			info.Token = MaskToken(creds.ServiceAccountToken)
			info.Region = creds.Region
			info.APIURL = creds.APIURL
			info.AccountID = creds.AccountID
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// CurrentProfileName returns the profile persisted as current_profile, ignoring
// any --profile / CIO_PROFILE override for this invocation. Use ActiveProfileName
// for the profile actually selected for the current command.
func CurrentProfileName() string {
	cfg, _ := readConfig()
	return storedCurrentProfile(cfg)
}

// ProfileExists reports whether a profile with the given name is stored.
func ProfileExists(name string) bool {
	cfg, err := readConfig()
	if err != nil {
		return false
	}
	_, ok := cfg.Profiles[name]
	return ok
}

// SetCurrentProfile points current_profile at an existing profile.
func SetCurrentProfile(name string) error {
	unlock, err := lockConfigDir()
	if err != nil {
		return err
	}
	defer unlock()

	cfg := readConfigOrEmpty()
	if _, ok := cfg.Profiles[name]; !ok {
		return fmt.Errorf("profile %q not found", name)
	}
	cfg.CurrentProfile = name
	return writeConfigLocked(cfg)
}

// RemoveProfile deletes a profile. If it was the current one, current_profile is
// repointed to another profile (or cleared). When the last profile is removed,
// the config file is deleted entirely.
func RemoveProfile(name string) error {
	unlock, err := lockConfigDir()
	if err != nil {
		return err
	}
	defer unlock()

	cfg := readConfigOrEmpty()
	if _, ok := cfg.Profiles[name]; !ok {
		return fmt.Errorf("profile %q not found", name)
	}
	delete(cfg.Profiles, name)

	if len(cfg.Profiles) == 0 {
		return removeConfigFile()
	}

	if cfg.CurrentProfile == name {
		cfg.CurrentProfile = anyProfileName(cfg.Profiles)
	}
	return writeConfigLocked(cfg)
}

// anyProfileName returns a deterministic profile name from the map (lowest by
// sort order) so repointing current_profile is stable.
func anyProfileName(profiles map[string]*Credentials) string {
	names := make([]string, 0, len(profiles))
	for n := range profiles {
		names = append(names, n)
	}
	if len(names) == 0 {
		return ""
	}
	sort.Strings(names)
	return names[0]
}

func removeConfigFile() error {
	path, err := configFilePath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove config file: %w", err)
	}
	return nil
}
