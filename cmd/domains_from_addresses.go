package cmd

import (
	"encoding/json"
	"fmt"

	"github.com/customerio/cli/internal/output"
	"github.com/customerio/cli/internal/validate"
	"github.com/spf13/cobra"
)

var domainsFromAddressesCmd = &cobra.Command{
	Use:   "from_addresses",
	Short: "Manage from addresses (sender identities)",
	Long: `Manage from addresses (sender identities) for an environment.

From addresses are the email addresses recipients see when they receive
messages from Customer.io. Each from address must belong to a verified
sending domain.

Subcommands:
  list     List from addresses
  add      Add a from address
  update   Update a from address
  delete   Delete a from address`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return cmd.Help()
	},
}

var domainsFromAddressesListCmd = &cobra.Command{
	Use:   "list",
	Short: "List from addresses",
	Long: `List from addresses (sender identities) for an environment.

GET /v1/environments/:env_id/identities?type=email

Examples:
  cio domains --env-id 456 from_addresses list
  cio domains --env-id 456 from_addresses list --in-use
  cio domains --env-id 456 from_addresses list --jq '.identities[]'`,
	Args: cobra.NoArgs,
	RunE: runFromAddressesList,
}

var domainsFromAddressesAddCmd = &cobra.Command{
	Use:   "add",
	Short: "Add a from address",
	Long: `Add a from address (sender identity) to an environment.

POST /v1/environments/:env_id/identities

Examples:
  cio domains --env-id 456 from_addresses add --name "Support" --email support@example.com`,
	Args: cobra.NoArgs,
	RunE: runFromAddressesAdd,
}

var domainsFromAddressesUpdateCmd = &cobra.Command{
	Use:   "update <identity-id>",
	Short: "Update a from address",
	Long: `Update a from address (sender identity).

PUT /v1/environments/:env_id/identities/:identity_id

At least one of --name or --email must be provided.
Use 'cio domains from_addresses list' to find the identity ID.

Examples:
  cio domains --env-id 456 from_addresses update 123 --name "New Name"
  cio domains --env-id 456 from_addresses update 123 --email new@example.com`,
	Args: requireArg("identity-id"),
	RunE: runFromAddressesUpdate,
}

var domainsFromAddressesDeleteCmd = &cobra.Command{
	Use:   "delete <identity-id>",
	Short: "Delete a from address",
	Long: `Delete a from address (sender identity).

DELETE /v1/environments/:env_id/identities/:identity_id

Returns 204 on success.
Use 'cio domains from_addresses list' to find the identity ID.

Examples:
  cio domains --env-id 456 from_addresses delete 123`,
	Args: requireArg("identity-id"),
	RunE: runFromAddressesDelete,
}

func init() {
	domainsFromAddressesListCmd.Flags().Bool("in-use", false, "Filter to identities currently in use")

	domainsFromAddressesAddCmd.Flags().String("name", "", "Display name for the from address (required)")
	domainsFromAddressesAddCmd.Flags().String("email", "", "Email address, e.g. support@example.com (required)")

	domainsFromAddressesUpdateCmd.Flags().String("name", "", "New display name")
	domainsFromAddressesUpdateCmd.Flags().String("email", "", "New email address")

	domainsFromAddressesCmd.AddCommand(domainsFromAddressesListCmd)
	domainsFromAddressesCmd.AddCommand(domainsFromAddressesAddCmd)
	domainsFromAddressesCmd.AddCommand(domainsFromAddressesUpdateCmd)
	domainsFromAddressesCmd.AddCommand(domainsFromAddressesDeleteCmd)
	domainsCmd.AddCommand(domainsFromAddressesCmd)
}

func runFromAddressesList(cmd *cobra.Command, args []string) error {
	c := clientFromCmd(cmd)
	if c == nil {
		return errNoClient(cmd)
	}

	envID, err := resolveEnvID(cmd)
	if err != nil {
		return err
	}

	path := fmt.Sprintf("/v1/environments/%s/identities", envID)
	params := map[string]string{"type": "email"}

	if cmd.Flags().Changed("in-use") {
		inUse, _ := cmd.Flags().GetBool("in-use")
		if inUse {
			params["in_use"] = "true"
		} else {
			params["in_use"] = "false"
		}
	}

	if GetDryRun(cmd) {
		return output.FprintJSON(cmd.OutOrStdout(), map[string]any{
			"dry_run": true,
			"method":  "GET",
			"url":     c.BaseURL() + path,
			"params":  params,
		})
	}

	result, err := c.Do(cmd.Context(), "GET", path, params, nil)
	if err != nil {
		return handleAPIError(err)
	}
	return output.FprintProcess(cmd.OutOrStdout(), result, GetJQFlag(cmd), GetRawFlag(cmd))
}

func runFromAddressesAdd(cmd *cobra.Command, args []string) error {
	c := clientFromCmd(cmd)
	if c == nil {
		return errNoClient(cmd)
	}

	envID, err := resolveEnvID(cmd)
	if err != nil {
		return err
	}

	name, _ := cmd.Flags().GetString("name")
	email, _ := cmd.Flags().GetString("email")

	if name == "" {
		valErr := fmt.Errorf("--name is required")
		output.PrintError(output.CodeValidationError, valErr.Error(), map[string]any{
			"flag": "--name",
			"hint": `Example: --name "Support"`,
		})
		return valErr
	}
	if err := validateStringInput("name", name); err != nil {
		output.PrintError(output.CodeValidationError, err.Error(), map[string]any{"flag": "--name"})
		return err
	}
	if email == "" {
		valErr := fmt.Errorf("--email is required")
		output.PrintError(output.CodeValidationError, valErr.Error(), map[string]any{
			"flag": "--email",
			"hint": "Example: --email support@example.com",
		})
		return valErr
	}
	if err := validateStringInput("email", email); err != nil {
		output.PrintError(output.CodeValidationError, err.Error(), map[string]any{"flag": "--email"})
		return err
	}

	body, err := json.Marshal(map[string]any{
		"identity": map[string]any{
			"name":          name,
			"email":         email,
			"template_type": "email",
		},
	})
	if err != nil {
		return err
	}

	path := fmt.Sprintf("/v1/environments/%s/identities", envID)

	if GetDryRun(cmd) {
		return output.FprintJSON(cmd.OutOrStdout(), map[string]any{
			"dry_run": true,
			"method":  "POST",
			"url":     c.BaseURL() + path,
			"body":    json.RawMessage(body),
		})
	}

	result, err := c.Do(cmd.Context(), "POST", path, nil, body)
	if err != nil {
		return handleAPIError(err)
	}
	return output.FprintProcess(cmd.OutOrStdout(), result, GetJQFlag(cmd), GetRawFlag(cmd))
}

func runFromAddressesUpdate(cmd *cobra.Command, args []string) error {
	c := clientFromCmd(cmd)
	if c == nil {
		return errNoClient(cmd)
	}

	envID, err := resolveEnvID(cmd)
	if err != nil {
		return err
	}

	identityID := args[0]
	if err := validate.ValidateResourceID(identityID); err != nil {
		output.PrintError(output.CodeValidationError, err.Error(), map[string]any{
			"argument": "identity-id",
		})
		return err
	}

	name, _ := cmd.Flags().GetString("name")
	email, _ := cmd.Flags().GetString("email")

	if name == "" && email == "" {
		valErr := fmt.Errorf("at least one of --name or --email is required")
		output.PrintError(output.CodeValidationError, valErr.Error(), map[string]any{
			"hint": "Example: --name \"New Name\" or --email new@example.com",
		})
		return valErr
	}

	identity := map[string]any{}
	if name != "" {
		if err := validateStringInput("name", name); err != nil {
			output.PrintError(output.CodeValidationError, err.Error(), map[string]any{"flag": "--name"})
			return err
		}
		identity["name"] = name
	}
	if email != "" {
		if err := validateStringInput("email", email); err != nil {
			output.PrintError(output.CodeValidationError, err.Error(), map[string]any{"flag": "--email"})
			return err
		}
		identity["email"] = email
	}

	body, err := json.Marshal(map[string]any{
		"identity": identity,
	})
	if err != nil {
		return err
	}

	path := fmt.Sprintf("/v1/environments/%s/identities/%s", envID, identityID)

	if GetDryRun(cmd) {
		return output.FprintJSON(cmd.OutOrStdout(), map[string]any{
			"dry_run": true,
			"method":  "PUT",
			"url":     c.BaseURL() + path,
			"body":    json.RawMessage(body),
		})
	}

	result, err := c.Do(cmd.Context(), "PUT", path, nil, body)
	if err != nil {
		return handleAPIError(err)
	}
	return output.FprintProcess(cmd.OutOrStdout(), result, GetJQFlag(cmd), GetRawFlag(cmd))
}

func runFromAddressesDelete(cmd *cobra.Command, args []string) error {
	c := clientFromCmd(cmd)
	if c == nil {
		return errNoClient(cmd)
	}

	envID, err := resolveEnvID(cmd)
	if err != nil {
		return err
	}

	identityID := args[0]
	if err := validate.ValidateResourceID(identityID); err != nil {
		output.PrintError(output.CodeValidationError, err.Error(), map[string]any{
			"argument": "identity-id",
		})
		return err
	}

	path := fmt.Sprintf("/v1/environments/%s/identities/%s", envID, identityID)

	if GetDryRun(cmd) {
		return output.FprintJSON(cmd.OutOrStdout(), map[string]any{
			"dry_run": true,
			"method":  "DELETE",
			"url":     c.BaseURL() + path,
		})
	}

	_, err = c.Do(cmd.Context(), "DELETE", path, nil, nil)
	if err != nil {
		return handleAPIError(err)
	}
	return output.FprintJSON(cmd.OutOrStdout(), map[string]any{
		"deleted":     true,
		"identity_id": identityID,
	})
}
