package pullreviewservertest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
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
	"go.kenn.io/forge/internal/server/authapi"
	"go.kenn.io/forge/internal/server/itemapi"
	"go.kenn.io/forge/internal/server/operationapi"
	"go.kenn.io/forge/internal/server/pullapi"
	"go.kenn.io/forge/internal/stacks"
	"go.kenn.io/forge/internal/testutil"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/platform"

	platformgithub "go.kenn.io/forge/platform/github"

	platformgitlab "go.kenn.io/forge/platform/gitlab"
)

func TestMain(m *testing.M) {
	os.Exit(serverfake.RunMain(m))
}

// setupTestServer opens a temp DB, builds a Server, and returns both.
func setupTestServer(t *testing.T) (*server.Server, *db.DB, *ghclient.Syncer) {
	t.Helper()
	return setupTestServerWithMock(t, &serverfake.MockGH{})
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

func TestAPIGitHubSyncPersistsReviewThreadsThroughPullDetail(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	now := time.Date(2026, 5, 27, 16, 1, 31, 0, time.UTC)
	reviewUpdatedAt := now.Add(time.Minute)
	providerUpdatedAt := now.Add(2 * time.Minute)
	prNumber := 42
	line := 1
	headSHA := "head-sha"
	commentCommitSHA := "comment-sha"
	baseSHA := "base-sha"
	mock := &serverfake.MockGH{
		GetPullRequestFn: func(_ context.Context, _, _ string, number int) (*gh.PullRequest, error) {
			require.Equal(prNumber, number)
			prID := int64(9001)
			prNodeID := "PR_kwDO123"
			title := "inline review"
			state := "open"
			url := "https://github.com/acme/widget/pull/42"
			author := "ada"
			headRef := "feature"
			baseRef := "main"
			return &gh.PullRequest{
				ID:        &prID,
				NodeID:    &prNodeID,
				Number:    &number,
				HTMLURL:   &url,
				Title:     &title,
				State:     &state,
				User:      &gh.User{Login: &author},
				CreatedAt: &gh.Timestamp{Time: now},
				UpdatedAt: &gh.Timestamp{Time: providerUpdatedAt},
				Head: &gh.PullRequestBranch{
					Ref: &headRef,
					SHA: &headSHA,
				},
				Base: &gh.PullRequestBranch{
					Ref: &baseRef,
					SHA: &baseSHA,
				},
			}, nil
		},
		ListIssueCommentsFn: func(context.Context, string, string, int) ([]*gh.IssueComment, error) {
			return nil, nil
		},
		ListReviewThreadsFn: func(context.Context, string, string, int) ([]platformgithub.PullRequestReviewThread, error) {
			return []platformgithub.PullRequestReviewThread{{
				NodeID:     "PRRT_1",
				IsOutdated: false,
				Path:       ".golangci.yml",
				Side:       "RIGHT",
				Line:       line,
				Comments: []platformgithub.PullRequestReviewThreadComment{{
					NodeID:           "PRRC_1",
					DatabaseID:       3312100450,
					ReviewDatabaseID: 4373946198,
					Body:             "inline note",
					AuthorLogin:      "reviewer",
					CommitID:         commentCommitSHA,
					IsMinimized:      true,
					MinimizedReason:  "OFF_TOPIC",
					CreatedAt:        now,
					UpdatedAt:        reviewUpdatedAt,
				}},
			}}, nil
		},
	}
	srv, _, syncer := setupTestServerWithMock(t, mock)
	client := servertest.SetupTestClient(t, srv)

	require.NoError(syncer.SyncMR(ctx, "acme", "widget", prNumber))

	resp, err := client.HTTP.GetPullWithResponse(ctx, &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(int64(prNumber))}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.NotNil(resp.JSON200.Events)
	assert.Equal(providerUpdatedAt, resp.JSON200.MergeRequest.LastActivityAt)
	require.Len(resp.JSON200.Events, 1)
	event := resp.JSON200.Events[0]
	assert.Equal("review_comment", event.EventType)
	assert.JSONEq(`{"provider_hidden":true,"provider_hidden_reason":"OFF_TOPIC"}`, event.MetadataJSON)
	assert.Equal("3312100450", event.PlatformExternalID)
	require.NotNil(event.ThreadID)
	assert.Equal("PRRT_1", *event.ThreadID)
	require.NotNil(event.DiffThread)
	assert.Equal(".golangci.yml", event.DiffThread.Path)
	assert.Equal("right", event.DiffThread.Side)
	assert.Equal(int64(line), event.DiffThread.Line)
	assert.Equal("inline note", event.DiffThread.Body)
	require.NotNil(event.DiffThread.MetadataJSON)
	assert.JSONEq(`{"provider_hidden":true,"provider_hidden_reason":"OFF_TOPIC"}`, *event.DiffThread.MetadataJSON)
	require.NotNil(event.DiffThread.DiffHeadSha)
	assert.Equal(commentCommitSHA, *event.DiffThread.DiffHeadSha)
	require.NotNil(event.DiffThread.CommitSha)
	assert.Equal(commentCommitSHA, *event.DiffThread.CommitSha)
	require.NotNil(event.DiffThread.ProviderCommentID)
	assert.Equal("3312100450", *event.DiffThread.ProviderCommentID)
}

func TestAPICommentAutocomplete(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	srv, database, _ := setupTestServer(t)
	ctx := t.Context()

	repoID, err := database.UpsertRepo(ctx, serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	prID, err := database.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID:         repoID,
		PlatformID:     12000,
		Number:         12,
		URL:            "https://github.com/acme/widget/pull/12",
		Title:          "Polish mentions",
		Author:         "alice",
		State:          "open",
		HeadBranch:     "feature-12",
		BaseBranch:     "main",
		CreatedAt:      time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Second),
		UpdatedAt:      time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Second),
		LastActivityAt: time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Second),
	})
	require.NoError(err)
	require.NoError(database.EnsureKanbanState(ctx, prID))
	_, err = database.UpsertIssue(ctx, &db.Issue{
		RepoID:         repoID,
		PlatformID:     17000,
		Number:         17,
		URL:            "https://github.com/acme/widget/issues/17",
		Title:          "Mention bug",
		Author:         "alex",
		State:          "open",
		CreatedAt:      time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second),
		UpdatedAt:      time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second),
		LastActivityAt: time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second),
	})
	require.NoError(err)
	require.NoError(database.UpsertMREvents(ctx, []db.MREvent{{
		MergeRequestID: prID,
		EventType:      "comment",
		Author:         "albert",
		CreatedAt:      time.Now().UTC().Add(-time.Hour).Truncate(time.Second),
		DedupeKey:      "autocomplete-mr-comment",
	}}))

	userReq := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/repo/gh/acme/widget/comment-autocomplete?trigger=@&q=al&limit=10", nil)
	userRR := httptest.NewRecorder()
	srv.ServeHTTP(userRR, userReq)
	require.Equal(http.StatusOK, userRR.Code, userRR.Body.String())

	var userBody itemapi.CommentAutocompleteResponse
	require.NoError(json.NewDecoder(userRR.Body).Decode(&userBody))
	assert.Equal([]string{"albert", "alex", "alice"}, userBody.Users)
	assert.Empty(userBody.References)

	// Naming the target item promotes its author and participants ahead of
	// the recency ordering.
	itemReq := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/repo/gh/acme/widget/comment-autocomplete?trigger=@&q=al&limit=10&item_type=issue&item_number=17", nil)
	itemRR := httptest.NewRecorder()
	srv.ServeHTTP(itemRR, itemReq)
	require.Equal(http.StatusOK, itemRR.Code, itemRR.Body.String())
	var itemBody itemapi.CommentAutocompleteResponse
	require.NoError(json.NewDecoder(itemRR.Body).Decode(&itemBody))
	assert.Equal([]string{"alex", "albert", "alice"}, itemBody.Users)

	halfReq := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/repo/gh/acme/widget/comment-autocomplete?trigger=@&q=al&item_number=17", nil)
	halfRR := httptest.NewRecorder()
	srv.ServeHTTP(halfRR, halfReq)
	assert.Equal(http.StatusBadRequest, halfRR.Code, halfRR.Body.String())

	refReq := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/repo/gh/acme/widget/comment-autocomplete?trigger=%23&q=1&limit=10", nil)
	refRR := httptest.NewRecorder()
	srv.ServeHTTP(refRR, refReq)
	require.Equal(http.StatusOK, refRR.Code, refRR.Body.String())

	var refBody itemapi.CommentAutocompleteResponse
	require.NoError(json.NewDecoder(refRR.Body).Decode(&refBody))
	assert.Equal([]db.CommentAutocompleteReference{
		{Kind: "issue", Number: 17, Title: "Mention bug", State: "open"},
		{Kind: "pull", Number: 12, Title: "Polish mentions", State: "open"},
	}, refBody.References)
	assert.Empty(refBody.Users)

	bangReq := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/repo/gh/acme/widget/comment-autocomplete?trigger=!&q=1&limit=10", nil)
	bangRR := httptest.NewRecorder()
	srv.ServeHTTP(bangRR, bangReq)
	assert.Equal(http.StatusBadRequest, bangRR.Code, bangRR.Body.String())

	gitlabRepoID, err := database.UpsertRepo(ctx, db.RepoIdentity{
		Platform:       "gitlab",
		PlatformHost:   "gitlab.example.com",
		PlatformRepoID: "gid://gitlab/Project/42",
		Owner:          "group",
		Name:           "project",
	})
	require.NoError(err)
	_, err = database.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID:         gitlabRepoID,
		PlatformID:     12001,
		Number:         12,
		URL:            "https://gitlab.example.com/group/project/-/merge_requests/12",
		Title:          "Polish merge request mentions",
		Author:         "alice",
		State:          "open",
		HeadBranch:     "feature-12",
		BaseBranch:     "main",
		CreatedAt:      time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Second),
		UpdatedAt:      time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Second),
		LastActivityAt: time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Second),
	})
	require.NoError(err)
	_, err = database.UpsertIssue(ctx, &db.Issue{
		RepoID:         gitlabRepoID,
		PlatformID:     17001,
		Number:         17,
		URL:            "https://gitlab.example.com/group/project/-/issues/17",
		Title:          "Mention issue",
		Author:         "alex",
		State:          "open",
		CreatedAt:      time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second),
		UpdatedAt:      time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second),
		LastActivityAt: time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second),
	})
	require.NoError(err)

	gitlabIssueReq := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/host/gitlab.example.com/repo/gitlab/group/project/comment-autocomplete?trigger=%23&q=1&limit=10", nil)
	gitlabIssueRR := httptest.NewRecorder()
	srv.ServeHTTP(gitlabIssueRR, gitlabIssueReq)
	require.Equal(http.StatusOK, gitlabIssueRR.Code, gitlabIssueRR.Body.String())

	var gitlabIssueBody itemapi.CommentAutocompleteResponse
	require.NoError(json.NewDecoder(gitlabIssueRR.Body).Decode(&gitlabIssueBody))
	assert.Equal([]db.CommentAutocompleteReference{
		{Kind: "issue", Number: 17, Title: "Mention issue", State: "open"},
	}, gitlabIssueBody.References)

	gitlabMRReq := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/host/gitlab.example.com/repo/gitlab/group/project/comment-autocomplete?trigger=!&q=1&limit=10", nil)
	gitlabMRRR := httptest.NewRecorder()
	srv.ServeHTTP(gitlabMRRR, gitlabMRReq)
	require.Equal(http.StatusOK, gitlabMRRR.Code, gitlabMRRR.Body.String())

	var gitlabMRBody itemapi.CommentAutocompleteResponse
	require.NoError(json.NewDecoder(gitlabMRRR.Body).Decode(&gitlabMRBody))
	assert.Equal([]db.CommentAutocompleteReference{
		{Kind: "pull", Number: 12, Title: "Polish merge request mentions", State: "open"},
	}, gitlabMRBody.References)
}

func TestAPICommentAutocompleteUsesRepoPlatformHost(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	srv, database, _ := setupTestServer(t)
	ctx := t.Context()

	githubRepoID, err := database.UpsertRepo(ctx, serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	_, err = database.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID:         githubRepoID,
		PlatformID:     12001,
		Number:         12,
		URL:            "https://github.com/acme/widget/pull/12",
		Title:          "Wrong host mention",
		Author:         "alice",
		State:          "open",
		HeadBranch:     "feature-12",
		BaseBranch:     "main",
		CreatedAt:      time.Now().UTC().Add(-4 * time.Hour).Truncate(time.Second),
		UpdatedAt:      time.Now().UTC().Add(-4 * time.Hour).Truncate(time.Second),
		LastActivityAt: time.Now().UTC().Add(-4 * time.Hour).Truncate(time.Second),
	})
	require.NoError(err)

	repoID, err := database.UpsertRepo(ctx, serverfake.VerifiedGitHubRepoIdentity("ghe.example.com", "acme", "widget"))
	require.NoError(err)
	_, err = database.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID:         repoID,
		PlatformID:     12000,
		Number:         12,
		URL:            "https://ghe.example.com/acme/widget/pull/12",
		Title:          "Polish mentions",
		Author:         "alice",
		State:          "open",
		HeadBranch:     "feature-12",
		BaseBranch:     "main",
		CreatedAt:      time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Second),
		UpdatedAt:      time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Second),
		LastActivityAt: time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Second),
	})
	require.NoError(err)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/host/ghe.example.com/repo/gh/acme/widget/comment-autocomplete?trigger=%23&q=1&limit=10", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())

	var body itemapi.CommentAutocompleteResponse
	require.NoError(json.NewDecoder(rr.Body).Decode(&body))
	assert.Equal([]db.CommentAutocompleteReference{{Kind: "pull", Number: 12, Title: "Polish mentions", State: "open"}}, body.References)
}

func TestAPICommentAutocompleteReferencesScopesByProvider(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	srv, database, _ := setupTestServer(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)

	githubRepoID, err := database.UpsertRepo(ctx, db.RepoIdentity{
		Platform:       "github",
		PlatformHost:   "github.com",
		PlatformRepoID: "repo-github-widget",
		Owner:          "acme",
		Name:           "widget",
		RepoPath:       "acme/widget",
	})
	require.NoError(err)
	giteaRepoID, err := database.UpsertRepo(ctx, db.RepoIdentity{
		Platform:       "gitea",
		PlatformHost:   "github.com",
		PlatformRepoID: "repo-gitea-widget",
		Owner:          "acme",
		Name:           "widget",
		RepoPath:       "acme/widget",
	})
	require.NoError(err)

	_, err = database.UpsertIssue(ctx, &db.Issue{
		RepoID:         githubRepoID,
		PlatformID:     17001,
		Number:         1,
		URL:            "https://github.com/acme/widget/issues/1",
		Title:          "Provider collision issue",
		Author:         "alice",
		State:          "open",
		CreatedAt:      now.Add(-2 * time.Hour),
		UpdatedAt:      now.Add(-2 * time.Hour),
		LastActivityAt: now.Add(-2 * time.Hour),
	})
	require.NoError(err)
	_, err = database.UpsertIssue(ctx, &db.Issue{
		RepoID:         giteaRepoID,
		PlatformID:     17901,
		Number:         901,
		URL:            "https://github.com/acme/widget/issues/901",
		Title:          "Provider collision issue",
		Author:         "gina",
		State:          "open",
		CreatedAt:      now.Add(-time.Hour),
		UpdatedAt:      now.Add(-time.Hour),
		LastActivityAt: now.Add(-time.Hour),
	})
	require.NoError(err)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/host/github.com/repo/gitea/acme/widget/comment-autocomplete?trigger=%23&q=collision&limit=10", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())

	var body itemapi.CommentAutocompleteResponse
	require.NoError(json.NewDecoder(rr.Body).Decode(&body))
	assert.Equal([]db.CommentAutocompleteReference{
		{Kind: "issue", Number: 901, Title: "Provider collision issue", State: "open"},
	}, body.References)
	assert.Empty(body.Users)
}

func TestAPICommentAutocompleteGitLabMergeRequestReferencesScopesByProvider(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	srv, database, _ := setupTestServer(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)

	giteaRepoID, err := database.UpsertRepo(ctx, db.RepoIdentity{
		Platform:       "gitea",
		PlatformHost:   "gitlab.example.com",
		PlatformRepoID: "repo-gitea-widget",
		Owner:          "acme",
		Name:           "widget",
		RepoPath:       "acme/widget",
	})
	require.NoError(err)
	gitlabRepoID, err := database.UpsertRepo(ctx, db.RepoIdentity{
		Platform:       "gitlab",
		PlatformHost:   "gitlab.example.com",
		PlatformRepoID: "repo-gitlab-widget",
		Owner:          "acme",
		Name:           "widget",
		RepoPath:       "acme/widget",
	})
	require.NoError(err)

	_, err = database.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID:         giteaRepoID,
		PlatformID:     12001,
		Number:         1,
		URL:            "https://gitlab.example.com/acme/widget/pulls/1",
		Title:          "Provider collision merge request",
		Author:         "gina",
		State:          "open",
		HeadBranch:     "feature-gitea",
		BaseBranch:     "main",
		CreatedAt:      now.Add(-2 * time.Hour),
		UpdatedAt:      now.Add(-2 * time.Hour),
		LastActivityAt: now.Add(-2 * time.Hour),
	})
	require.NoError(err)
	_, err = database.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID:         gitlabRepoID,
		PlatformID:     12901,
		Number:         901,
		URL:            "https://gitlab.example.com/acme/widget/-/merge_requests/901",
		Title:          "Provider collision merge request",
		Author:         "glenda",
		State:          "open",
		HeadBranch:     "feature-gitlab",
		BaseBranch:     "main",
		CreatedAt:      now.Add(-time.Hour),
		UpdatedAt:      now.Add(-time.Hour),
		LastActivityAt: now.Add(-time.Hour),
	})
	require.NoError(err)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/host/gitlab.example.com/repo/gitlab/acme/widget/comment-autocomplete?trigger=!&q=collision&limit=10", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())

	var body itemapi.CommentAutocompleteResponse
	require.NoError(json.NewDecoder(rr.Body).Decode(&body))
	assert.Equal([]db.CommentAutocompleteReference{
		{Kind: "pull", Number: 901, Title: "Provider collision merge request", State: "open"},
	}, body.References)
	assert.Empty(body.Users)
}

func TestE2EPRDetailRefreshesEditedCommentBody(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()

	now := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
	prNumber := 160
	prID := int64(160000)
	prTitle := "Edited comment refresh"
	prState := "open"
	prURL := "https://github.com/acme/widget/pull/160"
	headRef := "feature/edited-comment"
	headSHA := "deadbeef"
	baseRef := "main"
	commentID := int64(9001)
	commentAuthor := "reviewer"
	commentCreatedAt := now.Add(2 * time.Minute)
	commentBody := "original body"

	mock := &serverfake.MockGH{
		ListOpenIssuesFn: func(_ context.Context, _, _ string) ([]*gh.Issue, error) {
			return nil, nil
		},
		GetPullRequestFn: func(_ context.Context, _, _ string, number int) (*gh.PullRequest, error) {
			require.Equal(prNumber, number)
			return &gh.PullRequest{
				ID:        &prID,
				Number:    &prNumber,
				Title:     &prTitle,
				HTMLURL:   &prURL,
				State:     &prState,
				UpdatedAt: &gh.Timestamp{Time: now},
				CreatedAt: &gh.Timestamp{Time: now},
				Head: &gh.PullRequestBranch{
					Ref: &headRef,
					SHA: &headSHA,
				},
				Base: &gh.PullRequestBranch{
					Ref: &baseRef,
				},
			}, nil
		},
	}
	prListCalls := 0
	mock.ListOpenPullRequestsFn = func(_ context.Context, _, _ string) ([]*gh.PullRequest, error) {
		prListCalls++
		if prListCalls == 1 {
			return []*gh.PullRequest{{
				ID:        &prID,
				Number:    &prNumber,
				Title:     &prTitle,
				HTMLURL:   &prURL,
				State:     &prState,
				UpdatedAt: &gh.Timestamp{Time: now},
				CreatedAt: &gh.Timestamp{Time: now},
				Head: &gh.PullRequestBranch{
					Ref: &headRef,
					SHA: &headSHA,
				},
				Base: &gh.PullRequestBranch{
					Ref: &baseRef,
				},
			}}, nil
		}
		return nil, &gh.ErrorResponse{
			Response: &http.Response{StatusCode: http.StatusNotModified},
		}
	}
	mockComments := []*gh.IssueComment{{
		ID:        &commentID,
		Body:      &commentBody,
		User:      &gh.User{Login: &commentAuthor},
		CreatedAt: &gh.Timestamp{Time: commentCreatedAt},
		UpdatedAt: &gh.Timestamp{Time: commentCreatedAt},
	}}
	// A complete provider snapshot can contain overlapping pages or duplicate
	// identities. Detail count must follow the unique synchronized event row.
	mockComments = append(mockComments, mockComments[0])
	mock.ListIssueCommentsFn = func(_ context.Context, _, _ string, _ int) ([]*gh.IssueComment, error) {
		return mockComments, nil
	}

	database := dbtest.Open(t)

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

	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	t.Cleanup(func() { serverfake.GracefulShutdown(t, srv) })
	client := servertest.SetupTestClient(t, srv)

	syncer.RunOnce(ctx)

	firstResp, err := client.HTTP.GetPullWithResponse(ctx, &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(int64(prNumber))}})
	require.NoError(err)
	require.Equal(http.StatusOK, firstResp.StatusCode)
	require.NotNil(firstResp.JSON200)
	require.NotNil(firstResp.JSON200.Events)
	require.Len(firstResp.JSON200.Events, 1)
	assert.Equal("original body", firstResp.JSON200.Events[0].Body)

	editedBody := "edited body"
	mockComments = []*gh.IssueComment{{
		ID:        &commentID,
		Body:      &editedBody,
		User:      &gh.User{Login: &commentAuthor},
		CreatedAt: &gh.Timestamp{Time: commentCreatedAt},
		UpdatedAt: &gh.Timestamp{Time: now.Add(4 * time.Minute)},
	}}

	syncer.RunOnce(ctx)

	secondResp, err := client.HTTP.GetPullWithResponse(ctx, &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(int64(prNumber))}})
	require.NoError(err)
	require.Equal(http.StatusOK, secondResp.StatusCode)
	require.NotNil(secondResp.JSON200)
	require.NotNil(secondResp.JSON200.Events)
	require.Len(secondResp.JSON200.Events, 1)
	assert.Equal("edited body", secondResp.JSON200.Events[0].Body)
}

func TestE2EPRDetailRemovesDeletedCommentWhenPRListIsUnchanged(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()

	now := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
	prNumber := 160
	prID := int64(160000)
	prTitle := "Deleted comment refresh"
	prState := "open"
	prURL := "https://github.com/acme/widget/pull/160"
	headRef := "feature/deleted-comment"
	headSHA := "deadbeef"
	baseRef := "main"
	commentID := int64(9001)
	commentAuthor := "reviewer"
	commentCreatedAt := now.Add(2 * time.Minute)
	providerUpdatedAt := now.Add(3 * time.Minute)
	commentBody := "body to remove"

	mock := &serverfake.MockGH{
		ListOpenIssuesFn: func(_ context.Context, _, _ string) ([]*gh.Issue, error) {
			return nil, nil
		},
		GetPullRequestFn: func(_ context.Context, _, _ string, number int) (*gh.PullRequest, error) {
			require.Equal(prNumber, number)
			return &gh.PullRequest{
				ID:        &prID,
				Number:    &prNumber,
				Title:     &prTitle,
				HTMLURL:   &prURL,
				State:     &prState,
				UpdatedAt: &gh.Timestamp{Time: providerUpdatedAt},
				CreatedAt: &gh.Timestamp{Time: now},
				Head: &gh.PullRequestBranch{
					Ref: &headRef,
					SHA: &headSHA,
				},
				Base: &gh.PullRequestBranch{
					Ref: &baseRef,
				},
			}, nil
		},
	}
	prListCalls := 0
	mock.ListOpenPullRequestsFn = func(_ context.Context, _, _ string) ([]*gh.PullRequest, error) {
		prListCalls++
		if prListCalls == 1 {
			return []*gh.PullRequest{{
				ID:        &prID,
				Number:    &prNumber,
				Title:     &prTitle,
				HTMLURL:   &prURL,
				State:     &prState,
				UpdatedAt: &gh.Timestamp{Time: providerUpdatedAt},
				CreatedAt: &gh.Timestamp{Time: now},
				Head: &gh.PullRequestBranch{
					Ref: &headRef,
					SHA: &headSHA,
				},
				Base: &gh.PullRequestBranch{
					Ref: &baseRef,
				},
			}}, nil
		}
		return nil, &gh.ErrorResponse{
			Response: &http.Response{StatusCode: http.StatusNotModified},
		}
	}
	mockComments := []*gh.IssueComment{{
		ID:        &commentID,
		Body:      &commentBody,
		User:      &gh.User{Login: &commentAuthor},
		CreatedAt: &gh.Timestamp{Time: commentCreatedAt},
		UpdatedAt: &gh.Timestamp{Time: commentCreatedAt},
	}}
	// Exercise the PR detail API with the same duplicate-identity snapshot
	// that the atomic replacement layer must collapse.
	mockComments = append(mockComments, mockComments[0])
	mock.ListIssueCommentsFn = func(_ context.Context, _, _ string, _ int) ([]*gh.IssueComment, error) {
		return mockComments, nil
	}

	database := dbtest.Open(t)

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

	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	t.Cleanup(func() { serverfake.GracefulShutdown(t, srv) })
	client := servertest.SetupTestClient(t, srv)

	syncer.RunOnce(ctx)

	firstResp, err := client.HTTP.GetPullWithResponse(ctx, &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(int64(prNumber))}})
	require.NoError(err)
	require.Equal(http.StatusOK, firstResp.StatusCode)
	require.NotNil(firstResp.JSON200)
	require.Equal(int64(1), firstResp.JSON200.MergeRequest.CommentCount)
	require.Equal(providerUpdatedAt.UTC(), firstResp.JSON200.MergeRequest.LastActivityAt.UTC())
	require.NotNil(firstResp.JSON200.Events)
	require.Len(firstResp.JSON200.Events, 1)
	assert.Equal("body to remove", firstResp.JSON200.Events[0].Body)

	mockComments = []*gh.IssueComment{}

	syncer.RunOnce(ctx)

	secondResp, err := client.HTTP.GetPullWithResponse(ctx, &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(int64(prNumber))}})
	require.NoError(err)
	require.Equal(http.StatusOK, secondResp.StatusCode)
	require.NotNil(secondResp.JSON200)
	require.Equal(int64(0), secondResp.JSON200.MergeRequest.CommentCount)
	require.Equal(providerUpdatedAt.UTC(), secondResp.JSON200.MergeRequest.LastActivityAt.UTC())
	require.NotNil(secondResp.JSON200.Events)
	require.Empty(secondResp.JSON200.Events)
}

func TestE2EPRDetailRemovesDeletedCommentWhenAnotherPRChanges(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()

	now := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
	targetNumber := 160
	targetID := int64(160000)
	targetTitle := "Target PR keeps stale comment"
	targetURL := "https://github.com/acme/widget/pull/160"
	otherNumber := 161
	otherID := int64(161000)
	otherTitle := "Other PR changes"
	otherURL := "https://github.com/acme/widget/pull/161"
	prState := "open"
	headRef := "feature/comments"
	headSHA := "deadbeef"
	baseRef := "main"
	commentID := int64(9050)
	commentAuthor := "reviewer"
	commentCreatedAt := now.Add(2 * time.Minute)
	targetCommentBody := "target comment"
	targetComments := []*gh.IssueComment{{
		ID:        &commentID,
		Body:      &targetCommentBody,
		User:      &gh.User{Login: &commentAuthor},
		CreatedAt: &gh.Timestamp{Time: commentCreatedAt},
		UpdatedAt: &gh.Timestamp{Time: commentCreatedAt},
	}}
	otherUpdatedAt := now

	prListCalls := 0
	mock := &serverfake.MockGH{
		ListOpenIssuesFn: func(_ context.Context, _, _ string) ([]*gh.Issue, error) {
			return nil, nil
		},
		ListOpenPullRequestsFn: func(_ context.Context, _, _ string) ([]*gh.PullRequest, error) {
			prListCalls++
			if prListCalls > 1 {
				otherUpdatedAt = now.Add(5 * time.Minute)
			}
			return []*gh.PullRequest{
				{
					ID:        &targetID,
					Number:    &targetNumber,
					Title:     &targetTitle,
					HTMLURL:   &targetURL,
					State:     &prState,
					UpdatedAt: &gh.Timestamp{Time: now},
					CreatedAt: &gh.Timestamp{Time: now},
					Head:      &gh.PullRequestBranch{Ref: &headRef, SHA: &headSHA},
					Base:      &gh.PullRequestBranch{Ref: &baseRef},
				},
				{
					ID:        &otherID,
					Number:    &otherNumber,
					Title:     &otherTitle,
					HTMLURL:   &otherURL,
					State:     &prState,
					UpdatedAt: &gh.Timestamp{Time: otherUpdatedAt},
					CreatedAt: &gh.Timestamp{Time: now},
					Head:      &gh.PullRequestBranch{Ref: &headRef, SHA: &headSHA},
					Base:      &gh.PullRequestBranch{Ref: &baseRef},
				},
			}, nil
		},
		GetPullRequestFn: func(_ context.Context, _, _ string, number int) (*gh.PullRequest, error) {
			switch number {
			case targetNumber:
				return &gh.PullRequest{
					ID:        &targetID,
					Number:    &targetNumber,
					Title:     &targetTitle,
					HTMLURL:   &targetURL,
					State:     &prState,
					UpdatedAt: &gh.Timestamp{Time: now},
					CreatedAt: &gh.Timestamp{Time: now},
					Head:      &gh.PullRequestBranch{Ref: &headRef, SHA: &headSHA},
					Base:      &gh.PullRequestBranch{Ref: &baseRef},
				}, nil
			case otherNumber:
				return &gh.PullRequest{
					ID:        &otherID,
					Number:    &otherNumber,
					Title:     &otherTitle,
					HTMLURL:   &otherURL,
					State:     &prState,
					UpdatedAt: &gh.Timestamp{Time: otherUpdatedAt},
					CreatedAt: &gh.Timestamp{Time: now},
					Head:      &gh.PullRequestBranch{Ref: &headRef, SHA: &headSHA},
					Base:      &gh.PullRequestBranch{Ref: &baseRef},
				}, nil
			default:
				return nil, fmt.Errorf("unexpected pull request %d", number)
			}
		},
		ListIssueCommentsFn: func(_ context.Context, _, _ string, number int) ([]*gh.IssueComment, error) {
			if number == targetNumber {
				return targetComments, nil
			}
			return []*gh.IssueComment{}, nil
		},
	}

	database := dbtest.Open(t)

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

	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	t.Cleanup(func() { serverfake.GracefulShutdown(t, srv) })
	client := servertest.SetupTestClient(t, srv)

	syncer.RunOnce(ctx)

	firstResp, err := client.HTTP.GetPullWithResponse(ctx, &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(int64(targetNumber))}})
	require.NoError(err)
	require.Equal(http.StatusOK, firstResp.StatusCode)
	require.NotNil(firstResp.JSON200)
	require.Equal(int64(1), firstResp.JSON200.MergeRequest.CommentCount)
	require.NotNil(firstResp.JSON200.Events)
	require.Len(firstResp.JSON200.Events, 1)
	assert.Equal("target comment", firstResp.JSON200.Events[0].Body)

	targetComments = []*gh.IssueComment{}

	syncer.RunOnce(ctx)

	secondResp, err := client.HTTP.GetPullWithResponse(ctx, &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(int64(targetNumber))}})
	require.NoError(err)
	require.Equal(http.StatusOK, secondResp.StatusCode)
	require.NotNil(secondResp.JSON200)
	require.Equal(int64(0), secondResp.JSON200.MergeRequest.CommentCount)
	require.NotNil(secondResp.JSON200.Events)
	require.Empty(secondResp.JSON200.Events)
}

func TestE2EPRDetailRemovesDeletedCommentOnFullRefresh(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()

	now := time.Date(2026, 4, 13, 10, 0, 0, 0, time.UTC)
	prNumber := 170
	prID := int64(170000)
	prTitle := "Full refresh deleted comment"
	prState := "open"
	prURL := "https://github.com/acme/widget/pull/170"
	headRef := "feature/full-refresh-delete"
	headSHA := "feedface"
	baseRef := "main"
	commentID := int64(9101)
	commentAuthor := "reviewer"
	commentCreatedAt := now.Add(2 * time.Minute)
	commentBody := "comment removed on full refresh"
	currentUpdatedAt := now.Add(3 * time.Minute)
	currentComments := []*gh.IssueComment{{
		ID:        &commentID,
		Body:      &commentBody,
		User:      &gh.User{Login: &commentAuthor},
		CreatedAt: &gh.Timestamp{Time: commentCreatedAt},
		UpdatedAt: &gh.Timestamp{Time: commentCreatedAt},
	}}

	mock := &serverfake.MockGH{
		ListOpenIssuesFn: func(_ context.Context, _, _ string) ([]*gh.Issue, error) {
			return nil, nil
		},
		GetPullRequestFn: func(_ context.Context, _, _ string, number int) (*gh.PullRequest, error) {
			require.Equal(prNumber, number)
			return &gh.PullRequest{
				ID:        &prID,
				Number:    &prNumber,
				Title:     &prTitle,
				HTMLURL:   &prURL,
				State:     &prState,
				UpdatedAt: &gh.Timestamp{Time: currentUpdatedAt},
				CreatedAt: &gh.Timestamp{Time: now},
				Head: &gh.PullRequestBranch{
					Ref: &headRef,
					SHA: &headSHA,
				},
				Base: &gh.PullRequestBranch{
					Ref: &baseRef,
				},
			}, nil
		},
		ListIssueCommentsFn: func(_ context.Context, _, _ string, number int) ([]*gh.IssueComment, error) {
			require.Equal(prNumber, number)
			return currentComments, nil
		},
	}

	database := dbtest.Open(t)

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

	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	t.Cleanup(func() { serverfake.GracefulShutdown(t, srv) })
	client := servertest.SetupTestClient(t, srv)

	require.NoError(syncer.SyncMR(ctx, "acme", "widget", prNumber))

	firstResp, err := client.HTTP.GetPullWithResponse(ctx, &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(int64(prNumber))}})
	require.NoError(err)
	require.Equal(http.StatusOK, firstResp.StatusCode)
	require.NotNil(firstResp.JSON200)
	require.Equal(int64(1), firstResp.JSON200.MergeRequest.CommentCount)
	require.Equal(currentUpdatedAt.UTC(), firstResp.JSON200.MergeRequest.LastActivityAt.UTC())
	require.NotNil(firstResp.JSON200.Events)
	require.Len(firstResp.JSON200.Events, 1)
	assert.Equal("comment removed on full refresh", firstResp.JSON200.Events[0].Body)

	currentUpdatedAt = now.Add(4 * time.Minute)
	currentComments = []*gh.IssueComment{}

	require.NoError(syncer.SyncMR(ctx, "acme", "widget", prNumber))

	secondResp, err := client.HTTP.GetPullWithResponse(ctx, &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(int64(prNumber))}})
	require.NoError(err)
	require.Equal(http.StatusOK, secondResp.StatusCode)
	require.NotNil(secondResp.JSON200)
	require.Equal(int64(0), secondResp.JSON200.MergeRequest.CommentCount)
	require.Equal(currentUpdatedAt.UTC(), secondResp.JSON200.MergeRequest.LastActivityAt.UTC())
	require.NotNil(secondResp.JSON200.Events)
	require.Empty(secondResp.JSON200.Events)
}

func TestE2EPRDetailRemovesDeletedCommentOnGraphQLBulkSync(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()

	now := time.Date(2026, 4, 13, 11, 30, 0, 0, time.UTC)
	createdAt := now.Format(time.RFC3339)
	firstUpdatedAt := now.Add(3 * time.Minute).Format(time.RFC3339)
	secondUpdatedAt := now.Add(4 * time.Minute).Format(time.RFC3339)
	commentCreatedAt := now.Add(2 * time.Minute).Format(time.RFC3339)
	currentUpdatedAt := firstUpdatedAt
	currentCommentsJSON := `{"nodes":[{"databaseId":9222,"author":{"login":"commenter"},"body":"bulk PR comment removed","createdAt":"` + commentCreatedAt + `","updatedAt":"` + commentCreatedAt + `"}],"pageInfo":{"hasNextPage":false,"endCursor":""}}`

	gqlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if bytes.Contains(body, []byte("pullRequests")) {
			resp := `{"data":{"repository":{"pullRequests":{"nodes":[{
				"databaseId":172100,
				"number":173,
				"title":"Bulk deleted comment PR",
				"state":"OPEN",
				"isDraft":false,
				"body":"GraphQL bulk PR",
				"url":"https://github.com/acme/widget/pull/173",
				"author":{"login":"heidi"},
				"createdAt":"` + createdAt + `",
				"updatedAt":"` + currentUpdatedAt + `",
				"mergedAt":null,
				"closedAt":null,
				"additions":1,
				"deletions":0,
				"mergeable":"MERGEABLE",
				"reviewDecision":"",
				"headRefName":"feature/bulk-pr",
				"baseRefName":"main",
				"headRefOid":"deadbeef",
				"baseRefOid":"feedface",
				"headRepository":{"url":"https://github.com/acme/widget"},
				"labels":{"nodes":[]},
				"comments":` + currentCommentsJSON + `,
				"reviews":{"nodes":[],"pageInfo":{"hasNextPage":true,"endCursor":"review-cursor"}},
				"allCommits":{"nodes":[],"pageInfo":{"hasNextPage":true,"endCursor":"commit-cursor"}},
				"lastCommit":{"nodes":[]}
			}],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`
			_, _ = w.Write([]byte(resp))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"repository":{"issues":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`))
	}))
	defer gqlSrv.Close()

	prID := int64(172100)
	prNumber := 173
	prTitle := "Bulk deleted comment PR"
	prState := "open"
	prURL := "https://github.com/acme/widget/pull/173"
	headRef := "feature/bulk-pr"
	headSHA := "deadbeef"
	baseRef := "main"
	prTime := gh.Timestamp{Time: now}
	mock := &serverfake.MockGH{
		ListOpenPullRequestsFn: func(_ context.Context, _, _ string) ([]*gh.PullRequest, error) {
			updatedAt, parseErr := time.Parse(time.RFC3339, currentUpdatedAt)
			require.NoError(parseErr)
			updatedStamp := gh.Timestamp{Time: updatedAt}
			return []*gh.PullRequest{{
				ID:        &prID,
				Number:    &prNumber,
				Title:     &prTitle,
				State:     &prState,
				HTMLURL:   &prURL,
				User:      &gh.User{Login: new("heidi")},
				CreatedAt: &prTime,
				UpdatedAt: &updatedStamp,
				Head:      &gh.PullRequestBranch{Ref: &headRef, SHA: &headSHA},
				Base:      &gh.PullRequestBranch{Ref: &baseRef},
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

	firstResp, err := client.HTTP.GetPullWithResponse(ctx, &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(int64(prNumber))}})
	require.NoError(err)
	require.Equal(http.StatusOK, firstResp.StatusCode)
	require.NotNil(firstResp.JSON200)
	require.Equal(int64(1), firstResp.JSON200.MergeRequest.CommentCount)
	require.Equal(now.Add(3*time.Minute).UTC(), firstResp.JSON200.MergeRequest.LastActivityAt.UTC())
	require.NotNil(firstResp.JSON200.Events)
	require.Len(firstResp.JSON200.Events, 1)
	assert.Equal("bulk PR comment removed", firstResp.JSON200.Events[0].Body)

	currentUpdatedAt = secondUpdatedAt
	currentCommentsJSON = `{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}`

	syncer.RunOnce(ctx)

	secondResp, err := client.HTTP.GetPullWithResponse(ctx, &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(int64(prNumber))}})
	require.NoError(err)
	require.Equal(http.StatusOK, secondResp.StatusCode)
	require.NotNil(secondResp.JSON200)
	require.Equal(int64(0), secondResp.JSON200.MergeRequest.CommentCount)
	require.Equal(now.Add(4*time.Minute).UTC(), secondResp.JSON200.MergeRequest.LastActivityAt.UTC())
	require.NotNil(secondResp.JSON200.Events)
	require.Empty(secondResp.JSON200.Events)
}

// TestE2EGraphQLBulkSyncAppliesAuthoritativeReviewDecisionOverIncompleteReviews
// drives the real GraphQL bulk sync twice against a mocked GraphQL backend with
// real SQLite. The first pass persists an APPROVED review decision; the second
// pass reports a CHANGED authoritative reviewDecision (CHANGES_REQUESTED)
// alongside an incomplete reviews connection (hasNextPage=true, so
// ReviewsComplete is false). GitHub's reviewDecision scalar is authoritative
// over the PR's whole review history, so nested-connection truncation must not
// gate it: the changed decision must reach the HTTP API.
func TestE2EGraphQLBulkSyncAppliesAuthoritativeReviewDecisionOverIncompleteReviews(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()

	now := time.Date(2026, 5, 2, 9, 0, 0, 0, time.UTC)
	firstUpdatedAt := now.Format(time.RFC3339)
	secondUpdatedAt := now.Add(time.Minute).Format(time.RFC3339)
	currentUpdatedAt := firstUpdatedAt
	currentReviewDecision := "APPROVED"
	// First pass: reviews connection complete (empty). The second pass
	// overrides these to an incomplete connection carrying a changed decision.
	currentReviewsConn := `{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}`

	gqlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if bytes.Contains(body, []byte("pullRequests")) {
			resp := `{"data":{"repository":{"pullRequests":{"nodes":[{
				"databaseId":181100,
				"number":181,
				"title":"Authoritative review decision PR",
				"state":"OPEN",
				"isDraft":false,
				"body":"GraphQL bulk PR",
				"url":"https://github.com/acme/widget/pull/181",
				"author":{"login":"heidi"},
				"createdAt":"` + firstUpdatedAt + `",
				"updatedAt":"` + currentUpdatedAt + `",
				"mergedAt":null,
				"closedAt":null,
				"additions":1,
				"deletions":0,
				"mergeable":"MERGEABLE",
				"reviewDecision":"` + currentReviewDecision + `",
				"headRefName":"feature/decision",
				"baseRefName":"main",
				"headRefOid":"cafebabe",
				"baseRefOid":"feedface",
				"headRepository":{"url":"https://github.com/acme/widget"},
				"labels":{"nodes":[]},
				"comments":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}},
				"reviews":` + currentReviewsConn + `,
				"allCommits":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}},
				"lastCommit":{"nodes":[]}
			}],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`
			_, _ = w.Write([]byte(resp))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"repository":{"issues":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`))
	}))
	defer gqlSrv.Close()

	prID := int64(181100)
	prNumber := 181
	prTitle := "Authoritative review decision PR"
	prState := "open"
	prURL := "https://github.com/acme/widget/pull/181"
	headRef := "feature/decision"
	headSHA := "cafebabe"
	baseRef := "main"
	prCreated := gh.Timestamp{Time: now}
	mock := &serverfake.MockGH{
		ListOpenPullRequestsFn: func(_ context.Context, _, _ string) ([]*gh.PullRequest, error) {
			updatedAt, parseErr := time.Parse(time.RFC3339, currentUpdatedAt)
			require.NoError(parseErr)
			updatedStamp := gh.Timestamp{Time: updatedAt}
			return []*gh.PullRequest{{
				ID:        &prID,
				Number:    &prNumber,
				Title:     &prTitle,
				State:     &prState,
				HTMLURL:   &prURL,
				User:      &gh.User{Login: new("heidi")},
				CreatedAt: &prCreated,
				UpdatedAt: &updatedStamp,
				Head:      &gh.PullRequestBranch{Ref: &headRef, SHA: &headSHA},
				Base:      &gh.PullRequestBranch{Ref: &baseRef},
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

	// First pass persists the APPROVED decision through the real pipeline.
	syncer.RunOnce(ctx)
	firstResp, err := client.HTTP.GetPullWithResponse(ctx, &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(int64(prNumber))}})
	require.NoError(err)
	require.Equal(http.StatusOK, firstResp.StatusCode)
	require.NotNil(firstResp.JSON200)
	require.Equal("approved", firstResp.JSON200.MergeRequest.ReviewDecision)

	// Second pass: the provider reports a CHANGED authoritative decision while
	// the reviews connection is truncated (incomplete). The authoritative
	// scalar must still reach the API.
	currentUpdatedAt = secondUpdatedAt
	currentReviewDecision = "CHANGES_REQUESTED"
	currentReviewsConn = `{"nodes":[],"pageInfo":{"hasNextPage":true,"endCursor":"review-cursor"}}`

	syncer.RunOnce(ctx)
	secondResp, err := client.HTTP.GetPullWithResponse(ctx, &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(int64(prNumber))}})
	require.NoError(err)
	require.Equal(http.StatusOK, secondResp.StatusCode)
	require.NotNil(secondResp.JSON200)
	assert.Equal("changes_requested", secondResp.JSON200.MergeRequest.ReviewDecision)
}

func TestAPIGitHubPublishReviewDraftRejectsSelfApprovalBeforeProvider(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()
	var publishCalls int
	mock := &serverfake.MockGH{
		AuthenticatedViewerLoginFn: func(context.Context) (string, error) {
			return "marius", nil
		},
		CreateReviewWithCommentsFn: func(
			context.Context,
			string, string,
			int,
			string,
			string,
			string,
			[]*gh.DraftReviewComment,
		) (*gh.PullRequestReview, error) {
			publishCalls++
			return nil, errors.New("provider should not be called")
		},
	}
	srv, database, _ := setupTestServerWithMock(t, mock)
	serverfake.SeedPR(t, database, "acme", "widget", 42, serverfake.WithSeedPRAuthor("marius"))
	mr, err := database.GetMergeRequest(ctx, "github", "github.com", "acme", "widget", 42)
	require.NoError(err)
	require.NotNil(mr)
	require.NoError(database.UpdateDiffSHAs(ctx, mr.RepoID, 42, "github-head", "base", "merge-base"))

	basePath := "/api/v1/pulls/gh/acme/widget/42/review-draft"
	createRR := testutil.DoJSON(t, srv, http.MethodPost, basePath+"/comments", map[string]any{
		"body": "Please tighten this line.",
		"range": map[string]any{
			"path":          "src/main.go",
			"side":          "right",
			"line":          42,
			"new_line":      42,
			"line_type":     "add",
			"diff_head_sha": "github-head",
			"commit_sha":    "github-head",
		},
	})

	require.Equal(http.StatusCreated, createRR.Code, createRR.Body.String())

	publishRR := testutil.DoJSON(t, srv, http.MethodPost, basePath+"/publish", map[string]string{
		"action": "approve",
		"body":   "looks good",
	})

	require.Equal(http.StatusForbidden, publishRR.Code, publishRR.Body.String())
	var problem serverfake.RawProblemDetail
	require.NoError(json.NewDecoder(publishRR.Body).Decode(&problem))
	assert.Equal("forbidden", problem.Code)
	require.NotNil(problem.Details)
	assert.Equal(operationapi.AvailabilityCodeSelfApproval, problem.Details["reason"])
	assert.Zero(publishCalls)

	storedDraft, err := database.GetMRReviewDraft(ctx, mr.ID)
	require.NoError(err)
	assert.NotNil(storedDraft, "rejected self-approval must leave the local draft intact")
}

func TestAPIGitHubApprovePullRejectsSelfApprovalBeforeProvider(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	var providerCalled atomic.Bool
	mock := &serverfake.MockGH{
		AuthenticatedViewerLoginFn: func(context.Context) (string, error) {
			return "marius", nil
		},
		CreateReviewWithCommentsFn: func(
			context.Context,
			string, string,
			int,
			string, string, string,
			[]*gh.DraftReviewComment,
		) (*gh.PullRequestReview, error) {
			providerCalled.Store(true)
			return nil, errors.New("provider should not be called")
		},
	}
	srv, database, _ := setupTestServerWithMock(t, mock)
	serverfake.SeedPR(t, database, "acme", "widget", 42, serverfake.WithSeedPRAuthor("marius"))

	approveRR := testutil.DoJSON(
		t, srv, http.MethodPost,
		"/api/v1/pulls/gh/acme/widget/42/approve",
		map[string]any{"body": ""})

	require.Equal(http.StatusForbidden, approveRR.Code, approveRR.Body.String())
	var problem serverfake.RawProblemDetail
	require.NoError(json.NewDecoder(approveRR.Body).Decode(&problem))
	assert.Equal("forbidden", problem.Code)
	require.NotNil(problem.Details)
	assert.Equal(operationapi.AvailabilityCodeSelfApproval, problem.Details["reason"])
	assert.False(providerCalled.Load(), "self-approval must be rejected before the provider call")
}

func TestAPIGitLabPublishReviewDraftSendsSummaryThroughServer(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)
	providerUpdatedAt := now.Add(time.Minute)
	var order []string
	gitlabServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.EscapedPath() {
		case "/api/v4/projects/4242/merge_requests/7/draft_notes":
			assert.Equal(http.MethodPost, r.Method)
			order = append(order, "create-draft")
			authapi.WriteJSON(w, http.StatusOK, map[string]any{"id": 55, "note": "inline note"})
		case "/api/v4/projects/4242/merge_requests/7/draft_notes/55/publish":
			assert.Equal(http.MethodPut, r.Method)
			order = append(order, "publish-draft")
			authapi.WriteJSON(w, http.StatusOK, map[string]any{})
		case "/api/v4/projects/4242/merge_requests/7/notes":
			assert.Equal(http.MethodPost, r.Method)
			order = append(order, "summary-note")
			var body struct {
				Body string `json:"body"`
			}
			if !assert.NoError(json.NewDecoder(r.Body).Decode(&body)) {
				http.Error(w, "invalid request body", http.StatusBadRequest)
				return
			}
			assert.Equal("review summary from ui", body.Body)
			authapi.WriteJSON(w, http.StatusOK, map[string]any{"id": 77, "body": body.Body})
		case "/api/v4/projects/4242/merge_requests/7/approve":
			assert.Equal(http.MethodPost, r.Method)
			order = append(order, "approve")
			http.Error(w, "approval failed", http.StatusBadRequest)
		case "/api/v4/projects/4242/merge_requests/7/discussions":
			assert.Equal(http.MethodGet, r.Method)
			writeRawJSONForTest(w, `[
				{
					"id": "discussion-55",
					"individual_note": false,
					"notes": [{
						"id": 55,
						"type": "DiscussionNote",
						"body": "inline note",
						"author": {"username": "reviewer"},
						"system": false,
						"resolvable": true,
						"resolved": false,
						"created_at": "`+now.Format(time.RFC3339)+`",
						"updated_at": "`+now.Format(time.RFC3339)+`",
						"position": {
							"base_sha": "base",
							"start_sha": "merge-base",
							"head_sha": "gitlab-head",
							"position_type": "text",
							"new_path": "src/main.go",
							"new_line": 41
						}
					}]
				}
			]`)
		case "/api/v4/projects/4242/merge_requests/7":
			assert.Equal(http.MethodGet, r.Method)
			authapi.WriteJSON(w, http.StatusOK, map[string]any{
				"id": 7001, "iid": 7,
				"updated_at": providerUpdatedAt.Format(time.RFC3339),
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer gitlabServer.Close()

	srv, database, repoID := setupActualGitLabReviewServer(t, gitlabServer.URL, now)
	require.NoError(database.UpdateDiffSHAs(ctx, repoID, 7, "gitlab-head", "base", "merge-base"))

	basePath := "/api/v1/host/gitlab.example.com/pulls/gl/group/project/7/review-draft"
	createRR := testutil.DoJSON(t, srv, http.MethodPost, basePath+"/comments", map[string]any{
		"body": "inline note",
		"range": map[string]any{
			"path":          "src/main.go",
			"side":          "right",
			"line":          41,
			"new_line":      41,
			"line_type":     "add",
			"diff_head_sha": "gitlab-head",
		},
	})

	require.Equal(http.StatusCreated, createRR.Code, createRR.Body.String())

	publishRR := testutil.DoJSON(t, srv, http.MethodPost, basePath+"/publish", map[string]string{
		"action": "approve",
		"body":   " review summary from ui ",
	})

	require.Equal(http.StatusOK, publishRR.Code, publishRR.Body.String())
	var publishStatus pullapi.ActionStatusBody
	require.NoError(json.NewDecoder(publishRR.Body).Decode(&publishStatus))
	assert.Equal("partially_published", publishStatus.Status)
	assert.Equal([]string{"create-draft", "publish-draft", "summary-note", "approve"}, order)

	mr, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 7)
	require.NoError(err)
	require.NotNil(mr)
	draft, err := database.GetMRReviewDraft(ctx, mr.ID)
	require.NoError(err)
	assert.Nil(draft)
	threads, err := database.ListMRReviewThreads(ctx, mr.ID)
	require.NoError(err)
	require.Len(threads, 1)
	assert.Equal("discussion-55", threads[0].ProviderThreadID)
	assert.Equal(now, threads[0].CreatedAt)
	assert.Equal(now, threads[0].UpdatedAt)
	freshMR, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 7)
	require.NoError(err)
	require.NotNil(freshMR)
	assert.Equal(providerUpdatedAt, freshMR.UpdatedAt)
	assert.Equal(providerUpdatedAt, freshMR.LastActivityAt)
}

func setupActualGitLabReviewServer(
	t *testing.T,
	gitlabServerURL string,
	now time.Time,
) (*server.Server, *db.DB, int64) {
	t.Helper()
	require := require.New(t)
	ctx := t.Context()
	database := dbtest.Open(t)
	client, err := platformgitlab.NewClient(
		"gitlab.example.com",
		serverfake.TestTokenSource("token"),
		platformgitlab.WithBaseURLForTesting(gitlabServerURL+"/api/v4"),
		platformgitlab.WithoutRetriesForTesting(), platformgitlab.
			WithTransport(http.DefaultTransport),
	)
	require.NoError(err)
	registry, err := platform.NewRegistry(client)
	require.NoError(err)
	repoRef := ghclient.RepoRef{
		Platform:           platform.KindGitLab,
		PlatformHost:       "gitlab.example.com",
		Owner:              "group",
		Name:               "project",
		RepoPath:           "group/project",
		PlatformRepoID:     4242,
		PlatformExternalID: "gid://gitlab/Project/4242",
		WebURL:             "https://gitlab.example.com/group/project",
		CloneURL:           "https://gitlab.example.com/group/project.git",
		DefaultBranch:      "main",
	}
	syncer := ghclient.NewSyncerWithRegistry(
		registry, database, nil, []ghclient.RepoRef{repoRef}, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	t.Cleanup(func() { serverfake.GracefulShutdown(t, srv) })

	repoID, err := database.UpsertRepo(ctx, db.RepoIdentity{
		Platform:       "gitlab",
		PlatformHost:   "gitlab.example.com",
		PlatformRepoID: "4242",
		Owner:          "group",
		Name:           "project",
		RepoPath:       "group/project",
	})
	require.NoError(err)
	require.NoError(database.UpdateRepoProviderMetadata(ctx, repoID, db.RepoProviderMetadata{
		PlatformRepoID: "4242",
		WebURL:         "https://gitlab.example.com/group/project",
		CloneURL:       "https://gitlab.example.com/group/project.git",
		DefaultBranch:  "main",
	}))
	_, err = database.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID:             repoID,
		PlatformID:         7001,
		PlatformExternalID: "gid://gitlab/MergeRequest/7001",
		Number:             7,
		URL:                "https://gitlab.example.com/group/project/-/merge_requests/7",
		Title:              "GitLab provider MR",
		Author:             "ada",
		State:              "open",
		HeadBranch:         "feature/gitlab",
		BaseBranch:         "main",
		PlatformHeadSHA:    "gitlab-head",
		PlatformBaseSHA:    "base",
		CreatedAt:          now,
		UpdatedAt:          now,
		LastActivityAt:     now,
	})
	require.NoError(err)
	return srv, database, repoID
}

func writeRawJSONForTest(w http.ResponseWriter, body string) {
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, body)
}

func TestAPIApplyReviewSuggestionRejectsRateLimitedOperationBeforeProviderCall(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	caps := platform.Capabilities{
		ReadRepositories:            true,
		ReadMergeRequests:           true,
		ReadIssues:                  true,
		ReadComments:                true,
		ReviewSuggestionApplication: true,
		MutationHeadBinding:         true,
		ReadReviewThreads:           true,
	}
	srv, database, provider, syncer := setupGitLabCapabilityServerWithProvider(t, &caps)
	ctx := t.Context()
	provider.RateLimitBuckets = map[platform.OperationName][]platform.RateLimitBucket{
		platform.OperationApplyReviewSuggestion: {platform.RateLimitBucketREST},
	}
	rt := ghclient.NewPlatformRateTracker(database, "gitlab", "gitlab.example.com", "host", "rest")
	syncer.RateTrackers()[ghclient.RateBucketKey("gitlab", "gitlab.example.com", "host")] = rt
	rt.UpdateFromRate(ghclient.Rate{
		Limit:     5000,
		Remaining: 0,
		Reset:     time.Now().UTC().Add(30 * time.Minute),
	})

	repo, err := database.GetRepoByIdentity(ctx, db.RepoIdentity{
		Platform:     "gitlab",
		PlatformHost: "gitlab.example.com",
		RepoPath:     "group/project",
	})
	require.NoError(err)
	require.NotNil(repo)
	mr, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repo.ID, 7)
	require.NoError(err)
	require.NotNil(mr)
	line := 11
	require.NoError(database.UpsertMRReviewThreads(ctx, mr.ID, []db.MRReviewThread{{
		ProviderThreadID:  "provider-thread-1",
		ProviderCommentID: "provider-comment-1",
		Body:              "Please apply this.\n\n```suggestion\nreturn client.publishThreads();\n```",
		AuthorLogin:       "ada",
		Range: db.ReviewLineRange{
			Path:        "src/review.ts",
			Side:        "right",
			Line:        line,
			NewLine:     &line,
			LineType:    "context",
			DiffHeadSHA: "abc123",
		},
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}}))
	threads, err := database.ListMRReviewThreads(ctx, mr.ID)
	require.NoError(err)
	require.Len(threads, 1)

	rr := testutil.DoJSON(
		t,
		srv,
		http.MethodPost,
		"/api/v1/host/gitlab.example.com/pulls/gl/group/project/7/review-suggestions/apply",
		map[string]any{
			"expected_head_sha": "abc123",
			"suggestions": []map[string]any{{
				"thread_id":   strconv.FormatInt(threads[0].ID, 10),
				"replacement": "return client.publishThreads();",
			}},
		})

	require.Equal(http.StatusTooManyRequests, rr.Code, rr.Body.String())
	var problem serverfake.RawProblemDetail
	require.NoError(json.NewDecoder(rr.Body).Decode(&problem))
	assert.Equal("rateLimited", problem.Code)
	assert.Empty(provider.AppliedSuggestions)
}

func setupGitLabCapabilityServerWithProvider(
	t *testing.T,
	caps *platform.Capabilities,
) (*server.Server, *db.DB, *serverfake.ApiTestGitLabProvider, *ghclient.Syncer) {
	t.Helper()
	require := require.New(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)

	database := dbtest.Open(t)

	ref := platform.RepoRef{
		Platform:           platform.KindGitLab,
		Host:               "gitlab.example.com",
		Owner:              "group",
		Name:               "project",
		RepoPath:           "group/project",
		PlatformID:         4242,
		PlatformExternalID: "gid://gitlab/Project/4242",
		WebURL:             "https://gitlab.example.com/group/project",
		CloneURL:           "https://gitlab.example.com/group/project.git",
		DefaultBranch:      "main",
	}
	provider := &serverfake.ApiTestGitLabProvider{
		Ref:               ref,
		CapabilitiesValue: caps,
		MergeRequests: []platform.MergeRequest{{
			Repo:               ref,
			PlatformID:         7001,
			PlatformExternalID: "gid://gitlab/MergeRequest/7001",
			Number:             7,
			URL:                "https://gitlab.example.com/group/project/-/merge_requests/7",
			Title:              "GitLab provider MR",
			Author:             "ada",
			State:              "open",
			IsDraft:            true,
			HeadBranch:         "feature/gitlab",
			HeadRepoCloneURL:   "https://gitlab.example.com/fork/project.git",
			BaseBranch:         "main",
			HeadSHA:            "abc123",
			BaseSHA:            "def456",
			CreatedAt:          now,
			UpdatedAt:          now,
			LastActivityAt:     now,
		}},
		Issues: []platform.Issue{{
			Repo:               ref,
			PlatformID:         8001,
			PlatformExternalID: "gid://gitlab/Issue/8001",
			Number:             11,
			URL:                "https://gitlab.example.com/group/project/-/issues/11",
			Title:              "GitLab provider issue",
			Author:             "grace",
			State:              "open",
			CreatedAt:          now,
			UpdatedAt:          now,
			LastActivityAt:     now,
		}},
	}
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)

	repo := ghclient.RepoRef{
		Platform:           platform.KindGitLab,
		Owner:              "group",
		Name:               "project",
		PlatformHost:       "gitlab.example.com",
		RepoPath:           "group/project",
		PlatformRepoID:     4242,
		PlatformExternalID: "gid://gitlab/Project/4242",
		WebURL:             "https://gitlab.example.com/group/project",
		CloneURL:           "https://gitlab.example.com/group/project.git",
		DefaultBranch:      "main",
	}
	syncer := ghclient.NewSyncerWithRegistry(
		registry, database, nil, []ghclient.RepoRef{repo}, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	srv := server.New(database, syncer, nil, "/", &config.Config{Tmux: config.Tmux{
		Command: []string{filepath.Join(t.TempDir(), "missing-tmux")},
	}}, server.ServerOptions{
		WorktreeDir:                        t.TempDir(),
		DisableWorkspaceBackgroundMonitors: true,
	})
	t.Cleanup(func() { serverfake.GracefulShutdown(t, srv) })

	syncer.RunOnce(ctx)
	return srv, database, provider, syncer
}

func TestAPIRateLimitsUsesSafeIdentityKeyAndResolvedPrincipalLabel(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	database := dbtest.Open(t)
	identity := ghclient.IdentityKey{Host: "github.com", Principal: "user:123"}
	restRT := ghclient.NewRateTracker(database, "github.com", "user:123", "rest")
	bucket := ghclient.RateBucketKey("github", "github.com", "user:123")
	router, err := ghclient.NewHostRouter(
		"github.com",
		&ghclient.Route{
			Key:          ghclient.RouteKey{Host: "github.com", Owner: "acme"},
			ReadIdentity: identity, WriteIdentity: identity,
		},
	)
	require.NoError(err)
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": &serverfake.MockGH{}},
		database, nil,
		[]ghclient.RepoRef{{Owner: "acme", Name: "widget", PlatformHost: "github.com"}},
		time.Minute,
		map[string]*ghclient.RateTracker{bucket: restRT}, nil,
	)
	syncer.SetGitHubRouters(map[string]*ghclient.HostRouter{"github.com": router})
	syncer.SetRatePrincipalLabels(map[string]string{bucket: "GitHub user maintainer"})

	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	ts := httptest.NewServer(srv)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/v1/rate-limits")
	require.NoError(err)
	defer resp.Body.Close()
	require.Equal(http.StatusOK, resp.StatusCode)
	var body itemapi.RateLimitsResponse
	require.NoError(json.NewDecoder(resp.Body).Decode(&body))
	require.Len(body.ProviderPools, 1)
	for key, status := range body.ProviderPools {
		assert.NotContains(key, "\x00")
		assert.Equal("github:github.com:user:123", key)
		assert.Equal("user:123", status.RatePrincipal)
		assert.Equal("GitHub user maintainer", status.PrincipalLabel)
	}
}

// TestMergeBlocksPredecessorRestoredWhenNativeStackAgesOut is the full-stack
// consequence of the observation-based aging bound. Cache aging spans hours, so
// the syncer clock is injected rather than waited on. Under the stale native
// projection PR 101 follows a merged predecessor and would merge; once the
// observation ages out the projection returns to branch inference, which
// restores the open predecessor the merge safeguard must block on.
func TestMergeBlocksPredecessorRestoredWhenNativeStackAgesOut(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	observed := time.Now().UTC().Add(-24 * time.Hour).Truncate(time.Second)
	repoCloneURL := "https://github.com/acme/widget.git"
	makeGHPR := func(id int64, number int, head, base string) *gh.PullRequest {
		sha := fmt.Sprintf("sha%d", number)
		title := fmt.Sprintf("PR #%d", number)
		return &gh.PullRequest{
			ID: &id, Number: &number, State: new("open"), Title: &title,
			Body: new(""), User: &gh.User{Login: new("testuser")},
			CreatedAt: &gh.Timestamp{Time: observed}, UpdatedAt: &gh.Timestamp{Time: observed},
			Head: &gh.PullRequestBranch{
				Ref: &head, SHA: &sha,
				Repo: &gh.Repository{CloneURL: &repoCloneURL},
			},
			Base: &gh.PullRequestBranch{Ref: &base, SHA: new("basesha")},
		}
	}
	prs := []*gh.PullRequest{
		makeGHPR(1000, 100, "feature/a", "main"),
		makeGHPR(1001, 101, "feature/b", "feature/a"),
	}
	var listCalls atomic.Int32
	merged := false
	mock := &serverfake.MockGH{
		GetRepositoryFn: func(_ context.Context, owner, repo string) (*gh.Repository, error) {
			nodeID := "repo-" + owner + "-" + repo
			return &gh.Repository{
				Name: &repo, NodeID: &nodeID, Owner: &gh.User{Login: &owner},
				CloneURL: &repoCloneURL, Archived: new(false),
			}, nil
		},
		MergePullRequestFn: func(_ context.Context, _, _ string, _ int, _, _, _ string) (*gh.PullRequestMergeResult, error) {
			merged = true
			return &gh.PullRequestMergeResult{}, nil
		},
		NativeStackAPI: &serverfake.MockGHNativeStackAPI{
			ListOpenPullRequests: func(
				context.Context, string, string,
			) ([]*gh.PullRequest, map[int]*platformgithub.NativeStackHint, error) {
				if listCalls.Add(1) > 1 {
					// The open-PR list is byte-identical on the later sync, which
					// is the path a stale confirmation would survive on.
					return nil, nil, &gh.ErrorResponse{Response: &http.Response{
						StatusCode: http.StatusNotModified,
						Request: &http.Request{
							Method: http.MethodGet,
							URL:    &url.URL{Scheme: "https", Host: "api.github.com", Path: "/pulls"},
						},
					}}
				}
				// Only the tip is claimed by the stack, so no hint can attest to the
				// leading member the cached row names.
				return prs, map[int]*platformgithub.NativeStackHint{
					101: {Number: 42, Size: 2, Position: 2, BaseRef: "main"},
				}, nil
			},
			ListStackPage: func(
				context.Context, string, string, int,
			) (platformgithub.NativeStackPage, error) {
				return platformgithub.NativeStackPage{}, errors.New("catalog must not be refetched while confirmed")
			},
		},
	}
	srv, database, syncer := setupTestServerWithMock(t, mock)
	_, err := database.UpsertRepo(ctx, db.RepoIdentity{
		Platform:       "github",
		PlatformHost:   "github.com",
		PlatformRepoID: "repo-acme-widget",
		Owner:          "acme",
		Name:           "widget",
	})
	require.NoError(err)
	// PR 900 is merged, so the stale native chain shows PR 101 following a
	// finished predecessor.
	serverfake.SeedStackedPR(t, database, "acme", "widget", 900, "feature/z", "main", db.MergeRequestStateMerged, "", "")
	repo, err := database.GetRepoByIdentity(ctx, serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	require.NotNil(repo)
	require.NoError(database.ReplaceGitHubNativeStack(ctx, db.GitHubNativeStack{
		RepoID: repo.ID, GitHubID: 9042, Number: 42, Size: 2,
		BaseRef: "main", IsOpen: true, GitHubCreatedAt: observed,
		ContentFingerprint: "native-42", LastObservedAt: observed,
		Members: []db.GitHubNativeStackMember{
			{Position: 1, PullRequestNumber: 900, State: "merged", HeadRef: "feature/z", HeadSHA: "sha900"},
			{Position: 2, PullRequestNumber: 101, State: "open", HeadRef: "feature/b", HeadSHA: "sha101"},
		},
	}))
	clock := observed.Add(11 * time.Hour)
	syncer.SetClock(func() time.Time { return clock })
	syncer.SetPreferGitHubNativeStacks(true)
	syncer.SetOnSyncCompleted(stacks.SyncCompletedHook(ctx, database, nil))
	client := servertest.SetupTestClient(t, srv)

	syncer.RunOnce(ctx)

	stackResp, err := client.HTTP.GetPullStackWithResponse(ctx, &generated.GetPullStackRequestOptions{PathParams: &generated.GetPullStackPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(101)}})
	require.NoError(err)
	require.Equal(http.StatusOK, stackResp.StatusCode, string(stackResp.Body))
	require.NotNil(stackResp.JSON200)
	require.NotNil(stackResp.JSON200.Members)
	require.Equal([]int64{900, 101}, serverfake.StackMemberNumbers(stackResp.JSON200.Members),
		"inside its observation window the cached stack still owns the projection")

	// Two hours later the cached stack is past its own 12h window.
	clock = observed.Add(13 * time.Hour)
	syncer.RunOnce(ctx)

	stackResp, err = client.HTTP.GetPullStackWithResponse(ctx, &generated.GetPullStackRequestOptions{PathParams: &generated.GetPullStackPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(101)}})
	require.NoError(err)
	require.Equal(http.StatusOK, stackResp.StatusCode, string(stackResp.Body))
	require.NotNil(stackResp.JSON200)
	require.NotNil(stackResp.JSON200.Members)
	assert.Equal([]int64{100, 101}, serverfake.StackMemberNumbers(stackResp.JSON200.Members),
		"an aged observation must hand the repository back to branch inference")

	tipHeadSHA := "sha101"
	mergeResp, err := client.HTTP.MergePullWithResponse(ctx, &generated.MergePullRequestOptions{PathParams: &generated.MergePullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(101)}, Body: &generated.MergePRInputBody{Method: "squash", ExpectedHeadSha: &tipHeadSHA}})
	require.Error(err)
	require.NotNil(mergeResp)

	assert.Equal(http.StatusConflict, mergeResp.StatusCode, string(mergeResp.Body))
	assert.Contains(string(mergeResp.Body), `"reason":"mid_stack_merge_disallowed"`)
	assert.Contains(string(mergeResp.Body), `"blocking_number":100`)
	assert.False(merged, "the provider must not be asked to merge past an open predecessor")
}

// TestMergeBlocksPredecessorWhenNativeStackRefreshIsPartial is the full-stack
// consequence of refusing to project a partial refresh. Stack 42 is confirmable
// from cache and would place PR 101 behind a merged predecessor; stack 43 is
// hinted but its catalog row is rejected, so nothing about it -- including
// whether it also claims PR 101 -- is known. The pass must fall back to branch
// inference, which restores the open predecessor the safeguard blocks on.
func TestMergeBlocksPredecessorWhenNativeStackRefreshIsPartial(t *testing.T) {
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
		makeGHPR(1000, 100, "feature/a", "main"),
		makeGHPR(1001, 101, "feature/b", "feature/a"),
		makeGHPR(1003, 103, "feature/c", "main"),
	}
	merged := false
	mock := &serverfake.MockGH{
		GetRepositoryFn: func(_ context.Context, owner, repo string) (*gh.Repository, error) {
			nodeID := "repo-" + owner + "-" + repo
			return &gh.Repository{
				Name: &repo, NodeID: &nodeID, Owner: &gh.User{Login: &owner},
				CloneURL: &repoCloneURL, Archived: new(false),
			}, nil
		},
		MergePullRequestFn: func(_ context.Context, _, _ string, _ int, _, _, _ string) (*gh.PullRequestMergeResult, error) {
			merged = true
			return &gh.PullRequestMergeResult{}, nil
		},
		NativeStackAPI: &serverfake.MockGHNativeStackAPI{
			ListOpenPullRequests: func(
				context.Context, string, string,
			) ([]*gh.PullRequest, map[int]*platformgithub.NativeStackHint, error) {
				return prs, map[int]*platformgithub.NativeStackHint{
					101: {Number: 42, Size: 2, Position: 2, BaseRef: "main"},
					103: {Number: 43, Size: 1, Position: 1, BaseRef: "main"},
				}, nil
			},
			ListStackPage: func(
				context.Context, string, string, int,
			) (platformgithub.NativeStackPage, error) {
				// Stack 43 comes back naming a different pull request than the hint,
				// so the row is rejected and the target stays unresolved.
				return platformgithub.NativeStackPage{Stacks: []platformgithub.NativeStack{{
					ID: 9043, Number: 43, BaseRef: "main", Open: true, CreatedAt: now,
					Members: []platformgithub.NativeStackMember{
						{Position: 1, PullRequestNumber: 999, State: "open", HeadRef: "feature/x", HeadSHA: "sha999"},
					},
				}}}, nil
			},
		},
	}
	srv, database, syncer := setupTestServerWithMock(t, mock)
	serverfake.SeedStackedPR(t, database, "acme", "widget", 900, "feature/z", "main", db.MergeRequestStateMerged, "", "")
	repo, err := database.GetRepoByIdentity(ctx, serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	require.NotNil(repo)
	require.NoError(database.ReplaceGitHubNativeStack(ctx, db.GitHubNativeStack{
		RepoID: repo.ID, GitHubID: 9042, Number: 42, Size: 2,
		BaseRef: "main", IsOpen: true, GitHubCreatedAt: now,
		ContentFingerprint: "native-42", LastObservedAt: now,
		Members: []db.GitHubNativeStackMember{
			{Position: 1, PullRequestNumber: 900, State: "merged", HeadRef: "feature/z", HeadSHA: "sha900"},
			{Position: 2, PullRequestNumber: 101, State: "open", HeadRef: "feature/b", HeadSHA: "sha101"},
		},
	}))
	syncer.SetPreferGitHubNativeStacks(true)
	syncer.SetOnSyncCompleted(stacks.SyncCompletedHook(ctx, database, nil))
	client := servertest.SetupTestClient(t, srv)

	syncer.RunOnce(ctx)

	stackResp, err := client.HTTP.GetPullStackWithResponse(ctx, &generated.GetPullStackRequestOptions{PathParams: &generated.GetPullStackPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(101)}})
	require.NoError(err)
	require.Equal(http.StatusOK, stackResp.StatusCode, string(stackResp.Body))
	require.NotNil(stackResp.JSON200)
	require.NotNil(stackResp.JSON200.Members)
	assert.Equal([]int64{100, 101}, serverfake.StackMemberNumbers(stackResp.JSON200.Members),
		"a pass that could not resolve every stack must project none of them")

	tipHeadSHA := "sha101"
	mergeResp, err := client.HTTP.MergePullWithResponse(ctx, &generated.MergePullRequestOptions{PathParams: &generated.MergePullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(101)}, Body: &generated.MergePRInputBody{Method: "squash", ExpectedHeadSha: &tipHeadSHA}})
	require.Error(err)
	require.NotNil(mergeResp)

	assert.Equal(http.StatusConflict, mergeResp.StatusCode, string(mergeResp.Body))
	assert.Contains(string(mergeResp.Body), `"reason":"mid_stack_merge_disallowed"`)
	assert.Contains(string(mergeResp.Body), `"blocking_number":100`)
	assert.False(merged, "the provider must not be asked to merge past an open predecessor")
}
