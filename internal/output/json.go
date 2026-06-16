package output

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// PrintJSON writes v as a single JSON line to stdout.
func PrintJSON(v any) error {
	return FprintJSON(os.Stdout, v)
}

// FprintJSON writes v as a single JSON line to w.
func FprintJSON(w io.Writer, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("json marshal: %w", err)
	}
	_, err = fmt.Fprintf(w, "%s\n", data)
	return err
}

// PrintNDJSON writes each element of items as a separate JSON line to stdout.
func PrintNDJSON(items []json.RawMessage) error {
	return FprintNDJSON(os.Stdout, items)
}

// FprintNDJSON writes each element of items as a separate JSON line to w.
func FprintNDJSON(w io.Writer, items []json.RawMessage) error {
	for _, item := range items {
		if _, err := fmt.Fprintf(w, "%s\n", item); err != nil {
			return err
		}
	}
	return nil
}

// FprintRaw writes each item the way `jq -r` does: a JSON string is written as
// its raw, unquoted value; anything else (object, array, number, bool, null) is
// written as its compact JSON. One item per line. This lets callers extract a
// scalar (e.g. a compiled HTML body) without the external `jq -r` round-trip.
func FprintRaw(w io.Writer, items []json.RawMessage) error {
	for _, item := range items {
		var s string
		if err := json.Unmarshal(item, &s); err == nil {
			if _, err := fmt.Fprintln(w, s); err != nil {
				return err
			}
			continue
		}
		if _, err := fmt.Fprintf(w, "%s\n", item); err != nil {
			return err
		}
	}
	return nil
}
