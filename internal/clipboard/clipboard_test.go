package clipboard

import (
	"context"
	"errors"
	"testing"
)

func TestCandidatesPerOS(t *testing.T) {
	tests := []struct {
		goos  string
		first string
		count int
	}{
		{"darwin", "pbpaste", 1},
		{"windows", "powershell", 1},
		{"linux", "wl-paste", 3},
		{"freebsd", "wl-paste", 3},
	}
	for _, tt := range tests {
		got := candidates(tt.goos)
		if len(got) != tt.count {
			t.Errorf("%s: expected %d candidates, got %d", tt.goos, tt.count, len(got))
		}
		if got[0].name != tt.first {
			t.Errorf("%s: expected first candidate %q, got %q", tt.goos, tt.first, got[0].name)
		}
	}
}

func TestReadNoToolOnPath(t *testing.T) {
	origLookPath := lookPath
	defer func() { lookPath = origLookPath }()
	lookPath = func(string) (string, error) { return "", errors.New("not found") }

	_, err := Read(context.Background())
	if !errors.Is(err, ErrNoTool) {
		t.Fatalf("expected ErrNoTool, got %v", err)
	}
}

func TestReadUsesFirstAvailableTool(t *testing.T) {
	origLookPath, origRun := lookPath, runCommand
	defer func() { lookPath, runCommand = origLookPath, origRun }()

	lookPath = func(name string) (string, error) { return "/usr/bin/" + name, nil }
	var ran string
	runCommand = func(_ context.Context, name string, _ ...string) ([]byte, error) {
		ran = name
		return []byte("sa_live_test\n"), nil
	}

	got, err := Read(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "sa_live_test\n" {
		t.Errorf("expected raw clipboard content, got %q", got)
	}
	if ran == "" {
		t.Error("expected a clipboard tool to run")
	}
}

func TestReadToolFailure(t *testing.T) {
	origLookPath, origRun := lookPath, runCommand
	defer func() { lookPath, runCommand = origLookPath, origRun }()

	lookPath = func(name string) (string, error) { return "/usr/bin/" + name, nil }
	runCommand = func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("display not available")
	}

	if _, err := Read(context.Background()); err == nil {
		t.Fatal("expected error when the tool fails")
	}
}
