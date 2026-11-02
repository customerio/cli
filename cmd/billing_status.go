package cmd

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/customerio/cli/internal/client"
	"github.com/customerio/cli/internal/output"
	"github.com/customerio/cli/internal/validate"
	"github.com/spf13/cobra"
)

var billingStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show Flex funds, free grants, forecasts and plan limits",
	Long: `Show what a Flex account has left this billing period, when funds or a free
grant will run out at the current rate, and how close each workspace is to its
plan limits. Read-only, so it runs under --read-only.

Forecasts use the average daily usage so far this period and stop at the
period end, when the free grants renew. When a meter's free grant runs out, its
usage is paid for from funds, so the funds forecast speeds up from that day.
Agent usage is the exception: it can't draw on funds, so it stops instead.

Flex accounts only. The login needs access to the account's billing.

Output:
  period     the billing period's first and last day
  funds      balance_usd, spent_usd this period, daily_spend_usd, and
             runs_out_on (today when the balance is zero, null when funds
             last the period)
  meters     per meter: usage, free grant left, grant_runs_out_on (today
             once it's used up, null when it lasts the period), and
             rate_usd per unit
  limits     workspaces, and custom object types and data-driven segments
             per workspace (allowed is null when there's no limit). Counts
             cover the workspaces this login can see. A workspace whose
             settings can't be read gets an error on its segments entry,
             with used and allowed null, and the rest still reports.
  warnings   FUNDS_LOW, GRANT_LOW, LIMIT_NEAR or LIMIT_REACHED, each with a
             message to pass on

Examples:
  cio billing status
  cio billing status --jq .warnings
  cio billing status --jq .funds.runs_out_on -r`,
	Args: cobra.NoArgs,
	RunE: runBillingStatus,
}

func init() {
	billingCmd.AddCommand(billingStatusCmd)
}

// agentUsageMeter only draws on its own grant, never on funds.
const agentUsageMeter = "agent_usage"

type billingAccount struct {
	ID            json.Number             `json:"id"`
	BillingVendor string                  `json:"billing_vendor"`
	GatedFeatures map[string]gatedFeature `json:"gated_features"`
}

type gatedFeature struct {
	Limit int `json:"limit"`
}

type walletUsage struct {
	Start   string                 `json:"start"`
	End     string                 `json:"end"`
	Balance walletBalance          `json:"balance"`
	Meters  map[string]walletMeter `json:"meters"`
}

type walletBalance struct {
	Used      float64 `json:"used"`
	Remaining float64 `json:"remaining"`
}

type walletMeter struct {
	GrantTotal     int64   `json:"grant_total"`
	GrantUsed      int64   `json:"grant_used"`
	GrantRemaining int64   `json:"grant_remaining"`
	BalanceUsed    int64   `json:"balance_used"`
	BalanceCost    float64 `json:"balance_cost"`
	TotalUsed      int64   `json:"total_used"`
}

type billingStatus struct {
	Period   billingPeriod          `json:"period"`
	Funds    fundsStatus            `json:"funds"`
	Meters   map[string]meterStatus `json:"meters"`
	Limits   []limitStatus          `json:"limits"`
	Warnings []billingWarning       `json:"warnings"`
}

type billingPeriod struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

type fundsStatus struct {
	BalanceUSD    float64 `json:"balance_usd"`
	SpentUSD      float64 `json:"spent_usd"`
	DailySpendUSD float64 `json:"daily_spend_usd"`
	RunsOutOn     *string `json:"runs_out_on"`
}

type meterStatus struct {
	TotalUsed      int64    `json:"total_used"`
	GrantTotal     int64    `json:"grant_total"`
	GrantUsed      int64    `json:"grant_used"`
	GrantRemaining int64    `json:"grant_remaining"`
	GrantRunsOutOn *string  `json:"grant_runs_out_on"`
	BalanceUsed    int64    `json:"balance_used"`
	BalanceCostUSD float64  `json:"balance_cost_usd"`
	RateUSD        *float64 `json:"rate_usd"`
}

type limitStatus struct {
	Limit       string `json:"limit"`
	WorkspaceID string `json:"workspace_id,omitempty"`
	Workspace   string `json:"workspace,omitempty"`
	Used        *int   `json:"used"`
	Allowed     *int   `json:"allowed"`
	Error       string `json:"error,omitempty"`
	// limitMessage is the server's own wording for reaching this limit.
	limitMessage string
}

type billingWarning struct {
	Code        string `json:"code"`
	Message     string `json:"message"`
	Meter       string `json:"meter,omitempty"`
	Limit       string `json:"limit,omitempty"`
	WorkspaceID string `json:"workspace_id,omitempty"`
}

type workspaceRef struct {
	ID   json.Number `json:"id"`
	Name string      `json:"name"`
}

func runBillingStatus(cmd *cobra.Command, args []string) error {
	c := clientFromCmd(cmd)
	if c == nil {
		return errNoClient(cmd)
	}

	if GetDryRun(cmd) {
		urls := []string{"/v1/accounts/current"}
		for _, p := range billingStatusAccountPaths {
			urls = append(urls, "/v1/accounts/{account_id}"+p)
		}
		urls = append(urls, "/v1/environments/{environment_id}/settings/workspace")
		for i := range urls {
			urls[i] = c.BaseURL() + urls[i]
		}
		return output.FprintJSON(cmd.OutOrStdout(), map[string]any{
			"dry_run": true,
			"method":  "GET",
			"urls":    urls,
		})
	}

	ctx := cmd.Context()
	get := func(path string, into any) error {
		raw, err := c.Do(ctx, "GET", path, nil, nil)
		if err != nil {
			return handleBillingStatusError(err)
		}
		if err := json.Unmarshal(raw, into); err != nil {
			output.PrintError(output.CodeGeneralError, "failed to parse the response from "+path, nil)
			return err
		}
		return nil
	}

	var current struct {
		Account billingAccount `json:"account"`
		// The workspaces this login can see, which for an admin is all of them.
		Environments []workspaceRef `json:"environments"`
	}
	if err := get("/v1/accounts/current", &current); err != nil {
		return err
	}
	account := current.Account
	if account.BillingVendor != flexBillingVendor {
		err := fmt.Errorf("billing status is only available on the Flex plan")
		output.PrintError(output.CodeValidationError, err.Error(), map[string]any{
			"hint": "This account isn't on Flex, so it has no balance or free grants. Its plan is under Settings → Billing in Customer.io.",
		})
		return err
	}
	accountID := account.ID.String()
	if err := validate.ValidateResourceID(accountID); err != nil {
		output.PrintError(output.CodeGeneralError, "the account read returned an invalid account id", nil)
		return err
	}
	accountPath := "/v1/accounts/" + url.PathEscape(accountID)

	var usage walletUsage
	if err := get(accountPath+"/billing/wallet/usage", &usage); err != nil {
		return err
	}
	var rates struct {
		Rates map[string]float64 `json:"rates"`
	}
	if err := get(accountPath+"/billing/wallet/rates", &rates); err != nil {
		return err
	}
	var accountUsage struct {
		AccountUsage struct {
			EnvironmentUsages []struct {
				EnvironmentID       json.Number `json:"environment_id"`
				ObjectTypesTotal    int         `json:"object_types_total"`
				ObjectTypesAllotted int         `json:"object_types_allotted"`
			} `json:"environment_usages"`
		} `json:"account_usage"`
	}
	if err := get(accountPath+"/usage_v2", &accountUsage); err != nil {
		return err
	}

	objectTypesLimit := account.GatedFeatures["object_types"].Limit
	objectTypes := map[string]limitStatus{}
	for _, u := range accountUsage.AccountUsage.EnvironmentUsages {
		allowed := objectTypesLimit
		if allowed <= 0 {
			allowed = u.ObjectTypesAllotted
		}
		objectTypes[u.EnvironmentID.String()] = limitStatus{Used: intPtr(u.ObjectTypesTotal), Allowed: positiveLimit(allowed)}
	}

	limits := []limitStatus{{
		Limit:   "workspaces",
		Used:    intPtr(len(current.Environments)),
		Allowed: positiveLimit(account.GatedFeatures["workspaces"].Limit),
	}}
	for _, env := range current.Environments {
		envID := env.ID.String()
		if err := validate.ValidateResourceID(envID); err != nil {
			output.PrintError(output.CodeGeneralError, "the workspace list returned an invalid workspace id", nil)
			return err
		}
		objects, ok := objectTypes[envID]
		if !ok {
			objects = limitStatus{Used: intPtr(0), Allowed: positiveLimit(objectTypesLimit)}
		}
		objects.Limit, objects.WorkspaceID, objects.Workspace = "object_types", envID, env.Name

		segments := limitStatus{Limit: "data_driven_segments", WorkspaceID: envID, Workspace: env.Name}
		path := "/v1/environments/" + url.PathEscape(envID) + "/settings/workspace"
		raw, err := c.Do(ctx, "GET", path, nil, nil)
		var apiErr *client.APIError
		switch {
		case errors.As(err, &apiErr):
			// One workspace the login can't read shouldn't cost the rest of
			// the report, so its segment limit is left unknown.
			segments.Error = workspaceSettingsError(apiErr.StatusCode)
			printWarning(cmd, "LIMIT_UNREADABLE", fmt.Sprintf("Workspace %q's data-driven segment limit is missing from limits: %s.", env.Name, segments.Error))
		case err != nil:
			return handleBillingStatusError(err)
		default:
			var settings struct {
				Settings struct {
					DataDrivenSegmentsUsed         int    `json:"data_driven_segments_used"`
					DataDrivenSegmentsAllotted     int    `json:"data_driven_segments_allotted"`
					DataDrivenSegmentsLimitMessage string `json:"data_driven_segments_limit_message"`
				} `json:"settings"`
			}
			if err := json.Unmarshal(raw, &settings); err != nil {
				output.PrintError(output.CodeGeneralError, "failed to parse the response from "+path, nil)
				return err
			}
			segments.Used = intPtr(settings.Settings.DataDrivenSegmentsUsed)
			segments.Allowed = positiveLimit(settings.Settings.DataDrivenSegmentsAllotted)
			segments.limitMessage = strings.TrimSpace(settings.Settings.DataDrivenSegmentsLimitMessage)
		}
		limits = append(limits, objects, segments)
	}

	period, funds, meters, warnings, err := forecastWallet(time.Now(), usage, rates.Rates)
	if err != nil {
		output.PrintError(output.CodeGeneralError, err.Error(), nil)
		return err
	}
	status := billingStatus{
		Period:   period,
		Funds:    funds,
		Meters:   meters,
		Limits:   limits,
		Warnings: append(append([]billingWarning{}, warnings...), limitWarnings(limits)...),
	}
	data, _ := json.Marshal(status)
	return output.FprintProcess(cmd.OutOrStdout(), data, GetJQFlag(cmd), GetRawFlag(cmd))
}

// billingStatusAccountPaths are the account reads after /v1/accounts/current,
// in the order the command makes them.
var billingStatusAccountPaths = []string{
	"/billing/wallet/usage",
	"/billing/wallet/rates",
	"/usage_v2",
}

// handleBillingStatusError passes on the wallet's reason for refusing a read,
// such as an account that isn't on the Flex plan, and says plainly when the
// login can't read billing.
func handleBillingStatusError(err error) error {
	var apiErr *client.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.StatusCode {
		case http.StatusForbidden:
			msg := "this login can't read this account's billing and usage"
			output.PrintError(output.CodeAuthzError, msg, map[string]any{
				"status_code": apiErr.StatusCode,
				"hint":        "Ask an account admin for access to billing, or sign in as someone who has it.",
			})
			return errors.New(msg)
		case http.StatusBadRequest:
			if detail := apiErrorDetail(apiErr.Body); detail != "" {
				output.PrintError(output.CodeValidationError, detail, map[string]any{
					"status_code": apiErr.StatusCode,
				})
				return errors.New(detail)
			}
		}
	}
	return handleAPIError(err)
}

// forecastWallet projects this period's usage forward at its average daily
// rate. Funds drain at the balance's daily spend, and each meter whose grant
// runs out adds the rate its grant was covering from that day on.
func forecastWallet(now time.Time, usage walletUsage, rates map[string]float64) (billingPeriod, fundsStatus, map[string]meterStatus, []billingWarning, error) {
	period := billingPeriod{Start: usage.Start, End: usage.End}
	start, err := time.Parse(time.DateOnly, usage.Start)
	if err != nil {
		return period, fundsStatus{}, nil, nil, fmt.Errorf("the wallet usage read returned an invalid period start %q", usage.Start)
	}
	lastDay, err := time.Parse(time.DateOnly, usage.End)
	if err != nil {
		return period, fundsStatus{}, nil, nil, fmt.Errorf("the wallet usage read returned an invalid period end %q", usage.End)
	}
	now = now.UTC()
	periodEnd := lastDay.Add(24 * time.Hour)
	// A few hours into a period are too few to extrapolate from.
	elapsed := max(now.Sub(start).Hours()/24, 1)
	daysLeft := max(periodEnd.Sub(now).Hours()/24, 0)
	at := func(days float64) time.Time { return now.Add(time.Duration(days * 24 * float64(time.Hour))) }

	type rollover struct{ day, perDay float64 }
	dailySpend := usage.Balance.Used / elapsed
	var later []rollover
	var warnings []billingWarning
	meters := make(map[string]meterStatus, len(usage.Meters))

	names := make([]string, 0, len(usage.Meters))
	for name := range usage.Meters {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		m := usage.Meters[name]
		status := meterStatus{
			TotalUsed:      m.TotalUsed,
			GrantTotal:     m.GrantTotal,
			GrantUsed:      m.GrantUsed,
			GrantRemaining: m.GrantRemaining,
			BalanceUsed:    m.BalanceUsed,
			BalanceCostUSD: roundCents(m.BalanceCost),
		}
		rate, priced := rates[name]
		if priced {
			status.RateUSD = &rate
		}

		grantPerDay := float64(m.GrantUsed) / elapsed
		if m.GrantTotal > 0 && grantPerDay > 0 {
			day := max(float64(m.GrantRemaining)/grantPerDay, 0)
			if day < daysLeft {
				runsOut := at(day)
				status.GrantRunsOutOn = dateOf(runsOut)
				// A used-up grant's usage is already in the funds forecast, so
				// only agent usage, which stops instead, still warns about it.
				if day > 0 || name == agentUsageMeter {
					warnings = append(warnings, grantWarning(name, day == 0, runsOut, lastDay))
				}
				if name != agentUsageMeter && priced {
					if day == 0 {
						dailySpend += grantPerDay * rate
					} else {
						later = append(later, rollover{day, grantPerDay * rate})
					}
				}
			}
		}
		meters[name] = status
	}

	funds := fundsStatus{
		BalanceUSD:    roundCents(usage.Balance.Remaining),
		SpentUSD:      roundCents(usage.Balance.Used),
		DailySpendUSD: roundCents(dailySpend),
	}
	var fundsWarning *billingWarning
	if usage.Balance.Remaining <= 0 {
		funds.RunsOutOn = dateOf(now)
		fundsWarning = &billingWarning{
			Code:    "FUNDS_LOW",
			Message: "No funds left. New active profiles beyond the free grant are dropped, and metered sends may fail. Run 'cio billing add-funds' to add funds.",
		}
	} else {
		slices.SortFunc(later, func(a, b rollover) int { return cmp.Compare(a.day, b.day) })
		later = append(later, rollover{day: daysLeft})
		balance, day, rate := usage.Balance.Remaining, 0.0, dailySpend
		for _, next := range later {
			if rate > 0 && day+balance/rate < next.day {
				runsOut := at(day + balance/rate)
				funds.RunsOutOn = dateOf(runsOut)
				fundsWarning = &billingWarning{
					Code: "FUNDS_LOW",
					Message: fmt.Sprintf("Funds run out around %s at the current rate, before the period ends %s. Run 'cio billing add-funds' to add funds.",
						runsOut.Format("Jan 2"), lastDay.Format("Jan 2")),
				}
				break
			}
			balance -= rate * (next.day - day)
			day, rate = next.day, rate+next.perDay
		}
	}
	if fundsWarning != nil {
		warnings = append([]billingWarning{*fundsWarning}, warnings...)
	}
	return period, funds, meters, warnings, nil
}

func grantWarning(meter string, usedUp bool, runsOut, lastDay time.Time) billingWarning {
	label := strings.ReplaceAll(meter, "_", " ")
	var msg string
	switch {
	case meter == agentUsageMeter && usedUp:
		msg = fmt.Sprintf("The free %s grant is used up, and %s can't be paid for from funds.", label, label)
	case meter == agentUsageMeter:
		msg = fmt.Sprintf("The free %s grant runs out around %s, before the period ends %s. %s can't be paid for from funds, so it stops then.",
			label, runsOut.Format("Jan 2"), lastDay.Format("Jan 2"), capitalize(label))
	default:
		msg = fmt.Sprintf("The free %s grant runs out around %s, before the period ends %s. After that, %s are paid for from funds.",
			label, runsOut.Format("Jan 2"), lastDay.Format("Jan 2"), label)
	}
	return billingWarning{Code: "GRANT_LOW", Message: msg, Meter: meter}
}

// limitWarnings flags each limit that is reached, or one below. Workspaces
// only warn once reached: every account has at least one, so with a limit of
// two the near warning would never go away.
func limitWarnings(limits []limitStatus) []billingWarning {
	var warnings []billingWarning
	for _, l := range limits {
		if l.Allowed == nil || l.Used == nil {
			continue
		}
		used, allowed := *l.Used, *l.Allowed
		var code string
		switch {
		case used >= allowed:
			code = "LIMIT_REACHED"
		case used == allowed-1 && l.Limit != "workspaces":
			code = "LIMIT_NEAR"
		default:
			continue
		}

		msg := l.limitMessage
		if code != "LIMIT_REACHED" || msg == "" {
			switch l.Limit {
			case "workspaces":
				msg = fmt.Sprintf("This account uses %d of its %d workspaces.", used, allowed)
			case "object_types":
				msg = fmt.Sprintf("Workspace %q uses %d of its %d custom object types.", l.Workspace, used, allowed)
			default:
				msg = fmt.Sprintf("Workspace %q uses %d of its %d data-driven segments.", l.Workspace, used, allowed)
			}
		}
		warnings = append(warnings, billingWarning{Code: code, Message: msg, Limit: l.Limit, WorkspaceID: l.WorkspaceID})
	}
	return warnings
}

// workspaceSettingsError says why a workspace's settings couldn't be read.
func workspaceSettingsError(status int) string {
	if status == http.StatusForbidden {
		return "this login can't read this workspace's settings"
	}
	return fmt.Sprintf("reading this workspace's settings failed with status %d", status)
}

func intPtr(n int) *int {
	return &n
}

// positiveLimit reads a limit of 0 as no limit.
func positiveLimit(n int) *int {
	if n <= 0 {
		return nil
	}
	return &n
}

func dateOf(t time.Time) *string {
	s := t.UTC().Format(time.DateOnly)
	return &s
}

func roundCents(usd float64) float64 {
	return math.Round(usd*100) / 100
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
