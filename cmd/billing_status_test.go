package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
)

// statusNow is six days into an October billing period, with 25 days left.
var statusNow = time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC)

func octoberUsage(used, remaining float64, meters map[string]walletMeter) walletUsage {
	return walletUsage{
		Start:   "2026-10-01",
		End:     "2026-10-31",
		Balance: walletBalance{Used: used, Remaining: remaining},
		Meters:  meters,
	}
}

func warningCodes(warnings []billingWarning) []string {
	codes := make([]string, len(warnings))
	for i, w := range warnings {
		codes[i] = w.Code
	}
	return codes
}

func deref(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}

func TestForecastWallet_FundsRunOutBeforeThePeriodEnds(t *testing.T) {
	_, funds, _, warnings, err := forecastWallet(statusNow, octoberUsage(38.40, 51.20, nil), nil)
	if err != nil {
		t.Fatal(err)
	}
	if funds.DailySpendUSD != 6.4 {
		t.Errorf("expected daily spend 6.40, got %v", funds.DailySpendUSD)
	}
	if got := deref(funds.RunsOutOn); got != "2026-10-15" {
		t.Errorf("expected funds to run out on 2026-10-15, got %s", got)
	}
	if len(warnings) != 1 || warnings[0].Code != "FUNDS_LOW" || !strings.Contains(warnings[0].Message, "around Oct 15") {
		t.Errorf("expected one FUNDS_LOW warning naming Oct 15, got %+v", warnings)
	}
}

func TestForecastWallet_FundsThatLastThePeriodDontWarn(t *testing.T) {
	cases := map[string]walletUsage{
		"slow spend": octoberUsage(6, 500, nil),
		"no spend":   octoberUsage(0, 20, nil),
		// Exactly the 25 days left in the period.
		"lasts to the last second": octoberUsage(6, 25, nil),
	}
	for name, usage := range cases {
		t.Run(name, func(t *testing.T) {
			_, funds, _, warnings, err := forecastWallet(statusNow, usage, nil)
			if err != nil {
				t.Fatal(err)
			}
			if funds.RunsOutOn != nil {
				t.Errorf("expected no run-out date, got %s", *funds.RunsOutOn)
			}
			if len(warnings) != 0 {
				t.Errorf("expected no warnings, got %+v", warnings)
			}
		})
	}
}

func TestForecastWallet_ZeroBalanceIsLowNow(t *testing.T) {
	_, funds, _, warnings, err := forecastWallet(statusNow, octoberUsage(12, 0, nil), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := deref(funds.RunsOutOn); got != "2026-10-07" {
		t.Errorf("expected run-out today, got %s", got)
	}
	if len(warnings) != 1 || warnings[0].Code != "FUNDS_LOW" || !strings.HasPrefix(warnings[0].Message, "No funds left.") {
		t.Errorf("expected the no-funds warning, got %+v", warnings)
	}
}

func TestForecastWallet_GrantRunningOutSpeedsUpTheFundsForecast(t *testing.T) {
	// $1 a day from the balance alone would last exactly to the period end.
	// The active profiles grant covers 20 a day and runs out after 4.5 more
	// days, adding $1 a day, so funds now run out first.
	usage := octoberUsage(6, 25, map[string]walletMeter{
		"active_profiles": {GrantTotal: 210, GrantUsed: 120, GrantRemaining: 90, TotalUsed: 120},
	})
	_, funds, meters, warnings, err := forecastWallet(statusNow, usage, map[string]float64{"active_profiles": 0.05})
	if err != nil {
		t.Fatal(err)
	}
	if got := deref(meters["active_profiles"].GrantRunsOutOn); got != "2026-10-11" {
		t.Errorf("expected the grant to run out on 2026-10-11, got %s", got)
	}
	// 4.5 days at $1 leaves $20.50, which lasts 10.25 days at $2.
	if got := deref(funds.RunsOutOn); got != "2026-10-21" {
		t.Errorf("expected funds to run out on 2026-10-21, got %s", got)
	}
	if funds.DailySpendUSD != 1 {
		t.Errorf("expected today's daily spend to stay $1 until the grant runs out, got %v", funds.DailySpendUSD)
	}
	if !slices.Equal(warningCodes(warnings), []string{"FUNDS_LOW", "GRANT_LOW"}) {
		t.Fatalf("expected FUNDS_LOW then GRANT_LOW, got %+v", warnings)
	}
	if warnings[1].Meter != "active_profiles" || !strings.Contains(warnings[1].Message, "paid for from funds") {
		t.Errorf("expected the grant warning to say usage moves to funds, got %+v", warnings[1])
	}
}

func TestForecastWallet_UsedUpGrantAddsItsRateWithoutWarning(t *testing.T) {
	// The grant covered 300 profiles in 6 days, so its $2.50 a day now comes
	// from funds on top of the $1 the balance already spends.
	usage := octoberUsage(6, 500, map[string]walletMeter{
		"active_profiles": {GrantTotal: 300, GrantUsed: 300, GrantRemaining: 0, BalanceUsed: 120, BalanceCost: 6, TotalUsed: 420},
	})
	_, funds, meters, warnings, err := forecastWallet(statusNow, usage, map[string]float64{"active_profiles": 0.05})
	if err != nil {
		t.Fatal(err)
	}
	if funds.DailySpendUSD != 3.5 {
		t.Errorf("expected daily spend $3.50, got %v", funds.DailySpendUSD)
	}
	if got := deref(meters["active_profiles"].GrantRunsOutOn); got != "2026-10-07" {
		t.Errorf("expected a used-up grant to report today, got %s", got)
	}
	if len(warnings) != 0 {
		t.Errorf("expected no warning for a grant already in the funds forecast, got %+v", warnings)
	}
}

func TestForecastWallet_AgentUsageNeverDrawsOnFunds(t *testing.T) {
	usage := octoberUsage(6, 30, map[string]walletMeter{
		"agent_usage": {GrantTotal: 600, GrantUsed: 600, GrantRemaining: 0, TotalUsed: 600},
		"ai_credits":  {GrantTotal: 1000, GrantUsed: 600, GrantRemaining: 400, TotalUsed: 600},
	})
	// Both meters would add $5 a day if their usage moved to funds.
	_, funds, _, warnings, err := forecastWallet(statusNow, usage, map[string]float64{"agent_usage": 0.05, "ai_credits": 0.05})
	if err != nil {
		t.Fatal(err)
	}
	if funds.DailySpendUSD != 1 {
		t.Errorf("expected agent usage to stay out of daily spend, got %v", funds.DailySpendUSD)
	}
	// ai_credits runs out on Oct 11 and adds $5 a day: $26 left lasts 4.3 days.
	if got := deref(funds.RunsOutOn); got != "2026-10-15" {
		t.Errorf("expected funds to run out on 2026-10-15, got %s", got)
	}
	if !slices.Equal(warningCodes(warnings), []string{"FUNDS_LOW", "GRANT_LOW", "GRANT_LOW"}) {
		t.Fatalf("expected FUNDS_LOW and two GRANT_LOW, got %+v", warnings)
	}
	if warnings[1].Meter != "agent_usage" || !strings.Contains(warnings[1].Message, "can't be paid for from funds") {
		t.Errorf("expected the agent usage warning to say it can't use funds, got %+v", warnings[1])
	}
}

func TestForecastWallet_EarlyPeriodCountsAsADay(t *testing.T) {
	now := time.Date(2026, 10, 1, 2, 0, 0, 0, time.UTC)
	_, funds, _, _, err := forecastWallet(now, octoberUsage(10, 1000, nil), nil)
	if err != nil {
		t.Fatal(err)
	}
	if funds.DailySpendUSD != 10 {
		t.Errorf("expected two hours of spend to count as one day, got %v", funds.DailySpendUSD)
	}
}

func TestForecastWallet_RejectsAnInvalidPeriod(t *testing.T) {
	usage := octoberUsage(1, 1, nil)
	usage.End = "October"
	if _, _, _, _, err := forecastWallet(statusNow, usage, nil); err == nil {
		t.Fatal("expected an error for an invalid period end")
	}
}

func TestLimitWarnings(t *testing.T) {
	limit := func(n int) *int { return &n }
	cases := []struct {
		name  string
		limit limitStatus
		want  string
		text  string
	}{
		{"workspaces one below", limitStatus{Limit: "workspaces", Used: limit(1), Allowed: limit(2)}, "", ""},
		{"workspaces reached", limitStatus{Limit: "workspaces", Used: limit(2), Allowed: limit(2)}, "LIMIT_REACHED", "This account uses 2 of its 2 workspaces."},
		{"object types one below", limitStatus{Limit: "object_types", Workspace: "Prod", Used: limit(1), Allowed: limit(2)}, "LIMIT_NEAR", `Workspace "Prod" uses 1 of its 2 custom object types.`},
		{"object types under", limitStatus{Limit: "object_types", Workspace: "Prod", Used: limit(0), Allowed: limit(2)}, "", ""},
		{"segments one below", limitStatus{Limit: "data_driven_segments", Workspace: "Prod", Used: limit(9), Allowed: limit(10), limitMessage: "Upgrade for more."}, "LIMIT_NEAR", `Workspace "Prod" uses 9 of its 10 data-driven segments.`},
		{"segments reached", limitStatus{Limit: "data_driven_segments", Workspace: "Prod", Used: limit(10), Allowed: limit(10), limitMessage: "Upgrade for more."}, "LIMIT_REACHED", "Upgrade for more."},
		{"no limit", limitStatus{Limit: "data_driven_segments", Workspace: "Prod", Used: limit(400)}, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			warnings := limitWarnings([]limitStatus{tc.limit})
			if tc.want == "" {
				if len(warnings) != 0 {
					t.Errorf("expected no warning, got %+v", warnings)
				}
				return
			}
			if len(warnings) != 1 || warnings[0].Code != tc.want || warnings[0].Message != tc.text {
				t.Errorf("expected %s %q, got %+v", tc.want, tc.text, warnings)
			}
		})
	}
}

// statusServer answers the reads billing status makes, and records each path.
type statusServer struct {
	*httptest.Server
	paths     []string
	responses map[string]string
	statuses  map[string]int
}

func newStatusServer(t *testing.T, account string) *statusServer {
	t.Helper()
	today := time.Now().UTC()
	s := &statusServer{
		statuses: map[string]int{},
		responses: map[string]string{
			"/v1/accounts/current": `{"account":` + account + `,"environments":[{"id":"101","name":"Production"},{"id":102,"name":"Staging"}]}`,
			"/v1/accounts/1/billing/wallet/usage": fmt.Sprintf(`{"start":%q,"end":%q,"balance":{"used":10,"remaining":51.2},"meters":{
				"email_sends":{"grant_total":0,"grant_used":0,"grant_remaining":0,"balance_used":5000,"balance_cost":10,"total_used":5000}}}`,
				today.AddDate(0, 0, -9).Format(time.DateOnly), today.AddDate(0, 0, 20).Format(time.DateOnly)),
			"/v1/accounts/1/billing/wallet/rates": `{"rates":{"email_sends":0.002}}`,
			"/v1/accounts/1/usage_v2":             `{"account_usage":{"environment_usages":[{"environment_id":101,"object_types_total":1,"object_types_allotted":2}]}}`,
			"/v1/environments/101/settings/workspace": `{"settings":{"data_driven_segments_used":10,"data_driven_segments_allotted":10,
				"data_driven_segments_limit_message":"You've reached your plan's data-driven segment limit."}}`,
			"/v1/environments/102/settings/workspace": `{"settings":{"data_driven_segments_used":2,"data_driven_segments_allotted":10}}`,
		},
	}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/service_accounts/oauth/token" {
			_, _ = w.Write([]byte(`{"access_token":"jwt-test-session","token_type":"Bearer","expires_in":3600,"scope":"read_only"}`))
			return
		}
		s.paths = append(s.paths, r.URL.Path)
		if r.Method != http.MethodGet {
			t.Errorf("expected only GETs, got %s %s", r.Method, r.URL.Path)
		}
		body, ok := s.responses[r.URL.Path]
		if !ok {
			t.Errorf("unexpected request to %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if status := s.statuses[r.URL.Path]; status != 0 {
			w.WriteHeader(status)
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(s.Close)
	return s
}

const flexStatusAccount = `{"id":1,"billing_vendor":"modern_stripe","gated_features":{"workspaces":{"limit":2},"object_types":{"limit":2},"messaging":{}}}`

func TestBillingStatus_ReportsFundsMetersAndLimits(t *testing.T) {
	setupAddFunds(t)
	server := newStatusServer(t, flexStatusAccount)

	stdout, stderr, err := executeCommand("billing", "status", "--api-url", server.URL, "--read-only")
	if err != nil {
		t.Fatalf("unexpected error: %v\nstderr: %s", err, stderr)
	}

	var status billingStatus
	if err := json.Unmarshal([]byte(stdout), &status); err != nil {
		t.Fatalf("invalid JSON output: %v\nstdout: %s", err, stdout)
	}
	if status.Funds.BalanceUSD != 51.2 || status.Funds.SpentUSD != 10 || status.Funds.RunsOutOn != nil {
		t.Errorf("unexpected funds: %+v", status.Funds)
	}
	if rate := status.Meters["email_sends"].RateUSD; rate == nil || *rate != 0.002 {
		t.Errorf("expected the email rate from wallet/rates, got %v", rate)
	}

	type key struct{ limit, workspace string }
	got := map[key]limitStatus{}
	for _, l := range status.Limits {
		got[key{l.Limit, l.WorkspaceID}] = l
	}
	if l := got[key{"workspaces", ""}]; l.Used == nil || *l.Used != 2 || l.Allowed == nil || *l.Allowed != 2 {
		t.Errorf("expected 2 of 2 workspaces, got %+v", l)
	}
	if l := got[key{"object_types", "102"}]; l.Used == nil || *l.Used != 0 || l.Allowed == nil || *l.Allowed != 2 || l.Workspace != "Staging" {
		t.Errorf("expected a workspace missing from usage_v2 to use 0 of the gated 2 object types, got %+v", l)
	}
	if len(status.Limits) != 5 {
		t.Errorf("expected workspaces plus two limits per workspace, got %+v", status.Limits)
	}

	if !slices.Equal(warningCodes(status.Warnings), []string{"LIMIT_REACHED", "LIMIT_NEAR", "LIMIT_REACHED"}) {
		t.Fatalf("expected workspaces reached, object types near and segments reached, got %+v", status.Warnings)
	}
	if w := status.Warnings[2]; w.WorkspaceID != "101" || w.Message != "You've reached your plan's data-driven segment limit." {
		t.Errorf("expected the server's segment limit message, got %+v", w)
	}
}

func TestBillingStatus_NoWarningsIsAnEmptyList(t *testing.T) {
	setupAddFunds(t)
	server := newStatusServer(t, `{"id":1,"billing_vendor":"modern_stripe","gated_features":{}}`)
	server.responses["/v1/accounts/current"] = `{"account":{"id":1,"billing_vendor":"modern_stripe","gated_features":{}},"environments":[]}`

	stdout, stderr, err := executeCommand("billing", "status", "--api-url", server.URL, "--jq", ".warnings")
	if err != nil {
		t.Fatalf("unexpected error: %v\nstderr: %s", err, stderr)
	}
	if stdout != "[]\n" {
		t.Errorf("expected an empty warnings list, got %q", stdout)
	}
}

func TestBillingStatus_JQReadsAField(t *testing.T) {
	setupAddFunds(t)
	server := newStatusServer(t, flexStatusAccount)

	stdout, stderr, err := executeCommand("billing", "status", "--api-url", server.URL, "--jq", ".funds.balance_usd", "-r")
	if err != nil {
		t.Fatalf("unexpected error: %v\nstderr: %s", err, stderr)
	}
	if stdout != "51.2\n" {
		t.Errorf("expected 51.2, got %q", stdout)
	}
}

func TestBillingStatus_RefusesOtherPlans(t *testing.T) {
	setupAddFunds(t)
	server := newStatusServer(t, `{"id":1,"billing_vendor":"ordway"}`)

	stdout, _, err := executeCommand("billing", "status", "--api-url", server.URL)
	if err == nil {
		t.Fatalf("expected an error, got stdout: %s", stdout)
	}
	if !strings.Contains(err.Error(), "Flex") {
		t.Errorf("expected the error to name the Flex plan, got %q", err)
	}
	if !slices.Equal(server.paths, []string{"/v1/accounts/current"}) {
		t.Errorf("expected only the account read, got %v", server.paths)
	}
}

func TestBillingStatus_PassesOnTheWalletRefusal(t *testing.T) {
	cases := map[string]struct {
		status int
		body   string
		want   string
	}{
		"not on the Flex plan": {http.StatusBadRequest, `{"errors":[{"detail":"the wallet is only available to Flex plan accounts","status":"400"}]}`, "the wallet is only available to Flex plan accounts"},
		"no billing access":    {http.StatusForbidden, `{"errors":[{"detail":"missing account/billing/view","status":"403"}]}`, "can't read this account's billing"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			setupAddFunds(t)
			server := newStatusServer(t, flexStatusAccount)
			server.statuses["/v1/accounts/1/billing/wallet/usage"] = tc.status
			server.responses["/v1/accounts/1/billing/wallet/usage"] = tc.body

			stdout, _, err := executeCommand("billing", "status", "--api-url", server.URL)
			if err == nil {
				t.Fatalf("expected an error, got stdout: %s", stdout)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("expected %q in the error, got %q", tc.want, err)
			}
			if strings.Contains(err.Error(), "account/billing") {
				t.Errorf("expected the error not to name the permission, got %q", err)
			}
		})
	}
}

func TestBillingStatus_ReportsTheRestWhenAWorkspaceIsUnreadable(t *testing.T) {
	setupAddFunds(t)
	server := newStatusServer(t, flexStatusAccount)
	server.statuses["/v1/environments/102/settings/workspace"] = http.StatusForbidden
	server.responses["/v1/environments/102/settings/workspace"] = `{"errors":[{"detail":"forbidden","status":"403"}]}`

	stdout, stderr, err := executeCommand("billing", "status", "--api-url", server.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v\nstderr: %s", err, stderr)
	}
	var status billingStatus
	if err := json.Unmarshal([]byte(stdout), &status); err != nil {
		t.Fatalf("invalid JSON output: %v\nstdout: %s", err, stdout)
	}
	if status.Funds.BalanceUSD != 51.2 || len(status.Limits) != 5 {
		t.Fatalf("expected the full report, got funds %+v and limits %+v", status.Funds, status.Limits)
	}
	var segments *limitStatus
	for i, l := range status.Limits {
		if l.Limit == "data_driven_segments" && l.WorkspaceID == "102" {
			segments = &status.Limits[i]
		}
	}
	if segments == nil || segments.Used != nil || segments.Allowed != nil || segments.Error != "this login can't read this workspace's settings" {
		t.Errorf("expected the unreadable workspace's segments entry to carry the error, got %+v", segments)
	}
	if strings.Contains(stderr, "billing and usage") || !strings.Contains(stderr, `"code":"LIMIT_UNREADABLE"`) || !strings.Contains(stderr, `Workspace \"Staging\"`) {
		t.Errorf("expected a LIMIT_UNREADABLE warning naming the workspace, got stderr: %s", stderr)
	}
}

func TestBillingStatus_DryRunReadsNothing(t *testing.T) {
	setupAddFunds(t)
	server := newStatusServer(t, flexStatusAccount)

	stdout, stderr, err := executeCommand("billing", "status", "--api-url", server.URL, "--dry-run")
	if err != nil {
		t.Fatalf("unexpected error: %v\nstderr: %s", err, stderr)
	}
	if len(server.paths) != 0 {
		t.Errorf("expected no requests, got %v", server.paths)
	}
	var result struct {
		DryRun bool     `json:"dry_run"`
		URLs   []string `json:"urls"`
	}
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("invalid JSON output: %v\nstdout: %s", err, stdout)
	}
	if !result.DryRun || len(result.URLs) != 5 || result.URLs[1] != server.URL+"/v1/accounts/{account_id}/billing/wallet/usage" {
		t.Errorf("expected the five reads, got %+v", result)
	}
}
