package cmd

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/customerio/cli/internal/output"
	"github.com/spf13/cobra"
)

var domainsConfigureCmd = &cobra.Command{
	Use:   "configure <domain>",
	Short: "Launch guided DNS setup for a domain via Entri",
	Long: `Open the Entri DNS setup flow in a browser for the specified domain.

Entri automatically configures your sending authentication DNS records (MX,
SPF, DKIM, DMARC) with your DNS provider — no manual record entry needed. For
manual setup, add the DNS records shown by 'cio domains verify' at your DNS
provider, then re-run verify.

To set up link tracking, use 'cio domains link_tracking configure'.

The domain argument can be either a domain name (e.g. example.com) or a
numeric domain ID. When a name is given, the CLI resolves it to an ID.

Examples:
  cio domains --env-id 456 configure example.com`,
	Args: requireArg("domain"),
	RunE: runDomainsConfigure,
}

var domainsVerifyCmd = &cobra.Command{
	Use:   "verify <domain>",
	Short: "Verify DNS records for a domain",
	Long: `Check DNS record verification status for a domain.

Returns per-record pass/fail status for domain authentication (MX, SPF,
DKIM, DMARC). For failing records, shows the expected value to configure
at your DNS provider.

The domain argument can be either a domain name (e.g. example.com) or a
numeric domain ID. When a name is given, the CLI resolves it to an ID.

Examples:
  cio domains --env-id 456 verify example.com`,
	Args: requireArg("domain"),
	RunE: runDomainsVerify,
}

func init() {
	domainsCmd.AddCommand(domainsConfigureCmd)
	domainsCmd.AddCommand(domainsVerifyCmd)
}

// --- DNS setup (Entri) ---

// dnsSetupLinkResponse is the response from the dns_setup_link endpoint: a
// short-lived handoff token the browser exchanges for the Entri config at the
// dns-setup page.
type dnsSetupLinkResponse struct {
	HandoffToken string `json:"handoff_token"`
	ExpiresIn    int    `json:"expires_in"`
}

func runDomainsConfigure(cmd *cobra.Command, args []string) error {
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

	// Dry run: show what would happen without making the API call.
	if GetDryRun(cmd) {
		return output.FprintJSON(cmd.OutOrStdout(), map[string]any{
			"dry_run":    true,
			"domain":     dom.Name,
			"domain_id":  dom.ID,
			"env_id":     envID,
			"will_fetch": []string{"POST dns_setup_link"},
		})
	}

	// Mint a short-lived DNS-setup handoff token. The browser exchanges it at
	// the dns-setup page, which resolves the domain's provisioned records
	// server-side — so the link stays short enough to survive a terminal and
	// the records reflect completed provisioning rather than whatever existed
	// when the link was minted.
	linkPath := fmt.Sprintf("/v1/environments/%s/domains/%s/dns_setup_link?flow=domain_auth", envID, dom.ID)
	raw, err := c.Do(cmd.Context(), "POST", linkPath, nil, nil)
	if err != nil {
		return handleAPIError(err)
	}

	var resp dnsSetupLinkResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		output.PrintError(output.CodeGeneralError, "failed to parse dns setup link response", nil)
		return err
	}

	setupURL := fmt.Sprintf("%s/cli/dns-setup#%s", resolveUIBase(c.BaseURL()), resp.HandoffToken)

	// Human-readable output to stderr.
	fmt.Fprintf(cmd.ErrOrStderr(), "Open this URL to configure DNS for %s:\n\n    %s\n\n", dom.Name, setupURL)

	// Structured JSON to stdout for agents.
	return output.FprintJSON(cmd.OutOrStdout(), map[string]any{
		"url":       setupURL,
		"domain":    dom.Name,
		"domain_id": dom.ID,
	})
}

// --- Verify command ---

// domainVerifyResponse matches the shape returned by
// POST /v1/environments/:env/domains/:id/verify. The backend pokes Mailgun
// to re-check DNS, persists the result, and returns the updated domain plus
// any soft verification errors/warnings.
type domainVerifyResponse struct {
	Domain struct {
		ID             json.Number `json:"id"`
		Domain         string      `json:"domain"`
		Verified       *bool       `json:"verified"`
		VerifiedSPF    *bool       `json:"verified_spf"`
		VerifiedDKIM   *bool       `json:"verified_dkim"`
		VerifiedDomain *bool       `json:"verified_domain"`
		VerifiedDMARC  *bool       `json:"verified_dmarc"`
	} `json:"domain"`
	Errors   map[string][]string `json:"errors,omitempty"`
	Warnings map[string][]string `json:"warnings,omitempty"`
}

func runDomainsVerify(cmd *cobra.Command, args []string) error {
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
		return handleAPIError(err)
	}

	if GetJQFlag(cmd) != "" {
		return output.FprintProcess(cmd.OutOrStdout(), result, GetJQFlag(cmd), GetRawFlag(cmd))
	}

	return printDomainVerifyResult(cmd, result, dom.Name)
}

// printDomainVerifyResult formats the /verify response: a human-readable
// summary on stderr and structured JSON on stdout.
func printDomainVerifyResult(cmd *cobra.Command, result json.RawMessage, domainArg string) error {
	var resp domainVerifyResponse
	if err := json.Unmarshal(result, &resp); err != nil {
		return output.FprintJSON(cmd.OutOrStdout(), json.RawMessage(result))
	}

	verified := resp.Domain.Verified != nil && *resp.Domain.Verified

	w := cmd.ErrOrStderr()
	if verified {
		fmt.Fprintf(w, "Domain %s: verified.\n", domainArg)
	} else {
		fmt.Fprintf(w, "Domain %s: NOT verified.\n\n", domainArg)
	}

	printCheck(w, "MX", resp.Domain.VerifiedDomain)
	printCheck(w, "SPF", resp.Domain.VerifiedSPF)
	printCheck(w, "DKIM", resp.Domain.VerifiedDKIM)
	printCheck(w, "DMARC", resp.Domain.VerifiedDMARC)

	if len(resp.Errors) > 0 {
		fmt.Fprintln(w)
		for key, msgs := range resp.Errors {
			for _, m := range msgs {
				fmt.Fprintf(w, "  %s: %s\n", key, m)
			}
		}
	}

	if len(resp.Warnings) > 0 {
		fmt.Fprintln(w)
		for key, msgs := range resp.Warnings {
			for _, m := range msgs {
				fmt.Fprintf(w, "  warning (%s): %s\n", key, m)
			}
		}
	}

	if !verified {
		fmt.Fprintln(w, "\nAdd the expected DNS records at your DNS provider, then re-run 'cio domains verify' to confirm.")
	}

	out := map[string]any{
		"verified": verified,
		"domain":   domainArg,
		"checks": map[string]any{
			"mx":    boolPtrValue(resp.Domain.VerifiedDomain),
			"spf":   boolPtrValue(resp.Domain.VerifiedSPF),
			"dkim":  boolPtrValue(resp.Domain.VerifiedDKIM),
			"dmarc": boolPtrValue(resp.Domain.VerifiedDMARC),
		},
	}
	if len(resp.Errors) > 0 {
		out["errors"] = resp.Errors
	}
	if len(resp.Warnings) > 0 {
		out["warnings"] = resp.Warnings
	}
	return output.FprintJSON(cmd.OutOrStdout(), out)
}

func printCheck(w io.Writer, label string, v *bool) {
	switch {
	case v == nil:
		fmt.Fprintf(w, "  %s: unknown\n", label)
	case *v:
		fmt.Fprintf(w, "  %s: passing\n", label)
	default:
		fmt.Fprintf(w, "  %s: FAILING\n", label)
	}
}

func boolPtrValue(v *bool) any {
	if v == nil {
		return nil
	}
	return *v
}

// --- DNS check helpers (used by link_tracking verify) ---

// --- Shared DNS check output helpers ---

type dnsCheckRecord struct {
	Name     string   `json:"name"`
	Passing  bool     `json:"passing"`
	Expected string   `json:"expected,omitempty"`
	Actual   string   `json:"actual,omitempty"`
	Errors   []string `json:"errors,omitempty"`
}

type dnsCheckGroup struct {
	Records []dnsCheckRecord `json:"records"`
}

type dnsCheckResponse struct {
	DomainAuth   *dnsCheckGroup `json:"domain_auth,omitempty"`
	LinkTracking *dnsCheckGroup `json:"link_tracking,omitempty"`
}

func printDNSCheckResult(cmd *cobra.Command, result json.RawMessage, domainArg, flow, rerunCmd string) error {
	var resp dnsCheckResponse
	if err := json.Unmarshal(result, &resp); err != nil {
		return output.FprintJSON(cmd.OutOrStdout(), json.RawMessage(result))
	}

	var group *dnsCheckGroup
	if flow == "domain_auth" {
		group = resp.DomainAuth
	} else {
		group = resp.LinkTracking
	}

	allPassing := true
	var failing []map[string]any

	if group != nil {
		for _, rec := range group.Records {
			if !rec.Passing {
				allPassing = false
				entry := map[string]any{
					"record":   rec.Name,
					"expected": rec.Expected,
				}
				if rec.Actual != "" {
					entry["actual"] = rec.Actual
				}
				if len(rec.Errors) > 0 {
					entry["errors"] = rec.Errors
				}
				failing = append(failing, entry)
			}
		}
	}

	w := cmd.ErrOrStderr()
	printDNSSummary(w, domainArg, flow, group, allPassing, len(failing), rerunCmd)
	fmt.Fprintln(w)

	if allPassing {
		return output.FprintJSON(cmd.OutOrStdout(), map[string]any{
			"verified": true,
			"domain":   domainArg,
			flow:       group,
		})
	}

	return output.FprintJSON(cmd.OutOrStdout(), map[string]any{
		"verified":        false,
		"domain":          domainArg,
		"failing_records": failing,
		flow:              group,
	})
}

func printDNSSummary(w io.Writer, domainArg, flow string, group *dnsCheckGroup, allPassing bool, failCount int, rerunCmd string) {
	label := "DNS"
	if flow == "link_tracking" {
		label = "link tracking DNS"
	}

	if allPassing {
		fmt.Fprintf(w, "Domain %s: all %s records verified.\n", domainArg, label)
		return
	}

	fmt.Fprintf(w, "Domain %s: %d %s record(s) failing.\n\n", domainArg, failCount, label)
	if group != nil {
		for _, rec := range group.Records {
			if rec.Passing {
				fmt.Fprintf(w, "  %s: passing\n", rec.Name)
			} else {
				fmt.Fprintf(w, "  %s: FAILING\n", rec.Name)
				for _, e := range rec.Errors {
					fmt.Fprintf(w, "    Error: %s\n", e)
				}
				fmt.Fprintf(w, "    Expected: %s\n", rec.Expected)
				if rec.Actual != "" {
					fmt.Fprintf(w, "    Actual:   %s\n", rec.Actual)
				}
			}
		}
	}
	fmt.Fprintf(w, "\nAdd the expected DNS records at your DNS provider, then re-run '%s' to confirm.\n", rerunCmd)
}
