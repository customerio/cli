package output

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestFprintProcess_Raw(t *testing.T) {
	cases := []struct {
		name string
		data string
		jq   string
		raw  bool
		want string
	}{
		{
			name: "raw string result is unquoted",
			data: `{"html":"<h1>Hi</h1>"}`,
			jq:   ".html",
			raw:  true,
			want: "<h1>Hi</h1>\n",
		},
		{
			name: "raw preserves multi-line value verbatim",
			data: `{"html":"line1\nline2"}`,
			jq:   ".html",
			raw:  true,
			want: "line1\nline2\n",
		},
		{
			name: "without raw, string stays JSON-quoted",
			data: `{"name":"Acme Inc"}`,
			jq:   ".name",
			raw:  false,
			want: "\"Acme Inc\"\n",
		},
		{
			name: "raw on non-string result falls back to compact JSON",
			data: `{"obj":{"a":1}}`,
			jq:   ".obj",
			raw:  true,
			want: "{\"a\":1}\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := FprintProcess(&buf, json.RawMessage(tc.data), tc.jq, tc.raw); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got := buf.String(); got != tc.want {
				t.Errorf("output mismatch:\n  want: %q\n  got:  %q", tc.want, got)
			}
		})
	}
}
