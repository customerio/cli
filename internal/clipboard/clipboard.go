// Package clipboard reads the system clipboard by shelling out to the
// platform's clipboard tool, keeping the CLI free of cgo and extra
// dependencies.
package clipboard

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
)

// ErrNoTool means no clipboard tool was found on PATH. Typical in remote
// shells (SSH, containers), where the user's local clipboard isn't visible
// anyway.
var ErrNoTool = errors.New("no clipboard tool found on PATH")

type tool struct {
	name string
	args []string
}

// candidates returns the clipboard read commands for the given OS, in
// preference order.
func candidates(goos string) []tool {
	switch goos {
	case "darwin":
		return []tool{{name: "pbpaste"}}
	case "windows":
		return []tool{{name: "powershell", args: []string{"-NoProfile", "-Command", "Get-Clipboard"}}}
	default: // linux and other unixes
		return []tool{
			{name: "wl-paste", args: []string{"--no-newline"}},
			{name: "xclip", args: []string{"-o", "-selection", "clipboard"}},
			{name: "xsel", args: []string{"-b"}},
		}
	}
}

// Test seams.
var (
	lookPath   = exec.LookPath
	runCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, name, args...).Output()
	}
)

// Read returns the clipboard's text content using the first available
// clipboard tool. Returns ErrNoTool when none is installed.
func Read(ctx context.Context) (string, error) {
	for _, t := range candidates(runtime.GOOS) {
		if _, err := lookPath(t.name); err != nil {
			continue
		}
		out, err := runCommand(ctx, t.name, t.args...)
		if err != nil {
			return "", fmt.Errorf("%s failed: %w", t.name, err)
		}
		return string(out), nil
	}
	return "", ErrNoTool
}
