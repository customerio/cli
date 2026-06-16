package cmd

import (
	"encoding/json"
	"fmt"

	"github.com/customerio/cli/internal/output"
	"github.com/spf13/cobra"
)

var domainsAddCmd = &cobra.Command{
	Use:   "add <domain>",
	Short: "Add a sending domain",
	Long: `Add a new sending domain to an environment.

POST /v1/environments/:env_id/domains

Response includes the created domain, DNS records, dns_provider, and
domain_connect_url (when available).

Examples:
  cio domains --env-id 456 add example.com
  cio domains --env-id 456 add example.com --auto-tls`,
	Args: requireArg("domain"),
	RunE: runDomainsAdd,
}

func init() {
	domainsAddCmd.Flags().Bool("auto-tls", false, "Enable automatic TLS for link tracking")
	domainsCmd.AddCommand(domainsAddCmd)
}

func runDomainsAdd(cmd *cobra.Command, args []string) error {
	c := clientFromCmd(cmd)
	if c == nil {
		return errNoClient(cmd)
	}

	envID, err := resolveEnvID(cmd)
	if err != nil {
		return err
	}

	domain := args[0]
	if err := validateDomainName(domain); err != nil {
		output.PrintError(output.CodeValidationError, err.Error(), map[string]any{
			"argument": "domain",
		})
		return err
	}

	autoTLS, _ := cmd.Flags().GetBool("auto-tls")

	body, err := json.Marshal(map[string]any{
		"domain":   map[string]any{"domain": domain},
		"auto_tls": autoTLS,
	})
	if err != nil {
		return err
	}

	path := fmt.Sprintf("/v1/environments/%s/domains", envID)

	if GetDryRun(cmd) {
		return output.FprintJSON(cmd.OutOrStdout(), map[string]any{
			"dry_run": true,
			"method":  "POST",
			"url":     c.BaseURL() + path,
			"body":    json.RawMessage(body),
		})
	}

	result, err := c.Do(cmd.Context(), "POST", path, nil, body)
	if err != nil {
		return handleAPIError(err)
	}
	return output.FprintProcess(cmd.OutOrStdout(), result, GetJQFlag(cmd), GetRawFlag(cmd))
}
