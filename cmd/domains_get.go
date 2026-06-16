package cmd

import (
	"fmt"

	"github.com/customerio/cli/internal/output"
	"github.com/spf13/cobra"
)

var domainsGetCmd = &cobra.Command{
	Use:   "get <domain>",
	Short: "Get a sending domain",
	Long: `Get details for a sending domain.

GET /v1/environments/:env_id/domains/:domain_id

The domain argument can be either a domain name (e.g. example.com) or a
numeric domain ID. When a name is given, the CLI resolves it to an ID.

Examples:
  cio domains --env-id 456 get example.com`,
	Args: requireArg("domain"),
	RunE: runDomainsGet,
}

func init() {
	domainsCmd.AddCommand(domainsGetCmd)
}

func runDomainsGet(cmd *cobra.Command, args []string) error {
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

	path := fmt.Sprintf("/v1/environments/%s/domains/%s", envID, dom.ID)

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
