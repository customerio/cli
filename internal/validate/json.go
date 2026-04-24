package validate

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

// ValidateJSONPayload validates a raw JSON string for use as a request body.
func ValidateJSONPayload(raw string) (json.RawMessage, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, &JSONValidationError{Reason: "JSON payload must not be empty"}
	}

	for i := 0; i < len(raw); {
		r, size := utf8.DecodeRuneInString(raw[i:])
		if r == utf8.RuneError && size <= 1 {
			return nil, &JSONValidationError{
				Reason: fmt.Sprintf("JSON payload contains invalid UTF-8 at byte position %d", i),
			}
		}
		if r < 0x20 && r != '\n' && r != '\r' && r != '\t' {
			return nil, &JSONValidationError{
				Reason: fmt.Sprintf("JSON payload contains control character at byte position %d (U+%04X)", i, r),
			}
		}
		i += size
	}

	var parsed json.RawMessage
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil, &JSONValidationError{
			Reason: fmt.Sprintf("JSON payload is not valid JSON: %s", err.Error()),
		}
	}

	trimmed := strings.TrimSpace(string(parsed))
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, &JSONValidationError{
			Reason: "JSON payload must be an object (not array, string, number, etc.)",
		}
	}

	if err := checkControlCharsInStrings(parsed); err != nil {
		return nil, err
	}

	return parsed, nil
}

func checkControlCharsInStrings(data json.RawMessage) error {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err == nil {
		for key, val := range obj {
			if err := stringHasControlChars(key); err != nil {
				return err
			}
			if err := checkControlCharsInStrings(val); err != nil {
				return err
			}
		}
		return nil
	}

	var arr []json.RawMessage
	if err := json.Unmarshal(data, &arr); err == nil {
		for _, val := range arr {
			if err := checkControlCharsInStrings(val); err != nil {
				return err
			}
		}
		return nil
	}

	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		return stringHasControlChars(s)
	}

	return nil
}

func stringHasControlChars(s string) error {
	for i, r := range s {
		if r < 0x20 {
			return &JSONValidationError{
				Reason: fmt.Sprintf("JSON string value contains control character at position %d (U+%04X)", i, r),
			}
		}
	}
	return nil
}

// ValidateStringValue rejects string values containing control characters
// (U+0000–U+001F). Used for flag values that end up in request bodies.
func ValidateStringValue(flag, value string) error {
	for i, r := range value {
		if r < 0x20 {
			return &JSONValidationError{
				Reason: fmt.Sprintf("--%s contains control character at position %d (U+%04X)", flag, i, r),
			}
		}
	}
	return nil
}

// JSONValidationError provides structured details for JSON validation failures.
type JSONValidationError struct {
	Reason string `json:"reason"`
}

func (e *JSONValidationError) Error() string {
	return e.Reason
}
