package skills

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/customerio/cli/internal/filelock"
	"github.com/customerio/cli/internal/useragent"
)

const (
	defaultSkillsTTL   = 5 * time.Minute
	defaultBaseURL     = "https://us.fly.customer.io"
	skillsCacheSubdir  = "cache/skills"
	skillsCacheFile    = "skills.json"
	metaFileName       = "meta.json"
	cacheDirMode       = 0700
	skillsDownloadSize = 10 << 20 // 10MB max
	skillsEndpointPath = "/v1/agent/skills"
)

// Skill represents a single skill in the response.
type Skill struct {
	Path        string            `json:"path"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Content     string            `json:"content"`
	Files       map[string]string `json:"files"`
}

// SortedFiles returns the skill's sub-file names in stable alphabetical order.
func (s Skill) SortedFiles() []string {
	names := make([]string, 0, len(s.Files))
	for name := range s.Files {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Index returns the skill's routing index: its authored Content if present,
// otherwise an index synthesized from each sub-file's frontmatter description.
// A skill with no SKILL.md serves an empty Content, so the index is the single
// source of routing truth built from the files themselves.
func (s Skill) Index() string {
	if s.Content != "" {
		return s.Content
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "# %s\n\n%s\n\n", s.Name, s.Description)
	fmt.Fprintf(&sb, "Read the file matching the task (`cio skills read %s/<file>`):\n\n", s.Path)
	for _, name := range s.SortedFiles() {
		desc := FrontmatterDescription(s.Files[name])
		if desc == "" {
			fmt.Fprintf(&sb, "- **%s**\n", name)
			continue
		}
		fmt.Fprintf(&sb, "- **%s** - %s\n", name, desc)
	}
	return sb.String()
}

// FrontmatterDescription extracts the `description:` value from a file's YAML
// frontmatter. Handles single-line values and folded (`>`) blocks, where the
// value continues on indented lines. Returns "" when there is no frontmatter
// or no description.
func FrontmatterDescription(raw string) string {
	const delim = "---\n"
	if !strings.HasPrefix(raw, delim) {
		return ""
	}
	rest := raw[len(delim):]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return ""
	}
	lines := strings.Split(rest[:end], "\n")
	for i, line := range lines {
		val, ok := strings.CutPrefix(line, "description:")
		if !ok {
			continue
		}
		val = strings.TrimSpace(val)
		if val != ">" && val != "" {
			return val
		}
		// Folded block: join the following indented lines.
		var parts []string
		for _, cont := range lines[i+1:] {
			if cont == "" || (cont[0] != ' ' && cont[0] != '\t') {
				break
			}
			parts = append(parts, strings.TrimSpace(cont))
		}
		return strings.Join(parts, " ")
	}
	return ""
}

// SkillsResponse is the full response from GET /v1/agent/skills.
type SkillsResponse struct {
	Prompt  string   `json:"prompt"`
	Skills  []Skill  `json:"skills"`
	Notices []string `json:"notices,omitempty"`
}

// LoadOptions configures skills loading behavior.
type LoadOptions struct {
	// BaseURL is the API base URL for downloading skills.
	BaseURL string
	// ForceRefresh bypasses TTL and re-downloads with ETag validation.
	ForceRefresh bool
	// CacheDir overrides the cache directory (for testing).
	CacheDir string
	// TTL overrides the cache TTL (default: 5m, env: CIO_SKILLS_TTL).
	TTL time.Duration
	// HTTPClient overrides the HTTP client (for testing).
	HTTPClient *http.Client
}

type skillsMeta struct {
	ETag      string    `json:"etag,omitempty"`
	FetchedAt time.Time `json:"fetched_at"`
	Size      int64     `json:"size"`
	// UserAgent is the User-Agent the cached bundle was fetched with. The
	// response varies by User-Agent (the server tailors content to the CLI
	// version), so a different one — e.g. after a CLI upgrade — must not reuse
	// this cache entry.
	UserAgent string `json:"user_agent,omitempty"`
}

func (o *LoadOptions) resolveBaseURL() string {
	if o.BaseURL != "" {
		return o.BaseURL
	}
	return defaultBaseURL
}

func (o *LoadOptions) resolveCacheDir() (string, error) {
	if o.CacheDir != "" {
		return o.CacheDir, nil
	}
	if v := os.Getenv("CIO_SKILLS_CACHE_DIR"); v != "" {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("get home dir: %w", err)
	}
	return filepath.Join(home, ".cio", skillsCacheSubdir), nil
}

func (o *LoadOptions) resolveTTL() time.Duration {
	if o.TTL > 0 {
		return o.TTL
	}
	if v := os.Getenv("CIO_SKILLS_TTL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return defaultSkillsTTL
}

func (o *LoadOptions) resolveHTTPClient() *http.Client {
	if o.HTTPClient != nil {
		return o.HTTPClient
	}
	return &http.Client{Timeout: 30 * time.Second}
}

// lockCacheDir acquires an exclusive file lock on the cache directory.
func lockCacheDir(cacheDir string) (unlock func(), err error) {
	lockPath := filepath.Join(cacheDir, "skills.lock")
	unlock, err = filelock.Lock(lockPath, 0600)
	if err != nil {
		return nil, fmt.Errorf("acquire lock: %w", err)
	}

	return unlock, nil
}

// EnsureSkills returns the cached skills response, downloading if stale or missing.
func EnsureSkills(ctx context.Context, opts LoadOptions) (*SkillsResponse, error) {
	cacheDir, err := opts.resolveCacheDir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(cacheDir, cacheDirMode); err != nil {
		return nil, fmt.Errorf("create cache dir: %w", err)
	}

	unlock, err := lockCacheDir(cacheDir)
	if err != nil {
		return nil, err
	}
	defer unlock()

	meta := readMeta(cacheDir)
	baseURL := opts.resolveBaseURL()
	ttl := opts.resolveTTL()
	httpClient := opts.resolveHTTPClient()

	data, err := ensureSkillsData(ctx, httpClient, cacheDir, baseURL, useragent.Get(), meta, ttl, opts.ForceRefresh)
	if err != nil {
		return nil, err
	}

	var resp SkillsResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("parse skills response: %w", err)
	}
	return &resp, nil
}

func ensureSkillsData(
	ctx context.Context,
	httpClient *http.Client,
	cacheDir, baseURL, userAgent string,
	meta *skillsMeta,
	ttl time.Duration,
	forceRefresh bool,
) ([]byte, error) {
	cachedPath := filepath.Join(cacheDir, skillsCacheFile)

	// Cache is fresh only when it was fetched with this User-Agent — the
	// response varies by it, so a UA change (e.g. a CLI upgrade) is a miss.
	sameUA := meta.UserAgent == userAgent
	if !forceRefresh && meta.ETag != "" && sameUA && time.Since(meta.FetchedAt) < ttl {
		data, err := os.ReadFile(cachedPath)
		if err == nil {
			return data, nil
		}
		// Cache file missing/corrupt — fall through to download.
	}

	// Conditional request, but only revalidate against the cached ETag when the
	// cached entry is for this UA; a different UA must fetch its own variant
	// rather than risk a 304 onto the wrong cached one.
	conditionalETag := meta.ETag
	if !sameUA {
		conditionalETag = ""
	}

	url := baseURL + skillsEndpointPath
	newData, newETag, dlErr := downloadSkills(ctx, httpClient, url, userAgent, conditionalETag)
	if dlErr != nil {
		// Try stale cache on download failure.
		data, readErr := os.ReadFile(cachedPath)
		if readErr == nil {
			fmt.Fprintf(os.Stderr, "warning: using stale cached skills (download failed: %v)\n", dlErr)
			return data, nil
		}
		return nil, dlErr
	}

	if newData == nil {
		// 304 Not Modified — update timestamp, read from cache.
		meta.FetchedAt = time.Now().UTC()
		meta.UserAgent = userAgent
		if err := writeMeta(cacheDir, meta); err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to write skills cache metadata: %v\n", err)
		}
		data, err := os.ReadFile(cachedPath)
		if err != nil {
			return nil, fmt.Errorf("cache file missing after 304: %w", err)
		}
		return data, nil
	}

	// Got new data — write to cache.
	if err := writeAtomic(cacheDir, skillsCacheFile, newData); err != nil {
		return nil, fmt.Errorf("write cache: %w", err)
	}

	meta.ETag = newETag
	meta.FetchedAt = time.Now().UTC()
	meta.Size = int64(len(newData))
	meta.UserAgent = userAgent
	if err := writeMeta(cacheDir, meta); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to write skills cache metadata: %v\n", err)
	}

	return newData, nil
}

func downloadSkills(ctx context.Context, httpClient *http.Client, url, userAgent, etag string) (data []byte, newETag string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)
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
		body, err := io.ReadAll(io.LimitReader(resp.Body, skillsDownloadSize))
		if err != nil {
			return nil, "", fmt.Errorf("read response from %s: %w", url, err)
		}
		return body, resp.Header.Get("ETag"), nil
	default:
		return nil, "", fmt.Errorf("HTTP %d from %s", resp.StatusCode, url)
	}
}

// CacheETag returns the ETag of the currently cached skills, or "" if no cache exists.
// Useful for testing.
func CacheETag(opts LoadOptions) string {
	cacheDir, err := opts.resolveCacheDir()
	if err != nil {
		return ""
	}
	meta := readMeta(cacheDir)
	return meta.ETag
}

// writeAtomic writes data atomically using temp file + rename.
func writeAtomic(dir, filename string, data []byte) error {
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

func readMeta(cacheDir string) *skillsMeta {
	path := filepath.Join(cacheDir, metaFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		return &skillsMeta{}
	}

	var meta skillsMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return &skillsMeta{}
	}
	return &meta
}

func writeMeta(cacheDir string, meta *skillsMeta) error {
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return writeAtomic(cacheDir, metaFileName, data)
}

// ComputeETag computes a SHA-256 ETag for the given data.
func ComputeETag(data []byte) string {
	h := sha256.Sum256(data)
	return `"` + hex.EncodeToString(h[:]) + `"`
}
