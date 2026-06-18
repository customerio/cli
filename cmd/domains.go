package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/customerio/cli/internal/client"
	"github.com/customerio/cli/internal/output"
	"github.com/customerio/cli/internal/validate"
	"github.com/spf13/cobra"
)

var domainsCmd = &cobra.Command{
	Use:   "domains",
	Short: "Manage sending domains and from addresses",
	Long: `Manage sending domains, DNS authentication, and from addresses.

Sending domains are used to verify email sending identity through DNS records.
All subcommands require --env-id (the numeric environment/workspace ID).

Examples:
  cio domains --env-id 456 list
  cio domains --env-id 456 add example.com
  cio domains --env-id 456 configure example.com
  cio domains --env-id 456 verify example.com
  cio domains --env-id 456 from_addresses list
  cio domains --env-id 456 from_addresses add --name "Support" --email support@example.com`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return cmd.Help()
	},
}

func init() {
	domainsCmd.PersistentFlags().String("env-id", "", "Environment/workspace ID (required)")
	rootCmd.AddCommand(domainsCmd)
}

// resolveEnvID validates and returns the --env-id flag value.
func resolveEnvID(cmd *cobra.Command) (string, error) {
	envID, _ := cmd.Flags().GetString("env-id")
	if envID == "" {
		err := fmt.Errorf("--env-id is required")
		output.PrintError(output.CodeValidationError, err.Error(), map[string]any{
			"flag": "--env-id",
			"hint": "Pass the numeric environment/workspace ID.",
		})
		return "", err
	}
	if err := validate.ValidateResourceID(envID); err != nil {
		output.PrintError(output.CodeValidationError, err.Error(), map[string]any{
			"flag": "--env-id",
		})
		return "", err
	}
	return envID, nil
}

// validateStringInput rejects strings with control characters (including DEL),
// consistent with the input hardening principle.
func validateStringInput(field, value string) error {
	for i, r := range value {
		if r < 0x20 || r == 0x7F {
			return fmt.Errorf("%s contains control character at position %d", field, i)
		}
	}
	return nil
}

// validateDomainName rejects domain names with control characters or
// obviously invalid characters, consistent with the input hardening principle.
func validateDomainName(name string) error {
	if err := validateStringInput("domain name", name); err != nil {
		return err
	}
	for _, banned := range []rune{'?', '#', '%', '/', '\\', ' '} {
		if strings.ContainsRune(name, banned) {
			return fmt.Errorf("domain name contains invalid character '%c'", banned)
		}
	}
	return nil
}

// requireArg returns a cobra.PositionalArgs that requires exactly one argument
// with a contextual error message naming what's missing.
func requireArg(name string) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 {
			return fmt.Errorf("missing required %s argument; see 'cio %s --help'", name, cmd.CommandPath()[4:])
		}
		if len(args) > 1 {
			return fmt.Errorf("expected 1 %s argument, got %d; see 'cio %s --help'", name, len(args), cmd.CommandPath()[4:])
		}
		return nil
	}
}

// resolveUIBase returns the app (UI) base URL for building handoff links.
// It mirrors the cio auth login handoff: an explicit CIO_UI_URL override wins,
// otherwise derive the app origin from the configured API base by stripping a
// leading us./eu. region label (e.g. us.api.example.com -> api.example.com),
// falling back to the production app.
func resolveUIBase(apiBaseURL string) string {
	if envURL := os.Getenv("CIO_UI_URL"); envURL != "" {
		return strings.TrimRight(envURL, "/")
	}
	if origin := uiOriginFromAPIBase(apiBaseURL); origin != "" {
		return origin
	}
	return "https://fly.customer.io"
}

// domainRef holds the resolved numeric ID and domain name.
type domainRef struct {
	ID   string
	Name string
}

// resolveDomain looks up a domain by name or ID and returns both the numeric ID
// and the domain name. When a numeric ID is passed, the domain name is fetched
// from the API. When a name is passed, the ID is resolved via the API.
func resolveDomain(ctx context.Context, c *client.Client, envID, domainArg string) (domainRef, error) {
	path := fmt.Sprintf("/v1/environments/%s/domains", envID)
	result, err := c.Do(ctx, "GET", path, nil, nil)
	if err != nil {
		return domainRef{}, fmt.Errorf("failed to list domains for lookup: %w", err)
	}

	var resp struct {
		Domains []struct {
			ID     json.Number `json:"id"`
			Domain string      `json:"domain"`
		} `json:"domains"`
	}
	if err := json.Unmarshal(result, &resp); err != nil {
		return domainRef{}, fmt.Errorf("failed to parse domains response: %w", err)
	}

	isNumeric := validate.ValidateResourceID(domainArg) == nil

	for _, d := range resp.Domains {
		if isNumeric && d.ID.String() == domainArg {
			return domainRef{ID: d.ID.String(), Name: d.Domain}, nil
		}
		if !isNumeric && strings.EqualFold(d.Domain, domainArg) {
			return domainRef{ID: d.ID.String(), Name: d.Domain}, nil
		}
	}

	return domainRef{}, fmt.Errorf("domain %q not found in environment %s", domainArg, envID)
}
