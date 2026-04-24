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
	})

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
