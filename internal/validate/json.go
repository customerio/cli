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

	// Validate UTF-8 explicitly. The payload is sent to the API verbatim, and
	// json.Unmarshal does NOT reject invalid UTF-8 inside string values — for a
	// json.RawMessage it copies the bytes through unchanged — so this scan is
	// what guarantees the bytes we send are valid UTF-8. It also rejects
	// unescaped control characters (other than \n, \r, \t); escaped control
	// characters (\n, \t, \r, \uXXXX) are valid JSON and pass through.
	for i := 0; i < len(raw); {
		r, size := utf8.DecodeRuneInString(raw[i:])
		if r == utf8.RuneError && size <= 1 {
			return nil, &JSONValidationError{
				Reason: fmt.Sprintf("JSON payload contains invalid UTF-8 at byte position %d", i),
			}
		}
		if r < 0x20 && r != '\n' && r != '\r' && r != '\t' {
			return nil, &JSONValidationError{
				Reason: fmt.Sprintf("JSON payload contains an unescaped control character at byte position %d (U+%04X)", i, r),
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

	// parsed is the raw bytes verbatim, already confirmed valid UTF-8 by the
	// scan above. Escaped control characters (\n, \t, \r, \uXXXX) remain — they
	// are valid JSON and required for multi-line content such as template bodies.
	return parsed, nil
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
