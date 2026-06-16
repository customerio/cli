package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/customerio/cli/internal/client"
	"github.com/customerio/cli/internal/output"
	"github.com/spf13/cobra"
)

var profileCmd = &cobra.Command{
	Use:   "profile",
	Short: "Manage named configuration profiles",
	Long: `Manage named configuration profiles.

Each profile holds its own credentials, region, and optional API base URL,
letting you switch between accounts (e.g. production, staging, a client) without
re-authenticating. Select a profile per command with --profile, or set a default
with 'cio profile use'.

Profiles are stored in ~/.cio/config.json.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return cmd.Help()
	},
}

var profileListCmd = &cobra.Command{
	Use:   "list",
	Short: "List configured profiles",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		profiles, err := client.ListProfiles()
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				// A corrupt or unreadable config is a real error — surface it
				// rather than masquerading as "no profiles configured".
				output.PrintError(output.CodeGeneralError, err.Error(), nil)
				return err
			}
			// No config yet — report an empty list rather than an error.
			profiles = []client.ProfileInfo{}
		}
		data, _ := json.Marshal(map[string]any{"profiles": profiles})
		return output.FprintProcess(cmd.OutOrStdout(), json.RawMessage(data), GetJQFlag(cmd), GetRawFlag(cmd))
	},
}

var profileUseCmd = &cobra.Command{
	Use:   "use <name>",
	Short: "Set the default profile",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		if err := client.SetCurrentProfile(name); err != nil {
			output.PrintError(output.CodeValidationError, err.Error(), map[string]any{
				"hint": "Run 'cio profile list' to see available profiles, or 'cio auth login --profile " + name + "' to create one.",
			})
			return err
		}
		return output.FprintJSON(cmd.OutOrStdout(), map[string]any{
			"status":  "ok",
			"message": fmt.Sprintf("Switched current profile to %q", name),
			"profile": name,
		})
	},
}

var profileRemoveCmd = &cobra.Command{
	Use:   "remove <name>",
	Short: "Remove a profile",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		if err := client.RemoveProfile(name); err != nil {
			output.PrintError(output.CodeValidationError, err.Error(), nil)
			return err
		}
		return output.FprintJSON(cmd.OutOrStdout(), map[string]any{
			"status":  "ok",
			"message": fmt.Sprintf("Removed profile %q", name),
			"profile": name,
		})
	},
}

func init() {
	profileCmd.AddCommand(profileListCmd)
	profileCmd.AddCommand(profileUseCmd)
	profileCmd.AddCommand(profileRemoveCmd)
	rootCmd.AddCommand(profileCmd)
}
