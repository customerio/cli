package validate

import (
	"fmt"
	"strings"
)

var invalidIDChars = []rune{'?', '#', '%', '/', '\\'}

// ValidateResourceID validates that id is a non-empty numeric string.
func ValidateResourceID(id string) error {
	if id == "" {
		return &IDValidationError{Field: "id", Value: id, Reason: "ID must not be empty"}
	}

	for i, r := range id {
		if r < 0x20 || r == 0x7F {
			return &IDValidationError{
				Field:  "id",
				Value:  id,
				Reason: fmt.Sprintf("ID contains control character at position %d (U+%04X)", i, r),
			}
		}
	}

	for _, banned := range invalidIDChars {
		if strings.ContainsRune(id, banned) {
			return &IDValidationError{
				Field:  "id",
				Value:  id,
				Reason: fmt.Sprintf("ID contains reserved path character '%c'", banned),
			}
		}
	}

	for _, r := range id {
		if r < '0' || r > '9' {
			return &IDValidationError{
				Field:  "id",
				Value:  id,
				Reason: fmt.Sprintf("ID must be numeric, contains '%c'", r),
			}
		}
	}

	return nil
}

// ValidatePathSegmentID validates that id is a non-empty, single path segment.
// It is intentionally semantic-free for generic API path placeholders, while
// still rejecting characters that could smuggle query fragments, traversal, or
// pre-encoded path data.
func ValidatePathSegmentID(id string) error {
	if id == "" {
		return &IDValidationError{Field: "id", Value: id, Reason: "ID must not be empty"}
	}
	if id == "." || id == ".." {
		return &IDValidationError{Field: "id", Value: id, Reason: "ID must not be a dot path segment"}
	}

	for i, r := range id {
		if r < 0x20 || r == 0x7F {
			return &IDValidationError{
				Field:  "id",
				Value:  id,
				Reason: fmt.Sprintf("ID contains control character at position %d (U+%04X)", i, r),
			}
		}
	}

	for _, banned := range invalidIDChars {
		if strings.ContainsRune(id, banned) {
			return &IDValidationError{
				Field:  "id",
				Value:  id,
				Reason: fmt.Sprintf("ID contains reserved path character '%c'", banned),
			}
		}
	}

	return nil
}

// IDValidationError provides structured details for ID validation failures.
type IDValidationError struct {
	Field  string `json:"field"`
	Value  string `json:"value"`
	Reason string `json:"reason"`
}

func (e *IDValidationError) Error() string {
	return e.Reason
}
