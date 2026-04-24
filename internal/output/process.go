package output

import (
	"encoding/json"
	"io"
)

// FprintProcess applies --jq filtering to data and prints to w.
func FprintProcess(w io.Writer, data json.RawMessage, jqExpr string) error {
	if jqExpr != "" {
		results, err := ApplyJQ(data, jqExpr)
		if err != nil {
			return err
		}
		return FprintNDJSON(w, results)
	}

	return FprintJSON(w, json.RawMessage(data))
}
