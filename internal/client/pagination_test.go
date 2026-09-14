package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// importsServer mimics a Journeys list endpoint: the collection is keyed by
// resource name and the total lives under meta.pagination.
func importsServer(t *testing.T, total int, defaultLimit int) (*httptest.Server, *[]string) {
	t.Helper()
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.URL.RawQuery)
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		if page < 1 {
			page = 1
		}
		if limit < 1 {
			limit = defaultLimit
		}
		start := (page - 1) * limit
		items := make([]map[string]int, 0, limit)
		for id := start + 1; id <= total && id <= start+limit; id++ {
			items = append(items, map[string]int{"id": id})
		}
		resp := map[string]any{
			"imports": items,
			"meta": map[string]any{"pagination": map[string]int{
				"page": page, "size": len(items), "total": total,
			}},
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			t.Errorf("encode: %v", err)
		}
	}))
	t.Cleanup(server.Close)
	return server, &requests
}

func pageAllLines(t *testing.T, server *httptest.Server, limit int) []string {
	t.Helper()
	c := New(Config{
		BaseURL:     server.URL,
		AccessToken: "test-jwt",
		RetryConfig: &RetryConfig{MaxRetries: 0, SleepFn: ContextSleep},
	})
	var buf bytes.Buffer
	err := c.PageAll(PageAllConfig{
		Ctx:    context.Background(),
		Method: "GET",
		Path:   "/v1/environments/1/imports",
		Limit:  limit,
		Writer: &buf,
	})
	if err != nil {
		t.Fatalf("PageAll: %v", err)
	}
	var lines []string
	for _, line := range strings.Split(buf.String(), "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func TestPageAll_StopsOnMetaPaginationTotal(t *testing.T) {
	server, requests := importsServer(t, 5, 50)
	lines := pageAllLines(t, server, 2)

	if len(lines) != 3 {
		t.Fatalf("expected 3 pages for total=5 limit=2, got %d: %v", len(lines), *requests)
	}
	if len(*requests) != 3 {
		t.Errorf("expected exactly 3 requests, got %d: %v", len(*requests), *requests)
	}
}

func TestPageAll_DerivesPageSizeWithoutLimit(t *testing.T) {
	server, requests := importsServer(t, 5, 2)
	lines := pageAllLines(t, server, 0)

	if len(lines) != 3 {
		t.Fatalf("expected 3 pages for total=5 server-default-limit=2, got %d: %v", len(lines), *requests)
	}
}

// The server clamps limit to 2; total=5 must still yield 3 pages even though
// --limit 100 would compute a single page.
func TestPageAll_UsesReturnedPageSizeWhenServerClampsLimit(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		const total, clamp = 5, 2
		items := make([]int, 0, clamp)
		for id := (page-1)*clamp + 1; id <= total && id <= page*clamp; id++ {
			items = append(items, id)
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{
			"imports": items,
			"meta":    map[string]any{"pagination": map[string]int{"total": total}},
		}); err != nil {
			t.Errorf("encode: %v", err)
		}
	}))
	t.Cleanup(server.Close)

	lines := pageAllLines(t, server, 100)
	if len(lines) != 3 || requests != 3 {
		t.Fatalf("expected 3 pages and 3 requests, got %d pages, %d requests", len(lines), requests)
	}
}

func TestPageAll_StopsOnEmptyResourceKeyedArray(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"imports": [], "meta": {"other": true}}`)
	}))
	t.Cleanup(server.Close)

	lines := pageAllLines(t, server, 0)
	if len(lines) != 1 || requests != 1 {
		t.Fatalf("expected a single request and page, got %d requests, %d lines", requests, len(lines))
	}
}

func TestExtractPaginationMeta(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		total     int
		emptyData bool
		dataLen   int
	}{
		{"top-level total", `{"total": 7, "data": [1, 2]}`, 7, false, 2},
		{"meta.pagination.total", `{"imports": [1], "meta": {"pagination": {"total": 9}}}`, 9, false, 1},
		{"top-level wins over meta", `{"total": 3, "items": [], "meta": {"pagination": {"total": 9}}}`, 3, true, 0},
		{"empty resource array", `{"segments": [], "meta": {}}`, -1, true, 0},
		{"largest array is the collection", `{"imports": [1, 2, 3], "warnings": ["w"]}`, -1, false, 3},
		{"null field is not a collection", `{"next": null, "id": 1}`, -1, false, 0},
		{"null known collection is empty", `{"data": null, "total": 0}`, 0, true, 0},
		{"no arrays is not empty", `{"id": 1}`, -1, false, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			meta := extractPaginationMeta(json.RawMessage(tt.body), 0)
			switch {
			case tt.total < 0 && meta.Total != nil:
				t.Errorf("expected no total, got %d", *meta.Total)
			case tt.total >= 0 && (meta.Total == nil || *meta.Total != tt.total):
				t.Errorf("expected total %d, got %v", tt.total, meta.Total)
			}
			if meta.EmptyData != tt.emptyData {
				t.Errorf("EmptyData = %v, want %v", meta.EmptyData, tt.emptyData)
			}
			if meta.DataLen != tt.dataLen {
				t.Errorf("DataLen = %d, want %d", meta.DataLen, tt.dataLen)
			}
		})
	}
}
