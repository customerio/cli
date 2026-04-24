package routes

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

//go:embed enrichment.json
var embeddedEnrichmentJSON []byte

// enrichmentEntry holds enrichment data extracted from skill files.
type enrichmentEntry struct {
	Summary           string            `json:"summary"`
	ParamDescriptions map[string]string `json:"param_descriptions"`
	QueryParams       []QueryParam      `json:"query_params"`
}

// walkedRoute matches the JSON structure output by the route walker.
type walkedRoute struct {
	Method string `json:"method"`
	Path   string `json:"path"`
}

// colonParamRegex matches :param_name segments in httprouter paths.
var colonParamRegex = regexp.MustCompile(`:(\w+)`)

// LoadRegistry downloads (or reads cached) OpenAPI specs and parses them into a Registry.
// Enrichment data is applied to fill in any missing summaries, param descriptions, and query params.
func LoadRegistry(opts LoadRegistryOptions) (*Registry, error) {
	ctx := context.Background()
	journeysData, cdpData, err := EnsureSpecs(ctx, opts)
	if err != nil {
		return nil, err
	}

	var reg *Registry
	if IsOpenAPISpec(journeysData) {
		reg, err = LoadRegistryFromOpenAPI(journeysData)
	} else {
		reg, err = LoadRegistryFromData(journeysData)
	}
	if err != nil {
		return nil, err
	}

	// Merge CDP Pipelines routes if available.
	if len(cdpData) > 0 {
		cdpReg, cdpErr := LoadRegistryFromOpenAPI(cdpData)
		if cdpErr == nil {
			mergeRoutes(reg, cdpReg)
		}
	}

	applyEnrichment(reg, embeddedEnrichmentJSON)
	return reg, nil
}

// mergeRoutes appends all routes from src into dst, rebuilds the index, and re-sorts.
func mergeRoutes(dst, src *Registry) {
	dst.Routes = append(dst.Routes, src.Routes...)

	sort.Slice(dst.Routes, func(i, j int) bool {
		if dst.Routes[i].Resource != dst.Routes[j].Resource {
			return dst.Routes[i].Resource < dst.Routes[j].Resource
		}
		return dst.Routes[i].Method < dst.Routes[j].Method
	})

	dst.ByResource = make(map[string][]Route)
	for i := range dst.Routes {
		r := &dst.Routes[i]
		dst.ByResource[r.Resource] = append(dst.ByResource[r.Resource], *r)
	}
}

// LoadRegistryFromData parses walked routes JSON into a Registry. Exported for testing.
func LoadRegistryFromData(data []byte) (*Registry, error) {
	var walked []walkedRoute
	if err := json.Unmarshal(data, &walked); err != nil {
		return nil, fmt.Errorf("parsing walked routes JSON: %w", err)
	}

	reg := &Registry{
		ByResource: make(map[string][]Route),
	}

	type routeKey struct {
		resource, method, httpMethod string
	}
	seen := make(map[routeKey]bool)

	for _, wr := range walked {
		resource, method := deriveFromWalkedPath(wr.Path, wr.Method)
		if resource == "" {
			continue
		}

		key := routeKey{resource, method, wr.Method}
		if seen[key] {
			continue
		}
		seen[key] = true

		// Normalize :param to {param} for URL substitution.
		normalizedPath := colonParamRegex.ReplaceAllString(wr.Path, "{${1}}")

		var pathParams []RouteParam
		for _, match := range colonParamRegex.FindAllStringSubmatch(wr.Path, -1) {
			pathParams = append(pathParams, RouteParam{
				Name:     match[1],
				Required: true,
				Type:     "string",
			})
		}

		route := Route{
			Resource:   resource,
			Method:     method,
			HTTPMethod: wr.Method,
			Path:       normalizedPath,
			PathParams: pathParams,
			HasBody:    wr.Method == "POST" || wr.Method == "PUT" || wr.Method == "PATCH",
			Summary:    fmt.Sprintf("%s %s", wr.Method, wr.Path),
		}

		reg.Routes = append(reg.Routes, route)
	}

	sort.Slice(reg.Routes, func(i, j int) bool {
		if reg.Routes[i].Resource != reg.Routes[j].Resource {
			return reg.Routes[i].Resource < reg.Routes[j].Resource
		}
		return reg.Routes[i].Method < reg.Routes[j].Method
	})

	for i := range reg.Routes {
		r := &reg.Routes[i]
		reg.ByResource[r.Resource] = append(reg.ByResource[r.Resource], *r)
	}

	return reg, nil
}

// deriveFromWalkedPath extracts the CLI resource name and method name from an
// httprouter-style path.
//
// Examples:
//
//	GET  /v1/environments/:eid/campaigns              → campaigns, list
//	POST /v1/environments/:eid/campaigns              → campaigns, create
//	GET  /v1/environments/:eid/campaigns/:cid         → campaigns, get
//	PUT  /v1/environments/:eid/campaigns/:cid         → campaigns, update
//	DELETE /v1/environments/:eid/campaigns/:cid       → campaigns, delete
//	POST /v1/environments/:eid/campaigns/:cid/duplicate → campaigns, duplicate
//	GET  /v1/environments/:eid/campaigns/:cid/tags    → campaigns, list-tags
//	GET  /v1/accounts/:aid/billing/plans              → billing, list-plans
func deriveFromWalkedPath(path, httpMethod string) (resource, method string) {
	stripped := stripScopePrefix(path)
	if stripped == "" {
		return "", ""
	}

	// Handle scope root paths like /v1/accounts/:account_id (path ends with :param).
	// Only treat as scope root if the original path actually ends with a param segment.
	scopeRoots := map[string]bool{"accounts": true, "environments": true, "workspaces": true}
	if scopeRoots[stripped] {
		parts := strings.Split(strings.Trim(path, "/"), "/")
		lastPart := parts[len(parts)-1]
		if strings.HasPrefix(lastPart, ":") {
			return stripped, deriveSimpleMethod(httpMethod, true)
		}
	}

	segments := strings.Split(strings.Trim(stripped, "/"), "/")
	if len(segments) == 0 {
		return "", ""
	}

	var nouns []string
	endsWithParam := false
	for _, seg := range segments {
		if strings.HasPrefix(seg, ":") || (strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}")) {
			endsWithParam = true
		} else {
			nouns = append(nouns, seg)
			endsWithParam = false
		}
	}

	if len(nouns) == 0 {
		return "", ""
	}

	resource = nouns[0]

	if len(nouns) == 1 {
		method = deriveSimpleMethod(httpMethod, endsWithParam)
	} else {
		subPath := strings.Join(nouns[1:], "-")
		method = deriveSubResourceMethod(httpMethod, subPath, endsWithParam)
	}

	return resource, method
}

func deriveSimpleMethod(httpMethod string, endsWithParam bool) string {
	switch httpMethod {
	case "GET":
		if endsWithParam {
			return "get"
		}
		return "list"
	case "POST":
		return "create"
	case "PUT":
		if endsWithParam {
			return "update"
		}
		return "update"
	case "DELETE":
		return "delete"
	case "PATCH":
		return "patch"
	default:
		return strings.ToLower(httpMethod)
	}
}

func deriveSubResourceMethod(httpMethod, subPath string, endsWithParam bool) string {
	switch httpMethod {
	case "GET":
		if endsWithParam {
			return "get-" + subPath
		}
		return "list-" + subPath
	case "POST":
		if !endsWithParam && !strings.Contains(subPath, "-") && isLikelyAction(subPath) {
			return subPath
		}
		return "create-" + subPath
	case "PUT":
		return "update-" + subPath
	case "DELETE":
		return "delete-" + subPath
	case "PATCH":
		return "patch-" + subPath
	default:
		return strings.ToLower(httpMethod) + "-" + subPath
	}
}

// isLikelyAction returns true if a path segment looks like a verb/action.
func isLikelyAction(segment string) bool {
	actions := map[string]bool{
		"activate": true, "archive": true, "cancel": true, "capture": true,
		"deactivate": true, "disable": true, "duplicate": true, "enable": true,
		"export": true, "import": true, "merge": true, "migrate": true,
		"pause": true, "populate": true, "reactivate": true, "refresh": true,
		"reset": true, "restart": true, "retry": true, "revoke": true,
		"scan": true, "send": true, "start": true, "stop": true,
		"subscribe": true, "sync": true, "toggle": true, "track": true,
		"trigger": true, "unsubscribe": true, "validate": true, "verify": true,
		"enrich": true, "restore": true, "test": true,
	}
	return actions[segment]
}

func stripScopePrefix(path string) string {
	prefixes := []string{
		"/v1/environments/",
		"/v1/accounts/",
		"/v1/workspaces/",
		"/cdp/api/workspaces/",
		"/cdp/api/accounts/",
	}

	for _, prefix := range prefixes {
		if !strings.HasPrefix(path, prefix) {
			continue
		}

		rest := strings.TrimPrefix(path, prefix)
		idx := strings.Index(rest, "/")
		if idx == -1 {
			return scopeResource(prefix)
		}
		return rest[idx+1:]
	}

	// Top-level /cdp/api/ routes (no account/workspace scope).
	if strings.HasPrefix(path, "/cdp/api/") {
		return strings.TrimPrefix(path, "/cdp/api/")
	}

	return ""
}

func scopeResource(prefix string) string {
	switch {
	case strings.Contains(prefix, "environments"):
		return "environments"
	case strings.Contains(prefix, "accounts"):
		return "accounts"
	case strings.Contains(prefix, "workspaces"):
		return "workspaces"
	default:
		return ""
	}
}

// isAutoSummary returns true if the summary looks auto-generated
// (e.g. "GET /v1/environments/{environment_id}/campaigns") rather than
// a human-written description.
func isAutoSummary(summary, httpMethod, path string) bool {
	return summary == "" || summary == httpMethod+" "+path
}

// applyEnrichment merges enrichment data (summaries, param descriptions,
// query params) into routes. Enrichment is keyed by "METHOD /path/template".
// It only fills gaps — if the route already has a real summary (e.g. from an
// OpenAPI spec annotation), the enrichment summary is not applied.
func applyEnrichment(reg *Registry, data []byte) {
	if len(data) == 0 {
		return
	}

	var enrichment map[string]enrichmentEntry
	if err := json.Unmarshal(data, &enrichment); err != nil {
		return
	}

	for i := range reg.Routes {
		r := &reg.Routes[i]
		key := r.HTTPMethod + " " + r.Path
		e, ok := enrichment[key]
		if !ok {
			continue
		}

		// Only apply enrichment summary if the route doesn't already
		// have a real one (e.g. from an OpenAPI WithSummary annotation).
		if e.Summary != "" && isAutoSummary(r.Summary, r.HTTPMethod, r.Path) {
			r.Summary = e.Summary
		}

		// Apply param descriptions only to params that don't already have one.
		if len(e.ParamDescriptions) > 0 {
			for j := range r.PathParams {
				if r.PathParams[j].Description != "" {
					continue
				}
				if desc, ok := e.ParamDescriptions[r.PathParams[j].Name]; ok {
					r.PathParams[j].Description = desc
					// Infer type from description/name
					if r.PathParams[j].Type == "string" {
						if strings.HasSuffix(r.PathParams[j].Name, "_id") || r.PathParams[j].Name == "id" {
							r.PathParams[j].Type = "integer"
						}
					}
				}
			}
		}

		// Apply query params only if the route doesn't already have them.
		if len(e.QueryParams) > 0 && len(r.QueryParams) == 0 {
			r.QueryParams = e.QueryParams
		}
	}

	// Rebuild ByResource index
	reg.ByResource = make(map[string][]Route)
	for i := range reg.Routes {
		r := &reg.Routes[i]
		reg.ByResource[r.Resource] = append(reg.ByResource[r.Resource], *r)
	}
}
