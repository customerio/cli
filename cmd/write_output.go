package cmd

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/customerio/cli/internal/client"
	"github.com/customerio/cli/internal/output"
	"github.com/spf13/cobra"
)

// printWriteResult prints a saved write's response so that an unreadable --jq
// never makes it look failed, since a caller that thinks it failed sends it again.
func printWriteResult(cmd *cobra.Command, method string, result json.RawMessage, jq string) error {
	if jq == "" {
		return output.FprintProcess(cmd.OutOrStdout(), result, jq, GetRawFlag(cmd))
	}
	results, err := output.ApplyJQ(result, jq)
	if err != nil {
		printWarning(cmd, "JQ_UNREADABLE_AFTER_WRITE", fmt.Sprintf("the %s request succeeded and its change is saved; --jq could not be applied (%v), so the full response is printed instead", method, err))
		return output.FprintJSON(cmd.OutOrStdout(), result)
	}
	if problem := unreadProjection(results, result); problem != "" {
		printWarning(cmd, "JQ_UNREADABLE_AFTER_WRITE", fmt.Sprintf("the %s request succeeded and its change is saved; --jq %s, so its paths may not match this response, whose top-level keys are: %s", method, problem, topLevelKeys(result)))
	}
	if GetRawFlag(cmd) {
		return output.FprintRaw(cmd.OutOrStdout(), results)
	}
	return output.FprintNDJSON(cmd.OutOrStdout(), results)
}

// handleWriteError reports a failed write, warning first when the server may
// still have applied it.
func handleWriteError(cmd *cobra.Command, method string, err error) error {
	if client.MayHaveApplied(err) {
		printWarning(cmd, "WRITE_OUTCOME_UNKNOWN", fmt.Sprintf("the %s request failed without showing whether its change was saved; read the resource before you send it again", method))
	}
	return handleAPIError(err)
}

// printWarning is deliberately not an error envelope, so it never makes a command read as failed.
func printWarning(cmd *cobra.Command, code, message string) {
	warning, _ := json.Marshal(map[string]any{"warning": true, "code": code, "message": message})
	_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "%s\n", warning)
}

// unreadProjection describes how --jq output may have missed the response:
// no values, only nulls, or null fields the response does not have.
func unreadProjection(results []json.RawMessage, response json.RawMessage) string {
	if len(results) == 0 {
		return "returned no values"
	}
	if allNull(results) {
		return "returned only nulls"
	}
	var top map[string]json.RawMessage
	_ = json.Unmarshal(response, &top)
	var missing []string
	for _, r := range results {
		var obj map[string]any
		if json.Unmarshal(r, &obj) != nil {
			continue
		}
		for k, v := range obj {
			if _, ok := top[k]; v == nil && !ok {
				missing = append(missing, k)
			}
		}
	}
	if len(missing) == 0 {
		return ""
	}
	sort.Strings(missing)
	return "returned null for fields the response does not have (" + strings.Join(missing, ", ") + ")"
}

func topLevelKeys(data json.RawMessage) string {
	if strings.TrimSpace(string(data)) == "null" {
		return "(none; the response has no body)"
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(data, &obj) != nil {
		return "(not an object)"
	}
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ", ")
}

// allNull reports whether every value is null or holds only nulls, the shape a
// projection like {id, name} gives when the response nests its fields deeper.
func allNull(items []json.RawMessage) bool {
	var isNull func(v any) bool
	isNull = func(v any) bool {
		switch t := v.(type) {
		case nil:
			return true
		case map[string]any:
			for _, child := range t {
				if !isNull(child) {
					return false
				}
			}
			return len(t) > 0
		case []any:
			for _, child := range t {
				if !isNull(child) {
					return false
				}
			}
			return len(t) > 0
		default:
			return false
		}
	}
	for _, item := range items {
		var v any
		if json.Unmarshal(item, &v) != nil || !isNull(v) {
			return false
		}
	}
	return true
}
