package routes

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/customerio/cli/internal/filelock"
)

const minimalSpec = `{"openapi":"3.1.0","info":{"title":"T","version":"1"},"paths":{"/v1/environments/{environment_id}/campaigns":{"get":{}}}}`

// countingSpecServer serves a minimal spec and counts how many times it was asked.
func countingSpecServer(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(minimalSpec))
	}))
	t.Cleanup(s.Close)
	return s, &hits
}

// NonBlocking: a lock held by someone else means ErrCacheBusy now — not a wait,
// and not a download.
func TestEnsureSpecs_NonBlockingStepsAsideWhenLockHeld(t *testing.T) {
	dir := t.TempDir()
	server, hits := countingSpecServer(t)

	// Simulate another process mid-download.
	unlock, err := filelock.Lock(filepath.Join(dir, "specs.lock"), 0600)
	if err != nil {
		t.Fatalf("holding lock: %v", err)
	}
	defer unlock()

	start := time.Now()
	_, _, err = EnsureSpecs(context.Background(), LoadRegistryOptions{
		CacheDir: dir, BaseURL: server.URL, NonBlocking: true,
	})
	elapsed := time.Since(start)

	if !errors.Is(err, ErrCacheBusy) {
		t.Fatalf("want ErrCacheBusy, got %v", err)
	}
	if hits.Load() != 0 {
		t.Errorf("must not download while another process holds the lock, got %d fetches", hits.Load())
	}
	if elapsed > 2*time.Second {
		t.Errorf("took %v; a held lock must be reported at once, not waited on", elapsed)
	}
}

// The default (blocking) behaviour is untouched: the same held lock is waited
// for, and released, and the download then proceeds.
func TestEnsureSpecs_BlockingStillWaitsForLock(t *testing.T) {
	dir := t.TempDir()
	server, hits := countingSpecServer(t)

	unlock, err := filelock.Lock(filepath.Join(dir, "specs.lock"), 0600)
	if err != nil {
		t.Fatalf("holding lock: %v", err)
	}
	go func() {
		time.Sleep(150 * time.Millisecond)
		unlock()
	}()

	if _, _, err := EnsureSpecs(context.Background(), LoadRegistryOptions{CacheDir: dir, BaseURL: server.URL}); err != nil {
		t.Fatalf("blocking EnsureSpecs after the holder released: %v", err)
	}
	if hits.Load() == 0 {
		t.Error("expected the download to proceed once the lock was released")
	}
}

// The caller's deadline bounds the download.
func TestEnsureSpecs_HonoursContextDeadline(t *testing.T) {
	dir := t.TempDir()
	release := make(chan struct{})
	defer close(release)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, _, err := EnsureSpecs(ctx, LoadRegistryOptions{CacheDir: dir, BaseURL: server.URL, NonBlocking: true})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected an error from a stalled host under a 200ms deadline")
	}
	if elapsed > 3*time.Second {
		t.Errorf("took %v; the 200ms deadline was not honoured", elapsed)
	}
}

// Quiet keeps the stale-cache warning off stderr; without it the warning is
// written as before.
func TestEnsureSpecs_QuietSuppressesWarnings(t *testing.T) {
	for _, quiet := range []bool{false, true} {
		t.Run(map[bool]string{false: "loud", true: "quiet"}[quiet], func(t *testing.T) {
			dir := t.TempDir()
			good, _ := countingSpecServer(t)
			if _, _, err := EnsureSpecs(context.Background(), LoadRegistryOptions{CacheDir: dir, BaseURL: good.URL}); err != nil {
				t.Fatalf("priming cache: %v", err)
			}

			// A host that now fails, and a TTL that forces a revalidation
			// attempt, drives the "using stale cached" warning path.
			bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "down", http.StatusInternalServerError)
			}))
			defer bad.Close()

			stderr := captureStderr(t, func() {
				_, _, err := EnsureSpecs(context.Background(), LoadRegistryOptions{
					CacheDir: dir, BaseURL: bad.URL, TTL: time.Nanosecond, Quiet: quiet,
				})
				if err != nil {
					t.Fatalf("stale fallback should succeed: %v", err)
				}
			})

			if quiet && strings.Contains(stderr, "warning:") {
				t.Errorf("Quiet must suppress cache warnings, got stderr: %q", stderr)
			}
			if !quiet && !strings.Contains(stderr, "warning: using stale cached") {
				t.Errorf("without Quiet the warning must still be written, got stderr: %q", stderr)
			}
		})
	}
}

// captureStderr runs fn with os.Stderr redirected to a pipe and returns what
// was written.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	old := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = old }()

	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()

	fn()
	_ = w.Close()
	return <-done
}
