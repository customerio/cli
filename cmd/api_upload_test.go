package cmd

import (
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/customerio/cli/internal/client"
)

// Echoes the parts back so a test asserts on the wire format, not just on flag parsing.
func uploadServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/service_accounts/oauth/token" {
			_, _ = w.Write([]byte(`{"access_token":"jwt-test-session","token_type":"Bearer","expires_in":3600}`))
			return
		}
		if r.Header.Get("Authorization") != "Bearer jwt-test-session" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mediaType != "multipart/form-data" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"not multipart"}`))
			return
		}

		out := map[string]any{"method": r.Method, "path": r.URL.Path}
		// Keyed by a slice: a repeated field name sends several parts, and keying
		// by name alone would silently drop all but the last.
		files := map[string][]any{}
		fields := map[string]string{}
		mr := multipart.NewReader(r.Body, params["boundary"])
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			body, _ := io.ReadAll(part)
			if part.FileName() != "" {
				files[part.FormName()] = append(files[part.FormName()], map[string]any{
					"filename":     part.FileName(),
					"content":      string(body),
					"content_type": part.Header.Get("Content-Type"),
				})
			} else {
				fields[part.FormName()] = string(body)
			}
		}
		out["files"] = files
		out["fields"] = fields
		data, _ := json.Marshal(out)
		_, _ = w.Write(data)
	}))
}

func writeTempFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

func TestAPIFileUpload_SendsMultipart(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	t.Setenv("CIO_TOKEN", "sa_live_test123")
	t.Setenv("CIO_ACCESS_TOKEN", "")
	server := uploadServer(t)
	defer server.Close()

	path := writeTempFile(t, "runbook.md", "# Runbook\n")
	stdout, _, err := executeCommand("api", "/v1/environments/{environment_id}/knowledge_source_library/upload",
		"--params", `{"environment_id":"456"}`,
		"--file", "@"+path,
		"--json", `{"name":"Ops runbook","description":"how to"}`,
		"--api-url", server.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var got struct {
		Method string `json:"method"`
		Path   string `json:"path"`
		Files  map[string][]struct {
			Filename    string `json:"filename"`
			Content     string `json:"content"`
			ContentType string `json:"content_type"`
		} `json:"files"`
		Fields map[string]string `json:"fields"`
	}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("parse response: %v (%s)", err, stdout)
	}

	if got.Method != "POST" {
		t.Errorf("method = %q, want POST (--file should default the method)", got.Method)
	}
	if got.Path != "/v1/environments/456/knowledge_source_library/upload" {
		t.Errorf("path = %q", got.Path)
	}
	parts, ok := got.Files["file"]
	if !ok || len(parts) != 1 {
		t.Fatalf("want exactly one part named \"file\": %+v", got.Files)
	}
	file := parts[0]
	if file.Filename != "runbook.md" {
		t.Errorf("filename = %q, want runbook.md", file.Filename)
	}
	if file.Content != "# Runbook\n" {
		t.Errorf("content = %q", file.Content)
	}
	if got.Fields["name"] != "Ops runbook" || got.Fields["description"] != "how to" {
		t.Errorf("fields = %+v, want --json to become form fields", got.Fields)
	}
}

func TestAPIFileUpload_NamedPartField(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	t.Setenv("CIO_TOKEN", "sa_live_test123")
	t.Setenv("CIO_ACCESS_TOKEN", "")
	server := uploadServer(t)
	defer server.Close()

	path := writeTempFile(t, "data.csv", "a,b\n1,2\n")
	stdout, _, err := executeCommand("api", "/v1/environments/{environment_id}/imports",
		"--params", `{"environment_id":"456"}`,
		"--file", "attachment=@"+path,
		"--api-url", server.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(stdout, `"attachment"`) {
		t.Errorf("part not named from the binding: %s", stdout)
	}
	// Asserting a resolved type here would assert whatever the local OS mime
	// table returns, which differs per platform — the point is that we send none.
	if !strings.Contains(stdout, `"content_type":""`) {
		t.Errorf("part should declare no Content-Type: %s", stdout)
	}
}

func TestAPIFileUpload_DryRunReportsPartsNotContents(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	t.Setenv("CIO_TOKEN", "sa_live_test123")
	t.Setenv("CIO_ACCESS_TOKEN", "")

	path := writeTempFile(t, "secrets.md", "topsecret contents")
	stdout, _, err := executeCommand("api", "/v1/environments/{environment_id}/knowledge_source_library/upload",
		"--params", `{"environment_id":"456"}`,
		"--file", "@"+path,
		"--json", `{"name":"Notes"}`,
		"--dry-run")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(stdout, "topsecret") {
		t.Errorf("dry run leaked file contents: %s", stdout)
	}
	for _, want := range []string{"multipart/form-data", "secrets.md", `"size"`, `"fields"`} {
		if !strings.Contains(stdout, want) {
			t.Errorf("dry run missing %q: %s", want, stdout)
		}
	}
}

func TestAPIFileUpload_Errors(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	t.Setenv("CIO_TOKEN", "sa_live_test123")
	t.Setenv("CIO_ACCESS_TOKEN", "")

	good := writeTempFile(t, "notes.md", "hello")
	empty := writeTempFile(t, "empty.md", "")
	oversize := writeTempFile(t, "big.txt", strings.Repeat("a", client.MaxUploadBytes+1))

	tests := []struct {
		name     string
		args     []string
		contains string
	}{
		{"missing file", []string{"--file", "@" + filepath.Join(tmpDir, "nope.md")}, "no such file"},
		{"empty file", []string{"--file", "@" + empty}, "file is empty"},
		{"bad field name", []string{"--file", "bad field=@" + good}, "letters, digits, underscores and hyphens"},
		{"missing path", []string{"--file", "field=@"}, "missing filename"},
		{"duplicate field", []string{"--file", "@" + good, "--file", "file=@" + good}, "more than once"},
		{"oversize file", []string{"--file", "@" + oversize}, "upload limit"},
		{"nested json field", []string{"--file", "@" + good, "--json", `{"meta":{"a":1}}`}, "nested values"},
		{"page-all", []string{"--file", "@" + good, "--page-all"}, "--page-all cannot be combined with --file"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := append([]string{"api", "/v1/environments/{environment_id}/uploads",
				"--params", `{"environment_id":"456"}`, "--api-url", "http://127.0.0.1:1"}, tt.args...)
			_, _, err := executeCommand(args...)
			if err == nil {
				t.Fatalf("expected an error")
			}
			if !strings.Contains(err.Error(), tt.contains) {
				t.Errorf("error = %q, want it to mention %q", err, tt.contains)
			}
		})
	}
}

// A dry run that reports "valid" for a request the real run rejects is worse
// than no check at all.
func TestAPIFileUpload_DryRunAppliesTheSameGuards(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	t.Setenv("CIO_TOKEN", "sa_live_test123")
	t.Setenv("CIO_ACCESS_TOKEN", "")

	good := writeTempFile(t, "notes.md", "hello")
	oversize := writeTempFile(t, "big.txt", strings.Repeat("a", client.MaxUploadBytes+1))

	tests := []struct {
		name     string
		args     []string
		contains string
	}{
		{"oversize", []string{"--file", "@" + oversize}, "upload limit"},
		{"page-all", []string{"--file", "@" + good, "--page-all"}, "--page-all cannot be combined with --file"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := append([]string{"api", "/v1/environments/{environment_id}/uploads",
				"--params", `{"environment_id":"456"}`, "--dry-run"}, tt.args...)
			_, _, err := executeCommand(args...)
			if err == nil {
				t.Fatalf("dry run reported valid for a request the real run rejects")
			}
			if !strings.Contains(err.Error(), tt.contains) {
				t.Errorf("error = %q, want it to mention %q", err, tt.contains)
			}
		})
	}
}

// [] is the conventional encoding for a repeated field, so it is the one name
// that may appear twice.
func TestAPIFileUpload_RepeatedBracketField(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	t.Setenv("CIO_TOKEN", "sa_live_test123")
	t.Setenv("CIO_ACCESS_TOKEN", "")
	server := uploadServer(t)
	defer server.Close()

	a := writeTempFile(t, "a.csv", "1\n")
	b := writeTempFile(t, "b.csv", "2\n")
	stdout, _, err := executeCommand("api", "/v1/environments/{environment_id}/uploads",
		"--params", `{"environment_id":"456"}`,
		"--file", "files[]=@"+a,
		"--file", "files[]=@"+b,
		"--api-url", server.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(stdout, "a.csv") || !strings.Contains(stdout, "b.csv") {
		t.Errorf("both files should be sent: %s", stdout)
	}
}

func TestSplitFileBinding(t *testing.T) {
	tests := []struct {
		binding   string
		wantField string
		wantPath  string
		wantErr   bool
	}{
		{"@notes.md", "file", "notes.md", false},
		{"notes.md", "file", "notes.md", false},
		{"doc=@notes.md", "doc", "notes.md", false},
		{"doc=notes.md", "doc", "notes.md", false},
		// A leading @ wins over the '=' split, so a filename can contain '='.
		{"@a=b.md", "file", "a=b.md", false},
		{"files[]=@a.csv", "files[]", "a.csv", false},
		{"source-file=@a.csv", "source-file", "a.csv", false},
		{"", "", "", true},
		{"=@notes.md", "", "", true},
		{"doc=", "", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.binding, func(t *testing.T) {
			field, path, err := splitFileBinding(tt.binding)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tt.wantErr)
			}
			if err != nil {
				return
			}
			if field != tt.wantField || path != tt.wantPath {
				t.Errorf("got (%q, %q), want (%q, %q)", field, path, tt.wantField, tt.wantPath)
			}
		})
	}
}

func TestFormFieldsFromJSON(t *testing.T) {
	fields, err := formFieldsFromJSON(json.RawMessage(`{"s":"x","n":12,"f":1.5,"b":true,"z":null}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := map[string]string{"s": "x", "n": "12", "f": "1.5", "b": "true", "z": ""}
	for k, v := range want {
		if fields[k] != v {
			t.Errorf("fields[%q] = %q, want %q", k, fields[k], v)
		}
	}

	if _, err := formFieldsFromJSON(json.RawMessage(`{"a":[1,2]}`)); err == nil {
		t.Error("expected an error for a nested value")
	}

	// Resource IDs exceed float64's exact range; a silently-rounded ID is worse
	// than an error.
	big, err := formFieldsFromJSON(json.RawMessage(`{"id":1234567890123456789}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if big["id"] != "1234567890123456789" {
		t.Errorf("id = %q, want it sent verbatim", big["id"])
	}
}
