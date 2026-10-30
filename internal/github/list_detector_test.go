package github

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	platformgithub "go.kenn.io/forge/platform/github"
)

// largeListServer serves a two-page open pull request list and the
// one-item detector read. generation moves the most recently updated item.
type largeListServer struct {
	generation  atomic.Int64
	failList    atomic.Bool
	failPage2   atomic.Bool
	listPages   atomic.Int64
	detectors   atomic.Int64
	detector304 atomic.Int64
}

func (s *largeListServer) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/repos/acme/widget/pulls" {
			assert.Failf(t, "unexpected path", "%s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		query := r.URL.Query()
		if query.Get("state") == "all" {
			s.detectors.Add(1)
			assert.Equal(t, "1", query.Get("per_page"))
			assert.Equal(t, "updated", query.Get("sort"))
			etag := `"top-` + strconv.FormatInt(s.generation.Load(), 10) + `"`
			if r.Header.Get("If-None-Match") == etag {
				s.detector304.Add(1)
				w.WriteHeader(http.StatusNotModified)
				return
			}
			w.Header().Set("ETag", etag)
			_, _ = fmt.Fprintf(w, `[{"number":1,"updated_at":"2024-06-01T12:%02d:00Z"}]`, s.generation.Load())
			return
		}
		s.listPages.Add(1)
		if s.failList.Load() {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.Header().Set("ETag", `"page-etag"`)
		if query.Get("page") == "" || query.Get("page") == "1" {
			w.Header().Set("Link", fmt.Sprintf(`<http://%s%s?state=open&per_page=100&page=2>; rel="next"`, r.Host, r.URL.Path))
			_, _ = w.Write([]byte(`[{"number":1,"state":"open"}]`))
			return
		}
		if s.failPage2.Load() {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(`[{"number":2,"state":"open"}]`))
	})
}

func TestLargeOpenListIsSkippedWhileNoPullRequestChanged(t *testing.T) {
	t.Parallel()

	assert := assert.New(t)
	require := require.New(t)

	srv := &largeListServer{}
	server := httptest.NewServer(srv.handler(t))
	t.Cleanup(server.Close)
	budget := NewSyncBudget(100)
	client, err := NewClient(testTokenSource("token"), "github.com", nil, budget,
		WithBaseURLForTesting(server.URL))
	require.NoError(err)
	ctx := WithSyncBudget(t.Context())
	list := func() ([]int, error) {
		prs, err := client.ListOpenPullRequests(ctx, "acme", "widget")
		numbers := make([]int, 0, len(prs))
		for _, pr := range prs {
			numbers = append(numbers, pr.GetNumber())
		}
		return numbers, err
	}

	// First read learns the list is paginated; second captures the
	// detector before reading the list again in full.
	for range 2 {
		numbers, err := list()
		require.NoError(err)
		assert.Equal([]int{1, 2}, numbers)
	}
	assert.Equal(int64(4), srv.listPages.Load())
	assert.Equal(int64(1), srv.detectors.Load())

	// Nothing changed: one free detector 304 answers for the whole list.
	spent := budget.Spent()
	_, err = list()
	require.True(platformgithub.IsNotModified(err), "an unchanged large list reads as not modified: %v", err)
	assert.Equal(int64(4), srv.listPages.Load())
	assert.Equal(int64(1), srv.detector304.Load())
	assert.Equal(spent, budget.Spent(), "the detector 304 must not spend the sync budget")

	// A pull request changed, but the full read fails: the new detector
	// validator must not be kept, or the next read would hide the change.
	srv.generation.Add(1)
	srv.failList.Store(true)
	_, err = list()
	require.Error(err)
	srv.failList.Store(false)
	numbers, err := list()
	require.NoError(err)
	assert.Equal([]int{1, 2}, numbers)

	// Once the full read succeeds, the next unchanged read is free again.
	_, err = list()
	require.True(platformgithub.IsNotModified(err))
	assert.Equal(int64(2), srv.detector304.Load())
}

func TestExpiredLargeListDetectorCannotVouch(t *testing.T) {
	t.Parallel()

	assert := assert.New(t)
	require := require.New(t)

	url := "https://api.github.com/repos/o/n/pulls?per_page=100&state=open"
	var detectorConditional, listReads int
	et := &etagTransport{base: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		rec := httptest.NewRecorder()
		if r.URL.Query().Get("state") == "all" {
			if r.Header.Get("If-None-Match") != "" {
				detectorConditional++
			}
			rec.Header().Set("ETag", `"top-new"`)
			rec.WriteHeader(http.StatusOK)
			return rec.Result(), nil
		}
		listReads++
		rec.Header().Set("Link", `<https://api.github.com/repos/o/n/pulls?page=2>; rel="next"`)
		rec.WriteHeader(http.StatusOK)
		return rec.Result(), nil
	})}
	et.cache.Store(url, etagEntry{
		paginated: true, detectorETag: `"top-old"`,
		cachedAt: time.Now().Add(-etagTTL - time.Minute),
	})

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	require.NoError(err)
	resp, err := et.RoundTrip(req)
	require.NoError(err)
	defer resp.Body.Close()

	assert.Equal(http.StatusOK, resp.StatusCode)
	assert.Equal(0, detectorConditional, "an expired detector must not vouch")
	assert.Equal(1, listReads)
	val, ok := et.cache.Load(url)
	require.True(ok)
	entry := val.(etagEntry)
	assert.Equal(`"top-new"`, entry.detectorETag)
	assert.WithinDuration(time.Now(), entry.cachedAt, time.Minute)
}

func TestFailedLaterPageCannotLeaveTheDetectorVouching(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		fail func(srv *largeListServer, budget *SyncBudget, restore bool)
	}{
		{name: "provider error", fail: func(srv *largeListServer, _ *SyncBudget, restore bool) {
			srv.failPage2.Store(!restore)
		}},
		{name: "budget refusal", fail: func(_ *largeListServer, budget *SyncBudget, restore bool) {
			if restore {
				budget.Reset()
				budget.SetLimit(100)
				return
			}
			// The detector and page 1 fit; page 2 is refused before the wire.
			budget.Reset()
			budget.SetLimit(2)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert := assert.New(t)
			require := require.New(t)

			srv := &largeListServer{}
			server := httptest.NewServer(srv.handler(t))
			t.Cleanup(server.Close)
			budget := NewSyncBudget(100)
			client, err := NewClient(testTokenSource("token"), "github.com", nil, budget,
				WithBaseURLForTesting(server.URL))
			require.NoError(err)
			ctx := WithSyncBudget(t.Context())
			for range 2 {
				_, err := client.ListOpenPullRequests(ctx, "acme", "widget")
				require.NoError(err)
			}

			// A pull request changed and page 1 arrived, but page 2 failed.
			srv.generation.Add(1)
			tc.fail(srv, budget, false)
			_, err = client.ListOpenPullRequests(ctx, "acme", "widget")
			require.Error(err)

			// After recovery the list is read in full, not vouched for by a
			// detector validator taken from the incomplete read.
			tc.fail(srv, budget, true)
			prs, err := client.ListOpenPullRequests(ctx, "acme", "widget")
			require.NoError(err, "a detector taken from an incomplete read must not vouch")
			assert.Len(prs, 2)
		})
	}
}

func TestLargeIssueListIsNotVouchedForByItsNewestIssue(t *testing.T) {
	t.Parallel()

	assert := assert.New(t)
	require := require.New(t)

	var detectors, pages atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ETag", `"issues"`)
		if r.URL.Query().Get("state") == "all" {
			detectors.Add(1)
			_, _ = w.Write([]byte(`[]`))
			return
		}
		pages.Add(1)
		if r.URL.Query().Get("page") == "" {
			w.Header().Set("Link", fmt.Sprintf(`<http://%s%s?state=open&page=2>; rel="next"`, r.Host, r.URL.Path))
		}
		_, _ = w.Write([]byte(`[]`))
	}))
	t.Cleanup(server.Close)
	client, err := NewClient(testTokenSource("token"), "github.com", nil, nil,
		WithBaseURLForTesting(server.URL))
	require.NoError(err)
	for range 3 {
		_, err := client.ListOpenIssues(t.Context(), "acme", "widget")
		require.NoError(err)
	}
	// An issue can be deleted or transferred without moving the newest
	// one, so large issue lists are always read in full.
	assert.Zero(detectors.Load())
	assert.Equal(int64(6), pages.Load())
}
