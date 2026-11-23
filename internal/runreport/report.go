package runreport

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptrace"
	"os"
	"path/filepath"
	"sync/atomic"
)

type report struct {
	Version    string  `json:"version"`
	Command    string  `json:"command"`
	ExitCode   int     `json:"exit_code"`
	ErrorCode  *string `json:"error_code"`
	Reason     *string `json:"reason"`
	Method     *string `json:"method"`
	Route      *string `json:"route"`
	RouteMatch string  `json:"route_match"`
	Requests   int     `json:"requests"`
	Status     *int    `json:"status"`

	path        string
	lookupRoute func(method, path string) (string, bool)
}

// The CLI runs one command per process. An unset report keeps the hooks inert.
var active *report

func Enabled() bool { return active != nil }

// Start begins a report written to path at Finish. It removes any earlier
// report first, so a run killed before Finish leaves none rather than a stale one.
func Start(path, version string) {
	if info, err := os.Lstat(path); err == nil && info.Mode().IsRegular() {
		_ = os.Remove(path)
	}
	active = &report{path: path, Version: version, RouteMatch: "not_checked"}
}

func Finish(command string, exitCode int) {
	r := active
	active = nil
	r.Command = command
	r.ExitCode = exitCode
	if exitCode == 0 {
		r.ErrorCode = nil
		r.Reason = nil
	} else if r.Reason == nil {
		r.Reason = new("other")
	}
	data, _ := json.Marshal(r)
	// Write beside the target and rename, so a reader or a second run sharing the path never
	// sees a partly written report.
	tmp, err := os.CreateTemp(filepath.Dir(r.path), ".cio-run-report-*")
	if err != nil {
		return
	}
	_, werr := tmp.Write(append(data, '\n'))
	if cerr := tmp.Close(); werr != nil || cerr != nil || os.Rename(tmp.Name(), r.path) != nil {
		_ = os.Remove(tmp.Name())
	}
}

func ErrorCode(code string) {
	if active != nil {
		active.ErrorCode = &code
	}
}

func Reason(reason string) {
	if active != nil {
		active.Reason = &reason
	}
}

func Method(method string) {
	if active == nil {
		return
	}
	switch method {
	case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		active.Method = &method
	default:
		active.Method = nil
	}
}

func MatchedRoute(template string) {
	if active != nil {
		active.Route = &template
		active.RouteMatch = "matched"
	}
}

func RejectRoute(reason string) {
	if active != nil {
		active.RouteMatch = "not_in_spec"
		Reason(reason)
	}
}

func SetRouteLookup(lookup func(method, path string) (string, bool)) {
	active.lookupRoute = lookup
}

type failure struct {
	error
	reason string
}

func (f *failure) Unwrap() error { return f.error }

// WithReason keeps the cause attached to errors that may be retried or ignored.
func WithReason(err error, reason string) error {
	if active == nil {
		return err
	}
	return &failure{error: err, reason: reason}
}

func FailureReason(err error) string {
	var f *failure
	if errors.As(err, &f) {
		return f.reason
	}
	return ""
}

// ReadFailure tags a response body that failed to arrive: a dropped connection on a success, the
// status's failure otherwise.
func ReadFailure(err error, status int) error {
	if status >= http.StatusOK && status < http.StatusMultipleChoices {
		return WithReason(err, "network")
	}
	return WithHTTPStatus(err, status)
}

func WithHTTPStatus(err error, status int) error {
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		return WithReason(err, "http_error")
	}
	return err
}

type responseObserver struct {
	http.RoundTripper
	status int
}

func (o *responseObserver) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := o.RoundTripper.RoundTrip(req)
	if resp != nil {
		o.status = resp.StatusCode
	}
	return resp, err
}

// Do observes API traffic at the send boundary. Token exchanges and spec
// downloads use their own HTTP calls.
type untrackedKey struct{}

// Untracked marks requests that are the CLI's own housekeeping, such as a token promotion probe or a
// funds check after a send, so the report describes only the command's requests.
func Untracked(ctx context.Context) context.Context {
	return context.WithValue(ctx, untrackedKey{}, true)
}

func Do(c *http.Client, req *http.Request) (*http.Response, error) {
	r := active
	if r == nil || req.Context().Value(untrackedKey{}) != nil {
		return c.Do(req)
	}
	// A failed connection has no request to count. WroteHeaders also covers
	// attempts that reached the server but received no response.
	var sent atomic.Int64
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), &httptrace.ClientTrace{
		WroteHeaders: func() { sent.Add(1) },
	}))
	// http.Client follows redirects, so its final return can omit a response
	// already received by the transport if the redirect fails.
	observer := &responseObserver{RoundTripper: c.Transport}
	if observer.RoundTripper == nil {
		observer.RoundTripper = http.DefaultTransport
	}
	observedClient := *c
	observedClient.Transport = observer
	resp, err := observedClient.Do(req)
	r.Requests += int(sent.Load())
	if status := observer.status; status != 0 {
		r.Status = &status
		if err != nil {
			err = WithHTTPStatus(err, status)
		}
	} else if err != nil {
		r.Status = nil
		err = WithReason(err, "network")
	}
	// Like the status, method and route describe the last request, so a command
	// that reads before it writes reports the write.
	if lookup := r.lookupRoute; lookup != nil {
		Method(req.Method)
		r.Route, r.RouteMatch = nil, "not_checked"
		if template, ok := lookup(req.Method, req.URL.EscapedPath()); ok {
			MatchedRoute(template)
		}
	}
	return resp, err
}
