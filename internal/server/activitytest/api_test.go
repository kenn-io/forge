package activitytest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	serverfake "go.kenn.io/forge/internal/testutil/serverfake"
	servertest "go.kenn.io/forge/internal/testutil/servertest"

	gh "github.com/google/go-github/v91/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/apiclient/generated"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/gitclone"

	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/platformdb"
	"go.kenn.io/forge/internal/server"
	"go.kenn.io/forge/internal/testutil"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/testutil/gitfixture"
	"go.kenn.io/forge/platform"

	platformgithub "go.kenn.io/forge/platform/github"
)

func TestMain(m *testing.M) {
	os.Exit(serverfake.RunMain(m))
}

func TestAPIListPullsOrdersByLastActivityDescending(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	srv, database := servertest.SetupTestServer(t)
	base := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	serverfake.SeedPR(t, database, "acme", "widget", 1,
		serverfake.WithSeedPRTimes(base, base, base.Add(time.Hour)),
	)
	serverfake.SeedPR(t, database, "acme", "widget", 2,
		serverfake.WithSeedPRTimes(base, base, base.Add(3*time.Hour)),
	)
	serverfake.SeedPR(t, database, "acme", "widget", 3,
		serverfake.WithSeedPRTimes(base, base, base.Add(2*time.Hour)),
	)
	client := servertest.SetupTestClient(t, srv)

	resp, err := client.HTTP.ListPullsWithResponse(t.Context(), &generated.ListPullsRequestOptions{})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.Len(*resp.JSON200, 3)
	assert := assert.New(t)
	assert.Equal(int64(2), (*resp.JSON200)[0].Number)
	assert.Equal(int64(3), (*resp.JSON200)[1].Number)
	assert.Equal(int64(1), (*resp.JSON200)[2].Number)
}

func TestAPISyncPRUsesProviderActivityAfterForcePush(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	forcePushAt := base.Add(3 * time.Hour)
	otherActivity := base.Add(2 * time.Hour)
	headSHA := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	str := func(v string) *string { return &v }
	mock := &serverfake.MockGH{
		GetPullRequestFn: func(_ context.Context, _, _ string, number int) (*gh.PullRequest, error) {
			id := int64(1001)
			return &gh.PullRequest{
				ID:        &id,
				Number:    &number,
				Title:     str("Force push activity"),
				State:     str("open"),
				HTMLURL:   str("https://github.com/acme/widget/pull/1"),
				User:      &gh.User{Login: str("octocat")},
				CreatedAt: &gh.Timestamp{Time: base.Add(-time.Hour)},
				UpdatedAt: &gh.Timestamp{Time: forcePushAt},
				Head:      &gh.PullRequestBranch{Ref: str("feature"), SHA: &headSHA},
				Base:      &gh.PullRequestBranch{Ref: str("main")},
			}, nil
		},
		ListIssueCommentsFn: func(context.Context, string, string, int) ([]*gh.IssueComment, error) {
			return nil, nil
		},
		ListPRTimelineEventsFn: func(context.Context, string, string, int) ([]platformgithub.PullRequestTimelineEvent, error) {
			return []platformgithub.PullRequestTimelineEvent{{
				EventType: "force_push",
				Actor:     "octocat",
				BeforeSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				AfterSHA:  headSHA,
				Ref:       "feature",
				CreatedAt: forcePushAt,
			}}, nil
		},
	}
	srv, database := servertest.SetupTestServerWithMock(t, mock)
	serverfake.SeedPR(t, database, "acme", "widget", 1,
		serverfake.WithSeedPRTitle("Force push activity"),
		serverfake.WithSeedPRTimes(base.Add(-time.Hour), base, base),
	)
	serverfake.SeedPR(t, database, "acme", "widget", 2,
		serverfake.WithSeedPRTitle("Other activity"),
		serverfake.WithSeedPRTimes(base.Add(-time.Hour), otherActivity, otherActivity),
	)
	client := servertest.SetupTestClient(t, srv)

	syncResp, err := client.HTTP.SyncPullWithResponse(ctx, &generated.SyncPullRequestOptions{PathParams: &generated.SyncPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
	require.NoError(err)
	require.Equal(http.StatusOK, syncResp.StatusCode, string(syncResp.Body))
	require.NotNil(syncResp.JSON200)
	assert.Equal(forcePushAt, syncResp.JSON200.MergeRequest.LastActivityAt.UTC())

	detailResp, err := client.HTTP.GetPullWithResponse(ctx, &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
	require.NoError(err)
	require.Equal(http.StatusOK, detailResp.StatusCode, string(detailResp.Body))
	require.NotNil(detailResp.JSON200)
	assert.Equal(forcePushAt, detailResp.JSON200.MergeRequest.LastActivityAt.UTC())

	listResp, err := client.HTTP.ListPullsWithResponse(ctx, &generated.ListPullsRequestOptions{})
	require.NoError(err)
	require.Equal(http.StatusOK, listResp.StatusCode, string(listResp.Body))
	require.NotNil(listResp.JSON200)
	require.Len(*listResp.JSON200, 2)
	assert.Equal(int64(1), (*listResp.JSON200)[0].Number)
	assert.Equal(forcePushAt, (*listResp.JSON200)[0].LastActivityAt.UTC())
	assert.Equal(int64(2), (*listResp.JSON200)[1].Number)
}

func TestAPIGetPullIncludesLifecycleTimelineEvents(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	srv, database := servertest.SetupTestServer(t)
	client := servertest.SetupTestClient(t, srv)
	ctx := t.Context()
	createdAt := time.Date(2024, 6, 1, 10, 0, 0, 0, time.UTC)
	mergedAt := createdAt.Add(2 * time.Hour)
	closedAt := createdAt.Add(3 * time.Hour)
	reopenedAt := createdAt.Add(4 * time.Hour)
	previousClosedAt := createdAt.Add(time.Hour)

	serverfake.SeedPR(t, database, "acme", "widget", 1,
		serverfake.WithSeedPRTimes(createdAt, mergedAt, mergedAt),
		serverfake.WithSeedPRLifecycle(db.MergeRequestStateMerged, &mergedAt, &mergedAt),
	)
	serverfake.SeedPR(t, database, "acme", "widget", 2,
		serverfake.WithSeedPRTimes(createdAt, closedAt, closedAt),
		serverfake.WithSeedPRLifecycle(db.MergeRequestStateClosed, nil, &closedAt),
	)
	serverfake.SeedPR(t, database, "acme", "widget", 3,
		serverfake.WithSeedPRTimes(createdAt, reopenedAt, reopenedAt),
		serverfake.WithSeedPRLifecycle(db.MergeRequestStateOpen, nil, &previousClosedAt),
	)
	duplicateMRID := serverfake.SeedPR(t, database, "acme", "widget", 4,
		serverfake.WithSeedPRTimes(createdAt, mergedAt, mergedAt),
		serverfake.WithSeedPRLifecycle(db.MergeRequestStateMerged, &mergedAt, &mergedAt),
	)
	repeatedClosedMRID := serverfake.SeedPR(t, database, "acme", "widget", 5,
		serverfake.WithSeedPRTimes(createdAt, closedAt, closedAt),
		serverfake.WithSeedPRLifecycle(db.MergeRequestStateClosed, nil, &closedAt),
	)
	repeatedReopenedMRID := serverfake.SeedPR(t, database, "acme", "widget", 6,
		serverfake.WithSeedPRTimes(createdAt, reopenedAt, reopenedAt),
		serverfake.WithSeedPRLifecycle(db.MergeRequestStateOpen, nil, &previousClosedAt),
	)
	duplicateReopenedMRID := serverfake.SeedPR(t, database, "acme", "widget", 7,
		serverfake.WithSeedPRTimes(createdAt, reopenedAt.Add(time.Hour), reopenedAt.Add(time.Hour)),
		serverfake.WithSeedPRLifecycle(db.MergeRequestStateOpen, nil, &previousClosedAt),
	)
	require.NoError(database.UpsertMREvents(ctx, []db.MREvent{{
		MergeRequestID: duplicateMRID,
		EventType:      "merged",
		Author:         "maintainer",
		Summary:        "merged by provider",
		CreatedAt:      mergedAt,
		DedupeKey:      "provider-merged",
	}, {
		MergeRequestID: repeatedClosedMRID,
		EventType:      "closed",
		Author:         "maintainer",
		Summary:        "previously closed by provider",
		CreatedAt:      previousClosedAt,
		DedupeKey:      "provider-closed",
	}, {
		MergeRequestID: repeatedReopenedMRID,
		EventType:      "reopened",
		Author:         "maintainer",
		Summary:        "previously reopened by provider",
		CreatedAt:      previousClosedAt.Add(-30 * time.Minute),
		DedupeKey:      "provider-reopened",
	}, {
		MergeRequestID: duplicateReopenedMRID,
		EventType:      "reopened",
		Author:         "maintainer",
		Summary:        "reopened by provider",
		CreatedAt:      previousClosedAt.Add(30 * time.Minute),
		DedupeKey:      "provider-current-reopened",
	}}))

	mergedResp, err := client.HTTP.GetPullWithResponse(ctx, &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
	require.NoError(err)
	require.Equal(http.StatusOK, mergedResp.StatusCode)
	require.NotNil(mergedResp.JSON200)
	require.NotNil(mergedResp.JSON200.Events)
	require.Len(mergedResp.JSON200.Events, 1)
	assert.Equal("merged", mergedResp.JSON200.Events[0].EventType)
	assert.Equal("merged this", mergedResp.JSON200.Events[0].Summary)
	assert.Empty(mergedResp.JSON200.Events[0].Author)
	assert.Equal(int64(-1), mergedResp.JSON200.Events[0].ID)
	assert.True(mergedResp.JSON200.Events[0].CreatedAt.Equal(mergedAt))

	closedResp, err := client.HTTP.GetPullWithResponse(ctx, &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(2)}})
	require.NoError(err)
	require.Equal(http.StatusOK, closedResp.StatusCode)
	require.NotNil(closedResp.JSON200)
	require.NotNil(closedResp.JSON200.Events)
	require.Len(closedResp.JSON200.Events, 1)
	assert.Equal("closed", closedResp.JSON200.Events[0].EventType)
	assert.Equal("closed this", closedResp.JSON200.Events[0].Summary)
	assert.Empty(closedResp.JSON200.Events[0].Author)
	assert.Equal(int64(-2), closedResp.JSON200.Events[0].ID)
	assert.True(closedResp.JSON200.Events[0].CreatedAt.Equal(closedAt))

	reopenedResp, err := client.HTTP.GetPullWithResponse(ctx, &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(3)}})
	require.NoError(err)
	require.Equal(http.StatusOK, reopenedResp.StatusCode)
	require.NotNil(reopenedResp.JSON200)
	require.NotNil(reopenedResp.JSON200.Events)
	require.Len(reopenedResp.JSON200.Events, 1)
	assert.Equal("reopened", reopenedResp.JSON200.Events[0].EventType)
	assert.Equal("reopened this", reopenedResp.JSON200.Events[0].Summary)
	assert.Empty(reopenedResp.JSON200.Events[0].Author)
	assert.Equal(int64(-3), reopenedResp.JSON200.Events[0].ID)
	assert.True(reopenedResp.JSON200.Events[0].CreatedAt.Equal(reopenedAt))

	duplicateResp, err := client.HTTP.GetPullWithResponse(ctx, &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(4)}})
	require.NoError(err)
	require.Equal(http.StatusOK, duplicateResp.StatusCode)
	require.NotNil(duplicateResp.JSON200)
	require.NotNil(duplicateResp.JSON200.Events)
	require.Len(duplicateResp.JSON200.Events, 1)
	assert.Equal("merged", duplicateResp.JSON200.Events[0].EventType)
	assert.Equal("merged by provider", duplicateResp.JSON200.Events[0].Summary)
	assert.NotEqual(int64(-1), duplicateResp.JSON200.Events[0].ID)

	repeatedClosedResp, err := client.HTTP.GetPullWithResponse(ctx, &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(5)}})
	require.NoError(err)
	require.Equal(http.StatusOK, repeatedClosedResp.StatusCode)
	require.NotNil(repeatedClosedResp.JSON200)
	require.NotNil(repeatedClosedResp.JSON200.Events)
	require.Len(repeatedClosedResp.JSON200.Events, 2)
	assert.Equal("closed this", repeatedClosedResp.JSON200.Events[0].Summary)
	assert.True(repeatedClosedResp.JSON200.Events[0].CreatedAt.Equal(closedAt))
	assert.Equal("previously closed by provider", repeatedClosedResp.JSON200.Events[1].Summary)
	assert.True(repeatedClosedResp.JSON200.Events[1].CreatedAt.Equal(previousClosedAt))

	repeatedReopenedResp, err := client.HTTP.GetPullWithResponse(ctx, &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(6)}})
	require.NoError(err)
	require.Equal(http.StatusOK, repeatedReopenedResp.StatusCode)
	require.NotNil(repeatedReopenedResp.JSON200)
	require.NotNil(repeatedReopenedResp.JSON200.Events)
	require.Len(repeatedReopenedResp.JSON200.Events, 2)
	assert.Equal("reopened this", repeatedReopenedResp.JSON200.Events[0].Summary)
	assert.True(repeatedReopenedResp.JSON200.Events[0].CreatedAt.Equal(reopenedAt))
	assert.Equal("previously reopened by provider", repeatedReopenedResp.JSON200.Events[1].Summary)
	assert.True(repeatedReopenedResp.JSON200.Events[1].CreatedAt.Equal(previousClosedAt.Add(-30 * time.Minute)))

	duplicateReopenedResp, err := client.HTTP.GetPullWithResponse(ctx, &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(7)}})
	require.NoError(err)
	require.Equal(http.StatusOK, duplicateReopenedResp.StatusCode)
	require.NotNil(duplicateReopenedResp.JSON200)
	require.NotNil(duplicateReopenedResp.JSON200.Events)
	require.Len(duplicateReopenedResp.JSON200.Events, 1)
	assert.Equal("reopened", duplicateReopenedResp.JSON200.Events[0].EventType)
	assert.Equal("reopened by provider", duplicateReopenedResp.JSON200.Events[0].Summary)
	assert.NotEqual(int64(-3), duplicateReopenedResp.JSON200.Events[0].ID)
}

func TestGitLabSyncCoversRepositoryItemsEventsOverviewAndCI(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)
	publishedAt := now.Add(-72 * time.Hour)

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
	mrEvent := platform.MergeRequestEvent{
		Repo:               ref,
		PlatformID:         9101,
		PlatformExternalID: "gid://gitlab/Note/9101",
		MergeRequestNumber: 7,
		EventType:          "issue_comment",
		Author:             "ada",
		Body:               "Looks good from GitLab",
		CreatedAt:          now.Add(time.Minute),
		DedupeKey:          "gitlab:note:9101",
	}
	issueEvent := platform.IssueEvent{
		Repo:               ref,
		PlatformID:         9201,
		PlatformExternalID: "gid://gitlab/Note/9201",
		IssueNumber:        11,
		EventType:          "issue_comment",
		Author:             "grace",
		Body:               "Issue comment from GitLab",
		CreatedAt:          now.Add(2 * time.Minute),
		DedupeKey:          "gitlab:issue-note:9201",
	}
	provider := &serverfake.ApiTestGitLabProvider{
		Ref: ref,
		MergeRequests: []platform.MergeRequest{{
			Repo:               ref,
			PlatformID:         7001,
			PlatformExternalID: "gid://gitlab/MergeRequest/7001",
			Number:             7,
			URL:                "https://gitlab.example.com:8443/Group/SubGroup/Project.Special/-/merge_requests/7",
			Title:              "GitLab provider MR",
			Author:             "ada",
			State:              "open",
			Body:               "MR body",
			HeadBranch:         "feature/gitlab",
			BaseBranch:         "main",
			HeadSHA:            "abc123",
			BaseSHA:            "def456",
			Additions:          12,
			Deletions:          3,
			CommentCount:       1,
			CreatedAt:          now,
			UpdatedAt:          now,
			LastActivityAt:     now,
			Labels: []platform.Label{{
				Repo:               ref,
				PlatformID:         9301,
				PlatformExternalID: "gid://gitlab/ProjectLabel/9301",
				Name:               "backend",
				Color:              "0052cc",
				Description:        "Backend work",
			}},
		}},
		MergeRequestEvents: map[int][]platform.MergeRequestEvent{
			7: {mrEvent, mrEvent},
		},
		Issues: []platform.Issue{{
			Repo:               ref,
			PlatformID:         8001,
			PlatformExternalID: "gid://gitlab/Issue/8001",
			Number:             11,
			URL:                "https://gitlab.example.com:8443/Group/SubGroup/Project.Special/-/issues/11",
			Title:              "GitLab provider issue",
			Author:             "grace",
			State:              "open",
			Body:               "Issue body",
			CommentCount:       1,
			CreatedAt:          now,
			UpdatedAt:          now,
			LastActivityAt:     now,
			Labels: []platform.Label{{
				Repo:               ref,
				PlatformID:         9302,
				PlatformExternalID: "gid://gitlab/ProjectLabel/9302",
				Name:               "bug",
				Color:              "d73a4a",
			}},
		}},
		IssueEvents: map[int][]platform.IssueEvent{
			11: {issueEvent, issueEvent},
		},
		Releases: []platform.Release{{
			Repo:               ref,
			PlatformID:         9401,
			PlatformExternalID: "gid://gitlab/Release/v1.2.0",
			TagName:            "v1.2.0",
			Name:               "Version 1.2.0",
			URL:                "https://gitlab.example.com:8443/Group/SubGroup/Project.Special/-/releases/v1.2.0",
			TargetCommitish:    "main",
			PublishedAt:        &publishedAt,
		}},
		Tags: []platform.Tag{{
			Repo: ref,
			Name: "v1.1.0",
			SHA:  "oldtagsha",
			URL:  "https://gitlab.example.com:8443/Group/SubGroup/Project.Special/-/tree/v1.1.0",
		}},
		CiChecks: map[string][]platform.CICheck{
			"abc123": {{
				Repo:       ref,
				Name:       "pipeline",
				Status:     "completed",
				Conclusion: "success",
				URL:        "https://gitlab.example.com:8443/Group/SubGroup/Project.Special/-/pipelines/123",
				App:        "gitlab-ci",
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
	syncer := ghclient.NewSyncerWithRegistry(
		registry, database, nil, []ghclient.RepoRef{repo}, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	t.Cleanup(func() { serverfake.GracefulShutdown(t, srv) })
	client := servertest.SetupTestClient(t, srv)

	syncer.RunOnce(ctx)
	require.NoError(syncer.SyncMR(ctx, ref.Owner, ref.Name, 7))
	require.NoError(syncer.SyncIssue(ctx, ref.Owner, ref.Name, 11))

	repoRow, err := database.GetRepoByIdentity(ctx, platformdb.DBRepoIdentity(ref))
	require.NoError(err)
	require.NotNil(repoRow)
	assert.Equal("gitlab", repoRow.Platform)
	assert.Equal("gitlab.example.com:8443", repoRow.PlatformHost)
	assert.Equal("Group/SubGroup/Project.Special", repoRow.RepoPath)
	require.NotNil(repoRow.LastSyncCompletedAt)

	mr, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repoRow.ID, 7)
	require.NoError(err)
	require.NotNil(mr)
	assert.Equal("gid://gitlab/MergeRequest/7001", mr.PlatformExternalID)
	assert.Equal("success", mr.CIStatus)
	assert.JSONEq(
		`[{"name":"pipeline","status":"completed","conclusion":"success","url":"https://gitlab.example.com:8443/Group/SubGroup/Project.Special/-/pipelines/123","app":"gitlab-ci"}]`,
		mr.CIChecksJSON,
	)
	require.Len(mr.Labels, 1)
	assert.Equal("backend", mr.Labels[0].Name)
	mrEvents, err := database.ListMREvents(ctx, mr.ID)
	require.NoError(err)
	require.Len(mrEvents, 1)
	assert.Equal("Looks good from GitLab", mrEvents[0].Body)

	issue, err := database.GetIssueByRepoIDAndNumber(ctx, repoRow.ID, 11)
	require.NoError(err)
	require.NotNil(issue)
	require.Len(issue.Labels, 1)
	assert.Equal("bug", issue.Labels[0].Name)
	issueEvents, err := database.ListIssueEvents(ctx, issue.ID)
	require.NoError(err)
	require.Len(issueEvents, 1)
	assert.Equal("Issue comment from GitLab", issueEvents[0].Body)

	providerName := "gitlab"
	providerHost := "gitlab.example.com:8443"
	mrNumber := int64(7)
	pullResp, err := client.HTTP.GetPullOnHostWithResponse(ctx, &generated.GetPullOnHostRequestOptions{PathParams: &generated.GetPullOnHostPath{PlatformHost: providerHost, Provider: providerName, Owner: "Group/SubGroup", Name: "Project.Special", Number: int64(mrNumber)}})
	require.NoError(err)
	require.Equal(http.StatusOK, pullResp.StatusCode)
	require.NotNil(pullResp.JSON200)
	assert.Equal("gitlab", pullResp.JSON200.Repo.Provider)
	assert.Equal("gitlab.example.com:8443", pullResp.JSON200.Repo.PlatformHost)
	assert.Equal("Group/SubGroup/Project.Special", pullResp.JSON200.Repo.RepoPath)
	assert.Equal("success", pullResp.JSON200.MergeRequest.CIStatus)
	assert.Len(pullResp.JSON200.Events, 1)

	issueNumber := int64(11)
	issueResp, err := client.HTTP.GetIssueOnHostWithResponse(ctx, &generated.GetIssueOnHostRequestOptions{PathParams: &generated.GetIssueOnHostPath{PlatformHost: providerHost, Provider: providerName, Owner: "Group/SubGroup", Name: "Project.Special", Number: int64(issueNumber)}})
	require.NoError(err)
	require.Equal(http.StatusOK, issueResp.StatusCode)
	require.NotNil(issueResp.JSON200)
	assert.Equal("gitlab", issueResp.JSON200.Repo.Provider)
	assert.Equal("gitlab.example.com:8443", issueResp.JSON200.Repo.PlatformHost)
	assert.Len(issueResp.JSON200.Events, 1)

	summaryResp, err := client.HTTP.ListRepoSummariesWithResponse(ctx)
	require.NoError(err)
	require.Equal(http.StatusOK, summaryResp.StatusCode)
	require.NotNil(summaryResp.JSON200)
	require.Len(*summaryResp.JSON200, 1)
	summary := (*summaryResp.JSON200)[0]
	assert.Equal("gitlab", summary.Repo.Provider)
	assert.Equal("Group/SubGroup/Project.Special", summary.Repo.RepoPath)
	require.NotNil(summary.LatestRelease)
	assert.Equal("v1.2.0", summary.LatestRelease.TagName)
	assert.Equal(
		"https://gitlab.example.com:8443/Group/SubGroup/Project.Special/-/releases/v1.2.0",
		summary.LatestRelease.URL,
	)
}

func TestAPIActivityReturnsUTCCreatedAt(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)

	srv, database := servertest.SetupTestServer(t)
	client := servertest.SetupTestClient(t, srv)
	prID := serverfake.SeedPR(t, database, "acme", "widget", 1)
	ctx := t.Context()
	createdAtUTC := time.Now().UTC().Add(-2 * time.Hour).Round(time.Second)
	//nolint:forbidigo // Test fixture intentionally uses a non-UTC timestamp to verify UTC normalization.
	createdAt := createdAtUTC.In(time.FixedZone("EDT", -4*60*60))

	require.NoError(database.UpsertMREvents(ctx, []db.MREvent{{
		MergeRequestID: prID,
		EventType:      "issue_comment",
		Author:         "reviewer",
		Body:           "Looks good",
		CreatedAt:      createdAt,
		DedupeKey:      "comment-utc-created-at",
	}}))

	since := createdAtUTC.Add(-time.Hour).Format(time.RFC3339)
	resp, err := client.HTTP.ListActivityWithResponse(ctx, &generated.ListActivityRequestOptions{Query: &generated.ListActivityQuery{Since: &since}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.NotNil(resp.JSON200.Items)
	require.NotEmpty(resp.JSON200.Items)

	var commentItem *generated.ActivityItemResponse
	for i := range resp.JSON200.Items {
		item := resp.JSON200.Items[i]
		if item.Author == "reviewer" && item.ActivityType == "comment" {
			commentItem = &item
			break
		}
	}
	require.NotEmpty(commentItem.ActivityType)
	serverfake.AssertRFC3339UTC(t, commentItem.CreatedAt, createdAt)
	assert.Equal("reviewer", commentItem.Author)
	assert.Equal("comment", commentItem.ActivityType)
}

func TestAPIListActivityCapsDefaultBranchCommitMetadata(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	srv, database := servertest.SetupTestServer(t)
	ctx := t.Context()
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	repoID, err := database.UpsertRepo(ctx, serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)

	committedAt := base.Add(10 * time.Minute)
	_, err = database.WriteDB().ExecContext(ctx, `
		INSERT INTO forge_branch_commits (
		    repo_id, branch_name, commit_sha, author_name, author_email,
		    authored_at, committer_name, committer_email, committed_at,
		    subject
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		repoID,
		"main",
		"abc123def456abc123def456abc123def456abcd",
		strings.Repeat("a", 300),
		strings.Repeat("e", 300),
		committedAt.Add(-time.Minute),
		strings.Repeat("c", 300),
		strings.Repeat("m", 300),
		committedAt,
		strings.Repeat("s", 700),
	)
	require.NoError(err)

	since := url.QueryEscape(base.Format(time.RFC3339))
	rr := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/activity?since="+since, nil)
	require.Equal(http.StatusOK, rr.Code)
	var body struct {
		Items []map[string]any `json:"items"`
	}
	require.NoError(json.NewDecoder(rr.Body).Decode(&body))
	require.Len(body.Items, 1)

	commit := body.Items[0]
	assert.Equal("default_branch_commit", commit["activity_type"])
	assert.Len(commit["body_preview"], 200)
	assert.Len(commit["author"], 256)
	assert.Len(commit["author_name"], 256)
	assert.Len(commit["author_email"], 256)
	assert.Len(commit["committer_name"], 256)
	assert.Len(commit["committer_email"], 256)
}

func TestAPIListActivityReflectsConfiguredDefaultBranchCommitCap(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	serverfake.AcquireRootWorkspaceGitSlot(t)
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	dir := t.TempDir()
	remote := filepath.Join(dir, "remote.git")
	work := filepath.Join(dir, "work")
	gitfixture.Run(t, dir, "init", "--bare", "--initial-branch=main", remote)
	gitfixture.Run(t, dir, "clone", remote, work)
	gitfixture.Run(t, work, "config", "user.email", "alice@example.com")
	gitfixture.Run(t, work, "config", "user.name", "Alice")

	shas := map[string]string{}
	for _, subject := range []string{"oldest", "third", "second", "newest"} {
		require.NoError(os.WriteFile(
			filepath.Join(work, subject+".txt"),
			[]byte(subject+"\n"),
			0o644,
		))
		gitfixture.Run(t, work, "add", ".")
		gitfixture.Run(t, work, "commit", "-m", subject)
		shas[subject] = gitfixture.SHA(t, work, "HEAD")
	}
	gitfixture.Run(t, work, "push", "origin", "main")

	database := dbtest.Open(t)
	clones := gitclone.New(filepath.Join(dir, "clones"), nil)
	repoRef := platform.RepoRef{
		Platform:           platform.KindGitLab,
		Host:               "gitlab.example.com",
		Owner:              "group",
		Name:               "project",
		RepoPath:           "group/project",
		PlatformExternalID: "gid://gitlab/Project/branch-activity-cap",
		CloneURL:           remote,
		DefaultBranch:      "main",
	}
	provider := &serverfake.ApiTestGitLabProvider{Ref: repoRef}
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)
	tracked := []ghclient.RepoRef{{
		Platform:           platform.KindGitLab,
		PlatformHost:       repoRef.Host,
		Owner:              repoRef.Owner,
		Name:               repoRef.Name,
		RepoPath:           repoRef.RepoPath,
		PlatformExternalID: repoRef.PlatformExternalID,
		CloneURL:           repoRef.CloneURL,
		DefaultBranch:      repoRef.DefaultBranch,
	}}
	syncer := ghclient.NewSyncerWithRegistry(
		registry, database, clones, tracked, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	cfg := &config.Config{Activity: config.Activity{
		DefaultBranchRetentionDays: 90,
		DefaultBranchMaxCommits:    2,
	}}
	syncer.SetBranchActivityLimits(
		cfg.BranchActivityRetention(),
		cfg.Activity.DefaultBranchMaxCommits,
	)
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{Clones: clones})
	t.Cleanup(func() { serverfake.GracefulShutdown(t, srv) })
	client := servertest.SetupTestClient(t, srv)

	syncer.RunOnce(ctx)

	types := []string{"default_branch_commit"}
	dbItems, err := database.ListActivity(ctx, db.ListActivityOpts{
		Limit: 10,
		Types: types,
	})
	require.NoError(err)
	require.Len(dbItems, 2)
	gotPersisted := []string{dbItems[0].CommitSHA, dbItems[1].CommitSHA}
	assert.ElementsMatch([]string{shas["newest"], shas["second"]}, gotPersisted)

	since := time.Now().UTC().Add(-24 * time.Hour).Format(time.RFC3339)
	resp, err := client.HTTP.ListActivityWithResponse(ctx, &generated.ListActivityRequestOptions{Query: &generated.ListActivityQuery{Since: &since, Types: types}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.NotNil(resp.JSON200.Items)
	require.Len(resp.JSON200.Items, 2)
	gotAPI := []string{
		*resp.JSON200.Items[0].CommitSha,
		*resp.JSON200.Items[1].CommitSha,
	}
	assert.ElementsMatch([]string{shas["newest"], shas["second"]}, gotAPI)
}

func TestAPIListActivityReturnsProviderCompareURLsForDefaultBranchForcePushes(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	srv, database := servertest.SetupTestServer(t)
	ctx := t.Context()
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	beforeSHA := "before1234567890abcdef"
	afterSHA := "after1234567890abcdef"

	tests := []struct {
		name     string
		identity db.RepoIdentity
		wantURL  string
	}{
		{
			name:     "github",
			identity: serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "github-widget"),
			wantURL:  "https://github.com/acme/github-widget/compare/" + beforeSHA + "..." + afterSHA,
		},
		{
			name: "forgejo",
			identity: db.RepoIdentity{
				Platform:       "forgejo",
				PlatformHost:   "codeberg.org",
				PlatformRepoID: "forgejo-widget",
				Owner:          "acme",
				Name:           "forgejo-widget",
			},
			wantURL: "https://codeberg.org/acme/forgejo-widget/compare/" + beforeSHA + "..." + afterSHA,
		},
		{
			name: "gitea",
			identity: db.RepoIdentity{
				Platform:       "gitea",
				PlatformHost:   "gitea.com",
				PlatformRepoID: "gitea-widget",
				Owner:          "acme",
				Name:           "gitea-widget",
			},
			wantURL: "https://gitea.com/acme/gitea-widget/compare/" + beforeSHA + "..." + afterSHA,
		},
		{
			name: "gitlab",
			identity: db.RepoIdentity{
				Platform:       "gitlab",
				PlatformHost:   "gitlab.com",
				PlatformRepoID: "gitlab-widget",
				Owner:          "acme/platform",
				Name:           "gitlab-widget",
			},
			wantURL: "https://gitlab.com/acme/platform/gitlab-widget/-/compare/" + beforeSHA + "..." + afterSHA,
		},
	}

	for i, tt := range tests {
		repoID, err := database.UpsertRepo(ctx, tt.identity)
		require.NoError(err)
		require.NoError(database.InsertBranchForcePush(ctx, db.BranchForcePush{
			RepoID:     repoID,
			BranchName: "main",
			BeforeSHA:  beforeSHA,
			AfterSHA:   afterSHA,
			DetectedAt: base.Add(time.Duration(i) * time.Minute),
		}))
	}

	since := url.QueryEscape(base.Add(-time.Minute).Format(time.RFC3339))
	rr := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/activity?since="+since, nil)
	require.Equal(http.StatusOK, rr.Code)
	var body struct {
		Items []map[string]any `json:"items"`
	}
	require.NoError(json.NewDecoder(rr.Body).Decode(&body))

	gotURLs := make(map[string]string)
	for _, item := range body.Items {
		if item["activity_type"] != "default_branch_force_push" {
			continue
		}
		repo, ok := item["repo"].(map[string]any)
		require.True(ok)
		repoPath, ok := repo["repo_path"].(string)
		require.True(ok)
		activityURL, _ := item["activity_url"].(string)
		gotURLs[repoPath] = activityURL
	}

	for _, tt := range tests {
		repo := tt.identity.RepoPath
		if repo == "" {
			repo = tt.identity.Owner + "/" + tt.identity.Name
		}
		assert.Equal(tt.wantURL, gotURLs[repo], tt.name)
	}
}

func TestAPIListActivityCanHideDefaultBranchActivity(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	srv, database := servertest.SetupTestServer(t)
	client := servertest.SetupTestClient(t, srv)
	ctx := t.Context()
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	repoID, err := database.UpsertRepo(ctx, serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	serverfake.SeedPR(t, database, "acme", "widget", 1, serverfake.WithSeedPRTimes(base, base, base))
	require.NoError(database.UpsertBranchCommits(ctx, []db.BranchCommit{{
		RepoID:         repoID,
		BranchName:     "main",
		CommitSHA:      "abc123def456abc123def456abc123def456abcd",
		AuthorName:     "Commit Author",
		AuthorEmail:    "author@example.com",
		AuthoredAt:     base.Add(9 * time.Minute),
		CommitterName:  "Committer Person",
		CommitterEmail: "committer@example.com",
		CommittedAt:    base.Add(10 * time.Minute),
		Subject:        "ship default branch work",
	}}))
	require.NoError(database.InsertBranchForcePush(ctx, db.BranchForcePush{
		RepoID:     repoID,
		BranchName: "main",
		BeforeSHA:  "before1234567890",
		AfterSHA:   "after1234567890",
		DetectedAt: base.Add(20 * time.Minute),
	}))

	since := base.Add(-time.Minute).Format(time.RFC3339)
	types := []string{"new_pr"}
	resp, err := client.HTTP.ListActivityWithResponse(ctx, &generated.ListActivityRequestOptions{Query: &generated.ListActivityQuery{Since: &since, Types: types}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.NotNil(resp.JSON200.Items)
	require.Len(resp.JSON200.Items, 1)
	assert.Equal("new_pr", resp.JSON200.Items[0].ActivityType)
	assert.Equal(int64(1), resp.JSON200.Items[0].ItemNumber)
}

func TestAPIListActivityAcceptsProviderQualifiedRepoFilter(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	srv, database := servertest.SetupTestServer(t)
	client := servertest.SetupTestClient(t, srv)
	ctx := t.Context()

	githubRepo, err := database.UpsertRepo(ctx, db.RepoIdentity{
		Platform:       "github",
		PlatformHost:   "github.com",
		PlatformRepoID: "github-widget",
		Owner:          "acme",
		Name:           "widget",
	})
	require.NoError(err)
	giteaRepo, err := database.UpsertRepo(ctx, db.RepoIdentity{
		Platform:       "gitea",
		PlatformHost:   "github.com",
		PlatformRepoID: "gitea-widget",
		Owner:          "acme",
		Name:           "widget",
	})
	require.NoError(err)
	serverfake.SeedPRForRepo(t, database, githubRepo, "github.com", "acme", "widget", 1)
	serverfake.SeedPRForRepo(t, database, giteaRepo, "github.com", "acme", "widget", 2)

	since := time.Now().UTC().AddDate(0, 0, -7).Format(time.RFC3339)
	repo := "gitea|github.com/acme/widget"
	resp, err := client.HTTP.ListActivityWithResponse(ctx, &generated.ListActivityRequestOptions{Query: &generated.ListActivityQuery{Since: &since, Repo: &repo}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.NotNil(resp.JSON200.Items)
	require.NotEmpty(resp.JSON200.Items)
	for _, item := range resp.JSON200.Items {
		assert.Equal("gitea", item.Repo.Provider)
		assert.Equal("github.com", item.PlatformHost)
		assert.Equal("acme", item.RepoOwner)
		assert.Equal("widget", item.RepoName)
	}
}

func TestAPIListActivityKeepsProviderNamedHostsProviderQualified(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	srv, database := servertest.SetupTestServer(t)
	client := servertest.SetupTestClient(t, srv)
	ctx := t.Context()

	repoID, err := database.UpsertRepo(ctx, db.RepoIdentity{
		Platform:       "github",
		PlatformHost:   "gitea",
		PlatformRepoID: "github-widget",
		Owner:          "acme/team",
		Name:           "widget",
	})
	require.NoError(err)
	serverfake.SeedPRForRepo(t, database, repoID, "gitea", "acme/team", "widget", 1)
	serverfake.SeedPROnHost(t, database, "github.com", "acme", "widget", 2)

	since := time.Now().UTC().AddDate(0, 0, -7).Format(time.RFC3339)
	repo := "github|gitea/acme/team/widget"
	resp, err := client.HTTP.ListActivityWithResponse(ctx, &generated.ListActivityRequestOptions{Query: &generated.ListActivityQuery{Since: &since, Repo: &repo}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.NotNil(resp.JSON200.Items)
	require.NotEmpty(resp.JSON200.Items)
	for _, item := range resp.JSON200.Items {
		assert.Equal("github", item.Repo.Provider)
		assert.Equal("gitea", item.PlatformHost)
		assert.Equal("acme/team", item.RepoOwner)
		assert.Equal("widget", item.RepoName)
	}
}
