// Package routes provides a route registry for the Customer.io UI API.
// Routes are loaded from OpenAPI specs and normalized for CLI consumption.
// The CLI builds its command tree dynamically from this registry — zero
// per-endpoint Go code for most routes.
package routes

import (
	"encoding/json"
	"sort"
)

// Route is a fully resolved API endpoint ready for CLI consumption.
type Route struct {
	// Resource is the top-level resource name, e.g. "campaigns", "segments"
	Resource string
	// Method is the CLI method name, e.g. "list", "get", "create"
	Method string
	// HTTPMethod is GET, POST, PUT, DELETE, PATCH
	HTTPMethod string
	// Path is the URL path template, e.g. "/v1/environments/{environment_id}/campaigns"
	Path string
	// Summary is the short description (auto-generated or from enrichment)
	Summary string
	// Description is the long-form operation description from OpenAPI, when available.
	Description string
	// PathParams are parameters embedded in the URL path
	PathParams []RouteParam
	// QueryParams are URL query parameters (from enrichment data)
	QueryParams []QueryParam
	// HasBody indicates whether this route accepts a JSON request body
	HasBody bool
	// RequestBodySchema is the resolved JSON schema for the request body, when available.
	RequestBodySchema json.RawMessage
	// RequestBodyRequired indicates whether the request body itself is required.
	RequestBodyRequired bool
	// ResponseSchemas maps HTTP status code to the resolved JSON schema for that response, when available.
	ResponseSchemas map[string]json.RawMessage
}

// RouteParam describes a single path parameter.
type RouteParam struct {
	Name        string
	Required    bool
	Type        string // "string", "integer", etc.
	Description string
	Schema      json.RawMessage
}

// QueryParam describes a URL query parameter.
type QueryParam struct {
	Name        string
	Type        string
	Description string
	Required    bool
	Schema      json.RawMessage
}

// Registry holds all parsed routes grouped by resource.
type Registry struct {
	Routes     []Route
	ByResource map[string][]Route
}

// Resources returns a sorted list of all resource names.
func (reg *Registry) Resources() []string {
	var names []string
	for name := range reg.ByResource {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Methods returns a sorted list of method names for a given resource.
func (reg *Registry) Methods(resource string) []string {
	routes := reg.ByResource[resource]
	seen := make(map[string]bool)
	var methods []string
	for _, r := range routes {
		if !seen[r.Method] {
			seen[r.Method] = true
			methods = append(methods, r.Method)
		}
	}
	sort.Strings(methods)
	return methods
}

// FindRoute looks up a specific route by resource and method.
func (reg *Registry) FindRoute(resource, method string) *Route {
	for i := range reg.Routes {
		if reg.Routes[i].Resource == resource && reg.Routes[i].Method == method {
			return &reg.Routes[i]
		}
	}
	return nil
}
