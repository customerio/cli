package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/customerio/cli/internal/client"
	"github.com/customerio/cli/internal/filelock"
	"github.com/customerio/cli/internal/routes"
	"github.com/customerio/cli/internal/validate"
	"github.com/spf13/cobra"
)

// meteredSendPaths are the Track API sends Flex charges to the wallet. Push
// and inbox messages are free, so their sends never check funds.
var meteredSendPaths = map[string]bool{
	"/v1/send/email": true,
	"/v1/send/sms":   true,
}

const (
	// fundsCheckInterval spaces out the funds checks for one token. A forecast
	// moves slowly, and an agent may send in a loop.
	fundsCheckInterval = time.Hour
	// fundsCheckOffFlexInterval spaces them out further for an account that
	// isn't on Flex, where a check only matters again if the plan changes.
	fundsCheckOffFlexInterval = 24 * time.Hour
)

// fundsCheckTimeout bounds the whole check: the token exchange and every read.
// A variable so tests can shorten it.
var fundsCheckTimeout = 3 * time.Second

// fundsCheckState is what the funds cache keeps for one service account token.
type fundsCheckState struct {
	AccountID   string    `json:"account_id,omitempty"`
	NextCheckAt time.Time `json:"next_check_at"`
}

// warnIfFundsLow prints a FUNDS_LOW warning after a metered send when the
// account's funds are gone or will run out before the billing period ends.
//
// It is best-effort: every failure is swallowed, so the send's output and exit
// code never change. It checks at most once an hour per service account token,
// throttled in ~/.cio/cache/funds under a hash of the token, so a token from
// CIO_TOKEN or --token is throttled the same way as a profile's.
func warnIfFundsLow(cmd *cobra.Command, sendPath, saToken string) {
	if !meteredSendPaths[sendPath] {
		return
	}
	cache, err := openFundsCache(saToken)
	if err != nil {
		return
	}
	now := time.Now()
	state, due := cache.claim(now)
	if !due {
		return
	}

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, fundsCheckTimeout)
	defer cancel()
	c := fundsCheckClient(cmd, saToken)

	if state.AccountID == "" {
		accountID, flex, err := readFundsAccount(ctx, c)
		if err != nil {
			return
		}
		state.AccountID = accountID
		if !flex {
			state.NextCheckAt = now.Add(fundsCheckOffFlexInterval)
			cache.save(state)
			return
		}
		cache.save(state)
	}

	usage, rates, err := readWallet(ctx, c, state.AccountID)
	var apiErr *client.APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusBadRequest {
		// The wallet refuses an account that has left Flex.
		state.NextCheckAt = now.Add(fundsCheckOffFlexInterval)
		cache.save(state)
		return
	}
	if err != nil {
		return
	}
	_, _, _, warnings, err := forecastWallet(now, usage, rates)
	if err != nil {
		return
	}
	for _, w := range warnings {
		if w.Code == "FUNDS_LOW" {
			printWarning(cmd, w.Code, w.Message)
			return
		}
	}
}

// fundsCheckClient builds a UI API client with the command's own read-only
// flag and scopes, so a profile token's cached access token is reused and
// refreshed in place. On a send, --api-url names the Track host, so the UI
// API host comes from CIO_API_URL, the profile or the region. It never
// retries: the check gives up before it would hold up the command.
func fundsCheckClient(cmd *cobra.Command, saToken string) *client.Client {
	readOnly, _ := cmd.Flags().GetBool("read-only")
	scopes, _ := cmd.Flags().GetStringSlice("scope")
	return client.New(client.Config{
		BaseURL:             client.ResolveBaseURL("", false),
		ServiceAccountToken: saToken,
		AccessToken:         client.ResolveAccessToken(),
		ReadOnly:            readOnly,
		Scopes:              scopes,
		Timeout:             fundsCheckTimeout,
		RetryConfig:         &client.RetryConfig{},
	})
}

// readFundsAccount reads the token's account ID and whether it's on Flex.
func readFundsAccount(ctx context.Context, c *client.Client) (accountID string, flex bool, err error) {
	raw, err := c.Do(ctx, http.MethodGet, "/v1/accounts/current", nil, nil)
	if err != nil {
		return "", false, err
	}
	var current struct {
		Account billingAccount `json:"account"`
	}
	if err := json.Unmarshal(raw, &current); err != nil {
		return "", false, err
	}
	accountID = current.Account.ID.String()
	if err := validate.ValidateResourceID(accountID); err != nil {
		return "", false, err
	}
	return accountID, current.Account.BillingVendor == flexBillingVendor, nil
}

// readWallet makes the two reads forecastWallet needs, as billing status does.
func readWallet(ctx context.Context, c *client.Client, accountID string) (walletUsage, map[string]float64, error) {
	walletPath := "/v1/accounts/" + url.PathEscape(accountID) + "/billing/wallet"
	get := func(path string, into any) error {
		raw, err := c.Do(ctx, http.MethodGet, walletPath+path, nil, nil)
		if err != nil {
			return err
		}
		return json.Unmarshal(raw, into)
	}

	var usage walletUsage
	if err := get("/usage", &usage); err != nil {
		return walletUsage{}, nil, err
	}
	var rates struct {
		Rates map[string]float64 `json:"rates"`
	}
	if err := get("/rates", &rates); err != nil {
		return walletUsage{}, nil, err
	}
	return usage, rates.Rates, nil
}

// fundsCache is one token's file in ~/.cio/cache/funds. One lock covers the
// directory, held only while a file is read or written, never across a request.
type fundsCache struct {
	lockPath string
	path     string
}

func openFundsCache(saToken string) (*fundsCache, error) {
	configDir, err := client.ConfigDir()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(configDir, "cache", "funds")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &fundsCache{
		lockPath: filepath.Join(dir, "funds.lock"),
		path:     filepath.Join(dir, routes.CacheKeyDir(saToken)+".json"),
	}, nil
}

// claim reports whether the token's check is due, and when it is, records the
// next one before any request goes out. A check that hangs or fails, or a send
// running alongside, then still counts. A lock held by another send means that
// send is checking, so this one isn't due.
func (f *fundsCache) claim(now time.Time) (fundsCheckState, bool) {
	unlock, err := filelock.TryLock(f.lockPath, 0o600)
	if err != nil {
		return fundsCheckState{}, false
	}
	defer unlock()

	var state fundsCheckState
	if data, err := os.ReadFile(f.path); err == nil && json.Unmarshal(data, &state) != nil {
		// A damaged file only costs one extra check.
		state = fundsCheckState{}
	}
	if now.Before(state.NextCheckAt) {
		return state, false
	}
	state.NextCheckAt = now.Add(fundsCheckInterval)
	// Without a throttle every send would check, so a failed write skips it.
	if err := f.write(state); err != nil {
		return state, false
	}
	return state, true
}

// save records state when the lock is free. Losing a save only costs an
// extra account read, or an earlier check of an account that isn't on Flex.
func (f *fundsCache) save(state fundsCheckState) {
	unlock, err := filelock.TryLock(f.lockPath, 0o600)
	if err != nil {
		return
	}
	defer unlock()
	_ = f.write(state)
}

func (f *fundsCache) write(state fundsCheckState) error {
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return os.WriteFile(f.path, data, 0o600)
}
