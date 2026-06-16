package cmd

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strconv"

	"github.com/customerio/cli/internal/client"
	"github.com/customerio/cli/internal/output"
	"github.com/spf13/cobra"
)

var domainsConfigureCmd = &cobra.Command{
	Use:   "configure <domain>",
	Short: "Launch guided DNS setup for a domain via Entri",
	Long: `Open the Entri DNS setup flow in a browser for the specified domain.

Entri automatically configures your DNS records with your DNS provider —
no manual record entry needed. For manual setup, add the DNS records
shown by 'cio domains verify' at your DNS provider, then re-run verify.

Use --cname to also configure link tracking (CNAME + TXT records) in
the same Entri flow. This calls enable_auto_tls on the domain to generate
the required DNS records. Defaults to email.<domain> if no value is given.

The domain argument can be either a domain name (e.g. example.com) or a
numeric domain ID. When a name is given, the CLI resolves it to an ID.

Examples:
  cio domains --env-id 456 configure example.com
  cio domains --env-id 456 configure example.com --cname email
  cio domains --env-id 456 configure example.com --cname track`,
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
	domainsConfigureCmd.Flags().String("cname", "", "Also configure link tracking; value is the subdomain (default: email)")
	domainsCmd.AddCommand(domainsConfigureCmd)
	domainsCmd.AddCommand(domainsVerifyCmd)
}

// --- Entri types ---

type entriTokenResponse struct {
	Token         string `json:"token"`
	ApplicationID string `json:"application_id"`
	UserID        string `json:"user_id"`
}

type entriDNSRecord struct {
	Type     string `json:"type"`
	Host     string `json:"host"`
	Value    string `json:"value"`
	TTL      int    `json:"ttl"`
	Priority *int   `json:"priority,omitempty"`
}

type entriConfig struct {
	ApplicationID string           `json:"applicationId"`
	Token         string           `json:"token"`
	UserID        string           `json:"userId"`
	PrefilledDom  string           `json:"prefilledDomain"`
	DNSRecords    []entriDNSRecord `json:"dnsRecords"`
	ValidateDMARC bool             `json:"validateDmarc"`
}

type domainGetResponse struct {
	Domain struct {
		ID              json.Number       `json:"id"`
		Domain          string            `json:"domain"`
		DNSRecords      []domainDNSRecord `json:"dns_records"`
		AutoTLSDomainID int               `json:"auto_tls_domain_id"`
	} `json:"domain"`
	AutoTLSDomain *struct {
		Config struct {
			DNSRecords []domainDNSRecord `json:"dns_records"`
		} `json:"config"`
	} `json:"auto_tls_domain"`
}

type domainDNSRecord struct {
	Domain string `json:"domain"`
	Type   string `json:"type"`
	Value  string `json:"value"`
	Name   string `json:"name"`
}

var mxPriorityRegex = regexp.MustCompile(`^(\d+)\s+(.+)$`)

const (
	defaultTTL        = 300
	defaultMXPriority = 10
)

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

	ctx := cmd.Context()
	cnameFlag, _ := cmd.Flags().GetString("cname")
	wantCNAME := cmd.Flags().Changed("cname")

	// Dry run: show what would happen without making mutating API calls.
	if GetDryRun(cmd) {
		dryRun := map[string]any{
			"dry_run":    true,
			"domain":     dom.Name,
			"domain_id":  dom.ID,
			"env_id":     envID,
			"will_fetch": []string{"GET domain details", "GET entri_token"},
		}
		if wantCNAME {
			sub := cnameFlag
			if sub == "" {
				sub = "email"
			}
			dryRun["will_mutate"] = []string{
				fmt.Sprintf("PUT domain cname=%s.%s", sub, dom.Name),
				"POST enable_auto_tls",
			}
		}
		return output.FprintJSON(cmd.OutOrStdout(), dryRun)
	}

	// If --cname, enable auto TLS to get link tracking DNS records.
	var linkTrackingRecords []domainDNSRecord
	if wantCNAME {
		records, err := ensureAutoTLS(cmd, c, envID, dom, cnameFlag)
		if err != nil {
			return err
		}
		linkTrackingRecords = records
	}

	// Get the full domain details (DNS records).
	domPath := fmt.Sprintf("/v1/environments/%s/domains/%s", envID, dom.ID)
	domRaw, err := c.Do(ctx, "GET", domPath, nil, nil)
	if err != nil {
		return handleAPIError(err)
	}

	var domResp domainGetResponse
	if err := json.Unmarshal(domRaw, &domResp); err != nil {
		output.PrintError(output.CodeGeneralError, "failed to parse domain response", nil)
		return err
	}

	// Get Entri token.
	tokenPath := fmt.Sprintf("/v1/environments/%s/domains/%s/entri_token?flow=domain_auth", envID, dom.ID)
	tokenRaw, err := c.Do(ctx, "GET", tokenPath, nil, nil)
	if err != nil {
		return handleAPIError(err)
	}

	var tokenResp entriTokenResponse
	if err := json.Unmarshal(tokenRaw, &tokenResp); err != nil {
		output.PrintError(output.CodeGeneralError, "failed to parse entri token response", nil)
		return err
	}

	// Map DNS records to Entri format.
	entriRecords := mapDNSRecordsToEntri(domResp.Domain.DNSRecords)

	// Append link tracking records (CNAME + TXT).
	for _, r := range linkTrackingRecords {
		entriRecords = append(entriRecords, entriDNSRecord{
			Type:  r.Type,
			Host:  r.Domain,
			Value: r.Value,
			TTL:   defaultTTL,
		})
	}

	// Build config and encode as base64url.
	cfg := entriConfig{
		ApplicationID: tokenResp.ApplicationID,
		Token:         tokenResp.Token,
		UserID:        tokenResp.UserID,
		PrefilledDom:  domResp.Domain.Domain,
		DNSRecords:    entriRecords,
		ValidateDMARC: true,
	}

	cfgJSON, err := json.Marshal(cfg)
	if err != nil {
		output.PrintError(output.CodeGeneralError, "failed to encode configuration", nil)
		return err
	}

	encoded := base64.StdEncoding.EncodeToString(cfgJSON)
	setupURL := fmt.Sprintf("%s/cli/dns-setup#%s", resolveUIBase(), encoded)

	// Human-readable output to stderr.
	fmt.Fprintf(cmd.ErrOrStderr(), "Open this URL to configure DNS for %s:\n\n    %s\n\n", dom.Name, setupURL)

	// Structured JSON to stdout for agents.
	return output.FprintJSON(cmd.OutOrStdout(), map[string]any{
		"url":       setupURL,
		"domain":    dom.Name,
		"domain_id": dom.ID,
	})
}

// ensureAutoTLS enables auto TLS on the domain (if not already enabled) and
// returns the link tracking DNS records.
func ensureAutoTLS(cmd *cobra.Command, c *client.Client, envID string, dom domainRef, cnameSubdomain string) ([]domainDNSRecord, error) {
	ctx := cmd.Context()

	// Set the cname subdomain before enabling auto TLS.
	if cnameSubdomain == "" {
		cnameSubdomain = "email"
	}
	if err := validateStringInput("cname subdomain", cnameSubdomain); err != nil {
		return nil, fmt.Errorf("invalid --cname value: %w", err)
	}
	cnameFQDN := cnameSubdomain + "." + dom.Name

	updateBody, _ := json.Marshal(map[string]any{
		"cname": cnameFQDN,
	})
	_, err := c.Do(ctx, "PUT", fmt.Sprintf("/v1/environments/%s/domains/%s", envID, dom.ID), nil, updateBody)
	if err != nil {
		return nil, handleAPIError(err)
	}

	// Enable auto TLS (idempotent — 422 if already enabled).
	_, err = c.Do(ctx, "POST", fmt.Sprintf("/v1/environments/%s/domains/%s/enable_auto_tls", envID, dom.ID), nil, nil)
	if err != nil {
		apiErr, ok := err.(*client.APIError)
		if !ok || apiErr.StatusCode != 422 {
			return nil, handleAPIError(err)
		}
	}

	// Fetch the domain to get auto TLS DNS records.
	domPath := fmt.Sprintf("/v1/environments/%s/domains/%s", envID, dom.ID)
	domRaw, err := c.Do(ctx, "GET", domPath, nil, nil)
	if err != nil {
		return nil, handleAPIError(err)
	}

	var resp domainGetResponse
	if err := json.Unmarshal(domRaw, &resp); err != nil {
		return nil, fmt.Errorf("failed to parse domain response: %w", err)
	}

	if resp.AutoTLSDomain == nil || len(resp.AutoTLSDomain.Config.DNSRecords) == 0 {
		return nil, fmt.Errorf("auto TLS enabled but no DNS records found; the records may not be generated yet")
	}

	return resp.AutoTLSDomain.Config.DNSRecords, nil
}

// mapDNSRecordsToEntri converts the API's DNS records to Entri format.
func mapDNSRecordsToEntri(records []domainDNSRecord) []entriDNSRecord {
	hasValidDMARC := false
	for _, r := range records {
		if r.Name == "DMARC" && r.Value != "" {
			hasValidDMARC = true
			break
		}
	}

	var filtered []domainDNSRecord
	for _, r := range records {
		if r.Name == "DMARC" && r.Value == "" {
			continue
		}
		filtered = append(filtered, r)
	}
	if !hasValidDMARC {
		filtered = append(filtered, domainDNSRecord{
			Name:   "DMARC",
			Type:   "TXT",
			Domain: "_dmarc",
			Value:  "v=DMARC1; p=none",
		})
	}

	validNames := map[string]bool{"Domain": true, "SPF": true, "DKIM": true, "DMARC": true}
	var result []entriDNSRecord
	for _, r := range filtered {
		if !validNames[r.Name] {
			continue
		}

		rec := entriDNSRecord{
			Type:  r.Type,
			Host:  r.Domain,
			Value: r.Value,
			TTL:   defaultTTL,
		}

		if r.Name == "Domain" {
			if m := mxPriorityRegex.FindStringSubmatch(r.Value); m != nil {
				p, _ := strconv.Atoi(m[1])
				rec.Priority = &p
				rec.Value = m[2]
			} else {
				p := defaultMXPriority
				rec.Priority = &p
			}
		}

		result = append(result, rec)
	}

	return result
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
