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

func TestBuildJSON(t *testing.T) {
	t.Run("arg binds a string value, no shell escaping needed", func(t *testing.T) {
		got, err := BuildJSON(`{template:{body:$h}}`, []string{`h=<x-base>Hi "there"</x-base>`}, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := `{"template":{"body":"<x-base>Hi \"there\"</x-base>"}}`
		if string(got) != want {
			t.Errorf("mismatch:\n want: %s\n got:  %s", want, got)
		}
	})

	t.Run("argjson binds parsed JSON, tostring nests it as a string", func(t *testing.T) {
		got, err := BuildJSON(`{body_json:($cfg|tostring)}`, nil, []string{`cfg={"priority":10}`})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := `{"body_json":"{\"priority\":10}"}`
		if string(got) != want {
			t.Errorf("mismatch:\n want: %s\n got:  %s", want, got)
		}
	})

	t.Run("multi-line arg value is preserved", func(t *testing.T) {
		got, err := BuildJSON(`{body:$h}`, []string{"h=line1\nline2"}, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if string(got) != `{"body":"line1\nline2"}` {
			t.Errorf("got: %s", got)
		}
	})

	t.Run("rejects binding without =", func(t *testing.T) {
		if _, err := BuildJSON(`{a:$x}`, []string{"x"}, nil); err == nil {
			t.Fatal("expected error for missing =")
		}
	})

	t.Run("rejects invalid argjson", func(t *testing.T) {
		if _, err := BuildJSON(`{a:$x}`, nil, []string{"x=not json"}); err == nil {
			t.Fatal("expected error for invalid JSON")
		}
	})
}
