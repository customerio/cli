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
		{"query string single pair", `email=a@b.com`, map[string]string{"email": "a@b.com"}},
		{"query string multiple pairs", `type=event&size=200`, map[string]string{"type": "event", "size": "200"}},
		{"query string encoded space", `event_type=credit%20builder%20-%20purchase%20made`, map[string]string{"event_type": "credit builder - purchase made"}},
		{"query string percent escapes", `q=a%20b%26c`, map[string]string{"q": "a b&c"}},
		{"query string empty value", `q=`, map[string]string{"q": ""}},
		{"query string value with equals", `filter=a=b`, map[string]string{"filter": "a=b"}},
		{"comma inside a plain value is fine", `name=Smith%2C%20Jane`, map[string]string{"name": "Smith, Jane"}},
		{"comma separated list value is fine", `ids=1%2C2%2C3`, map[string]string{"ids": "1,2,3"}},
		{"escaped semicolon in a value is fine", `q=a%3Bb`, map[string]string{"q": "a;b"}},
		{"escaped pair-looking value is fine", `q=rock%2C%20paper%3Dscissors`, map[string]string{"q": "rock, paper=scissors"}},
		{"bare word after a space is fine", `q=hello%20world`, map[string]string{"q": "hello world"}},
		{"query string trailing ampersand", `type=event&`, map[string]string{"type": "event"}},
		{"query string literal plus via %2B", `email=a%2Btag@b.com`, map[string]string{"email": "a+tag@b.com"}},
		{"json value keeps literal plus", `{"email":"a+tag@b.com"}`, map[string]string{"email": "a+tag@b.com"}},
		{"bracketed key in json", `{"searchTagIds[]":"99"}`, map[string]string{"searchTagIds[]": "99"}},
		{"bracketed key in query string", `searchTagIds[]=99`, map[string]string{"searchTagIds[]": "99"}},
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
		{"not json shows usage hint", `not json`, `--params expects a JSON object`},
		{"malformed json is not treated as query string", `{"a"="b"}`, "not valid JSON"},
		{"query string duplicate key", `id=1&id=2`, "appears more than once"},
		{"query string bad percent escape", `q=%zz`, "invalid percent-escape"},
		{"query string bad key", `bad-key=1`, "invalid characters"},
		{"unencoded ampersand hints at %26", `q=tom&jerry`, "encode a literal & as %26"},
		{"query string bare plus rejected", `email=a+tag@b.com`, "ambiguous"},
		{"comma separated pairs rejected", `environment_id=198048,email=a@b.com`, "separate parameters with &"},
		{"comma separated pairs with space", `type=event, size=200`, "separate parameters with &"},
		{"semicolon separated pairs rejected", `type=seg_attr;size=200`, "separate parameters with &"},
		{"space separated pairs rejected", `environment_id=198048 tag_id=1`, "separate parameters with &"},
		{"misseparated error names the escapes", `type=event;size=200`, "%2C for a literal comma, %3B for a semicolon, %20 for a space"},
		{"query string bare plus as space rejected", `q=hello+world`, "%2B for a literal plus or %20 for a space"},
		{"query string control character", "q=a%00b", "control character"},
		{"json array shows usage hint", `["a","b"]`, `--params expects a JSON object`},
		{"indexed bracket key", `{"searchTagIds[0]":"99"}`, "invalid characters"},
		{"nested bracket key", `{"a[]b":"x"}`, "invalid characters"},
		{"invalid key points at cio schema", `{"bad-key":"x"}`, "cio schema"},
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
