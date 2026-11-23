package runreport

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestStartRemovesAnEarlierReport(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.json")
	if err := os.WriteFile(path, []byte(`{"stale":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { active = nil })

	Start(path, "test")

	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("earlier report still present after Start: %v", err)
	}
}

func TestReportDescribesTheLastRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusPreconditionRequired)
		}
	}))
	t.Cleanup(server.Close)
	path := filepath.Join(t.TempDir(), "report.json")
	templates := map[string]string{"GET /domains": "/domains", "DELETE /domains/7": "/domains/{domain_id}"}

	Start(path, "test")
	SetRouteLookup(func(method, p string) (string, bool) {
		template, ok := templates[method+" "+p]
		return template, ok
	})
	for _, step := range []struct{ method, path string }{{http.MethodGet, "/domains"}, {http.MethodDelete, "/domains/7"}} {
		req, err := http.NewRequest(step.method, server.URL+step.path, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := Do(server.Client(), req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
	}
	Finish("domains delete", 1)

	var got struct {
		Method   string `json:"method"`
		Route    string `json:"route"`
		Status   int    `json:"status"`
		Requests int    `json:"requests"`
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.Method != http.MethodDelete || got.Route != "/domains/{domain_id}" || got.Status != http.StatusPreconditionRequired || got.Requests != 2 {
		t.Errorf("report = %s, want the DELETE's method, route and status after 2 requests", data)
	}
}

func TestHousekeepingRequestsStayOutOfTheReport(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	t.Cleanup(server.Close)
	path := filepath.Join(t.TempDir(), "report.json")

	Start(path, "test")
	SetRouteLookup(func(method, p string) (string, bool) { return "/wallet", true })
	req, err := http.NewRequestWithContext(Untracked(context.Background()), http.MethodGet, server.URL+"/wallet", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := Do(server.Client(), req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	Finish("send email", 0)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Method   *string `json:"method"`
		Route    *string `json:"route"`
		Requests int     `json:"requests"`
		Status   *int    `json:"status"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.Method != nil || got.Route != nil || got.Requests != 0 || got.Status != nil {
		t.Errorf("report = %s, want the untracked request left out", data)
	}
}

func TestStatusClearsWhenTheLastAttemptGetsNoResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	gone := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	gone.Close()
	t.Cleanup(server.Close)
	path := filepath.Join(t.TempDir(), "report.json")

	Start(path, "test")
	for _, url := range []string{server.URL, gone.URL} {
		req, err := http.NewRequest(http.MethodGet, url+"/campaigns", nil)
		if err != nil {
			t.Fatal(err)
		}
		if resp, err := Do(server.Client(), req); err == nil {
			_ = resp.Body.Close()
		}
	}
	Finish("api", 1)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Status *int `json:"status"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status != nil {
		t.Errorf("status = %d, want none for an attempt that got no response", *got.Status)
	}
}

func TestReadFailureNamesADroppedSuccessAsNetwork(t *testing.T) {
	t.Cleanup(func() { active = nil })
	Start(filepath.Join(t.TempDir(), "report.json"), "test")
	readErr := errors.New("unexpected EOF")

	if got := FailureReason(ReadFailure(readErr, http.StatusOK)); got != "network" {
		t.Errorf("200 body read failure reason = %q, want network", got)
	}
	if got := FailureReason(ReadFailure(readErr, http.StatusNotFound)); got != "http_error" {
		t.Errorf("404 body read failure reason = %q, want http_error", got)
	}
}
