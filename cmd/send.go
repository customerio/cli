package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/customerio/cli/internal/client"
	"github.com/customerio/cli/internal/output"
	"github.com/customerio/cli/internal/validate"
	"github.com/spf13/cobra"
)

var sendCmd = &cobra.Command{
	Use:   "send",
	Short: "Send one-off messages",
	Long: `Send a one-off email via the Customer.io Track API.

Provide message content via flags (--to, --from, --subject, --body) or as a
raw JSON payload via --json. Flags and --json can be combined — flags override
JSON fields. A transactional_message_id is optional.

For push, SMS, and in-app messages (which require a template), use
'cio transactional send' instead.

Examples:
  cio send email --environment-id 123 \
    --to user@example.com \
    --from "Acme <noreply@example.com>" \
    --subject "Hello World" \
    --body "<h1>Hi there</h1>"

  cio send email --environment-id 123 --json '{"transactional_message_id":1,"identifiers":{"email":"user@example.com"}}'`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return cmd.Help()
	},
}

func init() {
	sendCmd.PersistentFlags().String("environment-id", "", "Workspace/environment ID (sent as X-Workspace-Id header)")

	sendCmd.AddCommand(newSendEmailCmd(false))

	rootCmd.AddCommand(sendCmd)
}

// ---------------------------------------------------------------------------
// Email
// ---------------------------------------------------------------------------

func newSendEmailCmd(requireTxnID bool) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "email",
		Short: emailShort(requireTxnID),
		Long:  longForEmail(requireTxnID),
		RunE: func(cmd *cobra.Command, args []string) error {
			payload, err := buildEmailPayload(cmd, requireTxnID)
			if err != nil {
				return err
			}
			return runTrackSend(cmd, "/v1/send/email", payload)
		},
	}
	cmd.Flags().String("to", "", "Recipient email address")
	cmd.Flags().String("from", "", `Sender (e.g. "Acme <noreply@example.com>")`)
	cmd.Flags().String("subject", "", "Email subject line")
	cmd.Flags().String("body", "", "HTML body")
	cmd.Flags().String("text", "", "Plaintext body")
	cmd.Flags().String("reply-to", "", "Reply-to address")
	cmd.Flags().String("bcc", "", "BCC address(es)")
	cmd.Flags().String("identifiers", "", `Identifiers as JSON (default: inferred from --to as {"email":"..."})`)
	cmd.Flags().String("message-data", "", "Template variables as JSON")
	cmd.Flags().String("transactional-message-id", "", "Transactional message ID or trigger name")
	cmd.Flags().BoolP("watch", "w", false, "Poll delivery status after queuing and print the result when complete")
	return cmd
}

func buildEmailPayload(cmd *cobra.Command, requireTxnID bool) (json.RawMessage, error) {
	payload, err := basePayloadFromFlags(cmd)
	if err != nil {
		return nil, err
	}

	for _, f := range [][2]string{
		{"to", "to"},
		{"from", "from"},
		{"subject", "subject"},
		{"body", "body"},
		{"reply-to", "reply_to"},
		{"bcc", "bcc"},
	} {
		if err := setIfChanged(cmd, payload, f[0], f[1]); err != nil {
			return nil, err
		}
	}
	if v, _ := cmd.Flags().GetString("text"); v != "" {
		if err := validate.ValidateStringValue("text", v); err != nil {
			output.PrintError(output.CodeValidationError, err.Error(), nil)
			return nil, err
		}
		payload["body_plain"] = v
		// The Track API requires "body" even when sending plaintext-only.
		// Auto-set body to the plaintext content if not already provided.
		if _, hasBody := payload["body"]; !hasBody {
			payload["body"] = v
		}
	}

	// Auto-infer identifiers from --to for email.
	if _, ok := payload["identifiers"]; !ok {
		if to, ok := payload["to"].(string); ok && to != "" {
			payload["identifiers"] = map[string]string{"email": to}
		}
	}

	if err := validateTxnID(payload, requireTxnID); err != nil {
		return nil, err
	}

	if err := validateIdentifiers(payload); err != nil {
		return nil, err
	}

	return json.Marshal(payload)
}

func emailShort(requireTxnID bool) string {
	if requireTxnID {
		return "Send a transactional email"
	}
	return "Send a one-off email"
}

func longForEmail(requireTxnID bool) string {
	if requireTxnID {
		return `Send a transactional email via the Customer.io Track API.

Requires --transactional-message-id. The template provides default content;
flags override specific fields.

Endpoint: POST /v1/send/email`
	}
	return `Send an email via the Customer.io Track API.

Provide message content via flags or --json. For email, --to auto-infers
the identifier as {"email":"<to>"} unless --identifiers is set explicitly.

Examples:
  cio send email --environment-id 123 \
    --to user@example.com \
    --from "Acme <noreply@example.com>" \
    --subject "Hello World" \
    --body "<h1>Hi</h1>"

  cio send email --environment-id 123 \
    --to user@example.com \
    --from noreply@example.com \
    --subject "Hello" \
    --text "It works!"

Endpoint: POST /v1/send/email`
}

// ---------------------------------------------------------------------------
// Shared helpers
// ---------------------------------------------------------------------------

// basePayloadFromFlags starts with --json (if provided) and merges common
// flags (--identifiers, --message-data, --transactional-message-id).
func basePayloadFromFlags(cmd *cobra.Command) (map[string]any, error) {
	payload := make(map[string]any)

	// Start with --json if provided.
	if jsonBody, _ := GetJSONBody(cmd); jsonBody != nil {
		if err := json.Unmarshal(jsonBody, &payload); err != nil {
			output.PrintError(output.CodeValidationError, fmt.Sprintf("invalid --json: %s", err.Error()), nil)
			return nil, err
		}
	}

	// --identifiers (JSON string — validated for control chars like --json)
	if raw, _ := cmd.Flags().GetString("identifiers"); raw != "" {
		if _, err := validate.ValidateJSONPayload(raw); err != nil {
			err = fmt.Errorf("invalid --identifiers: %w", err)
			output.PrintError(output.CodeValidationError, err.Error(), nil)
			return nil, err
		}
		var idents map[string]any
		if err := json.Unmarshal([]byte(raw), &idents); err != nil {
			err = fmt.Errorf("invalid --identifiers JSON: %w", err)
			output.PrintError(output.CodeValidationError, err.Error(), nil)
			return nil, err
		}
		payload["identifiers"] = idents
	}

	// --message-data (JSON string — validated for control chars like --json)
	if raw, _ := cmd.Flags().GetString("message-data"); raw != "" {
		if _, err := validate.ValidateJSONPayload(raw); err != nil {
			err = fmt.Errorf("invalid --message-data: %w", err)
			output.PrintError(output.CodeValidationError, err.Error(), nil)
			return nil, err
		}
		var data map[string]any
		if err := json.Unmarshal([]byte(raw), &data); err != nil {
			err = fmt.Errorf("invalid --message-data JSON: %w", err)
			output.PrintError(output.CodeValidationError, err.Error(), nil)
			return nil, err
		}
		payload["message_data"] = data
	}

	// --transactional-message-id (string or numeric — send as-is, the API accepts both)
	if v, _ := cmd.Flags().GetString("transactional-message-id"); v != "" {
		payload["transactional_message_id"] = v
	}

	return payload, nil
}

// setIfChanged sets payload[jsonKey] = flag value, only when the flag has a
// non-empty value. Returns an error if the value contains control characters.
func setIfChanged(cmd *cobra.Command, payload map[string]any, flagName, jsonKey string) error {
	if v, _ := cmd.Flags().GetString(flagName); v != "" {
		if err := validate.ValidateStringValue(flagName, v); err != nil {
			output.PrintError(output.CodeValidationError, err.Error(), nil)
			return err
		}
		payload[jsonKey] = v
	}
	return nil
}

// validateIdentifiers checks that identifiers are present in the payload.
func validateIdentifiers(payload map[string]any) error {
	if _, ok := payload["identifiers"]; !ok {
		err := fmt.Errorf("identifiers required — provide --identifiers '{\"email\":\"...\"}'")
		output.PrintError(output.CodeValidationError, err.Error(), nil)
		return err
	}
	return nil
}

// validateTxnID checks for transactional_message_id when required.
func validateTxnID(payload map[string]any, required bool) error {
	if !required {
		return nil
	}
	if _, ok := payload["transactional_message_id"]; !ok {
		err := fmt.Errorf("--transactional-message-id is required (or include transactional_message_id in --json)")
		output.PrintError(output.CodeValidationError, err.Error(), nil)
		return err
	}
	return nil
}

// ---------------------------------------------------------------------------
// Track API send execution
// ---------------------------------------------------------------------------

// runTrackSend sends the pre-built payload to the track API.
func runTrackSend(cmd *cobra.Command, sendPath string, body json.RawMessage) error {
	// Resolve the sa_live_ token — send commands use it directly (no OAuth).
	tokenFlag, _ := cmd.Flags().GetString("token")
	saToken := client.ResolveServiceAccountToken(tokenFlag)
	if saToken == "" {
		err := fmt.Errorf("no service account token available — run 'cio auth login' or set CIO_TOKEN")
		output.PrintError(output.CodeAuthError, err.Error(), nil)
		return err
	}

	// Resolve environment ID.
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

	// Resolve track API base URL: explicit --api-url override, CIO_TRACK_URL env
	// var, or derived from the active profile's region / base URL.
	apiURL, _ := cmd.Flags().GetString("api-url")
	trackURL := strings.TrimRight(
		client.ResolveTrackBaseURL(apiURL, cmd.Flags().Changed("api-url")),
		"/",
	)

	timeout, _ := cmd.Flags().GetDuration("timeout")

	jq := GetJQFlag(cmd)

	// Dry run.
	if GetDryRun(cmd) {
		dryRun := map[string]any{
			"dry_run": true,
			"method":  "POST",
			"url":     trackURL + sendPath,
			"headers": map[string]string{
				"Authorization":          "Bearer [REDACTED]",
				"Content-Type":           "application/json",
				client.WorkspaceIDHeader: envID,
			},
			"body": json.RawMessage(body),
		}
		return output.FprintJSON(cmd.OutOrStdout(), dryRun)
	}

	result, err := client.DoTrack(cmd.Context(), client.TrackRequest{
		TrackBaseURL:        trackURL,
		Path:                sendPath,
		ServiceAccountToken: saToken,
		WorkspaceID:         envID,
		Body:                body,
		Timeout:             timeout,
	})
	if err != nil {
		return handleAPIError(err)
	}

	watch, _ := cmd.Flags().GetBool("watch")
	if !watch {
		return output.FprintProcess(cmd.OutOrStdout(), result, jq)
	}

	// Extract the delivery_id so we can poll its status.
	var queued struct {
		DeliveryID string `json:"delivery_id"`
	}
	if err := json.Unmarshal(result, &queued); err != nil || queued.DeliveryID == "" {
		return fmt.Errorf("--watch: could not extract delivery_id from send response")
	}

	return watchDelivery(cmd, envID, queued.DeliveryID)
}

// isTrackSendCommand returns true for commands that send via the track API
// and skip the UI API client init (both 'cio send' and 'cio transactional send').
func isTrackSendCommand(cmd *cobra.Command) bool {
	p := cmd.CommandPath()
	return strings.HasPrefix(p, "cio send ") || strings.HasPrefix(p, "cio transactional send ")
}

// inProgressDeliveryStates are the delivery states that indicate the delivery
// is still being processed. Sourced from services/deliveries DisplayState logic:
// default (no metrics) → "queued"; intermediate metrics: "drafted", "attempted".
var inProgressDeliveryStates = map[string]bool{
	"queued":    true,
	"drafted":   true,
	"attempted": true,
}

// isTerminalDelivery reports whether the delivery response JSON has reached a
// final state. Checks both top-level "state" and nested "delivery.state".
func isTerminalDelivery(data json.RawMessage) (bool, string) {
	var obj map[string]any
	if err := json.Unmarshal(data, &obj); err != nil {
		return false, ""
	}
	var state string
	if s, ok := obj["state"].(string); ok {
		state = s
	} else if delivery, ok := obj["delivery"].(map[string]any); ok {
		if s, ok := delivery["state"].(string); ok {
			state = s
		}
	}
	if state == "" || inProgressDeliveryStates[state] {
		return false, ""
	}
	return true, state
}

// watchDelivery polls GET /v1/environments/{envID}/deliveries/{deliveryID}
// every 2 seconds until the delivery reaches a terminal status, then prints
// the response to stdout.
func watchDelivery(cmd *cobra.Command, envID, deliveryID string) error {
	c := clientFromCmd(cmd)
	path := fmt.Sprintf("/v1/environments/%s/deliveries/%s", envID, deliveryID)
	jq := GetJQFlag(cmd)

	timeout, _ := cmd.Flags().GetDuration("timeout")
	ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
	defer cancel()

	stderr := cmd.ErrOrStderr()
	fmt.Fprintf(stderr, "delivery queued (ID: %s) — watching for status", deliveryID)

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			fmt.Fprintln(stderr)
			return ctx.Err()
		case <-ticker.C:
			result, err := c.Do(ctx, http.MethodGet, path, nil, nil)
			if err != nil {
				if apiErr, ok := err.(*client.APIError); ok && apiErr.StatusCode == http.StatusNotFound {
					fmt.Fprint(stderr, ".")
					continue
				}
				fmt.Fprintln(stderr)
				return handleAPIError(err)
			}
			if terminal, state := isTerminalDelivery(result); terminal {
				fmt.Fprintf(stderr, " email %s!\n", state)
				return output.FprintProcess(cmd.OutOrStdout(), result, jq)
			}
			fmt.Fprint(stderr, ".")
		}
	}
}
