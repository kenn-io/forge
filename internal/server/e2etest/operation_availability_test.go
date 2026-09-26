package e2etest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	gh "github.com/google/go-github/v91/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/apiclient"
	"go.kenn.io/forge/internal/apiclient/generated"
	"go.kenn.io/forge/internal/db"
	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/server"
	"go.kenn.io/forge/internal/testutil"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/testutil/reposeed"
	"go.kenn.io/forge/internal/testutil/servertest"
	"go.kenn.io/forge/platform"
)

func TestRepoRenameSyncPreservesMergeAvailabilityE2E(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	database := dbtest.Open(t)
	providerID := int64(1001)
	previousSyncStartedAt := time.Now().UTC().Add(-time.Hour)
	previousSyncCompletedAt := previousSyncStartedAt.Add(time.Minute)

	sourceID, err := reposeed.Seed(ctx, database, db.RepoIdentity{
		Platform:       "github",
		PlatformHost:   "github.com",
		PlatformRepoID: providerID,
		Owner:          "acme",
		Name:           "old-widget",
	})
	require.NoError(err)
	require.NoError(database.UpdateRepoSettings(ctx, sourceID, true, false, false, true))
	require.NoError(database.UpdateRepoSyncStarted(ctx, sourceID, previousSyncStartedAt))
	require.NoError(database.UpdateRepoSyncCompleted(ctx, sourceID, previousSyncCompletedAt, ""))
	sourceMRID, err := database.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID:             sourceID,
		PlatformID:         7001,
		PlatformExternalID: "PR_source",
		Number:             7,
		URL:                "https://github.com/acme/old-widget/pull/7",
		Title:              "source PR snapshot",
		Author:             "ada",
		State:              db.MergeRequestStateOpen,
		CreatedAt:          previousSyncStartedAt.Add(-time.Hour),
		UpdatedAt:          previousSyncStartedAt,
		LastActivityAt:     previousSyncStartedAt,
	})
	require.NoError(err)
	require.NoError(database.SetStarred(ctx, db.ItemTypePR, sourceID, 7))
	_, err = database.SetItemWorkflowState(ctx, db.SetItemWorkflowStateParams{
		RepoID:     sourceID,
		ItemType:   db.ItemTypePR,
		ItemNumber: 7,
		Status:     string(db.KanbanStatusReviewing),
		Source:     "user",
	})
	require.NoError(err)

	destinationID, err := reposeed.Seed(ctx, database, db.RepoIdentity{
		Platform:       "github",
		PlatformHost:   "github.com",
		PlatformRepoID: 1003,
		Owner:          "acme",
		Name:           "widget",
	})
	require.NoError(err)
	require.NotEqual(sourceID, destinationID)
	// Keep every destination field distinct from the authoritative source
	// snapshot so the HTTP response proves reconciliation preserved all four.
	require.NoError(database.UpdateRepoSettings(ctx, destinationID, false, true, true, false))
	destinationSnapshotAt := previousSyncCompletedAt.Add(time.Hour)
	_, err = database.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID:             destinationID,
		PlatformID:         9001,
		PlatformExternalID: "PR_destination",
		Number:             7,
		URL:                "https://github.com/acme/widget/pull/7",
		Title:              "destination PR snapshot",
		Author:             "grace",
		State:              db.MergeRequestStateOpen,
		CreatedAt:          destinationSnapshotAt.Add(-time.Hour),
		UpdatedAt:          destinationSnapshotAt,
		LastActivityAt:     destinationSnapshotAt,
	})
	require.NoError(err)
	_, err = database.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID:             destinationID,
		PlatformID:         9002,
		PlatformExternalID: "PR_destination_only",
		Number:             8,
		URL:                "https://github.com/acme/widget/pull/8",
		Title:              "destination-only PR snapshot",
		Author:             "grace",
		State:              db.MergeRequestStateOpen,
		CreatedAt:          destinationSnapshotAt.Add(-time.Hour),
		UpdatedAt:          destinationSnapshotAt,
		LastActivityAt:     destinationSnapshotAt,
	})
	require.NoError(err)
	_, err = database.SetItemWorkflowState(ctx, db.SetItemWorkflowStateParams{
		RepoID:     destinationID,
		ItemType:   db.ItemTypePR,
		ItemNumber: 7,
		Status:     string(db.KanbanStatusWaiting),
		Source:     "user",
	})
	require.NoError(err)

	mock := &mockGH{
		getRepositoryFn: func(context.Context, string, string) (*gh.Repository, error) {
			return &gh.Repository{
				ID:    &providerID,
				Owner: &gh.User{Login: new("acme")}, Name: new("widget"),
				AllowSquashMerge: new(true), AllowMergeCommit: new(false),
				AllowRebaseMerge: new(false),
				Permissions:      &gh.RepositoryPermissions{Push: new(true)},
			}, nil
		},
	}
	ref := ghclient.RepoRef{
		Platform:     platform.KindGitHub,
		PlatformHost: "github.com",
		Owner:        "acme",
		Name:         "widget",
		RepoPath:     "acme/widget",
	}
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": mock},
		database, nil, []ghclient.RepoRef{ref}, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	srv := servertest.New(t, database, syncer, nil, "/", nil, server.ServerOptions{
		HostCheckAllowLoopbackAnyPort: true,
	})
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	forge := httptest.NewServer(srv)
	t.Cleanup(forge.Close)

	status, body := postJSON(t, forge.Client(), forge.URL+"/api/v1/sync", nil)
	require.Equal(http.StatusAccepted, status, body)
	waitForRepoSynced(t, database, "acme", "widget", &previousSyncCompletedAt)

	client, err := apiclient.NewWithHTTPClient(forge.URL, forge.Client())
	require.NoError(err)
	response, err := client.HTTP.GetRepoWithResponse(ctx, &generated.GetRepoRequestOptions{PathParams: &generated.GetRepoPath{Provider: "github", Owner: "acme", Name: "widget"}})
	require.NoError(err)
	require.Equal(http.StatusOK, response.StatusCode, string(response.Body))
	require.NotNil(response.JSON200)

	repo := response.JSON200
	assert.True(repo.AllowSquashMerge)
	assert.False(repo.AllowMergeCommit)
	assert.False(repo.AllowRebaseMerge)
	assert.True(repo.ViewerCanMerge)
	assert.True(repo.Operations.MergePr.Available)

	pull, err := client.HTTP.GetPullWithResponse(ctx, &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "github", Owner: "acme", Name: "widget", Number: int64(7)}})
	require.NoError(err)
	require.Equal(http.StatusOK, pull.StatusCode, string(pull.Body))
	require.NotNil(pull.JSON200)
	assert.Equal(sourceMRID, pull.JSON200.MergeRequest.ID)
	assert.Equal(sourceID, pull.JSON200.MergeRequest.RepoID)
	assert.Equal("source PR snapshot", pull.JSON200.MergeRequest.Title)
	assert.True(pull.JSON200.MergeRequest.Starred)
	assert.Equal("reviewing", string(pull.JSON200.MergeRequest.KanbanStatus))

	discarded, err := client.HTTP.GetPullWithResponse(ctx, &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "github", Owner: "acme", Name: "widget", Number: int64(8)}})
	require.Error(err)
	require.NotNil(discarded)
	assert.Equal(http.StatusNotFound, discarded.StatusCode, string(discarded.Body))
}

func TestRepoPathReuseSyncDropsPreviousProviderSnapshotE2E(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	database := dbtest.Open(t)
	displacedProviderID := int64(1001)
	incomingProviderID := int64(1002)
	previousSyncStartedAt := time.Now().UTC().Add(-time.Hour)
	previousSyncCompletedAt := previousSyncStartedAt.Add(time.Minute)

	displacedID, err := reposeed.Seed(ctx, database, db.RepoIdentity{
		Platform:       "github",
		PlatformHost:   "github.com",
		PlatformRepoID: displacedProviderID,
		Owner:          "acme",
		Name:           "widget",
	})
	require.NoError(err)
	require.NoError(database.UpdateRepoProviderObservation(ctx, displacedID, db.RepoProviderMetadata{
		WebURL:        "https://github.com/acme/obsolete-widget",
		CloneURL:      "https://github.com/acme/obsolete-widget.git",
		DefaultBranch: "obsolete-main",
	}, nil, nil))
	require.NoError(database.UpdateRepoSettings(ctx, displacedID, false, false, false, false))
	require.NoError(database.UpdateRepoSyncStarted(ctx, displacedID, previousSyncStartedAt))
	require.NoError(database.UpdateRepoSyncCompleted(ctx, displacedID, previousSyncCompletedAt, ""))
	_, err = database.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID:             displacedID,
		PlatformID:         7001,
		PlatformExternalID: "PR_displaced",
		Number:             7,
		URL:                "https://github.com/acme/obsolete-widget/pull/7",
		Title:              "displaced PR snapshot",
		Author:             "ada",
		State:              db.MergeRequestStateOpen,
		CreatedAt:          previousSyncStartedAt.Add(-time.Hour),
		UpdatedAt:          previousSyncStartedAt,
		LastActivityAt:     previousSyncStartedAt,
	})
	require.NoError(err)
	_, err = database.UpsertIssue(ctx, &db.Issue{
		RepoID:             displacedID,
		PlatformID:         8001,
		PlatformExternalID: "I_displaced",
		Number:             8,
		URL:                "https://github.com/acme/obsolete-widget/issues/8",
		Title:              "displaced issue snapshot",
		Author:             "ada",
		State:              "open",
		CreatedAt:          previousSyncStartedAt.Add(-time.Hour),
		UpdatedAt:          previousSyncStartedAt,
		LastActivityAt:     previousSyncStartedAt,
	})
	require.NoError(err)

	mock := &mockGH{
		getRepositoryFn: func(context.Context, string, string) (*gh.Repository, error) {
			return &gh.Repository{
				ID:    &incomingProviderID,
				Owner: &gh.User{Login: new("acme")}, Name: new("widget"),
				AllowSquashMerge: new(true), AllowMergeCommit: new(true),
				AllowRebaseMerge: new(true),
				Permissions:      &gh.RepositoryPermissions{Push: new(true)},
			}, nil
		},
	}
	ref := ghclient.RepoRef{
		Platform:     platform.KindGitHub,
		PlatformHost: "github.com",
		Owner:        "acme",
		Name:         "widget",
		RepoPath:     "acme/widget",
	}
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": mock},
		database, nil, []ghclient.RepoRef{ref}, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	srv := servertest.New(t, database, syncer, nil, "/", nil, server.ServerOptions{
		HostCheckAllowLoopbackAnyPort: true,
	})
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	forge := httptest.NewServer(srv)
	t.Cleanup(forge.Close)

	status, body := postJSON(t, forge.Client(), forge.URL+"/api/v1/sync", nil)
	require.Equal(http.StatusAccepted, status, body)
	newRepo := waitForRepoSynced(t, database, "acme", "widget", &previousSyncCompletedAt)
	assert.NotEqual(displacedID, newRepo.ID)
	assert.Equal(incomingProviderID, newRepo.PlatformRepoID)
	assert.Empty(newRepo.WebURL)
	assert.Empty(newRepo.CloneURL)
	assert.Empty(newRepo.DefaultBranch)

	client, err := apiclient.NewWithHTTPClient(forge.URL, forge.Client())
	require.NoError(err)
	response, err := client.HTTP.GetRepoWithResponse(ctx, &generated.GetRepoRequestOptions{PathParams: &generated.GetRepoPath{Provider: "github", Owner: "acme", Name: "widget"}})
	require.NoError(err)
	require.Equal(http.StatusOK, response.StatusCode, string(response.Body))
	require.NotNil(response.JSON200)
	assert.True(response.JSON200.AllowSquashMerge)
	assert.True(response.JSON200.AllowMergeCommit)
	assert.True(response.JSON200.AllowRebaseMerge)
	assert.True(response.JSON200.ViewerCanMerge)
	assert.True(response.JSON200.Operations.MergePr.Available)

	pull, err := client.HTTP.GetPullWithResponse(ctx, &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "github", Owner: "acme", Name: "widget", Number: int64(7)}})
	require.Error(err)
	require.NotNil(pull)
	assert.Equal(http.StatusNotFound, pull.StatusCode, string(pull.Body))
	issue, err := client.HTTP.GetIssueWithResponse(ctx, &generated.GetIssueRequestOptions{PathParams: &generated.GetIssuePath{Provider: "github", Owner: "acme", Name: "widget", Number: int64(8)}})
	require.Error(err)
	require.NotNil(issue)
	assert.Equal(http.StatusNotFound, issue.StatusCode, string(issue.Body))
}

func TestPullDetailReportsPausedRateTrackerE2E(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	database := dbtest.Open(t)
	tracker := ghclient.NewRateTracker(database, "github.com", "host", "rest")
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": &mockGH{}},
		database,
		nil,
		[]ghclient.RepoRef{{Owner: "acme", Name: "widget", PlatformHost: "github.com"}},
		time.Minute,
		map[string]*ghclient.RateTracker{ghclient.RateBucketKey("github", "github.com", "host"): tracker},
		nil,
	)
	t.Cleanup(syncer.Stop)

	identity := db.GitHubRepoIdentity("github.com", "acme", "widget")
	identity.PlatformRepoID = testutil.FixtureRepoID("acme", "widget")
	repoID, err := reposeed.Seed(t.Context(), database, identity)
	require.NoError(err)
	// Keep merge permission available so this fixture isolates the rate-limit
	// gate instead of the fail-closed viewer permission gate.
	require.NoError(database.UpdateRepoViewerCanMerge(t.Context(), repoID, true))
	_, err = database.UpsertMergeRequest(t.Context(), &db.MergeRequest{
		RepoID:         repoID,
		PlatformID:     7001,
		Number:         7,
		URL:            "https://github.com/acme/widget/pull/7",
		Title:          "Update widget",
		Author:         "ada",
		State:          "open",
		CreatedAt:      time.Date(2026, time.July, 22, 12, 0, 0, 0, time.UTC),
		UpdatedAt:      time.Date(2026, time.July, 22, 12, 5, 0, 0, time.UTC),
		LastActivityAt: time.Date(2026, time.July, 22, 12, 5, 0, 0, time.UTC),
	})
	require.NoError(err)

	resetAt := time.Now().UTC().Truncate(time.Second).Add(30 * time.Minute)
	tracker.UpdateFromRate(ghclient.Rate{Limit: 5000, Remaining: 0, Reset: resetAt})

	srv := servertest.New(t, database, syncer, nil, "/", nil, server.ServerOptions{
		HostCheckAllowLoopbackAnyPort: true,
	})
	forge := httptest.NewServer(srv)
	t.Cleanup(forge.Close)

	client, err := apiclient.NewWithHTTPClient(forge.URL, forge.Client())
	require.NoError(err)
	response, err := client.HTTP.GetPullWithResponse(t.Context(), &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "github", Owner: "acme", Name: "widget", Number: int64(7)}})
	require.NoError(err)
	require.Equal(http.StatusOK, response.StatusCode, string(response.Body))
	require.NotNil(response.JSON200)
	require.NotNil(response.JSON200.Repo.Operations)

	merge := response.JSON200.Repo.Operations.MergePr
	assert.False(merge.Available)
	require.NotNil(merge.Code)
	assert.Equal("rate_limited", *merge.Code)
	require.NotNil(merge.UnavailableReason)
	assert.Equal("github.com rate-limited", *merge.UnavailableReason)
	require.NotNil(merge.RetryAt)
	assert.Equal(resetAt.Format(time.RFC3339), *merge.RetryAt)
}
