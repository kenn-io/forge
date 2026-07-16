package github

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/middleman/internal/platform"
)

func TestGitHubArchiveDiscoveryParityUsesOldestFirstIssueOnlyConnection(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		assert.Equal("/api/graphql", r.URL.Path)
		var request struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		assert.NoError(json.NewDecoder(r.Body).Decode(&request))
		assert.Contains(request.Query, "issues(first: 100")
		assert.NotContains(request.Query, "pullRequests")
		assert.Equal("CREATED_AT", request.Variables["orderField"])
		w.Header().Set("Content-Type", "application/json")
		if requests == 1 {
			assert.Nil(request.Variables["cursor"])
			_, _ = w.Write([]byte(`{"data":{"repository":{"issues":{"nodes":[
				{"id":"I_kw1","databaseId":101,"number":1,"title":"old issue","state":"CLOSED","body":"body","url":"https://github.com/acme/widget/issues/1","author":{"login":"author"},"createdAt":"2025-01-01T00:00:00Z","updatedAt":"2025-01-02T00:00:00Z","closedAt":"2025-01-02T00:00:00Z","comments":{"totalCount":2},"labels":{"nodes":[]},"assignees":{"nodes":[]}}
			],"pageInfo":{"hasNextPage":true,"endCursor":"issue-1"}}}}}`))
			return
		}
		assert.Equal("issue-1", request.Variables["cursor"])
		_, _ = w.Write([]byte(`{"data":{"repository":{"issues":{"nodes":[
			{"id":"I_kw3","databaseId":103,"number":3,"title":"newer issue","state":"OPEN","body":"","url":"https://github.com/acme/widget/issues/3","author":{"login":"author"},"createdAt":"2025-01-03T00:00:00Z","updatedAt":"2025-01-04T00:00:00Z","closedAt":null,"comments":{"totalCount":0},"labels":{"nodes":[]},"assignees":{"nodes":[]}}
		],"pageInfo":{"hasNextPage":false,"endCursor":null}}}}}`))
	}))
	defer srv.Close()

	provider := newArchiveTestGitHubProvider(t, srv.URL)
	ref := platform.RepoRef{
		Platform: platform.KindGitHub, Host: "github.com",
		Owner: "acme", Name: "widget", RepoPath: "acme/widget",
	}
	first, err := provider.ListHistoricalIssues(t.Context(), ref, "")
	require.NoError(err)
	require.Len(first.Items, 1)
	assert.Equal(1, first.Items[0].Number)
	assert.NotEmpty(first.NextCursor)
	assert.False(first.Exhausted)

	second, err := provider.ListHistoricalIssues(t.Context(), ref, first.NextCursor)
	require.NoError(err)
	require.Len(second.Items, 1)
	assert.Equal(3, second.Items[0].Number)
	assert.Equal("I_kw3", second.Items[0].PlatformExternalID)
	assert.Empty(second.NextCursor)
	assert.True(second.Exhausted)
	assert.Equal(2, requests)
}

func TestGitHubArchiveUpdatedIssuesUseInclusiveWatermarkAndStableContinuation(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	watermark := time.Date(2026, 7, 1, 2, 3, 4, 0, time.UTC)
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var request struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		assert.NoError(json.NewDecoder(r.Body).Decode(&request))
		assert.Equal("UPDATED_AT", request.Variables["orderField"])
		assert.Contains(request.Query, "filterBy: {since: $since}")
		assert.Equal(watermark.Add(-time.Second).Format(time.RFC3339Nano), request.Variables["since"])
		w.Header().Set("Content-Type", "application/json")
		cursor := any(nil)
		more := requests == 1
		if requests == 1 {
			cursor = "updated-1"
		}
		cursorJSON, err := json.Marshal(cursor)
		assert.NoError(err)
		_, _ = fmt.Fprintf(w, `{"data":{"repository":{"issues":{"nodes":[{"id":"I_%d","databaseId":%d,"number":%d,"title":"item","state":"OPEN","body":"","url":"https://github.com/acme/widget/issues/%d","author":{"login":"author"},"createdAt":"2025-01-01T00:00:00Z","updatedAt":"2026-07-01T02:03:04Z","closedAt":null,"comments":{"totalCount":0},"labels":{"nodes":[]},"assignees":{"nodes":[]}}],"pageInfo":{"hasNextPage":%t,"endCursor":%s}}}}}`, requests, requests, requests, requests, more, cursorJSON)
	}))
	defer srv.Close()

	provider := newArchiveTestGitHubProvider(t, srv.URL)
	ref := platform.RepoRef{Platform: platform.KindGitHub, Host: "github.com", Owner: "acme", Name: "widget", RepoPath: "acme/widget"}
	first, err := provider.ListUpdatedIssues(t.Context(), ref, watermark, "")
	require.NoError(err)
	_, err = provider.ListUpdatedIssues(t.Context(), ref, watermark.Add(time.Second), first.NextCursor)
	require.ErrorIs(err, platform.ErrProviderContract)
	otherHost := ref
	otherHost.Host = "github.example.com"
	_, err = provider.ListUpdatedIssues(t.Context(), otherHost, watermark, first.NextCursor)
	require.ErrorIs(err, platform.ErrProviderContract)
	second, err := provider.ListUpdatedIssues(t.Context(), ref, watermark, first.NextCursor)
	require.NoError(err)
	assert.Equal([]int{1}, []int{first.Items[0].Number})
	assert.Equal([]int{2}, []int{second.Items[0].Number})
	assert.True(second.Exhausted)
	assert.Equal(2, requests)
}

func TestGitHubArchiveDiscoveryParityIncludesAllPullRequestStatesAndStableSorts(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	var sorts []string
	var directions []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal("/api/v3/repos/acme/widget/pulls", r.URL.Path)
		assert.Equal("all", r.URL.Query().Get("state"))
		sorts = append(sorts, r.URL.Query().Get("sort"))
		directions = append(directions, r.URL.Query().Get("direction"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":201,"node_id":"PR_201","number":4,"title":"pull","state":"closed","html_url":"https://github.com/acme/widget/pull/4","user":{"login":"author"},"created_at":"2025-01-01T00:00:00Z","updated_at":"2026-07-01T00:00:00Z"}]`))
	}))
	defer srv.Close()
	provider := newArchiveTestGitHubProvider(t, srv.URL)
	ref := platform.RepoRef{Platform: platform.KindGitHub, Host: "github.com", Owner: "acme", Name: "widget", RepoPath: "acme/widget"}

	historical, err := provider.ListHistoricalMergeRequests(t.Context(), ref, "")
	require.NoError(err)
	updated, err := provider.ListUpdatedMergeRequests(
		t.Context(), ref, time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC), "",
	)
	require.NoError(err)
	assert.Equal([]string{"created", "updated"}, sorts)
	assert.Equal([]string{"asc", "desc"}, directions)
	require.Len(historical.Items, 1)
	require.Len(updated.Items, 1)
	assert.Equal("PR_201", historical.Items[0].PlatformExternalID)
	assert.Equal("author", updated.Items[0].Author)
	assert.True(historical.Exhausted)
	assert.True(updated.Exhausted)
}

func TestGitHubArchiveDetailPagesStopAfterOneRequest(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	commentCalls := 0
	reviewCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v3/repos/acme/widget/issues/7/comments":
			commentCalls++
			if commentCalls == 1 {
				w.Header().Set("Link", fmt.Sprintf(`<%s/api/v3/repos/acme/widget/issues/7/comments?page=2>; rel="next"`, srvURL(r)))
			}
			_, _ = fmt.Fprintf(w, `[{"id":%d,"node_id":"C_%d","body":"comment %d","html_url":"https://github.com/acme/widget/issues/7#issuecomment-%d","user":{"login":"writer"},"created_at":"2026-07-01T00:00:00Z"}]`, commentCalls, commentCalls, commentCalls, commentCalls)
		case "/api/v3/repos/acme/widget/pulls/7/reviews":
			reviewCalls++
			_, _ = w.Write([]byte(`[{"id":31,"node_id":"R_31","state":"APPROVED","body":"looks good","html_url":"https://github.com/acme/widget/pull/7#pullrequestreview-31","user":{"login":"reviewer"},"submitted_at":"2026-07-02T00:00:00Z"}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	provider := newArchiveTestGitHubProvider(t, srv.URL)
	ref := platform.RepoRef{Platform: platform.KindGitHub, Host: "github.com", Owner: "acme", Name: "widget", RepoPath: "acme/widget"}

	first, err := provider.ListArchiveMergeRequestComments(t.Context(), ref, 7, "")
	require.NoError(err)
	assert.Equal(1, commentCalls)
	require.Len(first.Items, 1)
	assert.Equal("comment 1", first.Items[0].Body)
	second, err := provider.ListArchiveMergeRequestComments(t.Context(), ref, 7, first.NextCursor)
	require.NoError(err)
	assert.Equal(2, commentCalls)
	assert.True(second.Exhausted)

	reviews, err := provider.ListArchiveSubmittedReviews(t.Context(), ref, 7, "")
	require.NoError(err)
	assert.Equal(1, reviewCalls)
	require.Len(reviews.Items, 1)
	assert.Equal("APPROVED", reviews.Items[0].Summary)
	assert.Equal("reviewer", reviews.Items[0].Author)
	assert.Equal("https://github.com/acme/widget/pull/7#pullrequestreview-31", reviews.Items[0].DirectURL)
}

func TestGitHubArchiveSubmittedReviewsExcludePendingAcrossPages(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	reviewCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/api/v3/repos/acme/widget/pulls/7/reviews" {
			http.NotFound(w, r)
			return
		}
		reviewCalls++
		if reviewCalls == 1 {
			w.Header().Set("Link", fmt.Sprintf(`<%s/api/v3/repos/acme/widget/pulls/7/reviews?page=2>; rel="next"`, srvURL(r)))
			_, _ = w.Write([]byte(`[
				{"id":30,"node_id":"R_30","state":"PENDING","body":"draft body","user":{"login":"draft-reviewer"}},
				{"id":31,"node_id":"R_31","state":"APPROVED","body":"ship it","user":{"login":"reviewer"},"submitted_at":"2026-07-02T00:00:00Z"}
			]`))
			return
		}
		_, _ = w.Write([]byte(`[
			{"id":32,"node_id":"R_32","state":"COMMENTED","body":"follow-up","user":{"login":"second-reviewer"},"submitted_at":"2026-07-03T00:00:00Z"},
			{"id":33,"node_id":"R_33","state":"APPROVED","body":"missing timestamp","user":{"login":"incomplete-reviewer"}}
		]`))
	}))
	defer srv.Close()
	provider := newArchiveTestGitHubProvider(t, srv.URL)
	ref := platform.RepoRef{Platform: platform.KindGitHub, Host: "github.com", Owner: "acme", Name: "widget", RepoPath: "acme/widget"}

	first, err := provider.ListArchiveSubmittedReviews(t.Context(), ref, 7, "")
	require.NoError(err)
	assert.False(first.Exhausted)
	require.Len(first.Items, 1)
	assert.Equal("R_31", first.Items[0].PlatformExternalID)

	second, err := provider.ListArchiveSubmittedReviews(t.Context(), ref, 7, first.NextCursor)
	require.NoError(err)
	assert.True(second.Exhausted)
	require.Len(second.Items, 1)
	assert.Equal("R_32", second.Items[0].PlatformExternalID)
	assert.Equal(2, reviewCalls)
}

func TestGitHubArchiveCursorsAreBoundToRepositoryAndItem(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Link", fmt.Sprintf(`<%s%s?page=2>; rel="next"`, srvURL(r), r.URL.Path))
		switch r.URL.Path {
		case "/api/graphql":
			_, _ = w.Write([]byte(`{"data":{"repository":{"issues":{"nodes":[{"id":"I_1","databaseId":1,"number":1,"title":"issue","state":"OPEN","body":"","url":"https://github.com/acme/widget/issues/1","createdAt":"2025-01-01T00:00:00Z","updatedAt":"2025-01-02T00:00:00Z","comments":{"totalCount":0},"labels":{"nodes":[]},"assignees":{"nodes":[]}}],"pageInfo":{"hasNextPage":true,"endCursor":"issue-1"}}}}}`))
		case "/api/v3/repos/acme/widget/issues/7/comments":
			_, _ = w.Write([]byte(`[{"id":1,"node_id":"C_1","body":"comment","created_at":"2025-01-01T00:00:00Z"}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	provider := newArchiveTestGitHubProvider(t, srv.URL)
	ref := platform.RepoRef{Platform: platform.KindGitHub, Host: "github.com", Owner: "acme", Name: "widget", RepoPath: "acme/widget"}

	issues, err := provider.ListHistoricalIssues(t.Context(), ref, "")
	require.NoError(err)
	otherRef := ref
	otherRef.Name = "other"
	otherRef.RepoPath = "acme/other"
	_, err = provider.ListHistoricalIssues(t.Context(), otherRef, issues.NextCursor)
	require.ErrorIs(err, platform.ErrProviderContract)

	comments, err := provider.ListArchiveIssueComments(t.Context(), ref, 7, "")
	require.NoError(err)
	_, err = provider.ListArchiveIssueComments(t.Context(), ref, 8, comments.NextCursor)
	require.ErrorIs(err, platform.ErrProviderContract)
	assert.Equal(2, requests)
}

func TestGitHubArchiveReviewThreadsStopBetweenGraphQLPages(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var request struct {
			Variables map[string]any `json:"variables"`
		}
		assert.NoError(json.NewDecoder(r.Body).Decode(&request))
		w.Header().Set("Content-Type", "application/json")
		more := calls == 1
		end := fmt.Sprintf("thread-%d", calls)
		_, _ = fmt.Fprintf(w, `{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[{"id":"T_%d","isResolved":%t,"path":"main.go","diffSide":"RIGHT","line":%d,"comments":{"nodes":[{"id":"RC_%d","databaseId":%d,"fullDatabaseId":"900000000%d","pullRequestReview":{"databaseId":%d},"body":"thread comment %d","author":{"login":"reviewer"},"path":"main.go","line":%d,"url":"https://github.com/acme/widget/pull/7#discussion_r%d","createdAt":"2026-07-03T00:00:00Z","updatedAt":"2026-07-03T01:00:00Z"}],"pageInfo":{"hasNextPage":false,"endCursor":null}}}],"pageInfo":{"hasNextPage":%t,"endCursor":"%s"}}}}}}`, calls, calls == 2, calls, calls, calls, calls, 40+calls, calls, calls, calls, more, end)
	}))
	defer srv.Close()
	provider := newArchiveTestGitHubProvider(t, srv.URL)
	ref := platform.RepoRef{Platform: platform.KindGitHub, Host: "github.com", Owner: "acme", Name: "widget", RepoPath: "acme/widget"}

	first, err := provider.ListArchiveReviewThreads(t.Context(), ref, 7, "")
	require.NoError(err)
	assert.Equal(1, calls)
	require.Len(first.Items, 1)
	assert.Equal("T_1", first.Items[0].ProviderThreadID)
	assert.Equal("reviewer", first.Items[0].AuthorLogin)
	assert.Equal("main.go", first.Items[0].Range.Path)
	assert.Equal(1, first.Items[0].Range.Line)
	assert.Equal("9000000001", first.Items[0].ProviderCommentID)
	assert.NotEmpty(first.NextCursor)
	_, err = provider.ListArchiveReviewThreads(t.Context(), ref, 8, first.NextCursor)
	require.ErrorIs(err, platform.ErrProviderContract)
	otherHost := ref
	otherHost.Host = "github.example.com"
	_, err = provider.ListArchiveReviewThreads(t.Context(), otherHost, 7, first.NextCursor)
	require.ErrorIs(err, platform.ErrProviderContract)
	assert.Equal(1, calls)

	second, err := provider.ListArchiveReviewThreads(t.Context(), ref, 7, first.NextCursor)
	require.NoError(err)
	assert.Equal(2, calls)
	require.Len(second.Items, 1)
	assert.Equal("T_2", second.Items[0].ProviderThreadID)
	assert.True(second.Items[0].Resolved)
	assert.True(second.Exhausted)
}

func TestGitHubArchiveReviewThreadCommentsHaveBoundedContinuation(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	calls := 0
	providerBody := strings.Repeat("provider body ", 4096)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		if calls == 1 {
			_, _ = fmt.Fprintf(w, `{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[{"id":"T_long","isResolved":false,"path":"main.go","diffSide":"RIGHT","line":9,"comments":{"nodes":[{"id":"RC_1","databaseId":1,"body":%q,"author":{"login":"a"},"path":"main.go","line":9,"createdAt":"2026-07-03T00:00:00Z","updatedAt":"2026-07-03T00:00:00Z"}],"pageInfo":{"hasNextPage":true,"endCursor":"comment-1"}}}],"pageInfo":{"hasNextPage":false,"endCursor":"thread-1"}}}}}}`, providerBody)
			return
		}
		_, _ = w.Write([]byte(`{"data":{"node":{"comments":{"nodes":[{"id":"RC_2","databaseId":2,"body":"reply","author":{"login":"b"},"path":"main.go","line":9,"createdAt":"2026-07-04T00:00:00Z","updatedAt":"2026-07-04T00:00:00Z"}],"pageInfo":{"hasNextPage":false,"endCursor":null}}}}}`))
	}))
	defer srv.Close()
	provider := newArchiveTestGitHubProvider(t, srv.URL)
	ref := platform.RepoRef{Platform: platform.KindGitHub, Host: "github.com", Owner: "acme", Name: "widget", RepoPath: "acme/widget"}

	first, err := provider.ListArchiveReviewThreads(t.Context(), ref, 7, "")
	require.NoError(err)
	assert.Equal(1, calls)
	require.Len(first.Items, 1)
	assert.Len(first.Items[0].Body, len(providerBody))
	assert.Equal("1", first.Items[0].ProviderCommentID)
	assert.False(first.Exhausted)
	rawCursor, err := base64.RawURLEncoding.DecodeString(first.NextCursor)
	require.NoError(err)
	assert.NotContains(string(rawCursor), "provider body")
	assert.Less(len(first.NextCursor), 2048)
	second, err := provider.ListArchiveReviewThreads(t.Context(), ref, 7, first.NextCursor)
	require.NoError(err)
	assert.Equal(2, calls)
	require.Len(second.Items, 1)
	assert.Equal("reply", second.Items[0].Body)
	assert.True(second.Exhausted)
}

func TestGitHubArchiveLookupOutcomes(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v3/repos/acme/widget/issues/7":
			_, _ = w.Write([]byte(`{"id":7,"node_id":"I_7","number":7,"repository_url":"https://api.github.com/repos/acme/widget","title":"present","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-02T00:00:00Z"}`))
		case "/api/v3/repos/acme/widget/issues/8":
			_, _ = w.Write([]byte(`{"id":8,"node_id":"I_8","number":8,"repository_url":"https://api.github.com/repos/acme/destination","title":"moved","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-02T00:00:00Z"}`))
		case "/api/v3/repos/acme/widget/issues/9":
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
		case "/api/v3/repos/acme/widget/issues/10":
			http.Error(w, `{"message":"Forbidden"}`, http.StatusForbidden)
		case "/api/v3/repos/acme/widget":
			_, _ = w.Write([]byte(`{"id":1,"name":"widget","owner":{"login":"acme"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	provider := newArchiveTestGitHubProvider(t, srv.URL)
	ref := platform.RepoRef{Platform: platform.KindGitHub, Host: "github.com", Owner: "acme", Name: "widget", RepoPath: "acme/widget"}

	present, err := provider.GetArchiveIssue(t.Context(), ref, 7)
	require.NoError(err)
	assert.Equal(platform.ArchiveLookupPresent, present.Outcome)
	moved, err := provider.GetArchiveIssue(t.Context(), ref, 8)
	require.NoError(err)
	assert.Equal(platform.ArchiveLookupMoved, moved.Outcome)
	require.NotNil(moved.Destination)
	assert.Equal("acme/destination", moved.Destination.RepoPath)
	removed, err := provider.GetArchiveIssue(t.Context(), ref, 9)
	require.NoError(err)
	assert.Equal(platform.ArchiveLookupRemoved, removed.Outcome)
	inaccessible, err := provider.GetArchiveIssue(t.Context(), ref, 10)
	require.NoError(err)
	assert.Equal(platform.ArchiveLookupInaccessible, inaccessible.Outcome)
}

func TestGitHubArchiveMergeRequestLookupDetectsTransfer(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		assert.Equal("/api/v3/repos/acme/widget/pulls/8", r.URL.Path)
		_, _ = w.Write([]byte(`{
			"id": 8,
			"number": 8,
			"base": {"repo": {"url": "https://api.github.com/repos/acme/destination"}}
		}`))
	}))
	t.Cleanup(srv.Close)
	provider := newArchiveTestGitHubProvider(t, srv.URL)
	ref := platform.RepoRef{Platform: platform.KindGitHub, Host: "github.com", Owner: "acme", Name: "widget", RepoPath: "acme/widget"}

	result, err := provider.GetArchiveMergeRequest(t.Context(), ref, 8)
	require.NoError(err)
	assert.Equal(platform.ArchiveLookupMoved, result.Outcome)
	require.NotNil(result.Destination)
	assert.Equal("acme/destination", result.Destination.RepoPath)
}

func TestGitHubArchiveLookupDoesNotRemoveWhenRepositoryIsInaccessible(t *testing.T) {
	require := require.New(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
	}))
	defer srv.Close()
	provider := newArchiveTestGitHubProvider(t, srv.URL)
	ref := platform.RepoRef{Platform: platform.KindGitHub, Host: "github.com", Owner: "hidden", Name: "repo", RepoPath: "hidden/repo"}

	_, err := provider.GetArchiveIssue(t.Context(), ref, 7)
	require.ErrorIs(err, platform.ErrPermissionDenied)
	_, err = provider.GetArchiveMergeRequest(t.Context(), ref, 8)
	require.ErrorIs(err, platform.ErrPermissionDenied)
}

func TestGitHubArchiveLookupPreservesTransientRepositoryProbeError(t *testing.T) {
	require := require.New(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/issues/7") {
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
			return
		}
		http.Error(w, `{"message":"Server Error"}`, http.StatusInternalServerError)
	}))
	defer srv.Close()
	provider := newArchiveTestGitHubProvider(t, srv.URL)
	ref := platform.RepoRef{Platform: platform.KindGitHub, Host: "github.com", Owner: "acme", Name: "widget", RepoPath: "acme/widget"}

	_, err := provider.GetArchiveIssue(t.Context(), ref, 7)
	require.Error(err)
	require.NotErrorIs(err, platform.ErrPermissionDenied)
}

func TestGitHubArchiveLookupPreservesRepositoryProbeRateLimit(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	resetAt := time.Date(2026, 7, 14, 20, 0, 0, 0, time.UTC)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/issues/7") {
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
			return
		}
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Limit", "5000")
		w.Header().Set("X-RateLimit-Reset", fmt.Sprint(resetAt.Unix()))
		http.Error(w, `{"message":"rate limited"}`, http.StatusForbidden)
	}))
	t.Cleanup(srv.Close)
	provider := newArchiveTestGitHubProvider(t, srv.URL)
	ref := platform.RepoRef{Platform: platform.KindGitHub, Host: "github.com", Owner: "acme", Name: "widget", RepoPath: "acme/widget"}

	_, err := provider.GetArchiveIssue(t.Context(), ref, 7)
	require.ErrorIs(err, platform.ErrRateLimited)
	var providerErr *platform.Error
	require.ErrorAs(err, &providerErr)
	assert.Equal(&resetAt, providerErr.ResetAt)
}

func TestGitHubArchiveRESTErrorsCarryProviderClassification(t *testing.T) {
	t.Parallel()
	resetAt := time.Date(2026, 7, 14, 18, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name      string
		status    int
		headers   http.Header
		want      error
		wantReset *time.Time
	}{
		{name: "authentication", status: http.StatusUnauthorized, want: platform.ErrPermissionDenied},
		{name: "rate limit", status: http.StatusForbidden, headers: http.Header{
			"X-Ratelimit-Remaining": []string{"0"},
			"X-Ratelimit-Limit":     []string{"5000"},
			"X-Ratelimit-Reset":     []string{fmt.Sprint(resetAt.Unix())},
		}, want: platform.ErrRateLimited, wantReset: &resetAt},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				for name, values := range tc.headers {
					for _, value := range values {
						w.Header().Add(name, value)
					}
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(`{"message":"denied"}`))
			}))
			t.Cleanup(srv.Close)
			provider := newArchiveTestGitHubProvider(t, srv.URL)
			ref := platform.RepoRef{Platform: platform.KindGitHub, Host: "github.com", Owner: "acme", Name: "widget", RepoPath: "acme/widget"}

			_, err := provider.ListHistoricalMergeRequests(t.Context(), ref, "")
			require.ErrorIs(err, tc.want)
			var providerErr *platform.Error
			require.ErrorAs(err, &providerErr)
			assert.Equal(platform.KindGitHub, providerErr.Provider)
			assert.Equal("github.com", providerErr.PlatformHost)
			assert.Equal(tc.wantReset, providerErr.ResetAt)
		})
	}
}

func TestGitHubArchiveGraphQLErrorsCarryProviderClassification(t *testing.T) {
	t.Parallel()
	resetAt := time.Date(2026, 7, 14, 19, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name      string
		status    int
		body      string
		headers   http.Header
		want      error
		wantReset *time.Time
	}{
		{name: "authentication", status: http.StatusUnauthorized, body: `{"message":"bad credentials"}`, want: platform.ErrPermissionDenied},
		{name: "rate limit response", status: http.StatusOK, body: `{"errors":[{"type":"RATE_LIMITED","message":"API rate limit exceeded"}]}`, headers: http.Header{
			"X-Ratelimit-Remaining": []string{"0"},
			"X-Ratelimit-Limit":     []string{"5000"},
			"X-Ratelimit-Reset":     []string{fmt.Sprint(resetAt.Unix())},
		}, want: platform.ErrRateLimited, wantReset: &resetAt},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				for name, values := range tc.headers {
					for _, value := range values {
						w.Header().Add(name, value)
					}
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			t.Cleanup(srv.Close)
			provider := newArchiveTestGitHubProvider(t, srv.URL)
			ref := platform.RepoRef{Platform: platform.KindGitHub, Host: "github.com", Owner: "acme", Name: "widget", RepoPath: "acme/widget"}

			_, err := provider.ListHistoricalIssues(t.Context(), ref, "")
			require.ErrorIs(err, tc.want)
			var providerErr *platform.Error
			require.ErrorAs(err, &providerErr)
			assert.Equal(platform.KindGitHub, providerErr.Provider)
			assert.Equal("github.com", providerErr.PlatformHost)
			assert.Equal(tc.wantReset, providerErr.ResetAt)
		})
	}
}

func TestGitHubArchiveCapabilitiesRequireBoundedClient(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	live := newArchiveTestGitHubProvider(t, newEmptyArchiveServer(t).URL)
	assert.Equal(platform.ArchiveCapabilities{
		HistoricalIssues: true, HistoricalMergeRequests: true,
		OrdinaryComments: true, SubmittedReviews: true, InlineReviewComments: true,
	}, live.Capabilities().Archive)
	assert.Equal(platform.ArchiveCapabilities{}, (&gitHubClientProvider{
		host: "github.com", client: &mockClient{},
	}).Capabilities().Archive)
	registry, err := platform.NewRegistry(live)
	require.NoError(err)
	reader, err := registry.ArchiveReader(platform.KindGitHub, "github.com")
	require.NoError(err)
	assert.NotNil(reader)
	_, err = registry.ArchiveReader(platform.KindGitHub, "github.example.com")
	require.Error(err)
}

func newEmptyArchiveServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	t.Cleanup(server.Close)
	return server
}

func newArchiveTestGitHubProvider(t *testing.T, baseURL string) *gitHubClientProvider {
	t.Helper()
	client, err := NewClient(
		testTokenSource("archive-token"), "github.com", nil, nil,
		WithBaseURLForTesting(baseURL),
	)
	require.NoError(t, err)
	return &gitHubClientProvider{host: "github.com", client: client}
}

func srvURL(r *http.Request) string {
	return "http://" + r.Host
}
