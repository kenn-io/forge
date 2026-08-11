package e2etest

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/internal/apiclient"
	"go.kenn.io/forge/internal/apiclient/generated"
	"go.kenn.io/forge/internal/archive"
	"go.kenn.io/forge/internal/db"
	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/platform"
	"go.kenn.io/forge/internal/server"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/testutil/servertest"
)

type archiveMergeMetricsClock struct{ now time.Time }

func (c archiveMergeMetricsClock) Now() time.Time { return c.now }

type archiveMergeMetricsCase struct {
	name            string
	state           db.MergeRequestState
	storeMergedTime bool
}

func TestArchiveReportRepairsMergedMetricsAcrossRepositoryRenameE2E(t *testing.T) {
	tests := []archiveMergeMetricsCase{
		{name: "merged timestamp only", state: db.MergeRequestStateOpen, storeMergedTime: true},
		{name: "merged state only", state: db.MergeRequestStateMerged},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			testArchiveReportRepairsMergedMetricsAcrossRepositoryRename(t, tt)
		})
	}
}

func testArchiveReportRepairsMergedMetricsAcrossRepositoryRename(
	t *testing.T,
	tt archiveMergeMetricsCase,
) {
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	now := time.Date(2026, 8, 2, 13, 0, 0, 0, time.UTC)
	canonicalUpdatedAt := now.Add(-time.Hour)
	localUpdatedAt := canonicalUpdatedAt.Add(835 * time.Millisecond)
	mergedAt := canonicalUpdatedAt.Add(-time.Second)
	var renamed atomic.Bool

	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v3/repos/acme/widget":
			if renamed.Load() {
				_, _ = w.Write([]byte(`{"id":1,"node_id":"R_widget","name":"renamed","full_name":"acme/renamed","owner":{"login":"acme"}}`))
				return
			}
			_, _ = w.Write([]byte(`{"id":1,"node_id":"R_widget","name":"widget","full_name":"acme/widget","owner":{"login":"acme"}}`))
		case "/api/v3/repos/acme/renamed/pulls/7":
			_, _ = w.Write([]byte(`{
				"id":7,"node_id":"PR_7","number":7,
				"html_url":"https://github.com/acme/renamed/pull/7",
				"title":"canonical title","state":"closed",
				"created_at":"2026-08-02T10:00:00Z",
				"updated_at":"2026-08-02T12:00:00Z",
				"closed_at":"2026-08-02T11:59:59Z",
				"merged_at":"2026-08-02T11:59:59Z",
				"merge_commit_sha":"merge-sha","changed_files":4,
				"head":{"ref":"feature","sha":"head-sha","repo":{"id":1,"node_id":"R_widget","name":"renamed","full_name":"acme/renamed","owner":{"login":"acme"}}},
				"base":{"ref":"main","sha":"base-sha","repo":{"id":1,"node_id":"R_widget","name":"renamed","full_name":"acme/renamed","owner":{"login":"acme"}}}
			}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(providerServer.Close)

	providerClient, err := ghclient.NewClient(
		staticTokenSource("archive-token"), "github.com", nil, nil,
		ghclient.WithBaseURLForTesting(providerServer.URL),
	)
	require.NoError(err)
	registry, err := ghclient.NewProviderRegistry(map[string]ghclient.Client{
		"github.com": providerClient,
	})
	require.NoError(err)
	database := dbtest.Open(t)
	ref := platform.RepoRef{
		Platform: platform.KindGitHub, Host: "github.com",
		Owner: "acme", Name: "widget", RepoPath: "acme/widget",
	}
	syncer := ghclient.NewSyncerWithRegistry(
		registry, database, nil,
		[]ghclient.RepoRef{{
			Platform: ref.Platform, PlatformHost: ref.Host,
			Owner: ref.Owner, Name: ref.Name, RepoPath: ref.RepoPath,
		}},
		time.Hour, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	clock := archiveMergeMetricsClock{now: now}
	archiveService, err := archive.NewService(
		database, registry, nil, syncer, nil, clock,
	)
	require.NoError(err)
	requireEnsureConfigured(t, archiveService, []platform.RepoRef{ref})

	srv := servertest.New(t, database, syncer, nil, "/", nil, server.ServerOptions{
		Archive:                       archiveService,
		HostCheckAllowLoopbackAnyPort: true,
	})
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	forgeServer := httptest.NewServer(srv)
	t.Cleanup(forgeServer.Close)
	api, err := apiclient.NewWithHTTPClient(forgeServer.URL, forgeServer.Client())
	require.NoError(err)
	repositories := []generated.ArchiveRepositoryRef{{
		Provider: "github", PlatformHost: "github.com",
		Owner: ref.Owner, Name: ref.Name, RepoPath: ref.RepoPath,
	}}
	started, err := api.HTTP.StartArchivesWithResponse(
		ctx, generated.ArchiveMutationBody{Repositories: &repositories},
	)
	require.NoError(err)
	require.NotNil(started.JSON200)

	repo, err := database.GetRepoByIdentity(ctx, platform.DBRepoIdentity(ref))
	require.NoError(err)
	require.NotNil(repo)
	var storedMergedAt *time.Time
	if tt.storeMergedTime {
		storedMergedAt = &mergedAt
	}
	_, err = database.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID: repo.ID, PlatformID: 7, PlatformExternalID: "PR_7", Number: 7,
		URL: "https://github.com/acme/widget/pull/7", Title: "newer local title",
		State: tt.state, PlatformHeadSHA: "head-sha",
		CreatedAt: canonicalUpdatedAt.Add(-2 * time.Hour), UpdatedAt: localUpdatedAt,
		LastActivityAt: localUpdatedAt, MergedAt: storedMergedAt, ClosedAt: &mergedAt,
	})
	require.NoError(err)
	require.NoError(database.CommitArchiveInventoryPage(ctx, db.ArchiveInventoryCommit{
		RepoID: repo.ID, ItemType: db.ArchiveItemTypeIssue,
		ScanGeneration: 1, Exhausted: true, Coverage: db.ArchiveCoverageSupported,
		Now: now,
	}))
	require.NoError(database.CommitArchiveInventoryPage(ctx, db.ArchiveInventoryCommit{
		RepoID: repo.ID, ItemType: db.ArchiveItemTypeMergeRequest,
		Items: []db.ArchiveInventoryItem{{
			Number: 7, ProviderItemID: "PR_7",
			ProviderCreatedAt: canonicalUpdatedAt.Add(-2 * time.Hour),
			ProviderUpdatedAt: canonicalUpdatedAt,
		}},
		ScanGeneration: 1, Exhausted: true, Coverage: db.ArchiveCoverageSupported,
		Now: now,
	}))
	progress, err := database.GetDatasetProgress(
		ctx, repo.ID, db.ArchiveItemTypeMergeRequest, 7, db.ArchiveDatasetLookup,
	)
	require.NoError(err)
	require.NoError(database.CommitArchiveItemSync(ctx, db.ArchiveItemSyncCommit{
		RepoID: repo.ID, ItemType: db.ArchiveItemTypeMergeRequest, ItemNumber: 7,
		ScanGeneration: progress.ScanGeneration, Outcome: db.ArchiveLookupPresent,
		Now: now,
	}))
	_, err = database.WriteDB().ExecContext(ctx, `
		UPDATE forge_archive_dataset_progress
		SET scan_generation = ?
		WHERE repo_id = ? AND item_type = 'merge_request'
		  AND item_number = 7 AND dataset = 'lookup'`, int64(1<<32), repo.ID)
	require.NoError(err)

	requireEnsureConfigured(t, archiveService, []platform.RepoRef{ref})
	progress, err = database.GetDatasetProgress(
		ctx, repo.ID, db.ArchiveItemTypeMergeRequest, 7, db.ArchiveDatasetLookup,
	)
	require.NoError(err)
	assert.Equal(db.ArchiveDatasetProgressPending, progress.Status)

	renamed.Store(true)
	require.NoError(archiveService.RunEligible(ctx))
	progress, err = database.GetDatasetProgress(
		ctx, repo.ID, db.ArchiveItemTypeMergeRequest, 7, db.ArchiveDatasetLookup,
	)
	require.NoError(err)
	assert.Equal(db.ArchiveDatasetProgressComplete, progress.Status)

	storedRepo, err := database.GetRepoByID(ctx, repo.ID)
	require.NoError(err)
	require.NotNil(storedRepo)
	assert.Equal("renamed", storedRepo.Name)
	storedMR, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repo.ID, 7)
	require.NoError(err)
	require.NotNil(storedMR)
	assert.Equal("merge-sha", storedMR.MergeCommitSHA)
	require.NotNil(storedMR.FilesChanged)
	assert.Equal(4, *storedMR.FilesChanged)
	require.NotNil(storedMR.MergedAt)
	assert.Equal(mergedAt, *storedMR.MergedAt)
	assert.Equal("newer local title", storedMR.Title)

	verbose := true
	reportResponse, err := api.HTTP.GetArchiveReportWithResponse(ctx, &generated.GetArchiveReportParams{
		Start: mergedAt.Add(-time.Minute).Format(time.RFC3339),
		End:   mergedAt.Add(time.Minute).Format(time.RFC3339), Verbose: &verbose,
	})
	require.NoError(err)
	require.NotNil(reportResponse.JSON200)
	require.NotNil(reportResponse.JSON200.Activity)
	require.Len(*reportResponse.JSON200.Activity, 1)
	merged := (*reportResponse.JSON200.Activity)[0]
	assert.Equal(generated.ArchiveReportActivityResponseKindMergeRequestMerged, merged.Kind)
	require.NotNil(merged.MergeCommitSha)
	assert.Equal("merge-sha", *merged.MergeCommitSha)
	require.NotNil(merged.FilesChanged)
	assert.Equal(int64(4), *merged.FilesChanged)
	require.NotNil(reportResponse.JSON200.Repositories)
	require.Len(*reportResponse.JSON200.Repositories, 1)
	assert.Equal("renamed", (*reportResponse.JSON200.Repositories)[0].Repository.Name)
}
