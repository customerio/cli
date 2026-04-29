package cmd

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"

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
defaults to GET (or POST if --json is provided); override with -X/--method.

All standard flags work: --jq, --dry-run, --page-all, --page, --limit.

Use 'cio schema' to discover endpoints and their parameters.

Examples:
  cio api /v1/environments/{environment_id}/campaigns --params '{"environment_id": "456"}'
  cio api /v1/environments/{environment_id}/campaigns/{campaign_id} --params '{"environment_id": "456", "campaign_id": "789"}'
  cio api /v1/environments/{environment_id}/campaigns -X POST --params '{"environment_id": "456"}' --json '{"campaign": {"name": "Test"}}'
  cio api /v1/accounts/{account_id} --params '{"account_id": "123"}'
  cio api /v1/environments/{environment_id}/segments --params '{"environment_id": "456"}' --dry-run`,
	Args: cobra.ExactArgs(1),
	RunE: runAPI,
}

func init() {
	apiCmd.Flags().StringP("method", "X", "", "HTTP method (default: GET, or POST if --json is provided)")
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

	// Determine HTTP method.
	methodFlag, _ := cmd.Flags().GetString("method")
	jsonBody, err := GetJSONBody(cmd)
	if err != nil {
		return err
	}

	httpMethod := resolveMethod(methodFlag, jsonBody)

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

	// Dry run.
	if GetDryRun(cmd) {
		apiURL, _ := cmd.Flags().GetString("api-url")
		if apiURL == "" {
			apiURL = c.BaseURL()
		}
		dryRun := map[string]any{
			"dry_run": true,
			"method":  httpMethod,
			"url":     apiURL + resolvedPath,
			"headers": map[string]string{
				"Authorization": "Bearer [REDACTED]",
				"Content-Type":  "application/json",
			},
			"validation": map[string]any{
				"valid":  true,
				"errors": []string{},
			},
		}
		if len(queryParams) > 0 {
			dryRun["params"] = queryParams
		}
		if jsonBody != nil {
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

	result, err := c.Do(cmd.Context(), httpMethod, resolvedPath, queryParams, jsonBody)
	if err != nil {
		return handleAPIError(err)
	}

	return output.FprintProcess(cmd.OutOrStdout(), result, jq)
}

// resolveMethod determines the HTTP method from the flag or defaults.
func resolveMethod(flag string, body []byte) string {
	if flag != "" {
		return strings.ToUpper(flag)
	}
	if body != nil {
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
// Input is validated through validate.ValidateParams (keys must match
// [a-zA-Z0-9_]+, values must not contain control characters, see
// MaxParamValueLength). Path params additionally must be numeric IDs.
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
			if err := validate.ValidateResourceID(v); err != nil {
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
