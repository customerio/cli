package cmd

import (
	_ "embed"
	"fmt"

	"github.com/spf13/cobra"
)

//go:embed prime_context.md
var primeContext string

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
		_, err := fmt.Fprint(cmd.OutOrStdout(), primeContext)
		return err
	},
}

func init() {
	rootCmd.AddCommand(primeCmd)
}
