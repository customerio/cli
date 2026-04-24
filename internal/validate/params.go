package validate

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

var validParamKeyRe = regexp.MustCompile(`^[a-zA-Z0-9_]+$`)

// MaxParamValueLength caps the length of a single query param value to
// bound request size and reject obviously abusive inputs. API query
// params are typically short (IDs, filters, search terms).
const MaxParamValueLength = 1024

// ValidateParams validates a raw JSON string as query parameters.
func ValidateParams(raw string) (map[string]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, &ParamsValidationError{Reason: "params must not be empty"}
	}

	var parsed any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil, &ParamsValidationError{
			Reason: fmt.Sprintf("params is not valid JSON: %s", err.Error()),
		}
	}

	obj, ok := parsed.(map[string]any)
	if !ok {
		return nil, &ParamsValidationError{Reason: "params must be a JSON object"}
	}

	result := make(map[string]string, len(obj))
	for key, val := range obj {
		if !validParamKeyRe.MatchString(key) {
			return nil, &ParamsValidationError{
				Reason: fmt.Sprintf("param key %q contains invalid characters (must be alphanumeric and underscores only)", key),
			}
		}

		switch v := val.(type) {
		case string:
			if err := validateParamValue(key, v); err != nil {
				return nil, err
			}
			result[key] = v
		case float64:
			if v == float64(int64(v)) {
				result[key] = fmt.Sprintf("%d", int64(v))
			} else {
				result[key] = fmt.Sprintf("%g", v)
			}
		case bool:
			result[key] = fmt.Sprintf("%t", v)
		case nil:
			continue
		default:
			return nil, &ParamsValidationError{
				Reason: fmt.Sprintf("param key %q has non-scalar value (objects and arrays not allowed)", key),
			}
		}
	}

	return result, nil
}

// validateParamValue rejects string values containing control characters
// (which can enable log/header injection downstream) and values that
// exceed MaxParamValueLength.
func validateParamValue(key, value string) error {
	if len(value) > MaxParamValueLength {
		return &ParamsValidationError{
			Reason: fmt.Sprintf("param %q value exceeds maximum length of %d bytes (got %d)", key, MaxParamValueLength, len(value)),
		}
	}
	for i, r := range value {
		if r < 0x20 || r == 0x7F {
			return &ParamsValidationError{
				Reason: fmt.Sprintf("param %q contains control character at byte %d (U+%04X)", key, i, r),
			}
		}
	}
	return nil
}

// ParamsValidationError provides structured details for params validation failures.
type ParamsValidationError struct {
	Reason string `json:"reason"`
}

func (e *ParamsValidationError) Error() string {
	return e.Reason
}
