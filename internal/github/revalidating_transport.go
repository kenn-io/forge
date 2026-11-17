package github

import (
	"bytes"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"
	"github.com/jellydator/ttlcache/v3"
	platformgithub "go.kenn.io/forge/platform/github"
)

// revalidatedPathPattern matches CI and repository metadata reads that sync polls:
// check runs, combined status, workflow runs, base branch rules and protection,
// repository identity, releases, tags, labels, and issues.
var revalidatedPathPattern = regexp.MustCompile(
	`^(?:/api/v3)?/(?:repositories/\d+|repos/[^/]+/[^/]+/(?:commits/[^/]+/(?:check-runs|status)|actions/runs|rules/branches/.+|branches/.+|releases|tags|labels|issues))$`,
)

const (
	// revalidatedEntryLimit bounds the cache for metadata and CI pages.
	// Superseded heads age out as new heads are polled.
	revalidatedEntryLimit = 2048
	// revalidatedBodyLimit skips caching an unusually large page.
	revalidatedBodyLimit = 1 << 20
	// issuePageEntryLimit keeps every issue page of about 2,000 repositories with
	// 300 open issues, so one sync pass does not evict the pages the next one reads.
	issuePageEntryLimit = 8192
	// issuePageBodyLimit is Forge's local bound on one cached issue page.
	issuePageBodyLimit = 32 << 20
	// issuePageCacheBytes bounds the issue-page bodies cached at once. The
	// least recently read pages are evicted first.
	issuePageCacheBytes = 256 << 20
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
	issues *ttlcache.Cache[string, revalidatedEntry]
}

func newRevalidatingTransport(base http.RoundTripper) *revalidatingTransport {
	return newRevalidatingTransportWithIssueBytes(base, issuePageCacheBytes)
}

func newRevalidatingTransportWithIssueBytes(base http.RoundTripper, issueBytes uint64) *revalidatingTransport {
	cache, err := lru.New[string, revalidatedEntry](revalidatedEntryLimit)
	if err != nil {
		panic(err) // only for a non-positive size
	}
	// A 304 does not refresh an issue page, so it expires etagTTL after its 200.
	issues := ttlcache.New(
		ttlcache.WithTTL[string, revalidatedEntry](etagTTL),
		ttlcache.WithDisableTouchOnHit[string, revalidatedEntry](),
		ttlcache.WithCapacity[string, revalidatedEntry](issuePageEntryLimit),
		ttlcache.WithMaxCost(issueBytes, issuePageCost),
	)
	return &revalidatingTransport{base: base, cache: cache, issues: issues}
}

func issuePageCost(item ttlcache.CostItem[string, revalidatedEntry]) uint64 {
	return uint64(len(item.Value.body))
}

func (t *revalidatingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method != http.MethodGet || !revalidatedPathPattern.MatchString(req.URL.Path) ||
		platformgithub.UnconditionalRead(req.Context()) || req.Header.Get("If-None-Match") != "" {
		return t.base.RoundTrip(req)
	}
	key := req.URL.String()
	issuePage := strings.HasSuffix(req.URL.Path, "/issues")
	var cache revalidatedCache = metadataCache{t.cache}
	limit := revalidatedBodyLimit
	if issuePage {
		cache, limit = issuePageCache{t.issues}, issuePageBodyLimit
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
		return store(cache, key, resp, limit)
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

type issuePageCache struct {
	*ttlcache.Cache[string, revalidatedEntry]
}

func (c issuePageCache) get(key string) (revalidatedEntry, bool) {
	item := c.Get(key)
	if item == nil || time.Since(item.Value().cachedAt) >= etagTTL {
		return revalidatedEntry{}, false
	}
	return item.Value(), true
}

func (c issuePageCache) add(key string, entry revalidatedEntry) {
	c.Set(key, entry, ttlcache.DefaultTTL)
}

func (c issuePageCache) remove(key string) { c.Delete(key) }

func store(cache revalidatedCache, key string, resp *http.Response, limit int) (*http.Response, error) {
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
