package output

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// Exit codes.
const (
	ExitGeneral    = 1
	ExitValidation = 2
	ExitAuth       = 3
	ExitAuthz      = 4
	ExitAPI        = 5
)

// Error codes mapped to exit codes.
const (
	CodeGeneralError    = "GENERAL_ERROR"
	CodeValidationError = "VALIDATION_ERROR"
	CodeAuthError       = "AUTH_ERROR"
	CodeAuthzError      = "AUTHZ_ERROR"
	CodeAPIError        = "API_ERROR"
)

// ExitCodeForError returns the process exit code for the given error code.
func ExitCodeForError(code string) int {
	switch code {
	case CodeValidationError:
		return ExitValidation
	case CodeAuthError:
		return ExitAuth
	case CodeAuthzError:
		return ExitAuthz
	case CodeAPIError:
		return ExitAPI
	default:
		return ExitGeneral
	}
}

// CLIError is a structured error for JSON output to stderr.
type CLIError struct {
	Error   bool   `json:"error"`
	Code    string `json:"code"`
	Message string `json:"message"`
	Details any    `json:"details,omitempty"`
}

// PrintError writes a structured JSON error to stderr and returns the
// appropriate exit code.
func PrintError(code, message string, details any) int {
	FprintError(os.Stderr, code, message, details)
	return ExitCodeForError(code)
}

// FprintError writes a structured JSON error to w.
func FprintError(w io.Writer, code, message string, details any) {
	e := CLIError{
		Error:   true,
		Code:    code,
		Message: message,
		Details: details,
	}
	data, err := json.Marshal(e)
	if err != nil {
		_, _ = fmt.Fprintf(w, `{"error":true,"code":%q,"message":%q}`+"\n", code, message)
		return
	}
	_, _ = fmt.Fprintf(w, "%s\n", data)
}
