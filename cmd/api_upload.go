package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/customerio/cli/internal/client"
	"github.com/spf13/cobra"
)

// Matches the field every upload endpoint names its file part.
const defaultFilePartField = "file"

// [] is the conventional encoding for a repeated (multi-file) field.
var filePartFieldRegex = regexp.MustCompile(`^[a-zA-Z0-9_-]+(\[\])?$`)

func GetFileParts(cmd *cobra.Command) ([]client.FilePart, error) {
	bindings, _ := cmd.Flags().GetStringArray("file")
	if len(bindings) == 0 {
		return nil, nil
	}

	parts := make([]client.FilePart, 0, len(bindings))
	seen := make(map[string]bool, len(bindings))
	for _, binding := range bindings {
		field, path, err := splitFileBinding(binding)
		if err != nil {
			return nil, err
		}
		if seen[field] && !strings.HasSuffix(field, "[]") {
			return nil, fmt.Errorf("--file %s: field %q given more than once; name it %s[] to send several files under one field", binding, field, field)
		}
		seen[field] = true

		content, err := readUploadFile(path)
		if err != nil {
			return nil, fmt.Errorf("--file %s: %w", binding, err)
		}
		parts = append(parts, client.FilePart{
			Field:    field,
			Filename: filepath.Base(path),
			Content:  content,
		})
	}
	return parts, nil
}

// Bounded: a bare os.ReadFile would pull a mistyped path at a huge file entirely
// into memory only to reject it for size.
func readUploadFile(path string) ([]byte, error) {
	fh, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = fh.Close() }()

	content, err := io.ReadAll(io.LimitReader(fh, client.MaxUploadBytes+1))
	if err != nil {
		return nil, err
	}
	if len(content) == 0 {
		return nil, fmt.Errorf("file is empty")
	}
	if len(content) > client.MaxUploadBytes {
		return nil, fmt.Errorf("file is over the %d byte upload limit", client.MaxUploadBytes)
	}
	return content, nil
}

func splitFileBinding(binding string) (field, path string, err error) {
	if binding == "" {
		return "", "", fmt.Errorf("--file: missing value, expected [field=]@path")
	}
	// A leading @ means the whole value is a path, so a filename containing '='
	// is not mistaken for a field binding.
	if rest, ok := strings.CutPrefix(binding, "@"); ok {
		field, path = defaultFilePartField, rest
	} else if name, value, found := strings.Cut(binding, "="); found {
		field, path = name, strings.TrimPrefix(value, "@")
	} else {
		field, path = defaultFilePartField, binding
	}

	if !filePartFieldRegex.MatchString(field) {
		return "", "", fmt.Errorf("--file %s: field name %q may use letters, digits, underscores and hyphens, with an optional trailing [] for a repeated field", binding, field)
	}
	if path == "" {
		return "", "", fmt.Errorf("--file %s: missing filename", binding)
	}
	return field, path, nil
}

// Reusing --json spares an upload a second flag for its metadata. Scalars only:
// a nested value has no unambiguous form representation.
func formFieldsFromJSON(body json.RawMessage) (map[string]string, error) {
	if len(body) == 0 {
		return nil, nil
	}
	// UseNumber, not the default float64: a resource ID like 1234567890123456789
	// would otherwise be sent as 1234567890123456800 — wrong but plausible.
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var raw map[string]any
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("--json must be an object when --file is used: %w", err)
	}
	fields := make(map[string]string, len(raw))
	for name, v := range raw {
		switch value := v.(type) {
		case string:
			fields[name] = value
		case bool:
			fields[name] = strconv.FormatBool(value)
		case json.Number:
			fields[name] = value.String()
		case nil:
			fields[name] = ""
		default:
			return nil, fmt.Errorf("--json field %q is not a string, number or boolean; a multipart request cannot carry nested values", name)
		}
	}
	return fields, nil
}

// Names and sizes only: a dry run must not echo file contents.
func filePartsSummary(parts []client.FilePart) []map[string]any {
	out := make([]map[string]any, 0, len(parts))
	for _, p := range parts {
		out = append(out, map[string]any{
			"field":    p.Field,
			"filename": p.Filename,
			"size":     len(p.Content),
		})
	}
	return out
}
