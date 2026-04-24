package cmd

import (
	"fmt"

	"github.com/customerio/cli/internal/output"
	"github.com/spf13/cobra"
)

var domainsDeleteCmd = &cobra.Command{
	Use:   "delete <domain>",
	Short: "Delete a sending domain",
	Long: `Delete a sending domain.

DELETE /v1/environments/:env_id/domains/:domain_id

The <domain> argument can be either a domain name (e.g. example.com) or a
numeric domain ID. When a name is given, the CLI resolves it to an ID.

Returns 204 on success.

Examples:
  cio domains --env-id 456 delete example.com
  cio domains --env-id 456 delete example.com --dry-run`,
	Args: requireArg("domain"),
	RunE: runDomainsDelete,
}

func init() {
	domainsCmd.AddCommand(domainsDeleteCmd)
}

func runDomainsDelete(cmd *cobra.Command, args []string) error {
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
			"method":  "DELETE",
			"url":     c.BaseURL() + path,
		})
	}

	_, err = c.Do(cmd.Context(), "DELETE", path, nil, nil)
	if err != nil {
		return handleAPIError(err)
	}
	return output.FprintJSON(cmd.OutOrStdout(), map[string]any{
		"deleted":   true,
		"domain_id": dom.ID,
	})
}
