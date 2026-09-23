package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
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

// deliveriesServer mimics a cursor-paged Journeys endpoint: it ignores page
// and limit, and serves pages[i] for the cursor "c<i>" (the first page for no
// cursor). Each page's next cursor is "c<i+1>", or "" on the last.
func deliveriesServer(t *testing.T, pages [][]int) (*httptest.Server, *[]string) {
	t.Helper()
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.URL.RawQuery)
		i := 0
		if c := r.URL.Query().Get("continuation"); c != "" {
			i, _ = strconv.Atoi(strings.TrimPrefix(c, "c"))
		}
		next := ""
		if i+1 < len(pages) {
			next = "c" + strconv.Itoa(i+1)
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{
			"deliveries": pages[i],
			"meta":       map[string]string{"continuation": next},
		}); err != nil {
			t.Errorf("encode: %v", err)
		}
	}))
	t.Cleanup(server.Close)
	return server, &requests
}

func TestPageAll_FollowsContinuation(t *testing.T) {
	server, requests := deliveriesServer(t, [][]int{{1, 2}, {3, 4}, {5}})
	lines := pageAllLines(t, server, 0)

	if len(lines) != 3 {
		t.Fatalf("expected 3 pages, got %d: %v", len(lines), *requests)
	}
	// The guessed page=1 is refetched without page once a cursor shows up.
	want := []string{"page=1", "", "continuation=c1", "continuation=c2"}
	if len(*requests) != len(want) {
		t.Fatalf("requests = %q, want %q", *requests, want)
	}
	for i, q := range *requests {
		if q != want[i] {
			t.Errorf("request %d query = %q, want %q", i, q, want[i])
		}
	}
}

// A cursor can step past a stretch with no matches, so an empty page with a
// cursor is not the end.
func TestPageAll_ContinuationPastEmptyPage(t *testing.T) {
	server, requests := deliveriesServer(t, [][]int{{1}, {}, {2}})
	lines := pageAllLines(t, server, 0)

	if len(lines) != 3 {
		t.Fatalf("expected 3 pages, got %d: %v", len(lines), *requests)
	}
	if !strings.Contains(lines[2], "2") {
		t.Errorf("last page = %s, want the page after the empty one", lines[2])
	}
}

// A null cursor after a real one ends the walk; it must not resend the last
// cursor until the page cap.
func TestPageAll_NullContinuationEndsCursorWalk(t *testing.T) {
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.URL.RawQuery)
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("continuation") == "" {
			fmt.Fprint(w, `{"deliveries": [1], "meta": {"continuation": "c1"}}`)
			return
		}
		fmt.Fprint(w, `{"deliveries": [2], "meta": {"continuation": null}}`)
	}))
	t.Cleanup(server.Close)

	lines := pageAllLines(t, server, 0)
	// page=1, its refetch without page, then c1.
	if len(lines) != 2 || len(requests) != 3 {
		t.Fatalf("expected 2 pages and 3 requests, got %d pages: %v", len(lines), requests)
	}
}

func TestPageAll_RepeatedContinuationFails(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"deliveries": [1], "meta": {"continuation": "stuck"}}`)
	}))
	t.Cleanup(server.Close)

	c := New(Config{
		BaseURL:     server.URL,
		AccessToken: "test-jwt",
		RetryConfig: &RetryConfig{MaxRetries: 0, SleepFn: ContextSleep},
	})
	var buf bytes.Buffer
	err := c.PageAll(PageAllConfig{Ctx: context.Background(), Method: "GET", Path: "/v1/environments/1/deliveries", Writer: &buf})
	if err == nil || !strings.Contains(err.Error(), `"stuck" twice`) {
		t.Fatalf("expected a repeated-cursor error, got %v", err)
	}
	// page=1, its refetch without page, then the repeat.
	if requests != 3 {
		t.Errorf("expected 3 requests before stopping, got %d", requests)
	}
}

// runPageAll walks server from startPage with params, returning the pages
// written and the queries sent.
func runPageAll(t *testing.T, startPage int, params map[string]string, handler func(query url.Values) string) (lines, queries []string) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.RawQuery)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, handler(r.URL.Query()))
	}))
	t.Cleanup(server.Close)

	c := New(Config{
		BaseURL:     server.URL,
		AccessToken: "test-jwt",
		RetryConfig: &RetryConfig{MaxRetries: 0, SleepFn: ContextSleep},
	})
	var buf bytes.Buffer
	if err := c.PageAll(PageAllConfig{Ctx: context.Background(), Method: "GET", Path: "/v1/environments/1/backfills", Params: params, StartPage: startPage, Writer: &buf}); err != nil {
		t.Fatalf("PageAll: %v", err)
	}
	for _, line := range strings.Split(buf.String(), "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	return lines, queries
}

// /backfills counts page from 0 and pages by continuation after that, so the
// guessed page=1 skipped the newest page.
func TestPageAll_RefetchesGuessedFirstPage(t *testing.T) {
	lines, _ := runPageAll(t, 0, nil, func(q url.Values) string {
		page := q.Get("page")
		if page == "" {
			page = q.Get("continuation")
		}
		switch page {
		case "", "0":
			return `{"backfills": ["newest"], "meta": {"continuation": "1"}}`
		case "1":
			return `{"backfills": ["older"], "meta": {"continuation": "2"}}`
		default:
			return `{"backfills": ["oldest"], "meta": {}}`
		}
	})

	want := []string{"newest", "older", "oldest"}
	if len(lines) != len(want) {
		t.Fatalf("expected %d pages, got %d: %v", len(want), len(lines), lines)
	}
	for i, w := range want {
		if !strings.Contains(lines[i], w) {
			t.Errorf("page %d = %s, want %q", i, lines[i], w)
		}
	}
}

// A page the caller chose is where it wants to start: it is not refetched,
// and the cursor replaces it after the first request.
func TestPageAll_KeepsCallerPage(t *testing.T) {
	_, queries := runPageAll(t, 3, map[string]string{"page": "3"}, func(q url.Values) string {
		if q.Get("continuation") == "" {
			return `{"deliveries": [1], "meta": {"continuation": "c1"}}`
		}
		return `{"deliveries": [2], "meta": {"continuation": ""}}`
	})

	want := []string{"page=3", "continuation=c1"}
	if strings.Join(queries, " ") != strings.Join(want, " ") {
		t.Errorf("queries = %q, want %q", queries, want)
	}
}

func TestExtractPaginationMeta_Continuation(t *testing.T) {
	tests := []struct {
		name string
		body string
		want *string
	}{
		{"meta.continuation", `{"deliveries": [1], "meta": {"continuation": "0:2"}}`, ptr("0:2")},
		{"last page", `{"deliveries": [1], "meta": {"continuation": ""}}`, ptr("")},
		{"meta.pagination.continuation is not followed", `{"customers": [1], "meta": {"pagination": {"from": 5, "continuation": "x"}}}`, nil},
		{"no cursor", `{"imports": [1], "meta": {"pagination": {"total": 1}}}`, nil},
		{"non-string cursor is ignored", `{"items": [1], "meta": {"continuation": 3}}`, nil},
		{"null cursor is no cursor", `{"items": [1], "meta": {"continuation": null}}`, nil},
		{"null cursor with meta.pagination is no cursor", `{"items": [1], "meta": {"continuation": null, "pagination": {"continuation": "x"}}}`, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractPaginationMeta(json.RawMessage(tt.body), 0).Continuation
			switch {
			case tt.want == nil && got != nil:
				t.Errorf("expected no cursor, got %q", *got)
			case tt.want != nil && (got == nil || *got != *tt.want):
				t.Errorf("cursor = %v, want %q", got, *tt.want)
			}
		})
	}
}

func ptr(s string) *string { return &s }
