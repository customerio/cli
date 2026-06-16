package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/customerio/cli/internal/client"
	"github.com/customerio/cli/internal/output"
	"github.com/spf13/cobra"
)

// doPageAll runs auto-pagination and writes NDJSON to stdout.
func doPageAll(cmd *cobra.Command, c *client.Client, path string, params map[string]string, startPage, limit int) error {
	jq := GetJQFlag(cmd)
	raw := GetRawFlag(cmd)

	var w io.Writer = cmd.OutOrStdout()
	if jq != "" || raw {
		w = &filterWriter{w: cmd.OutOrStdout(), jq: jq, raw: raw}
	}

	err := c.PageAll(client.PageAllConfig{
		Ctx:       cmd.Context(),
		Method:    "GET",
		Path:      path,
		Params:    params,
		Limit:     limit,
		StartPage: max(startPage, 1),
		Writer:    w,
	})
	if err != nil {
		return handleAPIError(err)
	}
	return nil
}

// filterWriter wraps an io.Writer and applies --jq / --raw-output to each line written.
type filterWriter struct {
	w   io.Writer
	jq  string
	raw bool
}

func (fw *filterWriter) Write(p []byte) (int, error) {
	trimmed := bytes.TrimRight(p, "\n")
	if len(trimmed) == 0 {
		return len(p), nil
	}
	if err := output.FprintProcess(fw.w, json.RawMessage(trimmed), fw.jq, fw.raw); err != nil {
		return 0, err
	}
	return len(p), nil
}

// errNoClient prints an auth error and returns a non-nil error.
func errNoClient(cmd *cobra.Command) error {
	err := fmt.Errorf("no API client available — check authentication")
	output.PrintError(output.CodeAuthError, err.Error(), nil)
	return err
}

// handleAPIError maps API errors to structured CLI errors.
func handleAPIError(err error) error {
	if njErr, ok := err.(*client.NonJSONResponseError); ok {
		output.PrintError(output.CodeAPIError, njErr.Error(), map[string]any{
			"status_code":  njErr.StatusCode,
			"content_type": njErr.ContentType,
		})
		return err
	}
	apiErr, ok := err.(*client.APIError)
	if !ok {
		output.PrintError(output.CodeGeneralError, err.Error(), nil)
		return err
	}
	switch apiErr.StatusCode {
	case http.StatusUnauthorized:
		output.PrintError(output.CodeAuthError, "Not authenticated. Run 'cio auth login' first.", map[string]any{
			"status_code": apiErr.StatusCode,
		})
	case http.StatusForbidden:
		output.PrintError(output.CodeAuthzError, "Insufficient permissions.", map[string]any{
			"status_code": apiErr.StatusCode,
		})
	default:
		output.PrintError(output.CodeAPIError, apiErr.Error(), map[string]any{
			"status_code": apiErr.StatusCode,
		})
	}
	return err
}
