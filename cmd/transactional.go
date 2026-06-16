package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/customerio/cli/internal/output"
	"github.com/customerio/cli/internal/validate"
	"github.com/spf13/cobra"
)

var transactionalCmd = &cobra.Command{
	Use:   "transactional",
	Short: "Manage and send transactional messages",
	Long: `Manage and send transactional messages.

Transactional messages are pre-configured message templates that you trigger
via the API. Use 'send' to deliver a message using a template, or 'list' to
see available templates.

For one-off messages without a template, use 'cio send' instead.

Examples:
  cio transactional send email --environment-id 123 \
    --transactional-message-id 1 \
    --to user@example.com \
    --message-data '{"name":"Alice"}'

  cio transactional list --environment-id 123`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return cmd.Help()
	},
}

// transactionalSendCmd is the 'cio transactional send' subgroup.
var transactionalSendCmd = &cobra.Command{
	Use:   "send",
	Short: "Send a transactional message",
	Long: `Send a transactional message via the Customer.io Track API.

Requires --transactional-message-id — the message is rendered using the
pre-configured template and delivered to the identified recipient.

For one-off messages without a template, use 'cio send' instead.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return cmd.Help()
	},
}

func init() {
	transactionalCmd.PersistentFlags().String("environment-id", "", "Workspace/environment ID")

	transactionalSendCmd.AddCommand(newSendEmailCmd(true))
	transactionalSendCmd.AddCommand(newTransactionalPushCmd())
	transactionalSendCmd.AddCommand(newTransactionalSMSCmd())
	transactionalSendCmd.AddCommand(newTransactionalInboxCmd())

	transactionalCmd.AddCommand(transactionalSendCmd)
	transactionalCmd.AddCommand(transactionalListCmd)

	rootCmd.AddCommand(transactionalCmd)
}

// ---------------------------------------------------------------------------
// Push
// ---------------------------------------------------------------------------

func newTransactionalPushCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "push",
		Short: "Send a transactional push notification",
		Long: `Send a transactional push notification via the Customer.io Track API.

Requires --transactional-message-id.

Endpoint: POST /v1/send/push`,
		RunE: func(cmd *cobra.Command, args []string) error {
			payload, err := buildPushPayload(cmd)
			if err != nil {
				return err
			}
			return runTrackSend(cmd, "/v1/send/push", payload)
		},
	}
	cmd.Flags().String("to", "", "Device token")
	cmd.Flags().String("title", "", "Push notification title")
	cmd.Flags().String("message", "", "Push notification body")
	cmd.Flags().String("image-url", "", "Image URL")
	cmd.Flags().String("link", "", "Deep link URL")
	cmd.Flags().String("identifiers", "", "Identifiers as JSON")
	cmd.Flags().String("message-data", "", "Template variables as JSON")
	cmd.Flags().String("transactional-message-id", "", "Transactional message ID or trigger name (required)")
	return cmd
}

func buildPushPayload(cmd *cobra.Command) (json.RawMessage, error) {
	payload, err := basePayloadFromFlags(cmd)
	if err != nil {
		return nil, err
	}
	for _, f := range [][2]string{
		{"to", "to"},
		{"title", "title"},
		{"message", "message"},
		{"image-url", "image_url"},
		{"link", "link"},
	} {
		if err := setIfChanged(cmd, payload, f[0], f[1]); err != nil {
			return nil, err
		}
	}
	if err := validateTxnID(payload, true); err != nil {
		return nil, err
	}
	if err := validateIdentifiers(payload); err != nil {
		return nil, err
	}
	return json.Marshal(payload)
}

// ---------------------------------------------------------------------------
// SMS
// ---------------------------------------------------------------------------

func newTransactionalSMSCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sms",
		Short: "Send a transactional SMS",
		Long: `Send a transactional SMS via the Customer.io Track API.

Requires --transactional-message-id.

Endpoint: POST /v1/send/sms`,
		RunE: func(cmd *cobra.Command, args []string) error {
			payload, err := buildSMSPayload(cmd)
			if err != nil {
				return err
			}
			return runTrackSend(cmd, "/v1/send/sms", payload)
		},
	}
	cmd.Flags().String("to", "", "Recipient phone number (E.164 format)")
	cmd.Flags().String("from", "", "Sender phone number (E.164 format)")
	cmd.Flags().String("identifiers", "", "Identifiers as JSON")
	cmd.Flags().String("message-data", "", "Template variables as JSON")
	cmd.Flags().String("transactional-message-id", "", "Transactional message ID or trigger name (required)")
	return cmd
}

func buildSMSPayload(cmd *cobra.Command) (json.RawMessage, error) {
	payload, err := basePayloadFromFlags(cmd)
	if err != nil {
		return nil, err
	}
	for _, f := range [][2]string{{"to", "to"}, {"from", "from"}} {
		if err := setIfChanged(cmd, payload, f[0], f[1]); err != nil {
			return nil, err
		}
	}
	if err := validateTxnID(payload, true); err != nil {
		return nil, err
	}
	if err := validateIdentifiers(payload); err != nil {
		return nil, err
	}
	return json.Marshal(payload)
}

// ---------------------------------------------------------------------------
// Inbox (in-app)
// ---------------------------------------------------------------------------

func newTransactionalInboxCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "inbox",
		Short: "Send a transactional in-app message",
		Long: `Send a transactional in-app message via the Customer.io Track API.

Requires --transactional-message-id.

Endpoint: POST /v1/send/inbox_message`,
		RunE: func(cmd *cobra.Command, args []string) error {
			payload, err := buildInboxPayload(cmd)
			if err != nil {
				return err
			}
			return runTrackSend(cmd, "/v1/send/inbox_message", payload)
		},
	}
	cmd.Flags().String("to", "", "Recipient")
	cmd.Flags().String("identifiers", "", "Identifiers as JSON")
	cmd.Flags().String("message-data", "", "Template variables as JSON")
	cmd.Flags().String("transactional-message-id", "", "Transactional message ID or trigger name (required)")
	return cmd
}

func buildInboxPayload(cmd *cobra.Command) (json.RawMessage, error) {
	payload, err := basePayloadFromFlags(cmd)
	if err != nil {
		return nil, err
	}
	if err := setIfChanged(cmd, payload, "to", "to"); err != nil {
		return nil, err
	}
	if err := validateTxnID(payload, true); err != nil {
		return nil, err
	}
	if err := validateIdentifiers(payload); err != nil {
		return nil, err
	}
	return json.Marshal(payload)
}

// ---------------------------------------------------------------------------
// transactional list
// ---------------------------------------------------------------------------

var transactionalListCmd = &cobra.Command{
	Use:   "list",
	Short: "List transactional messages",
	Long: `List transactional message templates in a workspace.

Returns all transactional messages configured in the specified environment.

Endpoint: GET /v1/environments/{environment_id}/transactional_messages`,
	RunE: runTransactionalList,
}

func runTransactionalList(cmd *cobra.Command, args []string) error {
	c := clientFromCmd(cmd)
	if c == nil {
		return errNoClient(cmd)
	}

	envID, _ := cmd.Flags().GetString("environment-id")
	if envID == "" {
		envID = os.Getenv("CIO_ENVIRONMENT_ID")
	}
	if envID == "" {
		err := fmt.Errorf("--environment-id is required (or set CIO_ENVIRONMENT_ID)")
		output.PrintError(output.CodeValidationError, err.Error(), nil)
		return err
	}
	if err := validate.ValidateResourceID(envID); err != nil {
		output.PrintError(output.CodeValidationError, fmt.Sprintf("invalid environment-id: %s", err.Error()), nil)
		return err
	}

	path := fmt.Sprintf("/v1/environments/%s/transactional_messages", envID)
	jq := GetJQFlag(cmd)

	if GetDryRun(cmd) {
		dryRun := map[string]any{
			"dry_run": true,
			"method":  "GET",
			"url":     c.BaseURL() + path,
			"headers": map[string]string{
				"Authorization": "Bearer [REDACTED]",
			},
		}
		return output.FprintJSON(cmd.OutOrStdout(), dryRun)
	}

	page, limit, pageAll := GetPaginationFlags(cmd)
	params := make(map[string]string)
	if page > 0 {
		params["page"] = fmt.Sprintf("%d", page)
	}
	if limit > 0 {
		params["limit"] = fmt.Sprintf("%d", limit)
	}

	if pageAll {
		return doPageAll(cmd, c, path, params, page, limit)
	}

	result, err := c.Do(cmd.Context(), "GET", path, params, nil)
	if err != nil {
		return handleAPIError(err)
	}

	return output.FprintProcess(cmd.OutOrStdout(), result, jq, GetRawFlag(cmd))
}
