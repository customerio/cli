package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/customerio/cli/internal/client"
	"github.com/customerio/cli/internal/output"
	"github.com/customerio/cli/internal/routes"
	"github.com/customerio/cli/internal/validate"
	"github.com/spf13/cobra"
)

var pathParamRegex = regexp.MustCompile(`\{(\w+)\}`)

var apiCmd = &cobra.Command{
	Use:   "api <path>",
	Short: "Make an authenticated Customer.io API request",
	Long: `Make an authenticated HTTP request to Customer.io APIs.

The path argument is an API endpoint, e.g. /v1/environments/{environment_id}/campaigns.
Placeholders like {environment_id} are substituted from --params. The HTTP method
defaults to GET (or POST if --json or --file is provided); override with -X/--method.

Endpoints that take a file accept it through --file, which sends the request as
multipart/form-data. --json then supplies the request's other form fields instead
of a JSON body. Large uploads can outrun the default 30s budget; raise it with
--timeout.

All standard flags work: --jq, --dry-run, --page-all, --page, --limit.

Use 'cio schema' to discover endpoints and their parameters.

Examples:
  cio api /v1/environments/{environment_id}/campaigns --params '{"environment_id": "456"}'
  cio api /v1/environments/{environment_id}/campaigns/{campaign_id} --params '{"environment_id": "456", "campaign_id": "789"}'
  cio api /v1/environments/{environment_id}/campaigns -X POST --params '{"environment_id": "456"}' --json '{"campaign": {"name": "Test"}}'
  cio api /v1/accounts/{account_id} --params '{"account_id": "123"}'
  cio api /v1/environments/{environment_id}/segments --params '{"environment_id": "456"}' --dry-run
  cio api /v1/environments/{environment_id}/knowledge_source_library/upload --params '{"environment_id": "456"}' --file @runbook.md --json '{"name": "Ops runbook"}' --timeout 120s`,
	Args: cobra.ExactArgs(1),
	RunE: runAPI,
}

func init() {
	apiCmd.Flags().StringP("method", "X", "", "HTTP method (default: GET, or POST if --json or --file is provided)")
	apiCmd.Flags().StringArray("file", nil, "Send the request as multipart/form-data with a file part: --file @path, or --file field=@path to name the part (repeatable). --json then supplies the request's other form fields")
	apiCmd.Flags().Bool("no-preflight", false, "Send the request even if the path is absent from the API spec")
	rootCmd.AddCommand(apiCmd)
}

func runAPI(cmd *cobra.Command, args []string) error {
	c := clientFromCmd(cmd)
	if c == nil {
		return errNoClient(cmd)
	}

	pathTemplate := args[0]
	if !strings.HasPrefix(pathTemplate, "/") {
		pathTemplate = "/" + pathTemplate
	}
	if strings.ContainsAny(pathTemplate, "?#") {
		err := fmt.Errorf("path must not contain query or fragment characters; pass query values via --params")
		output.PrintError(output.CodeValidationError, err.Error(), nil)
		return err
	}

	// Determine HTTP method.
	methodFlag, _ := cmd.Flags().GetString("method")
	jsonBody, err := GetJSONBody(cmd)
	if err != nil {
		return err
	}

	fileParts, err := GetFileParts(cmd)
	if err != nil {
		output.PrintError(output.CodeValidationError, err.Error(), nil)
		return err
	}

	httpMethod := resolveMethod(methodFlag, jsonBody != nil || len(fileParts) > 0)

	// Parse --params: separate path params from query params.
	paramsRaw, _ := cmd.Flags().GetString("params")
	pathParams, queryParams, err := parseAPIParams(pathTemplate, paramsRaw)
	if err != nil {
		output.PrintError(output.CodeValidationError, err.Error(), nil)
		return err
	}

	// Auto-fill account_id from stored credentials if not explicitly provided.
	// Skip when CIO_ACCESS_TOKEN is set — the JWT may belong to a different
	// service account / account than what was saved during login.
	if _, hasAccountID := extractPathParamNames(pathTemplate)["account_id"]; hasAccountID {
		if pathParams["account_id"] == "" && client.ResolveAccessToken() == "" {
			if creds, err := client.ReadCredentials(); err == nil && creds.AccountID != "" {
				if err := validate.ValidateResourceID(creds.AccountID); err != nil {
					valErr := fmt.Errorf("stored account_id %q is invalid; please re-run 'cio auth login' or provide account_id via --params: %w", creds.AccountID, err)
					output.PrintError(output.CodeValidationError, valErr.Error(), nil)
					return valErr
				}
				pathParams["account_id"] = creds.AccountID
			}
		}
	}

	// Resolve path template: {id} → actual value.
	resolvedPath, err := resolvePathTemplate(pathTemplate, pathParams)
	if err != nil {
		output.PrintError(output.CodeValidationError, err.Error(), nil)
		return err
	}

	jq := GetJQFlag(cmd)

	// Ahead of the dry run: reporting "valid" for a combination the real run
	// rejects is worse than no check.
	if _, _, pageAllFlag := GetPaginationFlags(cmd); pageAllFlag && len(fileParts) > 0 {
		err := fmt.Errorf("--page-all cannot be combined with --file")
		output.PrintError(output.CodeValidationError, err.Error(), nil)
		return err
	}

	if err := preflightPath(cmd, c, httpMethod, resolvedPath); err != nil {
		return err
	}

	// Dry run.
	if GetDryRun(cmd) {
		apiURL, _ := cmd.Flags().GetString("api-url")
		if apiURL == "" {
			apiURL = c.BaseURL()
		}
		contentType := "application/json"
		if len(fileParts) > 0 {
			contentType = "multipart/form-data"
		}
		dryRun := map[string]any{
			"dry_run": true,
			"method":  httpMethod,
			"url":     apiURL + resolvedPath,
			"headers": map[string]string{
				"Authorization": "Bearer [REDACTED]",
				"Content-Type":  contentType,
			},
			"validation": map[string]any{
				"valid":  true,
				"errors": []string{},
			},
		}
		if len(queryParams) > 0 {
			dryRun["params"] = queryParams
		}
		if len(fileParts) > 0 {
			// Names and sizes only — a dry run must not spill file contents.
			dryRun["files"] = filePartsSummary(fileParts)
			fields, err := formFieldsFromJSON(jsonBody)
			if err != nil {
				output.PrintError(output.CodeValidationError, err.Error(), nil)
				return err
			}
			if len(fields) > 0 {
				dryRun["fields"] = fields
			}
		} else if jsonBody != nil {
			dryRun["body"] = json.RawMessage(jsonBody)
		}
		return output.FprintJSON(cmd.OutOrStdout(), dryRun)
	}

	// Merge query params with pagination flags.
	page, limit, pageAll := GetPaginationFlags(cmd)
	if queryParams == nil {
		queryParams = make(map[string]string)
	}
	if page > 0 {
		queryParams["page"] = fmt.Sprintf("%d", page)
	}
	if limit > 0 {
		queryParams["limit"] = fmt.Sprintf("%d", limit)
	}

	if pageAll {
		return doPageAll(cmd, c, resolvedPath, queryParams, page, limit)
	}

	body, err := requestBody(jsonBody, fileParts)
	if err != nil {
		output.PrintError(output.CodeValidationError, err.Error(), nil)
		return err
	}

	result, err := c.DoWithBody(cmd.Context(), httpMethod, resolvedPath, queryParams, body)
	if err != nil {
		return handleAPIError(err)
	}

	return output.FprintProcess(cmd.OutOrStdout(), result, jq, GetRawFlag(cmd))
}

func requestBody(jsonBody json.RawMessage, fileParts []client.FilePart) (*client.Body, error) {
	if len(fileParts) == 0 {
		if jsonBody == nil {
			return nil, nil
		}
		return &client.Body{ContentType: "application/json", Bytes: jsonBody}, nil
	}
	fields, err := formFieldsFromJSON(jsonBody)
	if err != nil {
		return nil, err
	}
	return client.NewMultipartBody(fileParts, fields)
}

// preflightSpecFetchBudget bounds everything the path check does over the
// network when it finds the cache cold: the token exchange, if the caller's
// credential needs one, and the one spec download. The spec is ~2.7MB and
// normally arrives well inside this; a branch that does not finish in time is
// a sign of a slow or stalled host, and the check gives up rather than hold
// the caller's request behind it.
const preflightSpecFetchBudget = 5 * time.Second

// preflightPath rejects a path the API spec does not describe, before the
// request is sent.
//
// An unknown path on the API host does not answer 404: the web app's catch-all
// serves it a 200 and an HTML page. A mistyped or invented path therefore comes
// back looking like an ambiguous empty result rather than a mistake, which
// invites trying more variants of it. Failing here instead names the problem
// and points at `cio schema`, and costs no request.
//
// It is a gate, not a lookup: an error means the request must not be sent, and
// nil means let it through. Callers get nil both when the path matches a route
// and when the check cannot be trusted to judge it — no spec available, or a
// path outside the trees the spec documents. The API stays the authority on
// what exists; this only catches paths already known to be absent.
func preflightPath(cmd *cobra.Command, c *client.Client, httpMethod, resolvedPath string) error {
	if skip, _ := cmd.Flags().GetBool("no-preflight"); skip {
		return nil
	}

	// Read what the cache already holds first: no lock, no network, ~25ms.
	idx, err := routes.LoadPathIndexFromCache(specCacheOptions(c))
	if err != nil {
		// Cold cache. Fetch the spec once so the check can exist at all — a
		// session that never runs `cio schema` would otherwise never be
		// checked. The first version of this check refused to download here,
		// because EnsureSpecs then meant an unbounded flock and two 30s
		// fetches in front of every request; this fetch is bounded in each of
		// those respects instead: the lock is tried, not waited for (another
		// process downloading means we step aside), the download runs under a
		// short deadline, no prose reaches stderr, and any of those failing
		// means proceeding without an opinion. The identity's token is used so
		// what lands in the cache is the same plan-filtered spec `cio schema`
		// would fetch, never an anonymous one that would mislead it later.
		// One budget covers the whole branch — the token exchange as well as
		// the download — so a stalled token endpoint cannot hold the request
		// any longer than a stalled spec host can.
		ctx, cancel := context.WithTimeout(cmd.Context(), preflightSpecFetchBudget)
		defer cancel()
		opts := specLoadOptions(ctx, c)
		if c.ServiceAccountToken() != "" && opts.AccessToken == "" {
			// The token could not be exchanged, or not within the budget.
			// `cio schema` falls back to an anonymous fetch here; this check
			// must not: an anonymous spec is the wrong thing to judge a
			// plan-filtered identity against, and it would land in a partition
			// the identity's reads never look in.
			return nil
		}
		idx, err = routes.EnsurePathIndex(ctx, opts)
		if err != nil {
			return nil
		}
	}

	// The route exists: nothing to block, so let the request proceed. The
	// matched template is of no use here — the request keeps the caller's path.
	if _, matched := idx.Lookup(httpMethod, resolvedPath); matched {
		return nil
	}

	// The path did not match, which is only grounds to reject it if it sits
	// inside a scope the spec describes. Outside one, the spec has nothing to
	// say: real endpoints are omitted from it, and whole trees may postdate it.
	if !idx.Covers(resolvedPath) {
		return nil
	}

	// The shape exists but not for this verb — a different mistake, and one
	// the caller fixes by changing -X rather than the path.
	if allowed := idx.MethodsFor(resolvedPath); len(allowed) > 0 {
		// Name the fix, not just the verb: the method is usually implicit here
		// (GET unless a body was passed), so the caller needs to be told to
		// set it rather than left to infer that from the list.
		fix := fmt.Sprintf("the path accepts %s", strings.Join(allowed, ", "))
		if len(allowed) == 1 {
			fix = fmt.Sprintf("retry with -X %s", allowed[0])
		}
		err := fmt.Errorf(
			"%s is not allowed on %s (%s); request not sent",
			httpMethod, resolvedPath, fix)
		output.PrintError(output.CodeValidationError, err.Error(), map[string]any{
			"method":          httpMethod,
			"path":            resolvedPath,
			"allowed_methods": allowed,
			"skip_check_flag": "--no-preflight",
		})
		return err
	}

	suggestions := idx.Suggest(resolvedPath, 5)
	hint := "run 'cio schema' to list resources"
	if len(suggestions) > 0 && suggestions[0].Resource != "" {
		hint = fmt.Sprintf("run 'cio schema %s' to list that resource's endpoints", suggestions[0].Resource)
	}
	// The spec omits a few real endpoints, and for those `cio schema` cannot
	// list what it does not describe — so the way past a wrong rejection has to
	// be in the message, not only in the details below.
	hint += ", or resend with --no-preflight if you know the endpoint exists"

	closest := make([]string, 0, len(suggestions))
	for _, s := range suggestions {
		closest = append(closest, s.String())
	}

	err = fmt.Errorf(
		"%s %s is not an endpoint in the API spec, so the request was not sent; %s",
		httpMethod, resolvedPath, hint)
	output.PrintError(output.CodeValidationError, err.Error(), map[string]any{
		"method":            httpMethod,
		"path":              resolvedPath,
		"closest_endpoints": closest,
		"skip_check_flag":   "--no-preflight",
	})
	return err
}

// resolveMethod determines the HTTP method from the flag or defaults.
func resolveMethod(flag string, hasBody bool) string {
	if flag != "" {
		return strings.ToUpper(flag)
	}
	if hasBody {
		return "POST"
	}
	return "GET"
}

// extractPathParamNames returns the set of {placeholder} names from a path template.
func extractPathParamNames(pathTemplate string) map[string]bool {
	names := make(map[string]bool)
	for _, match := range pathParamRegex.FindAllStringSubmatch(pathTemplate, -1) {
		names[match[1]] = true
	}
	return names
}

// parseAPIParams separates path template params from query params.
// Path params are those matching {placeholder} in the path template.
// Input is validated through validate.ValidateParams (JSON object or
// query-string sugar; keys must match [a-zA-Z0-9_]+ with an optional []
// suffix, values must not contain control characters, see
// MaxParamValueLength). Path params are validated as safe URL path segments;
// the API remains the source of truth for endpoint-specific ID semantics.
func parseAPIParams(pathTemplate, paramsJSON string) (pathParams, queryParams map[string]string, err error) {
	pathParams = make(map[string]string)
	queryParams = make(map[string]string)

	if strings.TrimSpace(paramsJSON) == "" {
		return pathParams, queryParams, nil
	}

	params, err := validate.ValidateParams(paramsJSON)
	if err != nil {
		return nil, nil, err
	}

	pathParamNames := extractPathParamNames(pathTemplate)
	for k, v := range params {
		if pathParamNames[k] {
			if err := validate.ValidatePathSegmentID(v); err != nil {
				return nil, nil, fmt.Errorf("path parameter %q: %w", k, err)
			}
			pathParams[k] = v
		} else {
			queryParams[k] = v
		}
	}

	return pathParams, queryParams, nil
}

// resolvePathTemplate substitutes {param} placeholders with actual values.
func resolvePathTemplate(pathTemplate string, pathParams map[string]string) (string, error) {
	path := pathTemplate
	for name, value := range pathParams {
		placeholder := "{" + name + "}"
		if !strings.Contains(path, placeholder) {
			return "", fmt.Errorf("unknown path parameter: %s", name)
		}
		path = strings.ReplaceAll(path, placeholder, url.PathEscape(value))
	}

	// Check for unresolved placeholders.
	if matches := pathParamRegex.FindAllStringSubmatch(path, -1); len(matches) > 0 {
		var missing []string
		for _, m := range matches {
			missing = append(missing, m[1])
		}
		return "", fmt.Errorf("missing required path parameters: %s (pass via --params)", strings.Join(missing, ", "))
	}

	return path, nil
}

// listEndpoints returns a summary of all available API endpoints grouped by resource.
// Used by the schema command.
func listEndpoints(reg *routes.Registry) []map[string]any {
	var result []map[string]any
	for _, resource := range reg.Resources() {
		rs := reg.ByResource[resource]
		methods := make([]map[string]string, 0, len(rs))
		for _, r := range rs {
			methods = append(methods, map[string]string{
				"method":      r.Method,
				"http_method": r.HTTPMethod,
				"path":        r.Path,
				"summary":     r.Summary,
			})
		}
		result = append(result, map[string]any{
			"resource":  resource,
			"endpoints": methods,
			"count":     len(methods),
		})
	}
	return result
}
