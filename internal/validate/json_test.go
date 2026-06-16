package validate

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestValidateJSONPayload_Valid(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"simple object", `{"a":"b"}`},
		{"nested object", `{"template":{"subject":"hi"}}`},
		{"array value", `{"items":["a","b"]}`},
		{"unicode value", `{"name":"café"}`},
		{"emoji value", `{"note":"hi 👋"}`},
		// SELF-47: escaped control characters are valid JSON and must be accepted,
		// otherwise all multi-line content (HTML/plain-text bodies) is rejected.
		{"escaped newline", `{"template":{"subject":"line1\nline2"}}`},
		{"escaped tab", `{"template":{"subject":"col1\tcol2"}}`},
		{"escaped carriage return", `{"template":{"subject":"line1\rline2"}}`},
		{"unicode-escaped newline", "{\"template\":{\"subject\":\"line1\\u000aline2\"}}"},
		{"unicode-escaped NUL", "{\"template\":{\"subject\":\"a\\u0000b\"}}"},
		{"escaped control char in key", `{"a\nb":"c"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			parsed, err := ValidateJSONPayload(tc.raw)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			// The returned bytes are sent to the API verbatim, so they must be
			// valid UTF-8.
			if !utf8.Valid(parsed) {
				t.Errorf("returned payload is not valid UTF-8: %q", string(parsed))
			}
		})
	}
}

func TestValidateJSONPayload_Invalid(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		errWant string
	}{
		{"empty", ``, "must not be empty"},
		{"whitespace only", `   `, "must not be empty"},
		{"not json", `not json`, "not valid JSON"},
		{"array", `["a","b"]`, "must be an object"},
		{"string", `"hello"`, "must be an object"},
		{"number", `42`, "must be an object"},
		// An unescaped (literal) control character inside a string is invalid JSON
		// per the spec, so it is still rejected.
		{"literal NUL byte", "{\"a\":\"b\x00c\"}", "control character"},
		{"invalid utf-8", "{\"a\":\"\xff\"}", "invalid UTF-8"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ValidateJSONPayload(tc.raw)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.errWant)
			}
			if !strings.Contains(err.Error(), tc.errWant) {
				t.Errorf("error mismatch:\n  want substring: %q\n  got: %q", tc.errWant, err.Error())
			}
		})
	}
}
