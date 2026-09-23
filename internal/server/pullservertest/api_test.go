package pullservertest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	serverfake "go.kenn.io/forge/internal/testutil/serverfake"
	servertest "go.kenn.io/forge/internal/testutil/servertest"

	gh "github.com/google/go-github/v91/github"
	"github.com/shurcooL/githubv4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/apiclient/generated"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/db"

	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/platformdb"
	"go.kenn.io/forge/internal/server"
	"go.kenn.io/forge/internal/server/itemapi"
	"go.kenn.io/forge/internal/server/pullapi"
	"go.kenn.io/forge/internal/stacks"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/platform"

	platformgithub "go.kenn.io/forge/platform/github"
)

func TestMain(m *testing.M) {
	os.Exit(serverfake.RunMain(m))
}

func setupTestServerWithMock(t *testing.T, mock *serverfake.MockGH) (*server.Server, *db.DB, *ghclient.Syncer) {
	t.Helper()
	return setupTestServerWithRepos(t, mock, serverfake.DefaultTestRepos)
}

func setupTestServerWithRepos(
	t *testing.T, mock *serverfake.MockGH, repos []ghclient.RepoRef,
) (*server.Server, *db.DB, *ghclient.Syncer) {
	return setupTestServerWithReposAndOptions(t, mock, repos, server.ServerOptions{})
}

func setupTestServerWithReposAndOptions(
	t *testing.T, mock *serverfake.MockGH, repos []ghclient.RepoRef, options server.ServerOptions,
) (*server.Server, *db.DB, *ghclient.Syncer) {
	t.Helper()

	database := dbtest.Open(t)
	repos = append([]ghclient.RepoRef(nil), repos...)
	for i := range repos {
		repo := &repos[i]
		if repo.PlatformExternalID == "" {
			repo.PlatformExternalID = "repo-" + repo.Owner + "-" + repo.Name
		}
		_, err := database.UpsertRepo(
			t.Context(), platformdb.DBRepoIdentity(platform.RepoRef{
				Platform:           platform.Kind(repo.Platform),
				Host:               repo.PlatformHost,
				Owner:              repo.Owner,
				Name:               repo.Name,
				RepoPath:           repo.RepoPath,
				PlatformExternalID: repo.PlatformExternalID,
			}),
		)
		require.NoError(t, err)
	}

	syncer := ghclient.NewSyncer(map[string]ghclient.Client{"github.com": mock}, database, nil, repos, time.Minute, nil, nil)
	// Drain any TriggerRun goroutines (fired by handlers like
	// POST /sync) before tests tear down. Registered after the DB
	// cleanup so LIFO ordering runs Stop first: without this, a
	// leaked goroutine from one test's handler can outlive its DB.
	t.Cleanup(syncer.Stop)
	var cfg *config.Config
	if options.WorktreeDir != "" {
		cfg = &config.Config{Tmux: config.Tmux{
			Command: []string{filepath.Join(t.TempDir(), "missing-tmux")},
		}}
	}
	srv := server.New(
		database, syncer, nil, "/",
		cfg, options,
	)
	// Registered after the DB cleanup so LIFO ordering runs Shutdown
	// first and lets background goroutines finish before DB close.
	t.Cleanup(func() { serverfake.GracefulShutdown(t, srv) })
	return srv, database, syncer
}

func TestAPIListPullsKeepsCachedCIDecorationsAfterIndexSync(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	now := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	headSHA := "same-head"
	baseSHA := "base-sha"
	checksJSON := `[{"name":"build","status":"completed","conclusion":"failure"}]`

	str := func(v string) *string { return &v }
	mock := &serverfake.MockGH{
		ListOpenPullRequestsFn: func(_ context.Context, _, _ string) ([]*gh.PullRequest, error) {
			number := 1
			id := int64(1001)
			return []*gh.PullRequest{{
				ID:        &id,
				Number:    &number,
				Title:     str("Cached CI PR"),
				State:     str("open"),
				HTMLURL:   str("https://github.com/acme/widget/pull/1"),
				User:      &gh.User{Login: str("octocat")},
				CreatedAt: &gh.Timestamp{Time: now},
				UpdatedAt: &gh.Timestamp{Time: now},
				Head:      &gh.PullRequestBranch{Ref: str("feature"), SHA: &headSHA},
				Base:      &gh.PullRequestBranch{Ref: str("main"), SHA: &baseSHA},
			}}, nil
		},
	}
	srv, database, syncer := setupTestServerWithMock(t, mock)
	serverfake.SeedPR(t, database, "acme", "widget", 1,
		serverfake.WithSeedPRTitle("Cached CI PR"),
		serverfake.WithSeedPRHeadSHA(headSHA),
		serverfake.WithSeedPRBaseSHA(baseSHA),
		serverfake.WithSeedPRCI("failure", checksJSON),
		serverfake.WithSeedPRTimes(now, now, now),
	)
	client := servertest.SetupTestClient(t, srv)

	syncer.RunOnce(ctx)

	resp, err := client.HTTP.ListPullsWithResponse(ctx, &generated.ListPullsRequestOptions{})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.Len(*resp.JSON200, 1)
	pull := (*resp.JSON200)[0]
	assert.Equal("failure", pull.CIStatus)
	assert.JSONEq(checksJSON, pull.CIChecksJSON)
}

func TestE2ELargeRepoSkipsGraphQLAndUsesConditionalPRDetail(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()

	changedAt := time.Now().UTC().Truncate(time.Second)
	unchangedAt := changedAt.Add(-2 * time.Hour)
	detailFetchedAt := changedAt.Add(-time.Hour)

	buildPR := func(number int, updatedAt time.Time, title string) *gh.PullRequest {
		id := int64(number * 1000)
		state := "open"
		url := fmt.Sprintf("https://github.com/acme/widget/pull/%d", number)
		author := "alice"
		headSHA := fmt.Sprintf("head-%d", number)
		headRef := fmt.Sprintf("feature-%d", number)
		baseRef := "main"
		created := gh.Timestamp{Time: unchangedAt}
		updated := gh.Timestamp{Time: updatedAt}
		comments := 1
		return &gh.PullRequest{
			ID:        &id,
			Number:    &number,
			State:     &state,
			Title:     &title,
			HTMLURL:   &url,
			User:      &gh.User{Login: &author},
			CreatedAt: &created,
			UpdatedAt: &updated,
			Comments:  &comments,
			Head:      &gh.PullRequestBranch{SHA: &headSHA, Ref: &headRef},
			Base:      &gh.PullRequestBranch{Ref: &baseRef},
		}
	}

	openPRs := make([]*gh.PullRequest, 0, 100)
	for number := 1; number <= 100; number++ {
		updatedAt := unchangedAt
		title := fmt.Sprintf("existing PR %d", number)
		if number == 1 {
			updatedAt = changedAt
			title = "changed PR from list"
		}
		openPRs = append(openPRs, buildPR(number, updatedAt, title))
	}

	var conditionalCalls atomic.Int32
	var graphQLPRCalls atomic.Int32
	mock := &serverfake.MockGH{
		ListOpenPullRequestsFn: func(_ context.Context, _, _ string) ([]*gh.PullRequest, error) {
			return openPRs, nil
		},
		ListOpenIssuesFn: func(_ context.Context, _, _ string) ([]*gh.Issue, error) {
			return nil, &gh.ErrorResponse{
				Response: &http.Response{StatusCode: http.StatusNotModified},
			}
		},
		GetPullRequestIfChangedFn: func(_ context.Context, _, _ string, number int, etag string) (*gh.PullRequest, string, bool, error) {
			conditionalCalls.Add(1)
			require.Equal(1, number)
			require.Equal(`"etag-v1"`, etag)
			return buildPR(number, changedAt, "changed PR detail"), `"etag-v2"`, false, nil
		},
		ListIssueCommentsFn: func(_ context.Context, _, _ string, number int) ([]*gh.IssueComment, error) {
			if number != 1 {
				return nil, nil
			}
			id := int64(9001)
			body := "detail comment"
			author := "reviewer"
			created := gh.Timestamp{Time: changedAt}
			return []*gh.IssueComment{{
				ID:        &id,
				Body:      &body,
				User:      &gh.User{Login: &author},
				CreatedAt: &created,
				UpdatedAt: &created,
			}}, nil
		},
	}

	gqlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if bytes.Contains(body, []byte("pullRequests")) {
			graphQLPRCalls.Add(1)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"errors":[{"message":"bulk PR fetch should be skipped"}]}`))
	}))
	defer gqlSrv.Close()

	database := dbtest.Open(t)
	repoID, err := database.UpsertRepo(ctx, serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	for number := 1; number <= 100; number++ {
		_, err := database.UpsertMergeRequest(ctx, &db.MergeRequest{
			RepoID:          repoID,
			PlatformID:      int64(number * 1000),
			Number:          number,
			URL:             fmt.Sprintf("https://github.com/acme/widget/pull/%d", number),
			Title:           fmt.Sprintf("existing PR %d", number),
			Author:          "alice",
			State:           "open",
			HeadBranch:      fmt.Sprintf("feature-%d", number),
			BaseBranch:      "main",
			PlatformHeadSHA: fmt.Sprintf("head-%d", number),
			CreatedAt:       unchangedAt,
			UpdatedAt:       unchangedAt,
			LastActivityAt:  unchangedAt,
			DetailFetchedAt: &detailFetchedAt,
		})
		require.NoError(err)
	}
	require.NoError(database.UpsertHTTPEtag(
		ctx, "github", "github.com", "acme", "widget",
		"pull_request", 1, `"etag-v1"`,
	))

	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": mock},
		database,
		nil,
		serverfake.DefaultTestRepos,
		time.Minute,
		nil,
		map[string]*ghclient.SyncBudget{"github.com": ghclient.NewSyncBudget(10000)},
	)
	t.Cleanup(syncer.Stop)
	syncer.SetFetchers(map[string]*ghclient.GraphQLFetcher{
		"github.com": ghclient.NewGraphQLFetcherWithClient(
			githubv4.NewEnterpriseClient(gqlSrv.URL, gqlSrv.Client()),
			nil,
		),
	})

	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	t.Cleanup(func() { serverfake.GracefulShutdown(t, srv) })

	syncer.RunOnce(ctx)

	assert.Zero(int(graphQLPRCalls.Load()),
		"large existing repo refresh should not bulk-fetch PRs through GraphQL")
	assert.Equal(int32(1), conditionalCalls.Load(),
		"only the changed PR should run a conditional detail fetch")

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/pulls/gh/acme/widget/1", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	require.Equal(http.StatusOK, rr.Code)

	var detailResp pullapi.MergeRequestDetailResponse
	require.NoError(json.Unmarshal(rr.Body.Bytes(), &detailResp))
	require.NotNil(detailResp.MergeRequest)
	assert.Equal("changed PR detail", detailResp.MergeRequest.Title)
	assert.Equal(1, detailResp.MergeRequest.CommentCount)
	require.Len(detailResp.Events, 1)
	assert.Equal("detail comment", detailResp.Events[0].Body)

	etag, err := database.GetHTTPEtag(
		ctx, "github", "github.com", "acme", "widget",
		"pull_request", 1,
	)
	require.NoError(err)
	assert.Equal(`"etag-v2"`, etag)
}

func TestE2EConditionalPRDetailRefreshesInlineModerationThroughAPI(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()

	now := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
	recentDetail := now.Add(time.Minute)
	staleDetail := now.Add(-time.Hour)
	buildPR := func(number int) *gh.PullRequest {
		id := int64(number * 1000)
		state := "open"
		title := fmt.Sprintf("existing PR %d", number)
		url := fmt.Sprintf("https://github.com/acme/widget/pull/%d", number)
		author := "alice"
		headSHA := fmt.Sprintf("head-%d", number)
		headRef := fmt.Sprintf("feature-%d", number)
		baseRef := "main"
		timestamp := gh.Timestamp{Time: now}
		return &gh.PullRequest{
			ID: &id, Number: &number, State: &state, Title: &title, HTMLURL: &url,
			User: &gh.User{Login: &author}, CreatedAt: &timestamp, UpdatedAt: &timestamp,
			Head: &gh.PullRequestBranch{SHA: &headSHA, Ref: &headRef},
			Base: &gh.PullRequestBranch{Ref: &baseRef},
		}
	}

	openPRs := make([]*gh.PullRequest, 0, 100)
	for number := 1; number <= 100; number++ {
		openPRs = append(openPRs, buildPR(number))
	}
	inlineHidden := true
	var conditionalCalls atomic.Int32
	mock := &serverfake.MockGH{
		ListOpenPullRequestsFn: func(context.Context, string, string) ([]*gh.PullRequest, error) {
			return openPRs, nil
		},
		ListOpenIssuesFn: func(context.Context, string, string) ([]*gh.Issue, error) {
			return nil, &gh.ErrorResponse{Response: &http.Response{StatusCode: http.StatusNotModified}}
		},
		GetPullRequestIfChangedFn: func(
			_ context.Context, _, _ string, number int, etag string,
		) (*gh.PullRequest, string, bool, error) {
			conditionalCalls.Add(1)
			require.Equal(1, number)
			require.Equal(`"etag-inline"`, etag)
			return nil, etag, true, nil
		},
		ListReviewThreadsFn: func(context.Context, string, string, int) ([]platformgithub.PullRequestReviewThread, error) {
			line := 12
			reason := ""
			if inlineHidden {
				reason = "ABUSE"
			}
			return []platformgithub.PullRequestReviewThread{{
				NodeID: "PRRT_conditional", Path: "src/main.go", Side: "RIGHT", Line: line,
				Comments: []platformgithub.PullRequestReviewThreadComment{{
					NodeID: "PRRC_conditional", DatabaseID: 112233,
					Body: "moderated inline comment", AuthorLogin: "reviewer",
					CommitID: "head-1", IsMinimized: inlineHidden, MinimizedReason: reason,
					CreatedAt: now, UpdatedAt: now,
				}},
			}}, nil
		},
	}

	gqlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		if bytes.Contains(body, []byte("pullRequest(number:")) {
			_, _ = w.Write([]byte(`{"data":{"repository":{"pullRequest":{"comments":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":null}}}}}}`))
			return
		}
		_, _ = w.Write([]byte(`{"errors":[{"message":"bulk GraphQL should be skipped"}]}`))
	}))
	defer gqlSrv.Close()

	database := dbtest.Open(t)
	repoID, err := database.UpsertRepo(ctx, serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	var mrID int64
	for number := 1; number <= 100; number++ {
		detailFetchedAt := &recentDetail
		if number == 1 {
			detailFetchedAt = nil
		}
		id, err := database.UpsertMergeRequest(ctx, &db.MergeRequest{
			RepoID: repoID, PlatformID: int64(number * 1000), Number: number,
			URL:   fmt.Sprintf("https://github.com/acme/widget/pull/%d", number),
			Title: fmt.Sprintf("existing PR %d", number), Author: "alice", State: "open",
			HeadBranch: fmt.Sprintf("feature-%d", number), BaseBranch: "main",
			PlatformHeadSHA: fmt.Sprintf("head-%d", number), CreatedAt: now, UpdatedAt: now,
			LastActivityAt: now, DetailFetchedAt: detailFetchedAt,
		})
		require.NoError(err)
		if number == 1 {
			mrID = id
		}
	}
	require.NoError(database.UpsertHTTPEtag(
		ctx, "github", "github.com", "acme", "widget", "pull_request", 1, `"etag-inline"`,
	))

	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": mock}, database, nil, serverfake.DefaultTestRepos,
		time.Minute, nil, map[string]*ghclient.SyncBudget{"github.com": ghclient.NewSyncBudget(10000)},
	)
	t.Cleanup(syncer.Stop)
	syncer.SetFetchers(map[string]*ghclient.GraphQLFetcher{
		"github.com": ghclient.NewGraphQLFetcherWithClient(
			githubv4.NewEnterpriseClient(gqlSrv.URL, gqlSrv.Client()), nil,
		),
	})
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	t.Cleanup(func() { serverfake.GracefulShutdown(t, srv) })
	client := servertest.SetupTestClient(t, srv)

	syncer.RunOnce(ctx)
	first, err := client.HTTP.GetPullWithResponse(ctx, &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
	require.NoError(err)
	require.Equal(http.StatusOK, first.StatusCode)
	require.NotNil(first.JSON200)
	require.NotNil(first.JSON200.Events)
	require.Len(first.JSON200.Events, 1)
	assert.JSONEq(`{"provider_hidden":true,"provider_hidden_reason":"ABUSE"}`, first.JSON200.Events[0].MetadataJSON)
	threads, err := database.ListMRReviewThreads(ctx, mrID)
	require.NoError(err)
	require.Len(threads, 1)
	assert.JSONEq(`{"provider_hidden":true,"provider_hidden_reason":"ABUSE"}`, threads[0].MetadataJSON)

	inlineHidden = false
	_, err = database.WriteDB().ExecContext(ctx,
		`UPDATE forge_merge_requests SET detail_fetched_at = ? WHERE id = ?`, staleDetail, mrID,
	)
	require.NoError(err)
	syncer.RunOnce(ctx)
	second, err := client.HTTP.GetPullWithResponse(ctx, &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
	require.NoError(err)
	require.Equal(http.StatusOK, second.StatusCode)
	require.NotNil(second.JSON200)
	require.NotNil(second.JSON200.Events)
	require.Len(second.JSON200.Events, 1)
	assert.Empty(second.JSON200.Events[0].MetadataJSON)
	threads, err = database.ListMRReviewThreads(ctx, mrID)
	require.NoError(err)
	require.Len(threads, 1)
	assert.Empty(threads[0].MetadataJSON)
	assert.Equal(int32(2), conditionalCalls.Load())
}

func TestE2EPRDetailPersistsCombinedGraphQLDiscussions(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()

	now := time.Date(2026, 8, 5, 11, 30, 0, 0, time.UTC)
	createdAt := now.Format(time.RFC3339)
	firstUpdatedAt := now.Add(5 * time.Minute).Format(time.RFC3339)
	secondUpdatedAt := now.Add(6 * time.Minute).Format(time.RFC3339)
	currentUpdatedAt := firstUpdatedAt
	firstVisibility := `"isMinimized":true,"minimizedReason":"OFF_TOPIC"`
	reviewCreatedAt := now.Add(3 * time.Minute)
	reviewUpdatedAt := now.Add(4 * time.Minute)
	reviewThreadsPageInfo := `{"hasNextPage":false,"endCursor":null}`

	commentsJSON := func() string {
		return `{"nodes":[{
			"databaseId":9231,
			"author":{"login":"commenter"},
			"body":"visible after moderation review",
			"url":"https://github.com/acme/widget/pull/177#issuecomment-9231",
			"createdAt":"` + now.Add(time.Minute).Format(time.RFC3339) + `",
			"updatedAt":"` + now.Add(time.Minute).Format(time.RFC3339) + `",
			` + firstVisibility + `
		}],"pageInfo":{"hasNextPage":true,"endCursor":"cursor"}}`
	}
	reviewThreadsJSON := func() string {
		return `{"nodes":[{
			"id":"PRRT_combined",
			"isResolved":false,
			"isOutdated":false,
			"path":"src/main.go",
			"line":12,
			"originalLine":12,
			"startLine":null,
			"originalStartLine":null,
			"diffSide":"RIGHT",
			"comments":{"nodes":[{
				"id":"PRRC_combined",
				"databaseId":9233,
				"fullDatabaseId":9233,
				"body":"hidden inline reply",
				"path":"src/main.go",
				"line":12,
				"originalLine":12,
				"subjectType":"LINE",
				"diffHunk":"@@ -12 +12 @@",
				"url":"https://github.com/acme/widget/pull/177#discussion_r9233",
				"author":{"login":"reviewer"},
				"commit":{"oid":"deadbeef"},
				"originalCommit":{"oid":"deadbeef"},
				"pullRequestReview":{"databaseId":991},
				"isMinimized":true,
				"minimizedReason":"ABUSE",
				"createdAt":"` + reviewCreatedAt.Format(time.RFC3339) + `",
				"updatedAt":"` + reviewUpdatedAt.Format(time.RFC3339) + `"
			}],"pageInfo":{"hasNextPage":false,"endCursor":null}}
		}],"pageInfo":` + reviewThreadsPageInfo + `}`
	}

	gqlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if bytes.Contains(body, []byte("pullRequest(number:")) {
			_, _ = w.Write([]byte(`{"data":{"repository":{"pullRequest":{"comments":{"nodes":[{
				"databaseId":9232,
				"fullDatabaseId":9232,
				"author":{"login":"commenter"},
				"body":"outside the first GraphQL page",
				"url":"https://github.com/acme/widget/pull/177#issuecomment-9232",
				"isMinimized":true,
				"minimizedReason":"OFF_TOPIC",
				"createdAt":"` + now.Add(2*time.Minute).Format(time.RFC3339) + `",
				"updatedAt":"` + now.Add(2*time.Minute).Format(time.RFC3339) + `"
			}],"pageInfo":{"hasNextPage":false,"endCursor":null}}}}}}`))
			return
		}
		if bytes.Contains(body, []byte("pullRequests")) {
			resp := `{"data":{"repository":{"pullRequests":{"nodes":[{
				"databaseId":177100,
				"number":177,
				"title":"Moderated PR comments",
				"state":"OPEN",
				"isDraft":false,
				"body":"GraphQL moderation state",
				"url":"https://github.com/acme/widget/pull/177",
				"author":{"login":"heidi"},
				"createdAt":"` + createdAt + `",
				"updatedAt":"` + currentUpdatedAt + `",
				"mergedAt":null,
				"closedAt":null,
				"additions":1,
				"deletions":0,
				"mergeable":"MERGEABLE",
				"reviewDecision":"",
				"headRefName":"feature/moderated-comments",
				"baseRefName":"main",
				"headRefOid":"deadbeef",
				"baseRefOid":"feedface",
				"headRepository":{"url":"https://github.com/acme/widget"},
				"labels":{"nodes":[]},
				"comments":` + commentsJSON() + `,
				"reviewThreads":` + reviewThreadsJSON() + `,
				"reviews":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}},
				"allCommits":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}},
				"lastCommit":{"nodes":[]},
				"timelineItems":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}
			}],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`
			_, _ = w.Write([]byte(resp))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"repository":{"issues":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`))
	}))
	defer gqlSrv.Close()

	prID := int64(177100)
	prNumber := 177
	prTitle := "Moderated PR comments"
	prState := "open"
	prURL := "https://github.com/acme/widget/pull/177"
	prTime := gh.Timestamp{Time: now}
	firstCommentID := int64(9231)
	secondCommentID := int64(9232)
	var restCommentCalls atomic.Int32
	mock := &serverfake.MockGH{
		ListOpenPullRequestsFn: func(_ context.Context, _, _ string) ([]*gh.PullRequest, error) {
			updatedAt, err := time.Parse(time.RFC3339, currentUpdatedAt)
			require.NoError(err)
			return []*gh.PullRequest{{
				ID: &prID, Number: &prNumber, Title: &prTitle, State: &prState,
				HTMLURL: &prURL, User: &gh.User{Login: new("heidi")},
				CreatedAt: &prTime, UpdatedAt: &gh.Timestamp{Time: updatedAt},
				Head: &gh.PullRequestBranch{Ref: new("feature/moderated-comments"), SHA: new("deadbeef")},
				Base: &gh.PullRequestBranch{Ref: new("main")},
			}}, nil
		},
		ListOpenIssuesFn: func(_ context.Context, _, _ string) ([]*gh.Issue, error) {
			return nil, &gh.ErrorResponse{Response: &http.Response{StatusCode: http.StatusNotModified}}
		},
		ListIssueCommentsFn: func(_ context.Context, _, _ string, number int) ([]*gh.IssueComment, error) {
			require.Equal(prNumber, number)
			restCommentCalls.Add(1)
			return nil, nil
		},
	}

	database := dbtest.Open(t)
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": mock},
		database, nil, serverfake.DefaultTestRepos, time.Minute, nil,
		map[string]*ghclient.SyncBudget{"github.com": ghclient.NewSyncBudget(10000)},
	)
	t.Cleanup(syncer.Stop)
	syncer.SetFetchers(map[string]*ghclient.GraphQLFetcher{
		"github.com": ghclient.NewGraphQLFetcherWithClient(
			githubv4.NewEnterpriseClient(gqlSrv.URL, gqlSrv.Client()), nil,
		),
	})

	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	t.Cleanup(func() { serverfake.GracefulShutdown(t, srv) })
	client := servertest.SetupTestClient(t, srv)

	syncer.RunOnce(ctx)
	assert.Zero(restCommentCalls.Load())
	firstResp, err := client.HTTP.GetPullWithResponse(ctx, &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(int64(prNumber))}})
	require.NoError(err)
	require.Equal(http.StatusOK, firstResp.StatusCode)
	require.NotNil(firstResp.JSON200)
	require.NotNil(firstResp.JSON200.Events)
	require.Len(firstResp.JSON200.Events, 3)
	firstProviderUpdatedAt, err := time.Parse(time.RFC3339, firstUpdatedAt)
	require.NoError(err)
	assert.Equal(firstProviderUpdatedAt, firstResp.JSON200.MergeRequest.LastActivityAt)
	firstMetadata := make(map[int64]string, len(firstResp.JSON200.Events))
	inlineFound := false
	for _, event := range firstResp.JSON200.Events {
		if event.EventType == "review_comment" {
			inlineFound = true
			assert.Equal("hidden inline reply", event.Body)
			assert.Equal(reviewCreatedAt, event.CreatedAt)
			assert.JSONEq(`{"provider_hidden":true,"provider_hidden_reason":"ABUSE"}`, event.MetadataJSON)
			require.NotNil(event.DiffThread)
			require.NotNil(event.DiffThread.MetadataJSON)
			assert.JSONEq(`{"provider_hidden":true,"provider_hidden_reason":"ABUSE"}`, *event.DiffThread.MetadataJSON)
			continue
		}
		require.NotNil(event.PlatformID)
		firstMetadata[*event.PlatformID] = event.MetadataJSON
	}
	require.True(inlineFound)
	assert.Contains(firstMetadata[firstCommentID], `"provider_hidden":true`)
	assert.Contains(firstMetadata[secondCommentID], `"provider_hidden":true`)
	storedMR, err := database.GetMergeRequest(ctx, "github", "github.com", "acme", "widget", prNumber)
	require.NoError(err)
	require.NotNil(storedMR)
	require.NotNil(storedMR.DetailFetchedAt)
	assert.Equal(firstProviderUpdatedAt, storedMR.LastActivityAt)
	storedThreads, err := database.ListMRReviewThreads(ctx, storedMR.ID)
	require.NoError(err)
	require.Len(storedThreads, 1)
	assert.Equal(reviewCreatedAt, storedThreads[0].CreatedAt)
	assert.Equal(reviewUpdatedAt, storedThreads[0].UpdatedAt)
	assert.JSONEq(`{"provider_hidden":true,"provider_hidden_reason":"ABUSE"}`, storedThreads[0].MetadataJSON)

	currentUpdatedAt = secondUpdatedAt
	firstVisibility = `"isMinimized":false,"minimizedReason":null`
	reviewThreadsPageInfo = `{"hasNextPage":true,"endCursor":"thread-cursor"}`
	syncer.RunOnce(ctx)
	assert.Zero(restCommentCalls.Load())
	secondResp, err := client.HTTP.GetPullWithResponse(ctx, &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(int64(prNumber))}})
	require.NoError(err)
	require.Equal(http.StatusOK, secondResp.StatusCode)
	require.NotNil(secondResp.JSON200)
	require.NotNil(secondResp.JSON200.Events)
	require.Len(secondResp.JSON200.Events, 3)
	secondProviderUpdatedAt, err := time.Parse(time.RFC3339, secondUpdatedAt)
	require.NoError(err)
	assert.Equal(secondProviderUpdatedAt, secondResp.JSON200.MergeRequest.LastActivityAt)
	secondMetadata := make(map[int64]string, len(secondResp.JSON200.Events))
	for _, event := range secondResp.JSON200.Events {
		if event.EventType == "review_comment" {
			assert.Contains(event.MetadataJSON, `"provider_hidden":true`)
			continue
		}
		require.NotNil(event.PlatformID)
		secondMetadata[*event.PlatformID] = event.MetadataJSON
	}
	assert.NotContains(secondMetadata[firstCommentID], `"provider_hidden":true`)
	assert.Contains(secondMetadata[secondCommentID], `"provider_hidden":true`)
	assert.Contains(secondMetadata[secondCommentID], `"provider_hidden_reason":"OFF_TOPIC"`)
	storedMR, err = database.GetMergeRequest(ctx, "github", "github.com", "acme", "widget", prNumber)
	require.NoError(err)
	require.NotNil(storedMR)
	assert.Nil(storedMR.DetailFetchedAt)
	assert.Equal(secondProviderUpdatedAt, storedMR.LastActivityAt)
}

// TestE2EGraphQLBulkSyncPersistsWorkflowApprovalForForkPR pins the
// fork-fallback matching path through the GraphQL bulk sync. The
// workflow run mock returns an empty PullRequests array (mirroring
// what GitHub returns for fork-triggered runs) and identifies the PR
// only by head repository full name and head branch. The GraphQL
// adapter never populates Head.Repo.FullName, so this exercises
// ParseHeadRepoFullName against the persisted clone URL; a regression
// removing that fallback would leave the Approve workflows button
// hidden for every fork PR synced through bulk.
func TestE2EGraphQLBulkSyncPersistsWorkflowApprovalForForkPR(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()

	now := time.Date(2026, 4, 14, 10, 0, 0, 0, time.UTC)
	nowRFC3339 := now.Format(time.RFC3339)
	const prNumber = 174
	const prID int64 = 174000
	const headSHA = "abc123"
	const forkFullName = "ericdill/widget"
	const headRef = "feature/fork-pr"

	gqlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if bytes.Contains(body, []byte("pullRequests")) {
			resp := `{"data":{"repository":{"pullRequests":{"nodes":[{
				"databaseId":174000,
				"number":174,
				"title":"Fork PR awaiting workflow approval",
				"state":"OPEN",
				"isDraft":false,
				"body":"",
				"url":"https://github.com/acme/widget/pull/174",
				"author":{"login":"ericdill"},
				"createdAt":"` + nowRFC3339 + `",
				"updatedAt":"` + nowRFC3339 + `",
				"mergedAt":null,
				"closedAt":null,
				"additions":1,
				"deletions":0,
				"mergeable":"MERGEABLE",
				"reviewDecision":"",
				"headRefName":"` + headRef + `",
				"baseRefName":"main",
				"headRefOid":"` + headSHA + `",
				"baseRefOid":"def456",
				"headRepository":{"url":"https://github.com/` + forkFullName + `"},
				"labels":{"nodes":[]},
				"comments":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}},
				"reviews":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}},
				"allCommits":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}},
				"lastCommit":{"nodes":[]},
				"timelineItems":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}
			}],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`
			_, _ = w.Write([]byte(resp))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"repository":{"issues":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`))
	}))
	defer gqlSrv.Close()

	prTime := gh.Timestamp{Time: now}
	mock := &serverfake.MockGH{
		ListOpenPullRequestsFn: func(_ context.Context, _, _ string) ([]*gh.PullRequest, error) {
			return []*gh.PullRequest{{
				ID:        new(prID),
				Number:    new(prNumber),
				Title:     new("Fork PR awaiting workflow approval"),
				State:     new("open"),
				HTMLURL:   new("https://github.com/acme/widget/pull/174"),
				User:      &gh.User{Login: new("ericdill")},
				CreatedAt: &prTime,
				UpdatedAt: &prTime,
				Head:      &gh.PullRequestBranch{Ref: new(headRef), SHA: new(headSHA)},
				Base:      &gh.PullRequestBranch{Ref: new("main")},
			}}, nil
		},
		ListOpenIssuesFn: func(_ context.Context, _, _ string) ([]*gh.Issue, error) {
			return nil, &gh.ErrorResponse{
				Response: &http.Response{StatusCode: http.StatusNotModified},
			}
		},
		ListWorkflowRunsForHeadFn: func(_ context.Context, _, _, sha string) ([]*gh.WorkflowRun, error) {
			require.Equal(headSHA, sha)
			// Fork-triggered runs return an empty PullRequests array.
			// The matcher must fall back to HeadRepository.FullName +
			// HeadBranch, identifying the PR via its persisted clone
			// URL.
			fullName := forkFullName
			branch := headRef
			return []*gh.WorkflowRun{{
				ID:             new(int64(7778)),
				HeadSHA:        new(headSHA),
				Event:          new("pull_request"),
				PullRequests:   nil,
				HeadRepository: &gh.Repository{FullName: &fullName},
				HeadBranch:     &branch,
			}}, nil
		},
	}

	srv, _, syncer := setupTestServerWithMock(t, mock)
	gqlClient := githubv4.NewEnterpriseClient(gqlSrv.URL, gqlSrv.Client())
	syncer.SetFetchers(map[string]*ghclient.GraphQLFetcher{
		"github.com": ghclient.NewGraphQLFetcherWithClient(gqlClient, nil),
	})
	client := servertest.SetupTestClient(t, srv)

	syncer.RunOnce(ctx)

	resp, err := client.HTTP.GetPullWithResponse(ctx, &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(int64(prNumber))}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.NotNil(resp.JSON200.WorkflowApproval)
	assert.True(resp.JSON200.WorkflowApproval.Checked,
		"GraphQL bulk path should persist a workflow approval snapshot")
	assert.True(resp.JSON200.WorkflowApproval.Required,
		"fork PR must match via HeadRepository.FullName + HeadBranch fallback")
	assert.Equal(int64(1), resp.JSON200.WorkflowApproval.Count)
}

func TestE2EGraphQLBulkSyncKeepsNewestCICheckBySuiteCreatedAt(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := context.Background()

	older := time.Date(2026, 4, 9, 12, 0, 0, 0, time.UTC)
	newer := older.Add(10 * time.Minute)
	prUpdatedAt := newer.Add(time.Minute)
	prID := int64(173100)
	prNumber := 174
	prTitle := "GraphQL check dedupe"
	prState := "open"
	prURL := "https://github.com/acme/widget/pull/174"
	prAuthor := "alice"
	headRef := "feature/graphql-check-dedupe"
	baseRef := "main"
	headSHA := "abc123"
	baseSHA := "def456"
	checkName := "build"
	oldCheckURL := "https://ci.example.com/runs/old"
	newCheckURL := "https://ci.example.com/runs/new"

	gqlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if bytes.Contains(body, []byte("pullRequests")) {
			resp := `{"data":{"repository":{"pullRequests":{"nodes":[{
				"databaseId":173100,
				"number":174,
				"title":"GraphQL check dedupe",
				"state":"OPEN",
				"isDraft":false,
				"body":"",
				"url":"https://github.com/acme/widget/pull/174",
				"author":{"login":"alice"},
				"createdAt":"` + older.Format(time.RFC3339) + `",
				"updatedAt":"` + prUpdatedAt.Format(time.RFC3339) + `",
				"mergedAt":null,
				"closedAt":null,
				"additions":1,
				"deletions":0,
				"mergeable":"MERGEABLE",
				"reviewDecision":"",
				"headRefName":"feature/graphql-check-dedupe",
				"baseRefName":"main",
				"headRefOid":"abc123",
				"baseRefOid":"def456",
				"headRepository":{"url":"https://github.com/acme/widget"},
				"labels":{"nodes":[]},
				"comments":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}},
				"reviews":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}},
				"allCommits":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}},
				"lastCommit":{"nodes":[{"commit":{"statusCheckRollup":{"contexts":{"nodes":[
					{"__typename":"CheckRun","name":"build","status":"COMPLETED","conclusion":"FAILURE","detailsUrl":"https://ci.example.com/runs/old","startedAt":null,"completedAt":null,"checkSuite":{"createdAt":"` + older.Format(time.RFC3339) + `","app":{"name":"GitHub Actions"}}},
					{"__typename":"CheckRun","name":"build","status":"COMPLETED","conclusion":"SUCCESS","detailsUrl":"https://ci.example.com/runs/new","startedAt":null,"completedAt":null,"checkSuite":{"createdAt":"` + newer.Format(time.RFC3339) + `","app":{"name":"GitHub Actions"}}}
				],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}]}
			}],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`
			_, _ = w.Write([]byte(resp))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"repository":{"issues":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`))
	}))
	defer gqlSrv.Close()

	prTime := gh.Timestamp{Time: prUpdatedAt}
	mock := &serverfake.MockGH{
		ListOpenPullRequestsFn: func(_ context.Context, _, _ string) ([]*gh.PullRequest, error) {
			return []*gh.PullRequest{{
				ID:        &prID,
				Number:    &prNumber,
				Title:     &prTitle,
				State:     &prState,
				HTMLURL:   &prURL,
				User:      &gh.User{Login: &prAuthor},
				CreatedAt: &gh.Timestamp{Time: older},
				UpdatedAt: &prTime,
				Head:      &gh.PullRequestBranch{Ref: &headRef, SHA: &headSHA},
				Base:      &gh.PullRequestBranch{Ref: &baseRef, SHA: &baseSHA},
			}}, nil
		},
		ListOpenIssuesFn: func(_ context.Context, _, _ string) ([]*gh.Issue, error) {
			return nil, &gh.ErrorResponse{
				Response: &http.Response{StatusCode: http.StatusNotModified},
			}
		},
	}

	srv, _, syncer := setupTestServerWithMock(t, mock)
	gqlClient := githubv4.NewEnterpriseClient(gqlSrv.URL, gqlSrv.Client())
	syncer.SetFetchers(map[string]*ghclient.GraphQLFetcher{
		"github.com": ghclient.NewGraphQLFetcherWithClient(gqlClient, nil),
	})
	client := servertest.SetupTestClient(t, srv)

	syncer.RunOnce(ctx)

	resp, err := client.HTTP.GetPullWithResponse(ctx, &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(int64(prNumber))}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.NotNil(resp.JSON200.MergeRequest)
	require.Equal("success", resp.JSON200.MergeRequest.CIStatus)

	var checks []db.CICheck
	require.NoError(json.Unmarshal(
		[]byte(resp.JSON200.MergeRequest.CIChecksJSON),
		&checks,
	))
	require.Len(checks, 1)
	assert.Equal(checkName, checks[0].Name)
	assert.Equal("completed", checks[0].Status)
	assert.Equal("success", checks[0].Conclusion)
	assert.Equal(newCheckURL, checks[0].URL)
	assert.Equal("GitHub Actions", checks[0].App)
	assert.NotEqual(oldCheckURL, checks[0].URL)
}

func TestAPISyncPRIncrementsRequestCount(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)

	database := dbtest.Open(t)

	rt := ghclient.NewRateTracker(database, "github.com", "host", "rest")

	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": &serverfake.MockGH{}},
		database, nil,
		[]ghclient.RepoRef{{
			Owner: "acme", Name: "widget",
			PlatformHost: "github.com",
		}},
		time.Minute,
		map[string]*ghclient.RateTracker{"github.com": rt},
		nil,
	)
	t.Cleanup(syncer.Stop)

	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	ts := httptest.NewServer(srv)
	defer ts.Close()

	// Before any requests: requests_hour should be 0.
	resp, err := http.Get(ts.URL + "/api/v1/rate-limits")
	require.NoError(err)
	defer resp.Body.Close()
	assert.Equal(200, resp.StatusCode)

	var before itemapi.RateLimitsResponse
	err = json.NewDecoder(resp.Body).Decode(&before)
	require.NoError(err)

	gh0, ok := before.ProviderPools["github.com"]
	assert.True(ok)
	assert.Equal(0, gh0.REST.Requests)

	// Simulate 5 API calls via RecordRequest.
	for range 5 {
		rt.RecordRequest()
	}

	// After recording: requests_hour should be 5.
	resp2, err := http.Get(ts.URL + "/api/v1/rate-limits")
	require.NoError(err)
	defer resp2.Body.Close()
	assert.Equal(200, resp2.StatusCode)

	var after itemapi.RateLimitsResponse
	err = json.NewDecoder(resp2.Body).Decode(&after)
	require.NoError(err)

	gh5, ok := after.ProviderPools["github.com"]
	assert.True(ok)
	assert.Equal(5, gh5.REST.Requests)
}

func TestAPIGetRepoCommitDiff(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)

	_, _, _, _, commitSHAs, srv := servertest.SetupTestServerWithClonesAndServer(t)
	req := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/repo/gh/acme/widget/commits/"+commitSHAs[2]+"/diff",
		nil,
	)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	resp := rr.Result()
	defer resp.Body.Close()

	require.Equal(http.StatusOK, resp.StatusCode)
	var body itemapi.DiffResponse
	require.NoError(json.NewDecoder(resp.Body).Decode(&body))
	require.Len(body.Files, 1)
	assert.False(body.Stale)
	assert.Equal("file3.txt", body.Files[0].Path)
	assert.Equal("added", body.Files[0].Status)
	require.NotEmpty(body.Files[0].Hunks)
	require.NotEmpty(body.Files[0].Hunks[0].Lines)
	assert.Equal("content 3", body.Files[0].Hunks[0].Lines[0].Content)
}

// TestAPIStacks_DetectionViaSyncHook exercises the production wiring:
// SetOnSyncCompleted(stacks.SyncCompletedHook) fires after RunOnce and
// populates stacks without calling RunDetection directly. Verifies that
// GET /stacks and GET /repos/{owner}/{name}/pulls/{number}/stack return
// data produced entirely by the sync-completion callback path.
func TestAPIStacks_DetectionViaSyncHook(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()

	// Build GitHub PRs the mock will return; the sync will persist these
	// into DB as open PRs forming a linear chain.
	now := time.Now().UTC().Truncate(time.Second)
	stringPtr := func(s string) *string { return &s }
	repoCloneURL := "https://github.com/acme/widget.git"
	makeGHPR := func(id int64, number int, head, base string) *gh.PullRequest {
		sha := fmt.Sprintf("sha%d", number)
		title := fmt.Sprintf("PR #%d: %s", number, head)
		return &gh.PullRequest{
			ID:        &id,
			Number:    &number,
			State:     stringPtr("open"),
			Title:     &title,
			Body:      stringPtr(""),
			User:      &gh.User{Login: stringPtr("testuser")},
			CreatedAt: &gh.Timestamp{Time: now},
			UpdatedAt: &gh.Timestamp{Time: now},
			Head: &gh.PullRequestBranch{
				Ref:  &head,
				SHA:  &sha,
				Repo: &gh.Repository{CloneURL: &repoCloneURL},
			},
			Base: &gh.PullRequestBranch{Ref: &base, SHA: stringPtr("basesha")},
		}
	}
	mock := &serverfake.MockGH{
		GetRepositoryFn: func(_ context.Context, owner, repo string) (*gh.Repository, error) {
			nodeID := "repo-" + owner + "-" + repo
			return &gh.Repository{
				Name:     &repo,
				NodeID:   &nodeID,
				Owner:    &gh.User{Login: &owner},
				CloneURL: &repoCloneURL,
				Archived: new(false),
			}, nil
		},
		ListOpenPullRequestsFn: func(_ context.Context, _, _ string) ([]*gh.PullRequest, error) {
			return []*gh.PullRequest{
				makeGHPR(1001, 10, "feat/hook-base", "main"),
				makeGHPR(1011, 11, "feat/hook-tip", "feat/hook-base"),
			}, nil
		},
	}
	srv, database, syncer := setupTestServerWithMock(t, mock)
	client := servertest.SetupTestClient(t, srv)

	// Wire the production hook and run one sync pass. RunOnce will fetch
	// from the mock, persist PRs into DB, then invoke OnSyncCompleted,
	// which runs stack detection.
	syncer.SetOnSyncCompleted(stacks.SyncCompletedHook(ctx, database, nil))
	syncer.RunOnce(ctx)

	// Stacks should be populated purely by the hook path.
	listResp, err := client.HTTP.ListStacksWithResponse(ctx, &generated.ListStacksRequestOptions{Query: &generated.ListStacksQuery{}})
	require.NoError(err)
	require.Equal(http.StatusOK, listResp.StatusCode)
	var stks []generated.StackResponse
	require.NoError(json.Unmarshal(listResp.Body, &stks))
	require.Len(stks, 1, "sync-hook detection should produce one stack")
	assert.Equal("hook", stks[0].Name)

	ctxResp, err := client.HTTP.GetPullStackWithResponse(ctx, &generated.GetPullStackRequestOptions{PathParams: &generated.GetPullStackPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(10)}})
	require.NoError(err)
	require.Equal(http.StatusOK, ctxResp.StatusCode)
	require.NotNil(ctxResp.JSON200)
	assert.Equal("hook", ctxResp.JSON200.StackName)
	assert.Equal(int64(2), ctxResp.JSON200.Size)
}

func TestAPIStacks_DetectionViaSyncHookPrefersGitHubNativeOrder(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)
	repoCloneURL := "https://github.com/acme/widget.git"
	makeGHPR := func(id int64, number int, head, base string) *gh.PullRequest {
		sha := fmt.Sprintf("sha%d", number)
		title := fmt.Sprintf("PR #%d", number)
		return &gh.PullRequest{
			ID: &id, Number: &number, State: new("open"), Title: &title,
			Body: new(""), User: &gh.User{Login: new("testuser")},
			CreatedAt: &gh.Timestamp{Time: now}, UpdatedAt: &gh.Timestamp{Time: now},
			Head: &gh.PullRequestBranch{
				Ref: &head, SHA: &sha,
				Repo: &gh.Repository{CloneURL: &repoCloneURL},
			},
			Base: &gh.PullRequestBranch{Ref: &base, SHA: new("basesha")},
		}
	}
	prs := []*gh.PullRequest{
		makeGHPR(1001, 10, "feat/base", "main"),
		makeGHPR(1011, 11, "feat/tip", "feat/base"),
	}
	mock := &serverfake.MockGH{
		GetRepositoryFn: func(_ context.Context, owner, repo string) (*gh.Repository, error) {
			nodeID := "repo-" + owner + "-" + repo
			return &gh.Repository{
				Name: &repo, NodeID: &nodeID, Owner: &gh.User{Login: &owner},
				CloneURL: &repoCloneURL, Archived: new(false),
			}, nil
		},
		NativeStackAPI: &serverfake.MockGHNativeStackAPI{
			ListOpenPullRequests: func(
				context.Context, string, string,
			) ([]*gh.PullRequest, map[int]*platformgithub.NativeStackHint, error) {
				return prs, map[int]*platformgithub.NativeStackHint{
					10: {Number: 42, Size: 2, Position: 2, BaseRef: "main"},
					11: {Number: 42, Size: 2, Position: 1, BaseRef: "main"},
				}, nil
			},
			ListStackPage: func(
				context.Context, string, string, int,
			) (platformgithub.NativeStackPage, error) {
				return platformgithub.NativeStackPage{Stacks: []platformgithub.NativeStack{{
					ID: 9001, Number: 42, BaseRef: "main", Open: true, CreatedAt: now,
					Members: []platformgithub.NativeStackMember{
						{Position: 1, PullRequestNumber: 11, State: "open", HeadRef: "feat/tip", HeadSHA: "sha11"},
						{Position: 2, PullRequestNumber: 10, State: "open", HeadRef: "feat/base", HeadSHA: "sha10"},
					},
				}}}, nil
			},
		},
	}
	srv, database, syncer := setupTestServerWithMock(t, mock)
	client := servertest.SetupTestClient(t, srv)
	syncer.SetPreferGitHubNativeStacks(true)
	syncer.SetOnSyncCompleted(stacks.SyncCompletedHook(ctx, database, nil))
	syncer.RunOnce(ctx)

	stackResp, err := client.HTTP.GetPullStackWithResponse(ctx, &generated.GetPullStackRequestOptions{PathParams: &generated.GetPullStackPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(10)}})
	require.NoError(err)
	require.Equal(http.StatusOK, stackResp.StatusCode, string(stackResp.Body))
	require.NotNil(stackResp.JSON200)
	require.NotNil(stackResp.JSON200.Members)
	assert.Equal([]int64{11, 10}, serverfake.StackMemberNumbers(stackResp.JSON200.Members))

	detailResp, err := client.HTTP.GetPullWithResponse(ctx, &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(10)}})
	require.NoError(err)
	require.Equal(http.StatusOK, detailResp.StatusCode, string(detailResp.Body))
	require.NotNil(detailResp.JSON200)
	require.NotNil(detailResp.JSON200.Stack)
	assert.Equal(int64(2), detailResp.JSON200.Stack.Position)
}

func TestAPIStacks_DetectionViaSyncHookIgnoresForkHeadBranchCollision(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()

	now := time.Now().UTC().Truncate(time.Second)
	stringPtr := func(s string) *string { return &s }
	repoCloneURL := "https://github.com/acme/widget.git"
	forkCloneURL := "https://github.com/fork/widget.git"
	makeGHPR := func(id int64, number int, head, base, headRepoCloneURL string) *gh.PullRequest {
		sha := fmt.Sprintf("sha%d", number)
		title := fmt.Sprintf("PR #%d: %s", number, head)
		return &gh.PullRequest{
			ID:        &id,
			Number:    &number,
			State:     stringPtr("open"),
			Title:     &title,
			Body:      stringPtr(""),
			User:      &gh.User{Login: stringPtr("testuser")},
			CreatedAt: &gh.Timestamp{Time: now},
			UpdatedAt: &gh.Timestamp{Time: now},
			Head: &gh.PullRequestBranch{
				Ref:  &head,
				SHA:  &sha,
				Repo: &gh.Repository{CloneURL: &headRepoCloneURL},
			},
			Base: &gh.PullRequestBranch{Ref: &base, SHA: stringPtr("basesha")},
		}
	}
	mock := &serverfake.MockGH{
		GetRepositoryFn: func(_ context.Context, owner, repo string) (*gh.Repository, error) {
			nodeID := "repo-" + owner + "-" + repo
			return &gh.Repository{
				Name:     &repo,
				NodeID:   &nodeID,
				Owner:    &gh.User{Login: &owner},
				CloneURL: &repoCloneURL,
				Archived: new(false),
			}, nil
		},
		ListOpenPullRequestsFn: func(_ context.Context, _, _ string) ([]*gh.PullRequest, error) {
			return []*gh.PullRequest{
				makeGHPR(9001, 90, "feature/auth", "main", forkCloneURL),
				makeGHPR(1001, 100, "feature/auth", "main", repoCloneURL),
				makeGHPR(1011, 101, "feature/auth-ui", "feature/auth", repoCloneURL),
			}, nil
		},
	}
	srv, database, syncer := setupTestServerWithRepos(t, mock, []ghclient.RepoRef{{
		Owner:        "acme",
		Name:         "widget",
		PlatformHost: "github.com",
		CloneURL:     repoCloneURL,
	}})
	client := servertest.SetupTestClient(t, srv)

	syncer.SetOnSyncCompleted(stacks.SyncCompletedHook(ctx, database, nil))
	syncer.RunOnce(ctx)

	ctxResp, err := client.HTTP.GetPullStackWithResponse(ctx, &generated.GetPullStackRequestOptions{PathParams: &generated.GetPullStackPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(101)}})
	require.NoError(err)
	require.Equal(http.StatusOK, ctxResp.StatusCode, string(ctxResp.Body))
	require.NotNil(ctxResp.JSON200)
	require.NotNil(ctxResp.JSON200.Members)
	assert.Equal([]int64{100, 101}, serverfake.StackMemberNumbers(ctxResp.JSON200.Members))

	forkResp, err := client.HTTP.GetPullWithResponse(ctx, &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(90)}})
	require.NoError(err)
	require.Equal(http.StatusOK, forkResp.StatusCode, string(forkResp.Body))
	require.NotNil(forkResp.JSON200)
	assert.Nil(forkResp.JSON200.Stack)
}

func TestAPIStacks_DetectionViaSyncHookIgnoresSameRepoSelfEdge(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()

	now := time.Now().UTC().Truncate(time.Second)
	stringPtr := func(s string) *string { return &s }
	repoCloneURL := "https://github.com/acme/widget.git"
	makeGHPR := func(id int64, number int, head, base string) *gh.PullRequest {
		sha := fmt.Sprintf("sha%d", number)
		title := fmt.Sprintf("PR #%d: %s", number, head)
		return &gh.PullRequest{
			ID:        &id,
			Number:    &number,
			State:     stringPtr("open"),
			Title:     &title,
			Body:      stringPtr(""),
			User:      &gh.User{Login: stringPtr("testuser")},
			CreatedAt: &gh.Timestamp{Time: now},
			UpdatedAt: &gh.Timestamp{Time: now},
			Head: &gh.PullRequestBranch{
				Ref:  &head,
				SHA:  &sha,
				Repo: &gh.Repository{CloneURL: &repoCloneURL},
			},
			Base: &gh.PullRequestBranch{Ref: &base, SHA: stringPtr("basesha")},
		}
	}
	mock := &serverfake.MockGH{
		GetRepositoryFn: func(_ context.Context, owner, repo string) (*gh.Repository, error) {
			nodeID := "repo-" + owner + "-" + repo
			return &gh.Repository{
				Name:     &repo,
				NodeID:   &nodeID,
				Owner:    &gh.User{Login: &owner},
				CloneURL: &repoCloneURL,
				Archived: new(false),
			}, nil
		},
		ListOpenPullRequestsFn: func(_ context.Context, _, _ string) ([]*gh.PullRequest, error) {
			return []*gh.PullRequest{
				makeGHPR(4491, 449, "legacy-parser-base", "legacy-parser-base"),
				makeGHPR(7481, 748, "locate-parser-interface", "legacy-parser-base"),
				makeGHPR(7511, 751, "provider-facade-core", "locate-parser-interface"),
			}, nil
		},
	}
	srv, database, syncer := setupTestServerWithRepos(t, mock, []ghclient.RepoRef{{
		Owner:        "acme",
		Name:         "widget",
		PlatformHost: "github.com",
		CloneURL:     repoCloneURL,
	}})
	client := servertest.SetupTestClient(t, srv)

	syncer.SetOnSyncCompleted(stacks.SyncCompletedHook(ctx, database, nil))
	syncer.RunOnce(ctx)

	ctxResp, err := client.HTTP.GetPullStackWithResponse(ctx, &generated.GetPullStackRequestOptions{PathParams: &generated.GetPullStackPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(751)}})
	require.NoError(err)
	require.Equal(http.StatusOK, ctxResp.StatusCode, string(ctxResp.Body))
	require.NotNil(ctxResp.JSON200)
	require.NotNil(ctxResp.JSON200.Members)
	assert.Equal([]int64{748, 751}, serverfake.StackMemberNumbers(ctxResp.JSON200.Members))

	selfEdgeResp, err := client.HTTP.GetPullWithResponse(ctx, &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(449)}})
	require.NoError(err)
	require.Equal(http.StatusOK, selfEdgeResp.StatusCode, string(selfEdgeResp.Body))
	require.NotNil(selfEdgeResp.JSON200)
	assert.Nil(selfEdgeResp.JSON200.Stack)
}
