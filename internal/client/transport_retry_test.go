package client

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

func noRetryDelay(ctx context.Context, _ time.Duration) error { return ctx.Err() }

func TestClient_RetriesDialTimeout(t *testing.T) {
	for _, method := range []string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE"} {
		t.Run(method, func(t *testing.T) {
			var requests atomic.Int32
			bodies := make(chan string, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				body, _ := io.ReadAll(r.Body)
				bodies <- string(body)
				_, _ = io.WriteString(w, `{"ok":true}`)
			}))
			defer server.Close()

			var dials atomic.Int32
			transport := &http.Transport{DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				if dials.Add(1) == 1 {
					return nil, &net.OpError{Op: "dial", Net: network, Err: os.ErrDeadlineExceeded}
				}
				return (&net.Dialer{}).DialContext(ctx, network, addr)
			}}
			defer transport.CloseIdleConnections()
			c := New(Config{
				BaseURL: server.URL, AccessToken: "test-jwt",
				HTTPClient:  &http.Client{Transport: transport},
				RetryConfig: &RetryConfig{MaxRetries: 2, SleepFn: noRetryDelay},
			})
			_, err := c.Do(context.Background(), method, "/test", nil, []byte(`{"name":"test"}`))
			if err != nil {
				t.Fatalf("expected recovery after dial timeout: %v", err)
			}
			if dials.Load() != 2 || requests.Load() != 1 {
				t.Fatalf("dials=%d requests=%d, want 2 dials and one server request", dials.Load(), requests.Load())
			}
			if got := <-bodies; got != `{"name":"test"}` {
				t.Errorf("request body = %q", got)
			}
		})
	}
}

func TestClient_RetriesReadTimeout(t *testing.T) {
	for _, tc := range []struct{ method, stage string }{{"GET", "headers"}, {"HEAD", "headers"}, {"GET", "body"}} {
		t.Run(tc.method+"/"+tc.stage, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if requests.Add(1) == 1 {
					if tc.stage == "body" {
						_, _ = io.WriteString(w, `{"ok":`)
						w.(http.Flusher).Flush()
					}
					<-r.Context().Done()
					return
				}
				_, _ = io.WriteString(w, `{"ok":true}`)
			}))
			defer server.Close()
			c := New(Config{
				BaseURL: server.URL, AccessToken: "test-jwt", Timeout: 100 * time.Millisecond,
				RetryConfig: &RetryConfig{MaxRetries: 2, SleepFn: noRetryDelay},
			})
			result, err := c.Do(context.Background(), tc.method, "/test", nil, nil)
			if err != nil {
				t.Fatalf("expected recovery after %s timeout: %v", tc.stage, err)
			}
			want := `{"ok":true}`
			if tc.method == "HEAD" {
				want = "null"
			}
			if string(result) != want || requests.Load() != 2 {
				t.Errorf("result=%s requests=%d, want %s after 2 requests", result, requests.Load(), want)
			}
		})
	}
}

func TestClient_DoesNotRetrySentWriteTimeout(t *testing.T) {
	for _, method := range []string{"POST", "PUT", "PATCH", "DELETE"} {
		for _, stage := range []string{"headers", "body"} {
			t.Run(method+"/"+stage, func(t *testing.T) {
				var requests atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					_, _ = io.Copy(io.Discard, r.Body)
					if stage == "body" {
						_, _ = io.WriteString(w, `{"ok":`)
						w.(http.Flusher).Flush()
					}
					<-r.Context().Done()
				}))
				defer server.Close()
				c := New(Config{
					BaseURL: server.URL, AccessToken: "test-jwt", Timeout: 100 * time.Millisecond,
					RetryConfig: &RetryConfig{MaxRetries: 2, SleepFn: noRetryDelay},
				})
				_, err := c.Do(context.Background(), method, "/test", nil, []byte(`{}`))
				if err == nil || !MayHaveApplied(err) || requests.Load() != 1 {
					t.Errorf("err=%v mayHaveApplied=%t requests=%d, want one ambiguous write", err, MayHaveApplied(err), requests.Load())
				}
			})
		}
	}
}

func TestClient_TransportRetryLimit(t *testing.T) {
	for _, method := range []string{"GET", "POST"} {
		for _, limit := range []int{0, 2} {
			t.Run(fmt.Sprintf("%s/%d", method, limit), func(t *testing.T) {
				var dials atomic.Int32
				transport := &http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) {
					dials.Add(1)
					return nil, &net.OpError{Op: "dial", Net: "tcp", Err: os.ErrDeadlineExceeded}
				}}
				defer transport.CloseIdleConnections()
				c := New(Config{
					BaseURL: "http://example.test", AccessToken: "test-jwt",
					HTTPClient:  &http.Client{Transport: transport},
					RetryConfig: &RetryConfig{MaxRetries: limit, SleepFn: noRetryDelay},
				})
				_, err := c.Do(context.Background(), method, "/test", nil, nil)
				var netErr net.Error
				if !errors.As(err, &netErr) || !netErr.Timeout() || MayHaveApplied(err) || dials.Load() != int32(limit+1) {
					t.Errorf("err=%v dials=%d, want an unsent timeout after %d attempts", err, dials.Load(), limit+1)
				}
			})
		}
	}
}

func TestClient_DoesNotRetryPermanentTransportError(t *testing.T) {
	var dials atomic.Int32
	transport := &http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) {
		dials.Add(1)
		return nil, &net.DNSError{Err: "no such host", Name: "example.test", IsNotFound: true}
	}}
	defer transport.CloseIdleConnections()
	c := New(Config{
		BaseURL: "http://example.test", AccessToken: "test-jwt",
		HTTPClient:  &http.Client{Transport: transport},
		RetryConfig: &RetryConfig{MaxRetries: 2, SleepFn: noRetryDelay},
	})
	_, err := c.Do(context.Background(), "GET", "/test", nil, nil)
	if err == nil || dials.Load() != 1 {
		t.Errorf("err=%v dials=%d, want one failed attempt", err, dials.Load())
	}
}

func TestClient_DoesNotRetryCancelledRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var dials atomic.Int32
	transport := &http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) {
		dials.Add(1)
		cancel()
		return nil, &net.OpError{Op: "dial", Net: "tcp", Err: os.ErrDeadlineExceeded}
	}}
	defer transport.CloseIdleConnections()
	c := New(Config{
		BaseURL: "http://example.test", AccessToken: "test-jwt",
		HTTPClient:  &http.Client{Transport: transport},
		RetryConfig: &RetryConfig{MaxRetries: 2, SleepFn: noRetryDelay},
	})
	_, err := c.Do(ctx, "GET", "/test", nil, nil)
	if !errors.Is(err, context.Canceled) || dials.Load() != 1 {
		t.Errorf("err=%v dials=%d, want cancellation after one attempt", err, dials.Load())
	}
}

func TestClient_CancelledWriteMayHaveApplied(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		cancel()
		<-r.Context().Done()
	}))
	defer server.Close()
	c := New(Config{
		BaseURL: server.URL, AccessToken: "test-jwt",
		RetryConfig: &RetryConfig{MaxRetries: 2, SleepFn: noRetryDelay},
	})
	_, err := c.Do(ctx, "POST", "/test", nil, []byte(`{}`))
	if !errors.Is(err, context.Canceled) || !MayHaveApplied(err) || requests.Load() != 1 {
		t.Errorf("err=%v mayHaveApplied=%t requests=%d, want one cancelled ambiguous write", err, MayHaveApplied(err), requests.Load())
	}
}

func TestPageAll_TransportTimeout(t *testing.T) {
	for _, recoverTimeout := range []bool{true, false} {
		name := "exhausted"
		if recoverTimeout {
			name = "recovered"
		}
		t.Run(name, func(t *testing.T) {
			var pageTwoAttempts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("page") == "1" {
					_, _ = io.WriteString(w, `{"items":[1],"total_pages":2}`)
					return
				}
				attempt := pageTwoAttempts.Add(1)
				if attempt == 1 || !recoverTimeout {
					<-r.Context().Done()
					return
				}
				_, _ = io.WriteString(w, `{"items":[2],"total_pages":2}`)
			}))
			defer server.Close()
			c := New(Config{
				BaseURL: server.URL, AccessToken: "test-jwt", Timeout: 100 * time.Millisecond,
				RetryConfig: &RetryConfig{MaxRetries: 2, SleepFn: noRetryDelay},
			})
			var out bytes.Buffer
			err := c.PageAll(PageAllConfig{Ctx: context.Background(), Method: "GET", Path: "/test", Writer: &out})
			want := "{\"items\":[1],\"total_pages\":2}\n"
			attempts := int32(3)
			if recoverTimeout {
				want += "{\"items\":[2],\"total_pages\":2}\n"
				attempts = 2
			}
			if (err == nil) != recoverTimeout || out.String() != want || pageTwoAttempts.Load() != attempts {
				t.Errorf("err=%v output=%q pageTwoAttempts=%d, want recovered=%t output=%q attempts=%d", err, out.String(), pageTwoAttempts.Load(), recoverTimeout, want, attempts)
			}
		})
	}
}
