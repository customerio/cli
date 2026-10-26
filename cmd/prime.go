package cmd

import (
	_ "embed"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

//go:embed prime_context.md
var primeContext string

// defaultCLILoginURL is the token page URL written into prime_context.md.
// prime swaps it for the resolved URL so non-production profiles see their
// own token page.
const defaultCLILoginURL = "https://fly.customer.io/cli"

var primeCmd = &cobra.Command{
	Use:   "prime",
	Short: "Print LLM-ready instructions for using this CLI",
	Long: `Output a compact reference for AI agents on how to use the Customer.io CLI.

For detailed domain knowledge, use 'cio skills read <name>'.

Example:
  cio prime | pbcopy
  cio prime >> system_prompt.txt`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		loginURL := resolveCLILoginURL(resolveLoginBaseURL(cmd))
		_, err := fmt.Fprint(cmd.OutOrStdout(), strings.ReplaceAll(primeContext, defaultCLILoginURL, loginURL))
		return err
	},
}

func init() {
	rootCmd.AddCommand(primeCmd)
}
