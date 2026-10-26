package cmd

import (
	"github.com/spf13/cobra"
)

var billingCmd = &cobra.Command{
	Use:   "billing",
	Short: "Check and add funds on a Flex account",
	// Hidden until the Flex plan launches. The commands still run.
	Hidden: true,
	Long: `Billing commands for Flex accounts, which pay for usage from a prepaid balance.

Examples:
  cio billing status
  cio billing add-funds`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return cmd.Help()
	},
}

func init() {
	rootCmd.AddCommand(billingCmd)
}
