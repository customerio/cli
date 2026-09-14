package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strconv"
)

// PageAllConfig holds configuration for auto-pagination.
type PageAllConfig struct {
	Ctx       context.Context
	Method    string
	Path      string
	Params    map[string]string
	Limit     int
	StartPage int
	Writer    io.Writer
}

// paginationMeta is extracted from each API response to determine when to stop.
type paginationMeta struct {
	TotalPages *int
	Total      *int
	HasMore    *bool
	EmptyData  bool
	// DataLen is the number of records on this page: the length of the
	// collection array, taken as the largest top-level array when the key is
	// not one of the well-known names.
	DataLen int
}

const maxAutoPages = 10000

// PageAll auto-paginates a GET endpoint, writing one JSON line per page.
func (c *Client) PageAll(cfg PageAllConfig) error {
	if cfg.Ctx == nil {
		cfg.Ctx = context.Background()
	}
	if cfg.StartPage <= 0 {
		cfg.StartPage = 1
	}
	if cfg.Params == nil {
		cfg.Params = make(map[string]string)
	}

	page := cfg.StartPage
	// The page size for the total-based stop is what the server actually
	// returned on the first page, not --limit: servers clamp oversized limits,
	// and trusting the flag would end the walk early. A later, final page may
	// be shorter, so only the first page is read.
	pageSize := 0

	for {
		params := copyParams(cfg.Params)
		params["page"] = strconv.Itoa(page)
		if cfg.Limit > 0 {
			params["limit"] = strconv.Itoa(cfg.Limit)
		}

		result, err := c.Do(cfg.Ctx, cfg.Method, cfg.Path, params, nil)
		if err != nil {
			return err
		}

		if _, err := fmt.Fprintf(cfg.Writer, "%s\n", result); err != nil {
			return fmt.Errorf("write page %d: %w", page, err)
		}

		meta := extractPaginationMeta(result, cfg.Limit)
		if pageSize == 0 {
			pageSize = meta.DataLen
		}

		if meta.TotalPages != nil && page >= *meta.TotalPages {
			return nil
		}

		if meta.Total != nil && pageSize > 0 {
			totalPages := int(math.Ceil(float64(*meta.Total) / float64(pageSize)))
			if totalPages == 0 {
				totalPages = 1
			}
			if page >= totalPages {
				return nil
			}
		}

		if meta.HasMore != nil && !*meta.HasMore {
			return nil
		}

		if meta.EmptyData {
			return nil
		}

		if page >= cfg.StartPage+maxAutoPages-1 {
			return nil
		}

		page++
	}
}

func extractPaginationMeta(data json.RawMessage, limit int) paginationMeta {
	var meta paginationMeta

	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil {
		return meta
	}

	// Totals live either at the top level or, on Journeys list endpoints,
	// under meta.pagination.
	meta.TotalPages = intField(obj, "total_pages")
	meta.Total = intField(obj, "total")
	if pagination := nestedObject(obj, "meta", "pagination"); pagination != nil {
		if meta.TotalPages == nil {
			meta.TotalPages = intField(pagination, "total_pages")
		}
		if meta.Total == nil {
			meta.Total = intField(pagination, "total")
		}
	}

	if raw, ok := obj["has_more"]; ok {
		var v bool
		if json.Unmarshal(raw, &v) == nil {
			meta.HasMore = &v
		}
	}

	for _, key := range []string{"data", "items", "results", "records", "entries", "campaigns"} {
		raw, ok := obj[key]
		if !ok {
			continue
		}
		// A known collection key that is null is an empty page: Go backends
		// emit null for a nil slice.
		if isNull(raw) {
			meta.EmptyData = true
			return meta
		}
		if arr, ok := arrayField(obj, key); ok {
			meta.DataLen = len(arr)
			meta.EmptyData = len(arr) == 0
			return meta
		}
	}

	// Endpoints name their collection after the resource (imports, segments,
	// ...). Without a known key, take the largest top-level array as the
	// collection, so a response like {"imports": [], "meta": {...}} still ends
	// the walk instead of running to maxAutoPages.
	arrays := 0
	for key := range obj {
		arr, ok := arrayField(obj, key)
		if !ok {
			continue
		}
		arrays++
		meta.DataLen = max(meta.DataLen, len(arr))
	}
	meta.EmptyData = arrays > 0 && meta.DataLen == 0

	return meta
}

func intField(obj map[string]json.RawMessage, key string) *int {
	raw, ok := obj[key]
	if !ok {
		return nil
	}
	var v int
	if json.Unmarshal(raw, &v) != nil {
		return nil
	}
	return &v
}

func isNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

// arrayField returns the field only when it is syntactically an array.
// json.Unmarshal accepts null into a slice, so an unrelated null field
// ("next": null) would otherwise read as an empty collection.
func arrayField(obj map[string]json.RawMessage, key string) ([]json.RawMessage, bool) {
	raw, ok := obj[key]
	if !ok || !bytes.HasPrefix(bytes.TrimSpace(raw), []byte("[")) {
		return nil, false
	}
	var arr []json.RawMessage
	if json.Unmarshal(raw, &arr) != nil {
		return nil, false
	}
	return arr, true
}

func nestedObject(obj map[string]json.RawMessage, keys ...string) map[string]json.RawMessage {
	for _, key := range keys {
		raw, ok := obj[key]
		if !ok {
			return nil
		}
		var next map[string]json.RawMessage
		if json.Unmarshal(raw, &next) != nil {
			return nil
		}
		obj = next
	}
	return obj
}

func copyParams(params map[string]string) map[string]string {
	out := make(map[string]string, len(params))
	for k, v := range params {
		out[k] = v
	}
	return out
}
