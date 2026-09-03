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
                                            — show all methods for a path

Add --compact to render endpoint detail as one line per field (dotted paths,
"[]" for arrays, "?" for optional) instead of full JSON Schema.`,
	Args: cobra.MaximumNArgs(2),
	RunE: runSchema,
}

func init() {
	schemaCmd.Flags().Bool("refresh", false, "Force re-download of API specs")
	schemaCmd.Flags().Bool("compact", false, "Render endpoint detail as flattened one-line-per-field text instead of full JSON Schema")
	rootCmd.AddCommand(schemaCmd)
}

func runSchema(cmd *cobra.Command, args []string) error {
	refresh, _ := cmd.Flags().GetBool("refresh")
	compact, _ := cmd.Flags().GetBool("compact")

	opts := specLoadOptions(cmd, clientFromCmd(cmd))
	opts.ForceRefresh = refresh

	if GetDryRun(cmd) {
		return schemaDryRun(cmd, opts.BaseURL, opts.AccessToken)
	}

	reg, err := routes.LoadRegistry(opts)
	if err != nil {
		output.PrintError(output.CodeGeneralError, fmt.Sprintf("failed to load routes: %v", err), nil)
		return err
	}

	q := parseSchemaArgs(args)
	switch q.kind {
	case schemaQueryResources:
		return schemaOutput(cmd, listEndpoints(reg))
	case schemaQueryPath:
		return schemaForPath(cmd, reg, q.a, compact)
	case schemaQueryResourceMethod:
		return schemaForResourceMethod(cmd, reg, q.a, q.b, compact)
	case schemaQueryResource:
		return schemaForResource(cmd, reg, q.a)
	case schemaQueryHTTPEndpoint:
		return schemaForHTTPEndpoint(cmd, reg, q.a, q.b, compact)
	case schemaQuerySpacedResourceMethod:
		return schemaForSpacedResourceMethod(cmd, reg, q.a, q.b)
	default:
		// Unreachable while Args is MaximumNArgs(2); still emits the structured
		// error every other path does, so relaxing that validator can't silently
		// break the JSON contract agents parse.
		err := fmt.Errorf("expected at most 2 arguments, got %d", len(args))
		output.PrintError(output.CodeValidationError, err.Error(), nil)
		return err
	}
}

type schemaQueryKind int

const (
	schemaQueryInvalid schemaQueryKind = iota
	schemaQueryResources
	schemaQueryResource
	schemaQueryResourceMethod
	schemaQueryPath
	schemaQueryHTTPEndpoint
	schemaQuerySpacedResourceMethod
)

type schemaQuery struct {
	kind schemaQueryKind
	a, b string
}

// httpMethods are the verbs the "METHOD /path" form accepts. No resource is named
// after one, so a first argument that matches is unambiguously that form.
var httpMethods = map[string]bool{
	"GET": true, "POST": true, "PUT": true, "PATCH": true,
	"DELETE": true, "HEAD": true, "OPTIONS": true,
}

// parseSchemaArgs interprets the documented argument forms. Two args normally mean
// "METHOD /path"; `schema campaigns update` can only be `campaigns.update` typed
// with a space, which gets its own kind so the caller can name the dotted form
// instead of reporting the resource as an unknown HTTP method. Agents hit that
// error, read it as "the endpoint does not exist", and go looking for another way
// to do the thing.
//
// The first argument decides: a verb keeps the "METHOD /path" reading even when the
// path is malformed, so `schema DELETE segments` (a path missing its leading slash)
// still reports an unknown endpoint rather than being read as a resource named
// "DELETE".
func parseSchemaArgs(args []string) schemaQuery {
	switch len(args) {
	case 0:
		return schemaQuery{kind: schemaQueryResources}

	case 1:
		arg := args[0]
		if strings.HasPrefix(arg, "/") {
			return schemaQuery{kind: schemaQueryPath, a: arg}
		}
		if parts := strings.SplitN(arg, ".", 2); len(parts) == 2 {
			return schemaQuery{kind: schemaQueryResourceMethod, a: parts[0], b: parts[1]}
		}
		return schemaQuery{kind: schemaQueryResource, a: arg}

	case 2:
		method := strings.ToUpper(args[0])
		if !httpMethods[method] && !strings.HasPrefix(args[1], "/") {
			return schemaQuery{kind: schemaQuerySpacedResourceMethod, a: args[0], b: args[1]}
		}
		return schemaQuery{kind: schemaQueryHTTPEndpoint, a: method, b: args[1]}

	default:
		return schemaQuery{kind: schemaQueryInvalid}
	}
}

// schemaForSpacedResourceMethod rejects `schema <resource> <method>` and names the
// dotted form. The separator is one character away from correct, so the old
// "unknown endpoint: CAMPAIGNS update" read as "no such endpoint" and sent callers
// looking elsewhere. Suggesting the fix rather than accepting the spelling keeps one
// documented form, matching schemaForResourceMethod's reject-and-suggest shape.
func schemaForSpacedResourceMethod(cmd *cobra.Command, reg *routes.Registry, resource, method string) error {
	dotted := resource + "." + method
	msg := fmt.Sprintf("%q is not an argument form: use \"<resource>.<method>\" or \"METHOD /path\"", resource+" "+method)

	if reg.FindRoute(resource, method) != nil {
		msg += "\n\nDid you mean:\n  cio schema " + dotted
	} else if suggestions := suggestRoutes(reg, resource, method); len(suggestions) > 0 {
		msg += "\n\nDid you mean:"
		for _, s := range suggestions {
			msg += fmt.Sprintf("\n  cio schema %s.%s", s.Resource, s.Method)
		}
	}

	output.PrintError(output.CodeValidationError, msg, map[string]any{"dotted_form": dotted})
	return fmt.Errorf("%s", msg)
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
func schemaForResourceMethod(cmd *cobra.Command, reg *routes.Registry, resource, method string, compact bool) error {
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

	return schemaOutput(cmd, routeDetail(route, compact))
}

// schemaForPath shows all methods for a given path.
func schemaForPath(cmd *cobra.Command, reg *routes.Registry, path string, compact bool) error {
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
		result = append(result, routeDetail(&r, compact))
	}
	return schemaOutput(cmd, result)
}

// schemaForHTTPEndpoint shows schema for a specific METHOD + path.
func schemaForHTTPEndpoint(cmd *cobra.Command, reg *routes.Registry, method, path string, compact bool) error {
	for _, r := range reg.Routes {
		if r.HTTPMethod == method && r.Path == path {
			return schemaOutput(cmd, routeDetail(&r, compact))
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

// compactSkippedReason explains a body that came back as JSON Schema despite
// --compact. Saying so matters more than the fallback itself: silently handing
// back a different shape than the flag asked for reads as the flag not working.
const compactSkippedReason = "flattening was larger than the schema itself, so the schema is returned instead"

// compactIfSmaller renders flattened lines when compact is requested and the
// result is actually smaller than the schema it replaces.
//
// Flattening enumerates one line per leaf path, so a schema whose components
// are shared across many branches can flatten to several times its own size:
// an endpoint embedding a third-party type resolved to 18MB and flattened to
// 54MB. Compact is a size optimisation, so it should never lose to the thing it
// optimises. This is decided per body, not per endpoint, so a small request
// still renders compact next to a pathological response.
//
// The flattening happens here rather than at the call site so it cannot run on
// the default path, where the result would be discarded: a route can carry a
// body and several response schemas, and schemaForPath renders every method on
// a path.
func compactIfSmaller(compact bool, schema json.RawMessage, rootRequired bool) ([]string, bool) {
	if !compact {
		return nil, false
	}

	lines := compactLines(flattenSchemaRoot(schema, rootRequired))
	total := 0
	for _, l := range lines {
		total += len(l)
	}
	if total >= len(schema) {
		return nil, false
	}

	return lines, true
}

// routeDetail returns the schema view of a route. When compact is true, params
// and bodies render as flattened field lines instead of full JSON Schema.
func routeDetail(r *routes.Route, compact bool) map[string]any {
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
		if compact {
			m["path_params"] = compactPathParamLines(r.PathParams)
		} else {
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
	}

	if len(r.QueryParams) > 0 {
		if compact {
			m["query_params"] = compactQueryParamLines(r.QueryParams)
		} else {
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
	}

	if len(r.RequestBodySchema) > 0 {
		lines, ok := compactIfSmaller(compact, r.RequestBodySchema, r.RequestBodyRequired)
		switch {
		case ok:
			m["request_body"] = lines
		case compact:
			m["request_body_schema"] = json.RawMessage(r.RequestBodySchema)
			m["compact_skipped"] = compactSkippedReason
		default:
			m["request_body_schema"] = json.RawMessage(r.RequestBodySchema)
		}
		m["request_body_required"] = r.RequestBodyRequired
	}
	if len(r.ResponseSchemas) > 0 {
		responses := make(map[string][]string, len(r.ResponseSchemas))
		schemas := make(map[string]json.RawMessage, len(r.ResponseSchemas))
		for status, schema := range r.ResponseSchemas {
			if lines, ok := compactIfSmaller(compact, schema, true); ok {
				responses[status] = lines
				continue
			}
			schemas[status] = json.RawMessage(schema)
			if compact {
				m["compact_skipped"] = compactSkippedReason
			}
		}
		if len(responses) > 0 {
			m["responses"] = responses
		}
		if len(schemas) > 0 {
			m["response_schemas"] = schemas
		}
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
