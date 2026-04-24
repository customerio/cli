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
