package validate

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// A trailing [] is the encoding fly binds for array query params
// (searchTagIds[]=1); the unbracketed name is silently ignored.
// A comma, semicolon, or space where '&' belongs: 'a=1,b=2' would otherwise
// parse as one param whose value carries the rest.
var misseparatedPairsRe = regexp.MustCompile(`[,;\s]\s*[a-zA-Z0-9_]+(\[\])?=`)

var validParamKeyRe = regexp.MustCompile(`^[a-zA-Z0-9_]+(\[\])?$`)

// paramsUsageHint is appended to parse failures so callers see the expected
// shape, not just the parser's complaint.
const paramsUsageHint = `--params expects a JSON object, e.g. --params '{"email":"a@b.com"}' (query-string form 'email=a@b.com' is also accepted)`

// MaxParamValueLength caps the length of a single query param value to
// bound request size and reject obviously abusive inputs. API query
// params are typically short (IDs, filters, search terms).
const MaxParamValueLength = 1024

// ValidateParams validates raw query parameters supplied as a JSON object, or
// as URL-query syntax (k=v&k2=v2) which is accepted as sugar for it.
func ValidateParams(raw string) (map[string]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, &ParamsValidationError{Reason: "params must not be empty"}
	}

	var parsed any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		if !looksLikeQueryString(raw) {
			hint := paramsUsageHint
			// A segment without '=' is usually an unencoded separator, not JSON.
			if strings.Contains(raw, "=") && strings.Contains(raw, "&") {
				hint = "in the query-string form, encode a literal & as %26. " + hint
			}
			return nil, &ParamsValidationError{
				Reason: fmt.Sprintf("params is not valid JSON: %s. %s", err.Error(), hint),
			}
		}
		obj, qsErr := parseQueryString(raw)
		if qsErr != nil {
			return nil, qsErr
		}
		return validateParamMap(obj)
	}

	obj, ok := parsed.(map[string]any)
	if !ok {
		return nil, &ParamsValidationError{
			Reason: fmt.Sprintf("params must be a JSON object. %s", paramsUsageHint),
		}
	}

	return validateParamMap(obj)
}

// looksLikeQueryString reports whether the input is unambiguously URL-query
// syntax, so that malformed JSON still reports as malformed JSON.
func looksLikeQueryString(raw string) bool {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "{") || strings.HasPrefix(raw, "[") {
		return false
	}
	seen := false
	for _, pair := range strings.Split(raw, "&") {
		if pair == "" {
			continue
		}
		key, _, found := strings.Cut(pair, "=")
		if !found || key == "" {
			return false
		}
		seen = true
	}
	return seen
}

// parseQueryString accepts URL-query syntax (k=v&k2=v2) as sugar for the JSON
// object form.
func parseQueryString(raw string) (map[string]any, error) {
	pairs := strings.Split(strings.TrimSpace(raw), "&")
	obj := make(map[string]any, len(pairs))
	for _, pair := range pairs {
		if pair == "" {
			continue
		}
		key, value, _ := strings.Cut(pair, "=")
		if misseparatedPairsRe.MatchString(value) {
			return nil, &ParamsValidationError{
				Reason: fmt.Sprintf("params query string value for %q looks like several pairs run together; separate parameters with &, or write %%2C for a literal comma, %%3B for a semicolon, %%20 for a space", key),
			}
		}
		// '+' is ambiguous here: query semantics say space, but agents reach for
		// it meaning a literal plus (a+tag@b.com). Refuse rather than guess.
		if strings.Contains(value, "+") {
			return nil, &ParamsValidationError{
				Reason: fmt.Sprintf("params query string value for %q contains a bare '+', which is ambiguous: write %%2B for a literal plus or %%20 for a space, or pass --params as JSON", key),
			}
		}
		decodedKey, err := url.QueryUnescape(key)
		if err != nil {
			return nil, &ParamsValidationError{
				Reason: fmt.Sprintf("params query string has an invalid percent-escape in key %q. %s", key, paramsUsageHint),
			}
		}
		decodedValue, err := url.QueryUnescape(value)
		if err != nil {
			return nil, &ParamsValidationError{
				Reason: fmt.Sprintf("params query string has an invalid percent-escape in the value for %q. %s", decodedKey, paramsUsageHint),
			}
		}
		if _, dup := obj[decodedKey]; dup {
			return nil, &ParamsValidationError{
				Reason: fmt.Sprintf("param key %q appears more than once; --params carries a single value per key, so multi-value filters cannot be sent", decodedKey),
			}
		}
		obj[decodedKey] = decodedValue
	}
	return obj, nil
}

func validateParamMap(obj map[string]any) (map[string]string, error) {
	result := make(map[string]string, len(obj))
	for key, val := range obj {
		if !validParamKeyRe.MatchString(key) {
			return nil, &ParamsValidationError{
				Reason: fmt.Sprintf("param key %q contains invalid characters (alphanumerics and underscores, with an optional [] suffix for array params); run 'cio schema' for the parameters an endpoint accepts", key),
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
