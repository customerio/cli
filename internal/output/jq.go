package output

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/itchyny/gojq"
)

// BuildJSON evaluates program as a jq expression against null input with the
// given variable bindings and returns the single JSON result, mirroring
// `jq -n --arg / --argjson`. args bind string values, argjson bind parsed JSON
// values; each entry is "name=value". Lets a request body be built with the
// bundled gojq, so no external jq is needed to encode embedded markup.
func BuildJSON(program string, args, argjson []string) (json.RawMessage, error) {
	var names []string
	var values []any

	for _, kv := range args {
		name, val, err := splitArgBinding("arg", kv)
		if err != nil {
			return nil, err
		}
		names = append(names, "$"+name)
		values = append(values, val)
	}
	for _, kv := range argjson {
		name, val, err := splitArgBinding("argjson", kv)
		if err != nil {
			return nil, err
		}
		var parsed any
		if err := json.Unmarshal([]byte(val), &parsed); err != nil {
			return nil, fmt.Errorf("--argjson %s: value is not valid JSON: %w", name, err)
		}
		names = append(names, "$"+name)
		values = append(values, parsed)
	}

	query, err := gojq.Parse(program)
	if err != nil {
		return nil, fmt.Errorf("--json jq parse: %w", err)
	}
	code, err := gojq.Compile(query, gojq.WithVariables(names))
	if err != nil {
		return nil, fmt.Errorf("--json jq compile: %w", err)
	}

	iter := code.Run(nil, values...)
	v, ok := iter.Next()
	if !ok {
		return nil, fmt.Errorf("--json jq program produced no output")
	}
	if err, isErr := v.(error); isErr {
		return nil, fmt.Errorf("--json jq eval: %w", err)
	}
	// Encode without HTML escaping so markup (`<`, `>`, `&`) stays literal in the
	// body, matching what `jq -n` produces.
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, fmt.Errorf("--json marshal result: %w", err)
	}
	return json.RawMessage(bytes.TrimRight(buf.Bytes(), "\n")), nil
}

// splitArgBinding parses a "name=value" binding for --arg / --argjson.
func splitArgBinding(flag, kv string) (name, value string, err error) {
	name, value, found := strings.Cut(kv, "=")
	if !found || name == "" {
		return "", "", fmt.Errorf("--%s expects name=value, got %q", flag, kv)
	}
	return name, value, nil
}

// ApplyJQ applies a jq expression to the given JSON data and returns the results.
func ApplyJQ(data json.RawMessage, expr string) ([]json.RawMessage, error) {
	query, err := gojq.Parse(expr)
	if err != nil {
		return nil, fmt.Errorf("jq parse: %w", err)
	}

	var input any
	if err := json.Unmarshal(data, &input); err != nil {
		return nil, fmt.Errorf("jq unmarshal input: %w", err)
	}

	var results []json.RawMessage
	iter := query.Run(input)
	for {
		v, ok := iter.Next()
		if !ok {
			break
		}
		if err, isErr := v.(error); isErr {
			return nil, fmt.Errorf("jq eval: %w", err)
		}
		encoded, err := json.Marshal(v)
		if err != nil {
			return nil, fmt.Errorf("jq marshal result: %w", err)
		}
		results = append(results, encoded)
	}

	return results, nil
}
