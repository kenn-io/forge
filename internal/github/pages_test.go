package github

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/middleman/internal/platform"
)

func pagesTestRef() platform.RepoRef {
	return platform.RepoRef{
		Platform: platform.KindGitHub, Host: "github.com",
		Owner: "acme", Name: "widget", RepoPath: "acme/widget",
	}
}

// requestRecorder captures the method, path, and raw query of every request a
// test server receives so parity tests can assert the canonical method and the
// legacy delegate issued identical requests.
type requestRecorder struct {
	mu    sync.Mutex
	lines []string
}

func (r *requestRecorder) record(req *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = append(r.lines, req.Method+" "+req.URL.Path+"?"+req.URL.RawQuery)
}

func (r *requestRecorder) take() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.lines
	r.lines = nil
	return out
}

// TestGitHubListIssuesPageDispatchesByQuery proves one method owns all three
// issue inventory request shapes: StateOpen hits the REST issue list scoped to
// open, StateAll ascending-by-created runs the GraphQL historical scan, and
// StateAll ordered-by-updated runs the GraphQL maintenance scan with the
// inclusive watermark.
func TestGitHubListIssuesPageDispatchesByQuery(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	watermark := time.Date(2026, 7, 1, 2, 3, 4, 0, time.UTC)

	var graphqlOrder []string
	var graphqlSince []any
	openState := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/graphql":
			var request struct {
				Variables map[string]any `json:"variables"`
			}
			assert.NoError(json.NewDecoder(r.Body).Decode(&request))
			graphqlOrder = append(graphqlOrder, fmt.Sprint(request.Variables["orderField"]))
			graphqlSince = append(graphqlSince, request.Variables["since"])
			_, _ = w.Write([]byte(`{"data":{"repository":{"issues":{"nodes":[{"id":"I_1","databaseId":1,"number":1,"title":"issue","state":"CLOSED","body":"","url":"https://github.com/acme/widget/issues/1","author":{"login":"a"},"createdAt":"2025-01-01T00:00:00Z","updatedAt":"2025-01-02T00:00:00Z","comments":{"totalCount":0},"labels":{"nodes":[]},"assignees":{"nodes":[]}}],"pageInfo":{"hasNextPage":false,"endCursor":null}}}}}`))
		case "/api/v3/repos/acme/widget/issues":
			openState = r.URL.Query().Get("state")
			_, _ = w.Write([]byte(`[{"id":9,"node_id":"I_9","number":9,"title":"open issue","state":"open","html_url":"https://github.com/acme/widget/issues/9","user":{"login":"a"},"created_at":"2025-01-01T00:00:00Z","updated_at":"2025-01-02T00:00:00Z"}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	provider := newArchiveTestGitHubProvider(t, srv.URL)
	ref := pagesTestRef()

	open, err := provider.ListIssuesPage(t.Context(), ref, platform.ItemPageQuery{
		State: platform.ItemStateOpen, Order: platform.ItemOrderUpdated,
	})
	require.NoError(err)
	require.Len(open.Items, 1)
	assert.Equal(9, open.Items[0].Number)
	assert.True(open.Exhausted)
	assert.Equal("open", openState)

	historical, err := provider.ListIssuesPage(t.Context(), ref, platform.ItemPageQuery{
		State: platform.ItemStateAll, Order: platform.ItemOrderCreated,
	})
	require.NoError(err)
	require.Len(historical.Items, 1)
	assert.Equal(1, historical.Items[0].Number)

	updated, err := provider.ListIssuesPage(t.Context(), ref, platform.ItemPageQuery{
		State: platform.ItemStateAll, Order: platform.ItemOrderUpdated, UpdatedSince: &watermark,
	})
	require.NoError(err)
	require.Len(updated.Items, 1)

	assert.Equal([]string{"CREATED_AT", "UPDATED_AT"}, graphqlOrder)
	require.Len(graphqlSince, 2)
	assert.Nil(graphqlSince[0])
	assert.Equal(watermark.Add(-time.Second).Format(time.RFC3339Nano), graphqlSince[1])
}

// TestGitHubListMergeRequestsPageDispatchesByQuery proves one method owns all
// three merge-request inventory request shapes and records the provider-side
// sort fences: open ignores sort, historical is created-ascending, and
// maintenance is updated-descending with a watermark stop (the ItemOrderUpdated
// contract leaves the direction provider-chosen; GitHub's pulls API has no
// since filter, so descending is the only shape that avoids re-paging the
// whole repository every maintenance pass).
func TestGitHubListMergeRequestsPageDispatchesByQuery(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	watermark := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	var states, sorts, directions []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal("/api/v3/repos/acme/widget/pulls", r.URL.Path)
		states = append(states, r.URL.Query().Get("state"))
		sorts = append(sorts, r.URL.Query().Get("sort"))
		directions = append(directions, r.URL.Query().Get("direction"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":201,"node_id":"PR_201","number":4,"title":"pull","state":"closed","html_url":"https://github.com/acme/widget/pull/4","user":{"login":"a"},"created_at":"2025-01-01T00:00:00Z","updated_at":"2026-07-01T00:00:00Z"}]`))
	}))
	defer srv.Close()
	provider := newArchiveTestGitHubProvider(t, srv.URL)
	ref := pagesTestRef()

	_, err := provider.ListMergeRequestsPage(t.Context(), ref, platform.ItemPageQuery{
		State: platform.ItemStateOpen, Order: platform.ItemOrderUpdated,
	})
	require.NoError(err)
	_, err = provider.ListMergeRequestsPage(t.Context(), ref, platform.ItemPageQuery{
		State: platform.ItemStateAll, Order: platform.ItemOrderCreated,
	})
	require.NoError(err)
	_, err = provider.ListMergeRequestsPage(t.Context(), ref, platform.ItemPageQuery{
		State: platform.ItemStateAll, Order: platform.ItemOrderUpdated, UpdatedSince: &watermark,
	})
	require.NoError(err)

	assert.Equal([]string{"open", "all", "all"}, states)
	assert.Equal([]string{"", "created", "updated"}, sorts)
	assert.Equal([]string{"", "asc", "desc"}, directions)
}

// TestGitHubLookupIssueDrivesLiveGet proves the canonical lookup records every
// outcome while the live collector requires a present item.
func TestGitHubLookupIssueDrivesLiveGet(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v3/repos/acme/widget/issues/7":
			_, _ = w.Write([]byte(`{"id":7,"node_id":"I_7","number":7,"repository_url":"https://api.github.com/repos/acme/widget","title":"present","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-02T00:00:00Z"}`))
		case "/api/v3/repos/acme/widget/issues/9":
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
		case "/api/v3/repos/acme/widget":
			_, _ = w.Write([]byte(`{"id":1,"name":"widget","owner":{"login":"acme"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	provider := newArchiveTestGitHubProvider(t, srv.URL)
	ref := pagesTestRef()

	canonical, err := provider.LookupIssue(t.Context(), ref, 7)
	require.NoError(err)
	assert.Equal(platform.LookupPresent, canonical.Outcome)

	live, err := platform.RequireIssue(t.Context(), provider, ref, 7)
	require.NoError(err)
	assert.Equal(canonical.Item, live)

	removed, err := provider.LookupIssue(t.Context(), ref, 9)
	require.NoError(err)
	assert.Equal(platform.LookupRemoved, removed.Outcome)
	_, liveErr := platform.RequireIssue(t.Context(), provider, ref, 9)
	require.ErrorIs(liveErr, platform.ErrNotFound)
}

func TestGitHubLookupMergeRequestDrivesLiveGet(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v3/repos/acme/widget/pulls/7":
			_, _ = w.Write([]byte(`{"id":7,"number":7,"title":"present","state":"open","base":{"repo":{"url":"https://api.github.com/repos/acme/widget"}},"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-02T00:00:00Z"}`))
		case "/api/v3/repos/acme/widget/pulls/9":
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
		case "/api/v3/repos/acme/widget":
			_, _ = w.Write([]byte(`{"id":1,"name":"widget","owner":{"login":"acme"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	provider := newArchiveTestGitHubProvider(t, srv.URL)
	ref := pagesTestRef()

	canonical, err := provider.LookupMergeRequest(t.Context(), ref, 7)
	require.NoError(err)

	live, err := platform.RequireMergeRequest(t.Context(), provider, ref, 7)
	require.NoError(err)
	assert.Equal(canonical.Item, live)

	_, liveErr := platform.RequireMergeRequest(t.Context(), provider, ref, 9)
	require.ErrorIs(liveErr, platform.ErrNotFound)
}

// TestGitHubListPagesRejectInvalidQueries proves the canonical entry points
// validate the query before spending any provider request: unknown states and
// orders are typed invalid_argument errors instead of silently dispatching a
// historical traversal, and open scans reject resumption state.
func TestGitHubListPagesRejectInvalidQueries(t *testing.T) {
	watermark := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()
	provider := newArchiveTestGitHubProvider(t, srv.URL)
	ref := pagesTestRef()

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
			_, issueErr := provider.ListIssuesPage(t.Context(), ref, tt.query)
			require.ErrorIs(t, issueErr, platform.ErrInvalidArgument)
			_, mrErr := provider.ListMergeRequestsPage(t.Context(), ref, tt.query)
			require.ErrorIs(t, mrErr, platform.ErrInvalidArgument)
			assert.Zero(t, requests)
		})
	}
}

// TestGitHubLiveGetMapsLookupOutcomes proves live require-present semantics
// distinguish the non-present lookup outcomes: removed is not_found,
// inaccessible is permission_denied (the pre-lookup transport behavior for a
// 403), and moved is not_found carrying the destination repository so callers
// can retarget the reference.
func TestGitHubLiveGetMapsLookupOutcomes(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v3/repos/acme/widget/issues/9", "/api/v3/repos/acme/widget/pulls/9":
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
		case "/api/v3/repos/acme/widget/issues/8", "/api/v3/repos/acme/widget/pulls/8":
			http.Error(w, `{"message":"Forbidden"}`, http.StatusForbidden)
		case "/api/v3/repos/acme/widget/issues/10":
			_, _ = w.Write([]byte(`{"id":10,"node_id":"I_10","number":10,"repository_url":"https://api.github.com/repos/other/place","title":"moved","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-02T00:00:00Z"}`))
		case "/api/v3/repos/acme/widget/pulls/10":
			_, _ = w.Write([]byte(`{"id":10,"number":10,"title":"moved","state":"open","base":{"repo":{"url":"https://api.github.com/repos/other/place"}},"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-02T00:00:00Z"}`))
		case "/api/v3/repos/acme/widget":
			_, _ = w.Write([]byte(`{"id":1,"name":"widget","owner":{"login":"acme"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	provider := newArchiveTestGitHubProvider(t, srv.URL)
	ref := pagesTestRef()

	_, removedIssueErr := platform.RequireIssue(t.Context(), provider, ref, 9)
	require.ErrorIs(removedIssueErr, platform.ErrNotFound)
	_, removedMRErr := platform.RequireMergeRequest(t.Context(), provider, ref, 9)
	require.ErrorIs(removedMRErr, platform.ErrNotFound)

	_, inaccessibleIssueErr := platform.RequireIssue(t.Context(), provider, ref, 8)
	require.ErrorIs(inaccessibleIssueErr, platform.ErrPermissionDenied)
	_, inaccessibleMRErr := platform.RequireMergeRequest(t.Context(), provider, ref, 8)
	require.ErrorIs(inaccessibleMRErr, platform.ErrPermissionDenied)

	_, movedIssueErr := platform.RequireIssue(t.Context(), provider, ref, 10)
	require.ErrorIs(movedIssueErr, platform.ErrNotFound)
	var typedIssueErr *platform.Error
	require.ErrorAs(movedIssueErr, &typedIssueErr)
	require.NotNil(typedIssueErr.Destination)
	assert.Equal("other", typedIssueErr.Destination.Owner)
	assert.Equal("place", typedIssueErr.Destination.Name)

	_, movedMRErr := platform.RequireMergeRequest(t.Context(), provider, ref, 10)
	require.ErrorIs(movedMRErr, platform.ErrNotFound)
	var typedMRErr *platform.Error
	require.ErrorAs(movedMRErr, &typedMRErr)
	require.NotNil(typedMRErr.Destination)
	assert.Equal("other", typedMRErr.Destination.Owner)
	assert.Equal("place", typedMRErr.Destination.Name)

	var typedRemovedErr *platform.Error
	require.ErrorAs(removedIssueErr, &typedRemovedErr)
	assert.Nil(typedRemovedErr.Destination)
}

// TestGitHubUpdatedMergeRequestsAcrossPages proves the canonical maintenance
// query keeps records updated exactly at the inclusive
// watermark, and stop once the descending traversal crosses the overlapped
// watermark.
func TestGitHubUpdatedMergeRequestsAcrossPages(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	watermark := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	recorder := &requestRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder.record(r)
		assert.Equal("/api/v3/repos/acme/widget/pulls", r.URL.Path)
		assert.Equal("all", r.URL.Query().Get("state"))
		assert.Equal("updated", r.URL.Query().Get("sort"))
		assert.Equal("desc", r.URL.Query().Get("direction"))
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Query().Get("page") {
		case "1":
			w.Header().Set("Link", fmt.Sprintf(`<%s%s?page=2>; rel="next"`, srvURL(r), r.URL.Path))
			_, _ = w.Write([]byte(`[
				{"id":201,"node_id":"PR_201","number":41,"title":"newest","state":"closed","html_url":"https://github.com/acme/widget/pull/41","user":{"login":"a"},"created_at":"2025-01-01T00:00:00Z","updated_at":"2026-07-02T00:00:00Z"},
				{"id":202,"node_id":"PR_202","number":42,"title":"newer","state":"open","html_url":"https://github.com/acme/widget/pull/42","user":{"login":"a"},"created_at":"2025-01-02T00:00:00Z","updated_at":"2026-07-01T00:00:00Z"}
			]`))
		case "2":
			w.Header().Set("Link", fmt.Sprintf(`<%s%s?page=3>; rel="next"`, srvURL(r), r.URL.Path))
			_, _ = w.Write([]byte(`[
				{"id":203,"node_id":"PR_203","number":43,"title":"at watermark","state":"closed","html_url":"https://github.com/acme/widget/pull/43","user":{"login":"a"},"created_at":"2025-01-03T00:00:00Z","updated_at":"2026-06-01T00:00:00Z"},
				{"id":204,"node_id":"PR_204","number":44,"title":"before watermark","state":"closed","html_url":"https://github.com/acme/widget/pull/44","user":{"login":"a"},"created_at":"2025-01-04T00:00:00Z","updated_at":"2026-05-01T00:00:00Z"}
			]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	provider := newArchiveTestGitHubProvider(t, srv.URL)
	ref := pagesTestRef()

	canonicalFirst, err := provider.ListMergeRequestsPage(t.Context(), ref, platform.ItemPageQuery{
		State: platform.ItemStateAll, Order: platform.ItemOrderUpdated, UpdatedSince: &watermark,
	})
	require.NoError(err)
	require.NotEmpty(canonicalFirst.NextCursor)
	canonicalSecond, err := provider.ListMergeRequestsPage(t.Context(), ref, platform.ItemPageQuery{
		State: platform.ItemStateAll, Order: platform.ItemOrderUpdated,
		UpdatedSince: &watermark, Cursor: canonicalFirst.NextCursor,
	})
	require.NoError(err)
	canonicalReqs := recorder.take()

	require.Len(canonicalFirst.Items, 2)
	assert.Equal([]int{41, 42}, []int{canonicalFirst.Items[0].Number, canonicalFirst.Items[1].Number})
	assert.False(canonicalFirst.Exhausted)
	// The record updated exactly at the watermark is inside the inclusive
	// boundary; the one before the overlapped watermark ends the scan.
	require.Len(canonicalSecond.Items, 1)
	assert.Equal(43, canonicalSecond.Items[0].Number)
	assert.True(canonicalSecond.Exhausted)
	assert.Empty(canonicalSecond.NextCursor)
	assert.Equal([]string{
		"GET /api/v3/repos/acme/widget/pulls?direction=desc&page=1&per_page=100&sort=updated&state=all",
		"GET /api/v3/repos/acme/widget/pulls?direction=desc&page=2&per_page=100&sort=updated&state=all",
	}, canonicalReqs)
}
