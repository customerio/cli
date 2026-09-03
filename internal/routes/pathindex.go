package routes

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// PathIndex is a lightweight view of the API surface: the HTTP method and path
// template of every route, without the request and response schemas a Registry
// resolves. Decoding one costs a few milliseconds against a cached spec where
// building a Registry costs closer to a second, which is what makes it usable
// as a pre-send check on every call rather than only in `cio schema`.
type PathIndex struct {
	entries []indexEntry
}

type indexEntry struct {
	httpMethod string
	path       string
	segments   []string
}

// Suggestion is a route offered as a near miss for a path that did not match.
type Suggestion struct {
	HTTPMethod string
	Path       string
	// Resource is the CLI resource name, suitable for `cio schema <resource>`.
	Resource string
}

// String renders the suggestion as "GET /v1/environments/{environment_id}/campaigns".
func (s Suggestion) String() string {
	return s.HTTPMethod + " " + s.Path
}

// pathItemVerbs are the OpenAPI path-item keys that describe an operation. A
// path item also carries non-operation keys (parameters, summary, servers,
// $ref), which must not be read as HTTP methods.
var pathItemVerbs = map[string]bool{
	"get": true, "put": true, "post": true, "delete": true,
	"options": true, "head": true, "patch": true, "trace": true,
}

// LoadPathIndexFromCache decodes the path templates out of whatever specs the
// cache already holds. It reads files and nothing else: no download, no cache
// lock, no metadata.
//
// EnsureSpecs, which LoadRegistry goes through, is the wrong tool in front of a
// request. It takes an exclusive lock on the cache directory before reading
// anything, and that lock is a plain flock — it cannot be bounded or
// cancelled — so concurrent callers serialize behind whichever one holds it,
// and the holder may sit through two sequential spec downloads first. A check
// worth ~25ms must not be able to cost a minute, nor make parallel calls queue.
//
// Reading without the lock is safe because cached specs are replaced by rename,
// so a reader sees one whole version or the previous one, never a torn file.
// The cost is that this reports what the cache knows rather than what the
// server currently serves: a caller that needs freshness (`cio schema`) keeps
// going through EnsureSpecs, and a caller checking a path treats a cold cache
// as "no opinion".
func LoadPathIndexFromCache(opts LoadRegistryOptions) (*PathIndex, error) {
	cacheDir, err := opts.resolveCacheDir()
	if err != nil {
		return nil, err
	}

	idx := &PathIndex{}
	for _, src := range defaultSpecSources {
		data, readErr := os.ReadFile(filepath.Join(cacheDir, src.Name+".json"))
		if readErr != nil {
			continue
		}
		// A spec that will not parse is skipped rather than fatal: the other
		// one still describes its own tree, and a tree with no routes indexed
		// simply goes unjudged.
		_ = idx.add(data)
	}

	if len(idx.entries) == 0 {
		return nil, fmt.Errorf("no cached API spec describes any route")
	}
	return idx, nil
}

// add indexes one spec document, accepting either an OpenAPI document or the
// walked-routes JSON that LoadRegistryFromData reads.
func (idx *PathIndex) add(data []byte) error {
	if IsOpenAPISpec(data) {
		var doc struct {
			Paths map[string]map[string]json.RawMessage `json:"paths"`
		}
		if err := json.Unmarshal(data, &doc); err != nil {
			return fmt.Errorf("parsing OpenAPI paths: %w", err)
		}
		for path, pathItem := range doc.Paths {
			for key := range pathItem {
				if !pathItemVerbs[strings.ToLower(key)] {
					continue
				}
				idx.push(strings.ToUpper(key), path)
			}
		}
		return nil
	}

	var walked []walkedRoute
	if err := json.Unmarshal(data, &walked); err != nil {
		return fmt.Errorf("parsing walked routes JSON: %w", err)
	}
	for _, wr := range walked {
		// Normalize :param to {param}, matching LoadRegistryFromData.
		idx.push(strings.ToUpper(wr.Method), colonParamRegex.ReplaceAllString(wr.Path, "{${1}}"))
	}
	return nil
}

func (idx *PathIndex) push(httpMethod, path string) {
	idx.entries = append(idx.entries, indexEntry{
		httpMethod: httpMethod,
		path:       path,
		segments:   splitPathSegments(path),
	})
}

// Len reports how many method/path pairs the index holds.
func (idx *PathIndex) Len() int {
	return len(idx.entries)
}

// Lookup returns the path template matching the given method and path, which
// may be given in either template form ("/v1/environments/{environment_id}/campaigns")
// or resolved form ("/v1/environments/123/campaigns").
func (idx *PathIndex) Lookup(httpMethod, path string) (string, bool) {
	reqSegs := splitPathSegments(path)
	for i := range idx.entries {
		e := &idx.entries[i]
		if e.httpMethod != strings.ToUpper(httpMethod) {
			continue
		}
		if segmentsMatch(e.segments, reqSegs) {
			return e.path, true
		}
	}
	return "", false
}

// MethodsFor returns the HTTP methods the index holds for the given path
// shape, sorted. An empty result means no route has that shape at all, which
// distinguishes an unknown path from a path called with the wrong method.
func (idx *PathIndex) MethodsFor(path string) []string {
	reqSegs := splitPathSegments(path)
	seen := make(map[string]bool)
	var methods []string
	for i := range idx.entries {
		e := &idx.entries[i]
		if !segmentsMatch(e.segments, reqSegs) || seen[e.httpMethod] {
			continue
		}
		seen[e.httpMethod] = true
		methods = append(methods, e.httpMethod)
	}
	sort.Strings(methods)
	return methods
}

// CoverageDepth is how many leading segments a path must share with a
// documented route before this index is willing to call the path absent.
//
// Three is the length of a scope prefix — "/v1/environments/{environment_id}",
// "/v1/accounts/{account_id}", "/cdp/api/workspaces/{workspace_id}" — so a
// path is judged only once it is inside a scope the spec describes. Matching
// less than that (the first segment alone, say) polices every path under "/v1",
// including real endpoints the spec deliberately omits, while still leaving a
// whole undocumented tree such as "/v2/..." unchecked. Neither is this index's
// business: it exists to catch a wrong turn inside a documented neighbourhood,
// not to police the shape of the API.
//
// Two limits come with the choice. A real endpoint the spec omits from inside
// a documented scope is still called absent, and reaching it needs the check
// turned off. And a bogus segment sitting where a template expects an id
// ("…/customers/search" against "…/customers/{customer_id}") matches by shape,
// so the server, not this index, is what rejects it.
const CoverageDepth = 3

// Covers reports whether the path sits deep enough inside a documented scope
// for its absence from the index to mean something. A path that shares less
// than CoverageDepth leading segments with every known route — "/health",
// "/v1/login", anything under a tree the spec does not describe — cannot be
// called absent on the strength of this index alone.
func (idx *PathIndex) Covers(path string) bool {
	reqSegs := splitPathSegments(path)
	if len(reqSegs) < CoverageDepth {
		return false
	}
	for i := range idx.entries {
		if sharedLeadingSegments(idx.entries[i].segments, reqSegs) >= CoverageDepth {
			return true
		}
	}
	return false
}

// Suggest returns up to limit routes that share the longest leading run of
// segments with path, nearest first. Routes sharing fewer than two leading
// segments are dropped: at that distance the suggestion is noise.
func (idx *PathIndex) Suggest(path string, limit int) []Suggestion {
	const minSharedSegments = 2

	reqSegs := splitPathSegments(path)
	type scored struct {
		entry *indexEntry
		score int
	}

	var candidates []scored
	for i := range idx.entries {
		e := &idx.entries[i]
		score := sharedLeadingSegments(e.segments, reqSegs)
		if score < minSharedSegments {
			continue
		}
		candidates = append(candidates, scored{entry: e, score: score})
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].score != candidates[j].score {
			return candidates[i].score > candidates[j].score
		}
		// Prefer the shape closest in length to what was asked for, then a
		// stable alphabetical order so output does not vary between runs.
		di := absDiff(len(candidates[i].entry.segments), len(reqSegs))
		dj := absDiff(len(candidates[j].entry.segments), len(reqSegs))
		if di != dj {
			return di < dj
		}
		if candidates[i].entry.path != candidates[j].entry.path {
			return candidates[i].entry.path < candidates[j].entry.path
		}
		return candidates[i].entry.httpMethod < candidates[j].entry.httpMethod
	})

	var out []Suggestion
	seen := make(map[string]bool)
	for _, c := range candidates {
		if len(out) >= limit {
			break
		}
		key := c.entry.httpMethod + " " + c.entry.path
		if seen[key] {
			continue
		}
		seen[key] = true
		resource, _ := deriveFromWalkedPath(c.entry.path, c.entry.httpMethod)
		out = append(out, Suggestion{
			HTTPMethod: c.entry.httpMethod,
			Path:       c.entry.path,
			Resource:   resource,
		})
	}
	return out
}

func splitPathSegments(p string) []string {
	p = strings.Trim(p, "/")
	if p == "" {
		return nil
	}
	return strings.Split(p, "/")
}

func isPlaceholderSegment(seg string) bool {
	return strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}")
}

// segmentsMatch reports whether a path template's segments match a request's.
// A {placeholder} matches any single concrete segment, so both the template
// and resolved forms of a path match the same route.
func segmentsMatch(templateSegs, reqSegs []string) bool {
	if len(templateSegs) != len(reqSegs) {
		return false
	}
	for i, ts := range templateSegs {
		if isPlaceholderSegment(ts) {
			continue
		}
		if ts != reqSegs[i] {
			return false
		}
	}
	return true
}

// sharedLeadingSegments counts how many leading segments two paths have in
// common, treating a {placeholder} as matching any concrete segment.
func sharedLeadingSegments(templateSegs, reqSegs []string) int {
	n := 0
	for i := 0; i < len(templateSegs) && i < len(reqSegs); i++ {
		if !isPlaceholderSegment(templateSegs[i]) && templateSegs[i] != reqSegs[i] {
			break
		}
		n++
	}
	return n
}

func absDiff(a, b int) int {
	if a > b {
		return a - b
	}
	return b - a
}
