package reposervertest

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
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
	"go.kenn.io/forge/internal/server/activityapi"
	"go.kenn.io/forge/internal/server/issueapi"
	"go.kenn.io/forge/internal/server/itemapi"
	"go.kenn.io/forge/internal/server/pullapi"
	"go.kenn.io/forge/internal/testutil"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/platform"

	platformgithub "go.kenn.io/forge/platform/github"
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

func TestAPIRepoFilterAcceptsMultipleRepos(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	srv, database, _ := setupTestServer(t)

	serverfake.SeedPROnHost(t, database, "github.com", "acme", "widget", 1)
	serverfake.SeedPROnHost(t, database, "github.com", "acme", "worker", 2)
	serverfake.SeedPROnHost(t, database, "github.com", "acme", "ignored", 3)
	serverfake.SeedIssueOnHost(t, database, "github.com", "acme", "widget", 11, "open", "widget issue")
	serverfake.SeedIssueOnHost(t, database, "github.com", "acme", "worker", 12, "open", "worker issue")
	serverfake.SeedIssueOnHost(t, database, "github.com", "acme", "ignored", 13, "open", "ignored issue")

	filter := url.QueryEscape("github|github.com/acme/widget,github|github.com/acme/worker")

	rawPulls := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/pulls?repo="+filter, nil)
	require.Equal(http.StatusOK, rawPulls.Code)
	var pulls []pullapi.MergeRequestResponse
	require.NoError(json.Unmarshal(rawPulls.Body.Bytes(), &pulls))
	require.Len(pulls, 2)
	assert.ElementsMatch([]string{"widget", "worker"}, []string{
		pulls[0].RepoName,
		pulls[1].RepoName,
	})

	rawIssues := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/issues?repo="+filter, nil)
	require.Equal(http.StatusOK, rawIssues.Code)
	var issues []issueapi.IssueResponse
	require.NoError(json.Unmarshal(rawIssues.Body.Bytes(), &issues))
	require.Len(issues, 2)
	assert.ElementsMatch([]string{"widget", "worker"}, []string{
		issues[0].RepoName,
		issues[1].RepoName,
	})

	since := url.QueryEscape(time.Now().UTC().Add(-time.Hour).Format(time.RFC3339))
	rawActivity := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/activity?since="+since+"&repo="+filter, nil)
	require.Equal(http.StatusOK, rawActivity.Code)
	var activity itemapi.ActivityResponse
	require.NoError(json.Unmarshal(rawActivity.Body.Bytes(), &activity))
	require.NotEmpty(activity.Items)
	for _, item := range activity.Items {
		assert.Contains([]string{"widget", "worker"}, item.RepoName)
	}
}

func TestAPIConfiguredRepoFiltersUseProviderIdentity(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()
	srv, database, _, syncer := servertest.SetupTestServerWithConfig(t)
	client := servertest.SetupTestClientWithBaseURL(t, srv, "http://127.0.0.1:8091")

	_, err := database.UpsertRepo(ctx, db.RepoIdentity{
		Platform:       "github",
		PlatformHost:   "github.com",
		PlatformRepoID: "github-widget",
		Owner:          "acme",
		Name:           "widget",
		RepoPath:       "acme/widget",
	})
	require.NoError(err)
	_, err = database.UpsertRepo(ctx, db.RepoIdentity{
		Platform:       "gitea",
		PlatformHost:   "github.com",
		PlatformRepoID: "gitea-widget",
		Owner:          "acme",
		Name:           "widget",
		RepoPath:       "acme/widget",
	})
	require.NoError(err)
	syncer.SetRepos([]ghclient.RepoRef{{
		Platform:     platform.KindGitHub,
		PlatformHost: "github.com",
		Owner:        "acme",
		Name:         "widget",
		RepoPath:     "acme/widget",
	}})

	reposResp, err := client.HTTP.ListReposWithResponse(ctx)
	require.NoError(err)
	require.Equal(http.StatusOK, reposResp.StatusCode)
	require.NotNil(reposResp.JSON200)
	require.Len(*reposResp.JSON200, 1)
	assert.Equal("github", (*reposResp.JSON200)[0].Platform)
	assert.Equal("github.com", (*reposResp.JSON200)[0].PlatformHost)
	assert.Equal("acme", (*reposResp.JSON200)[0].Owner)
	assert.Equal("widget", (*reposResp.JSON200)[0].Name)

	summariesResp, err := client.HTTP.ListRepoSummariesWithResponse(ctx)
	require.NoError(err)
	require.Equal(http.StatusOK, summariesResp.StatusCode)
	require.NotNil(summariesResp.JSON200)
	require.Len(*summariesResp.JSON200, 1)
	assert.Equal("github", (*summariesResp.JSON200)[0].Repo.Provider)
	assert.Equal("github.com", (*summariesResp.JSON200)[0].PlatformHost)
	assert.Equal("acme", (*summariesResp.JSON200)[0].Owner)
	assert.Equal("widget", (*summariesResp.JSON200)[0].Name)
}

func TestAPIListRepoSummaries(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)

	repos := []ghclient.RepoRef{
		{Platform: "github", Owner: "acme", Name: "widgets", PlatformHost: "github.com"},
		{Platform: "github", Owner: "acme", Name: "tools", PlatformHost: "github.com"},
		{Platform: "github", Owner: "acme", Name: "archived", PlatformHost: "github.com"},
	}
	srv, database, _ := setupTestServerWithRepos(t, &serverfake.MockGH{}, repos)
	client := servertest.SetupTestClient(t, srv)

	_, err := testutil.SeedFixtures(context.Background(), database)
	require.NoError(err)
	widgetsRepo, err := database.GetRepoByIdentity(context.Background(), serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "widgets"))
	require.NoError(err)
	require.NotNil(widgetsRepo)
	publishedAt := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)
	previousPublishedAt := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	commitsSince := 42
	err = database.UpsertRepoOverview(context.Background(), widgetsRepo.ID, db.RepoOverview{
		LatestRelease: &db.RepoRelease{
			TagName:         "v2.8.1",
			Name:            "Version 2.8.1",
			URL:             "https://github.com/acme/widgets/releases/tag/v2.8.1",
			TargetCommitish: "main",
			Prerelease:      false,
			PublishedAt:     &publishedAt,
		},
		Releases: []db.RepoRelease{
			{
				TagName:         "v2.8.1",
				Name:            "Version 2.8.1",
				URL:             "https://github.com/acme/widgets/releases/tag/v2.8.1",
				TargetCommitish: "main",
				Prerelease:      false,
				PublishedAt:     &publishedAt,
			},
			{
				TagName:         "v2.7.0",
				Name:            "Version 2.7.0",
				URL:             "https://github.com/acme/widgets/releases/tag/v2.7.0",
				TargetCommitish: "main",
				Prerelease:      true,
				PublishedAt:     &previousPublishedAt,
			},
		},
		CommitsSinceRelease: &commitsSince,
		CommitTimeline: []db.RepoCommitTimelinePoint{{
			SHA:         "abc123",
			Message:     "Ship repo overview",
			CommittedAt: time.Date(2026, 4, 2, 12, 0, 0, 0, time.UTC),
		}},
	})
	require.NoError(err)

	resp, err := client.HTTP.ListRepoSummariesWithResponse(context.Background())
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.Len(*resp.JSON200, 3)

	var widgets *generated.RepoSummaryResponse
	for i := range *resp.JSON200 {
		summary := &(*resp.JSON200)[i]
		if summary.Name == "widgets" {
			widgets = summary
			break
		}
	}
	require.NotNil(widgets)
	require.NotNil(widgets.ActiveAuthors)
	require.NotNil(widgets.RecentIssues)

	assert.Equal("acme", widgets.Owner)
	assert.Equal(int64(4), widgets.OpenPrCount)
	assert.Equal(int64(1), widgets.DraftPrCount)
	assert.Equal(int64(3), widgets.OpenIssueCount)
	assert.Equal(int64(7), widgets.CachedPrCount)
	assert.Equal(int64(4), widgets.CachedIssueCount)
	assert.NotNil(widgets.MostRecentActivityAt)
	require.NotNil(widgets.LatestRelease)
	assert.Equal("v2.8.1", widgets.LatestRelease.TagName)
	require.NotNil(widgets.Releases)
	assert.Len(widgets.Releases, 2)
	assert.Equal("v2.7.0", widgets.Releases[1].TagName)
	assert.True(widgets.Releases[1].Prerelease)
	assert.Equal(
		itemapi.FormatUTCRFC3339(previousPublishedAt),
		*widgets.Releases[1].PublishedAt,
	)
	assert.Equal(int64(42), *widgets.CommitsSinceRelease)
	require.NotNil(widgets.CommitTimeline)
	assert.Len(widgets.CommitTimeline, 1)
	assert.Equal("Ship repo overview", widgets.CommitTimeline[0].Message)
	assert.Len(widgets.ActiveAuthors, 3)
	assert.Equal("alice", widgets.ActiveAuthors[0].Login)
	assert.Equal(int64(3), widgets.ActiveAuthors[0].ItemCount)
	assert.NotEmpty(widgets.RecentIssues[0].Title)
}

func TestAPIListRepoSummariesIncludesDefaultPlatformHost(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)

	srv, database, _, syncer := servertest.SetupTestServerWithConfigContent(t, `
default_platform_host = "ghe.example.com"

[[repos]]
owner = "acme"
name = "widgets"
platform_host = "ghe.example.com"
`, &serverfake.MockGH{})

	_, err := database.UpsertRepo(
		t.Context(), serverfake.VerifiedGitHubRepoIdentity("ghe.example.com", "acme", "widgets"),
	)
	require.NoError(err)
	syncer.SetRepos([]ghclient.RepoRef{{
		Owner:        "acme",
		Name:         "widgets",
		PlatformHost: "ghe.example.com",
	}})

	rr := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/repos/summary", nil)
	require.Equal(http.StatusOK, rr.Code)

	var summaries []itemapi.RepoSummaryResponse
	require.NoError(json.NewDecoder(rr.Body).Decode(&summaries))
	require.Len(summaries, 1)
	assert.Equal("ghe.example.com", summaries[0].PlatformHost)
	assert.Equal("ghe.example.com", summaries[0].DefaultPlatformHost)
}

func TestAPISyncStatus(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)

	srv, _, syncer := setupTestServer(t)
	client := servertest.SetupTestClient(t, srv)
	syncer.RunOnce(t.Context())

	resp, err := client.HTTP.GetSyncStatusWithResponse(t.Context())
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.False(resp.JSON200.Running)
	require.NotNil(resp.JSON200.LastRunAt)
	assert.Equal(t, time.UTC, resp.JSON200.LastRunAt.Location())
}

func TestMatchPriorityRepoRequiresProviderQualifiedRepoPaths(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)

	tracked := []ghclient.RepoRef{
		{
			Platform:     platform.KindGitLab,
			PlatformHost: "gitlab.com",
			Owner:        "group/subgroup",
			Name:         "project",
			RepoPath:     "group/subgroup/project",
		},
		{
			Platform:     platform.KindGitHub,
			PlatformHost: "github.com",
			Owner:        "acme",
			Name:         "widget",
			RepoPath:     "acme/widget",
		},
		{
			Platform:     platform.KindGitea,
			PlatformHost: "github.com",
			Owner:        "acme",
			Name:         "widget",
			RepoPath:     "acme/widget",
		},
	}

	_, ok := activityapi.MatchPriorityRepo("group/subgroup/project", tracked)
	assert.False(ok)

	_, ok = activityapi.MatchPriorityRepo("github.com/acme/widget", tracked)
	assert.False(ok)

	repo, ok := activityapi.MatchPriorityRepo("gitea|github.com/acme/widget", tracked)
	assert.True(ok)
	assert.Equal(platform.KindGitea, repo.Platform)
	assert.Equal("github.com", repo.PlatformHost)
	assert.Equal("acme/widget", repo.RepoPath)

	repo, ok = activityapi.MatchPriorityRepo("github|github.com/acme/widget", tracked)
	assert.True(ok)
	assert.Equal(platform.KindGitHub, repo.Platform)
	assert.Equal("github.com", repo.PlatformHost)
	assert.Equal("acme/widget", repo.RepoPath)

	repo, ok = activityapi.MatchPriorityRepo("gitlab|gitlab.com/group/subgroup/project", tracked)
	assert.True(ok)
	assert.Equal(platform.KindGitLab, repo.Platform)
	assert.Equal("gitlab.com", repo.PlatformHost)
	assert.Equal("group/subgroup/project", repo.RepoPath)
}

// TestE2EGraphQLBulkSyncPersistsWorkflowApproval drives the periodic
// sync through the GraphQL bulk path and verifies the persisted
// workflow approval snapshot reaches the HTTP API. Regression test
// for the gap where fully-synced bulk PRs would mark detail_fetched_at
// without ever populating workflow_approval_checked_at, leaving the
// DB-only GET unable to surface the Approve workflows button.
func TestE2EGraphQLBulkSyncPersistsWorkflowApproval(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()

	now := time.Date(2026, 4, 14, 10, 0, 0, 0, time.UTC)
	nowRFC3339 := now.Format(time.RFC3339)
	const prNumber = 173
	const prID int64 = 173000
	const headSHA = "abc123"

	gqlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if bytes.Contains(body, []byte("pullRequests")) {
			resp := `{"data":{"repository":{"pullRequests":{"nodes":[{
				"databaseId":173000,
				"number":173,
				"title":"PR awaiting workflow approval",
				"state":"OPEN",
				"isDraft":false,
				"body":"",
				"url":"https://github.com/acme/widget/pull/173",
				"author":{"login":"ericdill"},
				"createdAt":"` + nowRFC3339 + `",
				"updatedAt":"` + nowRFC3339 + `",
				"mergedAt":null,
				"closedAt":null,
				"additions":1,
				"deletions":0,
				"mergeable":"MERGEABLE",
				"reviewDecision":"",
				"headRefName":"feature/fork-pr",
				"baseRefName":"main",
				"headRefOid":"` + headSHA + `",
				"baseRefOid":"def456",
				"headRepository":{"url":"https://github.com/ericdill/widget"},
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
				Title:     new("PR awaiting workflow approval"),
				State:     new("open"),
				HTMLURL:   new("https://github.com/acme/widget/pull/173"),
				User:      &gh.User{Login: new("ericdill")},
				CreatedAt: &prTime,
				UpdatedAt: &prTime,
				Head:      &gh.PullRequestBranch{Ref: new("feature/fork-pr"), SHA: new(headSHA)},
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
			return []*gh.WorkflowRun{{
				ID:           new(int64(7777)),
				HeadSHA:      new(headSHA),
				Event:        new("pull_request"),
				PullRequests: []*gh.PullRequest{{Number: new(prNumber)}},
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
		"head SHA has a pending workflow run; button must be live")
	assert.Equal(int64(1), resp.JSON200.WorkflowApproval.Count)
}

func TestAPIStateMutationDoesNotRecoverFromProviderWhenSyncDisabled(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	for _, itemType := range []string{"pull request", "issue"} {
		t.Run(itemType, func(t *testing.T) {
			require := require.New(t)
			var recoveryReads atomic.Int32
			mock := &serverfake.MockGH{
				EditPullRequestFn: func(context.Context, string, string, int, platformgithub.EditPullRequestOpts) (*gh.PullRequest, error) {
					return nil, serverfake.Make422Error()
				},
				GetPullRequestFn: func(context.Context, string, string, int) (*gh.PullRequest, error) {
					recoveryReads.Add(1)
					return nil, nil
				},
				EditIssueFn: func(context.Context, string, string, int, string) (*gh.Issue, error) {
					return nil, serverfake.Make422Error()
				},
				GetIssueFn: func(context.Context, string, string, int) (*gh.Issue, error) {
					recoveryReads.Add(1)
					return nil, nil
				},
			}
			srv, database, syncer := setupTestServerWithMock(t, mock)
			syncer.DisableSync()
			client := servertest.SetupTestClient(t, srv)
			var status int
			if itemType == "pull request" {
				serverfake.SeedPR(t, database, "acme", "widget", 1)
				resp, err := client.HTTP.SetPrGithubStateWithResponse(t.Context(), &generated.SetPrGithubStateRequestOptions{PathParams: &generated.SetPrGithubStatePath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}, Body: &generated.SetPrGithubStateBody{State: "closed"}})
				require.Error(err)
				status = resp.StatusCode
			} else {
				serverfake.SeedIssue(t, database, "acme", "widget", 5, "open")
				resp, err := client.HTTP.SetIssueGithubStateWithResponse(t.Context(), &generated.SetIssueGithubStateRequestOptions{PathParams: &generated.SetIssueGithubStatePath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(5)}, Body: &generated.SetIssueGithubStateBody{State: "closed"}})
				require.Error(err)
				status = resp.StatusCode
			}
			require.Equal(http.StatusServiceUnavailable, status)
			require.Zero(recoveryReads.Load())
		})
	}
}

func TestAPIRateLimits(t *testing.T) {
	serverfake.RunParallelServerTest(t)
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

	resp, err := http.Get(ts.URL + "/api/v1/rate-limits")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(200, resp.StatusCode)

	var body itemapi.RateLimitsResponse
	err = json.NewDecoder(resp.Body).Decode(&body)
	require.NoError(t, err)

	gh, ok := body.ProviderPools["github.com"]
	assert.True(ok)
	assert.Equal("host", gh.RatePrincipal)
	assert.Equal("Host credential", gh.PrincipalLabel)
	assert.Equal(0, gh.REST.Requests)
	assert.Equal(-1, gh.REST.Remaining)
	assert.False(gh.REST.Known)
	assert.Equal(200, gh.ReserveBuffer)
	assert.Empty(body.LocalCeilings)
}

func TestAPIRateLimitsSeparatesCredentialPoolsFromLocalCeilings(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	database := dbtest.Open(t)
	registry := ghclient.NewQuotaRegistry()
	reset := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	appIdentity := ghclient.IdentityKey{Host: "github.com", Principal: "installation:42"}
	userIdentity := ghclient.IdentityKey{Host: "github.com", Principal: "user:7"}
	registry.UpdateSnapshot(
		appIdentity, ghclient.QuotaResourceREST,
		ghclient.Rate{Limit: 15000, Remaining: 14900, Reset: reset},
	)
	registry.UpdateSnapshot(
		appIdentity, ghclient.QuotaResourceGraphQL,
		ghclient.Rate{Limit: 10000, Remaining: 9900, Reset: reset},
	)
	registry.UpdateSnapshot(
		userIdentity, ghclient.QuotaResourceREST,
		ghclient.Rate{Limit: 5000, Remaining: 4900, Reset: reset},
	)
	budget := ghclient.NewSyncBudgetWithEssentialReserve(3001)
	_, ok := budget.TrySpend(2701)
	require.True(ok)
	_, ok = budget.TrySpend(1)
	require.False(ok, "optional work must stop at the background boundary")
	expectedCeilingResetAt := budget.ResetAt().UTC().Format(time.RFC3339)
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": &serverfake.MockGH{}},
		database, nil,
		[]ghclient.RepoRef{{Owner: "acme", Name: "widget", PlatformHost: "github.com"}},
		time.Minute, nil,
		map[string]*ghclient.SyncBudget{"github.com": budget},
	)
	syncer.SetQuotaRegistry(registry)
	t.Cleanup(syncer.Stop)
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	ts := httptest.NewServer(srv)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/v1/rate-limits")
	require.NoError(err)
	defer resp.Body.Close()
	require.Equal(http.StatusOK, resp.StatusCode)
	var body itemapi.RateLimitsResponse
	require.NoError(json.NewDecoder(resp.Body).Decode(&body))

	app, ok := body.ProviderPools["github:github.com:installation:42"]
	require.True(ok)
	user, ok := body.ProviderPools["github:github.com:user:7"]
	require.True(ok)
	assert.Equal(14900, app.REST.Remaining)
	assert.Equal(9900, app.GraphQL.Remaining)
	assert.Equal(4900, user.REST.Remaining)
	assert.False(user.GraphQL.Known)
	ceiling, ok := body.LocalCeilings["github.com"]
	require.True(ok)
	assert.Equal(3001, ceiling.Limit)
	assert.Equal(2701, ceiling.BackgroundLimit)
	assert.Equal(2701, ceiling.Spent)
	assert.Equal(300, ceiling.Remaining)
	assert.Equal(expectedCeilingResetAt, ceiling.ResetAt)
}

func TestAPIRateLimitsMarksExpiredProviderQuotaUnknown(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	database := dbtest.Open(t)
	identity := ghclient.IdentityKey{Host: "github.com", Principal: "user:7"}
	registry := ghclient.NewQuotaRegistry()
	registry.UpdateSnapshot(identity, ghclient.QuotaResourceREST, ghclient.Rate{
		Limit: 5000, Remaining: 4800, Reset: time.Now().UTC().Add(-time.Second),
	})
	availability := registry.CheckReserve(
		identity, []ghclient.QuotaResource{ghclient.QuotaResourceREST}, 1,
		ghclient.RateReserveBuffer,
	)
	require.False(availability.Known)

	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": &serverfake.MockGH{}}, database, nil, nil,
		time.Minute, nil, nil,
	)
	syncer.SetQuotaRegistry(registry)
	t.Cleanup(syncer.Stop)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/rate-limits", nil)
	server.New(database, syncer, nil, "/", nil, server.ServerOptions{}).ServeHTTP(recorder, request)
	require.Equal(http.StatusOK, recorder.Code)

	var response itemapi.RateLimitsResponse
	require.NoError(json.NewDecoder(recorder.Body).Decode(&response))
	status, ok := response.ProviderPools["github:github.com:user:7"]
	require.True(ok)
	assert.False(status.REST.Known)
	assert.Equal(4800, status.REST.Remaining)
}

func TestAPIRateLimitsRollsExpiredTrackerBeforeResponse(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	database := dbtest.Open(t)
	tracker := ghclient.NewRateTracker(database, "github.com", "host", "rest")
	tracker.UpdateFromRate(ghclient.Rate{
		Limit: 5000, Remaining: 3000, Reset: time.Now().UTC().Add(time.Hour),
	})
	tracker.SetResetAtForTesting(time.Now().UTC().Add(-time.Second))
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": &serverfake.MockGH{}}, database, nil, nil,
		time.Minute, map[string]*ghclient.RateTracker{"github.com": tracker}, nil,
	)
	t.Cleanup(syncer.Stop)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/rate-limits", nil)
	server.New(database, syncer, nil, "/", nil, server.ServerOptions{}).ServeHTTP(recorder, request)
	require.Equal(http.StatusOK, recorder.Code)

	var response itemapi.RateLimitsResponse
	require.NoError(json.NewDecoder(recorder.Body).Decode(&response))
	status, ok := response.ProviderPools["github.com"]
	require.True(ok)
	assert.False(status.REST.Known)
	assert.Equal(-1, status.REST.Remaining)
	assert.Empty(status.REST.ResetAt)
}

func TestAPIRateLimitsWithBudget(t *testing.T) {
	serverfake.RunParallelServerTest(t)
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
		map[string]*ghclient.SyncBudget{"github.com": ghclient.NewSyncBudget(500)},
	)
	t.Cleanup(syncer.Stop)

	// Simulate some budget spend.
	budgets := syncer.Budgets()
	budgets["github.com"].Spend(42)

	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	ts := httptest.NewServer(srv)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/v1/rate-limits")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(200, resp.StatusCode)

	var body itemapi.RateLimitsResponse
	err = json.NewDecoder(resp.Body).Decode(&body)
	require.NoError(t, err)

	gh, ok := body.LocalCeilings["github.com"]
	assert.True(ok)
	assert.Equal(500, gh.Limit)
	assert.Equal(42, gh.Spent)
	assert.Equal(458, gh.Remaining)
}

func TestAPIRateLimitsResetExpiredBudgetWindow(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)

	database := dbtest.Open(t)

	rt := ghclient.NewRateTracker(database, "github.com", "host", "rest")
	budget := ghclient.NewSyncBudget(500)

	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": &serverfake.MockGH{}},
		database, nil,
		[]ghclient.RepoRef{{
			Owner: "acme", Name: "widget",
			PlatformHost: "github.com",
		}},
		time.Minute,
		map[string]*ghclient.RateTracker{"github.com": rt},
		map[string]*ghclient.SyncBudget{"github.com": budget},
	)
	t.Cleanup(syncer.Stop)

	budget.Spend(42)
	rt.UpdateFromRate(ghclient.Rate{
		Limit:     5000,
		Remaining: 3000,
		Reset:     time.Now().Add(time.Hour),
	})
	rt.SetResetAtForTesting(time.Now().Add(-time.Second))

	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	ts := httptest.NewServer(srv)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/v1/rate-limits")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(200, resp.StatusCode)

	var body itemapi.RateLimitsResponse
	err = json.NewDecoder(resp.Body).Decode(&body)
	require.NoError(t, err)

	gh, ok := body.LocalCeilings["github.com"]
	assert.True(ok)
	assert.Equal(500, gh.Limit)
	assert.Equal(0, gh.Spent)
	assert.Equal(500, gh.Remaining)
}

func TestAPIRateLimitsIncludesWriteOnlyGraphQLState(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	database := dbtest.Open(t)
	bucket := ghclient.RateBucketKey("github", "github.com", "user:123")
	restRT := ghclient.NewRateTracker(database, "github.com", "user:123", "rest")
	gqlRT := ghclient.NewRateTracker(database, "github.com", "user:123", "graphql")
	gqlRT.UpdateFromRate(ghclient.Rate{Limit: 5000, Remaining: 4300, Reset: time.Now().Add(time.Hour)})
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": &serverfake.MockGH{}}, database, nil,
		nil, time.Minute,
		map[string]*ghclient.RateTracker{bucket: restRT}, nil,
	)
	syncer.SetWriteGQLRateTrackers(map[string]*ghclient.RateTracker{bucket: gqlRT})
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	ts := httptest.NewServer(srv)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/v1/rate-limits")
	require.NoError(err)
	defer resp.Body.Close()
	var body itemapi.RateLimitsResponse
	require.NoError(json.NewDecoder(resp.Body).Decode(&body))
	status := body.ProviderPools["github:github.com:user:123"]
	assert.True(status.GraphQL.Known)
	assert.Equal(4300, status.GraphQL.Remaining)
}

func TestAPIRateLimitsWithGQL(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)

	database := dbtest.Open(t)

	restRT := ghclient.NewRateTracker(database, "github.com", "host", "rest")
	gqlRT := ghclient.NewRateTracker(database, "github.com", "host", "graphql")

	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": &serverfake.MockGH{}},
		database, nil,
		[]ghclient.RepoRef{{
			Owner: "acme", Name: "widget",
			PlatformHost: "github.com",
		}},
		time.Minute,
		map[string]*ghclient.RateTracker{"github.com": restRT},
		nil,
	)

	fetcher := ghclient.NewGraphQLFetcher(serverfake.TestTokenSource("token"), "github.com", gqlRT, nil)
	syncer.SetFetchers(map[string]*ghclient.GraphQLFetcher{
		"github.com": fetcher,
	})

	// Simulate GraphQL rate data.
	gqlRT.UpdateFromRate(ghclient.Rate{
		Limit:     5000,
		Remaining: 4800,
		Reset:     time.Now().Add(30 * time.Minute),
	})

	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	ts := httptest.NewServer(srv)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/v1/rate-limits")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(200, resp.StatusCode)

	var body itemapi.RateLimitsResponse
	err = json.NewDecoder(resp.Body).Decode(&body)
	require.NoError(t, err)

	host, ok := body.ProviderPools["github.com"]
	assert.True(ok)

	// GQL fields should be populated.
	assert.Equal("host", host.RatePrincipal)
	assert.Equal("Host credential", host.PrincipalLabel)
	assert.Equal(4800, host.GraphQL.Remaining)
	assert.Equal(5000, host.GraphQL.Limit)
	assert.True(host.GraphQL.Known)
	assert.NotEmpty(host.GraphQL.ResetAt)
}

func TestAPIRateLimitsReadsLocalStateWithoutRefreshingGitHubRateLimit(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)

	database := dbtest.Open(t)
	now := time.Now().UTC().Truncate(time.Second)
	localRestReset := now.Add(30 * time.Minute)
	localGQLReset := now.Add(45 * time.Minute)
	gitHubRestReset := now.Add(time.Hour)
	gitHubGQLReset := now.Add(90 * time.Minute)

	restRT := ghclient.NewRateTracker(database, "github.com", "host", "rest")
	gqlRT := ghclient.NewRateTracker(database, "github.com", "host", "graphql")
	restRT.UpdateFromRate(ghclient.Rate{
		Limit:     5000,
		Remaining: 3000,
		Reset:     localRestReset,
	})
	gqlRT.UpdateFromRate(ghclient.Rate{
		Limit:     5000,
		Remaining: 4000,
		Reset:     localGQLReset,
	})
	mock := &serverfake.MockGH{
		RateLimitSnapshotFn: func(context.Context) (*platformgithub.RateLimitSnapshot, error) {
			return &platformgithub.RateLimitSnapshot{
				Core: &ghclient.Rate{
					Limit:     5000,
					Remaining: 4991,
					Reset:     gitHubRestReset,
				},
				GraphQL: &ghclient.Rate{
					Limit:     5000,
					Remaining: 4988,
					Reset:     gitHubGQLReset,
				},
			}, nil
		},
	}
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": mock},
		database, nil,
		[]ghclient.RepoRef{{
			Owner: "acme", Name: "widget",
			PlatformHost: "github.com",
		}},
		time.Minute,
		map[string]*ghclient.RateTracker{"github.com": restRT},
		nil,
	)
	syncer.SetFetchers(map[string]*ghclient.GraphQLFetcher{
		"github.com": ghclient.NewGraphQLFetcher(serverfake.TestTokenSource("token"), "github.com", gqlRT, nil),
	})
	t.Cleanup(syncer.Stop)

	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	ts := httptest.NewServer(srv)
	defer ts.Close()

	fetch := func() itemapi.RateLimitsResponse {
		resp, err := http.Get(ts.URL + "/api/v1/rate-limits")
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(200, resp.StatusCode)

		var body itemapi.RateLimitsResponse
		err = json.NewDecoder(resp.Body).Decode(&body)
		require.NoError(t, err)
		return body
	}

	body := fetch()
	user, ok := body.ProviderPools["github.com"]
	assert.True(ok)
	assert.Equal(0, mock.RateLimitSnapshotCalls)
	assert.Equal(0, user.REST.Requests)
	assert.Equal(3000, user.REST.Remaining)
	assert.Equal(5000, user.REST.Limit)
	assert.Equal(itemapi.FormatUTCRFC3339(localRestReset), user.REST.ResetAt)
	assert.True(user.REST.Known)
	assert.Equal(4000, user.GraphQL.Remaining)
	assert.Equal(5000, user.GraphQL.Limit)
	assert.Equal(itemapi.FormatUTCRFC3339(localGQLReset), user.GraphQL.ResetAt)
	assert.True(user.GraphQL.Known)

	_ = fetch()
	assert.Equal(0, mock.RateLimitSnapshotCalls)
}

func TestAPIRateLimitsGQLDefaultsUnknown(t *testing.T) {
	serverfake.RunParallelServerTest(t)
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
	// No SetFetchers call — GQL data should be unknown.

	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	ts := httptest.NewServer(srv)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/v1/rate-limits")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(200, resp.StatusCode)

	var body itemapi.RateLimitsResponse
	err = json.NewDecoder(resp.Body).Decode(&body)
	require.NoError(t, err)

	user := body.ProviderPools["github.com"]
	assert.Equal(-1, user.GraphQL.Remaining)
	assert.Equal(-1, user.GraphQL.Limit)
	assert.False(user.GraphQL.Known)
	assert.Empty(user.GraphQL.ResetAt)
}

func TestAPIRateLimitsMultiHostMixed(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)

	database := dbtest.Open(t)

	// Two hosts: github.com has GQL data, ghe.example.com does not.
	ghRT := ghclient.NewRateTracker(database, "github.com", "host", "rest")
	gheRT := ghclient.NewRateTracker(database, "ghe.example.com", "host", "rest")
	gqlRT := ghclient.NewRateTracker(database, "github.com", "host", "graphql")
	gqlRT.UpdateFromRate(ghclient.Rate{
		Limit:     5000,
		Remaining: 4500,
		Reset:     time.Now().Add(30 * time.Minute),
	})

	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{
			"github.com":      &serverfake.MockGH{},
			"ghe.example.com": &serverfake.MockGH{},
		},
		database, nil,
		[]ghclient.RepoRef{
			{Platform: "github", Owner: "acme", Name: "widget", PlatformHost: "github.com"},
			{Owner: "corp", Name: "internal", PlatformHost: "ghe.example.com"},
		},
		time.Minute,
		map[string]*ghclient.RateTracker{
			"github.com":      ghRT,
			"ghe.example.com": gheRT,
		},
		nil,
	)

	fetcher := ghclient.NewGraphQLFetcher(serverfake.TestTokenSource("token"), "github.com", gqlRT, nil)
	syncer.SetFetchers(map[string]*ghclient.GraphQLFetcher{
		"github.com": fetcher,
	})

	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	ts := httptest.NewServer(srv)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/v1/rate-limits")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(200, resp.StatusCode)

	var body itemapi.RateLimitsResponse
	err = json.NewDecoder(resp.Body).Decode(&body)
	require.NoError(t, err)

	// Both hosts present.
	assert.Len(body.ProviderPools, 2)

	// github.com has GQL data.
	ghHost := body.ProviderPools["github.com"]
	assert.True(ghHost.GraphQL.Known)
	assert.Equal(4500, ghHost.GraphQL.Remaining)
	assert.Equal(5000, ghHost.GraphQL.Limit)

	// ghe.example.com has no GQL fetcher — defaults to unknown.
	gheHost := body.ProviderPools["ghe.example.com"]
	assert.Equal(-1, gheHost.GraphQL.Remaining)
	assert.Equal(-1, gheHost.GraphQL.Limit)
	assert.False(gheHost.GraphQL.Known)
}

func TestAPIRateLimitsScopesSameHostByProvider(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)

	database := dbtest.Open(t)

	host := "code.example.com"
	ghRT := ghclient.NewPlatformRateTracker(database, "github", host, "host", "rest")
	glRT := ghclient.NewPlatformRateTracker(database, "gitlab", host, "host", "rest")
	ghRT.RecordRequest()
	glRT.RecordRequest()
	glRT.RecordRequest()

	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{host: &serverfake.MockGH{}},
		database, nil,
		[]ghclient.RepoRef{
			{Platform: platform.KindGitHub, Owner: "acme", Name: "widget", PlatformHost: host},
			{Platform: platform.KindGitLab, Owner: "acme", Name: "widget", PlatformHost: host},
		},
		time.Minute,
		map[string]*ghclient.RateTracker{
			ghclient.RateBucketKey("github", host, "host"): ghRT,
			ghclient.RateBucketKey("gitlab", host, "host"): glRT,
		},
		nil,
	)
	t.Cleanup(syncer.Stop)

	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	ts := httptest.NewServer(srv)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/v1/rate-limits")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(200, resp.StatusCode)

	var body itemapi.RateLimitsResponse
	err = json.NewDecoder(resp.Body).Decode(&body)
	require.NoError(t, err)

	ghStatus, ok := body.ProviderPools[host]
	assert.True(ok)
	assert.Equal("github", ghStatus.Provider)
	assert.Equal(host, ghStatus.PlatformHost)
	assert.Equal(1, ghStatus.REST.Requests)

	glStatus, ok := body.ProviderPools["gitlab:"+host]
	assert.True(ok)
	assert.Equal("gitlab", glStatus.Provider)
	assert.Equal(host, glStatus.PlatformHost)
	assert.Equal(2, glStatus.REST.Requests)
}

// TestDisplayNameCacheE2E verifies the display-name cache
// through the full stack: sync → SQLite → HTTP API. Two
// RunOnce passes populate and then cache-hit the display name;
// the test asserts /api/v1/pulls returns the expected
// AuthorDisplayName after each pass, and that GetUser is only
// called during the first sync.
func TestDisplayNameCacheE2E(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)

	now := time.Now().UTC().Truncate(time.Second)
	prID := int64(1000)
	prNumber := 1
	prTitle := "test pr"
	prState := "open"
	prURL := "https://github.com/acme/widget/pull/1"
	prBody := ""
	prAuthor := "alice"
	displayName := "Alice Smith"
	getUserCalls := 0

	mock := &serverfake.MockGH{
		ListOpenPullRequestsFn: func(
			_ context.Context, _, _ string,
		) ([]*gh.PullRequest, error) {
			return []*gh.PullRequest{{
				ID:        &prID,
				Number:    &prNumber,
				Title:     &prTitle,
				State:     &prState,
				HTMLURL:   &prURL,
				Body:      &prBody,
				User:      &gh.User{Login: &prAuthor},
				CreatedAt: &gh.Timestamp{Time: now},
				UpdatedAt: &gh.Timestamp{Time: now},
			}}, nil
		},
		GetUserFn: func(_ context.Context, login string) (*gh.User, error) {
			getUserCalls++
			return &gh.User{Login: &login, Name: &displayName}, nil
		},
	}

	srv, _, syncer := setupTestServerWithMock(t, mock)

	// First sync: populates display name via GetUser.
	syncer.RunOnce(t.Context())
	require.Positive(getUserCalls, "first sync should call GetUser")
	firstCalls := getUserCalls

	// GET /api/v1/pulls — display name must appear.
	rr := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/pulls", nil)
	require.Equal(http.StatusOK, rr.Code)
	require.Contains(rr.Body.String(), `"AuthorDisplayName":"Alice Smith"`)

	// Second sync: cache hit, no new GetUser calls.
	syncer.RunOnce(t.Context())
	require.Equal(firstCalls, getUserCalls,
		"second sync must not re-fetch cached display names")

	// GET /api/v1/pulls — display name still present.
	rr2 := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/pulls", nil)
	require.Equal(http.StatusOK, rr2.Code)
	require.Contains(rr2.Body.String(), `"AuthorDisplayName":"Alice Smith"`)
}
