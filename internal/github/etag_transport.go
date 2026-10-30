package github

import (
	"errors"
	"net/http"
	urlpkg "net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	platformgithub "go.kenn.io/forge/platform/github"
)

const etagTTL = 30 * time.Minute

// etagEligibleListPath matches list endpoints that return a collection
// ETag. Supports both github.com (`/repos/{owner}/{name}/{pulls,issues}`)
// and GitHub Enterprise (`/api/v3/repos/...`), since GHE clients route
// through the same RoundTripper.
var etagEligibleListPath = regexp.MustCompile(
	`^(?:/api/v3)?/repos/([^/]+)/([^/]+)/(pulls|issues)$`,
)

var etagEligibleCommentPath = regexp.MustCompile(
	`^(?:/api/v3)?/repos/[^/]+/[^/]+/issues/[0-9]+/comments$`,
)

type etagEntry struct {
	etag     string
	cachedAt time.Time
	// paginated marks a list too long for one page. A page validator cannot
	// vouch for such a list, so detectorETag, the validator of the repo's
	// most recently updated item, stands in for it.
	paginated    bool
	detectorETag string
}

type etagTransport struct {
	base  http.RoundTripper
	cache sync.Map // URL string -> etagEntry
}

func (t *etagTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req == nil || req.URL == nil {
		return nil, errors.New("nil request")
	}

	// Gate: only GET requests to allowlisted endpoints
	if req.Method != http.MethodGet || !isETagEligible(req.URL.Path) {
		return t.base.RoundTrip(req)
	}
	if platformgithub.UnconditionalRead(req.Context()) {
		return t.base.RoundTrip(req)
	}

	// Later pages are read unconditionally. A failed one, such as a page
	// the sync budget refused, leaves the list incomplete, so the list's
	// validators must not vouch for it on the next read.
	if page := req.URL.Query().Get("page"); page != "" && page != "1" {
		resp, err := t.base.RoundTrip(req)
		if err != nil || resp.StatusCode != http.StatusOK {
			if owner, name, kind, ok := listKind(req.URL); ok {
				t.invalidateRepo(owner, name, kind)
			}
		}
		return resp, err
	}

	url := req.URL.String()

	// Check cache
	var detectorETag string
	if val, ok := t.cache.Load(url); ok {
		entry := val.(etagEntry)
		fresh := time.Since(entry.cachedAt) < etagTTL
		switch {
		case entry.paginated && detectorEligible(req.URL):
			unchanged, current := t.checkListDetector(req, entry, fresh)
			if unchanged != nil {
				return unchanged, nil
			}
			detectorETag = current
		case fresh:
			req = req.Clone(req.Context())
			req.Header.Set("If-None-Match", entry.etag)
		default:
			t.cache.Delete(url)
		}
	}

	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return resp, err
	}

	switch resp.StatusCode {
	case http.StatusOK:
		etag := resp.Header.Get("ETag")
		switch {
		case responseHasPaginationLink(resp) && detectorEligible(req.URL):
			// A page validator does not prove the complete paginated
			// collection is unchanged, so the list keeps only the detector
			// validator read before this fetch. Storing it now, once the
			// list arrived, keeps a failed fetch from hiding the change.
			t.cache.Store(url, etagEntry{
				cachedAt: time.Now(), paginated: true, detectorETag: detectorETag,
			})
		case responseHasPaginationLink(resp):
			t.cache.Delete(url)
		case etag != "":
			t.cache.Store(url, etagEntry{etag: etag, cachedAt: time.Now()})
		default:
			// No ETag — drop any stale validator so the next request
			// fetches fresh data instead of asserting an out-of-date
			// If-None-Match.
			t.cache.Delete(url)
		}
	case http.StatusNotModified:
		// Deliberately do NOT refresh cachedAt here. The TTL must
		// expire periodically so we issue an unconditional fetch
		// that can detect list growth beyond page 1 (a single-page
		// list that grows to two pages returns 304 for page 1 since
		// the first page content is unchanged). One unconditional
		// fetch per TTL window is a cheap safety net.
	}

	return resp, nil
}

// checkListDetector reads the repo's most recently updated item for a
// paginated list. Opening, closing, merging, or editing any item bumps its
// update time and moves it to the top, so an unchanged top item proves the
// whole list is unchanged. It then answers the list request with a 304 that
// costs no quota. Otherwise it returns the detector validator to store once
// the full list arrives. It vouches only while the list entry is fresh, so a
// full read still happens every etagTTL.
func (t *etagTransport) checkListDetector(
	req *http.Request, entry etagEntry, fresh bool,
) (*http.Response, string) {
	detector := req.Clone(req.Context())
	detectorURL := *req.URL
	detectorURL.RawQuery = urlpkg.Values{
		"state": {"all"}, "sort": {"updated"}, "direction": {"desc"}, "per_page": {"1"},
	}.Encode()
	detector.URL = &detectorURL
	detector.Header.Del("If-None-Match")
	if fresh && entry.detectorETag != "" {
		detector.Header.Set("If-None-Match", entry.detectorETag)
	}
	resp, err := t.base.RoundTrip(detector)
	if err != nil {
		return nil, ""
	}
	_ = resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotModified && fresh && entry.detectorETag != "":
		return &http.Response{
			Status:     "304 Not Modified",
			StatusCode: http.StatusNotModified,
			Proto:      resp.Proto,
			ProtoMajor: resp.ProtoMajor,
			ProtoMinor: resp.ProtoMinor,
			Header:     resp.Header.Clone(),
			Body:       http.NoBody,
			Request:    req,
		}, ""
	case resp.StatusCode == http.StatusOK:
		return nil, resp.Header.Get("ETag")
	default:
		return nil, ""
	}
}

// listKind names the repository and list ("pulls" or "issues") a list
// request reads.
func listKind(u *urlpkg.URL) (owner, name, kind string, ok bool) {
	match := etagEligibleListPath.FindStringSubmatch(u.Path)
	if match == nil {
		return "", "", "", false
	}
	return match[1], match[2], match[3], true
}

// detectorEligible reports whether a paginated list can be vouched for by
// its most recently updated item. Only pull request lists qualify: a pull
// request cannot be deleted or transferred, but an issue can, and removing
// one that is not on top changes no update time.
func detectorEligible(u *urlpkg.URL) bool {
	_, _, kind, ok := listKind(u)
	return ok && kind == "pulls"
}

func responseHasPaginationLink(resp *http.Response) bool {
	if resp == nil {
		return false
	}
	for _, value := range resp.Header.Values("Link") {
		for part := range strings.SplitSeq(value, ",") {
			if strings.TrimSpace(part) != "" {
				return true
			}
		}
	}
	return false
}

func isETagEligible(path string) bool {
	return etagEligibleListPath.MatchString(path) ||
		etagEligibleCommentPath.MatchString(path)
}

// invalidateRepo drops cached ETag entries for the given repo's list
// endpoints. The endpoints parameter selects which to invalidate —
// "pulls", "issues", or "comments" — and an empty slice clears every
// supported repo-scoped list path. Used by the sync engine to force an
// unconditional refetch after a partial failure so the next cycle
// re-applies per-item state that the previous cycle failed to persist.
func (t *etagTransport) invalidateRepo(owner, name string, endpoints ...string) {
	base := "/repos/" + owner + "/" + name + "/"
	gheBase := "/api/v3/repos/" + owner + "/" + name + "/"
	t.cache.Range(func(k, _ any) bool {
		urlStr, ok := k.(string)
		if !ok {
			return true
		}
		parsed, err := urlpkg.Parse(urlStr)
		if err != nil {
			return true
		}
		if matchesInvalidateEndpoint(parsed.Path, base, gheBase, endpoints) {
			t.cache.Delete(k)
		}
		return true
	})
}

func matchesInvalidateEndpoint(path, base, gheBase string, endpoints []string) bool {
	if len(endpoints) == 0 {
		// Empty means invalidate every supported repo-scoped list path
		// so callers can recover from partial-failure syncs without
		// needing to enumerate every endpoint we cache.
		return path == base+"pulls" || path == gheBase+"pulls" ||
			path == base+"issues" || path == gheBase+"issues" ||
			isCommentListPath(path, base, gheBase)
	}
	var exactPrefixes []string
	for _, ep := range endpoints {
		switch ep {
		case "comments":
			if isCommentListPath(path, base, gheBase) {
				return true
			}
		default:
			exactPrefixes = append(exactPrefixes, base+ep, gheBase+ep)
		}
	}
	return slices.Contains(exactPrefixes, path)
}

func isCommentListPath(path, base, gheBase string) bool {
	for _, prefix := range []string{base + "issues/", gheBase + "issues/"} {
		if strings.HasPrefix(path, prefix) && strings.HasSuffix(path, "/comments") {
			return true
		}
	}
	return false
}
