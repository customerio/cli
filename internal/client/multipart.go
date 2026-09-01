package client

import (
	"bytes"
	"fmt"
	"mime/multipart"
	"net/textproto"
	"sort"
)

// The API rejects anything larger, so failing here saves uploading megabytes
// only to be turned away.
const MaxUploadBytes = 25 * 1024 * 1024

type FilePart struct {
	Field    string
	Filename string
	Content  []byte
}

// Fields are written in sorted order so the encoding is reproducible.
func NewMultipartBody(files []FilePart, fields map[string]string) (*Body, error) {
	if len(files) == 0 {
		return nil, fmt.Errorf("multipart body requires at least one file part")
	}

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)

	for _, f := range files {
		if len(f.Content) > MaxUploadBytes {
			return nil, fmt.Errorf("%s: file is %d bytes, over the %d byte upload limit", f.Filename, len(f.Content), MaxUploadBytes)
		}
		h := textproto.MIMEHeader{}
		h.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename=%q`, f.Field, f.Filename))
		// No Content-Type: mime.TypeByExtension reads the OS table, so the same
		// .csv is text/csv on macOS and application/vnd.ms-excel on Windows —
		// which upload endpoints reject. They resolve the type from the filename
		// extension when the part declares none, so declaring nothing is both
		// deterministic and what a generic transport should assert.
		part, err := w.CreatePart(h)
		if err != nil {
			return nil, fmt.Errorf("encode file part %q: %w", f.Field, err)
		}
		if _, err := part.Write(f.Content); err != nil {
			return nil, fmt.Errorf("encode file part %q: %w", f.Field, err)
		}
	}

	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := w.WriteField(name, fields[name]); err != nil {
			return nil, fmt.Errorf("encode field %q: %w", name, err)
		}
	}

	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("close multipart body: %w", err)
	}
	return &Body{ContentType: w.FormDataContentType(), Bytes: buf.Bytes()}, nil
}
