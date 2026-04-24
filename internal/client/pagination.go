package client

import (
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
	DataKey    string
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

		if meta.TotalPages != nil && page >= *meta.TotalPages {
			return nil
		}

		if meta.Total != nil && cfg.Limit > 0 {
			totalPages := int(math.Ceil(float64(*meta.Total) / float64(cfg.Limit)))
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

	if raw, ok := obj["total_pages"]; ok {
		var v int
		if json.Unmarshal(raw, &v) == nil {
			meta.TotalPages = &v
		}
	}

	if raw, ok := obj["total"]; ok {
		var v int
		if json.Unmarshal(raw, &v) == nil {
			meta.Total = &v
		}
	}

	if raw, ok := obj["has_more"]; ok {
		var v bool
		if json.Unmarshal(raw, &v) == nil {
			meta.HasMore = &v
		}
	}

	for _, key := range []string{"data", "items", "results", "records", "entries", "campaigns"} {
		if raw, ok := obj[key]; ok {
			var arr []json.RawMessage
			if json.Unmarshal(raw, &arr) == nil {
				meta.DataKey = key
				if len(arr) == 0 {
					meta.EmptyData = true
				}
				break
			}
		}
	}

	return meta
}

func copyParams(params map[string]string) map[string]string {
	out := make(map[string]string, len(params))
	for k, v := range params {
		out[k] = v
	}
	return out
}
