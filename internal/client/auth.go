package client

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/customerio/cli/internal/filelock"
)

const (
	configDirName  = ".cio"
	configFileName = "config.json"
	configLockName = "config.lock"
	configFileMode = 0600
	configDirMode  = 0700

	// ServiceAccountTokenPrefix is the prefix for production service account credentials.
	// These must be exchanged for a short-lived JWT via the OAuth token endpoint.
	ServiceAccountTokenPrefix = "sa_live_"
	// SandboxServiceAccountTokenPrefix is the prefix for sandbox service account credentials.
	// Sandbox accounts use this token until go-live replaces it with production credentials.
	SandboxServiceAccountTokenPrefix = "sa_sandbox_"
)

var serviceAccountTokenPrefixes = []string{
	ServiceAccountTokenPrefix,
	SandboxServiceAccountTokenPrefix,
}

// Credentials holds the stored authentication credentials.
type Credentials struct {
	// ServiceAccountToken is the long-lived service account credential.
	ServiceAccountToken string `json:"service_account_token"`
	// AccountID is the account ID discovered during login.
	AccountID string `json:"account_id,omitempty"`
	// Region is "us" or "eu" — determines the base URL.
	Region string `json:"region,omitempty"`
	// AccessToken is the cached short-lived JWT (from OAuth exchange).
	AccessToken string `json:"access_token,omitempty"`
	// AccessTokenExpiresAt is when the cached JWT expires.
	AccessTokenExpiresAt time.Time `json:"access_token_expires_at,omitempty"`
	// ReadOnly indicates the cached access token was minted with scope=read_only.
	ReadOnly bool `json:"read_only,omitempty"`
	// Scopes holds additional OAuth scope values that were requested.
	Scopes []string `json:"scopes,omitempty"`
}

// ScopeReadOnly is the OAuth 2.0 scope value that requests a read-only session.
const ScopeReadOnly = "read_only"

// ScopeSeparator is the delimiter between multiple scope values in OAuth 2.0.
const ScopeSeparator = " "

// OAuthTokenResponse matches the RFC 6749 §5.1 response from the token endpoint.
type OAuthTokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
	Scope       string `json:"scope,omitempty"`
}

// BaseURLForRegion returns the production fly.customer.io URL for the given region.
func BaseURLForRegion(region string) string {
	switch strings.ToLower(strings.TrimSpace(region)) {
	case "eu":
		return "https://eu.fly.customer.io"
	default:
		return "https://us.fly.customer.io"
	}
}

// TrackBaseURLForRegion returns the track.customer.io URL for the given region.
// The track API hosts the transactional send endpoints (POST /v1/send/*).
// To override the host, set CIO_TRACK_URL.
func TrackBaseURLForRegion(region string) string {
	switch strings.ToLower(strings.TrimSpace(region)) {
	case "eu":
		return "https://track-eu.customer.io"
	default:
		return "https://track.customer.io"
	}
}

// RegionFromBaseURL extracts the region from a base URL, or empty string if unknown.
func RegionFromBaseURL(baseURL string) string {
	switch {
	case strings.Contains(baseURL, "eu.fly"):
		return "eu"
	case strings.Contains(baseURL, "us.fly"):
		return "us"
	default:
		return ""
	}
}

// ResolveAccessToken returns a pre-exchanged JWT from the CIO_ACCESS_TOKEN
// environment variable, if set. When provided, the CLI skips the OAuth token
// exchange and uses this JWT directly as the Bearer token.
func ResolveAccessToken() string {
	return os.Getenv("CIO_ACCESS_TOKEN")
}

// ResolveServiceAccountToken determines the sa_live_ token from (in priority order):
//  1. Explicit value (from --token flag)
//  2. CIO_TOKEN environment variable
//  3. ~/.cio/config.json file
//
// Returns empty string if no token is found.
func ResolveServiceAccountToken(explicit string) string {
	if explicit != "" {
		return explicit
	}

	if v := os.Getenv("CIO_TOKEN"); v != "" {
		return v
	}

	if creds, err := ReadCredentials(); err == nil && creds.ServiceAccountToken != "" {
		return creds.ServiceAccountToken
	}

	return ""
}

// ResolveRegion determines the region from (in priority order):
//  1. Explicit --api-url flag (extract region from URL)
//  2. CIO_REGION environment variable
//  3. ~/.cio/config.json
//  4. Default: "us"
func ResolveRegion(apiURL string, apiURLChanged bool) string {
	if apiURLChanged && apiURL != "" {
		if r := RegionFromBaseURL(apiURL); r != "" {
			return r
		}
	}

	if v := os.Getenv("CIO_REGION"); v != "" {
		return strings.ToLower(strings.TrimSpace(v))
	}

	if creds, err := ReadCredentials(); err == nil && creds.Region != "" {
		return creds.Region
	}

	return "us"
}

// CachedAccessToken returns the cached JWT if it's still valid (with 60s buffer).
// If readOnly is true, only returns a cached token that was minted with read-only scope.
// If readOnly is false, only returns a cached token that was NOT read-only (to avoid
// accidentally using a restricted token for a full-access session).
// The scopes parameter must match the cached scopes exactly for reuse.
func CachedAccessToken(readOnly bool, scopes []string) string {
	return cachedAccessToken("", readOnly, scopes)
}

// CachedAccessTokenForServiceAccount returns a cached JWT only when it belongs
// to the same service-account token that is active for this invocation.
func CachedAccessTokenForServiceAccount(serviceAccountToken string, readOnly bool, scopes []string) string {
	if serviceAccountToken == "" {
		return ""
	}
	return cachedAccessToken(serviceAccountToken, readOnly, scopes)
}

func cachedAccessToken(serviceAccountToken string, readOnly bool, scopes []string) string {
	creds, err := ReadCredentials()
	if err != nil {
		return ""
	}
	if serviceAccountToken != "" && creds.ServiceAccountToken != serviceAccountToken {
		return ""
	}
	if creds.AccessToken == "" {
		return ""
	}
	// Don't reuse a token if the read-only mode doesn't match.
	if creds.ReadOnly != readOnly {
		return ""
	}
	// Don't reuse a token if the extra scopes don't match.
	if !stringsEqual(creds.Scopes, scopes) {
		return ""
	}
	// Expire 60s early to avoid using a token that's about to expire mid-request.
	if time.Now().After(creds.AccessTokenExpiresAt.Add(-60 * time.Second)) {
		return ""
	}
	return creds.AccessToken
}

// stringsEqual returns true if two string slices have the same elements.
func stringsEqual(a, b []string) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// CacheAccessToken stores a JWT in the config file alongside existing credentials.
//
// Holds an exclusive lock across the read-modify-write sequence so two
// concurrent invocations don't lose each other's update.
func CacheAccessToken(accessToken string, expiresIn int, readOnly bool, scopes []string) error {
	return cacheAccessToken("", accessToken, expiresIn, readOnly, scopes)
}

// CacheAccessTokenForServiceAccount stores a JWT only when the stored config
// still belongs to the same service-account token that minted the JWT.
func CacheAccessTokenForServiceAccount(serviceAccountToken, accessToken string, expiresIn int, readOnly bool, scopes []string) error {
	if serviceAccountToken == "" {
		return nil
	}
	return cacheAccessToken(serviceAccountToken, accessToken, expiresIn, readOnly, scopes)
}

func cacheAccessToken(serviceAccountToken, accessToken string, expiresIn int, readOnly bool, scopes []string) error {
	unlock, err := lockConfigDir()
	if err != nil {
		// Can't cache without a lock — don't fail the caller's request.
		return nil
	}
	defer unlock()

	creds, err := ReadCredentials()
	if err != nil {
		// No existing config — can't cache without stored credentials.
		return nil
	}
	if serviceAccountToken != "" && creds.ServiceAccountToken != serviceAccountToken {
		// Env/flag overrides should not rewrite the cache for the stored token.
		return nil
	}
	creds.AccessToken = accessToken
	creds.AccessTokenExpiresAt = time.Now().Add(time.Duration(expiresIn) * time.Second)
	creds.ReadOnly = readOnly
	creds.Scopes = scopes
	return writeCredentialsLocked(creds)
}

// ReadCredentials reads credentials from ~/.cio/config.json.
func ReadCredentials() (*Credentials, error) {
	path, err := configFilePath()
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config file: %w", err)
	}

	var creds Credentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return nil, fmt.Errorf("parse config file: %w", err)
	}

	return &creds, nil
}

// WriteCredentials writes credentials to ~/.cio/config.json with 0600 permissions.
// Holds an exclusive lock for the duration of the write.
func WriteCredentials(creds *Credentials) error {
	unlock, err := lockConfigDir()
	if err != nil {
		return err
	}
	defer unlock()
	return writeCredentialsLocked(creds)
}

// writeCredentialsLocked performs an atomic write (temp file + rename) of the
// config file. Callers must already hold the config-dir lock.
func writeCredentialsLocked(creds *Credentials) error {
	dir, err := configDirPath()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(dir, configDirMode); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	data, err := json.MarshalIndent(creds, "", "  ")
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

// DeleteCredentials removes the config file.
func DeleteCredentials() error {
	unlock, err := lockConfigDir()
	if err != nil {
		return err
	}
	defer unlock()

	path, err := configFilePath()
	if err != nil {
		return err
	}

	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove config file: %w", err)
	}

	return nil
}

// lockConfigDir acquires an exclusive file lock on the config directory.
// The lock is held on a dedicated ~/.cio/config.lock file so that lock
// acquisition never conflicts with credential read/write semantics.
// Returns an unlock function that must be called when done.
func lockConfigDir() (unlock func(), err error) {
	dir, err := configDirPath()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, configDirMode); err != nil {
		return nil, fmt.Errorf("create config dir: %w", err)
	}

	lockPath := filepath.Join(dir, configLockName)
	unlock, err = filelock.Lock(lockPath, configFileMode)
	if err != nil {
		return nil, fmt.Errorf("acquire config lock: %w", err)
	}

	return unlock, nil
}

// ConfigDir returns the path to ~/.cio/.
func ConfigDir() (string, error) {
	return configDirPath()
}

func configDirPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("get home dir: %w", err)
	}
	return filepath.Join(home, configDirName), nil
}

func configFilePath() (string, error) {
	dir, err := configDirPath()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, configFileName), nil
}

// MaskToken returns a masked version of the token for display.
// Shows the service-account prefix + first 4 chars and last 4 chars, masking the middle.
func MaskToken(token string) string {
	token = strings.TrimSpace(token)
	for _, prefix := range serviceAccountTokenPrefixes {
		if !strings.HasPrefix(token, prefix) {
			continue
		}
		rest := token[len(prefix):]
		if len(rest) <= 8 {
			return prefix + strings.Repeat("*", len(rest))
		}
		return prefix + rest[:4] + strings.Repeat("*", len(rest)-8) + rest[len(rest)-4:]
	}
	if len(token) <= 8 {
		return strings.Repeat("*", len(token))
	}
	return token[:4] + strings.Repeat("*", len(token)-8) + token[len(token)-4:]
}

// IsServiceAccountToken returns true if the token has a supported service-account prefix.
func IsServiceAccountToken(token string) bool {
	for _, prefix := range serviceAccountTokenPrefixes {
		if strings.HasPrefix(token, prefix) {
			return true
		}
	}
	return false
}
