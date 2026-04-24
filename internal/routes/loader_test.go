package routes

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDeriveFromWalkedPath(t *testing.T) {
	tests := []struct {
		httpMethod string
		path       string
		wantRes    string
		wantMethod string
	}{
		// Simple CRUD
		{"GET", "/v1/environments/:environment_id/campaigns", "campaigns", "list"},
		{"POST", "/v1/environments/:environment_id/campaigns", "campaigns", "create"},
		{"GET", "/v1/environments/:environment_id/campaigns/:campaign_id", "campaigns", "get"},
		{"PUT", "/v1/environments/:environment_id/campaigns/:campaign_id", "campaigns", "update"},
		{"DELETE", "/v1/environments/:environment_id/campaigns/:campaign_id", "campaigns", "delete"},

		// Sub-resources
		{"GET", "/v1/environments/:environment_id/campaigns/:campaign_id/tags", "campaigns", "list-tags"},
		{"POST", "/v1/environments/:environment_id/campaigns/:campaign_id/tags", "campaigns", "create-tags"},
		{"DELETE", "/v1/environments/:environment_id/campaigns/:campaign_id/tags/:tag_id", "campaigns", "delete-tags"},
		{"GET", "/v1/environments/:environment_id/campaigns/:campaign_id/metrics", "campaigns", "list-metrics"},

		// Actions (POST to a verb-like path)
		{"POST", "/v1/environments/:environment_id/campaigns/:campaign_id/duplicate", "campaigns", "duplicate"},
		{"POST", "/v1/environments/:environment_id/campaigns/:campaign_id/activate", "campaigns", "activate"},

		// Account-scoped
		{"GET", "/v1/accounts/:account_id/billing/plans", "billing", "list-plans"},
		{"GET", "/v1/accounts/:account_id/billing/plans/:plan_id", "billing", "get-plans"},
		{"POST", "/v1/accounts/:account_id/billing/payment", "billing", "create-payment"},
		{"GET", "/v1/accounts/:account_id/users", "users", "list"},

		// Workspace-scoped
		{"GET", "/v1/workspaces/:workspace_id/sms/phone_numbers", "sms", "list-phone_numbers"},

		// Deeply nested
		{"GET", "/v1/environments/:eid/campaigns/:cid/triggers/:tid/download_errors", "campaigns", "list-triggers-download_errors"},

		// Scope root
		{"GET", "/v1/accounts/:account_id", "accounts", "get"},
		{"PUT", "/v1/accounts/:account_id", "accounts", "update"},

		// Environments (workspaces) — scope root
		{"GET", "/v1/environments/:environment_id", "environments", "get"},
		{"PUT", "/v1/environments/:environment_id", "environments", "update"},
		{"DELETE", "/v1/environments/:environment_id", "environments", "delete"},
		// environments under accounts get resource="environments"
		{"GET", "/v1/accounts/:account_id/environments", "environments", "list"},
		{"POST", "/v1/accounts/:account_id/environments", "environments", "create"},

		// Environment sub-resources — these become their own resources since
		// they're the first noun after stripping the scope prefix
		{"GET", "/v1/environments/:environment_id/site_ids", "site_ids", "list"},
		{"GET", "/v1/environments/:environment_id/signing_secret", "signing_secret", "list"},
		{"PUT", "/v1/environments/:environment_id/signing_secret", "signing_secret", "update"},
		{"GET", "/v1/environments/:environment_id/settings/workspace", "settings", "list-workspace"},
		{"PUT", "/v1/environments/:environment_id/settings/workspace", "settings", "update-workspace"},
		{"GET", "/v1/environments/:environment_id/geolocation", "geolocation", "list"},
		{"POST", "/v1/environments/:environment_id/geolocation", "geolocation", "create"},

		// CDP Pipelines — workspace-scoped
		{"GET", "/cdp/api/workspaces/:workspace_id/sources", "sources", "list"},
		{"POST", "/cdp/api/workspaces/:workspace_id/sources", "sources", "create"},
		{"GET", "/cdp/api/workspaces/:workspace_id/sources/:source_id", "sources", "get"},
		{"PUT", "/cdp/api/workspaces/:workspace_id/sources/:source_id", "sources", "update"},
		{"DELETE", "/cdp/api/workspaces/:workspace_id/sources/:source_id", "sources", "delete"},
		{"GET", "/cdp/api/workspaces/:workspace_id/destinations", "destinations", "list"},
		{"GET", "/cdp/api/workspaces/:workspace_id/destinations/:destination_id/events", "destinations", "list-events"},
		{"GET", "/cdp/api/workspaces/:workspace_id/reverse_etls", "reverse_etls", "list"},
		{"GET", "/cdp/api/workspaces/:workspace_id/sources/:source_id/syncs/:sync_id", "sources", "get-syncs"},

		// CDP Pipelines — account-scoped
		{"GET", "/cdp/api/accounts/:account_id/usage", "usage", "list"},
		{"GET", "/cdp/api/accounts/:account_id/users", "users", "list"},

		// CDP Pipelines — top-level (no scope)
		{"GET", "/cdp/api/auth/current", "auth", "list-current"},
		{"POST", "/cdp/api/track_event", "track_event", "create"},

		// Paths that shouldn't match
		{"GET", "/v1/auth/session", "", ""},
		{"GET", "/health", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.httpMethod+" "+tt.path, func(t *testing.T) {
			gotRes, gotMethod := deriveFromWalkedPath(tt.path, tt.httpMethod)
			if gotRes != tt.wantRes {
				t.Errorf("resource: got %q, want %q", gotRes, tt.wantRes)
			}
			if gotMethod != tt.wantMethod {
				t.Errorf("method: got %q, want %q", gotMethod, tt.wantMethod)
			}
		})
	}
}

func TestLoadRegistryFromData(t *testing.T) {
	input := []walkedRoute{
		{"GET", "/v1/environments/:environment_id/campaigns"},
		{"POST", "/v1/environments/:environment_id/campaigns"},
		{"GET", "/v1/environments/:environment_id/campaigns/:campaign_id"},
		{"PUT", "/v1/environments/:environment_id/campaigns/:campaign_id"},
		{"DELETE", "/v1/environments/:environment_id/campaigns/:campaign_id"},
		{"GET", "/v1/environments/:environment_id/segments"},
		{"POST", "/v1/environments/:environment_id/segments"},
		{"GET", "/v1/accounts/:account_id/users"},
	}

	data, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}

	reg, err := LoadRegistryFromData(data)
	if err != nil {
		t.Fatal(err)
	}

	resources := reg.Resources()
	if len(resources) != 3 {
		t.Errorf("expected 3 resources, got %d: %v", len(resources), resources)
	}

	if len(reg.ByResource["campaigns"]) != 5 {
		t.Errorf("expected 5 campaign methods, got %d", len(reg.ByResource["campaigns"]))
	}

	campaignsList := reg.FindRoute("campaigns", "list")
	if campaignsList == nil {
		t.Fatal("campaigns.list not found")
	}
	if campaignsList.Path != "/v1/environments/{environment_id}/campaigns" {
		t.Errorf("expected normalized path, got %q", campaignsList.Path)
	}
	if campaignsList.HTTPMethod != "GET" {
		t.Errorf("expected GET, got %q", campaignsList.HTTPMethod)
	}
	if len(campaignsList.PathParams) != 1 || campaignsList.PathParams[0].Name != "environment_id" {
		t.Errorf("expected environment_id path param, got %v", campaignsList.PathParams)
	}

	campaignsCreate := reg.FindRoute("campaigns", "create")
	if campaignsCreate == nil {
		t.Fatal("campaigns.create not found")
	}
	if !campaignsCreate.HasBody {
		t.Error("expected HasBody=true for POST")
	}

	campaignsGet := reg.FindRoute("campaigns", "get")
	if campaignsGet == nil {
		t.Fatal("campaigns.get not found")
	}
	if campaignsGet.HasBody {
		t.Error("expected HasBody=false for GET")
	}
}

func TestLoadRegistryDeduplication(t *testing.T) {
	input := []walkedRoute{
		{"GET", "/v1/environments/:environment_id/campaigns"},
		{"GET", "/v1/environments/:environment_id/campaigns"}, // duplicate
		{"POST", "/v1/environments/:environment_id/campaigns"},
	}

	data, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}

	reg, err := LoadRegistryFromData(data)
	if err != nil {
		t.Fatal(err)
	}

	if len(reg.ByResource["campaigns"]) != 2 {
		t.Errorf("expected 2 after dedup, got %d", len(reg.ByResource["campaigns"]))
	}
}

func TestLoadRegistry_WithHTTPServer(t *testing.T) {
	journeysSpec := `{
		"openapi": "3.1.0",
		"info": {"title": "Journeys", "version": "1.0.0"},
		"paths": {
			"/v1/environments/{environment_id}/campaigns": {
				"get": {
					"summary": "List campaigns",
					"parameters": [
						{"name": "environment_id", "in": "path", "required": true, "schema": {"type": "string"}}
					]
				},
				"post": {
					"summary": "Create campaign",
					"parameters": [
						{"name": "environment_id", "in": "path", "required": true, "schema": {"type": "string"}}
					],
					"requestBody": {"required": true, "content": {"application/json": {"schema": {"type": "object"}}}}
				}
			},
			"/v1/environments/{environment_id}/campaigns/{campaign_id}": {
				"get": {
					"summary": "Get campaign",
					"parameters": [
						{"name": "environment_id", "in": "path", "required": true, "schema": {"type": "string"}},
						{"name": "campaign_id", "in": "path", "required": true, "schema": {"type": "string"}}
					]
				}
			}
		}
	}`

	cdpSpec := `{
		"openapi": "3.1.0",
		"info": {"title": "CDP", "version": "1.0.0"},
		"paths": {
			"/cdp/api/workspaces/{workspace_id}/sources": {
				"get": {
					"summary": "List sources",
					"parameters": [
						{"name": "workspace_id", "in": "path", "required": true, "schema": {"type": "string"}}
					]
				}
			}
		}
	}`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/openapi.json":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(journeysSpec))
		case "/cdp/api/openapi.json":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(cdpSpec))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	reg, err := LoadRegistry(LoadRegistryOptions{
		BaseURL:  server.URL,
		CacheDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("failed to load routes: %v", err)
	}

	resources := reg.Resources()
	if len(resources) != 2 {
		t.Errorf("expected 2 resources, got %d: %v", len(resources), resources)
	}

	campaigns := reg.ByResource["campaigns"]
	if len(campaigns) != 3 {
		t.Errorf("expected 3 campaign methods, got %d", len(campaigns))
	}

	// All paths should be normalized.
	for _, r := range reg.Routes {
		if colonParamRegex.MatchString(r.Path) {
			t.Errorf("path should use {param} not :param: %s", r.Path)
		}
	}

	// CDP routes should be merged in.
	sourcesList := reg.FindRoute("sources", "list")
	if sourcesList == nil {
		t.Fatal("sources.list not found")
	}
	if sourcesList.Path != "/cdp/api/workspaces/{workspace_id}/sources" {
		t.Errorf("unexpected sources.list path: %q", sourcesList.Path)
	}
}

func TestStripScopePrefix(t *testing.T) {
	tests := []struct {
		path, want string
	}{
		{"/v1/environments/:eid/campaigns", "campaigns"},
		{"/v1/accounts/:aid/billing/plans", "billing/plans"},
		{"/v1/workspaces/:wid/sms/numbers", "sms/numbers"},
		{"/v1/accounts/:aid", "accounts"},
		{"/v1/other/path", ""},

		// CDP Pipelines scope prefixes
		{"/cdp/api/workspaces/:wid/sources", "sources"},
		{"/cdp/api/accounts/:aid/usage", "usage"},
		{"/cdp/api/auth/current", "auth/current"},
		{"/cdp/api/track_event", "track_event"},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := stripScopePrefix(tt.path)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}
