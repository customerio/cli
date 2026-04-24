package output

import (
	"encoding/json"
	"fmt"

	"github.com/itchyny/gojq"
)

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
