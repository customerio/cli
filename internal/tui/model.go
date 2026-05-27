package tui

import (
	"io"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

const logoText = ` ██████╗ ██╗  ██████╗
██╔════╝ ██║ ██╔═══██╗
██║      ██║ ██║   ██║
██║      ██║ ██║   ██║
╚██████╗ ██║ ╚██████╔╝
 ╚═════╝ ╚═╝  ╚═════╝`

type command struct {
	name string
	desc string
}

// RenderHelp writes the branded help screen to w.
func RenderHelp(w io.Writer) {
	r := lipgloss.NewRenderer(os.Stdout)

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

	io.WriteString(w, b.String())
}
