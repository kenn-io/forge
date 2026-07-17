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

// TestGitHubIssueInventoryLegacyMatchesCanonical proves the ArchiveReader and
// live delegates issue the same requests and produce the same normalized rows
// as the canonical ListIssuesPage for every query shape.
func TestGitHubIssueInventoryLegacyMatchesCanonical(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	watermark := time.Date(2026, 7, 1, 2, 3, 4, 0, time.UTC)
	recorder := &requestRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder.record(r)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/graphql":
			_, _ = w.Write([]byte(`{"data":{"repository":{"issues":{"nodes":[{"id":"I_1","databaseId":1,"number":1,"title":"issue","state":"CLOSED","body":"","url":"https://github.com/acme/widget/issues/1","author":{"login":"a"},"createdAt":"2025-01-01T00:00:00Z","updatedAt":"2025-01-02T00:00:00Z","comments":{"totalCount":0},"labels":{"nodes":[]},"assignees":{"nodes":[]}}],"pageInfo":{"hasNextPage":false,"endCursor":null}}}}}`))
		case "/api/v3/repos/acme/widget/issues":
			_, _ = w.Write([]byte(`[{"id":9,"node_id":"I_9","number":9,"title":"open issue","state":"open","html_url":"https://github.com/acme/widget/issues/9","user":{"login":"a"},"created_at":"2025-01-01T00:00:00Z","updated_at":"2025-01-02T00:00:00Z"}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	provider := newArchiveTestGitHubProvider(t, srv.URL)
	ref := pagesTestRef()

	canonicalHistorical, err := provider.ListIssuesPage(t.Context(), ref, platform.ItemPageQuery{
		State: platform.ItemStateAll, Order: platform.ItemOrderCreated,
	})
	require.NoError(err)
	canonicalHistoricalReqs := recorder.take()
	legacyHistorical, err := provider.ListHistoricalIssues(t.Context(), ref, "")
	require.NoError(err)
	assert.Equal(canonicalHistoricalReqs, recorder.take())
	assert.Equal(canonicalHistorical, legacyHistorical)

	canonicalUpdated, err := provider.ListIssuesPage(t.Context(), ref, platform.ItemPageQuery{
		State: platform.ItemStateAll, Order: platform.ItemOrderUpdated, UpdatedSince: &watermark,
	})
	require.NoError(err)
	canonicalUpdatedReqs := recorder.take()
	legacyUpdated, err := provider.ListUpdatedIssues(t.Context(), ref, watermark, "")
	require.NoError(err)
	assert.Equal(canonicalUpdatedReqs, recorder.take())
	assert.Equal(canonicalUpdated, legacyUpdated)

	canonicalOpen, err := provider.ListIssuesPage(t.Context(), ref, platform.ItemPageQuery{
		State: platform.ItemStateOpen, Order: platform.ItemOrderUpdated,
	})
	require.NoError(err)
	canonicalOpenReqs := recorder.take()
	legacyOpen, err := provider.ListOpenIssues(t.Context(), ref)
	require.NoError(err)
	assert.Equal(canonicalOpenReqs, recorder.take())
	assert.Equal(canonicalOpen.Items, legacyOpen)
}

// TestGitHubListMergeRequestsPageDispatchesByQuery proves one method owns all
// three merge-request inventory request shapes and records the provider-side
// sort fences: open ignores sort, historical is created-ascending, and
// maintenance is updated-descending (a documented deviation from the ascending
// ItemOrder contract that the watermark stop compensates for).
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

func TestGitHubMergeRequestInventoryLegacyMatchesCanonical(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	watermark := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	recorder := &requestRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder.record(r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":201,"node_id":"PR_201","number":4,"title":"pull","state":"closed","html_url":"https://github.com/acme/widget/pull/4","user":{"login":"a"},"created_at":"2025-01-01T00:00:00Z","updated_at":"2026-07-01T00:00:00Z"}]`))
	}))
	defer srv.Close()
	provider := newArchiveTestGitHubProvider(t, srv.URL)
	ref := pagesTestRef()

	canonicalHistorical, err := provider.ListMergeRequestsPage(t.Context(), ref, platform.ItemPageQuery{
		State: platform.ItemStateAll, Order: platform.ItemOrderCreated,
	})
	require.NoError(err)
	canonicalHistoricalReqs := recorder.take()
	legacyHistorical, err := provider.ListHistoricalMergeRequests(t.Context(), ref, "")
	require.NoError(err)
	assert.Equal(canonicalHistoricalReqs, recorder.take())
	assert.Equal(canonicalHistorical, legacyHistorical)

	canonicalOpen, err := provider.ListMergeRequestsPage(t.Context(), ref, platform.ItemPageQuery{
		State: platform.ItemStateOpen, Order: platform.ItemOrderUpdated,
	})
	require.NoError(err)
	canonicalOpenReqs := recorder.take()
	legacyOpen, err := provider.ListOpenMergeRequests(t.Context(), ref)
	require.NoError(err)
	assert.Equal(canonicalOpenReqs, recorder.take())
	assert.Equal(canonicalOpen.Items, legacyOpen)
	_ = watermark
}

// TestGitHubLookupIssueMatchesLegacyAndDrivesLiveGet proves the single lookup
// operation feeds both the archive delegate (records every outcome) and the
// live delegate (requires present).
func TestGitHubLookupIssueMatchesLegacyAndDrivesLiveGet(t *testing.T) {
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
	legacy, err := provider.GetArchiveIssue(t.Context(), ref, 7)
	require.NoError(err)
	assert.Equal(canonical, legacy)
	assert.Equal(platform.LookupPresent, canonical.Outcome)

	live, err := provider.GetIssue(t.Context(), ref, 7)
	require.NoError(err)
	assert.Equal(canonical.Item, live)

	removed, err := provider.LookupIssue(t.Context(), ref, 9)
	require.NoError(err)
	assert.Equal(platform.LookupRemoved, removed.Outcome)
	_, liveErr := provider.GetIssue(t.Context(), ref, 9)
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
	legacy, err := provider.GetArchiveMergeRequest(t.Context(), ref, 7)
	require.NoError(err)
	assert.Equal(canonical, legacy)

	live, err := provider.GetMergeRequest(t.Context(), ref, 7)
	require.NoError(err)
	assert.Equal(canonical.Item, live)

	_, liveErr := provider.GetMergeRequest(t.Context(), ref, 9)
	require.ErrorIs(liveErr, platform.ErrNotFound)
}

// TestGitHubDetailPagesLegacyMatchesCanonical proves each canonical detail-page
// method produces the same rows and requests as its ArchiveReader delegate.
func TestGitHubDetailPagesLegacyMatchesCanonical(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	recorder := &requestRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder.record(r)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v3/repos/acme/widget/issues/7/comments":
			_, _ = w.Write([]byte(`[{"id":1,"node_id":"C_1","body":"comment","html_url":"https://github.com/acme/widget/issues/7#issuecomment-1","user":{"login":"writer"},"created_at":"2026-07-01T00:00:00Z"}]`))
		case "/api/v3/repos/acme/widget/pulls/7/reviews":
			_, _ = w.Write([]byte(`[{"id":31,"node_id":"R_31","state":"APPROVED","body":"ship it","html_url":"https://github.com/acme/widget/pull/7#pullrequestreview-31","user":{"login":"reviewer"},"submitted_at":"2026-07-02T00:00:00Z"}]`))
		case "/api/graphql":
			_, _ = w.Write([]byte(`{"data":{"repository":{"pullRequest":{"reviewThreads":{"edges":[{"cursor":"edge-1","node":{"id":"T_1","isResolved":false,"path":"main.go","diffSide":"RIGHT","line":1,"comments":{"nodes":[{"id":"RC_1","databaseId":1,"fullDatabaseId":"9000000001","pullRequestReview":{"databaseId":1},"body":"thread comment","author":{"login":"reviewer"},"path":"main.go","line":1,"url":"https://github.com/acme/widget/pull/7#discussion_r1","createdAt":"2026-07-03T00:00:00Z","updatedAt":"2026-07-03T01:00:00Z"}],"pageInfo":{"hasNextPage":false,"endCursor":null}}}}],"pageInfo":{"hasNextPage":false,"endCursor":null}}}}}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	provider := newArchiveTestGitHubProvider(t, srv.URL)
	ref := pagesTestRef()

	issueComments, err := provider.ListIssueCommentsPage(t.Context(), ref, 7, "")
	require.NoError(err)
	issueCommentsReqs := recorder.take()
	legacyIssueComments, err := provider.ListArchiveIssueComments(t.Context(), ref, 7, "")
	require.NoError(err)
	assert.Equal(issueCommentsReqs, recorder.take())
	assert.Equal(issueComments, legacyIssueComments)

	mrComments, err := provider.ListMergeRequestCommentsPage(t.Context(), ref, 7, "")
	require.NoError(err)
	mrCommentsReqs := recorder.take()
	legacyMRComments, err := provider.ListArchiveMergeRequestComments(t.Context(), ref, 7, "")
	require.NoError(err)
	assert.Equal(mrCommentsReqs, recorder.take())
	assert.Equal(mrComments, legacyMRComments)

	reviews, err := provider.ListSubmittedReviewsPage(t.Context(), ref, 7, "")
	require.NoError(err)
	reviewsReqs := recorder.take()
	legacyReviews, err := provider.ListArchiveSubmittedReviews(t.Context(), ref, 7, "")
	require.NoError(err)
	assert.Equal(reviewsReqs, recorder.take())
	assert.Equal(reviews, legacyReviews)

	threads, err := provider.ListReviewThreadsPage(t.Context(), ref, 7, "")
	require.NoError(err)
	threadsReqs := recorder.take()
	legacyThreads, err := provider.ListArchiveReviewThreads(t.Context(), ref, 7, "")
	require.NoError(err)
	assert.Equal(threadsReqs, recorder.take())
	assert.Equal(threads, legacyThreads)
	require.Len(threads.Items, 1)
	assert.Equal("T_1", threads.Items[0].ProviderThreadID)
}
