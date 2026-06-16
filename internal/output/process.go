package output

import (
	"encoding/json"
	"io"
)

// FprintProcess applies --jq filtering to data and prints to w. When raw is
// true, string results are printed unquoted (like `jq -r`).
func FprintProcess(w io.Writer, data json.RawMessage, jqExpr string, raw bool) error {
	if jqExpr != "" {
		results, err := ApplyJQ(data, jqExpr)
		if err != nil {
			return err
		}
		if raw {
			return FprintRaw(w, results)
		}
		return FprintNDJSON(w, results)
	}

	if raw {
		return FprintRaw(w, []json.RawMessage{data})
	}
	return FprintJSON(w, json.RawMessage(data))
}
