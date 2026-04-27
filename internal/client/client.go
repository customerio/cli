package client

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// setStandardHeaders stamps headers that every outgoing CLI request should
// carry:
//
//   - X-Validate: strict — opts into strict server-side JSON validation so
//     that unknown/typo'd body fields produce a 400 instead of a silent 200.
//     Harmless on GETs and form-encoded bodies (the server only consults it
//     when unmarshaling JSON request bodies).
//   - X-CIO-Agent: 1 — set only when the CIO_AGENT env var is "1". The
//     sandbox that runs the CLI on behalf of an AI agent sets this so
//     downstream metrics can attribute traffic to the agent.
func setStandardHeaders(req *http.Request) {
	req.Header.Set("X-Validate", "strict")
	if os.Getenv("CIO_AGENT") == "1" {
		req.Header.Set("X-CIO-Agent", "1")
	}
}

const (
	defaultBaseURL = "https://us.fly.customer.io"
	defaultTimeout = 30 * time.Second
)

// DefaultTimeout is exported so the cmd package can reference the default value.
const DefaultTimeout = defaultTimeout

// Config holds the configuration for the Journeys API client.
type Config struct {
	// BaseURL is the API base URL. Defaults to https://us.fly.customer.io.
	BaseURL string
	// ServiceAccountToken is the long-lived sa_live_ credential.
	ServiceAccountToken string
	// AccessToken is an already-exchanged short-lived JWT. If set, skips OAuth exchange.
	AccessToken string
	// ReadOnly requests a read-only session (scope=read_only) during OAuth exchange.
	ReadOnly bool
	// Scopes holds additional OAuth scope values to include in the token exchange.
	Scopes []string
	// Timeout is the HTTP client timeout. Defaults to 30s.
	Timeout time.Duration
	// HTTPClient is an optional custom HTTP client. If nil, a default is used.
	HTTPClient *http.Client
	// RetryConfig overrides default retry behavior. If nil, defaults are used.
	RetryConfig *RetryConfig
}

// Client is an HTTP client for the Customer.io Journeys UI API.
type Client struct {
	baseURL              string
	serviceAccountToken  string
	accessToken          string
	accessTokenExpiresAt time.Time
	readOnly             bool
	scopes               []string
	httpClient           *http.Client
	retry                RetryConfig
}

// New creates a new Journeys API client from the given config.
func New(cfg Config) *Client {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	baseURL = strings.TrimRight(baseURL, "/")

	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = defaultTimeout
	}

	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: timeout}
	}

	retryCfg := DefaultRetryConfig()
	if cfg.RetryConfig != nil {
		retryCfg = *cfg.RetryConfig
	}

	c := &Client{
		baseURL:             baseURL,
		serviceAccountToken: cfg.ServiceAccountToken,
		accessToken:         cfg.AccessToken,
		readOnly:            cfg.ReadOnly,
		scopes:              cfg.Scopes,
		httpClient:          httpClient,
		retry:               retryCfg,
	}
	// When an access token is provided directly (e.g. via CIO_ACCESS_TOKEN),
	// try to extract the expiry from the JWT's exp claim. If that fails
	// (e.g. opaque token), fall back to a far-future expiry so the
	// expiry check never clears it.
	if cfg.AccessToken != "" {
		if exp, err := parseJWTExpiry(cfg.AccessToken); err == nil {
			c.accessTokenExpiresAt = exp
		} else {
			c.accessTokenExpiresAt = time.Now().Add(24 * 365 * time.Hour)
		}
	}
	return c
}

// APIError represents a structured error from the API.
//
// Body holds the raw response body. It is []byte (not json.RawMessage)
// because some upstream errors return non-JSON content (HTML proxy pages,
// plaintext) and we don't want consumers to trip MarshalJSON validation.
type APIError struct {
	StatusCode int           `json:"status_code"`
	Body       []byte        `json:"-"`
	RetryAfter time.Duration `json:"-"`
}

func (e *APIError) Error() string {
	if len(e.Body) > 0 {
		return fmt.Sprintf("API error %d: %s", e.StatusCode, string(e.Body))
	}
	return fmt.Sprintf("API error %d", e.StatusCode)
}

// EnsureAccessToken exchanges the sa_live_ credential for a short-lived JWT
// via POST /v1/service_accounts/oauth/token (OAuth 2.0 client credentials grant).
// Returns the access token, or error. Caches the result and refreshes
// automatically before expiry.
func (c *Client) EnsureAccessToken(ctx context.Context) (string, error) {
	// If we already have an in-memory access token that hasn't expired, use it.
	// Refresh 60 seconds before actual expiry to avoid edge-case 401s.
	if c.accessToken != "" && time.Now().Before(c.accessTokenExpiresAt.Add(-60*time.Second)) {
		return c.accessToken, nil
	}

	// Token missing or expired — clear in-memory cache.
	c.accessToken = ""
	c.accessTokenExpiresAt = time.Time{}

	if c.serviceAccountToken == "" {
		return "", fmt.Errorf("no service account token configured")
	}

	// Check the file cache.
	if cached := CachedAccessToken(c.readOnly, c.scopes); cached != "" {
		c.accessToken = cached
		// File cache already applies 60s buffer, so set a conservative in-memory expiry.
		c.accessTokenExpiresAt = time.Now().Add(55 * time.Minute)
		return cached, nil
	}

	// Exchange sa_live_ credential for a JWT.
	token, expiresIn, err := c.exchangeToken(ctx)
	if err != nil {
		return "", fmt.Errorf("token exchange failed: %w", err)
	}

	c.accessToken = token
	// Prefer the JWT's own exp claim; fall back to expires_in from the
	// token response; last resort is 55 minutes.
	if exp, err := parseJWTExpiry(token); err == nil {
		c.accessTokenExpiresAt = exp
	} else if expiresIn > 0 {
		c.accessTokenExpiresAt = time.Now().Add(time.Duration(expiresIn) * time.Second)
	} else {
		c.accessTokenExpiresAt = time.Now().Add(55 * time.Minute)
	}

	// Cache for future invocations.
	_ = CacheAccessToken(token, expiresIn, c.readOnly, c.scopes)

	return token, nil
}

// clearAccessToken resets the cached token so the next call to
// EnsureAccessToken will re-authenticate.
func (c *Client) clearAccessToken() {
	c.accessToken = ""
	c.accessTokenExpiresAt = time.Time{}
}

// exchangeToken performs the OAuth 2.0 client credentials grant.
func (c *Client) exchangeToken(ctx context.Context) (accessToken string, expiresIn int, err error) {
	return exchangeTokenAt(ctx, c.httpClient, c.baseURL, c.serviceAccountToken, c.readOnly, c.scopes)
}

// exchangeTokenAt performs the OAuth exchange against a specific base URL.
// If readOnly is true, the scope=read_only parameter is included to request
// a read-only session that only permits GET requests. Additional scopes are
// appended space-separated per RFC 6749.
func exchangeTokenAt(ctx context.Context, httpClient *http.Client, baseURL, saToken string, readOnly bool, scopes []string) (accessToken string, expiresIn int, err error) {
	tokenURL := baseURL + "/v1/service_accounts/oauth/token"

	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("client_secret", saToken)

	// Build scope value: read_only (if requested) + any additional scopes.
	var scopeParts []string
	if readOnly {
		scopeParts = append(scopeParts, ScopeReadOnly)
	}
	scopeParts = append(scopeParts, scopes...)
	if len(scopeParts) > 0 {
		form.Set("scope", strings.Join(scopeParts, ScopeSeparator))
	}

	req, err := http.NewRequestWithContext(ctx, "POST", tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", 0, fmt.Errorf("create token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	setStandardHeaders(req)

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("token request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", 0, fmt.Errorf("read token response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", 0, &APIError{
			StatusCode: resp.StatusCode,
			Body:       body,
		}
	}

	var tokenResp OAuthTokenResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return "", 0, fmt.Errorf("parse token response: %w", err)
	}

	if tokenResp.AccessToken == "" {
		return "", 0, fmt.Errorf("token response missing access_token")
	}

	// Verify the server actually granted the read-only scope when requested.
	if readOnly {
		grantedScopes := strings.Fields(tokenResp.Scope)
		hasReadOnly := false
		for _, s := range grantedScopes {
			if s == ScopeReadOnly {
				hasReadOnly = true
				break
			}
		}
		if !hasReadOnly {
			return "", 0, fmt.Errorf("requested read-only scope but server granted %q", tokenResp.Scope)
		}
	}

	return tokenResp.AccessToken, tokenResp.ExpiresIn, nil
}

// DiscoverRegionResult holds the result of data center discovery.
type DiscoverRegionResult struct {
	Region      string
	BaseURL     string
	AccessToken string
	ExpiresIn   int
	AccountID   string
}

// DiscoverRegion exchanges the sa_live_ token against the default US endpoint,
// then calls GET /v1/accounts/current to read the account's data_center field.
//
// If tokenBaseURL is non-empty, the token exchange hits that URL instead
// (for testing). The account lookup always uses the same base URL.
func DiscoverRegion(ctx context.Context, saToken string, tokenBaseURL string) (*DiscoverRegionResult, error) {
	httpClient := &http.Client{Timeout: 15 * time.Second}

	// Token exchange starts at the default US endpoint, unless overridden for tests.
	exchangeURL := BaseURLForRegion("us")
	if tokenBaseURL != "" {
		exchangeURL = strings.TrimRight(tokenBaseURL, "/")
	}

	accessToken, expiresIn, err := exchangeTokenAt(ctx, httpClient, exchangeURL, saToken, false, nil)
	if err != nil {
		return nil, fmt.Errorf("token exchange failed: %w", err)
	}

	// Now discover the account's data center and ID via GET /v1/accounts/current.
	region, accountID, err := fetchAccountInfo(ctx, httpClient, exchangeURL, accessToken)
	if err != nil {
		// If we can't determine the region, default to US (token exchange succeeded there).
		// AccountID will be empty — commands requiring it will prompt the user.
		region = "us"
		accountID = ""
	}

	baseURL := BaseURLForRegion(region)
	if tokenBaseURL != "" {
		// In test mode, keep using the test server URL.
		baseURL = exchangeURL
	}

	return &DiscoverRegionResult{
		Region:      region,
		BaseURL:     baseURL,
		AccessToken: accessToken,
		ExpiresIn:   expiresIn,
		AccountID:   accountID,
	}, nil
}

// fetchAccountInfo calls GET /v1/accounts/current and reads the data_center and id fields.
func fetchAccountInfo(ctx context.Context, httpClient *http.Client, baseURL, accessToken string) (region, accountID string, err error) {
	req, err := http.NewRequestWithContext(ctx, "GET", baseURL+"/v1/accounts/current", nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	setStandardHeaders(req)

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", "", err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("GET /v1/accounts/current returned %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", err
	}

	var parsed struct {
		Account struct {
			ID         json.Number `json:"id"`
			DataCenter string      `json:"data_center"`
		} `json:"account"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", "", err
	}

	accountID = parsed.Account.ID.String()
	if accountID == "" || accountID == "0" {
		return "", "", fmt.Errorf("GET /v1/accounts/current response missing required field 'id'")
	}

	region = strings.ToLower(parsed.Account.DataCenter)
	if region == "" {
		region = "us"
	}

	return region, accountID, nil
}

// Do executes an HTTP request against the Journeys UI API.
//
// If the client has a service account token (sa_live_), it automatically
// exchanges it for a JWT before making the request.
//
// Parameters:
//   - method: HTTP method (GET, POST, PUT, DELETE, etc.)
//   - path: API path (e.g. "/v1/accounts/current"). Will be appended to base URL.
//   - params: query parameters to append to the URL (may be nil)
//   - body: request body as JSON (may be nil for GET/DELETE)
//
// Returns the raw JSON response body on success (2xx status).
// Returns an *APIError for 4xx/5xx responses.
func (c *Client) Do(ctx context.Context, method, path string, params map[string]string, body json.RawMessage) (json.RawMessage, error) {
	// Block non-GET requests in read-only mode as a client-side safety net.
	if c.readOnly && method != http.MethodGet {
		return nil, fmt.Errorf("read-only mode: %s requests are not permitted (use without --read-only to allow writes)", method)
	}

	// Ensure we have a valid access token.
	accessToken, err := c.EnsureAccessToken(ctx)
	if err != nil {
		return nil, err
	}

	u, err := url.Parse(c.baseURL + path)
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %w", err)
	}

	if len(params) > 0 {
		q := u.Query()
		for k, v := range params {
			q.Set(k, v)
		}
		u.RawQuery = q.Encode()
	}

	var lastErr error
	for attempt := 0; attempt <= c.retry.MaxRetries; attempt++ {
		if attempt > 0 {
			delay := BackoffDelay(attempt-1, c.retry)
			if apiErr, ok := lastErr.(*APIError); ok && apiErr.RetryAfter > 0 {
				if apiErr.RetryAfter > delay {
					delay = apiErr.RetryAfter
				}
			}
			if err := c.retry.SleepFn(ctx, delay); err != nil {
				return nil, err
			}
		}

		result, err := c.doOnce(ctx, method, u.String(), accessToken, body)
		if err == nil {
			return result, nil
		}

		apiErr, ok := err.(*APIError)
		if !ok {
			return nil, err
		}

		// If we get a 401, the token may have expired between our check
		// and the request (race condition or clock skew). Clear it and
		// retry once with a fresh token.
		if apiErr.StatusCode == http.StatusUnauthorized && c.serviceAccountToken != "" {
			c.clearAccessToken()
			freshToken, tokenErr := c.EnsureAccessToken(ctx)
			if tokenErr != nil {
				return nil, err // return original 401 error
			}
			accessToken = freshToken
			// Retry once with the fresh token; if it also fails, return error.
			result, err = c.doOnce(ctx, method, u.String(), accessToken, body)
			if err == nil {
				return result, nil
			}
			return nil, err
		}

		if !IsRetryable(apiErr.StatusCode) {
			return nil, err
		}
		lastErr = err
	}

	return nil, lastErr
}

// doOnce executes a single HTTP request (no retry).
func (c *Client) doOnce(ctx context.Context, method, rawURL, accessToken string, body json.RawMessage) (json.RawMessage, error) {
	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, rawURL, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")

	// Inject Bearer token (the short-lived JWT).
	if accessToken != "" {
		req.Header.Set("Authorization", "Bearer "+accessToken)
	}
	setStandardHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode >= http.StatusBadRequest {
		apiErr := &APIError{
			StatusCode: resp.StatusCode,
		}
		if len(respBody) > 0 {
			apiErr.Body = respBody
		}
		if resp.StatusCode == http.StatusTooManyRequests {
			apiErr.RetryAfter = ParseRetryAfter(resp.Header.Get("Retry-After"))
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

// NonJSONResponseError is returned when the server responds with a success
// status but a body that isn't valid JSON. This typically means the request
// hit an upstream proxy or SPA catch-all rather than the API itself (e.g. an
// unknown path on fly.customer.io returns a 200 HTML page).
type NonJSONResponseError struct {
	StatusCode  int
	ContentType string
}

func (e *NonJSONResponseError) Error() string {
	return fmt.Sprintf(
		"endpoint did not return JSON (status %d, Content-Type %q); the path may not exist on this API host",
		e.StatusCode, e.ContentType,
	)
}

// ServiceAccountToken returns the configured sa_live_ token (for status display).
func (c *Client) ServiceAccountToken() string {
	return c.serviceAccountToken
}

// AccessToken returns the current JWT access token.
func (c *Client) AccessToken() string {
	return c.accessToken
}

// BaseURL returns the configured base URL.
func (c *Client) BaseURL() string {
	return c.baseURL
}

// ReadOnly returns whether the client was configured for read-only access.
func (c *Client) ReadOnly() bool {
	return c.readOnly
}

// PostAnonymous performs an unauthenticated POST against baseURL+path with the
// given JSON body. Used for endpoints that accept no credentials (e.g. the
// agentic signup flow). Returns the raw response body on 2xx, or *APIError
// for 4xx/5xx.
func PostAnonymous(ctx context.Context, baseURL, path string, body json.RawMessage, timeout time.Duration) (json.RawMessage, error) {
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	httpClient := &http.Client{Timeout: timeout}

	u := strings.TrimRight(baseURL, "/") + path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	setStandardHeaders(req)

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode >= http.StatusBadRequest {
		apiErr := &APIError{StatusCode: resp.StatusCode}
		if len(respBody) > 0 {
			apiErr.Body = respBody
		}
		if resp.StatusCode == http.StatusTooManyRequests {
			apiErr.RetryAfter = ParseRetryAfter(resp.Header.Get("Retry-After"))
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

// parseJWTExpiry extracts the exp claim from a JWT without verifying the
// signature. Returns the expiry time or an error if the token is not a
// valid 3-part JWT or lacks an exp claim.
func parseJWTExpiry(token string) (time.Time, error) {
	parts := strings.SplitN(token, ".", 4)
	if len(parts) != 3 {
		return time.Time{}, fmt.Errorf("not a JWT")
	}

	// The payload is base64url-encoded (no padding).
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}, fmt.Errorf("decode JWT payload: %w", err)
	}

	var claims struct {
		Exp json.Number `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return time.Time{}, fmt.Errorf("parse JWT claims: %w", err)
	}

	expFloat, err := claims.Exp.Float64()
	if err != nil || expFloat == 0 {
		return time.Time{}, fmt.Errorf("missing or invalid exp claim")
	}

	return time.Unix(int64(expFloat), 0), nil
}
