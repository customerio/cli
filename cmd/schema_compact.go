package cmd

import (
	"bytes"
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	"github.com/customerio/cli/internal/routes"
)

// flatField is one leaf of a flattened JSON schema: a dotted path plus the
// minimum an agent needs to fill the field.
type flatField struct {
	Path        string
	Type        string
	Required    bool
	Enum        []string
	Description string
}

// flattenSchema turns a resolved JSON schema into a flat list of leaf fields:
// nested objects use dotted paths, arrays append "[]", and a surviving $ref
// becomes a leaf whose type names the component. It renders an already-resolved
// schema and does not resolve refs itself.
func flattenSchema(raw json.RawMessage) []flatField {
	return flattenSchemaRoot(raw, true)
}

// flattenSchemaRoot flattens a schema, marking a root-level leaf (a scalar,
// array, or ref body with no properties) required per rootRequired. For object
// bodies this is irrelevant: each field's optionality comes from the object's
// own required set. Callers pass the body's RequestBodyRequired so a root
// scalar's "?" matches the separate required flag rather than always showing
// optional.
func flattenSchemaRoot(raw json.RawMessage, rootRequired bool) []flatField {
	if len(raw) == 0 {
		return nil
	}
	// UseNumber keeps numeric values (e.g. enum members) as their exact source
	// text instead of float64, which would mangle them on re-formatting.
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var node any
	if err := dec.Decode(&node); err != nil {
		return nil
	}
	var out []flatField
	flattenNode(node, "", rootRequired, &out)
	return mergeByPath(out)
}

// mergeByPath collapses fields that flattened to the same dotted path, keeping
// first-seen order. oneOf/anyOf members flatten at the same path, so a field
// shared across members repeats — and a discriminator carries a different enum
// in each member. Union those enums: keeping only the first member's would
// render a 13-branch update_type as the single value "main", which reads as the
// only legal one. Optionality stays first-seen; a description fills in from a
// later member only if the first had none.
func mergeByPath(fields []flatField) []flatField {
	at := make(map[string]int, len(fields))
	merged := make([]flatField, 0, len(fields))
	for _, f := range fields {
		i, ok := at[f.Path]
		if !ok {
			at[f.Path] = len(merged)
			merged = append(merged, f)
			continue
		}
		merged[i].Enum = unionEnum(merged[i].Enum, f.Enum)
		if merged[i].Description == "" {
			merged[i].Description = f.Description
		}
	}
	return merged
}

// unionEnum appends the members of b that a does not already list, preserving
// order so the first member's values stay first.
func unionEnum(a, b []string) []string {
	if len(b) == 0 {
		return a
	}
	seen := make(map[string]bool, len(a)+len(b))
	for _, v := range a {
		seen[v] = true
	}
	for _, v := range b {
		if !seen[v] {
			seen[v] = true
			a = append(a, v)
		}
	}
	return a
}

func flattenNode(node any, path string, required bool, out *[]flatField) {
	m, ok := node.(map[string]any)
	if !ok {
		// A boolean schema (true/false) or other non-object node: emit a labeled
		// leaf so it is not silently dropped, including at the schema root.
		*out = append(*out, flatField{Path: leafName(path), Type: "any", Required: required})
		return
	}

	// A $ref that survived resolution: emit a leaf naming the component.
	if ref, ok := m["$ref"].(string); ok {
		*out = append(*out, flatField{Path: leafName(path), Type: refType(ref), Required: required, Description: schemaDescription(m)})
		return
	}

	descended := false

	// allOf is a conjunction: union the members' required lists so a required
	// entry in one member applies to a property defined in another, and skip
	// constraint-only members (required but nothing to flatten) so they emit no
	// phantom leaf.
	if members, ok := m["allOf"].([]any); ok && len(members) > 0 {
		req := unionRequired(members)
		for _, member := range members {
			if mm, isObj := member.(map[string]any); isObj {
				if len(req) > 0 {
					mm["required"] = req
				}
				if isConstraintOnly(mm) {
					continue
				}
			}
			flattenNode(member, path, required, out)
		}
		descended = true
	}
	// anyOf/oneOf are alternatives: flatten each member at the same path to show
	// the union of fields an agent might supply.
	for _, key := range []string{"anyOf", "oneOf"} {
		members, ok := m[key].([]any)
		if !ok || len(members) == 0 {
			continue
		}
		for _, member := range members {
			flattenNode(member, path, required, out)
		}
		descended = true
	}

	// Object: flatten each property under a dotted path. An empty properties map
	// has nothing to descend into and falls through to a leaf.
	if props, ok := m["properties"].(map[string]any); ok && len(props) > 0 {
		reqSet := stringSet(m["required"])
		names := make([]string, 0, len(props))
		for name := range props {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			childPath := name
			if path != "" {
				childPath = path + "." + name
			}
			flattenNode(props[name], childPath, reqSet[name], out)
		}
		descended = true
	}

	// Array: flatten the item schema under path[]. items may be a schema object
	// or a boolean (any element type), in which case the element is "any".
	if isArrayType(m) {
		if items, ok := m["items"].(map[string]any); ok {
			flattenNode(items, path+"[]", required, out)
		} else {
			*out = append(*out, flatField{Path: path + "[]", Type: "any", Required: required, Description: schemaDescription(m)})
		}
		descended = true
	}

	if descended {
		return
	}

	// Leaf: scalar, union type, or object with no descendable fields. A field
	// with only an enum infers its type from the enum members.
	leaf := fieldType(m)
	if leaf == "" {
		leaf = enumType(m["enum"])
	}
	if leaf == "" {
		leaf = "object"
	}
	*out = append(*out, flatField{
		Path:        leafName(path),
		Type:        leaf,
		Required:    required,
		Enum:        stringSlice(m["enum"]),
		Description: schemaDescription(m),
	})
}

// leafName labels a nameless leaf (a scalar or ref at the schema root).
func leafName(path string) string {
	if path == "" {
		return "(value)"
	}
	return path
}

// fieldType renders a schema's "type", which may be a string or a union array
// such as ["string","null"].
func fieldType(m map[string]any) string {
	switch t := m["type"].(type) {
	case string:
		return t
	case []any:
		parts := make([]string, 0, len(t))
		for _, e := range t {
			if s, ok := e.(string); ok {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, "|")
	}
	return ""
}

// isArrayType reports whether the schema's type is "array", including a union
// such as ["array","null"].
func isArrayType(m map[string]any) bool {
	switch t := m["type"].(type) {
	case string:
		return t == "array"
	case []any:
		for _, e := range t {
			if s, ok := e.(string); ok && s == "array" {
				return true
			}
		}
	}
	return false
}

// unionRequired collects the union of "required" entries across composition
// members, as a []any suitable for re-injecting into a member schema.
func unionRequired(members []any) []any {
	seen := map[string]bool{}
	var out []any
	for _, member := range members {
		mm, ok := member.(map[string]any)
		if !ok {
			continue
		}
		for _, r := range stringSlice(mm["required"]) {
			if !seen[r] {
				seen[r] = true
				out = append(out, r)
			}
		}
	}
	return out
}

// isConstraintOnly reports whether an allOf member carries a required list but
// nothing the flattener would render as a field (so it should not emit a leaf).
func isConstraintOnly(m map[string]any) bool {
	if _, ok := m["required"]; !ok {
		return false
	}
	if _, ok := m["$ref"]; ok {
		return false
	}
	if p, ok := m["properties"].(map[string]any); ok && len(p) > 0 {
		return false
	}
	if _, ok := m["items"]; ok {
		return false
	}
	for _, k := range []string{"allOf", "anyOf", "oneOf"} {
		if a, ok := m[k].([]any); ok && len(a) > 0 {
			return false
		}
	}
	return true
}

// enumType infers a field's type from its enum members when no explicit type is
// set. Returns "" when the members are mixed or not scalars.
func enumType(v any) string {
	arr, ok := v.([]any)
	if !ok || len(arr) == 0 {
		return ""
	}
	t := ""
	for _, e := range arr {
		var et string
		switch e.(type) {
		case nil:
			continue // null is nullability, not a type; skip for inference
		case string:
			et = "string"
		case json.Number:
			et = "number"
		case bool:
			et = "boolean"
		default:
			return ""
		}
		if t == "" {
			t = et
		} else if t != et {
			return ""
		}
	}
	return t
}

// paramEnum extracts a parameter schema's enum values for compact rendering.
func paramEnum(schema json.RawMessage) []string {
	if len(schema) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(schema))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return nil
	}
	return stringSlice(m["enum"])
}

// compactLines renders fields one per line. Optional fields get a "?" type
// suffix (absence of "?" means required).
func compactLines(fields []flatField) []string {
	lines := make([]string, 0, len(fields))
	for _, f := range fields {
		typ := f.Type
		if !f.Required {
			typ += "?"
		}
		parts := []string{f.Path, typ}
		if len(f.Enum) > 0 {
			parts = append(parts, "enum: "+strings.Join(f.Enum, "|"))
		}
		if f.Description != "" {
			parts = append(parts, f.Description)
		}
		lines = append(lines, strings.Join(parts, "  "))
	}
	return lines
}

// compactPathParamLines renders path parameters as one line each.
func compactPathParamLines(params []routes.RouteParam) []string {
	lines := make([]string, 0, len(params))
	for _, p := range params {
		lines = append(lines, compactParam(p.Name, p.Type, p.Required, p.Description, p.Schema))
	}
	return lines
}

// compactQueryParamLines renders query parameters as one line each.
func compactQueryParamLines(params []routes.QueryParam) []string {
	lines := make([]string, 0, len(params))
	for _, p := range params {
		lines = append(lines, compactParam(p.Name, p.Type, p.Required, p.Description, p.Schema))
	}
	return lines
}

func compactParam(name, typ string, required bool, description string, schema json.RawMessage) string {
	if !required {
		typ += "?"
	}
	parts := []string{name, typ}
	if enum := paramEnum(schema); len(enum) > 0 {
		parts = append(parts, "enum: "+strings.Join(enum, "|"))
	}
	if description != "" {
		parts = append(parts, description)
	}
	return strings.Join(parts, "  ")
}

func refType(ref string) string {
	name := ref
	if idx := strings.LastIndex(name, "/"); idx >= 0 {
		name = name[idx+1:]
	}
	// Component names are generated from package-qualified type names (e.g.
	// pkg_subpkg_Filter); keep the trailing type name as a readable hint.
	if idx := strings.LastIndex(name, "_"); idx >= 0 && idx+1 < len(name) {
		name = name[idx+1:]
	}
	return "ref:" + name
}

func schemaDescription(m map[string]any) string {
	d, _ := m["description"].(string)
	// Collapse whitespace so the field stays on one line; keep the full text so
	// enum values and constraints reach the agent intact.
	return strings.Join(strings.Fields(d), " ")
}

func stringSet(v any) map[string]bool {
	out := map[string]bool{}
	for _, s := range stringSlice(v) {
		out[s] = true
	}
	return out
}

func stringSlice(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, e := range arr {
		switch s := e.(type) {
		case string:
			out = append(out, s)
		case json.Number:
			out = append(out, s.String())
		case bool:
			out = append(out, strconv.FormatBool(s))
		case nil:
			out = append(out, "null")
		}
	}
	return out
}
