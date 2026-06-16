package cmd

import (
	"fmt"

	"github.com/customerio/cli/internal/output"
	"github.com/spf13/cobra"
)

var domainsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List sending domains",
	Long: `List all sending domains for an environment.

GET /v1/environments/:env_id/domains

Returns domains and auto_tls_domains arrays.

Example:
  cio domains --env-id 456 list
  cio domains --env-id 456 list --jq '.domains[]'`,
	Args: cobra.NoArgs,
	RunE: runDomainsList,
}

func init() {
	domainsCmd.AddCommand(domainsListCmd)
}

func runDomainsList(cmd *cobra.Command, args []string) error {
	c := clientFromCmd(cmd)
	if c == nil {
		return errNoClient(cmd)
	}

	envID, err := resolveEnvID(cmd)
	if err != nil {
		return err
	}

	path := fmt.Sprintf("/v1/environments/%s/domains", envID)

	if GetDryRun(cmd) {
		return output.FprintJSON(cmd.OutOrStdout(), map[string]any{
			"dry_run": true,
			"method":  "GET",
			"url":     c.BaseURL() + path,
		})
	}

	result, err := c.Do(cmd.Context(), "GET", path, nil, nil)
	if err != nil {
		return handleAPIError(err)
	}
	return output.FprintProcess(cmd.OutOrStdout(), result, GetJQFlag(cmd), GetRawFlag(cmd))
}
