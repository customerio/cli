package routes

import (
	"encoding/json"
	"testing"
)

func TestLoadRegistryFromOpenAPI(t *testing.T) {
	spec := `{
		"openapi": "3.1.0",
		"info": {"title": "Test", "version": "1.0.0"},
		"components": {
			"schemas": {
				"CampaignCreateRequest": {
					"type": "object",
					"required": ["campaign"],
					"properties": {
						"campaign": {
							"type": "object",
							"required": ["name", "type"],
							"properties": {
								"name": {"type": "string"},
								"type": {"type": "string"}
							}
						}
					}
				}
			}
		},
		"paths": {
			"/v1/environments/{environment_id}/campaigns": {
				"get": {
					"summary": "List campaigns",
					"description": "Return campaigns for a workspace.",
					"tags": ["campaigns"],
					"parameters": [
						{"name": "environment_id", "in": "path", "required": true, "schema": {"type": "string"}},
						{"name": "page", "in": "query", "required": false, "schema": {"type": "integer", "default": 1, "minimum": 1}, "description": "Page number"},
						{"name": "state", "in": "query", "required": false, "schema": {"type": "string", "enum": ["draft", "active"]}, "description": "Campaign state"}
					],
					"responses": {
						"200": {
							"description": "Campaign list response",
							"content": {
								"application/json": {
									"schema": {
										"type": "object",
										"properties": {
											"campaigns": {
												"type": "array",
												"items": {"type": "object"}
											}
										}
									}
								}
							}
						}
					}
				},
				"post": {
					"summary": "Create campaign",
					"tags": ["campaigns"],
					"parameters": [
						{"name": "environment_id", "in": "path", "required": true, "schema": {"type": "string"}}
					],
					"requestBody": {
						"required": true,
						"content": {"application/json": {"schema": {"$ref": "#/components/schemas/CampaignCreateRequest"}}}
					}
				}
			},
			"/v1/environments/{environment_id}/campaigns/{campaign_id}": {
				"get": {
					"summary": "Get campaign",
					"tags": ["campaigns"],
					"parameters": [
						{"name": "environment_id", "in": "path", "required": true, "schema": {"type": "string"}},
						{"name": "campaign_id", "in": "path", "required": true, "schema": {"type": "string"}}
					]
				},
				"put": {
					"summary": "Update campaign",
					"tags": ["campaigns"],
					"parameters": [
						{"name": "environment_id", "in": "path", "required": true, "schema": {"type": "string"}},
						{"name": "campaign_id", "in": "path", "required": true, "schema": {"type": "string"}}
					]
				},
				"delete": {
					"summary": "Delete campaign",
					"tags": ["campaigns"],
					"parameters": [
						{"name": "environment_id", "in": "path", "required": true, "schema": {"type": "string"}},
						{"name": "campaign_id", "in": "path", "required": true, "schema": {"type": "string"}}
					]
				}
			},
			"/v1/accounts/{account_id}/users": {
				"get": {
					"summary": "List users",
					"tags": ["users"],
					"parameters": [
						{"name": "account_id", "in": "path", "required": true, "schema": {"type": "string"}}
					]
				}
			},
			"/v1/environments/{environment_id}/campaigns/{campaign_id}/duplicate": {
				"post": {
					"summary": "Duplicate campaign",
					"tags": ["campaigns"],
					"parameters": [
						{"name": "environment_id", "in": "path", "required": true, "schema": {"type": "string"}},
						{"name": "campaign_id", "in": "path", "required": true, "schema": {"type": "string"}}
					]
				}
			}
		}
	}`

	reg, err := LoadRegistryFromOpenAPI([]byte(spec))
	if err != nil {
		t.Fatal(err)
	}

	resources := reg.Resources()
	if len(resources) != 2 {
		t.Errorf("expected 2 resources, got %d: %v", len(resources), resources)
	}

	// Check campaign routes.
	campaigns := reg.ByResource["campaigns"]
	if len(campaigns) != 6 {
		t.Errorf("expected 6 campaign methods, got %d", len(campaigns))
		for _, c := range campaigns {
			t.Logf("  %s %s (%s)", c.HTTPMethod, c.Path, c.Method)
		}
	}

	// Check list campaigns.
	listRoute := reg.FindRoute("campaigns", "list")
	if listRoute == nil {
		t.Fatal("campaigns.list not found")
	}
	if listRoute.Summary != "List campaigns" {
		t.Errorf("expected summary 'List campaigns', got %q", listRoute.Summary)
	}
	if listRoute.Description != "Return campaigns for a workspace." {
		t.Errorf("expected description to be preserved, got %q", listRoute.Description)
	}
	if listRoute.Path != "/v1/environments/{environment_id}/campaigns" {
		t.Errorf("unexpected path: %q", listRoute.Path)
	}
	if len(listRoute.PathParams) != 1 || listRoute.PathParams[0].Name != "environment_id" {
		t.Errorf("unexpected path params: %v", listRoute.PathParams)
	}
	if len(listRoute.QueryParams) != 2 || listRoute.QueryParams[0].Name != "page" || listRoute.QueryParams[1].Name != "state" {
		t.Errorf("expected query params 'page' and 'state', got %v", listRoute.QueryParams)
	}
	if len(listRoute.QueryParams[0].Schema) == 0 {
		t.Fatal("expected page query param schema to be populated")
	}
	var pageSchema map[string]any
	if err := json.Unmarshal(listRoute.QueryParams[0].Schema, &pageSchema); err != nil {
		t.Fatalf("unmarshal page query param schema: %v", err)
	}
	if pageSchema["default"] != float64(1) {
		t.Errorf("expected page query param default=1, got %v", pageSchema["default"])
	}
	if len(listRoute.QueryParams[1].Schema) == 0 {
		t.Fatal("expected state query param schema to be populated")
	}
	var stateSchema map[string]any
	if err := json.Unmarshal(listRoute.QueryParams[1].Schema, &stateSchema); err != nil {
		t.Fatalf("unmarshal state query param schema: %v", err)
	}
	enumValues, _ := stateSchema["enum"].([]any)
	if len(enumValues) != 2 {
		t.Errorf("expected state enum values, got %v", stateSchema["enum"])
	}
	if len(listRoute.ResponseSchemas) != 1 {
		t.Fatalf("expected one response schema, got %v", listRoute.ResponseSchemas)
	}
	var responseSchema map[string]any
	if err := json.Unmarshal(listRoute.ResponseSchemas["200"], &responseSchema); err != nil {
		t.Fatalf("unmarshal 200 response schema: %v", err)
	}
	if responseSchema["type"] != "object" {
		t.Errorf("expected 200 response schema type=object, got %v", responseSchema["type"])
	}

	// Check create campaigns has body.
	createRoute := reg.FindRoute("campaigns", "create")
	if createRoute == nil {
		t.Fatal("campaigns.create not found")
	}
	if !createRoute.HasBody {
		t.Error("expected HasBody=true for POST")
	}
	if !createRoute.RequestBodyRequired {
		t.Error("expected RequestBodyRequired=true for POST request body")
	}
	if len(createRoute.RequestBodySchema) == 0 {
		t.Fatal("expected RequestBodySchema to be populated")
	}
	var bodySchema map[string]any
	if err := json.Unmarshal(createRoute.RequestBodySchema, &bodySchema); err != nil {
		t.Fatalf("unmarshal request body schema: %v", err)
	}
	if bodySchema["type"] != "object" {
		t.Errorf("expected request body schema type=object, got %v", bodySchema["type"])
	}
	properties, _ := bodySchema["properties"].(map[string]any)
	if _, ok := properties["campaign"]; !ok {
		t.Errorf("expected request body schema to include campaign property, got %v", properties)
	}

	// Check duplicate action.
	dupRoute := reg.FindRoute("campaigns", "duplicate")
	if dupRoute == nil {
		t.Fatal("campaigns.duplicate not found")
	}

	// Check ID params get inferred as integer type.
	getRoute := reg.FindRoute("campaigns", "get")
	if getRoute == nil {
		t.Fatal("campaigns.get not found")
	}
	for _, p := range getRoute.PathParams {
		if p.Name == "campaign_id" && p.Type != "integer" {
			t.Errorf("expected campaign_id type=integer, got %q", p.Type)
		}
	}
}

func TestIsOpenAPISpec(t *testing.T) {
	tests := []struct {
		name string
		data string
		want bool
	}{
		{"openapi spec", `{"openapi": "3.1.0", "info": {}, "paths": {}}`, true},
		{"walked routes", `[{"method": "GET", "path": "/v1/foo"}]`, false},
		{"empty object", `{}`, false},
		{"invalid json", `not json`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsOpenAPISpec([]byte(tt.data)); got != tt.want {
				t.Errorf("IsOpenAPISpec() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestOpenAPIPathToColonPath(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"/v1/environments/{environment_id}/campaigns", "/v1/environments/:environment_id/campaigns"},
		{"/v1/environments/{eid}/campaigns/{cid}", "/v1/environments/:eid/campaigns/:cid"},
		{"/v1/simple", "/v1/simple"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := openAPIPathToColonPath(tt.input); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLoadRegistryFromOpenAPI_NoSummaryFallback(t *testing.T) {
	spec := `{
		"openapi": "3.1.0",
		"info": {"title": "Test", "version": "1.0.0"},
		"paths": {
			"/v1/environments/{environment_id}/widgets": {
				"get": {
					"parameters": [
						{"name": "environment_id", "in": "path", "required": true, "schema": {"type": "string"}}
					]
				}
			}
		}
	}`

	reg, err := LoadRegistryFromOpenAPI([]byte(spec))
	if err != nil {
		t.Fatal(err)
	}

	route := reg.FindRoute("widgets", "list")
	if route == nil {
		t.Fatal("widgets.list not found")
	}
	if route.Summary == "" {
		t.Error("expected auto-generated summary for route without summary")
	}
}

func TestLoadRegistryFromOpenAPI_EmptyPaths(t *testing.T) {
	spec := `{"openapi": "3.1.0", "info": {"title": "Test", "version": "1.0.0"}}`
	_, err := LoadRegistryFromOpenAPI([]byte(spec))
	if err == nil {
		t.Error("expected error for spec with no paths")
	}
}

func TestHybridEnrichmentWithOpenAPI(t *testing.T) {
	// OpenAPI spec: tags.list has a real summary, campaigns.list has no summary.
	spec := `{
		"openapi": "3.1.0",
		"info": {"title": "Test", "version": "1.0.0"},
		"paths": {
			"/v1/environments/{environment_id}/tags": {
				"get": {
					"summary": "List tags",
					"parameters": [
						{"name": "environment_id", "in": "path", "required": true, "schema": {"type": "string"}, "description": "The workspace ID"}
					]
				}
			},
			"/v1/environments/{environment_id}/campaigns": {
				"get": {
					"parameters": [
						{"name": "environment_id", "in": "path", "required": true, "schema": {"type": "string"}}
					]
				}
			}
		}
	}`

	reg, err := LoadRegistryFromOpenAPI([]byte(spec))
	if err != nil {
		t.Fatal(err)
	}

	// Enrichment has summaries and param descriptions for both.
	enrichment := `{
		"GET /v1/environments/{environment_id}/tags": {
			"summary": "Enrichment: list all tags",
			"param_descriptions": {"environment_id": "Enrichment workspace ID"}
		},
		"GET /v1/environments/{environment_id}/campaigns": {
			"summary": "List campaigns in a workspace",
			"param_descriptions": {"environment_id": "The workspace (environment) ID"},
			"query_params": [{"name": "page", "type": "integer", "description": "Page number"}]
		}
	}`

	applyEnrichment(reg, []byte(enrichment))

	// tags.list: OpenAPI summary should win over enrichment.
	tagsList := reg.FindRoute("tags", "list")
	if tagsList == nil {
		t.Fatal("tags.list not found")
	}
	if tagsList.Summary != "List tags" {
		t.Errorf("expected OpenAPI summary 'List tags', got %q", tagsList.Summary)
	}
	// tags.list: OpenAPI param description should win over enrichment.
	if len(tagsList.PathParams) > 0 && tagsList.PathParams[0].Description != "The workspace ID" {
		t.Errorf("expected OpenAPI param desc 'The workspace ID', got %q", tagsList.PathParams[0].Description)
	}

	// campaigns.list: no OpenAPI summary, so enrichment should fill the gap.
	campaignsList := reg.FindRoute("campaigns", "list")
	if campaignsList == nil {
		t.Fatal("campaigns.list not found")
	}
	if campaignsList.Summary != "List campaigns in a workspace" {
		t.Errorf("expected enrichment summary, got %q", campaignsList.Summary)
	}
	// campaigns.list: no OpenAPI param desc, so enrichment should fill.
	if len(campaignsList.PathParams) > 0 && campaignsList.PathParams[0].Description != "The workspace (environment) ID" {
		t.Errorf("expected enrichment param desc, got %q", campaignsList.PathParams[0].Description)
	}
	// campaigns.list: no OpenAPI query params, so enrichment should fill.
	if len(campaignsList.QueryParams) != 1 || campaignsList.QueryParams[0].Name != "page" {
		t.Errorf("expected enrichment query param 'page', got %v", campaignsList.QueryParams)
	}
}

// Verify that the registry loaded from OpenAPI produces the same JSON output
// structure that schema commands expect.
func TestOpenAPIRegistryJSONOutput(t *testing.T) {
	spec := `{
		"openapi": "3.1.0",
		"info": {"title": "Test", "version": "1.0.0"},
		"paths": {
			"/v1/environments/{environment_id}/campaigns": {
				"get": {
					"summary": "List campaigns",
					"parameters": [
						{"name": "environment_id", "in": "path", "required": true, "schema": {"type": "string"}}
					]
				}
			}
		}
	}`

	reg, err := LoadRegistryFromOpenAPI([]byte(spec))
	if err != nil {
		t.Fatal(err)
	}

	// Route should be JSON-serializable with expected fields.
	route := reg.FindRoute("campaigns", "list")
	if route == nil {
		t.Fatal("campaigns.list not found")
	}

	data, err := json.Marshal(map[string]any{
		"resource":    route.Resource,
		"method":      route.Method,
		"http_method": route.HTTPMethod,
		"path":        route.Path,
		"summary":     route.Summary,
	})
	if err != nil {
		t.Fatal(err)
	}

	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}

	if result["resource"] != "campaigns" {
		t.Errorf("expected resource=campaigns, got %v", result["resource"])
	}
}

func TestLoadRegistryFromOpenAPI_ResolvesRequestBodyRefs(t *testing.T) {
	spec := `{
		"openapi": "3.1.0",
		"info": {"title": "Test", "version": "1.0.0"},
		"components": {
			"schemas": {
				"CreateThing": {
					"type": "object",
					"properties": {"name": {"type": "string"}},
					"required": ["name"]
				}
			}
		},
		"paths": {
			"/v1/environments/{environment_id}/things": {
				"post": {
					"summary": "Create thing",
					"parameters": [
						{"name": "environment_id", "in": "path", "required": true, "schema": {"type": "string"}}
					],
					"requestBody": {
						"required": true,
						"content": {
							"application/json": {
								"schema": {"$ref": "#/components/schemas/CreateThing"}
							}
						}
					}
				}
			}
		}
	}`

	reg, err := LoadRegistryFromOpenAPI([]byte(spec))
	if err != nil {
		t.Fatal(err)
	}
	route := reg.FindRoute("things", "create")
	if route == nil {
		t.Fatal("things.create not found")
	}
	if len(route.RequestBodySchema) == 0 {
		t.Fatal("expected RequestBodySchema to be populated from $ref")
	}
	var obj map[string]any
	if err := json.Unmarshal(route.RequestBodySchema, &obj); err != nil {
		t.Fatal(err)
	}
	if obj["type"] != "object" {
		t.Errorf("expected resolved schema type=object, got %v", obj["type"])
	}
	props, _ := obj["properties"].(map[string]any)
	if _, ok := props["name"]; !ok {
		t.Error("expected resolved schema to include 'name' property")
	}
}
