package repotest

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	serverfake "go.kenn.io/forge/internal/testutil/serverfake"
	servertest "go.kenn.io/forge/internal/testutil/servertest"

	gh "github.com/google/go-github/v91/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/apiclient/generated"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/gitclone"

	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/platformdb"
	"go.kenn.io/forge/internal/server"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/server/issueapi"
	"go.kenn.io/forge/internal/server/pullapi"
	"go.kenn.io/forge/internal/testutil"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/testutil/gitfixture"
	"go.kenn.io/forge/internal/tokenauth"
	"go.kenn.io/forge/platform"
	"go.kenn.io/forge/platform/gitealike"

	gitcmd "go.kenn.io/kit/git/cmd"
)

func TestMain(m *testing.M) {
	os.Exit(serverfake.RunMain(m))
}

func TestAPIGetVersionReturnsBuildMetadata(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	srv, _ := servertest.SetupTestServer(t)
	srv.SetBuildInfo(server.BuildInfo{
		Name:      "kenn-forge",
		Version:   "1.2.3",
		Commit:    "abc1234",
		BuildDate: "2026-07-12T12:00:00Z",
	})

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/version", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	var body map[string]any
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
	assert := assert.New(t)
	assert.Equal("kenn-forge", body["name"])
	assert.Equal("1.2.3", body["version"])
	assert.Equal("abc1234", body["commit"])
	assert.Equal("2026-07-12T12:00:00Z", body["buildDate"])
}

func TestAPIEnqueueItemSyncRejectsRemovedUpstreamWithoutProviderCalls(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	var pullCalls atomic.Int64
	var issueCalls atomic.Int64
	mock := &serverfake.MockGH{
		GetPullRequestFn: func(context.Context, string, string, int) (*gh.PullRequest, error) {
			pullCalls.Add(1)
			return nil, errors.New("removed pull must not be fetched")
		},
		GetIssueFn: func(context.Context, string, string, int) (*gh.Issue, error) {
			issueCalls.Add(1)
			return nil, errors.New("removed issue must not be fetched")
		},
	}

	srv, database := servertest.SetupTestServerWithMock(t, mock)
	ctx := t.Context()
	serverfake.SeedPR(t, database, "acme", "widget", 1)
	serverfake.SeedIssue(t, database, "acme", "widget", 2, "open")
	repo, err := database.GetRepoByIdentity(
		ctx, serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "widget"),
	)
	require.NoError(err)
	require.NotNil(repo)
	serverfake.MarkArchiveItemRemovedUpstreamForServerTest(
		t, database, repo.ID, db.ArchiveItemTypeMergeRequest, 1,
	)
	serverfake.MarkArchiveItemRemovedUpstreamForServerTest(
		t, database, repo.ID, db.ArchiveItemTypeIssue, 2,
	)
	client := servertest.SetupTestClient(t, srv)

	pullResp, err := client.HTTP.EnqueuePrSyncWithResponse(ctx, &generated.EnqueuePrSyncRequestOptions{PathParams: &generated.EnqueuePrSyncPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
	require.Error(err)
	require.NotNil(pullResp)
	require.Equal(http.StatusNotFound, pullResp.StatusCode, string(pullResp.Body))
	require.NotNil(pullResp.Error)
	require.Equal(
		generated.ProblemErrorCode("pullNotFound"),
		pullResp.Error.Code,
	)

	issueResp, err := client.HTTP.EnqueueIssueSyncWithResponse(ctx, &generated.EnqueueIssueSyncRequestOptions{PathParams: &generated.EnqueueIssueSyncPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(2)}})
	require.Error(err)
	require.NotNil(issueResp)
	require.Equal(http.StatusNotFound, issueResp.StatusCode, string(issueResp.Body))
	require.NotNil(issueResp.Error)
	require.Equal(
		generated.ProblemErrorCode("issueNotFound"),
		issueResp.Error.Code,
	)
	require.Zero(pullCalls.Load())
	require.Zero(issueCalls.Load())
}

func TestAPIRouteReuseServesOnlyCurrentRepository(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	srv, database := servertest.SetupTestServer(t)
	ctx := t.Context()

	oldEntry, err := database.GetRepositoryByProviderID(
		ctx, "github", "github.com", "repo-acme-widget",
	)
	require.NoError(err)
	require.NotNil(oldEntry)
	serverfake.SeedPRForRepo(
		t, database, oldEntry.Repository.ID,
		"github.com", "acme", "widget", 7,
		serverfake.WithSeedPRTitle("historical pull request"),
	)
	serverfake.SeedIssueForRepo(
		t, database, oldEntry.Repository.ID,
		"github.com", "acme", "widget", 8, "open", "historical issue",
	)

	newEntry, _, err := database.ReconcileRepositoryObservation(ctx, db.RepoIdentity{
		Platform:       "github",
		PlatformHost:   "github.com",
		PlatformRepoID: "repo-acme-widget-replacement",
		Owner:          "acme",
		Name:           "widget",
	}, time.Now().UTC().Add(time.Second))
	require.NoError(err)
	require.NotNil(newEntry)
	serverfake.SeedPRForRepo(
		t, database, newEntry.Repository.ID,
		"github.com", "acme", "widget", 7,
		serverfake.WithSeedPRTitle("current pull request"),
	)
	serverfake.SeedIssueForRepo(
		t, database, newEntry.Repository.ID,
		"github.com", "acme", "widget", 8, "open", "current issue",
	)

	repos := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/repos", nil)
	require.Equal(http.StatusOK, repos.Code, repos.Body.String())
	var repoBody []map[string]any
	require.NoError(json.NewDecoder(repos.Body).Decode(&repoBody))
	require.Len(repoBody, 1)

	pulls := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/pulls", nil)
	require.Equal(http.StatusOK, pulls.Code, pulls.Body.String())
	var pullBody []pullapi.MergeRequestResponse
	require.NoError(json.NewDecoder(pulls.Body).Decode(&pullBody))
	require.Len(pullBody, 1)
	assert.Equal("current pull request", pullBody[0].Title)
	pullDetail := testutil.DoJSON(
		t, srv, http.MethodGet, "/api/v1/pulls/gh/acme/widget/7", nil)

	require.Equal(http.StatusOK, pullDetail.Code, pullDetail.Body.String())
	var pullDetailBody pullapi.MergeRequestDetailResponse
	require.NoError(json.NewDecoder(pullDetail.Body).Decode(&pullDetailBody))
	require.NotNil(pullDetailBody.MergeRequest)
	assert.Equal("current pull request", pullDetailBody.MergeRequest.Title)

	issues := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/issues", nil)
	require.Equal(http.StatusOK, issues.Code, issues.Body.String())
	var issueBody []issueapi.IssueResponse
	require.NoError(json.NewDecoder(issues.Body).Decode(&issueBody))
	require.Len(issueBody, 1)
	assert.Equal("current issue", issueBody[0].Title)

	historical, err := database.GetMergeRequestByRepoIDAndNumber(
		ctx, oldEntry.Repository.ID, 7,
	)
	require.NoError(err)
	require.NotNil(historical)
	assert.Equal("historical pull request", historical.Title)
}

func TestProviderRefSyncEndpointsUseGitLabNestedRepoPath(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)

	database := dbtest.Open(t)

	ref := platform.RepoRef{
		Platform:           platform.KindGitLab,
		Host:               "gitlab.example.com:8443",
		Owner:              "Group/SubGroup",
		Name:               "Project.Special",
		RepoPath:           "Group/SubGroup/Project.Special",
		PlatformID:         4242,
		PlatformExternalID: "gid://gitlab/Project/4242",
		WebURL:             "https://gitlab.example.com:8443/Group/SubGroup/Project.Special",
		CloneURL:           "https://gitlab.example.com:8443/Group/SubGroup/Project.Special.git",
		DefaultBranch:      "main",
	}
	provider := &serverfake.ApiTestGitLabProvider{
		Ref: ref,
		MergeRequests: []platform.MergeRequest{
			{
				Repo:               ref,
				PlatformID:         7001,
				PlatformExternalID: "gid://gitlab/MergeRequest/7001",
				Number:             7,
				URL:                ref.WebURL + "/-/merge_requests/7",
				Title:              "Sync direct provider MR",
				Author:             "ada",
				State:              "open",
				Body:               "MR body",
				HeadBranch:         "feature/direct",
				BaseBranch:         "main",
				HeadSHA:            "abc123",
				BaseSHA:            "def456",
				CreatedAt:          now,
				UpdatedAt:          now,
				LastActivityAt:     now,
			},
			{
				Repo:               ref,
				PlatformID:         7002,
				PlatformExternalID: "gid://gitlab/MergeRequest/7002",
				Number:             8,
				URL:                ref.WebURL + "/-/merge_requests/8",
				Title:              "Sync async provider MR",
				Author:             "ada",
				State:              "open",
				Body:               "MR body",
				HeadBranch:         "feature/async",
				BaseBranch:         "main",
				HeadSHA:            "abc124",
				BaseSHA:            "def457",
				CreatedAt:          now,
				UpdatedAt:          now,
				LastActivityAt:     now,
			},
		},
		MergeRequestEvents: map[int][]platform.MergeRequestEvent{
			7: {{
				Repo:               ref,
				PlatformID:         9101,
				PlatformExternalID: "gid://gitlab/Note/9101",
				MergeRequestNumber: 7,
				EventType:          "issue_comment",
				Author:             "ada",
				Body:               "Direct MR event",
				CreatedAt:          now.Add(time.Minute),
				DedupeKey:          "gitlab:mr-note:9101",
			}},
			8: {{
				Repo:               ref,
				PlatformID:         9102,
				PlatformExternalID: "gid://gitlab/Note/9102",
				MergeRequestNumber: 8,
				EventType:          "issue_comment",
				Author:             "ada",
				Body:               "Async MR event",
				CreatedAt:          now.Add(2 * time.Minute),
				DedupeKey:          "gitlab:mr-note:9102",
			}},
		},
		Issues: []platform.Issue{
			{
				Repo:               ref,
				PlatformID:         8001,
				PlatformExternalID: "gid://gitlab/Issue/8001",
				Number:             11,
				URL:                ref.WebURL + "/-/issues/11",
				Title:              "Sync direct provider issue",
				Author:             "grace",
				State:              "open",
				Body:               "Issue body",
				CreatedAt:          now,
				UpdatedAt:          now,
				LastActivityAt:     now,
			},
			{
				Repo:               ref,
				PlatformID:         8002,
				PlatformExternalID: "gid://gitlab/Issue/8002",
				Number:             12,
				URL:                ref.WebURL + "/-/issues/12",
				Title:              "Sync async provider issue",
				Author:             "grace",
				State:              "open",
				Body:               "Issue body",
				CreatedAt:          now,
				UpdatedAt:          now,
				LastActivityAt:     now,
			},
		},
		IssueEvents: map[int][]platform.IssueEvent{
			11: {{
				Repo:               ref,
				PlatformID:         9201,
				PlatformExternalID: "gid://gitlab/Note/9201",
				IssueNumber:        11,
				EventType:          "issue_comment",
				Author:             "grace",
				Body:               "Direct issue event",
				CreatedAt:          now.Add(time.Minute),
				DedupeKey:          "gitlab:issue-note:9201",
			}},
			12: {{
				Repo:               ref,
				PlatformID:         9202,
				PlatformExternalID: "gid://gitlab/Note/9202",
				IssueNumber:        12,
				EventType:          "issue_comment",
				Author:             "grace",
				Body:               "Async issue event",
				CreatedAt:          now.Add(2 * time.Minute),
				DedupeKey:          "gitlab:issue-note:9202",
			}},
		},
	}
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)

	repo := ghclient.RepoRef{
		Platform:           platform.KindGitLab,
		Owner:              ref.Owner,
		Name:               ref.Name,
		PlatformHost:       ref.Host,
		RepoPath:           ref.RepoPath,
		PlatformRepoID:     ref.PlatformID,
		PlatformExternalID: ref.PlatformExternalID,
		WebURL:             ref.WebURL,
		CloneURL:           ref.CloneURL,
		DefaultBranch:      ref.DefaultBranch,
	}
	_, err = database.UpsertRepo(ctx, platformdb.DBRepoIdentity(ref))
	require.NoError(err)
	syncer := ghclient.NewSyncerWithRegistry(
		registry, database, nil, []ghclient.RepoRef{repo}, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	t.Cleanup(func() { serverfake.GracefulShutdown(t, srv) })
	client := servertest.SetupTestClient(t, srv)

	providerName := "gitlab"
	providerHost := "gitlab.example.com:8443"
	repoPath := "Group/SubGroup/Project.Special"
	mrDirect := int64(7)
	mrAsync := int64(8)
	issueDirect := int64(11)
	issueAsync := int64(12)

	prResp, err := client.HTTP.SyncPullOnHostWithResponse(ctx, &generated.SyncPullOnHostRequestOptions{PathParams: &generated.SyncPullOnHostPath{PlatformHost: providerHost, Provider: providerName, Owner: "Group/SubGroup", Name: "Project.Special", Number: int64(mrDirect)}})
	require.NoError(err)
	require.Equal(http.StatusOK, prResp.StatusCode, string(prResp.Body))
	require.NotNil(prResp.JSON200)
	assert.Equal("gitlab", prResp.JSON200.Repo.Provider)
	assert.Equal(repoPath, prResp.JSON200.Repo.RepoPath)
	assert.Equal("Sync direct provider MR", prResp.JSON200.MergeRequest.Title)
	assert.Len(prResp.JSON200.Events, 1)

	issueResp, err := client.HTTP.SyncIssueOnHostWithResponse(ctx, &generated.SyncIssueOnHostRequestOptions{PathParams: &generated.SyncIssueOnHostPath{PlatformHost: providerHost, Provider: providerName, Owner: "Group/SubGroup", Name: "Project.Special", Number: int64(issueDirect)}})
	require.NoError(err)
	require.Equal(http.StatusOK, issueResp.StatusCode, string(issueResp.Body))
	require.NotNil(issueResp.JSON200)
	assert.Equal("gitlab", issueResp.JSON200.Repo.Provider)
	assert.Equal(repoPath, issueResp.JSON200.Repo.RepoPath)
	assert.Equal("Sync direct provider issue", issueResp.JSON200.Issue.Title)
	assert.Len(issueResp.JSON200.Events, 1)

	asyncPRResp, err := client.HTTP.EnqueuePrSyncOnHostWithResponse(ctx, &generated.EnqueuePrSyncOnHostRequestOptions{PathParams: &generated.EnqueuePrSyncOnHostPath{PlatformHost: providerHost, Provider: providerName, Owner: "Group/SubGroup", Name: "Project.Special", Number: int64(mrAsync)}})
	require.NoError(err)
	require.Equal(http.StatusAccepted, asyncPRResp.StatusCode, string(asyncPRResp.Body))
	require.Eventually(func() bool {
		repoRow, rowErr := database.GetRepoByIdentity(ctx, platformdb.DBRepoIdentity(ref))
		if rowErr != nil || repoRow == nil {
			return false
		}
		mr, rowErr := database.GetMergeRequestByRepoIDAndNumber(ctx, repoRow.ID, 8)
		return rowErr == nil && mr != nil && mr.Title == "Sync async provider MR"
	}, 2*time.Second, 20*time.Millisecond)

	asyncIssueResp, err := client.HTTP.EnqueueIssueSyncOnHostWithResponse(ctx, &generated.EnqueueIssueSyncOnHostRequestOptions{PathParams: &generated.EnqueueIssueSyncOnHostPath{PlatformHost: providerHost, Provider: providerName, Owner: "Group/SubGroup", Name: "Project.Special", Number: int64(issueAsync)}})
	require.NoError(err)
	require.Equal(http.StatusAccepted, asyncIssueResp.StatusCode, string(asyncIssueResp.Body))
	require.Eventually(func() bool {
		repoRow, rowErr := database.GetRepoByIdentity(ctx, platformdb.DBRepoIdentity(ref))
		if rowErr != nil || repoRow == nil {
			return false
		}
		issue, rowErr := database.GetIssueByRepoIDAndNumber(ctx, repoRow.ID, 12)
		return rowErr == nil && issue != nil && issue.Title == "Sync async provider issue"
	}, 2*time.Second, 20*time.Millisecond)
}

func TestGitLabSyncUsesTagsForRepoOverviewWhenReleasesAreAbsent(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	ctx := t.Context()
	database := dbtest.Open(t)

	ref := platform.RepoRef{
		Platform:           platform.KindGitLab,
		Host:               "gitlab-tags.example.com",
		Owner:              "team",
		Name:               "service",
		RepoPath:           "team/service",
		PlatformID:         5150,
		PlatformExternalID: "gid://gitlab/Project/5150",
		WebURL:             "https://gitlab-tags.example.com/team/service",
		CloneURL:           "https://gitlab-tags.example.com/team/service.git",
		DefaultBranch:      "main",
	}
	provider := &serverfake.ApiTestGitLabProvider{
		Ref: ref,
		Tags: []platform.Tag{{
			Repo: ref,
			Name: "v0.9.0",
			SHA:  "tagsha",
			URL:  "https://gitlab-tags.example.com/team/service/-/tree/v0.9.0",
		}},
	}
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)

	syncer := ghclient.NewSyncerWithRegistry(
		registry,
		database,
		nil,
		[]ghclient.RepoRef{{
			Platform:           platform.KindGitLab,
			Owner:              ref.Owner,
			Name:               ref.Name,
			PlatformHost:       ref.Host,
			RepoPath:           ref.RepoPath,
			PlatformRepoID:     ref.PlatformID,
			PlatformExternalID: ref.PlatformExternalID,
			WebURL:             ref.WebURL,
			CloneURL:           ref.CloneURL,
			DefaultBranch:      ref.DefaultBranch,
		}},
		time.Minute,
		nil,
		nil,
	)
	t.Cleanup(syncer.Stop)
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	t.Cleanup(func() { serverfake.GracefulShutdown(t, srv) })
	client := servertest.SetupTestClient(t, srv)

	syncer.RunOnce(ctx)

	resp, err := client.HTTP.ListRepoSummariesWithResponse(ctx)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.Len(*resp.JSON200, 1)
	require.NotNil((*resp.JSON200)[0].LatestRelease)

	assert := assert.New(t)
	assert.Equal("v0.9.0", (*resp.JSON200)[0].LatestRelease.TagName)
	assert.Equal("https://gitlab-tags.example.com/team/service/-/tree/v0.9.0", (*resp.JSON200)[0].LatestRelease.URL)
}

func TestAPIListRepoSummariesIncludesSyncedReleaseTimeline(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	serverfake.AcquireRootWorkspaceGitSlot(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()
	dir := t.TempDir()
	database := dbtest.Open(t)

	remote := filepath.Join(dir, "remote.git")
	work := filepath.Join(dir, "work")
	gitfixture.Run(t, dir, "init", "--bare", "--initial-branch=main", remote)
	gitfixture.Run(t, dir, "clone", remote, work)
	gitfixture.Run(t, work, "config", "user.email", "test@test.com")
	gitfixture.Run(t, work, "config", "user.name", "Test")

	commitFile := func(name, message string) {
		t.Helper()
		require.NoError(os.WriteFile(
			filepath.Join(work, name),
			[]byte(message+"\n"),
			0o644,
		))
		gitfixture.Run(t, work, "add", ".")
		gitfixture.Run(t, work, "commit", "-m", message)
	}

	commitFile("base.txt", "release v1")
	gitfixture.Run(t, work, "tag", "v1.0.0")
	commitFile("v2.txt", "prepare v2")
	gitfixture.Run(t, work, "tag", "v2.0.0")
	commitFile("v3.txt", "prepare v3")
	gitfixture.Run(t, work, "tag", "v3.0.0")
	commitFile("post-1.txt", "post latest 1")
	commitFile("post-2.txt", "post latest 2")
	gitfixture.Run(t, work, "push", "--tags", "origin", "main")

	clones := gitclone.New(filepath.Join(dir, "clones"), nil)
	clonePath, err := clones.ClonePathForContext(
		gitclone.WithRepositoryIdentity(ctx, "repo-acme-widgets"),
		"github", "github.com", "acme", "widgets",
	)
	require.NoError(err)
	require.NoError(os.MkdirAll(filepath.Dir(clonePath), 0o755))
	gitfixture.Run(t, dir, "clone", "--bare", remote, clonePath)

	releaseForTag := func(tag string, publishedAt time.Time) *gh.RepositoryRelease {
		t.Helper()
		name := "Release " + tag
		url := "https://github.com/acme/widgets/releases/tag/" + tag
		return &gh.RepositoryRelease{
			TagName:         tag,
			Name:            &name,
			HTMLURL:         url,
			TargetCommitish: "main",
			Prerelease:      false,
			Draft:           false,
			PublishedAt:     &gh.Timestamp{Time: publishedAt},
		}
	}

	releases := []*gh.RepositoryRelease{
		releaseForTag("v3.0.0", time.Date(2026, 4, 3, 12, 0, 0, 0, time.UTC)),
		releaseForTag("v2.0.0", time.Date(2026, 4, 2, 12, 0, 0, 0, time.UTC)),
		releaseForTag("v1.0.0", time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)),
	}
	mock := &serverfake.MockGH{
		ListReleasesFn: func(
			_ context.Context, owner, repo string, perPage int,
		) ([]*gh.RepositoryRelease, error) {
			assert.Equal("acme", owner)
			assert.Equal("widgets", repo)
			assert.Equal(10, perPage)
			return releases, nil
		},
	}
	repos := []ghclient.RepoRef{{
		Owner: "acme", Name: "widgets", PlatformHost: "github.com",
	}}
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": mock},
		database, clones, repos, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)

	syncer.RunOnce(ctx)

	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{Clones: clones})
	t.Cleanup(func() { serverfake.GracefulShutdown(t, srv) })
	client := servertest.SetupTestClient(t, srv)

	resp, err := client.HTTP.ListRepoSummariesWithResponse(ctx)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.Len(*resp.JSON200, 1)

	widgets := (*resp.JSON200)[0]
	require.NotNil(widgets.LatestRelease)
	require.NotNil(widgets.Releases)
	require.NotNil(widgets.CommitsSinceRelease)
	require.NotNil(widgets.CommitTimeline)
	require.NotNil(widgets.TimelineUpdatedAt)

	assert.Equal("v3.0.0", widgets.LatestRelease.TagName)
	assert.Len(widgets.Releases, 3)
	assert.Equal("v1.0.0", widgets.Releases[2].TagName)
	assert.Equal(int64(2), *widgets.CommitsSinceRelease)
	assert.Len(widgets.CommitTimeline, 4)
	assert.Equal("post latest 2", widgets.CommitTimeline[0].Message)
	assert.Equal("post latest 1", widgets.CommitTimeline[1].Message)
	assert.Len(widgets.CommitTimeline[0].Sha, 40)

	gitfixture.Run(t, work, "tag", "-f", "v3.0.0", "HEAD")
	gitfixture.Run(t, work, "tag", "-f", "v1.0.0", "HEAD")
	gitfixture.Run(t, work, "push", "--force", "origin", "refs/tags/v3.0.0")
	gitfixture.Run(t, work, "push", "--force", "origin", "refs/tags/v1.0.0")
	syncer.RunOnce(ctx)

	resp, err = client.HTTP.ListRepoSummariesWithResponse(ctx)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.Len(*resp.JSON200, 1)

	widgets = (*resp.JSON200)[0]
	require.NotNil(widgets.LatestRelease)
	require.NotNil(widgets.CommitsSinceRelease)
	require.NotNil(widgets.CommitTimeline)
	assert.Equal("v3.0.0", widgets.LatestRelease.TagName)
	assert.Equal(int64(0), *widgets.CommitsSinceRelease)
	assert.Empty(widgets.CommitTimeline)
}

func TestAPIListRepoSummariesUsesTagsWhenNoReleases(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()
	database := dbtest.Open(t)

	tagName := "v0.5.0"
	sha := "1234567890abcdef1234567890abcdef12345678"
	mock := &serverfake.MockGH{
		ListReleasesFn: func(
			_ context.Context, owner, repo string, perPage int,
		) ([]*gh.RepositoryRelease, error) {
			assert.Equal("acme", owner)
			assert.Equal("tagged", repo)
			assert.Equal(10, perPage)
			return nil, nil
		},
		ListTagsFn: func(
			_ context.Context, owner, repo string, perPage int,
		) ([]*gh.RepositoryTag, error) {
			assert.Equal("acme", owner)
			assert.Equal("tagged", repo)
			assert.Equal(3, perPage)
			return []*gh.RepositoryTag{{
				Name: &tagName,
				Commit: &gh.Commit{
					SHA: &sha,
				},
			}}, nil
		},
	}
	repos := []ghclient.RepoRef{{
		Owner: "acme", Name: "tagged", PlatformHost: "github.com",
	}}
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": mock},
		database, nil, repos, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)

	syncer.RunOnce(ctx)

	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	t.Cleanup(func() { serverfake.GracefulShutdown(t, srv) })
	client := servertest.SetupTestClient(t, srv)

	resp, err := client.HTTP.ListRepoSummariesWithResponse(ctx)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.Len(*resp.JSON200, 1)

	tagged := (*resp.JSON200)[0]
	require.NotNil(tagged.LatestRelease)
	require.NotNil(tagged.Releases)

	assert.Equal("v0.5.0", tagged.LatestRelease.TagName)
	assert.Equal("v0.5.0", tagged.LatestRelease.Name)
	assert.Equal("https://github.com/acme/tagged/tree/v0.5.0", tagged.LatestRelease.URL)
	assert.Equal(sha, tagged.LatestRelease.TargetCommitish)
	assert.Nil(tagged.LatestRelease.PublishedAt)
	assert.False(tagged.LatestRelease.Prerelease)
	assert.Len(tagged.Releases, 1)
	assert.Equal("v0.5.0", tagged.Releases[0].TagName)
	assert.Nil(tagged.CommitsSinceRelease)
	assert.Empty(tagged.CommitTimeline)
}

func TestAPIListRepoSummariesClearsStaleOverviewWhenTagFallbackFails(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()

	database := dbtest.Open(t)

	repoID, err := database.UpsertRepo(ctx, serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "tagless"))
	require.NoError(err)

	publishedAt := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	timelineUpdatedAt := time.Date(2026, 3, 2, 12, 0, 0, 0, time.UTC)
	commitsSince := 9
	err = database.UpsertRepoOverview(ctx, repoID, db.RepoOverview{
		LatestRelease: &db.RepoRelease{
			TagName:     "v1.0.0",
			Name:        "Version 1.0.0",
			URL:         "https://github.com/acme/tagless/releases/tag/v1.0.0",
			PublishedAt: &publishedAt,
		},
		Releases: []db.RepoRelease{{
			TagName:     "v1.0.0",
			Name:        "Version 1.0.0",
			URL:         "https://github.com/acme/tagless/releases/tag/v1.0.0",
			PublishedAt: &publishedAt,
		}},
		CommitsSinceRelease: &commitsSince,
		CommitTimeline: []db.RepoCommitTimelinePoint{{
			SHA:         "abc123",
			Message:     "Old release timeline",
			CommittedAt: time.Date(2026, 3, 2, 10, 0, 0, 0, time.UTC),
		}},
		TimelineUpdatedAt: &timelineUpdatedAt,
	})
	require.NoError(err)

	mock := &serverfake.MockGH{
		ListReleasesFn: func(
			_ context.Context, owner, repo string, perPage int,
		) ([]*gh.RepositoryRelease, error) {
			assert.Equal("acme", owner)
			assert.Equal("tagless", repo)
			assert.Equal(10, perPage)
			return []*gh.RepositoryRelease{}, nil
		},
		ListTagsFn: func(
			_ context.Context, owner, repo string, perPage int,
		) ([]*gh.RepositoryTag, error) {
			assert.Equal("acme", owner)
			assert.Equal("tagless", repo)
			assert.Equal(3, perPage)
			return nil, errors.New("tags unavailable")
		},
	}
	repos := []ghclient.RepoRef{{
		Owner: "acme", Name: "tagless", PlatformHost: "github.com",
	}}
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": mock},
		database, nil, repos, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)

	syncer.RunOnce(ctx)

	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	t.Cleanup(func() { serverfake.GracefulShutdown(t, srv) })
	client := servertest.SetupTestClient(t, srv)

	resp, err := client.HTTP.ListRepoSummariesWithResponse(ctx)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.Len(*resp.JSON200, 1)

	tagless := (*resp.JSON200)[0]
	require.NotNil(tagless.Releases)
	require.NotNil(tagless.CommitTimeline)

	assert.Equal("acme", tagless.Owner)
	assert.Equal("tagless", tagless.Name)
	assert.Nil(tagless.LatestRelease)
	assert.Empty(tagless.Releases)
	assert.Nil(tagless.CommitsSinceRelease)
	assert.Empty(tagless.CommitTimeline)
	assert.Nil(tagless.TimelineUpdatedAt)
}

func TestAPITriggerSyncIgnoresRequestCancellation(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	database := dbtest.Open(t)

	syncReachedGitHub := make(chan struct{})
	var syncReachedGitHubOnce sync.Once
	mock := &serverfake.MockGH{
		ListOpenPullRequestsFn: func(
			_ context.Context, _, _ string,
		) ([]*gh.PullRequest, error) {
			syncReachedGitHubOnce.Do(func() { close(syncReachedGitHub) })
			return nil, nil
		},
	}
	syncer := ghclient.NewSyncer(map[string]ghclient.Client{"github.com": mock}, database, nil, []ghclient.RepoRef{{
		Owner:        "acme",
		Name:         "widget",
		PlatformHost: "github.com",
	}}, time.Minute, nil, nil)
	t.Cleanup(func() { syncer.Stop() })
	srv := server.New(
		database, syncer, nil, "/",
		nil, server.ServerOptions{},
	)
	t.Cleanup(syncer.Stop)

	ctx, cancel := context.WithCancel(t.Context())
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/sync", nil).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	cancel()

	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	require.Equal(http.StatusAccepted, rr.Code, rr.Body.String())

	select {
	case <-syncReachedGitHub:
	case <-time.After(2 * time.Second):
		require.Fail("expected sync to reach GitHub despite request context cancellation")
	}

	repos, err := database.ListRepos(t.Context())
	require.NoError(err)
	require.Len(repos, 1)
	assert.Equal(t, "acme", repos[0].Owner)
	assert.Equal(t, "widget", repos[0].Name)
}

// If the route returns 202 after admission is refused, the client believes a
// sync was retained even though no worker can execute it.
func TestAPITriggerSyncRejectsAfterSyncerStops(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	database := dbtest.Open(t)
	syncer := ghclient.NewSyncer(nil, database, nil, nil, time.Minute, nil, nil)
	syncer.Stop()

	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	t.Cleanup(func() { serverfake.GracefulShutdown(t, srv) })
	rr := testutil.DoJSON(t, srv, http.MethodPost, "/api/v1/sync", nil)
	require.Equal(http.StatusServiceUnavailable, rr.Code, rr.Body.String())
}

func TestAPITriggerSyncOnlyRepoRestrictsRun(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)

	database := dbtest.Open(t)
	var mu sync.Mutex
	var calls []string
	mock := &serverfake.MockGH{
		ListOpenPullRequestsFn: func(
			_ context.Context, owner, repo string,
		) ([]*gh.PullRequest, error) {
			mu.Lock()
			defer mu.Unlock()
			calls = append(calls, owner+"/"+repo)
			return nil, nil
		},
	}
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": mock},
		database,
		nil,
		[]ghclient.RepoRef{
			{Platform: platform.KindGitHub, Owner: "acme", Name: "first", PlatformHost: "github.com"},
			{Platform: platform.KindGitHub, Owner: "acme", Name: "second", PlatformHost: "github.com"},
		},
		time.Minute,
		nil,
		nil,
	)
	done := make(chan struct{}, 1)
	syncer.SetOnStatusChange(func(status *ghclient.SyncStatus) {
		if !status.Running {
			select {
			case done <- struct{}{}:
			default:
			}
		}
	})
	t.Cleanup(syncer.Stop)
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	t.Cleanup(func() { serverfake.GracefulShutdown(t, srv) })

	rr := testutil.DoJSON(
		t,
		srv,
		http.MethodPost,
		"/api/v1/sync?only_repo=gh|github.com/acme/second",
		nil)

	require.Equal(http.StatusAccepted, rr.Code, rr.Body.String())
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		require.Fail("expected repository-only sync to complete")
	}

	mu.Lock()
	got := slices.Clone(calls)
	mu.Unlock()
	assert.Equal([]string{"acme/second"}, got)
}

func TestAPITriggerSyncRejectsUnknownOnlyRepo(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)

	srv, _ := servertest.SetupTestServer(t)
	rr := testutil.DoJSON(
		t,
		srv,
		http.MethodPost,
		"/api/v1/sync?only_repo=github|github.com/acme/missing",
		nil)

	require.Equal(http.StatusBadRequest, rr.Code, rr.Body.String())

	var problem httpapi.ProblemError
	require.NoError(json.NewDecoder(rr.Body).Decode(&problem))
	assert.Equal(httpapi.CodeValidationError, problem.Code)
	assert.Equal("query.only_repo", problem.Details["field"])
}

func TestAPITriggerSyncBypassesNextSyncAfter(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)

	database := dbtest.Open(t)

	var listCalls atomic.Int32
	secondSync := make(chan struct{})
	var secondSyncOnce sync.Once
	mock := &serverfake.MockGH{
		ListOpenPullRequestsFn: func(
			_ context.Context, _, _ string,
		) ([]*gh.PullRequest, error) {
			if listCalls.Add(1) == 2 {
				secondSyncOnce.Do(func() { close(secondSync) })
			}
			return nil, nil
		},
	}
	trackers := map[string]*ghclient.RateTracker{
		"github.com": ghclient.NewRateTracker(
			database, "github.com", "host", "rest",
		),
	}
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": mock},
		database,
		nil,
		[]ghclient.RepoRef{{
			Owner:        "acme",
			Name:         "widget",
			PlatformHost: "github.com",
		}},
		time.Minute,
		trackers,
		nil,
	)
	t.Cleanup(syncer.Stop)

	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	t.Cleanup(func() { serverfake.GracefulShutdown(t, srv) })

	// Seed the host cooldown window exactly like a recent background sync.
	syncer.RunOnce(t.Context())
	require.Equal(int32(1), listCalls.Load())

	client := servertest.SetupTestClient(t, srv)
	resp, err := client.HTTP.TriggerSyncWithResponse(t.Context(), &generated.TriggerSyncRequestOptions{})
	require.NoError(err)
	require.Equal(http.StatusAccepted, resp.StatusCode)

	select {
	case <-secondSync:
	case <-time.After(2 * time.Second):
		require.Fail("expected explicit sync request to bypass background cooldown")
	}
}

func TestAPIMarkDraftDoesNotGetRevertedByStaleSync(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)

	staleUpdatedAt := time.Date(2026, 4, 12, 1, 0, 0, 0, time.UTC)
	syncStarted := make(chan struct{}, 1)
	releaseSync := make(chan struct{})
	mock := &serverfake.MockGH{
		GetPullRequestFn: func(_ context.Context, owner, repo string, number int) (*gh.PullRequest, error) {
			syncStarted <- struct{}{}
			<-releaseSync

			id := int64(101)
			state := "open"
			title := "stale sync"
			url := "https://github.com/acme/widget/pull/1"
			author := "alice"
			draft := false
			headSHA := "abc123"
			baseSHA := "def456"
			featureRef := "feature"
			mainRef := "main"
			createdAt := gh.Timestamp{Time: staleUpdatedAt.Add(-time.Hour)}
			updatedAt := gh.Timestamp{Time: staleUpdatedAt}
			return &gh.PullRequest{
				ID:        &id,
				Number:    &number,
				State:     &state,
				Title:     &title,
				HTMLURL:   &url,
				Draft:     &draft,
				User:      &gh.User{Login: &author},
				CreatedAt: &createdAt,
				UpdatedAt: &updatedAt,
				Head:      &gh.PullRequestBranch{SHA: &headSHA, Ref: &featureRef},
				Base:      &gh.PullRequestBranch{SHA: &baseSHA, Ref: &mainRef},
			}, nil
		},
		ConvertToDraftFn: func(_ context.Context, owner, repo string, number int) (*gh.PullRequest, error) {
			id := int64(101)
			state := "open"
			draft := true
			updatedAt := gh.Timestamp{Time: staleUpdatedAt.Add(time.Minute)}
			return &gh.PullRequest{
				ID:        &id,
				Number:    &number,
				State:     &state,
				Draft:     &draft,
				UpdatedAt: &updatedAt,
			}, nil
		},
	}

	srv, database := servertest.SetupTestServerWithMock(t, mock)
	client := servertest.SetupTestClient(t, srv)

	repoID, err := database.UpsertRepo(t.Context(), serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)

	prID, err := database.UpsertMergeRequest(t.Context(), &db.MergeRequest{
		RepoID:          repoID,
		PlatformID:      101,
		Number:          1,
		URL:             "https://github.com/acme/widget/pull/1",
		Title:           "ready PR",
		Author:          "alice",
		State:           "open",
		IsDraft:         false,
		Body:            "",
		HeadBranch:      "feature",
		BaseBranch:      "main",
		PlatformHeadSHA: "abc123",
		PlatformBaseSHA: "def456",
		Additions:       0,
		Deletions:       0,
		CommentCount:    0,
		ReviewDecision:  "",
		CIStatus:        "",
		CreatedAt:       staleUpdatedAt.Add(-time.Hour),
		UpdatedAt:       staleUpdatedAt,
		LastActivityAt:  staleUpdatedAt,
	})
	require.NoError(err)
	require.NoError(database.EnsureKanbanState(t.Context(), prID))

	syncDone := make(chan *generated.SyncPullResp, 1)
	syncErr := make(chan error, 1)
	go func() {
		resp, err := client.HTTP.SyncPullWithResponse(t.Context(), &generated.SyncPullRequestOptions{PathParams: &generated.SyncPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
		if err != nil {
			syncErr <- err
			return
		}
		syncDone <- resp
	}()

	<-syncStarted

	resp, err := client.HTTP.SetPrGithubStateWithResponse(t.Context(), &generated.SetPrGithubStateRequestOptions{PathParams: &generated.SetPrGithubStatePath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}, Body: &generated.SetPrGithubStateBody{State: "draft"}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)

	draftPR, err := database.GetMergeRequest(t.Context(), "github", "github.com", "acme", "widget", 1)
	require.NoError(err)
	require.True(draftPR.IsDraft)
	assert.True(draftPR.UpdatedAt.After(staleUpdatedAt))

	close(releaseSync)

	completed := false
	select {
	case err := <-syncErr:
		require.NoError(err)
		completed = true
	case resp := <-syncDone:
		require.Equal(http.StatusOK, resp.StatusCode)
		completed = true
	case <-time.After(5 * time.Second):
	}
	require.True(completed, "timed out waiting for stale draft sync")

	finalPR, err := database.GetMergeRequest(t.Context(), "github", "github.com", "acme", "widget", 1)
	require.NoError(err)
	assert.True(finalPR.IsDraft)
	assert.Equal("ready PR", finalPR.Title)
	assert.True(finalPR.UpdatedAt.Equal(draftPR.UpdatedAt))
}

func TestResolveItem_UsesItemTypeHintForGitLab(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	repos := []ghclient.RepoRef{{
		Platform:     platform.KindGitLab,
		PlatformHost: "gitlab.example.com",
		Owner:        "group",
		Name:         "project",
	}}
	srv, database := servertest.SetupTestServerWithRepos(t, &serverfake.MockGH{}, repos)
	repoID, err := database.UpsertRepo(t.Context(), db.RepoIdentity{
		Platform:     "gitlab",
		PlatformHost: "gitlab.example.com",
		Owner:        "group",
		Name:         "project",
	})
	require.NoError(err)
	now := time.Now().UTC().Truncate(time.Second)
	_, err = database.UpsertMergeRequest(t.Context(), &db.MergeRequest{
		RepoID:         repoID,
		PlatformID:     10000,
		Number:         10,
		URL:            "https://gitlab.example.com/group/project/-/merge_requests/10",
		Title:          "Test MR",
		Author:         "testuser",
		State:          "open",
		HeadBranch:     "feature",
		BaseBranch:     "main",
		CreatedAt:      now,
		UpdatedAt:      now,
		LastActivityAt: now,
	})
	require.NoError(err)
	_, err = database.UpsertIssue(t.Context(), &db.Issue{
		RepoID:         repoID,
		PlatformID:     10001,
		Number:         10,
		URL:            "https://gitlab.example.com/group/project/-/issues/10",
		Title:          "Test Issue",
		Author:         "testuser",
		State:          "open",
		CreatedAt:      now,
		UpdatedAt:      now,
		LastActivityAt: now,
	})
	require.NoError(err)
	client := servertest.SetupTestClient(t, srv)
	itemType := generated.ResolveRepoItemOnHostQueryItemTypeIssue

	resp, err := client.HTTP.ResolveRepoItemOnHostWithResponse(t.Context(), &generated.ResolveRepoItemOnHostRequestOptions{PathParams: &generated.ResolveRepoItemOnHostPath{PlatformHost: "gitlab.example.com", Provider: "gitlab", Owner: "group", Name: "project", Number: int64(10)}, Query: &generated.ResolveRepoItemOnHostQuery{ItemType: &itemType}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.Equal("issue", resp.JSON200.ItemType)
	require.EqualValues(10, resp.JSON200.Number)
	require.True(resp.JSON200.RepoTracked)
}

func TestResolveItem_NotFoundOnGitHub(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	mock := &serverfake.MockGH{
		GetIssueFn: func(_ context.Context, _, _ string, _ int) (*gh.Issue, error) {
			return nil, &gh.ErrorResponse{
				Response: &http.Response{StatusCode: 404},
				Message:  "Not Found",
			}
		},
	}
	repos := []ghclient.RepoRef{{Owner: "acme", Name: "widget"}}
	srv, database := servertest.SetupTestServerWithRepos(t, mock, repos)
	_, err := database.UpsertRepo(t.Context(), serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	client := servertest.SetupTestClient(t, srv)

	resp, err := client.HTTP.ResolveRepoItemWithResponse(t.Context(), &generated.ResolveRepoItemRequestOptions{PathParams: &generated.ResolveRepoItemPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(999)}})
	require.Error(err)
	require.NotNil(resp)
	require.Equal(http.StatusNotFound, resp.StatusCode)
}

func TestResolveItem_GitHubServerError(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	mock := &serverfake.MockGH{
		GetIssueFn: func(_ context.Context, _, _ string, _ int) (*gh.Issue, error) {
			return nil, &gh.ErrorResponse{
				Response: &http.Response{StatusCode: 500},
				Message:  "Internal Server Error",
			}
		},
	}
	repos := []ghclient.RepoRef{{Owner: "acme", Name: "widget"}}
	srv, database := servertest.SetupTestServerWithRepos(t, mock, repos)
	_, err := database.UpsertRepo(t.Context(), serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	client := servertest.SetupTestClient(t, srv)

	resp, err := client.HTTP.ResolveRepoItemWithResponse(t.Context(), &generated.ResolveRepoItemRequestOptions{PathParams: &generated.ResolveRepoItemPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(999)}})
	require.Error(err)
	require.NotNil(resp)
	require.Equal(http.StatusBadGateway, resp.StatusCode)
}

func TestAPICapabilityGatedRouteReturnsLookupProblemBeforeCapabilityProblem(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	srv, _ := servertest.SetupTestServer(t)

	tests := []struct {
		name     string
		path     string
		wantCode int
		wantWire string
	}{
		{
			name:     "unknown repo",
			path:     "/api/v1/pulls/gh/acme/unknown/7",
			wantCode: http.StatusNotFound,
			wantWire: "repoNotFound",
		},
		{
			name:     "invalid provider",
			path:     "/api/v1/pulls/not-a-provider/acme/widget/7",
			wantCode: http.StatusBadRequest,
			wantWire: "badRequest",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			assert := assert.New(t)

			rr := testutil.DoJSON(
				t,
				srv,
				http.MethodPatch,
				tt.path,
				map[string]string{"title": "Updated title"})

			require.Equal(tt.wantCode, rr.Code, rr.Body.String())

			var problem serverfake.RawProblemDetail
			require.NoError(json.NewDecoder(rr.Body).Decode(&problem))
			assert.Equal(tt.wantWire, problem.Code)
		})
	}
}

func TestAPICapabilityGatedMutationsHandleMissingSyncer(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	database := dbtest.Open(t)
	srv := server.New(database, nil, nil, "/", nil, server.ServerOptions{})
	t.Cleanup(func() { serverfake.GracefulShutdown(t, srv) })
	serverfake.SeedPR(t, database, "acme", "widget", 7)
	serverfake.SeedIssue(t, database, "acme", "widget", 11, "open")

	tests := []struct {
		name       string
		method     string
		path       string
		body       any
		capability string
	}{
		{
			name:       "PR content",
			method:     http.MethodPatch,
			path:       "/api/v1/pulls/gh/acme/widget/7",
			body:       map[string]string{"title": "Updated title"},
			capability: "state_mutation",
		},
		{
			name:       "issue content",
			method:     http.MethodPatch,
			path:       "/api/v1/issues/gh/acme/widget/11",
			body:       map[string]string{"title": "Updated title"},
			capability: "state_mutation",
		},
		{
			name:       "PR comment",
			method:     http.MethodPost,
			path:       "/api/v1/pulls/gh/acme/widget/7/comments",
			body:       map[string]string{"body": "hello"},
			capability: "comment_mutation",
		},
		{
			name:       "issue comment",
			method:     http.MethodPost,
			path:       "/api/v1/issues/gh/acme/widget/11/comments",
			body:       map[string]string{"body": "hello"},
			capability: "comment_mutation",
		},
		{
			name:       "issue creation",
			method:     http.MethodPost,
			path:       "/api/v1/issues/gh/acme/widget",
			body:       map[string]string{"title": "New issue", "body": "Issue body"},
			capability: "issue_mutation",
		},
		{
			name:       "review approval",
			method:     http.MethodPost,
			path:       "/api/v1/pulls/gh/acme/widget/7/approve",
			body:       map[string]string{"body": "looks good"},
			capability: "review_mutation",
		},
		{
			name:       "request changes",
			method:     http.MethodPost,
			path:       "/api/v1/pulls/gh/acme/widget/7/request-changes",
			body:       map[string]string{"body": "needs work"},
			capability: "review_mutation",
		},
		{
			name:       "workflow approval",
			method:     http.MethodPost,
			path:       "/api/v1/pulls/gh/acme/widget/7/approve-workflows",
			body:       nil,
			capability: "workflow_approval",
		},
		{
			name:       "ready for review",
			method:     http.MethodPost,
			path:       "/api/v1/pulls/gh/acme/widget/7/ready-for-review",
			body:       nil,
			capability: "ready_for_review",
		},
		{
			name:   "merge",
			method: http.MethodPost,
			path:   "/api/v1/pulls/gh/acme/widget/7/merge",
			body: map[string]string{
				"method":         "squash",
				"commit_title":   "Merge PR",
				"commit_message": "Merge PR",
			},
			capability: "merge_mutation",
		},
		{
			name:       "PR state",
			method:     http.MethodPost,
			path:       "/api/v1/pulls/gh/acme/widget/7/github-state",
			body:       map[string]string{"state": "closed"},
			capability: "state_mutation",
		},
		{
			name:       "issue state",
			method:     http.MethodPost,
			path:       "/api/v1/issues/gh/acme/widget/11/github-state",
			body:       map[string]string{"state": "closed"},
			capability: "state_mutation",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)

			rr := testutil.DoJSON(t, srv, tt.method, tt.path, tt.body)
			require.Equal(http.StatusConflict, rr.Code, rr.Body.String())
			assertUnsupportedCapabilityProblem(
				t, rr.Body, "github", "github.com", tt.capability,
			)
		})
	}
}

// TestAPIRateLimitedEnvelope drives a provider mutation through a fake
// gitlab provider that returns a platform.Error with ErrCodeRateLimited
// and a known ResetAt. The handler routes the failure through
// providerCallProblem / mapPlatformError, which builds the rateLimited
// problem with details.retryAfter populated as an RFC 3339 string.
func TestAPIRateLimitedEnvelope(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	reset := time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC)
	srv := servertest.SetupGitLabIssueMutatorServer(t, &platform.Error{
		Code:         platform.ErrCodeRateLimited,
		Provider:     platform.KindGitLab,
		PlatformHost: "gitlab.example.com",
		ResetAt:      &reset,
	})

	tests := []struct {
		name   string
		method string
		path   string
		body   any
	}{
		{
			name:   "issue create",
			method: http.MethodPost,
			path:   "/api/v1/host/gitlab.example.com/issues/gl/group/project",
			body:   map[string]string{"title": "Rate limited", "body": "test"},
		},
		{
			name:   "pull content edit",
			method: http.MethodPatch,
			path:   "/api/v1/host/gitlab.example.com/pulls/gl/group/project/7",
			body:   map[string]string{"title": "Rate limited"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			assert := assert.New(t)

			rr := testutil.DoJSON(t, srv, tt.method, tt.path, tt.body)
			require.Equal(http.StatusTooManyRequests, rr.Code, rr.Body.String())

			var problem serverfake.RawProblemDetail
			require.NoError(json.NewDecoder(rr.Body).Decode(&problem))
			assert.Equal("rateLimited", problem.Code)
			require.NotNil(problem.Details)
			assert.Equal("gitlab", problem.Details["provider"])
			assert.Equal("gitlab.example.com", problem.Details["platformHost"])
			retryAfter, ok := problem.Details["retryAfter"].(string)
			require.True(
				ok,
				"details.retryAfter must be a string, got %T",
				problem.Details["retryAfter"],
			)
			parsed, parseErr := time.Parse(time.RFC3339, retryAfter)
			require.NoError(parseErr)
			assert.Equal(reset.UTC(), parsed.UTC())
		})
	}
}

// TestAPIValidationErrorEnvelope sends an invalid kanban status and
// expects the typed validationError envelope with details.field and
// details.allowed.
func TestAPIValidationErrorEnvelope(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)

	srv, database := servertest.SetupTestServer(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)
	repoID, err := database.UpsertRepo(ctx, db.RepoIdentity{
		Platform:     "github",
		PlatformHost: "github.com",
		Owner:        "acme",
		Name:         "widget",
	})
	require.NoError(err)
	_, err = database.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID:         repoID,
		PlatformID:     7777,
		Number:         42,
		URL:            "https://github.com/acme/widget/pull/42",
		Title:          "Validation test",
		Author:         "alice",
		State:          "open",
		HeadBranch:     "feature",
		BaseBranch:     "main",
		CreatedAt:      now,
		UpdatedAt:      now,
		LastActivityAt: now,
	})
	require.NoError(err)

	rr := testutil.DoJSON(
		t,
		srv,
		http.MethodPut,
		"/api/v1/pulls/gh/acme/widget/42/state",
		map[string]string{"status": "frobnicated"})

	require.Equal(http.StatusBadRequest, rr.Code, rr.Body.String())

	var problem serverfake.RawProblemDetail
	require.NoError(json.NewDecoder(rr.Body).Decode(&problem))
	assert.Equal("validationError", problem.Code)
	require.NotNil(problem.Details)
	assert.Equal("body.status", problem.Details["field"])
	allowed, ok := problem.Details["allowed"].([]any)
	require.True(ok, "details.allowed must be an array, got %T", problem.Details["allowed"])
	expected := []any{"new", "reviewing", "waiting", "awaiting_merge"}
	assert.Equal(expected, allowed)
}

// TestAPIResolveItemMapsLookupOutcomes drives GitHub item resolution
// (/repo/.../resolve/{number}) through a mock client whose type-probe fetch
// reports a removed, inaccessible, or transferred item. The problem envelope
// must carry the classified outcome — not a raw upstream failure or a 500 —
// and the moved outcome must include the destination extension members.
func TestAPIResolveItemMapsLookupOutcomes(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	const movedRepoAPIURL = "https://api.github.com/repos/newowner/newname"

	statusErr := func(status int) error {
		return &gh.ErrorResponse{
			Response: &http.Response{StatusCode: status, Header: http.Header{}},
		}
	}

	cases := []struct {
		name            string
		getIssueErr     error
		wantStatus      int
		wantCode        string
		wantDestination bool
	}{
		{
			name:        "removed",
			getIssueErr: statusErr(http.StatusNotFound),
			wantStatus:  http.StatusNotFound,
			wantCode:    "notFound",
		},
		{
			name:        "inaccessible",
			getIssueErr: statusErr(http.StatusForbidden),
			wantStatus:  http.StatusForbidden,
			wantCode:    "forbidden",
		},
		{
			name:            "moved",
			wantStatus:      http.StatusNotFound,
			wantCode:        "notFound",
			wantDestination: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			assert := assert.New(t)
			ctx := t.Context()

			mock := &serverfake.MockGH{
				GetIssueFn: func(context.Context, string, string, int) (*gh.Issue, error) {
					if tc.getIssueErr != nil {
						return nil, tc.getIssueErr
					}
					return &gh.Issue{
						Number:        new(5),
						RepositoryURL: new(movedRepoAPIURL),
					}, nil
				},
			}
			srv, database := servertest.SetupTestServerWithMock(t, mock)
			_, err := database.UpsertRepo(
				ctx, serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "widget"),
			)
			require.NoError(err)

			rr := testutil.DoJSON(t, srv, http.MethodPost, "/api/v1/repo/gh/acme/widget/resolve/5", nil)
			require.Equal(tc.wantStatus, rr.Code, rr.Body.String())

			var problem serverfake.RawProblemDetail
			require.NoError(json.NewDecoder(rr.Body).Decode(&problem))
			assert.Equal(tc.wantCode, problem.Code)
			if !tc.wantDestination {
				return
			}
			require.NotNil(problem.Details)
			assert.Equal("github", problem.Details["destinationProvider"])
			assert.Equal("github.com", problem.Details["destinationPlatformHost"])
			assert.Equal("newowner", problem.Details["destinationOwner"])
			assert.Equal("newname", problem.Details["destinationName"])
		})
	}
}

// movedLookupGitLabProvider embeds apiTestGitLabProvider but reports every
// single-item read as moved to another repository via the supplied
// platform.Error. Used by TestAPIMovedLookupProblemCarriesDestination.
type movedLookupGitLabProvider struct {
	serverfake.ApiTestGitLabProvider
	lookupErr error
}

func (p *movedLookupGitLabProvider) GetIssue(
	_ context.Context,
	_ platform.RepoRef,
	_ int,
) (platform.Issue, error) {
	return platform.Issue{}, p.lookupErr
}

func (p *movedLookupGitLabProvider) GetMergeRequest(
	_ context.Context,
	_ platform.RepoRef,
	_ int,
) (platform.MergeRequest, error) {
	return platform.MergeRequest{}, p.lookupErr
}

// TestAPIMovedLookupProblemCarriesDestination drives item sync through a
// fake provider whose single-item read reports the item moved to another
// repository (a not_found platform.Error carrying Destination). The 404
// problem body must carry the full provider-aware destination identity as
// stable extension members so clients can retarget the reference.
func TestAPIMovedLookupProblemCarriesDestination(t *testing.T) {
	serverfake.RunParallelServerTest(t)
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
	provider := &movedLookupGitLabProvider{
		Ref: ref,
		MergeRequests: []platform.MergeRequest{{
			Repo:           ref,
			PlatformID:     7001,
			Number:         7,
			URL:            "https://gitlab.example.com/group/project/-/merge_requests/7",
			Title:          "Existing MR",
			Author:         "alice",
			State:          "open",
			HeadBranch:     "feature",
			BaseBranch:     "main",
			CreatedAt:      now,
			UpdatedAt:      now,
			LastActivityAt: now,
		}},
		Issues: []platform.Issue{{
			Repo:           ref,
			PlatformID:     8001,
			Number:         11,
			URL:            "https://gitlab.example.com/group/project/-/issues/11",
			Title:          "Existing",
			Author:         "alice",
			State:          "open",
			CreatedAt:      now,
			UpdatedAt:      now,
			LastActivityAt: now,
		}},
		lookupErr: &platform.Error{
			Code:         platform.ErrCodeNotFound,
			Provider:     platform.KindGitLab,
			PlatformHost: "gitlab.example.com",
			Destination: &platform.RepoRef{
				Platform: platform.KindGitLab,
				Host:     "gitlab.example.com",
				Owner:    "newgroup",
				Name:     "project",
				RepoPath: "newgroup/project",
			},
			Err: errors.New("group/project item is not present (moved)"),
		},
	}
	registry, err := platform.NewRegistry(provider)
	require.NoError(t, err)

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
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	t.Cleanup(func() { serverfake.GracefulShutdown(t, srv) })

	syncer.RunOnce(ctx)

	tests := []struct {
		name string
		path string
	}{
		{
			name: "issue sync",
			path: "/api/v1/host/gitlab.example.com/issues/gl/group/project/11/sync",
		},
		{
			name: "pull sync",
			path: "/api/v1/host/gitlab.example.com/pulls/gl/group/project/7/sync",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			assert := assert.New(t)

			rr := testutil.DoJSON(t, srv, http.MethodPost, tt.path, nil)
			require.Equal(http.StatusNotFound, rr.Code, rr.Body.String())

			var problem serverfake.RawProblemDetail
			require.NoError(json.NewDecoder(rr.Body).Decode(&problem))
			assert.Equal("notFound", problem.Code)
			require.NotNil(problem.Details)
			assert.Equal("gitlab", problem.Details["provider"])
			assert.Equal("gitlab.example.com", problem.Details["platformHost"])
			assert.Equal("gitlab", problem.Details["destinationProvider"])
			assert.Equal(
				"gitlab.example.com", problem.Details["destinationPlatformHost"],
			)
			assert.Equal("newgroup", problem.Details["destinationOwner"])
			assert.Equal("project", problem.Details["destinationName"])
		})
	}
}

func TestAPIGitealikeReadSyncPersistsThroughServer(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	base := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	transport := &serverfake.ApiTestGitealikeTransport{
		Repo: gitealike.RepositoryDTO{
			ID:            101,
			Owner:         gitealike.UserDTO{UserName: "forgejo"},
			Name:          "tea",
			FullName:      "forgejo/tea",
			HTMLURL:       "https://codeberg.test/forgejo/tea",
			CloneURL:      "https://codeberg.test/forgejo/tea.git",
			DefaultBranch: "main",
			Created:       base,
			Updated:       base,
		},
		Pulls: []gitealike.PullRequestDTO{{
			ID:       201,
			Index:    7,
			HTMLURL:  "https://codeberg.test/forgejo/tea/pulls/7",
			Title:    "Add tea",
			User:     gitealike.UserDTO{UserName: "alice"},
			State:    "open",
			IsLocked: true,
			Head:     gitealike.BranchDTO{Ref: "feature", SHA: "abc123"},
			Base:     gitealike.BranchDTO{Ref: "main", SHA: "def456"},
			Created:  base,
			Updated:  base.Add(time.Minute),
		}},
		PullComments: []gitealike.CommentDTO{{
			ID:      301,
			User:    gitealike.UserDTO{UserName: "reviewer"},
			Body:    "looks good",
			Created: base.Add(2 * time.Minute),
			Updated: base.Add(2 * time.Minute),
		}},
		Issues: []gitealike.IssueDTO{{
			ID:      401,
			Index:   8,
			HTMLURL: "https://codeberg.test/forgejo/tea/issues/8",
			Title:   "Missing cup",
			User:    gitealike.UserDTO{UserName: "bob"},
			State:   "open",
			Created: base,
			Updated: base.Add(time.Minute),
		}},
		IssueComments: []gitealike.CommentDTO{{
			ID:      501,
			User:    gitealike.UserDTO{UserName: "triager"},
			Body:    "confirmed",
			Created: base.Add(3 * time.Minute),
			Updated: base.Add(3 * time.Minute),
		}},
		Statuses: []gitealike.StatusDTO{{
			ID:        601,
			Context:   "build",
			State:     "success",
			TargetURL: "https://ci.test/build",
			Created:   base.Add(time.Minute),
			Updated:   base.Add(time.Minute),
		}},
	}
	provider := gitealike.NewProvider(
		platform.KindForgejo,
		"codeberg.test",
		transport,
	)
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)

	database := dbtest.Open(t)

	syncer := ghclient.NewSyncerWithRegistry(
		registry,
		database,
		nil,
		[]ghclient.RepoRef{{
			Platform:     platform.KindForgejo,
			PlatformHost: "codeberg.test",
			Owner:        "forgejo",
			Name:         "tea",
			RepoPath:     "forgejo/tea",
		}},
		time.Minute,
		nil,
		nil,
	)
	t.Cleanup(syncer.Stop)
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	t.Cleanup(func() { serverfake.GracefulShutdown(t, srv) })
	client := servertest.SetupTestClient(t, srv)

	syncer.RunOnce(ctx)
	require.NoError(syncer.SyncMR(ctx, "forgejo", "tea", 7))
	require.NoError(syncer.SyncIssue(ctx, "forgejo", "tea", 8))

	repo, err := database.GetRepoByIdentity(ctx, db.RepoIdentity{
		Platform:     "forgejo",
		PlatformHost: "codeberg.test",
		RepoPath:     "forgejo/tea",
	})
	require.NoError(err)
	require.NotNil(repo)
	mr, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repo.ID, 7)
	require.NoError(err)
	require.NotNil(mr)
	assert.True(mr.IsLocked)
	assert.Equal("success", mr.CIStatus)

	pullResp, err := client.HTTP.GetPullOnHostWithResponse(ctx, &generated.GetPullOnHostRequestOptions{PathParams: &generated.GetPullOnHostPath{PlatformHost: "codeberg.test", Provider: "forgejo", Owner: "forgejo", Name: "tea", Number: int64(7)}})
	require.NoError(err)
	require.Equal(http.StatusOK, pullResp.StatusCode)
	require.NotNil(pullResp.JSON200)
	assert.True(pullResp.JSON200.MergeRequest.IsLocked)
	assert.Equal("forgejo", pullResp.JSON200.Repo.Provider)
	assert.True(pullResp.JSON200.Repo.Capabilities.ReadMergeRequests)
	assert.True(pullResp.JSON200.Repo.Capabilities.ReadCi)
	require.NotNil(pullResp.JSON200.Events)
	require.Len(pullResp.JSON200.Events, 1)
	assert.Equal("looks good", pullResp.JSON200.Events[0].Body)

	issueResp, err := client.HTTP.GetIssueOnHostWithResponse(ctx, &generated.GetIssueOnHostRequestOptions{PathParams: &generated.GetIssueOnHostPath{PlatformHost: "codeberg.test", Provider: "forgejo", Owner: "forgejo", Name: "tea", Number: int64(8)}})
	require.NoError(err)
	require.Equal(http.StatusOK, issueResp.StatusCode)
	require.NotNil(issueResp.JSON200)
	assert.Equal("Missing cup", issueResp.JSON200.Issue.Title)
	require.NotNil(issueResp.JSON200.Events)
	require.Len(issueResp.JSON200.Events, 1)
	assert.Equal("confirmed", issueResp.JSON200.Events[0].Body)
}

func TestAPIGitealikeMutationsPersistThroughServer(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	base := time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)
	transport := &serverfake.ApiTestGitealikeTransport{
		NextCommentID:  900,
		NextIssueID:    950,
		NextIssueIndex: 81,
		Repo: gitealike.RepositoryDTO{
			ID:            101,
			Owner:         gitealike.UserDTO{UserName: "tea"},
			Name:          "kettle",
			FullName:      "tea/kettle",
			HTMLURL:       "https://gitea.test/tea/kettle",
			CloneURL:      "https://gitea.test/tea/kettle.git",
			DefaultBranch: "main",
			Created:       base,
			Updated:       base,
		},
		Pulls: []gitealike.PullRequestDTO{
			{
				ID:      201,
				Index:   7,
				HTMLURL: "https://gitea.test/tea/kettle/pulls/7",
				Title:   "Add kettle",
				User:    gitealike.UserDTO{UserName: "alice"},
				State:   "open",
				Head:    gitealike.BranchDTO{Ref: "feature", SHA: "abc123"},
				Base:    gitealike.BranchDTO{Ref: "main", SHA: "def456"},
				Created: base,
				Updated: base,
			},
			{
				ID:      202,
				Index:   9,
				HTMLURL: "https://gitea.test/tea/kettle/pulls/9",
				Title:   "Close me",
				User:    gitealike.UserDTO{UserName: "alice"},
				State:   "open",
				Head:    gitealike.BranchDTO{Ref: "close", SHA: "abc999"},
				Base:    gitealike.BranchDTO{Ref: "main", SHA: "def456"},
				Created: base,
				Updated: base,
			},
		},
		Issues: []gitealike.IssueDTO{{
			ID:      401,
			Index:   8,
			HTMLURL: "https://gitea.test/tea/kettle/issues/8",
			Title:   "Missing cup",
			User:    gitealike.UserDTO{UserName: "bob"},
			State:   "open",
			Created: base,
			Updated: base,
		}},
	}
	provider := gitealike.NewProvider(
		platform.KindGitea,
		"gitea.test",
		transport,
		gitealike.WithMutations(),
	)
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)

	database := dbtest.Open(t)

	syncer := ghclient.NewSyncerWithRegistry(
		registry,
		database,
		nil,
		[]ghclient.RepoRef{{
			Platform:     platform.KindGitea,
			PlatformHost: "gitea.test",
			Owner:        "tea",
			Name:         "kettle",
			RepoPath:     "tea/kettle",
		}},
		time.Minute,
		nil,
		nil,
	)
	t.Cleanup(syncer.Stop)
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	t.Cleanup(func() { serverfake.GracefulShutdown(t, srv) })
	client := servertest.SetupTestClient(t, srv)

	syncer.RunOnce(ctx)

	repo, err := database.GetRepoByIdentity(ctx, db.RepoIdentity{
		Platform:     "gitea",
		PlatformHost: "gitea.test",
		RepoPath:     "tea/kettle",
	})
	require.NoError(err)
	require.NotNil(repo)
	require.NoError(database.UpdateDiffSHAs(ctx, repo.ID, 7, "abc123", "def456", "merge-base"))

	editedTitle := "Edited kettle"
	editedBody := "Updated kettle body"
	editContentResp, err := client.HTTP.EditPrContentOnHostWithResponse(ctx, &generated.EditPrContentOnHostRequestOptions{PathParams: &generated.EditPrContentOnHostPath{PlatformHost: "gitea.test", Provider: "gitea", Owner: "tea", Name: "kettle", Number: int64(7)}, Body: &generated.EditPrContentOnHostBody{
		Title: &editedTitle,
		Body:  &editedBody,
	}})
	require.NoError(err)
	require.Equal(http.StatusOK, editContentResp.StatusCode)
	require.NotNil(editContentResp.JSON200)
	assert.Equal(editedTitle, editContentResp.JSON200.MergeRequest.Title)
	assert.Equal(editedBody, editContentResp.JSON200.MergeRequest.Body)
	mrSeven := serverfake.RequireMR(t, database, repo.ID, 7)
	assert.Equal(editedTitle, mrSeven.Title)
	assert.Equal(editedBody, mrSeven.Body)

	commentResp, err := client.HTTP.PostPrCommentOnHostWithResponse(ctx, &generated.PostPrCommentOnHostRequestOptions{PathParams: &generated.PostPrCommentOnHostPath{PlatformHost: "gitea.test", Provider: "gitea", Owner: "tea", Name: "kettle", Number: int64(7)}, Body: &generated.PostPrCommentOnHostBody{Body: "Looks good"}})
	require.NoError(err)
	require.Equal(http.StatusCreated, commentResp.StatusCode)
	mrEvents, err := database.ListMREvents(ctx, mrSeven.ID)
	require.NoError(err)
	require.Len(mrEvents, 1)
	require.NotNil(mrEvents[0].PlatformID)
	commentID := *mrEvents[0].PlatformID
	assert.Equal("Looks good", mrEvents[0].Body)

	editCommentResp, err := client.HTTP.EditPrCommentOnHostWithResponse(ctx, &generated.EditPrCommentOnHostRequestOptions{PathParams: &generated.EditPrCommentOnHostPath{PlatformHost: "gitea.test", Provider: "gitea", Owner: "tea", Name: "kettle", Number: int64(7), CommentID: int64(commentID)}, Body: &generated.EditPrCommentOnHostBody{Body: "Still good"}})
	require.NoError(err)
	require.Equal(http.StatusOK, editCommentResp.StatusCode)
	mrEvents, err = database.ListMREvents(ctx, mrSeven.ID)
	require.NoError(err)
	require.Len(mrEvents, 1)
	assert.Equal("Still good", mrEvents[0].Body)
	expectedHeadSHA := mrSeven.PlatformHeadSHA

	approveResp, err := client.HTTP.ApprovePullOnHostWithResponse(ctx, &generated.ApprovePullOnHostRequestOptions{PathParams: &generated.ApprovePullOnHostPath{PlatformHost: "gitea.test", Provider: "gitea", Owner: "tea", Name: "kettle", Number: int64(7)}, Body: &generated.ApprovePullOnHostBody{
		Body:            "approved",
		ExpectedHeadSha: &expectedHeadSHA,
	}})
	require.NoError(err)
	require.Equal(http.StatusOK, approveResp.StatusCode, string(approveResp.Body))
	mrEvents, err = database.ListMREvents(ctx, mrSeven.ID)
	require.NoError(err)
	require.Len(mrEvents, 2)
	var reviewEvent *db.MREvent
	for i := range mrEvents {
		if mrEvents[i].EventType == "review" {
			reviewEvent = &mrEvents[i]
			break
		}
	}
	require.NotNil(reviewEvent)
	assert.Equal("APPROVED", reviewEvent.Summary)

	mergeResp, err := client.HTTP.MergePullOnHostWithResponse(ctx, &generated.MergePullOnHostRequestOptions{PathParams: &generated.MergePullOnHostPath{PlatformHost: "gitea.test", Provider: "gitea", Owner: "tea", Name: "kettle", Number: int64(7)}, Body: &generated.MergePullOnHostBody{
		Method:          "squash",
		CommitTitle:     "Merge kettle",
		CommitMessage:   "Merge Gitea MR",
		ExpectedHeadSha: &expectedHeadSHA,
	}})
	require.NoError(err)
	require.Equal(http.StatusOK, mergeResp.StatusCode)
	mrSeven = serverfake.RequireMR(t, database, repo.ID, 7)
	assert.Equal(db.MergeRequestStateMerged, mrSeven.State)
	require.NotNil(mrSeven.MergedAt)

	stateResp, err := client.HTTP.SetPrGithubStateOnHostWithResponse(ctx, &generated.SetPrGithubStateOnHostRequestOptions{PathParams: &generated.SetPrGithubStateOnHostPath{PlatformHost: "gitea.test", Provider: "gitea", Owner: "tea", Name: "kettle", Number: int64(9)}, Body: &generated.SetPrGithubStateOnHostBody{State: "closed"}})
	require.NoError(err)
	require.Equal(http.StatusOK, stateResp.StatusCode)
	mrNine := serverfake.RequireMR(t, database, repo.ID, 9)
	assert.Equal(db.MergeRequestStateClosed, mrNine.State)
	require.NotNil(mrNine.ClosedAt)

	createIssueResp, err := client.HTTP.CreateIssueOnHostWithResponse(ctx, &generated.CreateIssueOnHostRequestOptions{PathParams: &generated.CreateIssueOnHostPath{PlatformHost: "gitea.test", Provider: "gitea", Owner: "tea", Name: "kettle"}, Body: &generated.CreateIssueOnHostBody{Title: "New issue", Body: "New issue body"}})
	require.NoError(err)
	require.Equal(http.StatusCreated, createIssueResp.StatusCode)
	createdIssue, err := database.GetIssueByRepoIDAndNumber(ctx, repo.ID, 81)
	require.NoError(err)
	require.NotNil(createdIssue)
	assert.Equal("New issue", createdIssue.Title)

	issueCommentResp, err := client.HTTP.PostIssueCommentOnHostWithResponse(ctx, &generated.PostIssueCommentOnHostRequestOptions{PathParams: &generated.PostIssueCommentOnHostPath{PlatformHost: "gitea.test", Provider: "gitea", Owner: "tea", Name: "kettle", Number: int64(8)}, Body: &generated.PostIssueCommentOnHostBody{Body: "Confirmed"}})
	require.NoError(err)
	require.Equal(http.StatusCreated, issueCommentResp.StatusCode)
	issueEight := requireIssue(t, database, repo.ID, 8)
	issueEvents, err := database.ListIssueEvents(ctx, issueEight.ID)
	require.NoError(err)
	require.Len(issueEvents, 1)
	require.NotNil(issueEvents[0].PlatformID)
	issueCommentID := *issueEvents[0].PlatformID
	assert.Equal("Confirmed", issueEvents[0].Body)

	editIssueCommentResp, err := client.HTTP.EditIssueCommentOnHostWithResponse(ctx, &generated.EditIssueCommentOnHostRequestOptions{PathParams: &generated.EditIssueCommentOnHostPath{PlatformHost: "gitea.test", Provider: "gitea", Owner: "tea", Name: "kettle", Number: int64(8), CommentID: int64(issueCommentID)}, Body: &generated.EditIssueCommentOnHostBody{Body: "Confirmed again"}})
	require.NoError(err)
	require.Equal(http.StatusOK, editIssueCommentResp.StatusCode)
	issueEvents, err = database.ListIssueEvents(ctx, issueEight.ID)
	require.NoError(err)
	require.Len(issueEvents, 1)
	assert.Equal("Confirmed again", issueEvents[0].Body)

	issueStateResp, err := client.HTTP.SetIssueGithubStateOnHostWithResponse(ctx, &generated.SetIssueGithubStateOnHostRequestOptions{PathParams: &generated.SetIssueGithubStateOnHostPath{PlatformHost: "gitea.test", Provider: "gitea", Owner: "tea", Name: "kettle", Number: int64(8)}, Body: &generated.SetIssueGithubStateOnHostBody{State: "closed"}})
	require.NoError(err)
	require.Equal(http.StatusOK, issueStateResp.StatusCode)
	issueEight = requireIssue(t, database, repo.ID, 8)
	assert.Equal("closed", issueEight.State)
	require.NotNil(issueEight.ClosedAt)

	assert.Subset(transport.MutationCalls, []string{
		"edit_pull_content:7:Edited kettle:Updated kettle body",
		"create_comment:7:Looks good",
		"edit_comment:900:Still good",
		"merge:7:squash",
		"edit_pull:9:closed",
		"create_issue:New issue",
		"create_comment:8:Confirmed",
		"edit_comment:901:Confirmed again",
		"edit_issue:8:closed",
	})
}

// setupGitealikeCloneFixture builds a local git history (base commit
// on main, head commit on feature) plus a bare clone usable as the
// sync remote, so a normal provider sync can compute the reviewed diff
// snapshot without any seeded SHAs.
func setupGitealikeCloneFixture(t *testing.T) (cloneURL, baseSHA, headSHA string) {
	t.Helper()
	require := require.New(t)
	dir := t.TempDir()
	work := filepath.Join(dir, "work")
	require.NoError(os.MkdirAll(work, 0o755))
	// gitcmd.New() strips inherited GIT_DIR/GIT_WORK_TREE: under the
	// pre-commit hook git exports them into test children, and a bare
	// procutil git here would re-init and reconfigure the HOST repo
	// instead of the temp fixture.
	run := func(args ...string) string {
		out, stderr, err := gitcmd.New().Run(t.Context(), work, nil, args...)
		require.NoError(err, "git %v: %s%s", args, out, stderr)
		return strings.TrimSpace(string(out))
	}
	run("init", "-b", "main")
	run("config", "user.email", "fixture@example.invalid")
	run("config", "user.name", "Fixture")
	require.NoError(os.WriteFile(filepath.Join(work, "a.txt"), []byte("base\n"), 0o644))
	run("add", "a.txt")
	run("commit", "-m", "base")
	baseSHA = run("rev-parse", "HEAD")
	run("checkout", "-b", "feature")
	require.NoError(os.WriteFile(filepath.Join(work, "b.txt"), []byte("head\n"), 0o644))
	run("add", "b.txt")
	run("commit", "-m", "head")
	headSHA = run("rev-parse", "HEAD")
	cloneURL = filepath.Join(dir, "origin.git")
	out, stderr, err := gitcmd.New().Run(t.Context(), dir, nil, "clone", "--bare", work, cloneURL)
	require.NoError(err, "%s%s", out, stderr)
	return cloneURL, baseSHA, headSHA
}

// A plain provider sync must produce the reviewed head snapshot on its
// own: head-binding providers gate merge/approve on DiffHeadSHA, so a
// sync path that never writes it would leave every head-bound action
// permanently rejected with 409 head_unknown.
func TestAPIGitealikeNormalSyncEnablesHeadBoundMutations(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	cloneURL, baseSHA, headSHA := setupGitealikeCloneFixture(t)

	base := time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)
	transport := &serverfake.ApiTestGitealikeTransport{
		Repo: gitealike.RepositoryDTO{
			ID:            101,
			Owner:         gitealike.UserDTO{UserName: "tea"},
			Name:          "kettle",
			FullName:      "tea/kettle",
			HTMLURL:       "https://gitea.test/tea/kettle",
			CloneURL:      cloneURL,
			DefaultBranch: "main",
			Created:       base,
			Updated:       base,
		},
		Pulls: []gitealike.PullRequestDTO{{
			ID:      201,
			Index:   7,
			HTMLURL: "https://gitea.test/tea/kettle/pulls/7",
			Title:   "Add kettle",
			User:    gitealike.UserDTO{UserName: "alice"},
			State:   "open",
			Head:    gitealike.BranchDTO{Ref: "feature", SHA: headSHA},
			Base:    gitealike.BranchDTO{Ref: "main", SHA: baseSHA},
			Created: base,
			Updated: base,
		}},
	}

	provider := gitealike.NewProvider(
		platform.KindGitea, "gitea.test", transport, gitealike.WithMutations(),
	)
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)
	database := dbtest.Open(t)
	clones := gitclone.New(t.TempDir(), nil)
	syncer := ghclient.NewSyncerWithRegistry(
		registry, database, clones,
		[]ghclient.RepoRef{{
			Platform:     platform.KindGitea,
			PlatformHost: "gitea.test",
			Owner:        "tea",
			Name:         "kettle",
			RepoPath:     "tea/kettle",
			CloneURL:     cloneURL,
		}},
		time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	t.Cleanup(func() { serverfake.GracefulShutdown(t, srv) })
	syncer.RunOnce(ctx)
	client := servertest.SetupTestClient(t, srv)

	detail, err := client.HTTP.GetPullOnHostWithResponse(ctx, &generated.GetPullOnHostRequestOptions{PathParams: &generated.GetPullOnHostPath{PlatformHost: "gitea.test", Provider: "gitea", Owner: "tea", Name: "kettle", Number: int64(7)}})
	require.NoError(err)
	require.Equal(http.StatusOK, detail.StatusCode)
	require.NotNil(detail.JSON200)
	assert.Equal(headSHA, detail.JSON200.ReviewedHeadSha,
		"a normal sync must expose the reviewed head for head-bound actions")

	mergeResp, err := client.HTTP.MergePullOnHostWithResponse(ctx, &generated.MergePullOnHostRequestOptions{PathParams: &generated.MergePullOnHostPath{PlatformHost: "gitea.test", Provider: "gitea", Owner: "tea", Name: "kettle", Number: int64(7)}, Body: &generated.MergePullOnHostBody{
		Method:          "squash",
		CommitTitle:     "t",
		CommitMessage:   "m",
		ExpectedHeadSha: &headSHA,
	}})
	require.NoError(err)
	require.Equal(http.StatusOK, mergeResp.StatusCode, string(mergeResp.Body))
	assert.Equal(headSHA, transport.LastMergeOpts.ExpectedHeadSHA,
		"the sync-derived reviewed head must reach the provider as the pin")
}

func TestAPIGiteaActionsSyncPersistsThroughServer(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	base := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	stopped := base.Add(2 * time.Minute)
	transport := &serverfake.ApiTestGitealikeTransport{
		Repo: gitealike.RepositoryDTO{
			ID:            301,
			Owner:         gitealike.UserDTO{UserName: "tea"},
			Name:          "actions",
			FullName:      "tea/actions",
			HTMLURL:       "https://gitea.test/tea/actions",
			CloneURL:      "https://gitea.test/tea/actions.git",
			DefaultBranch: "main",
			Created:       base,
			Updated:       base,
		},
		Pulls: []gitealike.PullRequestDTO{{
			ID:      302,
			Index:   5,
			HTMLURL: "https://gitea.test/tea/actions/pulls/5",
			Title:   "Wire actions",
			User:    gitealike.UserDTO{UserName: "alice"},
			State:   "open",
			Head:    gitealike.BranchDTO{Ref: "feature", SHA: "sha-actions"},
			Base:    gitealike.BranchDTO{Ref: "main", SHA: "base-sha"},
			Created: base,
			Updated: base,
		}},
		Statuses: []gitealike.StatusDTO{
			{
				ID:        401,
				Context:   "Build",
				State:     "success",
				TargetURL: "https://ci.test/build",
				Created:   base,
				Updated:   stopped,
			},
			{
				ID:        402,
				Context:   "Lint",
				State:     "pending",
				TargetURL: "https://ci.test/lint",
				Created:   base,
			},
		},
		ActionRuns: []gitealike.ActionRunDTO{
			{
				ID:         501,
				Title:      "Build",
				Status:     "completed",
				Conclusion: "success",
				CommitSHA:  "sha-actions",
				HTMLURL:    "https://ci.test/build",
				Started:    &base,
				Stopped:    &stopped,
				WorkflowID: "build.yml",
			},
			{
				ID:         502,
				Title:      "Build",
				Status:     "completed",
				Conclusion: "cancelled",
				CommitSHA:  "sha-actions",
				HTMLURL:    "https://gitea.test/tea/actions/actions/runs/502",
				Started:    &base,
				Stopped:    &stopped,
				WorkflowID: "build-action.yml",
			},
			{
				ID:         503,
				RunNumber:  1,
				Title:      "Deploy",
				Status:     "completed",
				Conclusion: "failure",
				CommitSHA:  "sha-actions",
				HTMLURL:    "https://gitea.test/tea/actions/actions/runs/503",
				Created:    base,
				Updated:    base,
				Started:    &base,
				Stopped:    &base,
				WorkflowID: "deploy.yml",
			},
			{
				ID:         504,
				RunNumber:  2,
				Title:      "Deploy",
				Status:     "queued",
				CommitSHA:  "sha-actions",
				HTMLURL:    "https://gitea.test/tea/actions/actions/runs/504",
				Created:    stopped,
				Updated:    stopped,
				WorkflowID: "deploy.yml",
			},
		},
	}
	provider := gitealike.NewProvider(
		platform.KindGitea,
		"gitea.test",
		transport,
		gitealike.WithReadActions(),
	)
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)

	database := dbtest.Open(t)

	syncer := ghclient.NewSyncerWithRegistry(
		registry,
		database,
		nil,
		[]ghclient.RepoRef{{
			Platform:     platform.KindGitea,
			PlatformHost: "gitea.test",
			Owner:        "tea",
			Name:         "actions",
			RepoPath:     "tea/actions",
		}},
		time.Minute,
		nil,
		nil,
	)
	t.Cleanup(syncer.Stop)
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	t.Cleanup(func() { serverfake.GracefulShutdown(t, srv) })
	client := servertest.SetupTestClient(t, srv)

	syncer.RunOnce(ctx)
	require.NoError(syncer.SyncMROnProvider(ctx, platform.KindGitea, "gitea.test", "tea", "actions", 5))

	repo, err := database.GetRepoByIdentity(ctx, db.RepoIdentity{
		Platform:     "gitea",
		PlatformHost: "gitea.test",
		RepoPath:     "tea/actions",
	})
	require.NoError(err)
	require.NotNil(repo)
	mr := serverfake.RequireMR(t, database, repo.ID, 5)
	require.Equal("failure", mr.CIStatus)

	pullResp, err := client.HTTP.GetPullOnHostWithResponse(ctx, &generated.GetPullOnHostRequestOptions{PathParams: &generated.GetPullOnHostPath{PlatformHost: "gitea.test", Provider: "gitea", Owner: "tea", Name: "actions", Number: int64(5)}})
	require.NoError(err)
	require.Equal(http.StatusOK, pullResp.StatusCode)
	require.NotNil(pullResp.JSON200)

	var checks []db.CICheck
	require.NoError(json.Unmarshal([]byte(pullResp.JSON200.MergeRequest.CIChecksJSON), &checks))
	require.Len(checks, 4)
	assert.Equal([]string{"Build/status/success", "Lint/status/", "Build/action/failure", "Deploy/action/"}, ciCheckSummaries(checks))
	assert.Equal("failure", pullResp.JSON200.MergeRequest.CIStatus)
}

func ciCheckSummaries(checks []db.CICheck) []string {
	out := make([]string, 0, len(checks))
	for _, check := range checks {
		out = append(out, check.Name+"/"+check.App+"/"+check.Conclusion)
	}
	return out
}

func requireIssue(t *testing.T, database *db.DB, repoID int64, number int) *db.Issue {
	t.Helper()
	require := require.New(t)
	issue, err := database.GetIssueByRepoIDAndNumber(t.Context(), repoID, number)
	require.NoError(err)
	require.NotNil(issue)
	return issue
}

func assertUnsupportedCapabilityProblem(
	t *testing.T,
	body io.Reader,
	provider, host, capability string,
) {
	t.Helper()
	require := require.New(t)
	assert := assert.New(t)

	var problem struct {
		Title   string         `json:"title"`
		Status  int            `json:"status"`
		Detail  string         `json:"detail"`
		Code    string         `json:"code"`
		Details map[string]any `json:"details"`
	}
	require.NoError(json.NewDecoder(body).Decode(&problem))
	assert.Equal(http.StatusText(http.StatusConflict), problem.Title)
	assert.Equal(http.StatusConflict, problem.Status)
	assert.Contains(problem.Detail, "Unsupported provider capability")
	assert.Equal("unsupportedCapability", problem.Code,
		"top-level RFC 9457 code must be the camelCase wire literal")
	require.NotNil(problem.Details, "details must be present on unsupportedCapability problem")
	assert.Equal(capability, problem.Details["capability"])
	assert.Equal(provider, problem.Details["provider"])
	assert.Equal(host, problem.Details["platformHost"])
}

// countingTokenFileSource wraps a managed token-file source and records how
// many times the credential is resolved. Local-read endpoints must never
// resolve it, so the count stays at zero even after the file is emptied.
type countingTokenFileSource struct {
	inner *tokenauth.ManagedSource
	calls atomic.Int64
}

func (s *countingTokenFileSource) Token(ctx context.Context) (string, error) {
	s.calls.Add(1)
	return s.inner.Token(ctx)
}

func (s *countingTokenFileSource) Invalidate(rejectedToken string) {
	s.inner.Invalidate(rejectedToken)
}

func (s *countingTokenFileSource) Descriptor() tokenauth.Descriptor {
	return s.inner.Descriptor()
}

// TestAPILocalReadEndpointsServeDuringTokenRotationE2E proves that an
// already-cloned repo keeps serving diff, files, file-preview, and commits
// after its host token file is emptied mid-rotation. Those endpoints only run
// local git reads, so they must never resolve the credential; if they did, the
// briefly empty file would surface a missing-token error and break commit and
// diff views for repos that are already on disk.
func TestAPILocalReadEndpointsServeDuringTokenRotationE2E(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	serverfake.AcquireRootWorkspaceGitSlot(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()

	dir := t.TempDir()
	database := dbtest.Open(t)

	bareDir := filepath.Join(dir, "clones")
	bare, err := gitclone.New(bareDir, nil).ClonePathForContext(
		gitclone.WithRepositoryIdentity(ctx, "repo-acme-widget"),
		"github", "github.com", "acme", "widget",
	)
	require.NoError(err)
	require.NoError(os.MkdirAll(filepath.Dir(bare), 0o755))

	work := filepath.Join(dir, "work")
	gitfixture.Run(t, dir, "init", "--bare", "--initial-branch=main", bare)
	gitfixture.Run(t, dir, "clone", bare, work)
	gitfixture.Run(t, work, "config", "user.email", "test@test.com")
	gitfixture.Run(t, work, "config", "user.name", "Test")

	require.NoError(os.WriteFile(filepath.Join(work, "base.txt"), []byte("base\n"), 0o644))
	gitfixture.Run(t, work, "add", ".")
	gitfixture.Run(t, work, "commit", "-m", "base commit")
	gitfixture.Run(t, work, "push", "origin", "main")
	mergeBase := gitfixture.SHA(t, work, "HEAD")

	gitfixture.Run(t, work, "checkout", "-b", "feature")
	require.NoError(os.WriteFile(filepath.Join(work, "feature.txt"), []byte("feature\n"), 0o644))
	gitfixture.Run(t, work, "add", ".")
	gitfixture.Run(t, work, "commit", "-m", "feature commit")
	gitfixture.Run(t, work, "push", "origin", "feature")
	headSHA := gitfixture.SHA(t, work, "HEAD")

	// A token-file source modeling the credential that was valid when the
	// clone was created. Rotation empties the file before the reads below.
	tokenPath := filepath.Join(dir, "github-token")
	require.NoError(os.WriteFile(tokenPath, []byte("ghp_local_rotation_token\n"), 0o600))
	source := &countingTokenFileSource{
		inner: tokenauth.NewManagedSource(tokenauth.Descriptor{
			Key: tokenauth.Key{Platform: string(platform.KindGitHub), Host: "github.com"},
			Candidates: []tokenauth.Candidate{
				{Kind: tokenauth.SourceKindFile, FilePath: tokenPath},
			},
		}, tokenauth.Options{}),
	}

	clones := gitclone.New(bareDir, gitclone.HostSources{"github.com": source})
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": &serverfake.MockGH{}},
		database, nil, serverfake.DefaultTestRepos, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{Clones: clones})
	t.Cleanup(func() { serverfake.GracefulShutdown(t, srv) })
	client := servertest.SetupTestClient(t, srv)

	serverfake.SeedPR(t, database, "acme", "widget", 1)
	repoID, err := database.UpsertRepo(ctx, serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	require.NoError(database.UpdateDiffSHAs(ctx, repoID, 1, headSHA, mergeBase, mergeBase))

	// Rotation in progress: the token file is briefly empty. Any attempt to
	// resolve it now fails, so the local-read endpoints below must not try.
	require.NoError(os.WriteFile(tokenPath, []byte("\n"), 0o600))

	commitsResp, err := client.HTTP.GetPullCommitsWithResponse(ctx, &generated.GetPullCommitsRequestOptions{PathParams: &generated.GetPullCommitsPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
	require.NoError(err)
	require.Equal(http.StatusOK, commitsResp.StatusCode, string(commitsResp.Body))
	require.NotNil(commitsResp.JSON200)
	require.Len(commitsResp.JSON200.Commits, 1)
	assert.Equal(headSHA, commitsResp.JSON200.Commits[0].Sha)

	diffResp, err := client.HTTP.GetPullDiffWithResponse(ctx, &generated.GetPullDiffRequestOptions{PathParams: &generated.GetPullDiffPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
	require.NoError(err)
	require.Equal(http.StatusOK, diffResp.StatusCode, string(diffResp.Body))
	require.NotNil(diffResp.JSON200)
	require.Len(diffResp.JSON200.Files, 1)

	filesResp, err := client.HTTP.GetPullFilesWithResponse(ctx, &generated.GetPullFilesRequestOptions{PathParams: &generated.GetPullFilesPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
	require.NoError(err)
	require.Equal(http.StatusOK, filesResp.StatusCode, string(filesResp.Body))
	require.NotNil(filesResp.JSON200)
	require.Len(filesResp.JSON200.Files, 1)

	previewPath := "feature.txt"
	previewResp, err := client.HTTP.GetPullFilePreviewWithResponse(ctx, &generated.GetPullFilePreviewRequestOptions{PathParams: &generated.GetPullFilePreviewPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}, Query: &generated.GetPullFilePreviewQuery{Path: &previewPath}})
	require.NoError(err)
	require.Equal(http.StatusOK, previewResp.StatusCode, string(previewResp.Body))
	require.NotNil(previewResp.JSON200)
	assert.Equal(previewPath, previewResp.JSON200.Path)
	decoded, err := base64.StdEncoding.DecodeString(previewResp.JSON200.Content)
	require.NoError(err)
	assert.Equal("feature\n", string(decoded))

	// The local-read endpoints above never resolved the rotated-out token.
	assert.Zero(source.calls.Load())

	// Sanity: resolving the emptied file really does fail, so the zero count
	// means the reads skipped the source rather than finding a usable token.
	_, err = source.Token(ctx)
	require.ErrorIs(err, tokenauth.ErrMissingToken)
}

func TestAPIHeadRepoKindClassifiesSameRepoForkAndUnknown(t *testing.T) {
	require := require.New(t)
	srv, database := servertest.SetupTestServer(t)
	repoID, err := database.UpsertRepo(
		t.Context(),
		serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "widget"),
	)
	require.NoError(err)
	now := time.Now().UTC().Truncate(time.Second)
	for index, test := range []struct {
		cloneURL string
		want     string
	}{
		{cloneURL: "https://github.com/acme/widget.git", want: "same_repo"},
		{cloneURL: "https://github.com/contributor/widget.git", want: "fork"},
		{cloneURL: "", want: "unknown"},
	} {
		number := 900 + index
		_, err := database.UpsertMergeRequest(t.Context(), &db.MergeRequest{
			RepoID: repoID, PlatformID: int64(number), Number: number,
			URL:   "https://github.com/acme/widget/pull/" + strconv.Itoa(number),
			Title: "Head repository classification", Author: "alice", State: "open",
			HeadBranch: "feature/head-repo", BaseBranch: "main",
			HeadRepoCloneURL: test.cloneURL,
			CreatedAt:        now, UpdatedAt: now, LastActivityAt: now,
		})
		require.NoError(err)
		response := testutil.DoJSON(
			t, srv, http.MethodGet,
			"/api/v1/pulls/gh/acme/widget/"+strconv.Itoa(number),
			nil,
		)
		require.Equal(http.StatusOK, response.Code, response.Body.String())
		var detail pullapi.MergeRequestDetailResponse
		require.NoError(json.Unmarshal(response.Body.Bytes(), &detail))
		assert.Equal(t, test.want, string(detail.HeadRepoKind))
	}
}
