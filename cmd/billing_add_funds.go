package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/customerio/cli/internal/client"
	"github.com/customerio/cli/internal/output"
	"github.com/spf13/cobra"
)

var billingAddFundsCmd = &cobra.Command{
	Use:   "add-funds",
	Short: "Print a sign-in link that opens Add funds",
	Long: `Print a one-time link that signs the browser in to Customer.io and opens
Add funds on the billing page. The CLI never charges anything: the person
picks an amount and pays on that page.

The link is valid for 60 seconds and works once. If it expires, run the
command again for a fresh one.

Flex accounts only. The person who created the token needs permission to
manage billing.

An account that requires SSO can't use sign-in links, so the command prints
the billing page URL instead, with sign_in_required set. The person opens it
and signs in with SSO there.

Output:
  {"status":"ok","url":"https://fly.customer.io/cli?token=...","expires_in":60}
  {"status":"ok","url":"https://fly.customer.io/settings/billing/...","sign_in_required":true}

Examples:
  cio billing add-funds
  cio billing add-funds --jq .url -r`,
	Args: cobra.NoArgs,
	RunE: runBillingAddFunds,
}

func init() {
	billingCmd.AddCommand(billingAddFundsCmd)
}

// flexBillingVendor is the billing_vendor the account read reports for Flex.
const flexBillingVendor = "modern_stripe"

// addFundsPagePath is the billing page with Add funds open, where an add_funds
// sign-in link lands. from=cli reports the purchase as one started from the CLI.
const addFundsPagePath = "/settings/billing/your-plan?purchaseModal=account-funds&from=cli"

func runBillingAddFunds(cmd *cobra.Command, args []string) error {
	c := clientFromCmd(cmd)
	if c == nil {
		return errNoClient(cmd)
	}

	// The link is minted outside the client's request path, which is where
	// --read-only is otherwise enforced, and the session it opens can spend.
	if c.ReadOnly() {
		err := fmt.Errorf("read-only mode: add-funds creates a sign-in link (use without --read-only to allow it)")
		output.PrintError(output.CodeValidationError, err.Error(), nil)
		return err
	}

	// Only a service account token can mint a link, and a pre-exchanged access
	// token may belong to a different account than the one it would mint for.
	if client.ResolveAccessToken() != "" {
		err := fmt.Errorf("add-funds can't run with CIO_ACCESS_TOKEN set: the sign-in link needs a service account token")
		output.PrintError(output.CodeAuthError, err.Error(), map[string]any{
			"hint": "Unset CIO_ACCESS_TOKEN and sign in with 'cio auth login', or set CIO_TOKEN to a service account token.",
		})
		return err
	}
	saToken := c.ServiceAccountToken()
	if saToken == "" {
		err := fmt.Errorf("not authenticated. Run 'cio auth login' or set CIO_TOKEN")
		output.PrintError(output.CodeAuthError, err.Error(), nil)
		return err
	}

	raw, err := c.Do(cmd.Context(), "GET", "/v1/accounts/current", nil, nil)
	if err != nil {
		return handleAPIError(err)
	}
	var resp struct {
		Account struct {
			BillingVendor string `json:"billing_vendor"`
			SSOSetting    string `json:"sso_setting"`
		} `json:"account"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		output.PrintError(output.CodeGeneralError, "failed to parse account response", nil)
		return err
	}

	if resp.Account.BillingVendor != flexBillingVendor {
		err := fmt.Errorf("add funds is only available on the Flex plan")
		output.PrintError(output.CodeValidationError, err.Error(), map[string]any{
			"hint": "This account isn't on Flex, so it has no balance to add funds to. Its plan is under Settings → Billing in Customer.io.",
		})
		return err
	}

	// The sign-in link refuses an account that requires SSO, so send the
	// person to the page itself to sign in there.
	if resp.Account.SSOSetting == "required" {
		pageURL := resolveUIBase(c.BaseURL()) + addFundsPagePath
		if GetDryRun(cmd) {
			return output.FprintJSON(cmd.OutOrStdout(), map[string]any{
				"dry_run":          true,
				"url":              pageURL,
				"sign_in_required": true,
			})
		}
		fmt.Fprintf(cmd.ErrOrStderr(), "This account signs in with SSO, so the CLI can't sign you in. Open this URL in your browser and sign in with SSO to add funds:\n\n    %s\n\n", pageURL)
		return printAddFundsResult(cmd, map[string]any{
			"status":           "ok",
			"message":          "Open the URL in your browser and sign in with SSO to add funds.",
			"url":              pageURL,
			"sign_in_required": true,
		})
	}

	body := map[string]string{"destination": client.LoginCLILinkDestinationAddFunds}
	if GetDryRun(cmd) {
		return output.FprintJSON(cmd.OutOrStdout(), map[string]any{
			"dry_run": true,
			"method":  "POST",
			"url":     c.BaseURL() + "/v1/login_cli/link",
			"headers": map[string]string{
				"Authorization": "Bearer [REDACTED]",
				"Content-Type":  "application/json",
			},
			"body": body,
		})
	}

	timeout, _ := cmd.Flags().GetDuration("timeout")
	link, err := client.MintLoginCLILink(cmd.Context(), c.BaseURL(), saToken, client.LoginCLILinkDestinationAddFunds, timeout)
	if err != nil {
		return handleAddFundsError(err)
	}

	linkURL := resolveCLILoginURL(c.BaseURL()) + "?token=" + url.QueryEscape(link.HandoffToken)

	fmt.Fprintf(cmd.ErrOrStderr(), "Open this URL in your browser to add funds to your Customer.io account:\n\n    %s\n\n", linkURL)
	fmt.Fprintf(cmd.ErrOrStderr(), "This link is valid for %d seconds and works once. If it expires, run 'cio billing add-funds' again.\n", link.ExpiresIn)

	return printAddFundsResult(cmd, map[string]any{
		"status":     "ok",
		"message":    "Open the URL in your browser to add funds.",
		"url":        linkURL,
		"expires_in": link.ExpiresIn,
	})
}

func printAddFundsResult(cmd *cobra.Command, result map[string]any) error {
	data, _ := json.Marshal(result)
	return output.FprintProcess(cmd.OutOrStdout(), json.RawMessage(data), GetJQFlag(cmd), GetRawFlag(cmd))
}

// handleAddFundsError passes on the server's reason for refusing a link, which
// says what plan and permission adding funds needs, where handleAPIError
// would reduce a 403 to "Insufficient permissions."
func handleAddFundsError(err error) error {
	var apiErr *client.APIError
	if errors.As(err, &apiErr) && (apiErr.StatusCode == http.StatusBadRequest || apiErr.StatusCode == http.StatusForbidden) {
		if detail := apiErrorDetail(apiErr.Body); detail != "" {
			code := output.CodeValidationError
			if apiErr.StatusCode == http.StatusForbidden {
				code = output.CodeAuthzError
			}
			output.PrintError(code, detail, map[string]any{
				"status_code": apiErr.StatusCode,
			})
			return errors.New(detail)
		}
	}
	return handleAPIError(err)
}

// apiErrorDetail reads the first message from an {"errors":[{"detail":...}]} body.
func apiErrorDetail(body []byte) string {
	var parsed struct {
		Errors []struct {
			Detail string `json:"detail"`
		} `json:"errors"`
	}
	if json.Unmarshal(body, &parsed) != nil || len(parsed.Errors) == 0 {
		return ""
	}
	return strings.TrimSpace(parsed.Errors[0].Detail)
}
