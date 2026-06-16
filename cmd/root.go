package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/customerio/cli/internal/client"
	"github.com/customerio/cli/internal/output"
	"github.com/customerio/cli/internal/tui"
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
	defaultHelp := rootCmd.HelpFunc()
	rootCmd.SetHelpFunc(func(cmd *cobra.Command, args []string) {
		if cmd == rootCmd {
			tui.RenderHelp(cmd.OutOrStdout(), topLevelCommands(rootCmd))
			return
		}
		defaultHelp(cmd, args)
	})

	flags := rootCmd.PersistentFlags()

	flags.String("json", "", "Raw JSON request body, @filename to read from a file, or - to read from stdin")
	flags.String("params", "", "Query parameters as JSON, converted to query string for GET")
	flags.String("jq", "", "jq expression filter (via gojq)")
	flags.BoolP("raw-output", "r", false, "Print string results unquoted, like jq -r (no external jq needed)")
	flags.StringArray("arg", nil, "Bind a string variable for --json's jq program: --arg name=value (repeatable). Makes --json a jq -n program — no external jq needed")
	flags.StringArray("argjson", nil, "Bind a JSON variable for --json's jq program: --argjson name=<json> (repeatable)")
	flags.StringArray("rawfile", nil, "Bind a file's contents as a string variable for --json: --rawfile name=path (repeatable)")
	flags.StringArray("slurpfile", nil, "Bind a file's JSON contents as a variable for --json: --slurpfile name=path (repeatable)")
	flags.Bool("dry-run", false, "Validate and print request, don't execute")
	flags.Bool("read-only", false, "Request a read-only session (scope=read_only); only GET requests are permitted")
	flags.StringSlice("scope", nil, "Additional OAuth scope(s) to request during token exchange")
	flags.String("api-url", "", "API base URL (default: derived from region)")
	flags.String("token", "", "Service account token (overrides stored credentials and CIO_TOKEN)")
	flags.String("profile", "", "Configuration profile to use (overrides CIO_PROFILE; default: current profile)")
	flags.Duration("timeout", client.DefaultTimeout, "HTTP request timeout")
	flags.Int("page", 0, "Page number")
	flags.Int("limit", 0, "Page size")
	flags.Bool("page-all", false, "Auto-paginate, emit NDJSON")

	rootCmd.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		// Select the active profile before any credential access so that auth
		// commands and credential lookups all resolve against the same profile.
		if profile, _ := cmd.Flags().GetString("profile"); profile != "" {
			if err := client.ValidateProfileName(profile); err != nil {
				output.PrintError(output.CodeValidationError, err.Error(), map[string]string{
					"flag": "--profile",
				})
				return err
			}
			client.SetActiveProfile(profile)
		}

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
			resolved, err := resolveJSONFlag(jsonFlag, cmd.InOrStdin())
			if err != nil {
				output.PrintError(output.CodeValidationError, err.Error(), map[string]string{
					"flag": "--json",
				})
				return err
			}
			// When --arg/--argjson are present, --json is a jq program (jq -n
			// style): evaluate it with those bindings, via the bundled gojq, to
			// build the body. Lets callers embed quoted/Liquid/multi-line values
			// without external jq or shell escaping.
			argVals, _ := cmd.Flags().GetStringArray("arg")
			argjsonVals, _ := cmd.Flags().GetStringArray("argjson")

			// --rawfile name=path binds a file's contents as a string variable;
			// --slurpfile binds its JSON contents. Resolve them into the same
			// bindings BuildJSON already understands.
			rawfiles, _ := cmd.Flags().GetStringArray("rawfile")
			slurpfiles, _ := cmd.Flags().GetStringArray("slurpfile")
			for _, spec := range rawfiles {
				binding, ferr := fileBinding("rawfile", spec)
				if ferr != nil {
					output.PrintError(output.CodeValidationError, ferr.Error(), map[string]string{"flag": "--rawfile"})
					return ferr
				}
				argVals = append(argVals, binding)
			}
			for _, spec := range slurpfiles {
				binding, ferr := fileBinding("slurpfile", spec)
				if ferr != nil {
					output.PrintError(output.CodeValidationError, ferr.Error(), map[string]string{"flag": "--slurpfile"})
					return ferr
				}
				argjsonVals = append(argjsonVals, binding)
			}

			if len(argVals) > 0 || len(argjsonVals) > 0 {
				built, buildErr := output.BuildJSON(resolved, argVals, argjsonVals)
				if buildErr != nil {
					output.PrintError(output.CodeValidationError, buildErr.Error(), map[string]string{
						"flag": "--json",
					})
					return buildErr
				}
				resolved = string(built)
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

		// Resolve base URL: explicit flag > env var > profile URL > region.
		apiURLFlag, _ := cmd.Flags().GetString("api-url")
		apiURL := client.ResolveBaseURL(apiURLFlag, cmd.Flags().Changed("api-url"))

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

		// Automatic sandbox→live self-heal: if we're on a sandbox token and the
		// account has gone live, promote it transparently before the command
		// runs. Best-effort and throttled; never blocks the command.
		maybePromoteSandboxToken(ctx, c, saToken, readOnly)

		return nil
	}
}

// topLevelCommands returns the live set of user-facing top-level commands for
// the branded help screen, sorted by name. Cobra's built-in help and
// completion commands are omitted, as are hidden/unavailable ones.
func topLevelCommands(root *cobra.Command) []tui.Command {
	var out []tui.Command
	for _, c := range root.Commands() {
		if !c.IsAvailableCommand() || c.Name() == "help" || c.Name() == "completion" {
			continue
		}
		out = append(out, tui.Command{Name: c.Name(), Desc: c.Short})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// isAuthCommand returns true for subcommands that don't need the UI API client.
func isAuthCommand(cmd *cobra.Command) bool {
	switch cmd.CommandPath() {
	case "cio auth login",
		"cio auth logout",
		"cio auth signup",
		"cio auth signup start",
		"cio auth signup verify",
		"cio profile",
		"cio profile list",
		"cio profile use",
		"cio profile remove":
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

// GetRawFlag returns the --raw-output flag value.
func GetRawFlag(cmd *cobra.Command) bool {
	raw, _ := cmd.Flags().GetBool("raw-output")
	return raw
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

// resolveJSONFlag reads stdin if value is exactly "-", a file if it starts with
// "@", otherwise returns the value as-is.
func resolveJSONFlag(value string, stdin io.Reader) (string, error) {
	if value == "-" {
		data, err := io.ReadAll(stdin)
		if err != nil {
			return "", fmt.Errorf("--json -: %w", err)
		}
		return string(data), nil
	}
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

// fileBinding resolves a "name=path" spec for --rawfile / --slurpfile into the
// "name=value" binding BuildJSON consumes, reading the file's contents as the
// value. The value's own "=" or newlines are preserved (BuildJSON splits on the
// first "=" only).
func fileBinding(flag, spec string) (string, error) {
	name, path, found := strings.Cut(spec, "=")
	if !found || name == "" {
		return "", fmt.Errorf("--%s expects name=path, got %q", flag, spec)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("--%s %s: %w", flag, name, err)
	}
	return name + "=" + string(data), nil
}

// Execute runs the root command.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		exitCode := output.PrintError(output.CodeGeneralError, err.Error(), nil)
		os.Exit(exitCode)
	}
}
