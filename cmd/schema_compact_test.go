package cmd

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/customerio/cli/internal/routes"
)

func fieldByPath(t *testing.T, fields []flatField, path string) flatField {
	t.Helper()
	for _, f := range fields {
		if f.Path == path {
			return f
		}
	}
	t.Fatalf("no field at path %q; got %v", path, fieldPaths(fields))
	return flatField{}
}

func hasFieldPath(fields []flatField, path string) bool {
	for _, f := range fields {
		if f.Path == path {
			return true
		}
	}
	return false
}

func fieldPaths(fields []flatField) []string {
	paths := make([]string, 0, len(fields))
	for _, f := range fields {
		paths = append(paths, f.Path)
	}
	return paths
}

func TestFlattenSchema_NestedObjectsArraysEnumsRefs(t *testing.T) {
	raw := json.RawMessage(`{
		"type": "object",
		"required": ["name", "audience"],
		"properties": {
			"name":  { "type": "string" },
			"audience": {
				"type": "object",
				"required": ["type"],
				"properties": {
					"type":           { "type": "integer", "enum": ["1", "2"], "description": "Audience selector" },
					"person_filters": { "type": "string" }
				}
			},
			"edges": {
				"type": "array",
				"items": { "type": "object", "properties": { "from": { "type": "string" }, "to": { "type": "string" } } }
			},
			"tags":     { "type": "array", "items": { "type": "string" } },
			"layout":   { "$ref": "#/components/schemas/api_ui_Layout" },
			"freeform": { "type": "object" }
		}
	}`)

	fields := flattenSchema(raw)

	if name := fieldByPath(t, fields, "name"); name.Type != "string" || !name.Required {
		t.Errorf("name = %+v, want type=string required=true", name)
	}

	at := fieldByPath(t, fields, "audience.type")
	if at.Type != "integer" || !at.Required {
		t.Errorf("audience.type = %+v, want type=integer required=true", at)
	}
	if !slices.Equal(at.Enum, []string{"1", "2"}) {
		t.Errorf("audience.type enum = %v, want [1 2]", at.Enum)
	}
	if at.Description != "Audience selector" {
		t.Errorf("audience.type description = %q", at.Description)
	}

	if pf := fieldByPath(t, fields, "audience.person_filters"); pf.Required {
		t.Error("field absent from parent required should be optional")
	}

	for _, path := range []string{"edges[].from", "edges[].to"} {
		if !hasFieldPath(fields, path) {
			t.Errorf("array-of-objects should flatten items under %q", path)
		}
	}

	if tags := fieldByPath(t, fields, "tags[]"); tags.Type != "string" {
		t.Errorf("array-of-scalars leaf type = %q, want string", tags.Type)
	}

	if layout := fieldByPath(t, fields, "layout"); !strings.Contains(layout.Type, "Layout") {
		t.Errorf("ref leaf type = %q, want it to name the component", layout.Type)
	}

	if free := fieldByPath(t, fields, "freeform"); free.Type != "object" {
		t.Errorf("object without properties = %q, want object leaf", free.Type)
	}
}

// An object with empty properties must still appear as a leaf, not vanish.
func TestFlattenSchema_EmptyPropertiesObjectIsLeaf(t *testing.T) {
	raw := json.RawMessage(`{"type":"object","properties":{"meta":{"type":"object","properties":{}}}}`)
	if f := fieldByPath(t, flattenSchema(raw), "meta"); f.Type != "object" {
		t.Errorf("meta type = %q, want object", f.Type)
	}
}

// A union type ("type": ["string","null"]) must render the members, not "object".
func TestFlattenSchema_UnionType(t *testing.T) {
	raw := json.RawMessage(`{"type":"object","properties":{"x":{"type":["string","null"]}}}`)
	if f := fieldByPath(t, flattenSchema(raw), "x"); f.Type != "string|null" {
		t.Errorf("x type = %q, want string|null", f.Type)
	}
}

// A root scalar body must not produce a blank-path line.
func TestFlattenSchema_RootScalarHasName(t *testing.T) {
	fields := flattenSchema(json.RawMessage(`{"type":"string"}`))
	if len(fields) != 1 || fields[0].Path == "" {
		t.Fatalf("root scalar must produce one labeled leaf, got %+v", fields)
	}
}

// A surviving ref renders as the trailing type name, not the full generated
// component path.
func TestRefType_ShortensToTypeName(t *testing.T) {
	cases := map[string]string{
		"#/components/schemas/pkg_libraries_filters_Filter": "ref:Filter",
		"#/components/schemas/pkg_core_MapFilter":           "ref:MapFilter",
		"#/components/schemas/Widget":                       "ref:Widget",
	}
	for ref, want := range cases {
		if got := refType(ref); got != want {
			t.Errorf("refType(%q) = %q, want %q", ref, got, want)
		}
	}
}

// oneOf/anyOf members are flattened at the same path, so overlapping fields
// must be de-duplicated to one line each (the union of possible fields).
func TestFlattenSchema_DeduplicatesPaths(t *testing.T) {
	raw := json.RawMessage(`{"oneOf":[
		{"type":"object","properties":{"id":{"type":"string"},"name":{"type":"string"}}},
		{"type":"object","properties":{"id":{"type":"string"},"extra":{"type":"integer"}}}
	]}`)
	fields := flattenSchema(raw)

	count := 0
	for _, f := range fields {
		if f.Path == "id" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("id appears %d times, want 1", count)
	}
	for _, path := range []string{"name", "extra"} {
		if !hasFieldPath(fields, path) {
			t.Errorf("union of member fields must keep %q", path)
		}
	}
}

// A required root-scalar body must not show the optional "?" marker; its
// optionality is conveyed by request_body_required, not by the root leaf.
func TestRouteDetail_CompactRootScalarBodyMatchesRequiredFlag(t *testing.T) {
	base := func(required bool) *routes.Route {
		return &routes.Route{
			Resource: "x", Method: "create", HTTPMethod: "POST", Path: "/v1/x", HasBody: true,
			RequestBodySchema: json.RawMessage(`{"type":"string"}`), RequestBodyRequired: required,
		}
	}
	cases := []struct {
		required bool
		want     string
	}{
		{true, "(value)  string"},
		{false, "(value)  string?"},
	}
	for _, tc := range cases {
		body, ok := routeDetail(base(tc.required), true)["request_body"].([]string)
		if !ok || len(body) != 1 {
			t.Fatalf("required=%v: request_body = %v", tc.required, routeDetail(base(tc.required), true)["request_body"])
		}
		if body[0] != tc.want {
			t.Errorf("required=%v: body line = %q, want %q", tc.required, body[0], tc.want)
		}
	}
}

// A root boolean schema (true/false) must still emit a labeled line.
func TestFlattenSchema_RootBooleanSchema(t *testing.T) {
	fields := flattenSchema(json.RawMessage(`true`))
	if len(fields) != 1 || fields[0].Path == "" {
		t.Fatalf("root boolean schema must emit one labeled leaf, got %+v", fields)
	}
}

// A field with only an enum (no type) must infer its type, not show "object".
func TestFlattenSchema_EnumOnlyFieldTyped(t *testing.T) {
	raw := json.RawMessage(`{"type":"object","properties":{"x":{"enum":["a","b"]}}}`)
	f := fieldByPath(t, flattenSchema(raw), "x")
	if f.Type != "string" {
		t.Errorf("enum-only field type = %q, want string inferred from members", f.Type)
	}
	if !slices.Equal(f.Enum, []string{"a", "b"}) {
		t.Errorf("enum = %v, want [a b]", f.Enum)
	}
}

// Compact param lines must surface a param's schema enum.
func TestRouteDetail_CompactParamIncludesEnum(t *testing.T) {
	r := &routes.Route{
		Resource: "c", Method: "list", HTTPMethod: "GET", Path: "/v1/x",
		QueryParams: []routes.QueryParam{{
			Name: "state", Type: "string", Required: false, Description: "Filter by state",
			Schema: json.RawMessage(`{"type":"string","enum":["running","draft"]}`),
		}},
	}
	qp, ok := routeDetail(r, true)["query_params"].([]string)
	if !ok || len(qp) != 1 {
		t.Fatalf("query_params = %v, want one line", routeDetail(r, true)["query_params"])
	}
	if !strings.Contains(qp[0], "enum: running|draft") {
		t.Errorf("query param line = %q, want it to carry the enum", qp[0])
	}
}

// A union type that includes "array" must still flatten its items.
func TestFlattenSchema_UnionArrayFlattensItems(t *testing.T) {
	raw := json.RawMessage(`{"type":"object","properties":{"xs":{"type":["array","null"],"items":{"type":"object","properties":{"a":{"type":"string"}}}}}}`)
	if !hasFieldPath(flattenSchema(raw), "xs[].a") {
		t.Error("union array type must descend into items")
	}
}

// An array whose items is a boolean schema must still use the [] convention.
func TestFlattenSchema_BooleanItemsKeepBracket(t *testing.T) {
	raw := json.RawMessage(`{"type":"object","properties":{"tags":{"type":"array","items":true}}}`)
	if !hasFieldPath(flattenSchema(raw), "tags[]") {
		t.Error("array with boolean items must render tags[]")
	}
}

// Boolean and null enum members must render, not be silently dropped, and a
// null member must not break type inference (it is nullability, not a type).
func TestFlattenSchema_BooleanAndNullEnum(t *testing.T) {
	raw := json.RawMessage(`{"type":"object","properties":{"flag":{"enum":[true,false,null]}}}`)
	f := fieldByPath(t, flattenSchema(raw), "flag")
	if !slices.Equal(f.Enum, []string{"true", "false", "null"}) {
		t.Errorf("enum = %v, want [true false null]", f.Enum)
	}
	if f.Type != "boolean" {
		t.Errorf("type = %q, want boolean (null must not drop inference)", f.Type)
	}
}

// allOf is a conjunction: a required list in one member applies to a property
// defined in another, and a constraint-only member emits no phantom leaf.
func TestFlattenSchema_AllOfRequiredAcrossMembers(t *testing.T) {
	raw := json.RawMessage(`{"type":"object","properties":{"obj":{"allOf":[
		{"type":"object","properties":{"id":{"type":"string"},"opt":{"type":"string"}}},
		{"required":["id"]}
	]}}}`)
	fields := flattenSchema(raw)

	if id := fieldByPath(t, fields, "obj.id"); !id.Required {
		t.Error("required from a sibling allOf member must apply")
	}
	if opt := fieldByPath(t, fields, "obj.opt"); opt.Required {
		t.Error("fields not in any member's required stay optional")
	}
	if hasFieldPath(fields, "obj") {
		t.Error("constraint-only allOf member must not emit a phantom leaf")
	}
}

// An empty composition array must not make the field disappear.
func TestFlattenSchema_EmptyCompositionStillLeaf(t *testing.T) {
	raw := json.RawMessage(`{"type":"object","properties":{"x":{"allOf":[]}}}`)
	if f := fieldByPath(t, flattenSchema(raw), "x"); f.Type != "object" {
		t.Errorf("x type = %q, want object", f.Type)
	}
}

// A surviving $ref at the schema root must be labeled, not blank-path.
func TestFlattenSchema_RootRefHasName(t *testing.T) {
	fields := flattenSchema(json.RawMessage(`{"$ref":"#/components/schemas/Foo"}`))
	if len(fields) != 1 || fields[0].Path == "" {
		t.Fatalf("root $ref must produce one labeled leaf, got %+v", fields)
	}
	if !strings.Contains(fields[0].Type, "Foo") {
		t.Errorf("root ref type = %q, want it to name Foo", fields[0].Type)
	}
}

// allOf/anyOf/oneOf members must be flattened, not collapsed to an "object" leaf.
func TestFlattenSchema_AllOfMergesFields(t *testing.T) {
	raw := json.RawMessage(`{"type":"object","properties":{"w":{"allOf":[
		{"type":"object","properties":{"a":{"type":"string"}}},
		{"type":"object","properties":{"b":{"type":"integer"}}}
	]}}}`)
	fields := flattenSchema(raw)
	for _, path := range []string{"w.a", "w.b"} {
		if !hasFieldPath(fields, path) {
			t.Errorf("allOf member field %q must flatten", path)
		}
	}
}

// Numeric enum values must render with their exact value (0 must not become
// empty, 10 must not become 1).
func TestFlattenSchema_NumericEnumValuesIntact(t *testing.T) {
	raw := json.RawMessage(`{"type":"object","properties":{"state":{"type":"integer","enum":[0,10,1,1.5]}}}`)
	f := fieldByPath(t, flattenSchema(raw), "state")
	if !slices.Equal(f.Enum, []string{"0", "10", "1", "1.5"}) {
		t.Errorf("enum = %v, want [0 10 1 1.5]", f.Enum)
	}
}

// Descriptions must reach the agent in full (enum docs, constraints), with
// internal whitespace collapsed so the field stays on one line.
func TestSchemaDescription_FullTextNoTruncation(t *testing.T) {
	raw := "Audience selector.\n0 = Self,\t1 = all,  2 = certain. " + strings.Repeat("detail ", 40)
	got := schemaDescription(map[string]any{"description": raw})

	if strings.ContainsAny(got, "\n\t") {
		t.Errorf("description = %q, want newlines and tabs collapsed", got)
	}
	if !strings.Contains(got, "0 = Self, 1 = all, 2 = certain.") {
		t.Errorf("description = %q, want enum docs intact", got)
	}
	if len(got) <= 200 {
		t.Errorf("description length = %d, want the full text preserved", len(got))
	}
}

func TestCompactLines_Formatting(t *testing.T) {
	lines := compactLines([]flatField{
		{Path: "name", Type: "string", Required: true},
		{Path: "audience.person_filters", Type: "string", Required: false},
		{Path: "audience.type", Type: "integer", Required: true, Enum: []string{"1", "2"}, Description: "Audience selector"},
	})
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3", len(lines))
	}
	if lines[0] != "name  string" {
		t.Errorf("required line = %q, want %q", lines[0], "name  string")
	}
	if lines[1] != "audience.person_filters  string?" {
		t.Errorf("optional line = %q, want a ? suffix", lines[1])
	}
	for _, want := range []string{"audience.type  integer", "enum: 1|2", "Audience selector"} {
		if !strings.Contains(lines[2], want) {
			t.Errorf("line %q missing %q", lines[2], want)
		}
	}
}

func TestRouteDetail_CompactReplacesRawBody(t *testing.T) {
	r := &routes.Route{
		Resource: "widgets", Method: "create", HTTPMethod: "POST",
		Path: "/v1/environments/{environment_id}/widgets", Summary: "Create widget", HasBody: true,
		PathParams:          []routes.RouteParam{{Name: "environment_id", Type: "integer", Required: true, Description: "The workspace (environment) ID"}},
		RequestBodySchema:   json.RawMessage(`{"type":"object","required":["widget"],"properties":{"widget":{"type":"object","properties":{"id":{"type":"integer"}}}}}`),
		RequestBodyRequired: true,
		ResponseSchemas:     map[string]json.RawMessage{"200": json.RawMessage(`{"type":"object","properties":{"ok":{"type":"boolean"}}}`)},
	}

	full := routeDetail(r, false)
	if _, ok := full["request_body_schema"]; !ok {
		t.Error("non-compact must keep the raw JSON schema")
	}
	if _, ok := full["request_body"]; ok {
		t.Error("non-compact must not emit flattened lines")
	}

	compact := routeDetail(r, true)
	if _, ok := compact["request_body_schema"]; ok {
		t.Error("compact must drop the raw JSON schema")
	}
	body, ok := compact["request_body"].([]string)
	if !ok {
		t.Fatalf("compact request_body = %T, want []string", compact["request_body"])
	}
	if !slices.Contains(body, "widget.id  integer?") {
		t.Errorf("request_body = %v, want a flattened widget.id line", body)
	}
	if compact["request_body_required"] != true {
		t.Errorf("request_body_required = %v, want true", compact["request_body_required"])
	}

	pp, ok := compact["path_params"].([]string)
	if !ok {
		t.Fatalf("compact path_params = %T, want []string", compact["path_params"])
	}
	if !slices.Contains(pp, "environment_id  integer  The workspace (environment) ID") {
		t.Errorf("path_params = %v, want a flattened environment_id line", pp)
	}

	resp, ok := compact["responses"].(map[string][]string)
	if !ok {
		t.Fatalf("compact responses = %T, want map[string][]string", compact["responses"])
	}
	if !slices.Contains(resp["200"], "ok  boolean?") {
		t.Errorf("responses[200] = %v, want a flattened ok line", resp["200"])
	}
	if _, ok := compact["response_schemas"]; ok {
		t.Error("compact must drop response_schemas")
	}
}

// A discriminated union carries a different single-value enum per member (the
// real newsletter update body has 13). Collapsing to the first member's enum
// would present one legal value where 13 exist, so the merge unions them.
func TestFlattenSchema_UnionsEnumsAcrossMembers(t *testing.T) {
	raw := json.RawMessage(`{"oneOf":[
		{"type":"object","required":["update_type"],"properties":{"update_type":{"type":"string","enum":["main"]},"name":{"type":"string"}}},
		{"type":"object","required":["update_type"],"properties":{"update_type":{"type":"string","enum":["tracking"]},"conversion":{"type":"boolean"}}},
		{"type":"object","required":["update_type"],"properties":{"update_type":{"type":"string","enum":["main","pause"]}}}
	]}`)
	fields := flattenSchema(raw)

	var got []string
	for _, f := range fields {
		if f.Path == "update_type" {
			got = append(got, f.Enum...)
		}
	}
	want := []string{"main", "tracking", "pause"}
	if len(got) != len(want) {
		t.Fatalf("update_type enum = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("update_type enum = %v, want %v", got, want)
		}
	}
}

// A member that documents a field the earlier member left undescribed should
// supply the description rather than lose it to first-seen order.
func TestFlattenSchema_MergeFillsMissingDescription(t *testing.T) {
	raw := json.RawMessage(`{"oneOf":[
		{"type":"object","properties":{"id":{"type":"string"}}},
		{"type":"object","properties":{"id":{"type":"string","description":"Newsletter identifier"}}}
	]}`)
	fields := flattenSchema(raw)

	for _, f := range fields {
		if f.Path == "id" {
			if f.Description != "Newsletter identifier" {
				t.Fatalf("id description = %q, want the later member's text", f.Description)
			}
			return
		}
	}
	t.Fatal("id field missing")
}

// Flattening enumerates one line per leaf path, so a schema whose components are
// shared across branches can flatten larger than the schema it replaces. Compact
// is a size optimisation, so it must fall back rather than return the bigger of
// the two, and it must say that it did.
func TestRouteDetail_CompactFallsBackWhenLarger(t *testing.T) {
	// A deep chain with a wide object at the bottom. The schema states each
	// level's name once; the flattened form repeats the whole dotted prefix on
	// every leaf line, which is what makes it the larger of the two.
	var leaves []string
	for i := range 40 {
		leaves = append(leaves, fmt.Sprintf(`"field%02d":{"type":"string"}`, i))
	}
	nested := `{"type":"object","properties":{` + strings.Join(leaves, ",") + `}}`
	for i := range 30 {
		nested = fmt.Sprintf(`{"type":"object","properties":{"nested_level_%02d":`, i) + nested + `}}`
	}
	r := &routes.Route{
		Resource: "x", Method: "create", HTTPMethod: "POST", Path: "/v1/x", HasBody: true,
		RequestBodySchema: json.RawMessage(nested), RequestBodyRequired: true,
	}

	detail := routeDetail(r, true)

	if _, ok := detail["request_body"]; ok {
		t.Fatal("compact rendering was kept even though it is larger than the schema")
	}
	if _, ok := detail["request_body_schema"]; !ok {
		t.Fatal("the schema was not returned as the fallback")
	}
	reason, ok := detail["compact_skipped"].(string)
	if !ok || reason == "" {
		t.Fatal("the fallback did not explain itself")
	}
}

// The common case must be untouched: when flattening is smaller, compact wins
// and no fallback marker appears.
func TestRouteDetail_CompactKeptWhenSmaller(t *testing.T) {
	r := &routes.Route{
		Resource: "x", Method: "create", HTTPMethod: "POST", Path: "/v1/x", HasBody: true,
		RequestBodySchema:   json.RawMessage(`{"type":"object","required":["name"],"properties":{"name":{"type":"string","description":"A reasonably long description that makes the schema bigger than its flattened form."}}}`),
		RequestBodyRequired: true,
	}

	detail := routeDetail(r, true)

	if _, ok := detail["request_body"]; !ok {
		t.Fatal("compact rendering was dropped for a schema it shrinks")
	}
	if _, ok := detail["compact_skipped"]; ok {
		t.Fatal("compact_skipped set on a body that rendered compact")
	}
}
