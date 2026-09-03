package routes

import (
	"strings"
	"testing"
)

// indexFixture builds an index directly, bypassing the spec cache, so the
// matching rules can be tested without a server or a temp HOME.
func indexFixture(t *testing.T, pairs ...string) *PathIndex {
	t.Helper()
	idx := &PathIndex{}
	for _, p := range pairs {
		parts := strings.SplitN(p, " ", 2)
		if len(parts) != 2 {
			t.Fatalf("fixture entry %q must be \"METHOD /path\"", p)
		}
		idx.push(parts[0], parts[1])
	}
	return idx
}

func campaignsIndex(t *testing.T) *PathIndex {
	t.Helper()
	return indexFixture(t,
		"GET /v1/environments/{environment_id}/campaigns",
		"POST /v1/environments/{environment_id}/campaigns",
		"GET /v1/environments/{environment_id}/campaigns/{campaign_id}",
		"PUT /v1/environments/{environment_id}/campaigns/{campaign_id}",
		"GET /v1/environments/{environment_id}/campaigns/{campaign_id}/action_metrics",
		"POST /v1/environments/{environment_id}/newsletters/{newsletter_id}/translations",
		"GET /v1/environments/{environment_id}/actions/{action_id}",
		"GET /cdp/api/workspaces/{workspace_id}/sources",
	)
}

// Nested sub-resources — /v1/a/{a_id}/b/{b_id}/c — are where the invented
// paths show up, so a real one must pass and its invented neighbours must not,
// at any depth.
func TestPathIndexNestedSubResources(t *testing.T) {
	idx := campaignsIndex(t)

	if _, ok := idx.Lookup("GET", "/v1/environments/456/campaigns/48/action_metrics"); !ok {
		t.Error("expected a real nested sub-resource to match")
	}

	for _, path := range []string{
		"/v1/environments/456/campaigns/48/actions",
		"/v1/environments/456/campaigns/48/actions/12",
		"/v1/environments/456/campaigns/48/foo/bar",
		"/v1/environments/456/newsletters/7/made_up",
	} {
		if _, ok := idx.Lookup("GET", path); ok {
			t.Errorf("expected %s not to match", path)
		}
		if !idx.Covers(path) {
			t.Errorf("expected %s to be judged (deep inside a documented scope)", path)
		}
	}

	// A nested route that exists only for another verb reports that verb
	// rather than being called absent.
	nested := "/v1/environments/456/newsletters/7/translations"
	if _, ok := idx.Lookup("GET", nested); ok {
		t.Error("expected GET not to match a POST-only nested route")
	}
	if methods := idx.MethodsFor(nested); strings.Join(methods, ",") != "POST" {
		t.Errorf("expected POST to be reported for %s, got %v", nested, methods)
	}
}

func TestPathIndexLookupTemplateAndResolvedForms(t *testing.T) {
	idx := campaignsIndex(t)

	for _, path := range []string{
		"/v1/environments/{environment_id}/campaigns",
		"/v1/environments/456/campaigns",
	} {
		if _, ok := idx.Lookup("GET", path); !ok {
			t.Errorf("expected GET %s to match a route", path)
		}
	}

	template, ok := idx.Lookup("GET", "/v1/environments/456/campaigns/48")
	if !ok {
		t.Fatal("expected a resolved two-id path to match")
	}
	if template != "/v1/environments/{environment_id}/campaigns/{campaign_id}" {
		t.Errorf("unexpected template: %s", template)
	}
}

// The sub-path form of a resource read is the mistake this index exists to
// catch: campaign actions come back inside the campaign, and there is no
// /campaigns/:id/actions route to call.
func TestPathIndexRejectsUnknownSubPath(t *testing.T) {
	idx := campaignsIndex(t)

	if _, ok := idx.Lookup("GET", "/v1/environments/456/campaigns/48/actions"); ok {
		t.Error("expected an invented sub-path not to match")
	}
	if methods := idx.MethodsFor("/v1/environments/456/campaigns/48/actions"); len(methods) != 0 {
		t.Errorf("expected no methods for an unknown shape, got %v", methods)
	}
}

func TestPathIndexMethodsForDistinguishesWrongVerb(t *testing.T) {
	idx := campaignsIndex(t)

	if _, ok := idx.Lookup("DELETE", "/v1/environments/456/campaigns"); ok {
		t.Error("expected DELETE not to match the campaigns collection")
	}

	methods := idx.MethodsFor("/v1/environments/456/campaigns")
	if strings.Join(methods, ",") != "GET,POST" {
		t.Errorf("expected sorted GET,POST, got %v", methods)
	}
}

func TestPathIndexLookupIsMethodCaseInsensitive(t *testing.T) {
	idx := campaignsIndex(t)

	if _, ok := idx.Lookup("get", "/v1/environments/456/campaigns"); !ok {
		t.Error("expected a lower-case verb to match")
	}
}

func TestPathIndexCovers(t *testing.T) {
	idx := campaignsIndex(t)

	// Inside a documented scope: absence from the index means something.
	for _, path := range []string{
		"/v1/environments/456/campaigns",
		"/v1/environments/456/campaigns/48/actions",
		"/v1/environments/456/made_up_resource",
		"/cdp/api/workspaces/1/sources",
	} {
		if !idx.Covers(path) {
			t.Errorf("expected %s to be covered", path)
		}
	}

	// Too shallow to judge. "/v1/login" and friends are real endpoints the
	// spec omits, so treating them as absent would block working calls; a
	// tree the spec never described ("/v2/...") is equally not ours to judge.
	for _, path := range []string{
		"/health",
		"/v1/login",
		"/v1/campaigns",
		"/v2/environments/456/campaigns",
		"/",
		"",
	} {
		if idx.Covers(path) {
			t.Errorf("expected %s not to be covered", path)
		}
	}
}

func TestPathIndexSuggestRanksNearestFirst(t *testing.T) {
	idx := campaignsIndex(t)

	got := idx.Suggest("/v1/environments/456/campaigns/48/actions", 3)
	if len(got) == 0 {
		t.Fatal("expected suggestions for a near-miss path")
	}

	// Same shared prefix, same depth: a real sibling of the invented segment
	// is a closer offer than the parent read one segment shorter.
	if got[0].Path != "/v1/environments/{environment_id}/campaigns/{campaign_id}/action_metrics" {
		t.Errorf("expected the same-depth sibling first, got %s", got[0].Path)
	}
	if got[0].String() != "GET /v1/environments/{environment_id}/campaigns/{campaign_id}/action_metrics" {
		t.Errorf("unexpected rendering: %s", got[0].String())
	}

	// Whichever route ranks first, the hint it drives must name the resource
	// the caller was reaching for.
	if got[0].Resource != "campaigns" {
		t.Errorf("expected a campaigns resource hint, got %q", got[0].Resource)
	}

	// The parent read stays in the list, just behind.
	var sawParent bool
	for _, s := range got {
		if s.Path == "/v1/environments/{environment_id}/campaigns/{campaign_id}" {
			sawParent = true
		}
	}
	if !sawParent {
		t.Errorf("expected the campaign read among the suggestions, got %v", got)
	}
}

func TestPathIndexSuggestRespectsLimitAndDistance(t *testing.T) {
	idx := campaignsIndex(t)

	if got := idx.Suggest("/v1/environments/456/campaigns", 2); len(got) > 2 {
		t.Errorf("expected at most 2 suggestions, got %d", len(got))
	}
	// Shares only "/cdp" with the indexed cdp route — below the two-segment
	// floor, so suggesting it would be noise.
	if got := idx.Suggest("/cdp/nonsense", 5); len(got) != 0 {
		t.Errorf("expected no suggestions for a distant path, got %v", got)
	}
}

func TestPathIndexAddOpenAPISkipsNonOperationKeys(t *testing.T) {
	spec := []byte(`{
		"openapi": "3.1.0",
		"paths": {
			"/v1/environments/{environment_id}/segments": {
				"get": {"summary": "List segments"},
				"parameters": [{"name": "environment_id", "in": "path"}],
				"summary": "Segments collection"
			}
		}
	}`)

	idx := &PathIndex{}
	if err := idx.add(spec); err != nil {
		t.Fatalf("add: %v", err)
	}
	if idx.Len() != 1 {
		t.Fatalf("expected only the get operation to be indexed, got %d entries", idx.Len())
	}
	if _, ok := idx.Lookup("GET", "/v1/environments/9/segments"); !ok {
		t.Error("expected the get operation to be indexed")
	}
	for _, verb := range []string{"PARAMETERS", "SUMMARY"} {
		if _, ok := idx.Lookup(verb, "/v1/environments/9/segments"); ok {
			t.Errorf("expected %s not to be indexed as a method", verb)
		}
	}
}

func TestPathIndexAddWalkedRoutes(t *testing.T) {
	walked := []byte(`[
		{"method": "GET", "path": "/v1/environments/:environment_id/campaigns"},
		{"method": "PUT", "path": "/v1/environments/:environment_id/campaigns/:campaign_id"}
	]`)

	idx := &PathIndex{}
	if err := idx.add(walked); err != nil {
		t.Fatalf("add: %v", err)
	}

	// :param must normalize to {param} so both path forms still match.
	template, ok := idx.Lookup("PUT", "/v1/environments/456/campaigns/48")
	if !ok {
		t.Fatal("expected a walked route to match a resolved path")
	}
	if template != "/v1/environments/{environment_id}/campaigns/{campaign_id}" {
		t.Errorf("expected a normalized template, got %s", template)
	}
}

func TestPathIndexAddRejectsUnparseableSpec(t *testing.T) {
	idx := &PathIndex{}
	if err := idx.add([]byte(`{"not": "a spec"}`)); err == nil {
		t.Error("expected an error for a document that is neither OpenAPI nor walked routes")
	}
}
