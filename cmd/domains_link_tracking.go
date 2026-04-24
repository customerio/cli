package cmd

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/customerio/cli/internal/output"
	"github.com/spf13/cobra"
)

var domainsLinkTrackingCmd = &cobra.Command{
	Use:   "link_tracking",
	Short: "Configure and verify link tracking for a domain",
	Long: `Configure a custom subdomain for tracked links in emails and verify DNS records.

Link tracking lets you use your own domain for tracked links in emails,
improving deliverability and brand consistency.

Subcommands:
  configure   Set the link tracking subdomain
  verify      Check link tracking DNS record status`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return cmd.Help()
	},
}

var domainsLinkTrackingConfigureCmd = &cobra.Command{
	Use:   "configure <domain>",
	Short: "Set the link tracking subdomain",
	Long: `Set the link tracking subdomain for a domain.

PUT /v1/environments/:env_id/domains/:domain_id

The --subdomain flag accepts either a bare subdomain (e.g. "email") or
a full subdomain (e.g. "email.example.com"). When a bare subdomain is
given, the domain name is appended automatically.

Returns the updated domain with DNS records to configure at your provider.

Examples:
  cio domains --env-id 456 link_tracking configure example.com --subdomain email
  cio domains --env-id 456 link_tracking configure example.com --subdomain email.example.com`,
	Args: requireArg("domain"),
	RunE: runLinkTrackingConfigure,
}

var domainsLinkTrackingVerifyCmd = &cobra.Command{
	Use:   "verify <domain>",
	Short: "Check link tracking DNS record status",
	Long: `Check the current DNS record verification status for link tracking.

POST /v1/environments/:env_id/domains/:domain_id/check_dns?flow=link_tracking

Returns per-record pass/fail status for CNAME and TXT records. For
failing records, shows the expected value to configure at your DNS provider.

Examples:
  cio domains --env-id 456 link_tracking verify example.com`,
	Args: requireArg("domain"),
	RunE: runLinkTrackingVerify,
}

func init() {
	domainsLinkTrackingConfigureCmd.Flags().String("subdomain", "", "Link tracking subdomain, e.g. \"email\" or \"email.example.com\" (required)")

	domainsLinkTrackingCmd.AddCommand(domainsLinkTrackingConfigureCmd)
	domainsLinkTrackingCmd.AddCommand(domainsLinkTrackingVerifyCmd)
	domainsCmd.AddCommand(domainsLinkTrackingCmd)
}

// resolveSubdomain builds the full cname from the --subdomain flag.
// Accepts "email" (bare) or "email.example.com" (full).
// When bare, appends the domain name: "email" + "example.com" → "email.example.com".
// When full, validates it ends with the parent domain name.
func resolveSubdomain(subdomain, domainName string) (string, error) {
	if strings.Contains(subdomain, ".") {
		if !strings.HasSuffix(subdomain, "."+domainName) {
			return "", fmt.Errorf("subdomain %q does not match domain %q", subdomain, domainName)
		}
		return subdomain, nil
	}
	return subdomain + "." + domainName, nil
}

func runLinkTrackingConfigure(cmd *cobra.Command, args []string) error {
	c := clientFromCmd(cmd)
	if c == nil {
		return errNoClient(cmd)
	}

	envID, err := resolveEnvID(cmd)
	if err != nil {
		return err
	}

	dom, err := resolveDomain(cmd.Context(), c, envID, args[0])
	if err != nil {
		output.PrintError(output.CodeValidationError, err.Error(), map[string]any{
			"argument": "domain",
		})
		return err
	}

	subdomain, _ := cmd.Flags().GetString("subdomain")
	if subdomain == "" {
		valErr := fmt.Errorf("--subdomain is required")
		output.PrintError(output.CodeValidationError, valErr.Error(), map[string]any{
			"flag": "--subdomain",
			"hint": `Example: --subdomain email`,
		})
		return valErr
	}
	if err := validateStringInput("subdomain", subdomain); err != nil {
		output.PrintError(output.CodeValidationError, err.Error(), map[string]any{
			"flag": "--subdomain",
		})
		return err
	}

	cname, err := resolveSubdomain(subdomain, dom.Name)
	if err != nil {
		output.PrintError(output.CodeValidationError, err.Error(), map[string]any{
			"flag": "--subdomain",
		})
		return err
	}

	body, err := json.Marshal(map[string]any{
		"cname": cname,
	})
	if err != nil {
		return err
	}

	path := fmt.Sprintf("/v1/environments/%s/domains/%s", envID, dom.ID)

	if GetDryRun(cmd) {
		return output.FprintJSON(cmd.OutOrStdout(), map[string]any{
			"dry_run": true,
			"method":  "PUT",
			"url":     c.BaseURL() + path,
			"body":    json.RawMessage(body),
		})
	}

	result, err := c.Do(cmd.Context(), "PUT", path, nil, body)
	if err != nil {
		return handleAPIError(err)
	}
	return output.FprintProcess(cmd.OutOrStdout(), result, GetJQFlag(cmd))
}

func runLinkTrackingVerify(cmd *cobra.Command, args []string) error {
	c := clientFromCmd(cmd)
	if c == nil {
		return errNoClient(cmd)
	}

	envID, err := resolveEnvID(cmd)
	if err != nil {
		return err
	}

	dom, err := resolveDomain(cmd.Context(), c, envID, args[0])
	if err != nil {
		output.PrintError(output.CodeValidationError, err.Error(), map[string]any{
			"argument": "domain",
		})
		return err
	}

	path := fmt.Sprintf("/v1/environments/%s/domains/%s/check_dns", envID, dom.ID)
	params := map[string]string{"flow": "link_tracking"}

	if GetDryRun(cmd) {
		return output.FprintJSON(cmd.OutOrStdout(), map[string]any{
			"dry_run": true,
			"method":  "POST",
			"url":     c.BaseURL() + path,
			"params":  params,
		})
	}

	result, err := c.Do(cmd.Context(), "POST", path, params, nil)
	if err != nil {
		return handleAPIError(err)
	}

	if GetJQFlag(cmd) != "" {
		return output.FprintProcess(cmd.OutOrStdout(), result, GetJQFlag(cmd))
	}

	return printDNSCheckResult(cmd, result, dom.Name, "link_tracking", "cio domains link_tracking verify")
}
