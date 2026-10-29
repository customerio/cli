package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/customerio/cli/internal/client"
	"github.com/customerio/cli/internal/filelock"
	"github.com/customerio/cli/internal/routes"
)

// stubUIAPI points the UI API at a server that answers every request with a
// 404, so a send's funds check fails fast in tests that aren't about it.
func stubUIAPI(t *testing.T) {
	t.Helper()
	srv := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)
	t.Setenv("CIO_API_URL", srv.URL)
}

const (
	flexFundsAccount  = `{"account":{"id":1,"billing_vendor":"modern_stripe"}}`
	otherFundsAccount = `{"account":{"id":1,"billing_vendor":"stripe"}}`
)

// fundsServer answers the reads a send's funds check makes, and records each
// request. By default its wallet runs out about five days into a period with
// twenty left.
type fundsServer struct {
	*httptest.Server
	mu        sync.Mutex
	paths     []string
	auth      []string
	responses map[string]string
	statuses  map[string]int
	block     map[string]bool
}

func newFundsServer(t *testing.T) *fundsServer {
	t.Helper()
	s := &fundsServer{
		statuses: map[string]int{},
		block:    map[string]bool{},
		responses: map[string]string{
			"/v1/service_accounts/oauth/token":    `{"access_token":"jwt-funds-check","token_type":"Bearer","expires_in":3600}`,
			"/v1/accounts/current":                flexFundsAccount,
			"/v1/accounts/1/billing/wallet/usage": fundsUsage(10, 5),
			"/v1/accounts/1/billing/wallet/rates": `{"rates":{"email_sends":0.002}}`,
		},
	}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.paths = append(s.paths, r.URL.Path)
		s.auth = append(s.auth, r.Header.Get("Authorization"))
		body, ok := s.responses[r.URL.Path]
		status, block := s.statuses[r.URL.Path], s.block[r.URL.Path]
		s.mu.Unlock()

		if block {
			select {
			case <-r.Context().Done():
			case <-time.After(5 * time.Second):
			}
			return
		}
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if status != 0 {
			w.WriteHeader(status)
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(s.Close)
	t.Setenv("CIO_API_URL", s.URL)
	return s
}

// fundsUsage is a wallet nine days into a 30-day period that has spent used
// dollars and has remaining left.
func fundsUsage(used, remaining float64) string {
	today := time.Now().UTC()
	return fmt.Sprintf(`{"start":%q,"end":%q,"balance":{"used":%g,"remaining":%g},"meters":{
		"email_sends":{"grant_total":0,"grant_used":0,"grant_remaining":0,"balance_used":5000,"balance_cost":%g,"total_used":5000}}}`,
		today.AddDate(0, 0, -9).Format(time.DateOnly), today.AddDate(0, 0, 20).Format(time.DateOnly), used, remaining, used)
}

func (s *fundsServer) requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.paths)
}

func (s *fundsServer) set(path string, status int, body string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.statuses[path] = status
	if body != "" {
		s.responses[path] = body
	}
}

func setupFundsTest(t *testing.T, token string) *fundsServer {
	t.Helper()
	_, cleanup := setupSendTest(t, token, "123")
	t.Cleanup(cleanup)
	return newFundsServer(t)
}

func sendEmail(args ...string) (stdout, stderr string, err error) {
	return executeCommand(append([]string{"send", "email",
		"--environment-id", "123",
		"--json", `{"identifiers":{"email":"test@example.com"},"to":"test@example.com","subject":"Hi","body":"Hi"}`,
	}, args...)...)
}

func fundsCachePath(t *testing.T, token string) string {
	t.Helper()
	return filepath.Join(os.Getenv("HOME"), ".cio", "cache", "funds", routes.CacheKeyDir(token)+".json")
}

func readFundsState(t *testing.T, token string) fundsCheckState {
	t.Helper()
	data, err := os.ReadFile(fundsCachePath(t, token))
	if err != nil {
		t.Fatal(err)
	}
	var state fundsCheckState
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	return state
}

// expectSendSucceeded checks that the send printed its result and exited
// cleanly, whatever the funds check did.
func expectSendSucceeded(t *testing.T, stdout, stderr string, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v\nstderr: %s", err, stderr)
	}
	if !strings.Contains(stdout, "RKalBAUAAZ21_test==") {
		t.Errorf("expected the send result on stdout, got %q", stdout)
	}
}

func TestFundsWarning_WarnsAfterEachMeteredSend(t *testing.T) {
	txnJSON := `{"transactional_message_id":1,"identifiers":{"email":"test@example.com"}}`
	cases := map[string][]string{
		"send email":               {"send", "email", "--environment-id", "123", "--json", txnJSON},
		"transactional send email": {"transactional", "send", "email", "--environment-id", "123", "--json", txnJSON},
		"transactional send sms":   {"transactional", "send", "sms", "--environment-id", "123", "--json", txnJSON},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			setupFundsTest(t, "sa_live_funds")

			stdout, stderr, err := executeCommand(args...)
			expectSendSucceeded(t, stdout, stderr, err)
			if got := strings.Count(stderr, `"code":"FUNDS_LOW"`); got != 1 {
				t.Fatalf("expected one FUNDS_LOW warning, got %d in %q", got, stderr)
			}
			if !strings.Contains(stderr, `"warning":true`) || !strings.Contains(stderr, "Funds run out around") {
				t.Errorf("expected the forecast's warning, got %q", stderr)
			}
		})
	}
}

func TestFundsWarning_ChecksOnceAnHourPerToken(t *testing.T) {
	server := setupFundsTest(t, "sa_live_funds")

	_, stderr, err := sendEmail()
	if err != nil || !strings.Contains(stderr, "FUNDS_LOW") {
		t.Fatalf("expected the first send to warn, got err=%v stderr=%q", err, stderr)
	}
	checked := len(server.requests())

	stdout, stderr, err := sendEmail()
	expectSendSucceeded(t, stdout, stderr, err)
	if strings.Contains(stderr, "FUNDS_LOW") {
		t.Errorf("expected no warning within the hour, got %q", stderr)
	}
	if got := len(server.requests()); got != checked {
		t.Errorf("expected no reads within the hour, got %v", server.requests()[checked:])
	}

	// Another token, here from --token, has its own hour.
	other := trackServer(t, "sa_live_other", "123")
	defer other.Close()
	t.Setenv("CIO_TRACK_URL", other.URL)
	_, stderr, err = sendEmail("--token", "sa_live_other")
	if err != nil || !strings.Contains(stderr, "FUNDS_LOW") {
		t.Fatalf("expected another token's send to warn, got err=%v stderr=%q", err, stderr)
	}

	entries, err := os.ReadDir(filepath.Dir(fundsCachePath(t, "sa_live_funds")))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		data, _ := os.ReadFile(filepath.Join(filepath.Dir(fundsCachePath(t, "sa_live_funds")), e.Name()))
		if strings.Contains(e.Name(), "sa_live") || strings.Contains(string(data), "sa_live") {
			t.Errorf("the funds cache must not hold the token, found it in %s", e.Name())
		}
	}
}

func TestFundsWarning_IgnoresTheTrackHostFromAPIURL(t *testing.T) {
	server := setupFundsTest(t, "sa_live_funds")
	track := trackServer(t, "sa_live_funds", "123")
	defer track.Close()

	stdout, stderr, err := sendEmail("--api-url", track.URL)
	expectSendSucceeded(t, stdout, stderr, err)
	if !strings.Contains(stderr, "FUNDS_LOW") {
		t.Errorf("expected a warning from the UI API while --api-url names the Track host, got %q", stderr)
	}
	if !slices.Contains(server.requests(), "/v1/accounts/current") {
		t.Errorf("expected the account read on the UI API, got %v", server.requests())
	}
}

func TestFundsWarning_ReusesTheProfilesCachedAccessToken(t *testing.T) {
	server := setupFundsTest(t, "sa_live_profile")
	t.Setenv("CIO_TOKEN", "")
	if err := client.WriteCredentials(&client.Credentials{
		ServiceAccountToken:  "sa_live_profile",
		AccountID:            "1",
		AccessToken:          "jwt-cached",
		AccessTokenExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	_, stderr, err := sendEmail()
	if err != nil || !strings.Contains(stderr, "FUNDS_LOW") {
		t.Fatalf("expected a warning, got err=%v stderr=%q", err, stderr)
	}
	if slices.Contains(server.requests(), "/v1/service_accounts/oauth/token") {
		t.Errorf("expected the cached access token to be reused, got %v", server.requests())
	}
	for _, auth := range server.auth {
		if auth != "Bearer jwt-cached" {
			t.Errorf("expected the cached access token on every read, got %q", auth)
		}
	}
}

func TestFundsWarning_ReusesTheCachedAccountID(t *testing.T) {
	server := setupFundsTest(t, "sa_live_funds")
	if _, _, err := sendEmail(); err != nil {
		t.Fatal(err)
	}
	if got := readFundsState(t, "sa_live_funds").AccountID; got != "1" {
		t.Fatalf("expected the account ID to be cached, got %q", got)
	}

	// Make the next check due.
	if err := os.WriteFile(fundsCachePath(t, "sa_live_funds"), []byte(`{"account_id":"1","next_check_at":"2026-01-01T00:00:00Z"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	before := len(server.requests())
	_, stderr, err := sendEmail()
	if err != nil || !strings.Contains(stderr, "FUNDS_LOW") {
		t.Fatalf("expected a warning, got err=%v stderr=%q", err, stderr)
	}
	if slices.Contains(server.requests()[before:], "/v1/accounts/current") {
		t.Errorf("expected the cached account ID to be used, got %v", server.requests()[before:])
	}
}

func TestFundsWarning_WaitsADayOffFlex(t *testing.T) {
	t.Run("account read says another plan", func(t *testing.T) {
		server := setupFundsTest(t, "sa_live_funds")
		server.set("/v1/accounts/current", 0, otherFundsAccount)

		stdout, stderr, err := sendEmail()
		expectSendSucceeded(t, stdout, stderr, err)
		if strings.Contains(stderr, "FUNDS_LOW") {
			t.Errorf("expected no warning off Flex, got %q", stderr)
		}
		if slices.Contains(server.requests(), "/v1/accounts/1/billing/wallet/usage") {
			t.Errorf("expected no wallet reads off Flex, got %v", server.requests())
		}
		if next := readFundsState(t, "sa_live_funds").NextCheckAt; time.Until(next) < 23*time.Hour {
			t.Errorf("expected the next check a day out, got %v", next)
		}
	})
	t.Run("wallet refuses the account", func(t *testing.T) {
		server := setupFundsTest(t, "sa_live_funds")
		server.set("/v1/accounts/1/billing/wallet/usage", http.StatusBadRequest, `{"errors":[{"detail":"not on Flex"}]}`)

		stdout, stderr, err := sendEmail()
		expectSendSucceeded(t, stdout, stderr, err)
		if strings.Contains(stderr, "FUNDS_LOW") || strings.Contains(stderr, "not on Flex") {
			t.Errorf("expected a silent check, got %q", stderr)
		}
		if next := readFundsState(t, "sa_live_funds").NextCheckAt; time.Until(next) < 23*time.Hour {
			t.Errorf("expected the next check a day out, got %v", next)
		}
	})
}

func TestFundsWarning_StaysSilentWhenTheCheckFails(t *testing.T) {
	cases := []struct {
		name   string
		path   string
		status int
		body   string
	}{
		{"token exchange fails", "/v1/service_accounts/oauth/token", http.StatusUnauthorized, `{"error":"invalid_client"}`},
		{"account read fails", "/v1/accounts/current", http.StatusInternalServerError, "oops"},
		{"wallet forbidden", "/v1/accounts/1/billing/wallet/usage", http.StatusForbidden, `{"errors":[{"detail":"forbidden"}]}`},
		{"rates unreadable", "/v1/accounts/1/billing/wallet/rates", 0, "not json"},
		{"funds last the period", "/v1/accounts/1/billing/wallet/usage", 0, fundsUsage(10, 500)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := setupFundsTest(t, "sa_live_funds")
			server.set(tc.path, tc.status, tc.body)

			stdout, stderr, err := sendEmail()
			expectSendSucceeded(t, stdout, stderr, err)
			if stderr != "" {
				t.Errorf("expected nothing on stderr, got %q", stderr)
			}
			if next := readFundsState(t, "sa_live_funds").NextCheckAt; time.Until(next) < 59*time.Minute {
				t.Errorf("expected a failed check to still count for the hour, got %v", next)
			}
		})
	}
}

func TestFundsWarning_GivesUpAtTheTimeout(t *testing.T) {
	server := setupFundsTest(t, "sa_live_funds")
	server.mu.Lock()
	server.block["/v1/accounts/1/billing/wallet/usage"] = true
	server.mu.Unlock()
	saved := fundsCheckTimeout
	fundsCheckTimeout = 100 * time.Millisecond
	t.Cleanup(func() { fundsCheckTimeout = saved })

	start := time.Now()
	stdout, stderr, err := sendEmail()
	expectSendSucceeded(t, stdout, stderr, err)
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("expected the check to give up at its timeout, took %v", elapsed)
	}
	if stderr != "" {
		t.Errorf("expected nothing on stderr, got %q", stderr)
	}
}

func TestFundsWarning_SkipsFreeSendsDryRunsAndFailedSends(t *testing.T) {
	txnJSON := `{"transactional_message_id":1,"identifiers":{"email":"test@example.com"}}`
	cases := map[string][]string{
		"push":        {"transactional", "send", "push", "--environment-id", "123", "--json", txnJSON},
		"inbox":       {"transactional", "send", "inbox", "--environment-id", "123", "--json", txnJSON},
		"dry run":     {"send", "email", "--environment-id", "123", "--json", txnJSON, "--dry-run"},
		"failed send": {"send", "email", "--environment-id", "123", "--json", txnJSON, "--token", "sa_live_wrong"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			server := setupFundsTest(t, "sa_live_funds")

			_, stderr, _ := executeCommand(args...)
			if strings.Contains(stderr, "FUNDS_LOW") {
				t.Errorf("expected no warning, got %q", stderr)
			}
			if got := server.requests(); len(got) != 0 {
				t.Errorf("expected no funds check, got %v", got)
			}
		})
	}
}

func TestFundsWarning_SkipsWhileAnotherSendHoldsTheLock(t *testing.T) {
	server := setupFundsTest(t, "sa_live_funds")
	dir := filepath.Dir(fundsCachePath(t, "sa_live_funds"))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	unlock, err := filelock.TryLock(filepath.Join(dir, "funds.lock"), 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()

	stdout, stderr, err := sendEmail()
	expectSendSucceeded(t, stdout, stderr, err)
	if got := server.requests(); len(got) != 0 {
		t.Errorf("expected no funds check while the lock is held, got %v", got)
	}
}
