package client

import (
	"context"
	"errors"
	"math"
	"math/rand/v2"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	defaultMaxRetries = 3
	baseDelay         = 500 * time.Millisecond
	maxDelay          = 30 * time.Second
)

// RetryConfig holds retry parameters.
type RetryConfig struct {
	MaxRetries int
	BaseDelay  time.Duration
	MaxDelay   time.Duration
	SleepFn    func(context.Context, time.Duration) error
}

// DefaultRetryConfig returns the default retry configuration, reading
// CIO_MAX_RETRIES from the environment if set.
func DefaultRetryConfig() RetryConfig {
	maxRetries := defaultMaxRetries
	if v := os.Getenv("CIO_MAX_RETRIES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			maxRetries = n
		}
	}
	return RetryConfig{
		MaxRetries: maxRetries,
		BaseDelay:  baseDelay,
		MaxDelay:   maxDelay,
		SleepFn:    ContextSleep,
	}
}

var (
	errTokenExchange = errors.New("token exchange failed")
	// errResponseLost marks a request that left the machine but whose response
	// never fully arrived.
	errResponseLost = errors.New("response lost")
)

// IsReadOnlyMethod reports whether a request with this method changes nothing.
func IsReadOnlyMethod(method string) bool {
	return method == http.MethodGet || method == http.MethodHead
}

// IsRetryable retries a write only on 429, because after a 5xx it may already
// be saved and sending it again can duplicate it.
func IsRetryable(method string, statusCode int) bool {
	if statusCode == http.StatusTooManyRequests {
		return true
	}
	return IsReadOnlyMethod(method) && statusCode >= http.StatusInternalServerError
}

// MayHaveApplied reports whether the server may have carried out a request
// that failed: it answered 5xx, or its response never fully arrived.
func MayHaveApplied(err error) bool {
	if errors.Is(err, errTokenExchange) {
		return false
	}
	if errors.Is(err, errResponseLost) {
		return true
	}
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode >= http.StatusInternalServerError
}

// ParseRetryAfter parses the Retry-After header value.
func ParseRetryAfter(value string) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}

	if seconds, err := strconv.Atoi(value); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}

	if t, err := time.Parse(time.RFC1123, value); err == nil {
		delay := time.Until(t)
		if delay > 0 {
			return delay
		}
		return 0
	}

	return 0
}

// ContextSleep sleeps for the given duration, returning early with ctx.Err()
// if the context is cancelled.
func ContextSleep(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

// BackoffDelay calculates the delay for the given attempt using exponential
// backoff with full jitter.
func BackoffDelay(attempt int, cfg RetryConfig) time.Duration {
	exp := math.Pow(2, float64(attempt))
	calculated := time.Duration(float64(cfg.BaseDelay) * exp)
	if calculated > cfg.MaxDelay {
		calculated = cfg.MaxDelay
	}
	if calculated <= 0 {
		return 0
	}
	return time.Duration(rand.Int64N(int64(calculated)))
}
