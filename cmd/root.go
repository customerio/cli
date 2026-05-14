package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/customerio/cli/internal/client"
	"github.com/customerio/cli/internal/output"
	"github.com/customerio/cli/internal/useragent"
	"github.com/customerio/cli/internal/validate"
	"github.com/spf13/cobra"
)

// contextKey is an unexported type for context keys in this package.
type contextKey struct{ name string }

var clientKey = &contextKey{"client"}

func clientFromContext(ctx context.Context) *client.Client {
	c, _ := ctx.Value(clientKey).(*client.Client)
	return c
}

func contextWithClient(ctx context.Context, c *client.Client) context.Context {
	return context.WithValue(ctx, clientKey, c)
}

var rootCmd = &cobra.Command{
	Use:   "cio",
	Short: "Customer.io CLI",
	Long: `Agent-first CLI for Customer.io APIs.

Authenticates using service account tokens (sa_live_...). The CLI
exchanges them for short-lived JWTs via OAuth 2.0 client credentials.
All output is JSON.

Base URLs:
  US: https://us.fly.customer.io  (default)
  EU: https://eu.fly.customer.io

Use 'cio auth login' to open the browser-based login flow, or set CIO_TOKEN
for direct token-based usage.`,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		return cmd.Help()
	},
}

func init() {
	flags := rootCmd.PersistentFlags()

	flags.String("json", "", "Raw JSON request body or @filename to read from file")
	flags.String("params", "", "Query parameters as JSON, converted to query string for GET")
	flags.String("jq", "", "jq expression filter (via gojq)")
	flags.Bool("dry-run", false, "Validate and print request, don't execute")
	flags.Bool("read-only", false, "Request a read-only session (scope=read_only); only GET requests are permitted")
	flags.StringSlice("scope", nil, "Additional OAuth scope(s) to request during token exchange")
	flags.String("api-url", "", "API base URL (default: derived from region)")
	flags.String("token", "", "Service account token (overrides stored credentials and CIO_TOKEN)")
	flags.Duration("timeout", client.DefaultTimeout, "HTTP request timeout")
	flags.Int("page", 0, "Page number")
	flags.Int("limit", 0, "Page size")
	flags.Bool("page-all", false, "Auto-paginate, emit NDJSON")

	rootCmd.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		// Bind environment variables as fallback defaults.
		if !cmd.Flags().Changed("timeout") {
			if v := os.Getenv("CIO_TIMEOUT"); v != "" {
				if d, err := time.ParseDuration(v); err == nil {
					_ = cmd.Flags().Set("timeout", d.String())
				}
			}
		}

		// Validate --params if provided.
		paramsFlag, _ := cmd.Flags().GetString("params")
		if paramsFlag != "" {
			if _, err := validate.ValidateParams(paramsFlag); err != nil {
				output.PrintError(output.CodeValidationError, err.Error(), map[string]string{
					"flag": "--params",
				})
				return err
			}
		}

		// Resolve and validate --json if provided.
		jsonFlag, _ := cmd.Flags().GetString("json")
		if jsonFlag != "" {
			resolved, err := resolveJSONFlag(jsonFlag)
			if err != nil {
				output.PrintError(output.CodeValidationError, err.Error(), map[string]string{
					"flag": "--json",
				})
				return err
			}
			// Store the resolved value back so downstream code sees the file contents.
			_ = cmd.Flags().Set("json", resolved)
			if _, err := validate.ValidateJSONPayload(resolved); err != nil {
				valErr := err
				if strings.HasPrefix(jsonFlag, "@") {
					valErr = fmt.Errorf("%s (from file %s)", err.Error(), jsonFlag[1:])
				}
				output.PrintError(output.CodeValidationError, valErr.Error(), map[string]string{
					"flag": "--json",
				})
				return valErr
			}
		}

		// Skip client/token init for auth commands that operate without credentials.
		if isAuthCommand(cmd) {
			return nil
		}

		// Resolve token: explicit flag > env var > config file.
		tokenFlag, _ := cmd.Flags().GetString("token")
		saToken := client.ResolveServiceAccountToken(tokenFlag)

		// Resolve base URL: explicit flag > env var > region.
		apiURL, _ := cmd.Flags().GetString("api-url")
		if apiURL == "" {
			if envURL := os.Getenv("CIO_API_URL"); envURL != "" {
				apiURL = envURL
			} else {
				region := client.ResolveRegion(apiURL, cmd.Flags().Changed("api-url"))
				apiURL = client.BaseURLForRegion(region)
			}
		}

		timeout, _ := cmd.Flags().GetDuration("timeout")
		readOnly, _ := cmd.Flags().GetBool("read-only")
		scopes, _ := cmd.Flags().GetStringSlice("scope")

		c := client.New(client.Config{
			BaseURL:             apiURL,
			ServiceAccountToken: saToken,
			AccessToken:         client.ResolveAccessToken(),
			ReadOnly:            readOnly,
			Scopes:              scopes,
			Timeout:             timeout,
		})

		ctx := cmd.Context()
		if ctx == nil {
			ctx = context.Background()
		}
		cmd.SetContext(contextWithClient(ctx, c))

		return nil
	}
}

// isAuthCommand returns true for subcommands that don't need the UI API client.
func isAuthCommand(cmd *cobra.Command) bool {
	switch cmd.CommandPath() {
	case "cio auth login",
		"cio auth logout",
		"cio auth signup",
		"cio auth signup start",
		"cio auth signup verify":
		return true
	}
	// Track API send commands authenticate directly with the sa_live_ token
	// and don't need the UI API OAuth exchange — unless --watch is set, which
	// polls the REST API for delivery status.
	if isTrackSendCommand(cmd) {
		watch, _ := cmd.Flags().GetBool("watch")
		return !watch
	}
	return false
}

// clientFromCmd extracts the API client from the command's context.
func clientFromCmd(cmd *cobra.Command) *client.Client {
	return clientFromContext(cmd.Context())
}

// GetJQFlag returns the --jq flag value.
func GetJQFlag(cmd *cobra.Command) string {
	jq, _ := cmd.Flags().GetString("jq")
	return jq
}

// GetPaginationFlags returns the --page, --limit, and --page-all flag values.
func GetPaginationFlags(cmd *cobra.Command) (page, limit int, pageAll bool) {
	page, _ = cmd.Flags().GetInt("page")
	limit, _ = cmd.Flags().GetInt("limit")
	pageAll, _ = cmd.Flags().GetBool("page-all")
	return
}

// GetDryRun returns whether --dry-run was specified.
func GetDryRun(cmd *cobra.Command) bool {
	v, _ := cmd.Flags().GetBool("dry-run")
	return v
}

// GetParams parses and returns validated --params.
func GetParams(cmd *cobra.Command) (map[string]string, error) {
	raw, _ := cmd.Flags().GetString("params")
	if raw == "" {
		return nil, nil
	}
	return validate.ValidateParams(raw)
}

// GetJSONBody returns the validated --json payload.
func GetJSONBody(cmd *cobra.Command) ([]byte, error) {
	raw, _ := cmd.Flags().GetString("json")
	if raw == "" {
		return nil, nil
	}
	return validate.ValidateJSONPayload(raw)
}

// SetVersion sets the CLI version string (called from main with ldflags value).
func SetVersion(v string) {
	if v != "" {
		useragent.SetVersion(v)
		rootCmd.Version = v
	}
}

// resolveJSONFlag reads the file if value starts with "@", otherwise returns as-is.
func resolveJSONFlag(value string) (string, error) {
	if !strings.HasPrefix(value, "@") {
		return value, nil
	}
	path := value[1:]
	if path == "" {
		return "", fmt.Errorf("--json @filename: missing filename after @")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("--json @%s: %w", path, err)
	}
	return string(data), nil
}

// Execute runs the root command.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		exitCode := output.PrintError(output.CodeGeneralError, err.Error(), nil)
		os.Exit(exitCode)
	}
}
