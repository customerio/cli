package cmd

import (
	"encoding/json"
	"fmt"

	"github.com/customerio/cli/internal/output"
	"github.com/spf13/cobra"
)

var domainsPostmasterToolsCmd = &cobra.Command{
	Use:   "postmaster_tools",
	Short: "Set up and verify a domain's Postmaster Tools record",
	Long: `Publish and verify the Postmaster Tools record for a domain.

Postmaster Tools is Google's report on how Gmail treats your mail. Once the
workspace has connected a Google account, each sending domain's registrable
root (example.com for mail.example.com) needs a TXT record that proves you own
it.

Subcommands:
  configure   Launch guided DNS setup for the record via Entri
  verify      Check the record with Google`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return cmd.Help()
	},
}

var domainsPostmasterToolsConfigureCmd = &cobra.Command{
	Use:   "configure <domain>",
	Short: "Launch guided DNS setup for the Postmaster Tools record via Entri",
	Long: `Open the Entri DNS setup flow in a browser to publish the domain's
Postmaster Tools TXT record at its registrable root, without touching its other
DNS records. Use it for a domain whose authentication is already set up.

POST /v1/environments/:env_id/domains/:domain_id/dns_setup_link?flow=postmaster_tools

The domain must have a pending Postmaster Tools record: the workspace has
connected a Google account and the domain's root is added in Postmaster Tools.

Examples:
  cio domains --env-id 456 postmaster_tools configure mail.example.com`,
	Args: requireArg("domain"),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runDNSSetupLink(cmd, args, "postmaster_tools")
	},
}

var domainsPostmasterToolsVerifyCmd = &cobra.Command{
	Use:   "verify <domain>",
	Short: "Check the Postmaster Tools record with Google",
	Long: `Ask Google to verify the domain's Postmaster Tools record and report its status.

POST /v1/environments/:env_id/domains/:domain_id/verify

Returns the registrable root, its status (verified, pending, not_added or
not_connected) and, while pending, the TXT record to publish.

Examples:
  cio domains --env-id 456 postmaster_tools verify mail.example.com`,
	Args: requireArg("domain"),
	RunE: runPostmasterToolsVerify,
}

func init() {
	domainsPostmasterToolsCmd.AddCommand(domainsPostmasterToolsConfigureCmd)
	domainsPostmasterToolsCmd.AddCommand(domainsPostmasterToolsVerifyCmd)
	domainsCmd.AddCommand(domainsPostmasterToolsCmd)
}

// postmasterToolsVerifyResponse is the part of the domain verify response this
// command reports; postmaster is absent where Postmaster Tools isn't set up.
type postmasterToolsVerifyResponse struct {
	Postmaster *struct {
		Root   string `json:"root"`
		Status string `json:"status"`
		Record *struct {
			Domain string `json:"domain"`
			Type   string `json:"type"`
			Value  string `json:"value"`
		} `json:"record,omitempty"`
	} `json:"postmaster"`
}

func runPostmasterToolsVerify(cmd *cobra.Command, args []string) error {
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

	path := fmt.Sprintf("/v1/environments/%s/domains/%s/verify", envID, dom.ID)

	if GetDryRun(cmd) {
		return output.FprintJSON(cmd.OutOrStdout(), map[string]any{
			"dry_run": true,
			"method":  "POST",
			"url":     c.BaseURL() + path,
		})
	}

	result, err := c.Do(cmd.Context(), "POST", path, nil, nil)
	if err != nil {
		return handleWriteError(cmd, "POST", err)
	}

	if GetJQFlag(cmd) != "" {
		return printWriteResult(cmd, "POST", result, GetJQFlag(cmd))
	}

	var resp postmasterToolsVerifyResponse
	if err := json.Unmarshal(result, &resp); err != nil {
		return output.FprintJSON(cmd.OutOrStdout(), json.RawMessage(result))
	}

	w := cmd.ErrOrStderr()
	out := map[string]any{"domain": dom.Name, "verified": false}
	setup := resp.Postmaster
	if setup == nil {
		fmt.Fprintf(w, "Postmaster Tools isn't set up for %s's workspace.\n", dom.Name)
		out["status"] = "unavailable"
		return output.FprintJSON(cmd.OutOrStdout(), out)
	}
	out["root"] = setup.Root
	out["status"] = setup.Status
	switch setup.Status {
	case "verified":
		out["verified"] = true
		fmt.Fprintf(w, "Postmaster Tools record for %s: verified.\n", setup.Root)
	case "pending":
		fmt.Fprintf(w, "Postmaster Tools record for %s: not verified yet.\n", setup.Root)
		if setup.Record != nil {
			out["record"] = setup.Record
			fmt.Fprintf(w, "\n  %s %s\n    %s\n", setup.Record.Type, setup.Record.Domain, setup.Record.Value)
		}
		fmt.Fprintf(w, "\nPublish the record, or run 'cio domains postmaster_tools configure %s', then re-run verify.\n", dom.Name)
	case "not_added":
		fmt.Fprintf(w, "%s isn't in Postmaster Tools yet. Add it in Customer.io to get its record.\n", setup.Root)
	case "not_connected":
		fmt.Fprintln(w, "The workspace hasn't connected a Google account for Postmaster Tools yet.")
	default:
		fmt.Fprintf(w, "Postmaster Tools record for %s: %s.\n", setup.Root, setup.Status)
	}
	return output.FprintJSON(cmd.OutOrStdout(), out)
}
