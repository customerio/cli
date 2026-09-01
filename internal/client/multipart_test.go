package client

import (
	"bytes"
	"io"
	"mime"
	"mime/multipart"
	"strings"
	"testing"
)

func TestNewMultipartBody(t *testing.T) {
	body, err := NewMultipartBody(
		[]FilePart{{Field: "file", Filename: "notes.md", Content: []byte("hello")}},
		map[string]string{"name": "Notes", "description": "a file"},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	mediaType, params, err := mime.ParseMediaType(body.ContentType)
	if err != nil {
		t.Fatalf("parse content type %q: %v", body.ContentType, err)
	}
	if mediaType != "multipart/form-data" {
		t.Errorf("media type = %q", mediaType)
	}

	files := map[string]string{}
	fields := map[string]string{}
	mr := multipart.NewReader(bytes.NewReader(body.Bytes), params["boundary"])
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read part: %v", err)
		}
		content, _ := io.ReadAll(part)
		if part.FileName() != "" {
			files[part.FormName()] = part.FileName()
		} else {
			fields[part.FormName()] = string(content)
		}
	}
	if files["file"] != "notes.md" {
		t.Errorf("files = %v", files)
	}
	if fields["name"] != "Notes" || fields["description"] != "a file" {
		t.Errorf("fields = %v", fields)
	}
}

// The body is buffered, not streamed, so the retry loop and the 401 refresh can
// re-send it; nothing consumes it on the first attempt.
func TestNewMultipartBodyIsReusable(t *testing.T) {
	body, err := NewMultipartBody([]FilePart{{Field: "file", Filename: "a.txt", Content: []byte("x")}}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	first, _ := io.ReadAll(bytes.NewReader(body.Bytes))
	second, _ := io.ReadAll(bytes.NewReader(body.Bytes))
	if !bytes.Equal(first, second) || len(first) == 0 {
		t.Errorf("body is not re-readable: %d vs %d bytes", len(first), len(second))
	}
}

func TestNewMultipartBodyRejectsOversizeFile(t *testing.T) {
	_, err := NewMultipartBody(
		[]FilePart{{Field: "file", Filename: "big.txt", Content: bytes.Repeat([]byte("a"), MaxUploadBytes+1)}},
		nil,
	)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "upload limit") {
		t.Errorf("error = %q", err)
	}
}

func TestNewMultipartBodyRequiresAFile(t *testing.T) {
	if _, err := NewMultipartBody(nil, map[string]string{"name": "x"}); err == nil {
		t.Error("expected an error")
	}
}
