package apitest

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/middleman/internal/apiclient"
	"go.kenn.io/middleman/internal/apiclient/generated"
	"go.kenn.io/middleman/internal/archive"
	"go.kenn.io/middleman/internal/archive/report"
	"go.kenn.io/middleman/internal/db"
	ghclient "go.kenn.io/middleman/internal/github"
	"go.kenn.io/middleman/internal/platform"
	"go.kenn.io/middleman/internal/server"
	"go.kenn.io/middleman/internal/testutil/dbtest"
)

func TestAPIArchiveRoutesRemainRegisteredWithoutController(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/archive/status", http.NoBody)
	req.Host = "middleman.test"
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	assert.Equal(http.StatusServiceUnavailable, rr.Code)
	var problem server.ProblemError
	require.NoError(json.Unmarshal(rr.Body.Bytes(), &problem))
	assert.Equal(server.CodeServiceUnavailable, problem.Code)
}

func TestAPIArchiveStartPauseStatusAndReport(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	srv, database, provider, wakeCount, ref := setupArchiveTestServer(t, nil)
	client := setupTestClient(t, srv)
	repositories := []generated.ArchiveRepositoryRef{archiveGeneratedRef(ref)}

	started, err := client.HTTP.StartArchivesWithResponse(t.Context(), generated.ArchiveMutationBody{
		Repositories: &repositories,
	})
	require.NoError(err)
	require.NotNil(started.JSON200)
	require.Len(*started.JSON200, 1)
	assert.Equal(generated.ArchiveStatusResponseStatusRunning, (*started.JSON200)[0].Status)
	assert.Equal("github.test", (*started.JSON200)[0].Repository.PlatformHost)
	assert.Equal([]generated.ArchiveStatusResponseActivePhases{
		generated.IssueInventory, generated.MergeRequestInventory,
	}, *(*started.JSON200)[0].ActivePhases)
	assert.Equal(generated.ArchiveCoverageResponseCommentsSupported, (*started.JSON200)[0].Coverage.Comments)
	assert.NotNil((*started.JSON200)[0].InitialStartedAt)
	assert.Equal(int32(1), wakeCount.Load())
	assert.Zero(provider.calls.Load(), "start must only mutate durable state and wake the worker")

	startedAgain, err := client.HTTP.StartArchivesWithResponse(t.Context(), generated.ArchiveMutationBody{
		Repositories: &repositories,
	})
	require.NoError(err)
	require.NotNil(startedAgain.JSON200)
	assert.Equal((*started.JSON200)[0].Status, (*startedAgain.JSON200)[0].Status)

	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	repo, err := database.GetRepoByIdentity(t.Context(), platform.DBRepoIdentity(ref))
	require.NoError(err)
	require.NotNil(repo)
	_, err = database.WriteDB().ExecContext(t.Context(), `
		INSERT INTO middleman_issues (
			repo_id, platform_id, platform_external_id, number, url, title, author,
			state, body, created_at, updated_at, last_activity_at
		) VALUES (?, 7, 'issue-7', 7, 'https://github.test/owner/repo/issues/7',
			'Synthetic issue', 'alice', 'closed', 'body', ?, ?, ?)`, repo.ID, now, now, now)
	require.NoError(err)
	verbose := true
	reportResponse, err := client.HTTP.GetArchiveReportWithResponse(t.Context(), &generated.GetArchiveReportParams{
		Start: now.Add(-time.Hour).Format(time.RFC3339),
		End:   now.Add(time.Hour).Format(time.RFC3339), Verbose: &verbose,
	})
	require.NoError(err)
	require.NotNil(reportResponse.JSON200)
	assert.Equal(int64(1), reportResponse.JSON200.Totals.IssuesOpened)
	require.NotNil(reportResponse.JSON200.Activity)
	require.Len(*reportResponse.JSON200.Activity, 1)
	assert.Equal("issue-7", (*reportResponse.JSON200.Activity)[0].ProviderExternalId)
	assert.Zero(provider.calls.Load(), "reports must read SQLite only")

	filter := []string{"github|github.test/owner/repo"}
	statusResponse, err := client.HTTP.ListArchiveStatusWithResponse(
		t.Context(), &generated.ListArchiveStatusParams{Repo: &filter},
	)
	require.NoError(err)
	require.NotNil(statusResponse.JSON200)
	require.Len(*statusResponse.JSON200, 1)
	assert.Equal("github", (*statusResponse.JSON200)[0].Repository.Provider)

	resetAt := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	_, err = database.WriteDB().ExecContext(t.Context(), `
		UPDATE middleman_archive_repos
		SET last_error_code = 'budget_exhausted',
			last_error_detail = '/private/path?token=should-not-leak', next_retry_at = ?
		WHERE repo_id = ?`, resetAt, repo.ID)
	require.NoError(err)
	budgetStatus, err := client.HTTP.ListArchiveStatusWithResponse(
		t.Context(), &generated.ListArchiveStatusParams{Repo: &filter},
	)
	require.NoError(err)
	require.NotNil(budgetStatus.JSON200)
	require.Len(*budgetStatus.JSON200, 1)
	assert.Equal(generated.ArchiveStatusResponseStatusWaitingForBudget, (*budgetStatus.JSON200)[0].Status)
	require.NotNil((*budgetStatus.JSON200)[0].BudgetWaitUntil)
	assert.Equal(resetAt, *(*budgetStatus.JSON200)[0].BudgetWaitUntil)
	require.NotNil((*budgetStatus.JSON200)[0].Failure)
	assert.Equal("budget_exhausted", (*budgetStatus.JSON200)[0].Failure.Code)
	assert.NotContains(string(budgetStatus.Body), "should-not-leak")
	assert.NotContains(string(budgetStatus.Body), "/private/path")

	paused, err := client.HTTP.PauseArchivesWithResponse(t.Context(), generated.ArchiveMutationBody{
		Repositories: &repositories,
	})
	require.NoError(err)
	require.NotNil(paused.JSON200)
	assert.Equal(generated.ArchiveStatusResponseStatusPaused, (*paused.JSON200)[0].Status)

	startedAll, err := client.HTTP.StartArchivesWithResponse(
		t.Context(), generated.ArchiveMutationBody{All: true},
	)
	require.NoError(err)
	require.NotNil(startedAll.JSON200)
	require.Len(*startedAll.JSON200, 1)
	assert.Equal(generated.ArchiveStatusResponseStatusWaitingForBudget, (*startedAll.JSON200)[0].Status,
		"idempotent start preserves an active provider budget wait")
}

func TestAPIArchiveValidationAndLimitProblemDetails(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	srv, database, _, _, ref := setupArchiveTestServer(t, nil)
	client := setupTestClient(t, srv)
	repositories := []generated.ArchiveRepositoryRef{archiveGeneratedRef(ref)}

	invalidMutation, err := client.HTTP.StartArchivesWithResponse(t.Context(), generated.ArchiveMutationBody{
		All: true, Repositories: &repositories,
	})
	require.NoError(err)
	require.NotNil(invalidMutation.ApplicationproblemJSONDefault)
	assert.Equal(generated.ValidationError, invalidMutation.ApplicationproblemJSONDefault.Code)

	emptyMutation, err := client.HTTP.StartArchivesWithResponse(
		t.Context(), generated.ArchiveMutationBody{},
	)
	require.NoError(err)
	require.NotNil(emptyMutation.ApplicationproblemJSONDefault)
	assert.Equal(generated.ValidationError, emptyMutation.ApplicationproblemJSONDefault.Code)

	missingRepository := archiveGeneratedRef(ref)
	missingRepository.Name = "missing"
	missingRepository.RepoPath = "owner/missing"
	mixedRepositories := []generated.ArchiveRepositoryRef{
		archiveGeneratedRef(ref), missingRepository,
	}
	mixedMutation, err := client.HTTP.StartArchivesWithResponse(t.Context(), generated.ArchiveMutationBody{
		Repositories: &mixedRepositories,
	})
	require.NoError(err)
	require.NotNil(mixedMutation.ApplicationproblemJSONDefault)
	assert.Equal(generated.BadRequest, mixedMutation.ApplicationproblemJSONDefault.Code)
	repo, err := database.GetRepoByIdentity(t.Context(), platform.DBRepoIdentity(ref))
	require.NoError(err)
	require.NotNil(repo)
	states, err := database.ListArchiveRepoStates(t.Context(), []int64{repo.ID})
	require.NoError(err)
	require.Len(states, 1)
	assert.Equal(db.ArchiveCollectionModeDiscovery, states[0].CollectionMode,
		"membership validation must finish before any repository is promoted")

	offsetReport, err := client.HTTP.GetArchiveReportWithResponse(t.Context(), &generated.GetArchiveReportParams{
		Start: "2026-07-01T00:00:00+01:00", End: "2026-07-02T00:00:00Z",
	})
	require.NoError(err)
	require.NotNil(offsetReport.ApplicationproblemJSONDefault)
	assert.Equal(generated.ValidationError, offsetReport.ApplicationproblemJSONDefault.Code)

	missing := []string{"github|github.test/owner/missing"}
	missingReport, err := client.HTTP.GetArchiveReportWithResponse(t.Context(), &generated.GetArchiveReportParams{
		Start: "2026-07-01T00:00:00Z", End: "2026-07-02T00:00:00Z", Repo: &missing,
	})
	require.NoError(err)
	require.NotNil(missingReport.ApplicationproblemJSONDefault)
	assert.Equal(generated.BadRequest, missingReport.ApplicationproblemJSONDefault.Code)

	limitSrv, _, _, _, _ := setupArchiveTestServer(t, archiveLimitController{})
	limitClient := setupTestClient(t, limitSrv)
	tooLarge, err := limitClient.HTTP.GetArchiveReportWithResponse(t.Context(), &generated.GetArchiveReportParams{
		Start: "2026-07-01T00:00:00Z", End: "2026-07-02T00:00:00Z",
	})
	require.NoError(err)
	assert.Equal(http.StatusRequestEntityTooLarge, tooLarge.StatusCode())
	require.NotNil(tooLarge.ApplicationproblemJSONDefault)
	assert.Equal(generated.PayloadTooLarge, tooLarge.ApplicationproblemJSONDefault.Code)
	require.NotNil(tooLarge.ApplicationproblemJSONDefault.Details)
	details := *tooLarge.ApplicationproblemJSONDefault.Details
	encodedDetails, err := json.Marshal(details)
	require.NoError(err)
	assert.JSONEq(`{
		"reason": "reportTooLarge",
		"observedRecords": 10001,
		"maxRecords": 10000,
		"observedTextBytes": 33554433,
		"maxTextBytes": 33554432
	}`, string(encodedDetails))
}

func TestAPIArchiveRoutesObeyHostAuthAndCSRFGuards(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	srv, _, _, _, _ := setupArchiveTestServer(t, nil)

	crossSite := httptest.NewRequest(
		http.MethodPost, "/api/v1/archive/start", strings.NewReader(`{"all":true}`),
	)
	crossSite.Host = "middleman.test"
	crossSite.Header.Set("Content-Type", "application/json")
	crossSite.Header.Set("Sec-Fetch-Site", "cross-site")
	crossSiteRecorder := httptest.NewRecorder()
	srv.ServeHTTP(crossSiteRecorder, crossSite)
	assert.Equal(http.StatusForbidden, crossSiteRecorder.Code)

	badHost := httptest.NewRequest(http.MethodGet, "/api/v1/archive/status", http.NoBody)
	badHost.Host = "attacker.example"
	badHostRecorder := httptest.NewRecorder()
	srv.ServeHTTP(badHostRecorder, badHost)
	assert.Equal(http.StatusForbidden, badHostRecorder.Code)

	database := dbtest.Open(t)
	syncer := ghclient.NewSyncer(nil, database, nil, nil, time.Minute, nil, nil)
	t.Cleanup(syncer.Stop)
	authServer := server.New(database, syncer, nil, "/", nil, server.ServerOptions{
		APIAuthToken: "archive-test-token", Archive: archiveStatusController{},
	})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(authServer.Shutdown(ctx))
	})
	authClient := setupTestClient(t, authServer)
	unauthorized, err := authClient.HTTP.ListArchiveStatusWithResponse(
		t.Context(), &generated.ListArchiveStatusParams{},
	)
	require.NoError(err)
	assert.Equal(http.StatusUnauthorized, unauthorized.StatusCode())
	require.NotNil(unauthorized.ApplicationproblemJSONDefault)
	assert.Equal(generated.Unauthorized, unauthorized.ApplicationproblemJSONDefault.Code)

	authorized, err := authClient.HTTP.ListArchiveStatusWithResponse(
		t.Context(), &generated.ListArchiveStatusParams{},
		func(_ context.Context, req *http.Request) error {
			req.Header.Set("Authorization", "Bearer archive-test-token")
			return nil
		},
	)
	require.NoError(err)
	require.NotNil(authorized.JSON200)
	assert.Empty(*authorized.JSON200)
}

func TestAPIArchiveStartDrivesMovedItemDestinationScheduling(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := dbtest.Open(t)
	source := platform.RepoRef{
		Platform: platform.KindGitHub, Host: "github.test", Owner: "owner",
		Name: "source", RepoPath: "owner/source",
	}
	destination := platform.RepoRef{
		Platform: platform.KindGitHub, Host: "github.test", Owner: "owner",
		Name: "destination", RepoPath: "owner/destination",
	}
	for _, ref := range []platform.RepoRef{source, destination} {
		_, err := database.UpsertRepo(t.Context(), platform.DBRepoIdentity(ref))
		require.NoError(err)
	}
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	provider := &archiveAPITestProvider{
		issues: map[string]platform.Issue{
			source.RepoPath: {
				Repo: source, PlatformID: 7, PlatformExternalID: "issue-7", Number: 7,
				State: "closed", CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
			},
		},
		movedDestinations: map[string]platform.RepoRef{source.RepoPath: destination},
	}
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)
	service, err := archive.NewService(
		database, registry, nil,
		archiveAPITestSource{refs: []platform.RepoRef{source, destination}}, nil, nil,
	)
	require.NoError(err)
	require.NoError(service.EnsureConfigured(t.Context(), []platform.RepoRef{source, destination}))
	syncer := ghclient.NewSyncer(nil, database, nil, nil, time.Minute, nil, nil)
	t.Cleanup(syncer.Stop)
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{Archive: service})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(srv.Shutdown(ctx))
	})
	client := setupTestClient(t, srv)
	repositories := []generated.ArchiveRepositoryRef{
		archiveGeneratedRef(source), archiveGeneratedRef(destination),
	}
	started, err := client.HTTP.StartArchivesWithResponse(t.Context(), generated.ArchiveMutationBody{
		Repositories: &repositories,
	})
	require.NoError(err)
	require.NotNil(started.JSON200)
	assert.Zero(provider.calls.Load(), "the HTTP mutation must not perform provider work inline")

	for range 5 {
		require.NoError(service.RunEligible(t.Context()))
	}
	destinationRepo, err := database.GetRepoByIdentity(
		t.Context(), platform.DBRepoIdentity(destination),
	)
	require.NoError(err)
	require.NotNil(destinationRepo)
	states, err := database.ListArchiveRepoStates(t.Context(), []int64{destinationRepo.ID})
	require.NoError(err)
	require.Len(states, 1)
	assert.NotNil(states[0].MaintenanceWatermark)
	assert.Nil(states[0].MaintenanceSucceededAt,
		"a move must wake destination enumeration without assuming item-number continuity")
}

func TestAPIArchiveStartDrivesMovedMergeRequestDestinationScheduling(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := dbtest.Open(t)
	source := platform.RepoRef{
		Platform: platform.KindGitHub, Host: "github.test", Owner: "owner",
		Name: "source-mr", RepoPath: "owner/source-mr",
	}
	destination := platform.RepoRef{
		Platform: platform.KindGitHub, Host: "github.test", Owner: "owner",
		Name: "destination-mr", RepoPath: "owner/destination-mr",
	}
	for _, ref := range []platform.RepoRef{source, destination} {
		_, err := database.UpsertRepo(t.Context(), platform.DBRepoIdentity(ref))
		require.NoError(err)
	}
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	provider := &archiveAPITestProvider{
		mergeRequests: map[string]platform.MergeRequest{source.RepoPath: {
			Repo: source, PlatformID: 8, PlatformExternalID: "mr-8", Number: 8,
			State: "closed", CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
		}},
		movedMRDestinations: map[string]platform.RepoRef{source.RepoPath: destination},
	}
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)
	service, err := archive.NewService(
		database, registry, nil,
		archiveAPITestSource{refs: []platform.RepoRef{source, destination}}, nil, nil,
	)
	require.NoError(err)
	require.NoError(service.EnsureConfigured(t.Context(), []platform.RepoRef{source, destination}))
	syncer := ghclient.NewSyncer(nil, database, nil, nil, time.Minute, nil, nil)
	t.Cleanup(syncer.Stop)
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{Archive: service})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(srv.Shutdown(ctx))
	})
	client := setupTestClient(t, srv)
	repositories := []generated.ArchiveRepositoryRef{
		archiveGeneratedRef(source), archiveGeneratedRef(destination),
	}
	started, err := client.HTTP.StartArchivesWithResponse(t.Context(), generated.ArchiveMutationBody{
		Repositories: &repositories,
	})
	require.NoError(err)
	require.NotNil(started.JSON200)

	for range 8 {
		require.NoError(service.RunEligible(t.Context()))
	}
	destinationRepo, err := database.GetRepoByIdentity(
		t.Context(), platform.DBRepoIdentity(destination),
	)
	require.NoError(err)
	require.NotNil(destinationRepo)
	states, err := database.ListArchiveRepoStates(t.Context(), []int64{destinationRepo.ID})
	require.NoError(err)
	require.Len(states, 1)
	assert.NotNil(states[0].MaintenanceWatermark)
	assert.NotNil(states[0].MaintenanceSucceededAt)
	sourceRepo, err := database.GetRepoByIdentity(t.Context(), platform.DBRepoIdentity(source))
	require.NoError(err)
	require.NotNil(sourceRepo)
	var lifecycle string
	require.NoError(database.ReadDB().QueryRowContext(t.Context(), `
		SELECT lifecycle_state FROM middleman_archive_items
		WHERE repo_id = ? AND item_type = 'merge_request' AND item_number = 8`,
		sourceRepo.ID,
	).Scan(&lifecycle))
	assert.Equal("removed_upstream", lifecycle)
}

func TestAPIArchiveHydrationResumesDatasetCursorAfterServiceRestart(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := dbtest.Open(t)
	ref := platform.RepoRef{Platform: platform.KindGitHub, Host: "github.test", Owner: "owner", Name: "resume", RepoPath: "owner/resume"}
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	provider := &archiveAPITestProvider{
		issues: map[string]platform.Issue{ref.RepoPath: {
			Repo: ref, PlatformID: 7, PlatformExternalID: "issue-7", Number: 7,
			Title: "Resumable issue", State: "closed", CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
		}},
		failSecondCommentPage: true,
	}
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)
	service, err := archive.NewService(database, registry, nil, archiveAPITestSource{refs: []platform.RepoRef{ref}}, nil, nil)
	require.NoError(err)
	require.NoError(service.EnsureConfigured(t.Context(), []platform.RepoRef{ref}))
	syncer := ghclient.NewSyncer(nil, database, nil, nil, time.Minute, nil, nil)
	t.Cleanup(syncer.Stop)
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{Archive: service})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(srv.Shutdown(ctx))
	})
	client := setupTestClient(t, srv)
	repositories := []generated.ArchiveRepositoryRef{archiveGeneratedRef(ref)}
	started, err := client.HTTP.StartArchivesWithResponse(t.Context(), generated.ArchiveMutationBody{Repositories: &repositories})
	require.NoError(err)
	require.NotNil(started.JSON200)

	require.NoError(service.RunEligible(t.Context()))
	require.NoError(service.RunEligible(t.Context()))
	require.Error(service.RunEligible(t.Context()))
	repo, err := database.GetRepoByIdentity(t.Context(), platform.DBRepoIdentity(ref))
	require.NoError(err)
	require.NotNil(repo)
	var cursor string
	require.NoError(database.ReadDB().QueryRowContext(t.Context(), `
		SELECT next_cursor FROM middleman_archive_dataset_progress
		WHERE repo_id = ? AND item_type = 'issue' AND item_number = 7
			AND dataset = 'comments'`, repo.ID).Scan(&cursor))
	assert.Equal("c2", cursor)

	provider.mu.Lock()
	provider.failSecondCommentPage = false
	provider.mu.Unlock()
	_, err = database.WriteDB().ExecContext(t.Context(), `
		UPDATE middleman_archive_dataset_progress SET next_retry_at = NULL WHERE repo_id = ?`, repo.ID)
	require.NoError(err)
	restarted, err := archive.NewService(database, registry, nil, archiveAPITestSource{refs: []platform.RepoRef{ref}}, nil, nil)
	require.NoError(err)
	require.NoError(restarted.EnsureConfigured(t.Context(), []platform.RepoRef{ref}))
	require.NoError(restarted.RunEligible(t.Context()))

	verbose := true
	reportResponse, err := client.HTTP.GetArchiveReportWithResponse(t.Context(), &generated.GetArchiveReportParams{
		Start: now.Add(-time.Hour).Format(time.RFC3339), End: now.Add(time.Hour).Format(time.RFC3339), Verbose: &verbose,
	})
	require.NoError(err)
	require.NotNil(reportResponse.JSON200)
	assert.Equal(int64(2), reportResponse.JSON200.Totals.OrdinaryComments)
	var eventCount int
	require.NoError(database.ReadDB().QueryRowContext(t.Context(), `
		SELECT COUNT(*) FROM middleman_issue_events WHERE issue_id IN (
			SELECT id FROM middleman_issues WHERE repo_id = ? AND number = 7
		) AND event_type = 'issue_comment'`, repo.ID).Scan(&eventCount))
	assert.Equal(2, eventCount)
	provider.mu.Lock()
	cursors := append([]string(nil), provider.issueCommentCursors...)
	provider.mu.Unlock()
	assert.Equal([]string{"", "c2", "c2"}, cursors)
}

func TestAPIArchiveRejectedHydrationKeepsNewerParentAndChildren(t *testing.T) {
	for _, itemType := range []db.ArchiveItemType{db.ArchiveItemTypeIssue, db.ArchiveItemTypeMergeRequest} {
		t.Run(string(itemType), func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			database := dbtest.Open(t)
			ref := platform.RepoRef{Platform: platform.KindGitHub, Host: "github.test", Owner: "owner", Name: string(itemType), RepoPath: "owner/" + string(itemType)}
			stale := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
			current := stale.Add(time.Hour)
			provider := &archiveAPITestProvider{}
			if itemType == db.ArchiveItemTypeIssue {
				provider.issues = map[string]platform.Issue{ref.RepoPath: {
					Repo: ref, PlatformID: 7, PlatformExternalID: "issue-7", Number: 7,
					Title: "stale issue", State: "closed", CreatedAt: stale.Add(-time.Hour),
					UpdatedAt: stale, LastActivityAt: stale,
				}}
			} else {
				provider.mergeRequests = map[string]platform.MergeRequest{ref.RepoPath: {
					Repo: ref, PlatformID: 8, PlatformExternalID: "mr-7", Number: 7,
					Title: "stale mr", State: "closed", CreatedAt: stale.Add(-time.Hour),
					UpdatedAt: stale, LastActivityAt: stale,
				}}
			}
			service, client := setupArchiveWorkflow(t, database, provider, ref)
			repositories := []generated.ArchiveRepositoryRef{archiveGeneratedRef(ref)}
			started, err := client.HTTP.StartArchivesWithResponse(t.Context(), generated.ArchiveMutationBody{Repositories: &repositories})
			require.NoError(err)
			require.NotNil(started.JSON200)
			require.NoError(service.RunEligible(t.Context()))
			require.NoError(service.RunEligible(t.Context()))

			repo, err := database.GetRepoByIdentity(t.Context(), platform.DBRepoIdentity(ref))
			require.NoError(err)
			require.NotNil(repo)
			var parentID int64
			if itemType == db.ArchiveItemTypeIssue {
				parentID, _, _, err = database.UpsertIssueSnapshotWithLabels(t.Context(), &db.Issue{
					RepoID: repo.ID, PlatformID: 7, PlatformExternalID: "issue-7", Number: 7,
					Title: "current issue", State: "closed", CreatedAt: stale.Add(-time.Hour),
					UpdatedAt: current, LastActivityAt: current,
				})
				require.NoError(err)
				parent, parentErr := database.GetIssueByRepoIDAndNumber(t.Context(), repo.ID, 7)
				require.NoError(parentErr)
				require.NotNil(parent)
				applied, commitErr := database.CommitIssueChildSnapshot(t.Context(), db.IssueChildSnapshot{
					IssueID: parentID, ExpectedRevision: parent.SnapshotRevision,
					Comments: []db.IssueEvent{{
						IssueID: parentID, PlatformExternalID: "current-comment", EventType: "issue_comment",
						Body: "keep current issue comment", CreatedAt: current, DedupeKey: "current-comment",
					}},
				})
				require.NoError(commitErr)
				require.True(applied)
			} else {
				parentID, err = database.UpsertMergeRequest(t.Context(), &db.MergeRequest{
					RepoID: repo.ID, PlatformID: 8, PlatformExternalID: "mr-7", Number: 7,
					Title: "current mr", State: db.MergeRequestStateClosed, CreatedAt: stale.Add(-time.Hour),
					UpdatedAt: current, LastActivityAt: current,
				})
				require.NoError(err)
				parent, parentErr := database.GetMergeRequestByRepoIDAndNumber(t.Context(), repo.ID, 7)
				require.NoError(parentErr)
				require.NotNil(parent)
				applied, commitErr := database.CommitMergeRequestChildSnapshot(t.Context(), db.MergeRequestChildSnapshot{
					MergeRequestID: parentID, ExpectedRevision: parent.SnapshotRevision,
					Comments: []db.MREvent{{
						MergeRequestID: parentID, PlatformExternalID: "current-comment", EventType: "issue_comment",
						Body: "keep current mr comment", CreatedAt: current, DedupeKey: "current-comment",
					}},
					CommentsComplete: true, ReviewsComplete: true, InlineComplete: true,
				})
				require.NoError(commitErr)
				require.True(applied)
			}

			require.NoError(service.RunEligible(t.Context()))
			var title, body string
			var providerUpdatedAt time.Time
			parentTable, eventTable, foreignKey := "middleman_issues", "middleman_issue_events", "issue_id"
			if itemType == db.ArchiveItemTypeMergeRequest {
				parentTable, eventTable, foreignKey = "middleman_merge_requests", "middleman_mr_events", "merge_request_id"
			}
			require.NoError(database.ReadDB().QueryRowContext(t.Context(),
				"SELECT title FROM "+parentTable+" WHERE id = ?", parentID).Scan(&title))
			require.NoError(database.ReadDB().QueryRowContext(t.Context(),
				"SELECT body FROM "+eventTable+" WHERE "+foreignKey+" = ? AND dedupe_key = 'current-comment'", parentID).Scan(&body))
			require.NoError(database.ReadDB().QueryRowContext(t.Context(), `
				SELECT provider_updated_at FROM middleman_archive_items
				WHERE repo_id = ? AND item_type = ? AND item_number = 7`, repo.ID, itemType,
			).Scan(&providerUpdatedAt))
			assert.Contains(title, "current")
			assert.Contains(body, "keep current")
			assert.Equal(current, providerUpdatedAt)
		})
	}
}

func TestAPIArchivePublicationRejectsParentAdvancedAfterAcceptance(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := dbtest.Open(t)
	ref := platform.RepoRef{Platform: platform.KindGitHub, Host: "github.test", Owner: "owner", Name: "publish-race", RepoPath: "owner/publish-race"}
	stagedAt := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	current := stagedAt.Add(time.Hour)
	provider := &archiveAPITestProvider{
		issues: map[string]platform.Issue{ref.RepoPath: {
			Repo: ref, PlatformID: 7, PlatformExternalID: "issue-7", Number: 7,
			Title: "accepted archive parent", State: "closed", CreatedAt: stagedAt.Add(-time.Hour),
			UpdatedAt: stagedAt, LastActivityAt: stagedAt,
		}},
		issueCommentStarted: make(chan struct{}),
		issueCommentRelease: make(chan struct{}),
	}
	service, client := setupArchiveWorkflow(t, database, provider, ref)
	repositories := []generated.ArchiveRepositoryRef{archiveGeneratedRef(ref)}
	started, err := client.HTTP.StartArchivesWithResponse(t.Context(), generated.ArchiveMutationBody{Repositories: &repositories})
	require.NoError(err)
	require.NotNil(started.JSON200)
	require.NoError(service.RunEligible(t.Context()))
	require.NoError(service.RunEligible(t.Context()))

	runDone := make(chan error, 1)
	go func() { runDone <- service.RunEligible(t.Context()) }()
	select {
	case <-provider.issueCommentStarted:
	case <-time.After(5 * time.Second):
		require.Fail("archive comment fetch did not start")
	}
	repo, err := database.GetRepoByIdentity(t.Context(), platform.DBRepoIdentity(ref))
	require.NoError(err)
	require.NotNil(repo)
	issueID, _, _, err := database.UpsertIssueSnapshotWithLabels(t.Context(), &db.Issue{
		RepoID: repo.ID, PlatformID: 7, PlatformExternalID: "issue-7", Number: 7,
		Title: "newer normal-sync parent", State: "closed", CreatedAt: stagedAt.Add(-time.Hour),
		UpdatedAt: current, LastActivityAt: current,
	})
	require.NoError(err)
	require.NoError(database.UpsertIssueEvents(t.Context(), []db.IssueEvent{{
		IssueID: issueID, PlatformExternalID: "current-comment", EventType: "issue_comment",
		Body: "keep post-acceptance comment", CreatedAt: current, DedupeKey: "current-comment",
	}}))
	close(provider.issueCommentRelease)
	// The stale page commit loses the revision compare-and-swap and is
	// discarded without an error; the dataset reopens for the new revision.
	require.NoError(<-runDone)

	verbose := true
	reportResponse, err := client.HTTP.GetArchiveReportWithResponse(t.Context(), &generated.GetArchiveReportParams{
		Start: stagedAt.Add(-2 * time.Hour).Format(time.RFC3339),
		End:   current.Add(time.Hour).Format(time.RFC3339), Verbose: &verbose,
	})
	require.NoError(err)
	require.NotNil(reportResponse.JSON200)
	assert.Equal(int64(1), reportResponse.JSON200.Totals.OrdinaryComments)
	require.NotNil(reportResponse.JSON200.Activity)
	var found bool
	for _, activity := range *reportResponse.JSON200.Activity {
		if activity.Body == "keep post-acceptance comment" {
			found = true
		}
	}
	assert.True(found)
}

func TestAPIArchiveAuthenticationFailureDefersAllProviderWork(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := dbtest.Open(t)
	ref := platform.RepoRef{Platform: platform.KindGitHub, Host: "github.test", Owner: "owner", Name: "auth", RepoPath: "owner/auth"}
	provider := &archiveAPITestProvider{issueInventoryErr: platform.PermissionDenied(
		ref.Platform, ref.Host, errors.New("expired synthetic credential"),
	)}
	service, client := setupArchiveWorkflow(t, database, provider, ref)
	repositories := []generated.ArchiveRepositoryRef{archiveGeneratedRef(ref)}
	started, err := client.HTTP.StartArchivesWithResponse(t.Context(), generated.ArchiveMutationBody{Repositories: &repositories})
	require.NoError(err)
	require.NotNil(started.JSON200)

	require.Error(service.RunEligible(t.Context()))
	assert.Equal(int32(1), provider.calls.Load())
	status, err := client.HTTP.ListArchiveStatusWithResponse(t.Context(), &generated.ListArchiveStatusParams{})
	require.NoError(err)
	require.NotNil(status.JSON200)
	require.Len(*status.JSON200, 1)
	assert.Equal(generated.ArchiveStatusResponseStatusBlocked, (*status.JSON200)[0].Status)
	require.NotNil((*status.JSON200)[0].Failure)
	assert.Equal("authentication_failed", (*status.JSON200)[0].Failure.Code)

	require.NoError(service.RunEligible(t.Context()))
	assert.Equal(int32(1), provider.calls.Load(), "a deferred repository must not make another provider call")
}

func TestAPIArchiveInventoryPreservesDetailedMergeRequestFields(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := dbtest.Open(t)
	ref := platform.RepoRef{Platform: platform.KindGitHub, Host: "github.test", Owner: "owner", Name: "details", RepoPath: "owner/details"}
	older := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	newer := older.Add(time.Hour)
	repoID, err := database.UpsertRepo(t.Context(), platform.DBRepoIdentity(ref))
	require.NoError(err)
	detailFetchedAt := time.Now().UTC()
	_, err = database.UpsertMergeRequest(t.Context(), &db.MergeRequest{
		RepoID: repoID, PlatformID: 9, PlatformExternalID: "mr-9", Number: 9,
		Title: "detailed", State: db.MergeRequestStateOpen, Additions: 31, Deletions: 12,
		CommentCount: 4, ReviewDecision: "approved", CIStatus: "success",
		CIChecksJSON: `[{"name":"checks"}]`, MergeableState: "clean", DetailFetchedAt: &detailFetchedAt,
		CreatedAt: older.Add(-time.Hour), UpdatedAt: older, LastActivityAt: older,
	})
	require.NoError(err)
	provider := &archiveAPITestProvider{mergeRequests: map[string]platform.MergeRequest{ref.RepoPath: {
		Repo: ref, PlatformID: 9, PlatformExternalID: "mr-9", Number: 9,
		Title: "inventory title", State: "open", CreatedAt: older.Add(-time.Hour),
		UpdatedAt: newer, LastActivityAt: newer,
	}}}
	service, client := setupArchiveWorkflow(t, database, provider, ref)
	repositories := []generated.ArchiveRepositoryRef{archiveGeneratedRef(ref)}
	started, err := client.HTTP.StartArchivesWithResponse(t.Context(), generated.ArchiveMutationBody{Repositories: &repositories})
	require.NoError(err)
	require.NotNil(started.JSON200)
	require.NoError(service.RunEligible(t.Context()))
	require.NoError(service.RunEligible(t.Context()))

	var title, decision, ciStatus, checks, mergeable string
	var additions, deletions, comments int
	require.NoError(database.ReadDB().QueryRowContext(t.Context(), `
		SELECT title, additions, deletions, comment_count, review_decision,
			ci_status, ci_checks_json, mergeable_state
		FROM middleman_merge_requests WHERE repo_id = ? AND number = 9`, repoID,
	).Scan(&title, &additions, &deletions, &comments, &decision, &ciStatus, &checks, &mergeable))
	assert.Equal("inventory title", title)
	assert.Equal(31, additions)
	assert.Equal(12, deletions)
	assert.Equal(4, comments)
	assert.Equal("approved", decision)
	assert.Equal("success", ciStatus)
	assert.JSONEq(`[{"name":"checks"}]`, checks)
	assert.Equal("clean", mergeable)
	status, err := client.HTTP.ListArchiveStatusWithResponse(t.Context(), &generated.ListArchiveStatusParams{})
	require.NoError(err)
	require.NotNil(status.JSON200)
	require.Len(*status.JSON200, 1)
	assert.Equal(int64(1), (*status.JSON200)[0].Counts.PendingItems)
}

func TestAPIArchiveHydrationPreservesCurrentMergeRequestDetail(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := dbtest.Open(t)
	ref := platform.RepoRef{
		Platform: platform.KindGitHub, Host: "github.test", Owner: "owner",
		Name: "hydration-detail", RepoPath: "owner/hydration-detail",
	}
	older := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	newer := older.Add(time.Hour)
	repoID, err := database.UpsertRepo(t.Context(), platform.DBRepoIdentity(ref))
	require.NoError(err)
	detailFetchedAt := older.Add(30 * time.Minute)
	_, err = database.UpsertMergeRequest(t.Context(), &db.MergeRequest{
		RepoID: repoID, PlatformID: 11, PlatformExternalID: "mr-11", Number: 11,
		Title: "current detail", State: db.MergeRequestStateOpen,
		PlatformHeadSHA: "same-head", PlatformBaseSHA: "same-base",
		Additions: 23, Deletions: 7, CommentCount: 5, ReviewDecision: "approved",
		CIStatus: "success", CIChecksJSON: `[{"name":"archive-test"}]`,
		CIHadPending: true, MergeableState: "clean", DetailFetchedAt: &detailFetchedAt,
		CreatedAt: older.Add(-time.Hour), UpdatedAt: older, LastActivityAt: older,
	})
	require.NoError(err)
	provider := &archiveAPITestProvider{mergeRequests: map[string]platform.MergeRequest{
		ref.RepoPath: {
			Repo: ref, PlatformID: 11, PlatformExternalID: "mr-11", Number: 11,
			Title: "archive snapshot", State: "closed", HeadSHA: "same-head", BaseSHA: "same-base",
			Additions: 29, Deletions: 8, CommentCount: 6, MergeableState: "blocked",
			CreatedAt: older.Add(-time.Hour), UpdatedAt: newer, LastActivityAt: newer,
		},
	}}
	service, client := setupArchiveWorkflow(t, database, provider, ref)
	repositories := []generated.ArchiveRepositoryRef{archiveGeneratedRef(ref)}
	started, err := client.HTTP.StartArchivesWithResponse(t.Context(), generated.ArchiveMutationBody{
		Repositories: &repositories,
	})
	require.NoError(err)
	require.NotNil(started.JSON200)
	for range 3 {
		require.NoError(service.RunEligible(t.Context()))
	}

	var title, reviewDecision, ciStatus, checks, mergeable string
	var additions, deletions, comments int
	var hadPending bool
	var storedDetailFetchedAt time.Time
	require.NoError(database.ReadDB().QueryRowContext(t.Context(), `
		SELECT title, additions, deletions, comment_count, review_decision,
			ci_status, ci_checks_json, ci_had_pending, mergeable_state, detail_fetched_at
		FROM middleman_merge_requests WHERE repo_id = ? AND number = 11`, repoID,
	).Scan(&title, &additions, &deletions, &comments, &reviewDecision,
		&ciStatus, &checks, &hadPending, &mergeable, &storedDetailFetchedAt))
	assert.Equal("archive snapshot", title)
	assert.Equal(29, additions)
	assert.Equal(8, deletions)
	assert.Equal(6, comments)
	assert.Equal("approved", reviewDecision)
	assert.Equal("success", ciStatus)
	assert.JSONEq(`[{"name":"archive-test"}]`, checks)
	assert.True(hadPending)
	assert.Equal("blocked", mergeable)
	assert.Equal(detailFetchedAt, storedDetailFetchedAt)
}

func TestAPIArchiveInventoryDiffChangePreservesCommentCount(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := dbtest.Open(t)
	ref := platform.RepoRef{
		Platform: platform.KindGitHub, Host: "github.test", Owner: "owner",
		Name: "inventory-diff", RepoPath: "owner/inventory-diff",
	}
	older := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	newer := older.Add(time.Hour)
	repoID, err := database.UpsertRepo(t.Context(), platform.DBRepoIdentity(ref))
	require.NoError(err)
	_, err = database.UpsertMergeRequest(t.Context(), &db.MergeRequest{
		RepoID: repoID, PlatformID: 13, PlatformExternalID: "mr-13", Number: 13,
		Title: "old diff", State: db.MergeRequestStateOpen,
		PlatformHeadSHA: "old-head", PlatformBaseSHA: "old-base",
		Additions: 17, Deletions: 9, CommentCount: 4, ReviewDecision: "approved",
		CIStatus: "success", CIChecksJSON: `[{"name":"archive-test"}]`, MergeableState: "clean",
		CreatedAt: older.Add(-time.Hour), UpdatedAt: older, LastActivityAt: older,
	})
	require.NoError(err)
	provider := &archiveAPITestProvider{mergeRequests: map[string]platform.MergeRequest{
		ref.RepoPath: {
			Repo: ref, PlatformID: 13, PlatformExternalID: "mr-13", Number: 13,
			Title: "new diff", State: "open", HeadSHA: "new-head", BaseSHA: "new-base",
			CreatedAt: older.Add(-time.Hour), UpdatedAt: newer, LastActivityAt: newer,
		},
	}}
	service, client := setupArchiveWorkflow(t, database, provider, ref)
	repositories := []generated.ArchiveRepositoryRef{archiveGeneratedRef(ref)}
	started, err := client.HTTP.StartArchivesWithResponse(t.Context(), generated.ArchiveMutationBody{
		Repositories: &repositories,
	})
	require.NoError(err)
	require.NotNil(started.JSON200)
	require.NoError(service.RunEligible(t.Context())) // issue inventory
	require.NoError(service.RunEligible(t.Context())) // merge-request inventory

	stored, err := database.GetMergeRequestByRepoIDAndNumber(t.Context(), repoID, 13)
	require.NoError(err)
	require.NotNil(stored)
	assert.Equal("new-head", stored.PlatformHeadSHA)
	assert.Equal("new-base", stored.PlatformBaseSHA)
	assert.Equal(4, stored.CommentCount)
	assert.Zero(stored.Additions)
	assert.Zero(stored.Deletions)
	assert.Empty(stored.ReviewDecision)
	assert.Empty(stored.CIStatus)
	assert.Empty(stored.CIChecksJSON)
	assert.Empty(stored.MergeableState)
	status, err := client.HTTP.ListArchiveStatusWithResponse(
		t.Context(), &generated.ListArchiveStatusParams{},
	)
	require.NoError(err)
	require.NotNil(status.JSON200)
	require.Len(*status.JSON200, 1)
	assert.Equal(int64(1), (*status.JSON200)[0].Counts.PendingItems)
}

func TestAPIArchiveHydrationPreservesKnownMergeabilityWhileProviderCalculates(t *testing.T) {
	tests := []struct {
		name              string
		providerMergeable string
	}{
		{name: "empty", providerMergeable: ""},
		{name: "unknown", providerMergeable: "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			database := dbtest.Open(t)
			ref := platform.RepoRef{
				Platform: platform.KindGitHub, Host: "github.test", Owner: "owner",
				Name: "mergeability-" + tt.name, RepoPath: "owner/mergeability-" + tt.name,
			}
			older := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
			newer := older.Add(time.Hour)
			repoID, err := database.UpsertRepo(t.Context(), platform.DBRepoIdentity(ref))
			require.NoError(err)
			_, err = database.UpsertMergeRequest(t.Context(), &db.MergeRequest{
				RepoID: repoID, PlatformID: 12, PlatformExternalID: "mr-12", Number: 12,
				Title: "known mergeability", State: db.MergeRequestStateOpen,
				PlatformHeadSHA: "same-head", PlatformBaseSHA: "same-base", MergeableState: "clean",
				CreatedAt: older.Add(-time.Hour), UpdatedAt: older, LastActivityAt: older,
			})
			require.NoError(err)
			provider := &archiveAPITestProvider{mergeRequests: map[string]platform.MergeRequest{
				ref.RepoPath: {
					Repo: ref, PlatformID: 12, PlatformExternalID: "mr-12", Number: 12,
					Title: "provider calculating", State: "open",
					HeadSHA: "same-head", BaseSHA: "same-base", MergeableState: tt.providerMergeable,
					CreatedAt: older.Add(-time.Hour), UpdatedAt: newer, LastActivityAt: newer,
				},
			}}
			service, client := setupArchiveWorkflow(t, database, provider, ref)
			repositories := []generated.ArchiveRepositoryRef{archiveGeneratedRef(ref)}
			started, err := client.HTTP.StartArchivesWithResponse(t.Context(), generated.ArchiveMutationBody{
				Repositories: &repositories,
			})
			require.NoError(err)
			require.NotNil(started.JSON200)
			for range 3 {
				require.NoError(service.RunEligible(t.Context()))
			}

			stored, err := database.GetMergeRequestByRepoIDAndNumber(t.Context(), repoID, 12)
			require.NoError(err)
			require.NotNil(stored)
			assert.Equal("clean", stored.MergeableState)
			status, err := client.HTTP.ListArchiveStatusWithResponse(
				t.Context(), &generated.ListArchiveStatusParams{},
			)
			require.NoError(err)
			require.NotNil(status.JSON200)
			require.Len(*status.JSON200, 1)
			assert.Zero((*status.JSON200)[0].Counts.PendingItems)
		})
	}
}

func TestAPIArchiveNewerMergeRequestRefreshesEverySupportedDataset(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := dbtest.Open(t)
	ref := platform.RepoRef{
		Platform: platform.KindGitHub, Host: "github.test", Owner: "owner",
		Name: "mixed-datasets", RepoPath: "owner/mixed-datasets",
	}
	older := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	newer := older.Add(time.Hour)
	provider := &archiveAPITestProvider{mergeRequests: map[string]platform.MergeRequest{
		ref.RepoPath: {
			Repo: ref, PlatformID: 9, PlatformExternalID: "mr-9", Number: 9,
			Title: "initial snapshot", State: "closed", CreatedAt: older.Add(-time.Hour),
			UpdatedAt: older, LastActivityAt: older,
		},
	}}
	service, client := setupArchiveWorkflow(t, database, provider, ref)
	repositories := []generated.ArchiveRepositoryRef{archiveGeneratedRef(ref)}
	started, err := client.HTTP.StartArchivesWithResponse(t.Context(), generated.ArchiveMutationBody{
		Repositories: &repositories,
	})
	require.NoError(err)
	require.NotNil(started.JSON200)

	for range 3 {
		require.NoError(service.RunEligible(t.Context()))
	}
	assert.Equal(int32(1), provider.mrCommentCalls.Load())
	assert.Equal(int32(1), provider.mrReviewCalls.Load())
	assert.Equal(int32(1), provider.mrThreadCalls.Load())

	repo, err := database.GetRepoByIdentity(t.Context(), platform.DBRepoIdentity(ref))
	require.NoError(err)
	require.NotNil(repo)
	provider.mergeRequests[ref.RepoPath] = platform.MergeRequest{
		Repo: ref, PlatformID: 9, PlatformExternalID: "mr-9", Number: 9,
		Title: "newer snapshot", State: "closed", CreatedAt: older.Add(-time.Hour),
		UpdatedAt: newer, LastActivityAt: newer,
	}
	_, err = database.WriteDB().ExecContext(t.Context(), `
		UPDATE middleman_archive_dataset_progress
		SET status = 'pending', next_retry_at = NULL
		WHERE repo_id = ? AND item_type = 'merge_request' AND item_number = 9
			AND dataset = 'lookup'`, repo.ID,
	)
	require.NoError(err)

	require.NoError(service.RunEligible(t.Context()))
	assert.Equal(int32(2), provider.mrCommentCalls.Load())
	assert.Equal(int32(2), provider.mrReviewCalls.Load())
	assert.Equal(int32(2), provider.mrThreadCalls.Load())
	status, err := client.HTTP.ListArchiveStatusWithResponse(
		t.Context(), &generated.ListArchiveStatusParams{},
	)
	require.NoError(err)
	require.NotNil(status.JSON200)
	require.Len(*status.JSON200, 1)
	assert.Zero((*status.JSON200)[0].Counts.PendingItems)
}

func TestAPIArchiveAdvancedSnapshotFailureRetriesOnlyIncompleteDatasets(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := dbtest.Open(t)
	ref := platform.RepoRef{
		Platform: platform.KindGitHub, Host: "github.test", Owner: "owner",
		Name: "forced-retry", RepoPath: "owner/forced-retry",
	}
	older := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	newer := older.Add(time.Hour)
	provider := &archiveAPITestProvider{mergeRequests: map[string]platform.MergeRequest{
		ref.RepoPath: {
			Repo: ref, PlatformID: 10, PlatformExternalID: "mr-10", Number: 10,
			Title: "initial snapshot", State: "closed", CreatedAt: older.Add(-time.Hour),
			UpdatedAt: older, LastActivityAt: older,
		},
	}}
	service, client := setupArchiveWorkflow(t, database, provider, ref)
	repositories := []generated.ArchiveRepositoryRef{archiveGeneratedRef(ref)}
	started, err := client.HTTP.StartArchivesWithResponse(t.Context(), generated.ArchiveMutationBody{
		Repositories: &repositories,
	})
	require.NoError(err)
	require.NotNil(started.JSON200)
	for range 3 {
		require.NoError(service.RunEligible(t.Context()))
	}

	repo, err := database.GetRepoByIdentity(t.Context(), platform.DBRepoIdentity(ref))
	require.NoError(err)
	require.NotNil(repo)
	provider.mergeRequests[ref.RepoPath] = platform.MergeRequest{
		Repo: ref, PlatformID: 10, PlatformExternalID: "mr-10", Number: 10,
		Title: "newer snapshot", State: "closed", CreatedAt: older.Add(-time.Hour),
		UpdatedAt: newer, LastActivityAt: newer,
	}
	provider.failMRReviewOnce.Store(true)
	_, err = database.WriteDB().ExecContext(t.Context(), `
		UPDATE middleman_archive_dataset_progress
		SET status = 'pending', next_retry_at = NULL
		WHERE repo_id = ? AND item_type = 'merge_request' AND item_number = 10
			AND dataset = 'lookup'`, repo.ID,
	)
	require.NoError(err)

	require.Error(service.RunEligible(t.Context()))
	var commentsStatus, reviewsStatus, inlineStatus string
	var retryAt *string
	require.NoError(database.ReadDB().QueryRowContext(t.Context(), `
		SELECT c.status, r.status, i.status, r.next_retry_at
		FROM middleman_archive_dataset_progress c
		JOIN middleman_archive_dataset_progress r
			ON r.repo_id = c.repo_id AND r.item_type = c.item_type
			AND r.item_number = c.item_number AND r.dataset = 'reviews'
		JOIN middleman_archive_dataset_progress i
			ON i.repo_id = c.repo_id AND i.item_type = c.item_type
			AND i.item_number = c.item_number AND i.dataset = 'inline_comments'
		WHERE c.repo_id = ? AND c.item_type = 'merge_request'
			AND c.item_number = 10 AND c.dataset = 'comments'`,
		repo.ID,
	).Scan(&commentsStatus, &reviewsStatus, &inlineStatus, &retryAt))
	assert.Equal("complete", commentsStatus)
	assert.Equal("failed", reviewsStatus)
	assert.Equal("pending", inlineStatus,
		"a failed dataset must not fail siblings that never started")
	require.NotNil(retryAt)

	_, err = database.WriteDB().ExecContext(t.Context(), `
		UPDATE middleman_archive_dataset_progress SET next_retry_at = NULL
		WHERE repo_id = ? AND item_type = 'merge_request' AND item_number = 10`, repo.ID)
	require.NoError(err)
	require.NoError(service.RunEligible(t.Context()))
	status, err := client.HTTP.ListArchiveStatusWithResponse(
		t.Context(), &generated.ListArchiveStatusParams{},
	)
	require.NoError(err)
	require.NotNil(status.JSON200)
	require.Len(*status.JSON200, 1)
	assert.Zero((*status.JSON200)[0].Counts.PendingItems)
	assert.Equal(int32(2), provider.mrCommentCalls.Load())
	assert.Equal(int32(3), provider.mrReviewCalls.Load())
	assert.Equal(int32(2), provider.mrThreadCalls.Load())
}

type archiveAPITestProvider struct {
	mu                    sync.Mutex
	calls                 atomic.Int32
	issues                map[string]platform.Issue
	mergeRequests         map[string]platform.MergeRequest
	movedDestinations     map[string]platform.RepoRef
	movedMRDestinations   map[string]platform.RepoRef
	issueInventoryErr     error
	failSecondCommentPage bool
	issueCommentCursors   []string
	issueCommentStarted   chan struct{}
	issueCommentRelease   chan struct{}
	issueCommentBlocked   bool
	mrCommentCalls        atomic.Int32
	mrReviewCalls         atomic.Int32
	mrThreadCalls         atomic.Int32
	failMRReviewOnce      atomic.Bool
}

func (p *archiveAPITestProvider) Platform() platform.Kind { return platform.KindGitHub }
func (p *archiveAPITestProvider) Host() string            { return "github.test" }
func (p *archiveAPITestProvider) Capabilities() platform.Capabilities {
	return platform.Capabilities{
		ReadIssues: true, ReadMergeRequests: true,
		Archive: platform.ArchiveCapabilities{
			HistoricalIssues: true, HistoricalMergeRequests: true, OrdinaryComments: true,
			SubmittedReviews: true, InlineReviewComments: true,
		},
	}
}
func (p *archiveAPITestProvider) pageCall() {
	p.calls.Add(1)
}
func (p *archiveAPITestProvider) ListIssuesPage(_ context.Context, ref platform.RepoRef, query platform.ItemPageQuery) (platform.Page[platform.Issue], error) {
	p.pageCall()
	if query.UpdatedSince != nil {
		return platform.Page[platform.Issue]{Exhausted: true}, nil
	}
	if p.issueInventoryErr != nil {
		return platform.Page[platform.Issue]{}, p.issueInventoryErr
	}
	if issue, ok := p.issues[ref.RepoPath]; ok {
		return platform.Page[platform.Issue]{Items: []platform.Issue{issue}, Exhausted: true}, nil
	}
	return platform.Page[platform.Issue]{Exhausted: true}, nil
}
func (p *archiveAPITestProvider) ListMergeRequestsPage(_ context.Context, ref platform.RepoRef, query platform.ItemPageQuery) (platform.Page[platform.MergeRequest], error) {
	p.pageCall()
	if query.UpdatedSince != nil {
		return platform.Page[platform.MergeRequest]{Exhausted: true}, nil
	}
	if mr, ok := p.mergeRequests[ref.RepoPath]; ok {
		return platform.Page[platform.MergeRequest]{Items: []platform.MergeRequest{mr}, Exhausted: true}, nil
	}
	return platform.Page[platform.MergeRequest]{Exhausted: true}, nil
}
func (p *archiveAPITestProvider) LookupIssue(_ context.Context, ref platform.RepoRef, _ int) (platform.ItemLookup[platform.Issue], error) {
	p.pageCall()
	if destination, ok := p.movedDestinations[ref.RepoPath]; ok {
		return platform.ItemLookup[platform.Issue]{
			Outcome: platform.LookupMoved, Destination: &destination,
		}, nil
	}
	if issue, ok := p.issues[ref.RepoPath]; ok {
		return platform.ItemLookup[platform.Issue]{Outcome: platform.LookupPresent, Item: issue}, nil
	}
	return platform.ItemLookup[platform.Issue]{Outcome: platform.LookupRemoved}, nil
}
func (p *archiveAPITestProvider) LookupMergeRequest(_ context.Context, ref platform.RepoRef, _ int) (platform.ItemLookup[platform.MergeRequest], error) {
	p.pageCall()
	if destination, ok := p.movedMRDestinations[ref.RepoPath]; ok {
		return platform.ItemLookup[platform.MergeRequest]{
			Outcome: platform.LookupMoved, Destination: &destination,
		}, nil
	}
	if mr, ok := p.mergeRequests[ref.RepoPath]; ok {
		return platform.ItemLookup[platform.MergeRequest]{Outcome: platform.LookupPresent, Item: mr}, nil
	}
	return platform.ItemLookup[platform.MergeRequest]{Outcome: platform.LookupRemoved}, nil
}
func (p *archiveAPITestProvider) ListIssueCommentsPage(_ context.Context, ref platform.RepoRef, number int, cursor string) (platform.Page[platform.IssueEvent], error) {
	p.pageCall()
	p.mu.Lock()
	p.issueCommentCursors = append(p.issueCommentCursors, cursor)
	fail := cursor == "c2" && p.failSecondCommentPage
	block := cursor == "" && p.issueCommentStarted != nil && !p.issueCommentBlocked
	if block {
		p.issueCommentBlocked = true
		close(p.issueCommentStarted)
	}
	release := p.issueCommentRelease
	p.mu.Unlock()
	if block {
		<-release
	}
	if fail {
		return platform.Page[platform.IssueEvent]{}, errors.New("second comment page failed")
	}
	event := platform.IssueEvent{Repo: ref, PlatformExternalID: "comment-1", IssueNumber: number,
		EventType: "issue_comment", Author: "alice", Body: "first", CreatedAt: time.Date(2026, 7, 1, 12, 1, 0, 0, time.UTC), DedupeKey: "comment-1"}
	if cursor == "" {
		return platform.Page[platform.IssueEvent]{Items: []platform.IssueEvent{event}, NextCursor: "c2"}, nil
	}
	event.PlatformExternalID, event.Body, event.DedupeKey = "comment-2", "second", "comment-2"
	event.CreatedAt = event.CreatedAt.Add(time.Minute)
	return platform.Page[platform.IssueEvent]{Items: []platform.IssueEvent{event}, Exhausted: true}, nil
}
func (p *archiveAPITestProvider) ListMergeRequestCommentsPage(context.Context, platform.RepoRef, int, string) (platform.Page[platform.MergeRequestEvent], error) {
	p.pageCall()
	p.mrCommentCalls.Add(1)
	return platform.Page[platform.MergeRequestEvent]{Exhausted: true}, nil
}
func (p *archiveAPITestProvider) ListSubmittedReviewsPage(context.Context, platform.RepoRef, int, string) (platform.Page[platform.MergeRequestEvent], error) {
	p.pageCall()
	p.mrReviewCalls.Add(1)
	if p.failMRReviewOnce.CompareAndSwap(true, false) {
		return platform.Page[platform.MergeRequestEvent]{}, errors.New("synthetic review refresh failure")
	}
	return platform.Page[platform.MergeRequestEvent]{Exhausted: true}, nil
}
func (p *archiveAPITestProvider) ListReviewThreadsPage(context.Context, platform.RepoRef, int, string) (platform.Page[platform.MergeRequestReviewThread], error) {
	p.pageCall()
	p.mrThreadCalls.Add(1)
	return platform.Page[platform.MergeRequestReviewThread]{Exhausted: true}, nil
}

type archiveAPITestSource struct{ refs []platform.RepoRef }

func (s archiveAPITestSource) ConfiguredRepositories(context.Context) ([]platform.RepoRef, error) {
	return s.refs, nil
}

type archiveLimitController struct{ archive.Controller }

func (archiveLimitController) Report(context.Context, archive.ReportOptions) (report.Model, error) {
	return report.Model{}, &report.LimitError{
		ObservedRecords: 10_001, MaxRecords: 10_000,
		ObservedTextBytes: 33_554_433, MaxTextBytes: 33_554_432,
	}
}

type archiveStatusController struct{ archive.Controller }

func (archiveStatusController) Status(context.Context, []platform.RepoRef) ([]archive.Status, error) {
	return []archive.Status{}, nil
}

func setupArchiveWorkflow(
	t *testing.T,
	database *db.DB,
	provider *archiveAPITestProvider,
	ref platform.RepoRef,
) (*archive.Service, *apiclient.Client) {
	t.Helper()
	_, err := database.UpsertRepo(t.Context(), platform.DBRepoIdentity(ref))
	require.NoError(t, err)
	registry, err := platform.NewRegistry(provider)
	require.NoError(t, err)
	service, err := archive.NewService(
		database, registry, nil, archiveAPITestSource{refs: []platform.RepoRef{ref}}, nil, nil,
	)
	require.NoError(t, err)
	require.NoError(t, service.EnsureConfigured(t.Context(), []platform.RepoRef{ref}))
	syncer := ghclient.NewSyncer(nil, database, nil, nil, time.Minute, nil, nil)
	t.Cleanup(syncer.Stop)
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{Archive: service})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, srv.Shutdown(ctx))
	})
	return service, setupTestClient(t, srv)
}

func setupArchiveTestServer(
	t *testing.T,
	controller archive.Controller,
) (*server.Server, *db.DB, *archiveAPITestProvider, *atomic.Int32, platform.RepoRef) {
	t.Helper()
	database := dbtest.Open(t)
	ref := platform.RepoRef{
		Platform: platform.KindGitHub, Host: "github.test", Owner: "owner",
		Name: "repo", RepoPath: "owner/repo",
	}
	_, err := database.UpsertRepo(t.Context(), platform.DBRepoIdentity(ref))
	require.NoError(t, err)
	provider := &archiveAPITestProvider{}
	wakeCount := &atomic.Int32{}
	if controller == nil {
		registry, registryErr := platform.NewRegistry(provider)
		require.NoError(t, registryErr)
		service, serviceErr := archive.NewService(
			database, registry, nil,
			archiveAPITestSource{refs: []platform.RepoRef{ref}}, nil, nil,
		)
		require.NoError(t, serviceErr)
		service.SetWake(func() { wakeCount.Add(1) })
		require.NoError(t, service.EnsureConfigured(t.Context(), []platform.RepoRef{ref}))
		controller = service
	}
	syncer := ghclient.NewSyncer(nil, database, nil, nil, time.Minute, nil, nil)
	t.Cleanup(syncer.Stop)
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{Archive: controller})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, srv.Shutdown(ctx))
	})
	return srv, database, provider, wakeCount, ref
}

func archiveGeneratedRef(ref platform.RepoRef) generated.ArchiveRepositoryRef {
	return generated.ArchiveRepositoryRef{
		Provider: string(ref.Platform), PlatformHost: ref.Host, Owner: ref.Owner,
		Name: ref.Name, RepoPath: ref.RepoPath,
	}
}
