package github

import (
	"bytes"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"
	platformgithub "go.kenn.io/forge/platform/github"
)

// revalidatedPathPattern matches CI and repository metadata reads that sync polls:
// check runs, combined status, workflow runs, base branch rules and protection,
// repository identity, releases, tags, labels, and issues.
var revalidatedPathPattern = regexp.MustCompile(
	`^(?:/api/v3)?/(?:repositories/\d+|repos/[^/]+/[^/]+/(?:commits/[^/]+/(?:check-runs|status)|actions/runs|rules/branches/.+|branches/.+|releases|tags|labels|issues))$`,
)

var issueListPath = regexp.MustCompile(`^(?:/api/v3)?/repos/[^/]+/[^/]+/issues$`)

const (
	// revalidatedEntryLimit bounds the cache at about four metadata pages per
	// tracked repository plus CI pages. Superseded heads age out as new heads are polled.
	revalidatedEntryLimit = 2048
	// revalidatedBodyLimit skips caching an unusually large page.
	revalidatedBodyLimit = 1 << 20
	// issuePageBodyLimit is Forge's local bound on one cached issue page.
	issuePageBodyLimit = 32 << 20
	// issuePageCacheBytes bounds the issue-page bodies cached at once.
	issuePageCacheBytes = 256 << 20
	// issuePageEntryLimit bounds cached issue pages, whose keys and headers the
	// byte budget does not count; an empty page costs no body bytes.
	issuePageEntryLimit = 8192
)

type revalidatedEntry struct {
	etag     string
	header   http.Header
	body     []byte
	cachedAt time.Time
}

// revalidatingTransport sends If-None-Match for CI, metadata, and issue reads and answers a
// 304 Not Modified with the cached 200 response. GitHub does not charge
// a 304 against the rate limit, and the budget transport refunds it, so
// polling unchanged CI, metadata, and issue lists costs nothing. Callers see the same 200 they would
// see from an unconditional read: a 304 proves the body is current.
//
// A 304 carries no Link header, so the cached headers are replayed for
// pagination, with the 304's rate-limit headers laid over them.
type revalidatingTransport struct {
	base   http.RoundTripper
	cache  *lru.Cache[string, revalidatedEntry]
	issues *issuePageCache
}

func newRevalidatingTransport(base http.RoundTripper) *revalidatingTransport {
	cache, err := lru.New[string, revalidatedEntry](revalidatedEntryLimit)
	if err != nil {
		panic(err) // only for a non-positive size
	}
	issues := &issuePageCache{limit: issuePageCacheBytes, maxPages: issuePageEntryLimit, pages: map[string]revalidatedEntry{}}
	return &revalidatingTransport{base: base, cache: cache, issues: issues}
}

func (t *revalidatingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method != http.MethodGet || !revalidatedPathPattern.MatchString(req.URL.Path) ||
		platformgithub.UnconditionalRead(req.Context()) || req.Header.Get("If-None-Match") != "" {
		return t.base.RoundTrip(req)
	}
	key := req.URL.String()
	var cache revalidatedCache = metadataCache{t.cache}
	limit := revalidatedBodyLimit
	if issueListPath.MatchString(req.URL.Path) {
		cache, limit = t.issues, issuePageBodyLimit
	}
	entry, cached := cache.get(key)
	sent := req
	if cached {
		sent = req.Clone(req.Context())
		sent.Header.Set("If-None-Match", entry.etag)
	}
	resp, err := t.base.RoundTrip(sent)
	if err != nil {
		return resp, err
	}
	switch {
	case resp.StatusCode == http.StatusNotModified && cached:
		_ = resp.Body.Close()
		return replayCachedResponse(entry, resp, req), nil
	case resp.StatusCode == http.StatusOK:
		return store(cache, key, req, resp, limit)
	default:
		cache.remove(key)
		return resp, nil
	}
}

type revalidatedCache interface {
	get(key string) (revalidatedEntry, bool)
	add(key string, entry revalidatedEntry)
	remove(key string)
}

type metadataCache struct {
	*lru.Cache[string, revalidatedEntry]
}

func (c metadataCache) get(key string) (revalidatedEntry, bool) { return c.Get(key) }
func (c metadataCache) add(key string, entry revalidatedEntry)  { c.Add(key, entry) }
func (c metadataCache) remove(key string)                       { c.Remove(key) }

// issuePageCache keeps each issue page until etagTTL after its 200; a 304 does
// not refresh it. When full it caches no new page rather than evict one: a sync
// pass over more pages than fit would evict each page before the next pass.
type issuePageCache struct {
	mu       sync.Mutex
	limit    int
	maxPages int
	bytes    int
	pages    map[string]revalidatedEntry
}

func (c *issuePageCache) get(key string) (revalidatedEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.pages[key]
	if ok && time.Since(entry.cachedAt) >= etagTTL {
		c.removeLocked(key)
		return revalidatedEntry{}, false
	}
	return entry, ok
}

func (c *issuePageCache) add(key string, entry revalidatedEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.removeLocked(key)
	if !c.fitsLocked(len(entry.body)) {
		for k, e := range c.pages {
			if time.Since(e.cachedAt) >= etagTTL {
				c.removeLocked(k)
			}
		}
	}
	if c.fitsLocked(len(entry.body)) {
		c.pages[key] = entry
		c.bytes += len(entry.body)
	}
}

func (c *issuePageCache) fitsLocked(size int) bool {
	return len(c.pages) < c.maxPages && c.bytes+size <= c.limit
}

func (c *issuePageCache) remove(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.removeLocked(key)
}

func (c *issuePageCache) removeLocked(key string) {
	c.bytes -= len(c.pages[key].body)
	delete(c.pages, key)
}

func store(cache revalidatedCache, key string, req *http.Request, resp *http.Response, limit int) (*http.Response, error) {
	if _, issues := cache.(*issuePageCache); issues && !cacheableIssuePage(req, resp) {
		cache.remove(key)
		return resp, nil
	}
	etag := resp.Header.Get("ETag")
	if etag == "" {
		cache.remove(key)
		return resp, nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(limit+1)))
	if err != nil {
		_ = resp.Body.Close()
		cache.remove(key)
		return nil, err
	}
	if len(body) > limit {
		cache.remove(key)
		resp.Body = struct {
			io.Reader
			io.Closer
		}{io.MultiReader(bytes.NewReader(body), resp.Body), resp.Body}
		return resp, nil
	}
	_ = resp.Body.Close()
	cache.add(key, revalidatedEntry{etag: etag, header: resp.Header.Clone(), body: body, cachedAt: time.Now()})
	resp.Body = io.NopCloser(bytes.NewReader(body))
	return resp, nil
}

func cacheableIssuePage(req *http.Request, resp *http.Response) bool {
	page, _ := strconv.Atoi(req.URL.Query().Get("page"))
	if page > 1 {
		return true
	}
	for _, link := range resp.Header.Values("Link") {
		if strings.Contains(link, `rel="next"`) {
			return true
		}
	}
	return false
}

func replayCachedResponse(entry revalidatedEntry, notModified *http.Response, req *http.Request) *http.Response {
	header := entry.header.Clone()
	for name, values := range notModified.Header {
		if name == "Date" || strings.HasPrefix(strings.ToLower(name), "x-ratelimit-") {
			header[name] = values
		}
	}
	header.Del("Content-Length")
	return &http.Response{
		Status:        "200 OK",
		StatusCode:    http.StatusOK,
		Proto:         notModified.Proto,
		ProtoMajor:    notModified.ProtoMajor,
		ProtoMinor:    notModified.ProtoMinor,
		Header:        header,
		Body:          io.NopCloser(bytes.NewReader(entry.body)),
		ContentLength: int64(len(entry.body)),
		Request:       req,
	}
}
