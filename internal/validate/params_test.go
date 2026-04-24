package validate

import (
	"strings"
	"testing"
)

func TestValidateParams_Valid(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want map[string]string
	}{
		{"string value", `{"name":"hello"}`, map[string]string{"name": "hello"}},
		{"integer value", `{"page":3}`, map[string]string{"page": "3"}},
		{"float value", `{"ratio":1.5}`, map[string]string{"ratio": "1.5"}},
		{"bool value", `{"enabled":true}`, map[string]string{"enabled": "true"}},
		{"null value skipped", `{"x":null,"y":"z"}`, map[string]string{"y": "z"}},
		{"value with spaces", `{"q":"hello world"}`, map[string]string{"q": "hello world"}},
		{"value with unicode", `{"name":"café"}`, map[string]string{"name": "café"}},
		{"value with reserved url chars", `{"q":"a&b=c#d?e"}`, map[string]string{"q": "a&b=c#d?e"}},
		{"value with emoji", `{"note":"hi 👋"}`, map[string]string{"note": "hi 👋"}},
		{"value at max length", `{"q":"` + strings.Repeat("a", MaxParamValueLength) + `"}`, map[string]string{"q": strings.Repeat("a", MaxParamValueLength)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ValidateParams(tc.raw)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("length mismatch: want %d, got %d (%v)", len(tc.want), len(got), got)
			}
			for k, v := range tc.want {
				if got[k] != v {
					t.Errorf("key %q: want %q, got %q", k, v, got[k])
				}
			}
		})
	}
}

func TestValidateParams_Invalid(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		errWant string
	}{
		{"empty", ``, "must not be empty"},
		{"whitespace only", `   `, "must not be empty"},
		{"not json", `not json`, "not valid JSON"},
		{"not object", `["a","b"]`, "must be a JSON object"},
		{"bad key with dash", `{"bad-key":"x"}`, "invalid characters"},
		{"bad key with space", `{"bad key":"x"}`, "invalid characters"},
		{"bad key with dot", `{"bad.key":"x"}`, "invalid characters"},
		{"nested object", `{"x":{"y":"z"}}`, "non-scalar value"},
		{"array value", `{"x":["a"]}`, "non-scalar value"},
		{"value with NUL byte (escaped)", `{"q":"foo\u0000bar"}`, "control character"},
		{"value with newline", "{\"q\":\"foo\\nbar\"}", "control character"},
		{"value with carriage return", "{\"q\":\"foo\\rbar\"}", "control character"},
		{"value with tab", "{\"q\":\"foo\\tbar\"}", "control character"},
		{"value with DEL", "{\"q\":\"foo\\u007fbar\"}", "control character"},
		{"value over max length", `{"q":"` + strings.Repeat("a", MaxParamValueLength+1) + `"}`, "exceeds maximum length"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ValidateParams(tc.raw)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.errWant)
			}
			if !strings.Contains(err.Error(), tc.errWant) {
				t.Errorf("error mismatch:\n  want substring: %q\n  got: %q", tc.errWant, err.Error())
			}
		})
	}
}
