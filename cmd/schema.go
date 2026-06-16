package cmd

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/customerio/cli/internal/output"
	"github.com/customerio/cli/internal/routes"
	"github.com/spf13/cobra"
)

var schemaCmd = &cobra.Command{
	Use:   "schema [resource | resource.method | METHOD /path]",
	Short: "Introspect API endpoint schemas from the route registry",
	Long: `Dump endpoint schema as JSON for agent introspection.

  cio schema                                — list all resources with endpoint counts
  cio schema campaigns                      — list all endpoints for a resource
  cio schema campaigns.list                 — show full schema for a specific method
  cio schema GET /v1/environments/{environment_id}/campaigns
                                            — show schema for a specific HTTP method + path
  cio schema /v1/environments/{environment_id}/campaigns
                                            — show all methods for a path`,
	Args: cobra.MaximumNArgs(2),
	RunE: runSchema,
}

func init() {
	schemaCmd.Flags().Bool("refresh", false, "Force re-download of API specs")
	rootCmd.AddCommand(schemaCmd)
}

func runSchema(cmd *cobra.Command, args []string) error {
	refresh, _ := cmd.Flags().GetBool("refresh")

	var baseURL string
	var accessToken string
	var cacheKey string
	if c := clientFromCmd(cmd); c != nil {
		baseURL = c.BaseURL()
		if c.ServiceAccountToken() != "" {
			jwt, err := c.EnsureAccessToken(cmd.Context())
			if err == nil {
				accessToken = jwt
				cacheKey = c.ServiceAccountToken()
			}
		}
	}

	if GetDryRun(cmd) {
		return schemaDryRun(cmd, baseURL, accessToken)
	}

	reg, err := routes.LoadRegistry(routes.LoadRegistryOptions{
		BaseURL:      baseURL,
		AccessToken:  accessToken,
		CacheKey:     cacheKey,
		ForceRefresh: refresh,
	})
	if err != nil {
		output.PrintError(output.CodeGeneralError, fmt.Sprintf("failed to load routes: %v", err), nil)
		return err
	}

	switch len(args) {
	case 0:
		// List all resources with endpoint counts.
		return schemaOutput(cmd, listEndpoints(reg))

	case 1:
		arg := args[0]

		// If it starts with /, it's a path — show all methods.
		if strings.HasPrefix(arg, "/") {
			return schemaForPath(cmd, reg, arg)
		}

		// If it contains a dot, treat as resource.method.
		if parts := strings.SplitN(arg, ".", 2); len(parts) == 2 {
			return schemaForResourceMethod(cmd, reg, parts[0], parts[1])
		}

		// Otherwise treat as a resource name.
		return schemaForResource(cmd, reg, arg)

	case 2:
		// "METHOD /path" form.
		method := strings.ToUpper(args[0])
		path := args[1]
		return schemaForHTTPEndpoint(cmd, reg, method, path)

	default:
		return fmt.Errorf("too many arguments")
	}
}

// schemaForResource lists all endpoints for a given resource.
func schemaForResource(cmd *cobra.Command, reg *routes.Registry, resource string) error {
	rs, ok := reg.ByResource[resource]
	if !ok {
		err := fmt.Errorf("unknown resource: %s", resource)
		output.PrintError(output.CodeValidationError, err.Error(), map[string]any{
			"resource":            resource,
			"available_resources": reg.Resources(),
		})
		return err
	}

	var result []map[string]any
	for _, r := range rs {
		result = append(result, routeSummary(&r))
	}
	return schemaOutput(cmd, result)
}

// schemaForResourceMethod shows the full schema for a resource.method pair.
func schemaForResourceMethod(cmd *cobra.Command, reg *routes.Registry, resource, method string) error {
	route := reg.FindRoute(resource, method)
	if route == nil {
		suggestions := suggestRoutes(reg, resource, method)
		msg := fmt.Sprintf("unknown method: %s.%s", resource, method)
		if len(suggestions) > 0 {
			msg += "\n\nDid you mean:"
			for _, s := range suggestions {
				msg += fmt.Sprintf("\n  cio schema %s.%s", s.Resource, s.Method)
			}
		}
		output.PrintError(output.CodeValidationError, msg, nil)
		return fmt.Errorf("%s", msg)
	}

	return schemaOutput(cmd, routeDetail(route))
}

// schemaForPath shows all methods for a given path.
func schemaForPath(cmd *cobra.Command, reg *routes.Registry, path string) error {
	var matches []routes.Route
	for _, r := range reg.Routes {
		if r.Path == path {
			matches = append(matches, r)
		}
	}

	if len(matches) == 0 {
		err := fmt.Errorf("no endpoints found for path: %s", path)
		output.PrintError(output.CodeValidationError, err.Error(), nil)
		return err
	}

	var result []map[string]any
	for _, r := range matches {
		result = append(result, routeDetail(&r))
	}
	return schemaOutput(cmd, result)
}

// schemaForHTTPEndpoint shows schema for a specific METHOD + path.
func schemaForHTTPEndpoint(cmd *cobra.Command, reg *routes.Registry, method, path string) error {
	for _, r := range reg.Routes {
		if r.HTTPMethod == method && r.Path == path {
			return schemaOutput(cmd, routeDetail(&r))
		}
	}

	err := fmt.Errorf("unknown endpoint: %s %s", method, path)
	output.PrintError(output.CodeValidationError, err.Error(), nil)
	return err
}

// routeSummary returns a brief view of a route.
func routeSummary(r *routes.Route) map[string]any {
	m := map[string]any{
		"resource":    r.Resource,
		"method":      r.Method,
		"http_method": r.HTTPMethod,
		"path":        r.Path,
		"summary":     r.Summary,
	}
	if r.HasBody {
		m["has_body"] = true
	}
	return m
}

// routeDetail returns the full schema view of a route.
func routeDetail(r *routes.Route) map[string]any {
	m := map[string]any{
		"resource":    r.Resource,
		"method":      r.Method,
		"http_method": r.HTTPMethod,
		"path":        r.Path,
		"summary":     r.Summary,
		"has_body":    r.HasBody,
	}
	if r.Description != "" {
		m["description"] = r.Description
	}

	if len(r.PathParams) > 0 {
		params := make([]map[string]any, 0, len(r.PathParams))
		for _, p := range r.PathParams {
			param := map[string]any{
				"name":        p.Name,
				"type":        p.Type,
				"required":    p.Required,
				"description": p.Description,
			}
			if len(p.Schema) > 0 {
				param["schema"] = json.RawMessage(p.Schema)
			}
			params = append(params, param)
		}
		m["path_params"] = params
	}

	if len(r.QueryParams) > 0 {
		qparams := make([]map[string]any, 0, len(r.QueryParams))
		for _, p := range r.QueryParams {
			qparam := map[string]any{
				"name":        p.Name,
				"type":        p.Type,
				"required":    p.Required,
				"description": p.Description,
			}
			if len(p.Schema) > 0 {
				qparam["schema"] = json.RawMessage(p.Schema)
			}
			qparams = append(qparams, qparam)
		}
		m["query_params"] = qparams
	}

	if len(r.RequestBodySchema) > 0 {
		m["request_body_schema"] = json.RawMessage(r.RequestBodySchema)
		m["request_body_required"] = r.RequestBodyRequired
	}
	if len(r.ResponseSchemas) > 0 {
		responseSchemas := make(map[string]json.RawMessage, len(r.ResponseSchemas))
		for status, schema := range r.ResponseSchemas {
			responseSchemas[status] = json.RawMessage(schema)
		}
		m["response_schemas"] = responseSchemas
	}

	// Include example usage.
	m["example"] = buildAPIExample(r)

	return m
}

// buildAPIExample generates a sample cio api command for a route.
func buildAPIExample(r *routes.Route) string {
	var sb strings.Builder
	sb.WriteString("cio api ")
	sb.WriteString(r.Path)

	if len(r.PathParams) > 0 {
		parts := make([]string, 0, len(r.PathParams))
		for _, p := range r.PathParams {
			parts = append(parts, fmt.Sprintf(`"%s": "<value>"`, p.Name))
		}
		sb.WriteString(fmt.Sprintf(` --params '{%s}'`, strings.Join(parts, ", ")))
	}
	if r.HTTPMethod != "GET" {
		sb.WriteString(fmt.Sprintf(` -X %s`, r.HTTPMethod))
	}
	if r.HasBody {
		sb.WriteString(` --json '{...}'`)
	}

	return sb.String()
}

func suggestRoutes(reg *routes.Registry, resource, method string) []routes.Route {
	var suggestions []routes.Route

	// Exact resource, fuzzy method.
	if rs, ok := reg.ByResource[resource]; ok {
		for _, r := range rs {
			if strings.Contains(r.Method, method) || strings.Contains(method, r.Method) {
				suggestions = append(suggestions, r)
			}
		}
	}

	// Fuzzy resource.
	if len(suggestions) == 0 {
		for res := range reg.ByResource {
			if strings.Contains(res, resource) || strings.Contains(resource, res) {
				for _, r := range reg.ByResource[res] {
					suggestions = append(suggestions, r)
				}
			}
		}
	}

	if len(suggestions) > 5 {
		suggestions = suggestions[:5]
	}
	return suggestions
}

// schemaDryRun prints the spec download requests that would be made.
func schemaDryRun(cmd *cobra.Command, baseURL, accessToken string) error {
	if baseURL == "" {
		baseURL = "https://us.fly.customer.io"
	}

	headers := map[string]string{
		"Accept": "application/json",
	}
	if accessToken != "" {
		headers["Authorization"] = "Bearer [REDACTED]"
	}

	var requests []map[string]any
	for _, path := range routes.SpecURLPaths() {
		requests = append(requests, map[string]any{
			"method":  "GET",
			"url":     baseURL + path,
			"headers": headers,
		})
	}

	return schemaOutput(cmd, map[string]any{
		"dry_run":  true,
		"requests": requests,
	})
}

// schemaOutput marshals v to JSON and applies --jq.
func schemaOutput(cmd *cobra.Command, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	jq := GetJQFlag(cmd)
	return output.FprintProcess(cmd.OutOrStdout(), json.RawMessage(data), jq, GetRawFlag(cmd))
}
