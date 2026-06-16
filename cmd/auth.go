package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/customerio/cli/internal/client"
	"github.com/customerio/cli/internal/clipboard"
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

var readClipboard = clipboard.Read

// Clipboard polling knobs; shrunk in tests. The wait budget stays under
// typical agent tool-call timeouts (~2m) so a timed-out wait returns a clean
// error the caller can react to instead of being killed mid-poll.
var (
	clipboardPollInterval = time.Second
	clipboardWaitBudget   = 90 * time.Second
)

// clipboardToken reads a service-account token from the system clipboard.
//
// With wait=true it polls until a token-shaped value appears or the budget
// runs out, so the command can be started before the user has copied the
// token — no confirmation round-trip. While polling, non-token clipboard
// contents are ignored; they are never echoed, stored, or sent anywhere.
func clipboardToken(cmd *cobra.Command, wait bool) (string, error) {
	if wait {
		fmt.Fprintf(cmd.ErrOrStderr(), "Sign in and copy your token from:\n\n    %s\n\nWaiting for it on your clipboard (Ctrl-C cancels)...\n", resolveCLILoginURL(resolveLoginBaseURL(cmd)))
	}
	deadline := time.Now().Add(clipboardWaitBudget)
	for {
		text, err := readClipboard(cmd.Context())
		if err != nil {
			err = fmt.Errorf("could not read the clipboard: %w", err)
			output.PrintError(output.CodeGeneralError, err.Error(), map[string]any{
				"hint": "Remote shells (SSH, containers) can't see your local clipboard. Run 'cio auth login' in your own terminal, or pipe the token: pbpaste | cio auth login --with-token",
			})
			return "", err
		}
		token := strings.TrimSpace(text)
		if client.IsServiceAccountToken(token) {
			return token, nil
		}

		if !wait {
			if token == "" {
				err := fmt.Errorf("clipboard is empty")
				output.PrintError(output.CodeValidationError, err.Error(), map[string]any{
					"hint": "Copy the token from the token page, then re-run. In a remote shell your local clipboard isn't visible — run 'cio auth login' in your own terminal instead.",
				})
				return "", err
			}
			// Unlike the generic token validation, don't echo any of the
			// input — whatever is on the clipboard may be unrelated and
			// sensitive.
			err := fmt.Errorf("clipboard contents don't look like a service account token (expected a %q or %q prefix)",
				client.ServiceAccountTokenPrefix, client.SandboxServiceAccountTokenPrefix)
			output.PrintError(output.CodeValidationError, err.Error(), map[string]any{
				"hint": "Copy the token last — it must be the most recent thing on your clipboard — then re-run.",
			})
			return "", err
		}

		if time.Now().After(deadline) {
			err := fmt.Errorf("timed out waiting for a token on the clipboard")
			output.PrintError(output.CodeValidationError, err.Error(), map[string]any{
				"hint": "Copy the token from the token page and re-run. In a remote shell your local clipboard isn't visible — run 'cio auth login' in your own terminal instead.",
			})
			return "", err
		}
		select {
		case <-cmd.Context().Done():
			return "", cmd.Context().Err()
		case <-time.After(clipboardPollInterval):
		}
	}
}

var authCmd = &cobra.Command{
	Use:   "auth",
	Short: "Authenticate Customer.io CLI with the Customer.io API",
	Long: `Manage authentication for the Customer.io CLI.

Credentials are stored in ~/.cio/config.json.
You can also set the CIO_TOKEN environment variable or pass --token on any command.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return cmd.Help()
	},
}

// ---------------------------------------------------------------------------
// auth login
// ---------------------------------------------------------------------------

var authLoginCmd = &cobra.Command{
	Use:   "login",
	Short: "Authenticate the Customer.io CLI",
	Long: `Sign in to the Customer.io CLI.

If you're already signed in, this prints a link to open Customer.io in
your browser — no password needed.

If this is your first time, you'll be guided to sign in at
fly.customer.io and paste a token back into your terminal.

After copying a token from the token page, you can read it straight from
your clipboard without pasting:
  $ cio auth login --from-clipboard

Add --wait to start the command first and have it pick the token up the
moment you copy it:
  $ cio auth login --from-clipboard --wait

For CI or non-interactive use:
  $ echo "$TOKEN" | cio auth login --with-token
  $ cio auth login <token>

Credentials are saved to the active profile and that profile becomes current.
Pass --profile <name> to log into a specific profile; without it, login
re-authenticates whichever profile is currently selected (see 'cio profile').`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		withToken, _ := cmd.Flags().GetBool("with-token")
		fromClipboard, _ := cmd.Flags().GetBool("from-clipboard")
		waitForClipboard, _ := cmd.Flags().GetBool("wait")

		if waitForClipboard && !fromClipboard {
			err := fmt.Errorf("--wait requires --from-clipboard")
			output.PrintError(output.CodeValidationError, err.Error(), nil)
			return err
		}

		var (
			token string
			err   error
		)

		switch {
		case fromClipboard:
			token, err = clipboardToken(cmd, waitForClipboard)
			if err != nil {
				return err
			}
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
			// If we already have a sa_live_ on disk, do the CLI → web
			// handoff: print a URL with a short-lived JWT that signs the
			// user into fly directly. Skips the password-reset detour for
			// users who signed up via CLI.
			if existing := loadStoredServiceAccountToken(); existing != "" {
				return runLoginCLILink(cmd, existing)
			}

			// Print the URL rather than shelling out to a browser — works
			// under SSH, headless CI, and restrictive sandboxes.
			loginURL := resolveCLILoginURL(resolveLoginBaseURL(cmd))
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
			err := fmt.Errorf("token must start with %q or %q — got %q",
				client.ServiceAccountTokenPrefix,
				client.SandboxServiceAccountTokenPrefix,
				token[:min(len(token), 10)])
			output.PrintError(output.CodeValidationError, err.Error(), map[string]any{
				"hint": "Production service account tokens start with sa_live_. Sandbox Builder accounts use sa_sandbox_ until go-live.",
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

		// Persist a custom base URL (e.g. staging) so the profile reuses it
		// without re-passing --api-url on every command.
		if apiURL := resolveLoginAPIURL(cmd); apiURL != "" {
			creds.APIURL = apiURL
		}

		if err := client.WriteCredentials(creds); err != nil {
			output.PrintError(output.CodeGeneralError, err.Error(), nil)
			return err
		}

		// Logging into a profile makes it the active one for later commands.
		profile := client.ActiveProfileName()
		if err := client.SetCurrentProfile(profile); err != nil {
			output.PrintError(output.CodeGeneralError, err.Error(), nil)
			return err
		}

		return output.FprintJSON(cmd.OutOrStdout(), map[string]any{
			"status":      "ok",
			"message":     "Authenticated successfully. Credentials saved to ~/.cio/config.json",
			"profile":     profile,
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
	Long:  "Delete the active profile's stored credentials from ~/.cio/config.json.",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		profile := client.ActiveProfileName()
		if err := client.DeleteCredentials(); err != nil {
			output.PrintError(output.CodeGeneralError, err.Error(), nil)
			return err
		}

		return output.FprintJSON(cmd.OutOrStdout(), map[string]any{
			"status":  "ok",
			"message": fmt.Sprintf("Credentials for profile %q removed from ~/.cio/config.json", profile),
			"profile": profile,
		})
	},
}

// ---------------------------------------------------------------------------
// auth status
// ---------------------------------------------------------------------------

var authStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Display active authentication state",
	Long: `Show which token the CLI is currently using and whether it's valid.

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
			"profile":      client.ActiveProfileName(),
			"token_source": tokenSource,
			"token":        client.MaskToken(token),
		}

		// Stored metadata is only authoritative when the stored token is active.
		if tokenSource == "config_file" {
			if creds, err := client.ReadCredentials(); err == nil {
				if creds.Region != "" {
					statusResult["region"] = creds.Region
				}
				if creds.AccountID != "" {
					statusResult["account_id"] = creds.AccountID
				}
			}
		}

		// Verify the active token and derive account metadata from that session.
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
				if info, err := c.CurrentAccountInfo(cmd.Context()); err == nil {
					if info.Region != "" {
						statusResult["region"] = info.Region
					}
					if info.AccountID != "" {
						statusResult["account_id"] = info.AccountID
					}
				} else if tokenSource != "config_file" {
					statusResult["account_info_error"] = err.Error()
				}
			}
		}

		jq := GetJQFlag(cmd)
		data, _ := json.Marshal(statusResult)
		return output.FprintProcess(cmd.OutOrStdout(), json.RawMessage(data), jq, GetRawFlag(cmd))
	},
}

// ---------------------------------------------------------------------------
// auth token
// ---------------------------------------------------------------------------

var authTokenCmd = &cobra.Command{
	Use:   "token",
	Short: "Print the active token",
	Long: `Print the active token to stdout.

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
	Short: "Create a new Customer.io account",
	Long: `Create a new Customer.io account from the command line.

Step 1: 'cio auth signup start' sends a verification code to your email.
Step 2: 'cio auth signup verify' confirms the code and creates your account.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return cmd.Help()
	},
}

var authSignupStartCmd = &cobra.Command{
	Use:   "start",
	Short: "Email a 6-digit verification code to the given address",
	Long: `Send a verification code to the given email address.

  cio auth signup start --json '{"email":"you@example.com"}'`,
	Args: cobra.NoArgs,
	RunE: runAuthSignupStart,
}

var authSignupVerifyCmd = &cobra.Command{
	Use:   "verify",
	Short: "Confirm the verification code and create the account",
	Long: `Confirm the verification code and create the account.

  cio auth signup verify --json '{
    "email": "you@example.com",
    "code": "123456",
    "company_name": "Acme",
    "first_name": "Ada",
    "last_name": "Lovelace",
    "data_center": "us"
  }'

On success, your credentials are saved automatically and you're ready
to use the CLI.`,
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
	// The returned service account token is shown once by the server; saving
	// it here is the agent-friendly default. On save failure we still print
	// the response so the caller can capture the token manually.
	saveErr := saveSignupCredentials(result, body, baseURL)

	if err := output.FprintProcess(cmd.OutOrStdout(), result, GetJQFlag(cmd), GetRawFlag(cmd)); err != nil {
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
// ~/.cio/config.json. Region priority: response data_center (authoritative
// from server), then request body data_center, then --api-url, then "us".
func saveSignupCredentials(response json.RawMessage, requestBody []byte, baseURL string) error {
	var parsed struct {
		Token      string          `json:"token"`
		AccountID  json.RawMessage `json:"account_id"`
		DataCenter string          `json:"data_center"`
	}
	if err := json.Unmarshal(response, &parsed); err != nil {
		return fmt.Errorf("parse signup response: %w", err)
	}
	if parsed.Token == "" {
		return fmt.Errorf("signup response missing 'token'")
	}
	if !client.IsServiceAccountToken(parsed.Token) {
		return fmt.Errorf("signup response token is not a supported service account credential")
	}

	accountID := strings.Trim(string(parsed.AccountID), `"`)
	if accountID == "" || accountID == "null" {
		accountID = ""
	}

	region := strings.ToLower(strings.TrimSpace(parsed.DataCenter))
	if region == "" {
		var req struct {
			DataCenter string `json:"data_center"`
		}
		_ = json.Unmarshal(requestBody, &req)
		region = strings.ToLower(strings.TrimSpace(req.DataCenter))
	}
	if region == "" {
		region = client.RegionFromBaseURL(baseURL)
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

	return output.FprintProcess(cmd.OutOrStdout(), result, GetJQFlag(cmd), GetRawFlag(cmd))
}

// loadStoredServiceAccountToken reads the saved sa_live_ token from
// ~/.cio/config.json. It deliberately ignores CIO_TOKEN and the --token
// flag — `cio auth login` is about persisting credentials, so we only
// branch into the handoff flow when we already wrote a config file.
func loadStoredServiceAccountToken() string {
	creds, err := client.ReadCredentials()
	if err != nil {
		return ""
	}
	if !client.IsServiceAccountToken(creds.ServiceAccountToken) {
		return ""
	}
	return creds.ServiceAccountToken
}

// runLoginCLILink exchanges a stored sa_live_ for a short-lived JWT and
// prints a one-click URL the user can open to sign into the Customer.io
// web UI. The CLI's stored credentials are unchanged — this flow only
// bootstraps a browser session, it does not refresh the saved token.
func runLoginCLILink(cmd *cobra.Command, saToken string) error {
	// Honor the host the stored credentials were issued against before falling
	// back to the region default. Otherwise a token minted on a non-production
	// host gets exchanged against us.fly.customer.io and is rejected with a 401
	// — the same resolution order used by every other command.
	baseURL := resolveLoginBaseURL(cmd)
	if baseURL == "" {
		baseURL = client.BaseURLForRegion("us")
	}
	timeout, _ := cmd.Flags().GetDuration("timeout")

	resp, err := client.MintLoginCLILink(cmd.Context(), baseURL, saToken, timeout)
	if err != nil {
		return handleAPIError(err)
	}

	uiURL := resolveCLILoginURL(baseURL) + "?token=" + url.QueryEscape(resp.HandoffToken)

	fmt.Fprintf(cmd.ErrOrStderr(), "You're already signed in. Open this URL in your browser to access Customer.io:\n\n    %s\n\n", uiURL)
	fmt.Fprintf(cmd.ErrOrStderr(), "This link is valid for %d seconds.\n", resp.ExpiresIn)

	return output.FprintJSON(cmd.OutOrStdout(), map[string]any{
		"status":     "ok",
		"message":    "Open the URL in your browser to sign into Customer.io.",
		"url":        uiURL,
		"expires_in": resp.ExpiresIn,
	})
}

// resolveCLILoginURL returns the hosted CLI login page URL for a given API base
// URL. CIO_UI_URL overrides the origin entirely (non-production or test flows).
// Otherwise the /cli page is served by the shared, region-less frontend that
// fronts the API host, so we derive the UI origin by dropping the region label:
//
//	https://us.fly.customer.io -> https://fly.customer.io/cli
//	https://eu.fly.customer.io -> https://fly.customer.io/cli
func resolveCLILoginURL(apiBaseURL string) string {
	if envURL := os.Getenv("CIO_UI_URL"); envURL != "" {
		return strings.TrimRight(envURL, "/") + "/cli"
	}
	if origin := uiOriginFromAPIBase(apiBaseURL); origin != "" {
		return origin + "/cli"
	}
	return "https://fly.customer.io/cli"
}

// uiOriginFromAPIBase derives the region-less UI origin from an API base URL by
// stripping a leading "us." or "eu." region label from the host. Returns "" for
// an unparseable or empty URL so the caller can fall back to the default.
func uiOriginFromAPIBase(apiBaseURL string) string {
	u, err := url.Parse(strings.TrimRight(apiBaseURL, "/"))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	host := u.Host
	if i := strings.IndexByte(host, '.'); i > 0 {
		switch strings.ToLower(host[:i]) {
		case "us", "eu":
			host = host[i+1:]
		}
	}
	return u.Scheme + "://" + host
}

// resolveLoginBaseURL determines the API base URL the login flow should talk to,
// in the same priority order as client.ResolveBaseURL: --api-url > CIO_API_URL >
// the active profile's stored APIURL > the region default. Returns "" only when
// nothing is known (e.g. a first-time login with no stored credentials).
func resolveLoginBaseURL(cmd *cobra.Command) string {
	if u := resolveLoginAPIURL(cmd); u != "" {
		return u
	}
	if creds, err := client.ReadCredentials(); err == nil {
		if creds.APIURL != "" {
			return creds.APIURL
		}
		if creds.Region != "" {
			return client.BaseURLForRegion(creds.Region)
		}
	}
	return ""
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
	authLoginCmd.Flags().Bool("from-clipboard", false, "Read token from the system clipboard")
	authLoginCmd.Flags().Bool("wait", false, "With --from-clipboard: poll until a token appears on the clipboard")
	authLoginCmd.MarkFlagsMutuallyExclusive("with-token", "from-clipboard")

	authSignupCmd.AddCommand(authSignupStartCmd)
	authSignupCmd.AddCommand(authSignupVerifyCmd)

	authCmd.AddCommand(authLoginCmd)
	authCmd.AddCommand(authLogoutCmd)
	authCmd.AddCommand(authStatusCmd)
	authCmd.AddCommand(authTokenCmd)
	authCmd.AddCommand(authSignupCmd)
	rootCmd.AddCommand(authCmd)
}
