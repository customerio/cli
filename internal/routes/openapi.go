package routes

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// OpenAPI 3.1 document types (subset needed for parsing).

type openapiSpec struct {
	OpenAPI    string                     `json:"openapi"`
	Info       openapiInfo                `json:"info"`
	Paths      map[string]openapiPathItem `json:"paths"`
	Components *openapiComponents         `json:"components,omitempty"`
}

type openapiInfo struct {
	Title   string `json:"title"`
	Version string `json:"version"`
}

type openapiComponents struct {
	Schemas   map[string]json.RawMessage `json:"schemas,omitempty"`
	Responses map[string]json.RawMessage `json:"responses,omitempty"`
}

type openapiPathItem map[string]openapiOperation // lowercase method -> operation

type openapiOperation struct {
	Summary     string                     `json:"summary,omitempty"`
	Description string                     `json:"description,omitempty"`
	OperationID string                     `json:"operationId,omitempty"`
	Tags        []string                   `json:"tags,omitempty"`
	Parameters  []openapiParameter         `json:"parameters,omitempty"`
	RequestBody *openapiRequestBody        `json:"requestBody,omitempty"`
	Responses   map[string]json.RawMessage `json:"responses,omitempty"`
}

type openapiParameter struct {
	Name        string          `json:"name"`
	In          string          `json:"in"` // "path", "query", "header"
	Required    bool            `json:"required"`
	Schema      json.RawMessage `json:"schema"`
	Description string          `json:"description,omitempty"`
}

type openapiRequestBody struct {
	Required bool                        `json:"required"`
	Content  map[string]openapiMediaType `json:"content"`
}

type openapiMediaType struct {
	Schema json.RawMessage `json:"schema"`
}

type openapiResponse struct {
	Ref         string                      `json:"$ref,omitempty"`
	Description string                      `json:"description,omitempty"`
	Content     map[string]openapiMediaType `json:"content,omitempty"`
}

// LoadRegistryFromOpenAPI parses an OpenAPI 3.1 JSON spec into a Registry.
// This replaces LoadRegistryFromData for OpenAPI-based specs.
func LoadRegistryFromOpenAPI(data []byte) (*Registry, error) {
	var spec openapiSpec
	if err := json.Unmarshal(data, &spec); err != nil {
		return nil, fmt.Errorf("parsing OpenAPI spec: %w", err)
	}

	if spec.Paths == nil {
		return nil, fmt.Errorf("OpenAPI spec has no paths")
	}

	reg := &Registry{
		ByResource: make(map[string][]Route),
	}

	type routeKey struct {
		resource, method, httpMethod string
	}
	seen := make(map[routeKey]bool)

	for path, pathItem := range spec.Paths {
		for method, op := range pathItem {
			httpMethod := strings.ToUpper(method)

			// Derive resource + CLI method from the path (reuse existing logic).
			// OpenAPI paths use {param}, convert to :param for deriveFromWalkedPath.
			colonPath := openAPIPathToColonPath(path)
			resource, cliMethod := deriveFromWalkedPath(colonPath, httpMethod)
			if resource == "" {
				continue
			}

			key := routeKey{resource, cliMethod, httpMethod}
			if seen[key] {
				continue
			}
			seen[key] = true

			// Extract path params.
			var pathParams []RouteParam
			var queryParams []QueryParam
			for _, p := range op.Parameters {
				schemaRaw := extractResolvedSchema(p.Schema, spec.Components)
				paramType := schemaType(schemaRaw)
				if paramType == "" {
					paramType = "string"
				}
				switch p.In {
				case "path":
					// Infer integer type for _id params.
					if paramType == "string" && (strings.HasSuffix(p.Name, "_id") || p.Name == "id") {
						paramType = "integer"
					}
					pathParams = append(pathParams, RouteParam{
						Name:        p.Name,
						Required:    p.Required,
						Type:        paramType,
						Description: p.Description,
						Schema:      schemaRaw,
					})
				case "query":
					queryParams = append(queryParams, QueryParam{
						Name:        p.Name,
						Type:        paramType,
						Required:    p.Required,
						Description: p.Description,
						Schema:      schemaRaw,
					})
				}
			}

			hasBody := op.RequestBody != nil ||
				httpMethod == "POST" || httpMethod == "PUT" || httpMethod == "PATCH"
			requestBodySchema, requestBodyRequired := extractRequestBodySchema(op.RequestBody, spec.Components)
			responseSchemas := extractResponseSchemas(op.Responses, spec.Components)

			summary := op.Summary
			if summary == "" {
				summary = fmt.Sprintf("%s %s", httpMethod, path)
			}

			route := Route{
				Resource:            resource,
				Method:              cliMethod,
				HTTPMethod:          httpMethod,
				Path:                path, // Already in {param} format.
				Summary:             summary,
				Description:         op.Description,
				PathParams:          pathParams,
				QueryParams:         queryParams,
				HasBody:             hasBody,
				RequestBodySchema:   requestBodySchema,
				RequestBodyRequired: requestBodyRequired,
				ResponseSchemas:     responseSchemas,
			}

			reg.Routes = append(reg.Routes, route)
		}
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

// openAPIPathToColonPath converts {param} to :param for compatibility with
// deriveFromWalkedPath.
func openAPIPathToColonPath(path string) string {
	// Replace {param_name} with :param_name
	result := path
	for {
		start := strings.Index(result, "{")
		if start == -1 {
			break
		}
		end := strings.Index(result[start:], "}")
		if end == -1 {
			break
		}
		param := result[start+1 : start+end]
		result = result[:start] + ":" + param + result[start+end+1:]
	}
	return result
}

// IsOpenAPISpec returns true if the data looks like an OpenAPI spec (has "openapi" key).
func IsOpenAPISpec(data []byte) bool {
	var probe struct {
		OpenAPI string `json:"openapi"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return false
	}
	return probe.OpenAPI != ""
}

func extractResolvedSchema(raw json.RawMessage, components *openapiComponents) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	resolved, err := resolveSchemaRaw(raw, components, map[string]bool{})
	if err != nil {
		return raw
	}
	normalized, err := json.Marshal(resolved)
	if err != nil {
		return raw
	}
	return normalized
}

func extractRequestBodySchema(body *openapiRequestBody, components *openapiComponents) (json.RawMessage, bool) {
	if body == nil {
		return nil, false
	}

	var schemaRaw json.RawMessage
	if mt, ok := body.Content["application/json"]; ok && len(mt.Schema) > 0 {
		schemaRaw = mt.Schema
	} else {
		for _, mt := range body.Content {
			if len(mt.Schema) > 0 {
				schemaRaw = mt.Schema
				break
			}
		}
	}
	if len(schemaRaw) == 0 {
		return nil, body.Required
	}

	return extractResolvedSchema(schemaRaw, components), body.Required
}

func extractResponseSchemas(responses map[string]json.RawMessage, components *openapiComponents) map[string]json.RawMessage {
	if len(responses) == 0 {
		return nil
	}
	out := make(map[string]json.RawMessage)
	for status, raw := range responses {
		resp := resolveResponse(raw, components)
		if len(resp.Content) == 0 {
			continue
		}
		var schemaRaw json.RawMessage
		if mt, ok := resp.Content["application/json"]; ok && len(mt.Schema) > 0 {
			schemaRaw = mt.Schema
		} else {
			for _, mt := range resp.Content {
				if len(mt.Schema) > 0 {
					schemaRaw = mt.Schema
					break
				}
			}
		}
		if len(schemaRaw) == 0 {
			continue
		}
		out[status] = extractResolvedSchema(schemaRaw, components)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func resolveResponse(raw json.RawMessage, components *openapiComponents) openapiResponse {
	var resp openapiResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return openapiResponse{}
	}
	if resp.Ref == "" {
		return resp
	}
	name, ok := strings.CutPrefix(resp.Ref, "#/components/responses/")
	if !ok || components == nil || components.Responses == nil {
		return resp
	}
	targetRaw, ok := components.Responses[name]
	if !ok {
		return resp
	}
	var resolved openapiResponse
	if err := json.Unmarshal(targetRaw, &resolved); err != nil {
		return resp
	}
	return resolved
}

func schemaType(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var v map[string]any
	if err := json.Unmarshal(raw, &v); err != nil {
		return ""
	}
	if t, ok := v["type"].(string); ok {
		return t
	}
	return ""
}

func resolveSchemaRaw(raw json.RawMessage, components *openapiComponents, seen map[string]bool) (any, error) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	return resolveSchemaValue(value, components, seen)
}

func resolveSchemaValue(value any, components *openapiComponents, seen map[string]bool) (any, error) {
	switch v := value.(type) {
	case map[string]any:
		if ref, ok := v["$ref"].(string); ok {
			return resolveSchemaRef(ref, v, components, seen)
		}
		for _, key := range []string{"properties", "items", "additionalProperties", "allOf", "anyOf", "oneOf", "not"} {
			resolved, ok, err := resolveSchemaField(v[key], components, seen)
			if err != nil {
				return nil, err
			}
			if ok {
				v[key] = resolved
			}
		}
		return v, nil
	case []any:
		resolved := make([]any, 0, len(v))
		for _, item := range v {
			next, err := resolveSchemaValue(item, components, seen)
			if err != nil {
				return nil, err
			}
			resolved = append(resolved, next)
		}
		return resolved, nil
	default:
		return value, nil
	}
}

func resolveSchemaField(value any, components *openapiComponents, seen map[string]bool) (any, bool, error) {
	if value == nil {
		return nil, false, nil
	}
	resolved, err := resolveSchemaValue(value, components, seen)
	if err != nil {
		return nil, false, err
	}
	return resolved, true, nil
}

func resolveSchemaRef(ref string, schema map[string]any, components *openapiComponents, seen map[string]bool) (any, error) {
	name, ok := strings.CutPrefix(ref, "#/components/schemas/")
	if !ok || components == nil || components.Schemas == nil {
		return schema, nil
	}
	if seen[name] {
		return schema, nil
	}
	targetRaw, ok := components.Schemas[name]
	if !ok {
		return schema, nil
	}

	seen[name] = true
	resolved, err := resolveSchemaRaw(targetRaw, components, seen)
	delete(seen, name)
	if err != nil {
		return nil, err
	}

	resolvedMap, ok := resolved.(map[string]any)
	if !ok {
		return resolved, nil
	}

	merged := make(map[string]any, len(resolvedMap)+len(schema))
	for k, v := range resolvedMap {
		merged[k] = v
	}
	for k, v := range schema {
		if k == "$ref" {
			continue
		}
		merged[k] = v
	}
	return merged, nil
}
