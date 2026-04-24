package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/customerio/cli/internal/client"
	"github.com/customerio/cli/internal/output"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var isTerminalInput = func(fd uintptr) bool {
	return term.IsTerminal(int(fd))
}

var readPasswordInput = func(fd uintptr) ([]byte, error) {
	return term.ReadPassword(int(fd))
}

var authCmd = &cobra.Command{
	Use:   "auth",
	Short: "Authenticate cio CLI with the Customer.io API",
	Long: `Manage authentication for the Journeys CLI.

The CLI uses service account tokens (sa_live_...) for authentication.
On login, the CLI exchanges the token for a short-lived JWT via OAuth 2.0
client credentials grant and caches it.

Credentials are stored in ~/.cio/config.json with 0600 permissions.

Alternatively, set the CIO_TOKEN environment variable or pass
--token on any command.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return cmd.Help()
	},
}

// ---------------------------------------------------------------------------
// auth login
// ---------------------------------------------------------------------------

var authLoginCmd = &cobra.Command{
	Use:   "login",
	Short: "Authenticate with a Customer.io service account token",
	Long: `Authenticate the cio CLI by minting a token through the web UI.

Default flow: the CLI prints a URL, you open it in any browser, log in
with email/password/SSO/2FA as normal, and the page mints a token scoped
to the account you're currently viewing. Copy the token, paste it back
at the prompt, and the CLI stores it in ~/.cio/config.json.

For CI / automation the existing token-paste flow is unchanged:
  $ echo "sa_live_abc123..." | cio auth login --with-token
  $ cio auth login sa_live_abc123...

The token (sa_live_...) is stored in ~/.cio/config.json with 0600
permissions. The CLI exchanges it for a short-lived JWT via the
OAuth 2.0 client credentials grant at /v1/service_accounts/oauth/token,
then auto-discovers your data center (US or EU) from the account.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		withToken, _ := cmd.Flags().GetBool("with-token")

		var (
			token string
			err   error
		)

		switch {
		case withToken:
			scanner := bufio.NewScanner(os.Stdin)
			if scanner.Scan() {
				token = strings.TrimSpace(scanner.Text())
			}
			if err := scanner.Err(); err != nil {
				err = fmt.Errorf("failed to read token from stdin: %w", err)
				output.PrintError(output.CodeGeneralError, err.Error(), nil)
				return err
			}
		case len(args) == 1:
			token = strings.TrimSpace(args[0])
		default:
			// Print the URL rather than shelling out to a browser — works
			// under SSH, headless CI, and restrictive sandboxes.
			loginURL := resolveCLILoginURL()
			fmt.Fprintf(cmd.ErrOrStderr(), "Open this URL in a browser to create a CLI token:\n\n    %s\n\n", loginURL)
			fmt.Fprint(cmd.ErrOrStderr(), "After logging in, copy the token shown and paste it here.\n")

			token, err = readInteractiveToken(cmd.InOrStdin(), cmd.ErrOrStderr())
			if err != nil {
				output.PrintError(output.CodeGeneralError, err.Error(), nil)
				return err
			}
		}

		if token == "" {
			err := fmt.Errorf("token must not be empty")
			output.PrintError(output.CodeValidationError, err.Error(), nil)
			return err
		}

		if !client.IsServiceAccountToken(token) {
			err := fmt.Errorf("token must start with %q — got %q", client.ServiceAccountTokenPrefix, token[:min(len(token), 10)])
			output.PrintError(output.CodeValidationError, err.Error(), map[string]any{
				"hint": "Service account tokens start with sa_live_. Create one in the Customer.io UI under Settings > Service Accounts.",
			})
			return err
		}

		// Discover region: exchange token through the default endpoint, then
		// call GET /v1/accounts/current to read data_center.
		result, err := client.DiscoverRegion(cmd.Context(), token, resolveLoginAPIURL(cmd))
		if err != nil {
			// Don't persist on failure — a rejected token would pollute
			// ~/.cio/config.json and break every subsequent command.
			output.PrintError(output.CodeAuthError, fmt.Sprintf("Authentication failed: %s", err.Error()), map[string]any{
				"hint": "Check that your token is valid, then re-run 'cio auth login'.",
			})
			return err
		}

		creds := &client.Credentials{
			ServiceAccountToken:  token,
			AccountID:            result.AccountID,
			Region:               result.Region,
			AccessToken:          result.AccessToken,
			AccessTokenExpiresAt: time.Now().Add(time.Duration(result.ExpiresIn) * time.Second),
		}

		if err := client.WriteCredentials(creds); err != nil {
			output.PrintError(output.CodeGeneralError, err.Error(), nil)
			return err
		}

		return output.FprintJSON(cmd.OutOrStdout(), map[string]any{
			"status":      "ok",
			"message":     "Authenticated successfully. Credentials saved to ~/.cio/config.json",
			"account_id":  result.AccountID,
			"region":      result.Region,
			"base_url":    result.BaseURL,
			"token":       client.MaskToken(token),
			"data_center": result.Region,
		})
	},
}

// ---------------------------------------------------------------------------
// auth logout
// ---------------------------------------------------------------------------

var authLogoutCmd = &cobra.Command{
	Use:   "logout",
	Short: "Remove stored authentication credentials",
	Long:  "Delete the stored token and cached JWT from ~/.cio/config.json.",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := client.DeleteCredentials(); err != nil {
			output.PrintError(output.CodeGeneralError, err.Error(), nil)
			return err
		}

		return output.FprintJSON(cmd.OutOrStdout(), map[string]any{
			"status":  "ok",
			"message": "Credentials removed from ~/.cio/config.json",
		})
	},
}

// ---------------------------------------------------------------------------
// auth status
// ---------------------------------------------------------------------------

var authStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Display active authentication state",
	Long: `Check authentication status by showing the stored/active token source
and verifying it against the API.

Token resolution order:
  1. --token flag
  2. CIO_TOKEN environment variable
  3. ~/.cio/config.json`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		tokenFlag, _ := cmd.Flags().GetString("token")

		// Determine token source.
		var tokenSource string
		var token string

		switch {
		case tokenFlag != "":
			tokenSource = "flag"
			token = tokenFlag
		case os.Getenv("CIO_TOKEN") != "":
			tokenSource = "environment"
			token = os.Getenv("CIO_TOKEN")
		default:
			tokenSource = "config_file"
			if creds, err := client.ReadCredentials(); err == nil {
				token = creds.ServiceAccountToken
			}
		}

		if token == "" {
			err := fmt.Errorf("not authenticated. Run 'cio auth login' or set CIO_TOKEN")
			output.PrintError(output.CodeAuthError, err.Error(), map[string]any{
				"token_source": "none",
			})
			return err
		}

		statusResult := map[string]any{
			"status":       "authenticated",
			"token_source": tokenSource,
			"token":        client.MaskToken(token),
		}

		// Show stored region/URL and account ID.
		if creds, err := client.ReadCredentials(); err == nil {
			if creds.Region != "" {
				statusResult["region"] = creds.Region
			}
			if creds.AccountID != "" {
				statusResult["account_id"] = creds.AccountID
			}
		}

		// Verify by exchanging the token.
		c := clientFromCmd(cmd)
		if c != nil {
			statusResult["base_url"] = c.BaseURL()
			statusResult["read_only"] = c.ReadOnly()
			_, err := c.EnsureAccessToken(cmd.Context())
			if err != nil {
				statusResult["verified"] = false
				statusResult["verify_error"] = err.Error()
			} else {
				statusResult["verified"] = true
			}
		}

		jq := GetJQFlag(cmd)
		data, _ := json.Marshal(statusResult)
		return output.FprintProcess(cmd.OutOrStdout(), json.RawMessage(data), jq)
	},
}

// ---------------------------------------------------------------------------
// auth token
// ---------------------------------------------------------------------------

var authTokenCmd = &cobra.Command{
	Use:   "token",
	Short: "Print the active service account token",
	Long: `Print the sa_live_ token that cio CLI is currently configured to use.

This is useful for debugging token resolution. The token is printed to
stdout with no formatting.

Token resolution order:
  1. --token flag
  2. CIO_TOKEN environment variable
  3. ~/.cio/config.json`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		tokenFlag, _ := cmd.Flags().GetString("token")
		token := client.ResolveServiceAccountToken(tokenFlag)

		if token == "" {
			err := fmt.Errorf("no token found. Run 'cio auth login' or set CIO_TOKEN")
			output.PrintError(output.CodeAuthError, err.Error(), nil)
			return err
		}

		// Print raw token to stdout (no JSON wrapping, like gh auth token).
		fmt.Fprintln(cmd.OutOrStdout(), token)
		return nil
	},
}

// ---------------------------------------------------------------------------
// auth signup
// ---------------------------------------------------------------------------

var authSignupCmd = &cobra.Command{
	Use:   "signup",
	Short: "Provision a new Customer.io account (unauthenticated agentic flow)",
	Long: `Two-step unauthenticated signup flow for agents.

Step 1 — 'signup start' emails a 6-digit verification code.
Step 2 — 'signup verify' consumes the code, creates the account, and returns
an Admin-scoped sa_live_ bootstrap token shown ONCE.

Both subcommands honor --api-url (defaults to https://us.fly.customer.io).
They require no credentials; --token / CIO_TOKEN are ignored.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return cmd.Help()
	},
}

var authSignupStartCmd = &cobra.Command{
	Use:   "start",
	Short: "Email a 6-digit verification code to the given address",
	Long: `POST /v1/account_signup.

Supply the request body via --json:
  cio auth signup start --json '{"email":"agent+demo@example.com"}'

A 200 response ("check your email") is not proof a code was sent — if one
doesn't arrive within a few minutes, try a different email.`,
	Args: cobra.NoArgs,
	RunE: runAuthSignupStart,
}

var authSignupVerifyCmd = &cobra.Command{
	Use:   "verify",
	Short: "Verify the code, create the account, and return the bootstrap sa_live_ token",
	Long: `POST /v1/account_signup/code.

Supply the request body via --json:
  cio auth signup verify --json '{
    "email": "agent+demo@example.com",
    "code": "123456",
    "company_name": "Acme",
    "first_name": "Ada",
    "last_name": "Lovelace",
    "data_center": "us"
  }'

The returned 'token' is shown ONCE and the server will not return it again.
On success, verify automatically writes the bootstrap token + account_id to
~/.cio/config.json, so the next 'cio api ...' call is already authenticated.

If persistence fails (rare), capture the 'token' field from stdout and run:
  echo "<token>" | cio auth login --with-token`,
	Args: cobra.NoArgs,
	RunE: runAuthSignupVerify,
}

func runAuthSignupStart(cmd *cobra.Command, args []string) error {
	return runSignupRequest(cmd, "/v1/account_signup")
}

func runAuthSignupVerify(cmd *cobra.Command, args []string) error {
	body, err := GetJSONBody(cmd)
	if err != nil {
		output.PrintError(output.CodeValidationError, err.Error(), map[string]string{
			"flag": "--json",
		})
		return err
	}
	if body == nil {
		err := fmt.Errorf("--json is required (request body for /v1/account_signup/code)")
		output.PrintError(output.CodeValidationError, err.Error(), nil)
		return err
	}

	baseURL := resolveSignupBaseURL(cmd)
	timeout, _ := cmd.Flags().GetDuration("timeout")

	if GetDryRun(cmd) {
		dryRun := map[string]any{
			"dry_run": true,
			"method":  "POST",
			"url":     baseURL + "/v1/account_signup/code",
			"headers": map[string]string{
				"Content-Type": "application/json",
			},
			"body": json.RawMessage(body),
		}
		return output.FprintJSON(cmd.OutOrStdout(), dryRun)
	}

	result, err := client.PostAnonymous(cmd.Context(), baseURL, "/v1/account_signup/code", body, timeout)
	if err != nil {
		return handleAPIError(err)
	}

	// Persist the bootstrap token so subsequent cio calls are authenticated.
	// The returned sa_live_ token is shown once by the server; saving it here
	// is the agent-friendly default. On save failure we still print the
	// response so the caller can capture the token manually.
	saveErr := saveSignupCredentials(result, body, baseURL)

	if err := output.FprintProcess(cmd.OutOrStdout(), result, GetJQFlag(cmd)); err != nil {
		return err
	}
	if saveErr != nil {
		output.PrintError(output.CodeGeneralError,
			fmt.Sprintf("signup succeeded but saving credentials failed: %s", saveErr.Error()),
			map[string]any{
				"hint": "Capture 'token' from the response above and run: cio auth login --with-token",
			})
		return saveErr
	}
	return nil
}

// saveSignupCredentials extracts the bootstrap token + account_id from a
// successful /v1/account_signup/code response and writes them to
// ~/.cio/config.json. Region is derived from --api-url if recognizable, else
// from the request body's data_center field, else defaults to "us".
func saveSignupCredentials(response json.RawMessage, requestBody []byte, baseURL string) error {
	var parsed struct {
		Token     string          `json:"token"`
		AccountID json.RawMessage `json:"account_id"`
	}
	if err := json.Unmarshal(response, &parsed); err != nil {
		return fmt.Errorf("parse signup response: %w", err)
	}
	if parsed.Token == "" {
		return fmt.Errorf("signup response missing 'token'")
	}
	if !client.IsServiceAccountToken(parsed.Token) {
		return fmt.Errorf("signup response token is not an sa_live_ credential")
	}

	accountID := strings.Trim(string(parsed.AccountID), `"`)
	if accountID == "" || accountID == "null" {
		accountID = ""
	}

	region := client.RegionFromBaseURL(baseURL)
	if region == "" {
		var req struct {
			DataCenter string `json:"data_center"`
		}
		_ = json.Unmarshal(requestBody, &req)
		region = strings.ToLower(strings.TrimSpace(req.DataCenter))
	}
	if region == "" {
		region = "us"
	}

	creds := &client.Credentials{
		ServiceAccountToken: parsed.Token,
		AccountID:           accountID,
		Region:              region,
	}
	return client.WriteCredentials(creds)
}

// runSignupRequest handles both signup POSTs: validates --json, resolves the
// base URL, honors --dry-run, and prints the response as JSON.
func runSignupRequest(cmd *cobra.Command, path string) error {
	body, err := GetJSONBody(cmd)
	if err != nil {
		output.PrintError(output.CodeValidationError, err.Error(), map[string]string{
			"flag": "--json",
		})
		return err
	}
	if body == nil {
		err := fmt.Errorf("--json is required (request body for %s)", path)
		output.PrintError(output.CodeValidationError, err.Error(), nil)
		return err
	}

	baseURL := resolveSignupBaseURL(cmd)
	timeout, _ := cmd.Flags().GetDuration("timeout")

	if GetDryRun(cmd) {
		dryRun := map[string]any{
			"dry_run": true,
			"method":  "POST",
			"url":     baseURL + path,
			"headers": map[string]string{
				"Content-Type": "application/json",
			},
			"body": json.RawMessage(body),
		}
		return output.FprintJSON(cmd.OutOrStdout(), dryRun)
	}

	result, err := client.PostAnonymous(cmd.Context(), baseURL, path, body, timeout)
	if err != nil {
		return handleAPIError(err)
	}

	return output.FprintProcess(cmd.OutOrStdout(), result, GetJQFlag(cmd))
}

// resolveCLILoginURL returns the shared hosted CLI login URL.
// CIO_UI_URL can override the UI origin for non-production or test flows.
// The API URL is intentionally ignored here: it is a backend host and bears no
// relation to where the UI is served.
func resolveCLILoginURL() string {
	if envURL := os.Getenv("CIO_UI_URL"); envURL != "" {
		return strings.TrimRight(envURL, "/") + "/cli"
	}
	return "https://fly.customer.io/cli"
}

// resolveLoginAPIURL picks --api-url > CIO_API_URL > the default token
// exchange path inside DiscoverRegion.
func resolveLoginAPIURL(cmd *cobra.Command) string {
	apiURL, _ := cmd.Flags().GetString("api-url")
	if apiURL != "" {
		return apiURL
	}
	if envURL := os.Getenv("CIO_API_URL"); envURL != "" {
		return envURL
	}
	return ""
}

func readInteractiveToken(input io.Reader, stderr io.Writer) (string, error) {
	var (
		fd    uintptr
		hasFD bool
	)
	if f, ok := input.(*os.File); ok {
		fd = f.Fd()
		hasFD = true
	}
	return readInteractiveTokenWithTTY(input, stderr, fd, hasFD)
}

func readInteractiveTokenWithTTY(input io.Reader, stderr io.Writer, fd uintptr, hasFD bool) (string, error) {
	fmt.Fprint(stderr, "Paste token: ")

	if hasFD && isTerminalInput(fd) {
		tokenBytes, err := readPasswordInput(fd)
		fmt.Fprintln(stderr)
		if err != nil {
			return "", fmt.Errorf("failed to read token: %w", err)
		}

		return strings.TrimSpace(string(tokenBytes)), nil
	}

	scanner := bufio.NewScanner(input)
	if scanner.Scan() {
		return strings.TrimSpace(scanner.Text()), nil
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("failed to read token: %w", err)
	}
	return "", nil
}

// resolveSignupBaseURL picks --api-url > CIO_API_URL > the default US base URL.
// These endpoints don't require a stored region since they run pre-account.
func resolveSignupBaseURL(cmd *cobra.Command) string {
	apiURL, _ := cmd.Flags().GetString("api-url")
	if apiURL != "" {
		return apiURL
	}
	if envURL := os.Getenv("CIO_API_URL"); envURL != "" {
		return envURL
	}
	return client.BaseURLForRegion("us")
}

// ---------------------------------------------------------------------------
// init
// ---------------------------------------------------------------------------

func init() {
	authLoginCmd.Flags().Bool("with-token", false, "Read token from standard input")

	authSignupCmd.AddCommand(authSignupStartCmd)
	authSignupCmd.AddCommand(authSignupVerifyCmd)

	authCmd.AddCommand(authLoginCmd)
	authCmd.AddCommand(authLogoutCmd)
	authCmd.AddCommand(authStatusCmd)
	authCmd.AddCommand(authTokenCmd)
	authCmd.AddCommand(authSignupCmd)
	rootCmd.AddCommand(authCmd)
}
