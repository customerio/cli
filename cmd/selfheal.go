package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/customerio/cli/internal/client"
)

// sandboxPromoteThrottle bounds how often the CLI probes the promote endpoint
// while still on a sandbox token. Promotion only succeeds after go-live; until
// then each attempt is a no-op, so we space them out. The post-go-live grace
// window is days, so up to an hour of heal latency is immaterial.
const sandboxPromoteThrottle = time.Hour

// maybePromoteSandboxToken performs the automatic sandbox→live self-heal.
//
// When the CLI is authenticated with a stored sandbox token and the account has
// gone live, the server's POST /promote_sandbox_token swaps it for a live token
// (inheriting the sandbox token's permissions) and revokes the sandbox token.
// This persists the new token, points the in-memory client at it, and lets the
// user's command proceed on the live token — all transparently.
//
// It is best-effort: any failure is swallowed (only a throttle timestamp is
// recorded) so the user's command always runs. It acts only on a sandbox token
// the CLI itself persisted — never an env/flag token, which is not ours to
// rewrite — and never in read-only mode, where the POST would be blocked.
func maybePromoteSandboxToken(ctx context.Context, c *client.Client, saToken string, readOnly bool) {
	if readOnly || !client.IsSandboxServiceAccountToken(saToken) {
		return
	}

	creds, err := client.ReadCredentials()
	if err != nil {
		return // token came from env/flag, not our config — leave it alone
	}
	if creds.ServiceAccountToken != saToken || creds.AccountID == "" {
		return
	}
	if time.Since(creds.SandboxPromoteCheckedAt) < sandboxPromoteThrottle {
		return
	}

	path := fmt.Sprintf("/v1/accounts/%s/promote_sandbox_token", creds.AccountID)
	resp, err := c.Do(ctx, "POST", path, nil, nil)
	if err != nil {
		// 403 (still in sandbox), 404 (already promoted), or transient — record
		// the attempt so we don't probe again until the throttle elapses.
		creds.SandboxPromoteCheckedAt = time.Now()
		_ = client.WriteCredentials(creds)
		return
	}

	var promoted struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(resp, &promoted); err != nil || !client.IsServiceAccountToken(promoted.Token) {
		creds.SandboxPromoteCheckedAt = time.Now()
		_ = client.WriteCredentials(creds)
		return
	}

	creds.ServiceAccountToken = promoted.Token
	creds.AccessToken = ""
	creds.AccessTokenExpiresAt = time.Time{}
	creds.SandboxPromoteCheckedAt = time.Time{}
	if err := client.WriteCredentials(creds); err != nil {
		return
	}
	c.SetServiceAccountToken(promoted.Token)
	fmt.Fprintln(os.Stderr, "cio: account is live — upgraded the CLI to a live token.")
}
