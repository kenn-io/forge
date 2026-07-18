package gitlab

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	ghsync "go.kenn.io/middleman/internal/github"
	"go.kenn.io/middleman/internal/platform"
)

func gitLabPagesTestRef() platform.RepoRef {
	return platform.RepoRef{
		Platform: platform.KindGitLab, Host: "gitlab.example.com",
		Owner: "group", Name: "project", RepoPath: "group/project", PlatformID: 42,
		PlatformExternalID: "42", WebURL: "https://gitlab.example.com/group/project",
	}
}

// requestRecorder captures the method, path, and raw query of every request a
// test server receives so parity tests can assert the canonical method and the
// legacy or live delegate issued identical requests.
type requestRecorder struct {
	mu    sync.Mutex
	lines []string
}

func (r *requestRecorder) record(req *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = append(r.lines, req.Method+" "+req.URL.EscapedPath()+"?"+req.URL.RawQuery)
}

func (r *requestRecorder) take() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.lines
	r.lines = nil
	return out
}

// TestGitLabListIssuesPageDispatchesByQuery proves one method owns all three
// issue inventory request shapes: StateOpen drains the open list, and both
// StateAll traversals run keyset pagination — created ascending for the
// historical scan and updated ascending behind an inclusive watermark served
// through GitLab's exclusive updated_after filter for the maintenance scan.
func TestGitLabListIssuesPageDispatchesByQuery(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	watermark := time.Date(2026, 7, 1, 2, 3, 4, 0, time.UTC)

	var states, orders, sorts, paginations, updatedAfters []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal("/api/v4/projects/42/issues", r.URL.EscapedPath())
		assert.Equal("100", r.URL.Query().Get("per_page"))
		states = append(states, r.URL.Query().Get("state"))
		orders = append(orders, r.URL.Query().Get("order_by"))
		sorts = append(sorts, r.URL.Query().Get("sort"))
		paginations = append(paginations, r.URL.Query().Get("pagination"))
		updatedAfters = append(updatedAfters, r.URL.Query().Get("updated_after"))
		writeJSON(w, `[{"id":101,"iid":1,"title":"issue","state":"closed","created_at":"2025-01-01T00:00:00Z","updated_at":"2026-07-01T02:03:04Z"}]`)
	}))
	defer server.Close()
	client := newTestClient(t, server.URL)
	ref := gitLabPagesTestRef()

	open, err := client.ListIssuesPage(t.Context(), ref, platform.ItemPageQuery{
		State: platform.ItemStateOpen, Order: platform.ItemOrderUpdated,
	})
	require.NoError(err)
	require.Len(open.Items, 1)
	assert.Equal(1, open.Items[0].Number)
	assert.True(open.Exhausted)

	historical, err := client.ListIssuesPage(t.Context(), ref, platform.ItemPageQuery{
		State: platform.ItemStateAll, Order: platform.ItemOrderCreated,
	})
	require.NoError(err)
	require.Len(historical.Items, 1)
	assert.True(historical.Exhausted)

	updated, err := client.ListIssuesPage(t.Context(), ref, platform.ItemPageQuery{
		State: platform.ItemStateAll, Order: platform.ItemOrderUpdated, UpdatedSince: &watermark,
	})
	require.NoError(err)
	require.Len(updated.Items, 1)
	assert.Equal(watermark, updated.Items[0].UpdatedAt)

	assert.Equal([]string{"opened", "all", "all"}, states)
	assert.Equal([]string{"", "created_at", "updated_at"}, orders)
	// Both keyset traversals ascend: under a keyset cursor an updated_at bump
	// only moves an item forward past the cursor, so ascending maintenance
	// re-serves moved items instead of skipping them.
	assert.Equal([]string{"", "asc", "asc"}, sorts)
	assert.Equal([]string{"", "keyset", "keyset"}, paginations)
	require.Len(updatedAfters, 3)
	assert.Empty(updatedAfters[0])
	assert.Empty(updatedAfters[1])
	overlapped, err := time.Parse(time.RFC3339Nano, updatedAfters[2])
	require.NoError(err)
	assert.True(overlapped.Before(watermark), "updated_after must overlap the inclusive watermark")
}

// TestGitLabListMergeRequestsPageDispatchesByQuery mirrors the issue dispatch
// proof for merge requests and pins the open-scan merge-status recheck flag to
// the canonical open branch.
func TestGitLabListMergeRequestsPageDispatchesByQuery(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	watermark := time.Date(2026, 7, 1, 2, 3, 4, 0, time.UTC)

	var states, orders, sorts, rechecks []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal("/api/v4/projects/42/merge_requests", r.URL.EscapedPath())
		states = append(states, r.URL.Query().Get("state"))
		orders = append(orders, r.URL.Query().Get("order_by"))
		sorts = append(sorts, r.URL.Query().Get("sort"))
		rechecks = append(rechecks, r.URL.Query().Get("with_merge_status_recheck"))
		writeJSON(w, `[{"id":201,"iid":2,"project_id":42,"title":"mr","state":"merged","created_at":"2025-01-03T00:00:00Z","updated_at":"2026-07-01T02:03:04Z"}]`)
	}))
	defer server.Close()
	client := newTestClient(t, server.URL)
	ref := gitLabPagesTestRef()

	open, err := client.ListMergeRequestsPage(t.Context(), ref, platform.ItemPageQuery{
		State: platform.ItemStateOpen, Order: platform.ItemOrderUpdated,
	})
	require.NoError(err)
	require.Len(open.Items, 1)
	assert.True(open.Exhausted)

	_, err = client.ListMergeRequestsPage(t.Context(), ref, platform.ItemPageQuery{
		State: platform.ItemStateAll, Order: platform.ItemOrderCreated,
	})
	require.ErrorIs(err, platform.ErrUnsupportedCapability)
	_, err = client.ListMergeRequestsPage(t.Context(), ref, platform.ItemPageQuery{
		State: platform.ItemStateAll, Order: platform.ItemOrderUpdated, UpdatedSince: &watermark,
	})
	require.NoError(err)

	assert.Equal([]string{"opened", "all"}, states)
	assert.Equal([]string{"", "updated_at"}, orders)
	assert.Equal([]string{"", "desc"}, sorts)
	assert.Equal([]string{"true", ""}, rechecks)
}

// TestGitLabInventoryUsesAllStatesOldestFirstAndBoundedPages proves historical
// issue traversal enumerates every state oldest-first with a resumable keyset
// cursor.
func TestGitLabInventoryUsesAllStatesOldestFirstAndBoundedPages(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	requests := 0
	var issueKeysetCursors []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		assert.Equal("all", r.URL.Query().Get("state"))
		assert.Equal("asc", r.URL.Query().Get("sort"))
		assert.Equal("created_at", r.URL.Query().Get("order_by"))
		assert.Equal("100", r.URL.Query().Get("per_page"))
		switch r.URL.EscapedPath() {
		case "/api/v4/projects/42/issues":
			assert.Equal("keyset", r.URL.Query().Get("pagination"))
			issueKeysetCursors = append(issueKeysetCursors, r.URL.Query().Get("cursor"))
			if r.URL.Query().Get("cursor") == "" {
				w.Header().Set("Link", `<https://gitlab.example.com/api/v4/projects/42/issues?pagination=keyset&order_by=created_at&sort=asc&cursor=tie-break-1>; rel="next"`)
			}
			writeJSON(w, `[{"id":101,"iid":1,"title":"issue","state":"closed","created_at":"2025-01-01T00:00:00Z","updated_at":"2025-01-02T00:00:00Z"}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := newTestClient(t, server.URL)
	ref := gitLabPagesTestRef()
	historical := platform.ItemPageQuery{State: platform.ItemStateAll, Order: platform.ItemOrderCreated}

	issues, err := client.ListIssuesPage(t.Context(), ref, historical)
	require.NoError(err)
	require.Len(issues.Items, 1)
	assert.Equal(1, issues.Items[0].Number)
	assert.False(issues.Exhausted)
	assert.NotEmpty(issues.NextCursor)
	resumed := historical
	resumed.Cursor = issues.NextCursor
	issues2, err := client.ListIssuesPage(t.Context(), ref, resumed)
	require.NoError(err)
	assert.True(issues2.Exhausted)
	assert.Equal([]string{"", "tie-break-1"}, issueKeysetCursors,
		"resumption must replay the provider's keyset continuation parameters")

	assert.Equal(2, requests)
}

// TestGitLabUpdatedInventoryBindsCursorToQueryShape proves a maintenance cursor
// only resumes the exact enumeration that produced it: watermark, repository,
// and host all participate in the binding.
func TestGitLabUpdatedInventoryBindsCursorToQueryShape(t *testing.T) {
	require := require.New(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Link", `<https://gitlab.example.com/api/v4/projects/42/issues?pagination=keyset&cursor=tie-break-1>; rel="next"`)
		writeJSON(w, `[{"id":101,"iid":1,"title":"issue","state":"opened","created_at":"2025-01-01T00:00:00Z","updated_at":"2026-07-01T02:03:04Z"}]`)
	}))
	defer server.Close()
	client := newTestClient(t, server.URL)
	ref := gitLabPagesTestRef()
	watermark := time.Date(2026, 7, 1, 2, 3, 4, 0, time.UTC)
	query := func(since time.Time, cursor string) platform.ItemPageQuery {
		return platform.ItemPageQuery{
			State: platform.ItemStateAll, Order: platform.ItemOrderUpdated,
			UpdatedSince: &since, Cursor: cursor,
		}
	}

	issues, err := client.ListIssuesPage(t.Context(), ref, query(watermark, ""))
	require.NoError(err)
	require.NotEmpty(issues.NextCursor)

	_, err = client.ListIssuesPage(t.Context(), ref, query(watermark.Add(time.Second), issues.NextCursor))
	require.ErrorIs(err, platform.ErrProviderContract)
	otherRepo := ref
	otherRepo.Name = "other"
	otherRepo.RepoPath = "group/other"
	otherRepo.PlatformID = 43
	_, err = client.ListIssuesPage(t.Context(), otherRepo, query(watermark, issues.NextCursor))
	require.ErrorIs(err, platform.ErrProviderContract)
	// Ref hydration pins every read to the client's own host, so cross-host
	// replay is guarded by the minting client's identity: a client for a
	// different GitLab instance must refuse the cursor.
	otherHostClient, err := NewClient(
		"other.gitlab.example.com", testTokenSource("token"),
		WithBaseURLForTesting(server.URL+"/api/v4"), WithoutRetriesForTesting(),
	)
	require.NoError(err)
	otherHostRef := ref
	otherHostRef.Host = "other.gitlab.example.com"
	_, err = otherHostClient.ListIssuesPage(t.Context(), otherHostRef, query(watermark, issues.NextCursor))
	require.ErrorIs(err, platform.ErrProviderContract)
	_, err = client.ListIssuesPage(t.Context(), ref, query(watermark, issues.NextCursor))
	require.NoError(err)
}

// TestGitLabUpdatedMergeRequestsDoNotSkipItemsMovedBetweenPages proves the
// descending offset maintenance traversal keeps mid-scan updates inside the
// consumed prefix so offset pagination cannot permanently skip an unseen item.
func TestGitLabUpdatedMergeRequestsDoNotSkipItemsMovedBetweenPages(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	watermark := time.Date(2026, 7, 1, 2, 3, 4, 0, time.UTC)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		assert.Equal("/api/v4/projects/42/merge_requests", r.URL.EscapedPath())
		assert.Equal("desc", r.URL.Query().Get("sort"))
		if requests == 1 {
			w.Header().Set("X-Next-Page", "2")
			writeJSON(w, `[{"id":202,"iid":2,"title":"newest","state":"opened","created_at":"2025-01-01T00:00:00Z","updated_at":"2026-07-01T02:03:06Z"}]`)
			return
		}
		// Item 2 changed after page one was read. Descending pagination
		// keeps it in the consumed prefix, so item 1 remains on page two.
		writeJSON(w, `[{"id":201,"iid":1,"title":"unseen","state":"opened","created_at":"2025-01-01T00:00:00Z","updated_at":"2026-07-01T02:03:05Z"}]`)
	}))
	defer server.Close()
	client := newTestClient(t, server.URL)
	ref := gitLabPagesTestRef()
	query := func(cursor string) platform.ItemPageQuery {
		return platform.ItemPageQuery{
			State: platform.ItemStateAll, Order: platform.ItemOrderUpdated,
			UpdatedSince: &watermark, Cursor: cursor,
		}
	}

	first, err := client.ListMergeRequestsPage(t.Context(), ref, query(""))
	require.NoError(err)
	require.Len(first.Items, 1)
	assert.Equal(2, first.Items[0].Number)
	assert.False(first.Exhausted)
	second, err := client.ListMergeRequestsPage(t.Context(), ref, query(first.NextCursor))
	require.NoError(err)
	require.Len(second.Items, 1)
	assert.Equal(1, second.Items[0].Number)
	assert.True(second.Exhausted)
	assert.Equal(2, requests)
}

// TestGitLabUpdatedIssuesReserveMidScanMovesThroughKeysetCursor proves the
// ascending keyset maintenance traversal re-serves an item whose updated_at
// moved mid-scan: under a keyset cursor the bump only moves it forward past
// the cursor, so it reappears later in the same scan instead of being
// skipped.
func TestGitLabUpdatedIssuesReserveMidScanMovesThroughKeysetCursor(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	watermark := time.Date(2026, 7, 1, 2, 3, 4, 0, time.UTC)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		assert.Equal("/api/v4/projects/42/issues", r.URL.EscapedPath())
		assert.Equal("keyset", r.URL.Query().Get("pagination"))
		assert.Equal("asc", r.URL.Query().Get("sort"))
		if r.URL.Query().Get("cursor") == "" {
			w.Header().Set("Link", `<https://gitlab.example.com/api/v4/projects/42/issues?pagination=keyset&cursor=after-issue-1>; rel="next"`)
			writeJSON(w, `[{"id":201,"iid":1,"title":"oldest update","state":"opened","created_at":"2025-01-01T00:00:00Z","updated_at":"2026-07-01T02:03:05Z"}]`)
			return
		}
		// Issue 1 was updated again after page one was consumed: the keyset
		// cursor keeps traversal position, so the moved item is re-served on
		// a later page alongside the still-unseen issue 2.
		assert.Equal("after-issue-1", r.URL.Query().Get("cursor"))
		writeJSON(w, `[
			{"id":202,"iid":2,"title":"unseen","state":"opened","created_at":"2025-01-01T00:00:00Z","updated_at":"2026-07-01T02:03:06Z"},
			{"id":201,"iid":1,"title":"moved forward","state":"opened","created_at":"2025-01-01T00:00:00Z","updated_at":"2026-07-01T02:03:07Z"}
		]`)
	}))
	defer server.Close()
	client := newTestClient(t, server.URL)
	ref := gitLabPagesTestRef()
	query := func(cursor string) platform.ItemPageQuery {
		return platform.ItemPageQuery{
			State: platform.ItemStateAll, Order: platform.ItemOrderUpdated,
			UpdatedSince: &watermark, Cursor: cursor,
		}
	}

	first, err := client.ListIssuesPage(t.Context(), ref, query(""))
	require.NoError(err)
	require.Len(first.Items, 1)
	assert.Equal(1, first.Items[0].Number)
	assert.False(first.Exhausted)
	second, err := client.ListIssuesPage(t.Context(), ref, query(first.NextCursor))
	require.NoError(err)
	require.Len(second.Items, 2)
	assert.Equal(2, second.Items[0].Number)
	assert.Equal(1, second.Items[1].Number, "moved item is re-served, never skipped")
	assert.True(second.Exhausted)
	assert.Equal(2, requests)
}

func TestGitLabIssueKeysetCursorReplaysOnlyContinuationToken(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	var resumeQueries []url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("cursor") == "" {
			w.Header().Set("Link",
				`<https://evil.example.com/api/v4/projects/999/issues?cursor=tok-1&order_by=updated_at&sort=desc&per_page=1&state=opened&updated_after=2030-01-01T00:00:00Z&pagination=offset>; rel="next"`)
		} else {
			resumeQueries = append(resumeQueries, r.URL.Query())
		}
		writeJSON(w, `[{"id":101,"iid":1,"title":"issue","state":"closed","created_at":"2025-01-01T00:00:00Z","updated_at":"2025-01-02T00:00:00Z"}]`)
	}))
	defer server.Close()
	client := newTestClient(t, server.URL)
	ref := gitLabPagesTestRef()
	historical := platform.ItemPageQuery{State: platform.ItemStateAll, Order: platform.ItemOrderCreated}

	first, err := client.ListIssuesPage(t.Context(), ref, historical)
	require.NoError(err)
	require.NotEmpty(first.NextCursor)
	resumed := historical
	resumed.Cursor = first.NextCursor
	_, err = client.ListIssuesPage(t.Context(), ref, resumed)
	require.NoError(err)

	require.Len(resumeQueries, 1)
	query := resumeQueries[0]
	assert.Equal("tok-1", query.Get("cursor"), "the continuation token is replayed")
	assert.Equal("created_at", query.Get("order_by"), "order comes from the validated cursor, not the link")
	assert.Equal("asc", query.Get("sort"))
	assert.Equal("100", query.Get("per_page"))
	assert.Equal("all", query.Get("state"))
	assert.Equal("keyset", query.Get("pagination"))
	assert.Empty(query.Get("updated_after"), "a smuggled watermark must not survive")

	// A tampered token stays one opaque cursor parameter: it cannot be split
	// into extra query parameters that reshape the request.
	tampered, err := encodeGitLabPageCursor(gitLabPageCursor{
		Mode: "historical_issues", Host: ref.Host, RepoPath: "group/project",
		KeysetCursor: "evil&sort=desc",
	})
	require.NoError(err)
	resumed.Cursor = tampered
	_, err = client.ListIssuesPage(t.Context(), ref, resumed)
	require.NoError(err)
	require.Len(resumeQueries, 2)
	query = resumeQueries[1]
	assert.Equal("evil&sort=desc", query.Get("cursor"))
	assert.Equal("asc", query.Get("sort"))

	// A cursor carrying a full provider link (the previous cursor schema, or
	// a hand-crafted URL) does not carry keyset continuation state.
	legacy := base64.RawURLEncoding.EncodeToString([]byte(
		`{"mode":"historical_issues","host":"gitlab.example.com","repo_path":"group/project",` +
			`"link":"https://evil.example.com/api/v4/projects/999/issues?cursor=tok-1&sort=desc"}`,
	))
	resumed.Cursor = legacy
	_, err = client.ListIssuesPage(t.Context(), ref, resumed)
	require.ErrorIs(err, platform.ErrProviderContract)
}

// TestGitLabIssueKeysetUnsupportedServerReturnsTypedError proves a server
// that ignores the keyset request (GitLab before 18.3 for project issues)
// and answers with offset pagination is detected by its response shape and
// rejected with a typed unsupported_capability error instead of silently
// degrading to a skippable offset traversal. A single complete page is
// accepted: with no continuation needed, both pagination modes serve the
// identical full result.
func TestGitLabIssueKeysetUnsupportedServerReturnsTypedError(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	watermark := time.Date(2026, 7, 1, 2, 3, 4, 0, time.UTC)
	multiPage := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The oldest supported response shape: offset headers and a
		// page-numbered next link, no keyset cursor parameter.
		w.Header().Set("X-Page", "1")
		w.Header().Set("X-Per-Page", "100")
		if multiPage {
			w.Header().Set("X-Total", "250")
			w.Header().Set("X-Total-Pages", "3")
			w.Header().Set("X-Next-Page", "2")
			w.Header().Set("Link",
				`<https://gitlab.example.com/api/v4/projects/42/issues?page=2&per_page=100&order_by=created_at&sort=asc>; rel="next"`)
		} else {
			w.Header().Set("X-Total", "1")
			w.Header().Set("X-Total-Pages", "1")
		}
		writeJSON(w, `[{"id":101,"iid":1,"title":"issue","state":"closed","created_at":"2025-01-01T00:00:00Z","updated_at":"2026-07-01T02:03:04Z"}]`)
	}))
	defer server.Close()
	client := newTestClient(t, server.URL)
	ref := gitLabPagesTestRef()

	_, err := client.ListIssuesPage(t.Context(), ref, platform.ItemPageQuery{
		State: platform.ItemStateAll, Order: platform.ItemOrderCreated,
	})
	require.ErrorIs(err, platform.ErrUnsupportedCapability)
	_, err = client.ListIssuesPage(t.Context(), ref, platform.ItemPageQuery{
		State: platform.ItemStateAll, Order: platform.ItemOrderUpdated, UpdatedSince: &watermark,
	})
	require.ErrorIs(err, platform.ErrUnsupportedCapability,
		"the maintenance traversal shares the keyset requirement")

	multiPage = false
	single, err := client.ListIssuesPage(t.Context(), ref, platform.ItemPageQuery{
		State: platform.ItemStateAll, Order: platform.ItemOrderCreated,
	})
	require.NoError(err, "a complete single page needs no continuation and is accepted")
	assert.True(single.Exhausted)
	require.Len(single.Items, 1)
}

func TestGitLabPaginationChargesEveryMarkedPage(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		if requests == 1 {
			w.Header().Set("Link", `<https://gitlab.example.com/api/v4/projects/42/issues?pagination=keyset&cursor=next>; rel="next"`)
		}
		writeJSON(w, `[{"id":101,"iid":1,"title":"issue","state":"opened","created_at":"2025-01-01T00:00:00Z","updated_at":"2025-01-02T00:00:00Z"}]`)
	}))
	defer server.Close()
	budget := ghsync.NewSyncBudget(100)
	client := newTestClient(t, server.URL, WithSyncBudget(budget))
	ctx := ghsync.WithSyncBudget(t.Context())
	historical := platform.ItemPageQuery{State: platform.ItemStateAll, Order: platform.ItemOrderCreated}

	first, err := client.ListIssuesPage(ctx, gitLabPagesTestRef(), historical)
	require.NoError(err)
	resumed := historical
	resumed.Cursor = first.NextCursor
	_, err = client.ListIssuesPage(ctx, gitLabPagesTestRef(), resumed)
	require.NoError(err)
	assert.Equal(2, requests)
	assert.Equal(2, budget.Spent())
}

// TestGitLabListPagesRejectInvalidQueries proves the canonical entry points
// validate the query before spending any provider request.
func TestGitLabListPagesRejectInvalidQueries(t *testing.T) {
	watermark := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		writeJSON(w, `[]`)
	}))
	defer server.Close()
	client := newTestClient(t, server.URL)
	ref := gitLabPagesTestRef()

	tests := []struct {
		name  string
		query platform.ItemPageQuery
	}{
		{
			name:  "unknown order does not fall through to historical",
			query: platform.ItemPageQuery{State: platform.ItemStateAll, Order: platform.ItemOrder("priority")},
		},
		{
			name:  "unknown state",
			query: platform.ItemPageQuery{State: platform.ItemStateFilter("closed"), Order: platform.ItemOrderCreated},
		},
		{
			name:  "open scan rejects cursor",
			query: platform.ItemPageQuery{State: platform.ItemStateOpen, Order: platform.ItemOrderUpdated, Cursor: "c1"},
		},
		{
			name: "open scan rejects watermark",
			query: platform.ItemPageQuery{
				State: platform.ItemStateOpen, Order: platform.ItemOrderUpdated, UpdatedSince: &watermark,
			},
		},
		{
			name: "watermark requires updated order",
			query: platform.ItemPageQuery{
				State: platform.ItemStateAll, Order: platform.ItemOrderCreated, UpdatedSince: &watermark,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, issueErr := client.ListIssuesPage(t.Context(), ref, tt.query)
			require.ErrorIs(t, issueErr, platform.ErrInvalidArgument)
			_, mrErr := client.ListMergeRequestsPage(t.Context(), ref, tt.query)
			require.ErrorIs(t, mrErr, platform.ErrInvalidArgument)
			assert.Zero(t, requests)
		})
	}
}

// TestGitLabDetailPagesShareDiscussionReads proves one discussions fetch shape
// feeds both the ordinary-comment filter and the review-thread extraction,
// while submitted reviews stay a typed unsupported capability.
func TestGitLabDetailPagesShareDiscussionReads(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	recorder := &requestRecorder{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder.record(r)
		switch r.URL.EscapedPath() {
		case "/api/v4/projects/42/issues/7/discussions":
			writeJSON(w, `[{"id":"issue-thread","notes":[{"id":301,"body":"issue comment","author":{"username":"ivy"},"created_at":"2026-07-01T00:00:00Z"}]}]`)
		case "/api/v4/projects/42/merge_requests/8/discussions":
			writeJSON(w, `[
				{"id":"ordinary","notes":[{"id":401,"body":"ordinary comment","author":{"username":"omar"},"created_at":"2026-07-02T00:00:00Z"}]},
				{"id":"system","notes":[{"id":404,"body":"assigned to @omar","system":true,"author":{"username":"omar"},"created_at":"2026-07-02T01:00:00Z"}]},
				{"id":"inline","notes":[
					{"id":402,"body":"inline comment","author":{"username":"rhea"},"created_at":"2026-07-03T00:00:00Z","updated_at":"2026-07-03T01:00:00Z","position":{"base_sha":"base","start_sha":"start","head_sha":"head","new_path":"main.go","new_line":9}},
					{"id":403,"body":"inline reply","author":{"username":"sam"},"created_at":"2026-07-03T02:00:00Z","updated_at":"2026-07-03T02:00:00Z"}
				]}
			]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := newTestClient(t, server.URL)
	ref := gitLabPagesTestRef()

	issueComments, err := client.ListIssueCommentsPage(t.Context(), ref, 7, "")
	require.NoError(err)
	recorder.take()
	require.Len(issueComments.Items, 1)
	assert.Equal("issue comment", issueComments.Items[0].Body)
	assert.True(issueComments.Exhausted)

	mrComments, err := client.ListMergeRequestCommentsPage(t.Context(), ref, 8, "")
	require.NoError(err)
	mrCommentsReqs := recorder.take()
	require.Len(mrComments.Items, 1)
	assert.Equal("ordinary comment", mrComments.Items[0].Body)
	assert.Equal("issue_comment", mrComments.Items[0].EventType)

	threads, err := client.ListReviewThreadsPage(t.Context(), ref, 8, "")
	require.NoError(err)
	threadsReqs := recorder.take()
	require.Len(threads.Items, 2)
	assert.Equal([]string{"402", "403"}, []string{threads.Items[0].ProviderCommentID, threads.Items[1].ProviderCommentID})
	assert.Equal("inline", threads.Items[1].ProviderThreadID)
	assert.Equal("main.go", threads.Items[1].Range.Path)
	assert.Equal(9, threads.Items[1].Range.Line)

	// The comments page and the thread page each consume one fetch of the same
	// discussions endpoint with identical request shapes.
	require.Len(mrCommentsReqs, 1)
	assert.Equal(mrCommentsReqs, threadsReqs)

	_, err = client.ListSubmittedReviewsPage(t.Context(), ref, 8, "")
	require.ErrorIs(err, platform.ErrUnsupportedCapability)
}

func TestGitLabFilteredDiscussionPageDeclaresProgress(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Next-Page", "2")
		writeJSON(w, `[{"id":"ordinary","notes":[{"id":401,"body":"ordinary","author":{"username":"omar"},"created_at":"2026-07-02T00:00:00Z"}]}]`)
	}))
	defer server.Close()
	client := newTestClient(t, server.URL)

	page, err := client.ListReviewThreadsPage(t.Context(), gitLabPagesTestRef(), 8, "")
	require.NoError(err)
	assert.Empty(page.Items)
	assert.True(page.ProgressOnly)
	assert.NotEmpty(page.NextCursor)
	assert.NoError(platform.ValidatePage(platform.KindGitLab, "gitlab.example.com", "", page))
}

// TestGitLabLiveEventsKeepSystemNotesWhileCanonicalCommentsFilterThem proves
// the live discussion-backed surfaces preserve system-note events and
// non-positional replies inside inline threads (paging through the shared
// discussions fetcher), while the canonical comments page keeps the ordinary
// filter the archive datasets require.
func TestGitLabLiveEventsKeepSystemNotesWhileCanonicalCommentsFilterThem(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	var discussionPages, commitPages []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.EscapedPath() {
		case "/api/v4/projects/42/merge_requests/8/discussions":
			discussionPages = append(discussionPages, r.URL.Query().Get("page"))
			if r.URL.Query().Get("page") == "1" {
				w.Header().Set("X-Next-Page", "2")
				writeJSON(w, `[
					{"id":"ordinary","notes":[{"id":401,"body":"ordinary comment","author":{"username":"omar"},"created_at":"2026-07-02T00:00:00Z"}]},
					{"id":"system","notes":[{"id":404,"body":"merged","system":true,"author":{"username":"maintainer"},"created_at":"2026-07-02T01:00:00Z"}]}
				]`)
				return
			}
			writeJSON(w, `[
				{"id":"inline","notes":[
					{"id":402,"body":"inline comment","author":{"username":"rhea"},"created_at":"2026-07-03T00:00:00Z","position":{"base_sha":"base","start_sha":"start","head_sha":"head","new_path":"main.go","new_line":9}},
					{"id":403,"body":"discussion reply","author":{"username":"sam"},"created_at":"2026-07-03T02:00:00Z"}
				]}
			]`)
		case "/api/v4/projects/42/merge_requests/8/commits":
			commitPages = append(commitPages, r.URL.Query().Get("page"))
			writeJSON(w, `[{"id":"abcdef123456","title":"commit title","message":"commit body","author_name":"Alice","created_at":"2026-07-01T09:00:00Z"}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := newTestClient(t, server.URL)
	ref := gitLabPagesTestRef()

	events, err := client.ListMergeRequestEvents(t.Context(), ref, 8)
	require.NoError(err)
	require.Len(events, 4)
	assert.Equal("issue_comment", events[0].EventType)
	assert.Equal("ordinary comment", events[0].Body)
	assert.Equal("merged", events[1].EventType)
	assert.Equal("maintainer", events[1].Author)
	assert.Equal("issue_comment", events[2].EventType)
	assert.Equal("discussion reply", events[2].Body)
	assert.Equal("inline", events[2].ThreadID)
	assert.Equal("commit", events[3].EventType)
	assert.Equal([]string{"1", "2"}, discussionPages)
	assert.Equal([]string{"1"}, commitPages)

	discussionPages = nil
	comments, err := client.ListMergeRequestComments(t.Context(), ref, 8)
	require.NoError(err)
	assert.Equal(events[:3], comments)
	assert.Equal([]string{"1", "2"}, discussionPages)

	discussionPages = nil
	canonical, err := platform.CollectPages(t.Context(), "", func(
		ctx context.Context, cursor string,
	) (platform.Page[platform.MergeRequestEvent], error) {
		return client.ListMergeRequestCommentsPage(ctx, ref, 8, cursor)
	})
	require.NoError(err)
	require.Len(canonical, 1)
	assert.Equal("ordinary comment", canonical[0].Body)
	assert.Equal([]string{"1", "2"}, discussionPages)
}

// TestGitLabLiveIssueEventsCollectCanonicalCommentPages proves the live issue
// event surfaces drain the canonical issue comments page method.
func TestGitLabLiveIssueEventsCollectCanonicalCommentPages(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	var pages []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal("/api/v4/projects/42/issues/7/discussions", r.URL.EscapedPath())
		pages = append(pages, r.URL.Query().Get("page"))
		if r.URL.Query().Get("page") == "1" {
			w.Header().Set("X-Next-Page", "2")
			writeJSON(w, `[{"id":"first","notes":[{"id":301,"body":"first comment","author":{"username":"ivy"},"created_at":"2026-07-01T00:00:00Z"}]}]`)
			return
		}
		writeJSON(w, `[{"id":"second","notes":[{"id":302,"body":"second comment","author":{"username":"joe"},"created_at":"2026-07-02T00:00:00Z"}]}]`)
	}))
	defer server.Close()
	client := newTestClient(t, server.URL)
	ref := gitLabPagesTestRef()

	events, err := client.ListIssueEvents(t.Context(), ref, 7)
	require.NoError(err)
	require.Len(events, 2)
	assert.Equal("first comment", events[0].Body)
	assert.Equal("second comment", events[1].Body)
	assert.Equal([]string{"1", "2"}, pages)

	pages = nil
	comments, err := client.ListIssueComments(t.Context(), ref, 7)
	require.NoError(err)
	assert.Equal(events, comments)
	assert.Equal([]string{"1", "2"}, pages)
}

// TestGitLabLookupClassifiesRemovalAndAccessLoss proves the canonical lookup
// records every outcome while live gets require present, with GitLab's confidential-content ambiguity
// mapped to inaccessible rather than removed.
func TestGitLabLookupClassifiesRemovalAndAccessLoss(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.EscapedPath() {
		case "/api/v4/projects/42/issues/7":
			http.NotFound(w, r)
		case "/api/v4/projects/42/issues/8":
			http.Error(w, "forbidden", http.StatusForbidden)
		case "/api/v4/projects/42/merge_requests/9":
			writeJSON(w, `{"id":209,"iid":9,"title":"present","state":"opened","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-02T00:00:00Z"}`)
		case "/api/v4/projects/42":
			writeJSON(w, `{"id":42,"path":"project","path_with_namespace":"group/project","name":"Project"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := newTestClient(t, server.URL)
	ref := gitLabPagesTestRef()

	// GitLab can return an item 404 for confidential content even while the
	// source project is readable, so the lookup must retain cached content.
	confidential, err := client.LookupIssue(t.Context(), ref, 7)
	require.NoError(err)
	assert.Equal(platform.LookupInaccessible, confidential.Outcome)

	inaccessible, err := client.LookupIssue(t.Context(), ref, 8)
	require.NoError(err)
	assert.Equal(platform.LookupInaccessible, inaccessible.Outcome)

	present, err := client.LookupMergeRequest(t.Context(), ref, 9)
	require.NoError(err)
	assert.Equal(platform.LookupPresent, present.Outcome)
	assert.Equal(9, present.Item.Number)

	live, err := platform.RequireMergeRequest(t.Context(), client, ref, 9)
	require.NoError(err)
	assert.Equal(present.Item, live)

	// Live gets require present: a non-present outcome surfaces the typed
	// permission_denied error the raw transport produced before lookup
	// classification existed.
	_, liveErr := platform.RequireIssue(t.Context(), client, ref, 7)
	require.ErrorIs(liveErr, platform.ErrPermissionDenied)
	_, liveErr = platform.RequireIssue(t.Context(), client, ref, 8)
	require.ErrorIs(liveErr, platform.ErrPermissionDenied)
}

func TestGitLabLookupRetainsItemWhenRepositoryIsInaccessible(t *testing.T) {
	require := require.New(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer server.Close()
	client := newTestClient(t, server.URL)
	_, err := client.LookupIssue(t.Context(), gitLabPagesTestRef(), 7)
	require.ErrorIs(err, platform.ErrPermissionDenied)
	_, err = platform.RequireIssue(t.Context(), client, gitLabPagesTestRef(), 7)
	require.ErrorIs(err, platform.ErrPermissionDenied)
}

func TestGitLabArchiveCapabilities(t *testing.T) {
	assert.Equal(t, platform.ArchiveCapabilities{
		HistoricalIssues: true,
		OrdinaryComments: true, InlineReviewComments: true,
	}, newTestClient(t, "http://127.0.0.1:1").Capabilities().Archive)
}

// TestGitLabCanonicalReadersHydratePathOnlyRefs proves the canonical readers
// accept path-only repository refs the way the pre-canonical live paths did:
// the ref is resolved once through the project lookup, and returned items
// carry the hydrated identity (platform id, owner, name, web URL) with local
// merge requests keeping the project clone URL.
func TestGitLabCanonicalReadersHydratePathOnlyRefs(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.EscapedPath())
		switch r.URL.EscapedPath() {
		case "/api/v4/projects/group%2Fproject":
			writeJSON(w, `{
				"id": 42,
				"path": "project",
				"path_with_namespace": "group/project",
				"name": "Project",
				"default_branch": "main",
				"web_url": "https://gitlab.example.com/group/project",
				"http_url_to_repo": "https://gitlab.example.com/group/project.git"
			}`)
		case "/api/v4/projects/42/merge_requests":
			writeJSON(w, `[{"id":1001,"iid":7,"project_id":42,"source_project_id":42,"target_project_id":42,"title":"local","state":"opened","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-02T00:00:00Z"}]`)
		case "/api/v4/projects/42/issues/5/discussions":
			writeJSON(w, `[{"id":"thread","notes":[{"id":301,"body":"comment","author":{"username":"ivy"},"created_at":"2026-07-01T00:00:00Z"}]}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := newTestClient(t, server.URL)
	pathOnly := platform.RepoRef{
		Platform: platform.KindGitLab, Host: "gitlab.example.com", RepoPath: "group/project",
	}

	mrs, err := platform.ListOpenMergeRequests(t.Context(), client, pathOnly)
	require.NoError(err)
	require.Len(mrs, 1)
	assert.Equal(int64(42), mrs[0].Repo.PlatformID)
	assert.Equal("group", mrs[0].Repo.Owner)
	assert.Equal("project", mrs[0].Repo.Name)
	assert.Equal("https://gitlab.example.com/group/project.git", mrs[0].HeadRepoCloneURL,
		"a local merge request keeps the hydrated project clone URL")

	comments, err := client.ListIssueCommentsPage(t.Context(), pathOnly, 5, "")
	require.NoError(err)
	require.Len(comments.Items, 1)
	assert.Equal(int64(42), comments.Items[0].Repo.PlatformID)
	assert.Equal("https://gitlab.example.com/group/project/-/issues/5#note_301",
		comments.Items[0].DirectURL,
		"detail events resolve their direct URL from the hydrated web URL")

	assert.Equal([]string{
		"/api/v4/projects/group%2Fproject",
		"/api/v4/projects/42/merge_requests",
		"/api/v4/projects/group%2Fproject",
		"/api/v4/projects/42/issues/5/discussions",
	}, paths, "each canonical read hydrates the path-only ref once, then uses the numeric id")
}

// TestGitLabLookupEnrichesForkHeadCloneURLOnce proves a present fork item
// carries the head clone-URL enrichment for the canonical and live readers, and the enrichment
// request is cached per source project so repeated lookups spend it once.
func TestGitLabLookupEnrichesForkHeadCloneURLOnce(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	sourceProjectRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.EscapedPath() {
		case "/api/v4/projects/42/merge_requests/7":
			writeJSON(w, `{
				"id": 1001,
				"iid": 7,
				"project_id": 42,
				"source_project_id": 77,
				"target_project_id": 42,
				"title": "fork MR",
				"state": "opened",
				"created_at": "2026-01-01T00:00:00Z",
				"updated_at": "2026-01-02T00:00:00Z"
			}`)
		case "/api/v4/projects/77":
			sourceProjectRequests++
			writeJSON(w, `{"id":77,"path_with_namespace":"fork/project","http_url_to_repo":"https://gitlab.example.com/fork/project.git"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := newTestClient(t, server.URL)
	ref := gitLabPagesTestRef()

	canonical, err := client.LookupMergeRequest(t.Context(), ref, 7)
	require.NoError(err)
	assert.Equal(platform.LookupPresent, canonical.Outcome)
	assert.Equal("https://gitlab.example.com/fork/project.git", canonical.Item.HeadRepoCloneURL)
	assert.False(canonical.Item.HeadRepoCloneURLUnknown,
		"a successful enrichment is authoritative")
	assert.Equal(1, sourceProjectRequests)

	live, err := platform.RequireMergeRequest(t.Context(), client, ref, 7)
	require.NoError(err)
	assert.Equal("https://gitlab.example.com/fork/project.git", live.HeadRepoCloneURL)
	assert.Equal(1, sourceProjectRequests,
		"the fork head enrichment request is cached per source project")
}

// TestGitLabLookupMergeRequestEnrichmentFailureDegradesToPresent proves the
// fork head clone-URL enrichment inside the canonical lookup is best-effort:
// a transient source-project failure degrades the result to an empty
// HeadRepoCloneURL and never fails the lookup or the live require path.
func TestGitLabLookupMergeRequestEnrichmentFailureDegradesToPresent(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	sourceProjectRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.EscapedPath() {
		case "/api/v4/projects/42/merge_requests/7":
			writeJSON(w, `{
				"id": 1001,
				"iid": 7,
				"project_id": 42,
				"source_project_id": 77,
				"target_project_id": 42,
				"title": "fork MR",
				"state": "opened",
				"created_at": "2026-01-01T00:00:00Z",
				"updated_at": "2026-01-02T00:00:00Z"
			}`)
		case "/api/v4/projects/77":
			sourceProjectRequests++
			http.Error(w, `{"message":"internal error"}`, http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := newTestClient(t, server.URL)
	ref := gitLabPagesTestRef()

	lookup, err := client.LookupMergeRequest(t.Context(), ref, 7)
	require.NoError(err, "an enrichment failure must not fail the lookup")
	assert.Equal(platform.LookupPresent, lookup.Outcome)
	assert.Empty(lookup.Item.HeadRepoCloneURL,
		"a failed enrichment degrades to an empty head clone URL")
	assert.True(lookup.Item.HeadRepoCloneURLUnknown,
		"a failed enrichment marks the clone URL unknown so persistence preserves a previously stored value")
	assert.Equal("fork MR", lookup.Item.Title)

	live, err := platform.RequireMergeRequest(t.Context(), client, ref, 7)
	require.NoError(err)
	assert.Empty(live.HeadRepoCloneURL)
	assert.Equal(2, sourceProjectRequests,
		"failed enrichment attempts are not cached and are retried per lookup")
}
