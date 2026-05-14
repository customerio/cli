package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	// WorkspaceIDHeader is the request header that selects which workspace
	// (environment) the transactional send targets. Required when the
	// service account token is authorized for more than one workspace.
	WorkspaceIDHeader = "X-Workspace-Id"
)

// TrackRequest holds the parameters for a transactional send via the track API.
type TrackRequest struct {
	// TrackBaseURL is the track API base (e.g. https://track.customer.io).
	TrackBaseURL string
	// Path is the send endpoint path (e.g. /v1/send/email).
	Path string
	// ServiceAccountToken is the raw sa_live_ credential used as Bearer token.
	ServiceAccountToken string
	// WorkspaceID is the environment/workspace ID sent via X-Workspace-Id.
	WorkspaceID string
	// Body is the JSON request body.
	Body json.RawMessage
	// Timeout for the HTTP request. Zero means 30s default.
	Timeout time.Duration
}

// DoTrack performs a POST to the track API for transactional sends.
// The sa_live_ token is used directly as a Bearer token (no OAuth exchange).
// Returns the raw JSON response body on 2xx, or *APIError for 4xx/5xx.
//
// Intentionally no retry logic — retrying a send POST risks duplicate
// deliveries. Transient failures (429, 5xx) surface as errors to the caller.
func DoTrack(ctx context.Context, req TrackRequest) (json.RawMessage, error) {
	timeout := req.Timeout
	if timeout == 0 {
		timeout = defaultTimeout
	}
	httpClient := &http.Client{Timeout: timeout}

	url := req.TrackBaseURL + req.Path

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(req.Body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+req.ServiceAccountToken)
	httpReq.Header.Set(WorkspaceIDHeader, req.WorkspaceID)
	setStandardHeaders(httpReq)

	resp, err := httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	const maxResponseSize = 10 << 20 // 10 MB
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode >= http.StatusBadRequest {
		apiErr := &APIError{StatusCode: resp.StatusCode}
		if len(respBody) > 0 {
			apiErr.Body = respBody
		}
		return nil, apiErr
	}

	if len(respBody) == 0 {
		return json.RawMessage("null"), nil
	}

	if !json.Valid(respBody) {
		return nil, &NonJSONResponseError{
			StatusCode:  resp.StatusCode,
			ContentType: resp.Header.Get("Content-Type"),
		}
	}

	return json.RawMessage(respBody), nil
}
