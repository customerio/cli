package client

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/customerio/cli/internal/filelock"
)

type envTokenCache struct {
	AccessToken string    `json:"access_token"`
	ExpiresAt   time.Time `json:"expires_at"`
}

// Hold the per-session lock through exchange so concurrent commands share one JWT.
func (c *Client) lockEnvTokenCache(ctx context.Context) (string, func()) {
	noop := func() {}
	if c.serviceAccountToken == "" || c.serviceAccountToken != os.Getenv("CIO_TOKEN") {
		return "", noop
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", noop
	}
	dir := filepath.Join(home, configDirName, "cache", "tokens")
	if err := os.MkdirAll(dir, configDirMode); err != nil {
		return "", noop
	}
	key, _ := json.Marshal([]any{c.serviceAccountToken, c.baseURL, c.readOnly, strings.Join(c.scopes, ScopeSeparator)})
	path := filepath.Join(dir, fmt.Sprintf("%x.json", sha256.Sum256(key)))
	for {
		unlock, err := filelock.TryLock(path+".lock", configFileMode)
		if err == nil {
			return path, unlock
		}
		if !errors.Is(err, filelock.ErrLocked) {
			return "", noop
		}
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return "", noop
		case <-timer.C:
		}
	}
}

func (c *Client) writeEnvTokenCache(path string) {
	tmp, err := os.CreateTemp(filepath.Dir(path), "token-*.tmp")
	if err != nil {
		return
	}
	defer os.Remove(tmp.Name())
	if err := json.NewEncoder(tmp).Encode(envTokenCache{AccessToken: c.accessToken, ExpiresAt: c.accessTokenExpiresAt}); err != nil {
		tmp.Close()
		return
	}
	if err := tmp.Close(); err != nil {
		return
	}
	_ = os.Rename(tmp.Name(), path)
}
