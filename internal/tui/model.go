package tui

import (
	"io"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

const logoText = `█████▄▄           ▄▄▄▄▄▄▄
████████▄         ████████▄
 ▀█████████▄      ██████████▄
    ▀█████████▄   ████████████▄
       ▀████████▄ █████████████
       ▄████████▀ █████████████
    ▄█████████▀   ████████████▀
 ▄█████████▀      ██████████▀
████████▀         ████████▀
█████▀▀           ▀▀▀▀▀▀▀`

type command struct {
	name string
	desc string
}

// Command is a name/description pair for the "All commands" listing. Callers
// pass the live set of registered commands so the help screen never drifts out
// of sync with what the CLI actually supports.
type Command struct {
	Name string
	Desc string
}

// Logo returns the cio wordmark rendered in the brand green, suitable for
// printing as a banner above interactive flows. Pass the writer the logo will
// actually be printed to so color capability is detected for that destination.
func Logo(w io.Writer) string {
	r := lipgloss.NewRenderer(w)
	return r.NewStyle().Foreground(lipgloss.Color("#7FE07F")).Render(logoText)
}

// RenderHelp writes the branded help screen to w. all is the complete set of
// top-level commands, rendered under "All commands".
func RenderHelp(w io.Writer, all []Command) {
	r := lipgloss.NewRenderer(w)

	logo := r.NewStyle().Foreground(lipgloss.Color("#7FE07F"))
	tag := r.NewStyle().Foreground(lipgloss.Color("#FFFFFF"))
	header := r.NewStyle().Foreground(lipgloss.Color("#FFFFFF")).Bold(true)
	cmd := r.NewStyle().Foreground(lipgloss.Color("#7FE07F"))
	desc := r.NewStyle().Foreground(lipgloss.Color("#FFFFFF"))

	var b strings.Builder

	b.WriteString(logo.Render(logoText))
	b.WriteString("\n\n")
	b.WriteString(tag.Render("» Customer.io CLI"))
	b.WriteString("\n\n")

	b.WriteString(header.Render("Get started:") + "\n")
	for _, c := range []command{
		{"cio auth login", "Authenticate with your workspace"},
		{"cio auth signup", "Create a new account"},
	} {
		b.WriteString("  " + cmd.Render(c.name) + " " + desc.Render(c.desc) + "\n")
	}
	b.WriteString("\n")

	b.WriteString(header.Render("Common commands:") + "\n")
	for _, c := range []command{
		{"cio api <path>", "Make an API request"},
		{"cio send email", "Send a one-off email"},
		{"cio domains list", "List sending domains"},
		{"cio <command> --help", "Get help for a command"},
	} {
		b.WriteString("  " + cmd.Render(c.name) + " " + desc.Render(c.desc) + "\n")
	}

	if len(all) > 0 {
		width := 0
		for _, c := range all {
			if len(c.Name) > width {
				width = len(c.Name)
			}
		}
		b.WriteString("\n")
		b.WriteString(header.Render("All commands:") + "\n")
		for _, c := range all {
			pad := strings.Repeat(" ", width-len(c.Name)+2)
			b.WriteString("  " + cmd.Render(c.Name) + pad + desc.Render(c.Desc) + "\n")
		}
	}

	io.WriteString(w, b.String())
}
