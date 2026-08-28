package cmd

import (
	"encoding/json"
	"testing"

	"github.com/customerio/cli/internal/routes"
)

func TestRouteDetailIncludesRequestBodySchema(t *testing.T) {
	detail := routeDetail(&routes.Route{
		Resource:            "campaigns",
		Method:              "create",
		HTTPMethod:          "POST",
		Path:                "/v1/environments/{environment_id}/campaigns",
		Summary:             "Create campaign",
		Description:         "Create a campaign in the workspace.",
		HasBody:             true,
		RequestBodyRequired: true,
		PathParams: []routes.RouteParam{
			{
				Name:        "environment_id",
				Type:        "integer",
				Required:    true,
				Description: "Workspace ID",
				Schema:      json.RawMessage(`{"type":"integer","minimum":1}`),
			},
		},
		QueryParams: []routes.QueryParam{
			{
				Name:        "state",
				Type:        "string",
				Required:    false,
				Description: "Campaign state",
				Schema:      json.RawMessage(`{"type":"string","enum":["draft","active"]}`),
			},
		},
		RequestBodySchema: json.RawMessage(`{
			"type": "object",
			"required": ["campaign"],
			"properties": {
				"campaign": {
					"type": "object"
				}
			}
		}`),
		ResponseSchemas: map[string]json.RawMessage{
			"200": json.RawMessage(`{"type":"object","properties":{"id":{"type":"integer"}}}`),
		},
	}, false)

	if got := detail["description"]; got != "Create a campaign in the workspace." {
		t.Fatalf("expected description to be preserved, got %v", got)
	}

	if got, ok := detail["request_body_required"].(bool); !ok || !got {
		t.Fatalf("expected request_body_required=true, got %v", detail["request_body_required"])
	}

	schema, ok := detail["request_body_schema"].(json.RawMessage)
	if !ok {
		t.Fatalf("expected request_body_schema to be json.RawMessage, got %T", detail["request_body_schema"])
	}

	var parsed map[string]any
	if err := json.Unmarshal(schema, &parsed); err != nil {
		t.Fatalf("unmarshal request_body_schema: %v", err)
	}
	if parsed["type"] != "object" {
		t.Fatalf("expected schema type=object, got %v", parsed["type"])
	}

	pathParams := detail["path_params"].([]map[string]any)
	pathParamSchema := pathParams[0]["schema"].(json.RawMessage)
	var parsedPathParamSchema map[string]any
	if err := json.Unmarshal(pathParamSchema, &parsedPathParamSchema); err != nil {
		t.Fatalf("unmarshal path param schema: %v", err)
	}
	if parsedPathParamSchema["minimum"] != float64(1) {
		t.Fatalf("expected path param minimum=1, got %v", parsedPathParamSchema["minimum"])
	}

	queryParams := detail["query_params"].([]map[string]any)
	queryParamSchema := queryParams[0]["schema"].(json.RawMessage)
	var parsedQueryParamSchema map[string]any
	if err := json.Unmarshal(queryParamSchema, &parsedQueryParamSchema); err != nil {
		t.Fatalf("unmarshal query param schema: %v", err)
	}
	if len(parsedQueryParamSchema["enum"].([]any)) != 2 {
		t.Fatalf("expected query param enum values, got %v", parsedQueryParamSchema["enum"])
	}

	responseSchemas := detail["response_schemas"].(map[string]json.RawMessage)
	var parsedResponseSchema map[string]any
	if err := json.Unmarshal(responseSchemas["200"], &parsedResponseSchema); err != nil {
		t.Fatalf("unmarshal response schema: %v", err)
	}
	if parsedResponseSchema["type"] != "object" {
		t.Fatalf("expected response schema type=object, got %v", parsedResponseSchema["type"])
	}
}

func TestParseSchemaArgs(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want schemaQuery
	}{
		{"no args", nil, schemaQuery{kind: schemaQueryResources}},
		{"resource", []string{"campaigns"}, schemaQuery{kind: schemaQueryResource, a: "campaigns"}},
		{"dotted method", []string{"campaigns.update"}, schemaQuery{kind: schemaQueryResourceMethod, a: "campaigns", b: "update"}},
		{"path", []string{"/v1/environments/{environment_id}/campaigns"}, schemaQuery{kind: schemaQueryPath, a: "/v1/environments/{environment_id}/campaigns"}},
		{"http endpoint", []string{"get", "/v1/environments/{environment_id}/campaigns"}, schemaQuery{kind: schemaQueryHTTPEndpoint, a: "GET", b: "/v1/environments/{environment_id}/campaigns"}},
		// A second arg that isn't a path can only be the dotted form typed with a
		// space; reading it as an HTTP method produces "unknown endpoint: CAMPAIGNS
		// update", which looks like the endpoint doesn't exist.
		// Not accepted as a second spelling: it gets its own kind so the caller can
		// reject it and name `campaigns.update`, keeping one documented form.
		{"space instead of dot", []string{"campaigns", "update"}, schemaQuery{kind: schemaQuerySpacedResourceMethod, a: "campaigns", b: "update"}},
		// A verb keeps the "METHOD /path" reading even when the path is malformed:
		// otherwise a slash-less path gets fuzzy-matched as a resource name and the
		// error points somewhere unrelated instead of at the missing slash.
		{"verb with slashless path", []string{"DELETE", "segments"}, schemaQuery{kind: schemaQueryHTTPEndpoint, a: "DELETE", b: "segments"}},
		{"lowercase verb with slashless path", []string{"delete", "segments"}, schemaQuery{kind: schemaQueryHTTPEndpoint, a: "DELETE", b: "segments"}},
		// Cobra's MaximumNArgs(2) rejects this before RunE, but parseSchemaArgs is a
		// pure function with its own contract — pinned so it stays total.
		{"too many args", []string{"a", "b", "c"}, schemaQuery{kind: schemaQueryInvalid}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseSchemaArgs(tc.args); got != tc.want {
				t.Errorf("parseSchemaArgs(%q) = %+v, want %+v", tc.args, got, tc.want)
			}
		})
	}
}
