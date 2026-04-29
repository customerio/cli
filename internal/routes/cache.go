package routes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/customerio/cli/internal/filelock"
)

const (
	defaultSpecTTL   = 24 * time.Hour
	defaultBaseURL   = "https://us.fly.customer.io"
	specCacheSubdir  = "cache/specs"
	metaFileName     = "meta.json"
	cacheDirMode     = 0700
	specDownloadSize = 10 << 20 // 10MB max per spec
)

// specSource defines one OpenAPI spec to download and cache.
type specSource struct {
	Name    string // "openapi" or "cdp_openapi"
	URLPath string // "/v1/openapi.json" or "/cdp/api/openapi.json"
}

var defaultSpecSources = []specSource{
	{Name: "openapi", URLPath: "/v1/openapi.json"},
	{Name: "cdp_openapi", URLPath: "/cdp/api/openapi.json"},
}

// SpecURLPaths returns the URL paths used to download OpenAPI specs.
func SpecURLPaths() []string {
	paths := make([]string, len(defaultSpecSources))
	for i, s := range defaultSpecSources {
		paths[i] = s.URLPath
	}
	return paths
}

// LoadRegistryOptions configures spec loading behavior.
type LoadRegistryOptions struct {
	// BaseURL is the API base URL for downloading specs.
	// Defaults to https://us.fly.customer.io (specs are identical across regions).
	BaseURL string
	// AccessToken is a session JWT (from OAuth exchange) sent as Bearer auth
	// when downloading specs. The API optionally returns a personalized spec
	// when authenticated. Do NOT pass the raw sa_live_ token here — it must
	// be exchanged first via the client's EnsureAccessToken.
	AccessToken string
	// CacheKey is an opaque string used to isolate cached specs per identity
	// (typically derived from the sa_live_ token). When set, specs are cached
	// in a key-specific subdirectory to avoid cross-account contamination.
	CacheKey string
	// ForceRefresh bypasses TTL and re-downloads with ETag validation.
	ForceRefresh bool
	// CacheDir overrides the cache directory (for testing).
	CacheDir string
	// TTL overrides the cache TTL (default: 24h, env: CIO_SPEC_TTL).
	TTL time.Duration
	// HTTPClient overrides the HTTP client (for testing).
	HTTPClient *http.Client
}

// specMeta holds per-spec cache metadata.
type specMeta struct {
	ETag      string    `json:"etag,omitempty"`
	FetchedAt time.Time `json:"fetched_at"`
	Size      int64     `json:"size"`
}

// cacheMeta holds the full cache metadata file.
type cacheMeta struct {
	Specs map[string]*specMeta `json:"specs"`
}

func (o *LoadRegistryOptions) resolveBaseURL() string {
	if o.BaseURL != "" {
		return o.BaseURL
	}
	return defaultBaseURL
}

func (o *LoadRegistryOptions) resolveCacheDir() (string, error) {
	var base string
	if o.CacheDir != "" {
		base = o.CacheDir
	} else {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("get home dir: %w", err)
		}
		base = filepath.Join(home, ".cio", specCacheSubdir)
	}
	if o.CacheKey != "" {
		return filepath.Join(base, tokenCacheKey(o.CacheKey)), nil
	}
	return base, nil
}

// tokenCacheKey returns a short, filesystem-safe hash of a token for use
// as a cache subdirectory name. Different tokens produce different keys
// so that personalized specs don't collide.
func tokenCacheKey(token string) string {
	h := sha256.Sum256([]byte(token))
	return "auth-" + hex.EncodeToString(h[:8])
}

func (o *LoadRegistryOptions) resolveTTL() time.Duration {
	if o.TTL > 0 {
		return o.TTL
	}
	if v := os.Getenv("CIO_SPEC_TTL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return defaultSpecTTL
}

func (o *LoadRegistryOptions) resolveHTTPClient() *http.Client {
	if o.HTTPClient != nil {
		return o.HTTPClient
	}
	return &http.Client{Timeout: 30 * time.Second}
}

// lockCacheDir acquires an exclusive file lock on the cache directory.
// Returns an unlock function that must be called when done.
func lockCacheDir(cacheDir string) (unlock func(), err error) {
	lockPath := filepath.Join(cacheDir, "specs.lock")
	unlock, err = filelock.Lock(lockPath, 0600)
	if err != nil {
		return nil, fmt.Errorf("acquire lock: %w", err)
	}

	return unlock, nil
}

// EnsureSpecs returns spec bytes for both APIs, downloading if the cache is stale or missing.
func EnsureSpecs(ctx context.Context, opts LoadRegistryOptions) (journeys, cdp []byte, err error) {
	cacheDir, err := opts.resolveCacheDir()
	if err != nil {
		return nil, nil, err
	}
	if err := os.MkdirAll(cacheDir, cacheDirMode); err != nil {
		return nil, nil, fmt.Errorf("create cache dir: %w", err)
	}

	unlock, err := lockCacheDir(cacheDir)
	if err != nil {
		return nil, nil, err
	}
	defer unlock()

	meta := readMeta(cacheDir)
	baseURL := opts.resolveBaseURL()
	ttl := opts.resolveTTL()
	httpClient := opts.resolveHTTPClient()
	updated := false

	results := make([][]byte, len(defaultSpecSources))
	var errs []error

	for i, src := range defaultSpecSources {
		data, changed, specErr := ensureSpec(ctx, httpClient, cacheDir, baseURL, opts.AccessToken, src, meta, ttl, opts.ForceRefresh)
		if specErr != nil {
			errs = append(errs, fmt.Errorf("%s: %w", src.Name, specErr))
			continue
		}
		if changed {
			updated = true
		}
		results[i] = data
	}

	if updated {
		if err := writeMeta(cacheDir, meta); err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to write spec cache metadata: %v\n", err)
		}
	}

	if len(errs) > 0 {
		return nil, nil, fmt.Errorf("failed to load API specs: %w", errors.Join(errs...))
	}

	return results[0], results[1], nil
}

// ensureSpec handles a single spec: checks cache freshness, downloads if needed, returns data.
// The changed return value indicates whether the metadata was updated.
func ensureSpec(
	ctx context.Context,
	httpClient *http.Client,
	cacheDir, baseURL, accessToken string,
	src specSource,
	meta *cacheMeta,
	ttl time.Duration,
	forceRefresh bool,
) (data []byte, changed bool, err error) {
	filename := src.Name + ".json"
	cachedPath := filepath.Join(cacheDir, filename)
	entry := meta.Specs[src.Name]

	// Check if cache is fresh.
	if !forceRefresh && entry != nil && time.Since(entry.FetchedAt) < ttl {
		data, err = os.ReadFile(cachedPath)
		if err == nil {
			return data, false, nil
		}
		// Cache file missing/corrupt — fall through to download.
	}

	// Download with conditional request.
	url := baseURL + src.URLPath
	var etag string
	if entry != nil {
		etag = entry.ETag
	}

	newData, newETag, dlErr := downloadSpec(ctx, httpClient, url, etag, accessToken)
	if dlErr != nil {
		// If the caller explicitly asked for a refresh, don't silently
		// fall back to stale data — they want fresh data or an error.
		if forceRefresh {
			return nil, false, dlErr
		}
		// Try stale cache on download failure.
		data, readErr := os.ReadFile(cachedPath)
		if readErr == nil {
			fmt.Fprintf(os.Stderr, "warning: using stale cached %s (download failed: %v)\n", src.Name, dlErr)
			return data, false, nil
		}
		return nil, false, dlErr
	}

	if newData == nil {
		// 304 Not Modified — update timestamp, read from cache.
		if entry != nil {
			entry.FetchedAt = time.Now().UTC()
		}
		data, err = os.ReadFile(cachedPath)
		if err != nil {
			return nil, false, fmt.Errorf("cache file missing after 304: %w", err)
		}
		return data, true, nil
	}

	// Got new data — write to cache.
	if err := writeCachedSpec(cacheDir, filename, newData); err != nil {
		return nil, false, fmt.Errorf("write cache: %w", err)
	}

	meta.Specs[src.Name] = &specMeta{
		ETag:      newETag,
		FetchedAt: time.Now().UTC(),
		Size:      int64(len(newData)),
	}

	return newData, true, nil
}

// downloadSpec fetches a spec, using If-None-Match for conditional requests.
// If accessToken is non-empty, it is sent as a Bearer token so the server can
// return a personalized spec for the authenticated account.
// Returns (nil, "", nil) on 304 Not Modified.
func downloadSpec(ctx context.Context, httpClient *http.Client, url, etag, accessToken string) (data []byte, newETag string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Accept", "application/json")
	if accessToken != "" {
		req.Header.Set("Authorization", "Bearer "+accessToken)
	}
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("fetch %s: %w", url, err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusNotModified:
		return nil, "", nil
	case http.StatusOK:
		body, err := io.ReadAll(io.LimitReader(resp.Body, specDownloadSize))
		if err != nil {
			return nil, "", fmt.Errorf("read response from %s: %w", url, err)
		}
		return body, resp.Header.Get("ETag"), nil
	default:
		return nil, "", fmt.Errorf("HTTP %d from %s", resp.StatusCode, url)
	}
}

// writeCachedSpec writes data atomically using temp file + rename.
func writeCachedSpec(dir, filename string, data []byte) error {
	tmp, err := os.CreateTemp(dir, filename+".tmp.*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}

	return os.Rename(tmpName, filepath.Join(dir, filename))
}

// readMeta reads the cache metadata file, returning an empty struct on any error.
func readMeta(cacheDir string) *cacheMeta {
	path := filepath.Join(cacheDir, metaFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		return &cacheMeta{Specs: make(map[string]*specMeta)}
	}

	var meta cacheMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return &cacheMeta{Specs: make(map[string]*specMeta)}
	}
	if meta.Specs == nil {
		meta.Specs = make(map[string]*specMeta)
	}

	return &meta
}

// writeMeta atomically writes the cache metadata file.
func writeMeta(cacheDir string, meta *cacheMeta) error {
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return writeCachedSpec(cacheDir, metaFileName, data)
}
