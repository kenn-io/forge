package github

import (
	"bytes"
	"io"
	"net/http"
	"regexp"
	"strings"

	lru "github.com/hashicorp/golang-lru/v2"
	platformgithub "go.kenn.io/forge/platform/github"
)

// revalidatedPathPattern matches CI and repository metadata reads that sync polls:
// check runs, combined status, workflow runs, base branch rules and protection,
// repository identity, releases, tags, and labels.
var revalidatedPathPattern = regexp.MustCompile(
	`^(?:/api/v3)?/(?:repositories/\d+|repos/[^/]+/[^/]+/(?:commits/[^/]+/(?:check-runs|status)|actions/runs|rules/branches/.+|branches/.+|releases|tags|labels))$`,
)

const (
	// revalidatedEntryLimit bounds the cache at about four metadata pages per
	// tracked repository plus CI pages. Superseded heads age out as new heads are polled.
	revalidatedEntryLimit = 2048
	// revalidatedBodyLimit skips caching an unusually large page.
	revalidatedBodyLimit = 1 << 20
)

type revalidatedEntry struct {
	etag   string
	header http.Header
	body   []byte
}

// revalidatingTransport sends If-None-Match for CI and repository metadata reads and answers a
// 304 Not Modified with the cached 200 response. GitHub does not charge
// a 304 against the rate limit, and the budget transport refunds it, so
// polling unchanged CI and metadata costs nothing. Callers see the same 200 they would
// see from an unconditional read: a 304 proves the body is current.
//
// A 304 carries no Link header, so the cached headers are replayed for
// pagination, with the 304's rate-limit headers laid over them.
type revalidatingTransport struct {
	base  http.RoundTripper
	cache *lru.Cache[string, revalidatedEntry]
}

func newRevalidatingTransport(base http.RoundTripper) *revalidatingTransport {
	cache, err := lru.New[string, revalidatedEntry](revalidatedEntryLimit)
	if err != nil {
		panic(err) // only for a non-positive size
	}
	return &revalidatingTransport{base: base, cache: cache}
}

func (t *revalidatingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method != http.MethodGet || !revalidatedPathPattern.MatchString(req.URL.Path) ||
		platformgithub.UnconditionalRead(req.Context()) {
		return t.base.RoundTrip(req)
	}
	key := req.URL.String()
	entry, cached := t.cache.Get(key)
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
		return t.store(key, resp)
	default:
		t.cache.Remove(key)
		return resp, nil
	}
}

func (t *revalidatingTransport) store(key string, resp *http.Response) (*http.Response, error) {
	etag := resp.Header.Get("ETag")
	if etag == "" {
		t.cache.Remove(key)
		return resp, nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, revalidatedBodyLimit+1))
	if err != nil {
		_ = resp.Body.Close()
		t.cache.Remove(key)
		return nil, err
	}
	if len(body) > revalidatedBodyLimit {
		t.cache.Remove(key)
		resp.Body = struct {
			io.Reader
			io.Closer
		}{io.MultiReader(bytes.NewReader(body), resp.Body), resp.Body}
		return resp, nil
	}
	_ = resp.Body.Close()
	t.cache.Add(key, revalidatedEntry{etag: etag, header: resp.Header.Clone(), body: body})
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
