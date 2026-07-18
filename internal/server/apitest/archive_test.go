package apitest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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

func TestAPIArchiveResetScanScopesRecovery(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	srv, database, _, _, ref := setupArchiveTestServer(t, nil)
	repo, err := database.GetRepoByIdentity(t.Context(), platform.DBRepoIdentity(ref))
	require.NoError(err)
	require.NotNil(repo)
	_, err = database.WriteDB().ExecContext(t.Context(), `
		UPDATE middleman_archive_repo_scans
		SET scan_generation = 7, next_cursor = 'issue-p9', last_input_cursor = 'issue-p8',
			page_count = 10000, status = 'blocked', last_error_code = 'page_bound'
		WHERE repo_id = ? AND scan = 'issue_inventory'`, repo.ID)
	require.NoError(err)

	requestBody := func(force bool) string {
		return fmt.Sprintf(`{
			"repository": {
				"provider": %q, "platform_host": %q, "owner": %q,
				"name": %q, "repo_path": %q
			},
			"scan": "issue_inventory", "force": %t
		}`, ref.Platform, ref.Host, ref.Owner, ref.Name, ref.RepoPath, force)
	}
	request := func(body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/archive/reset", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Host = "middleman.test"
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, req)
		return rr
	}

	rr := request(requestBody(false))
	assert.Equal(http.StatusNoContent, rr.Code, rr.Body.String())
	states, err := database.ListArchiveRepoStates(t.Context(), []int64{repo.ID})
	require.NoError(err)
	require.Len(states, 1)
	assert.Equal(int64(8), states[0].IssueInventory.Generation)
	assert.Empty(states[0].IssueInventory.Cursor())
	assert.Zero(states[0].IssueInventory.PageCount)
	assert.Equal(db.ArchiveScanPending, states[0].IssueInventory.Status)

	_, err = database.WriteDB().ExecContext(t.Context(), `
		UPDATE middleman_archive_repo_scans
		SET status = 'complete', last_error_code = NULL
		WHERE repo_id = ? AND scan = 'issue_inventory'`, repo.ID)
	require.NoError(err)
	rr = request(requestBody(false))
	assert.Equal(http.StatusBadRequest, rr.Code, rr.Body.String())
	var problem server.ProblemError
	require.NoError(json.Unmarshal(rr.Body.Bytes(), &problem))
	assert.Equal("not_blocked", problem.Details["reason"])
	rr = request(requestBody(true))
	assert.Equal(http.StatusNoContent, rr.Code, rr.Body.String())
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

type archiveAPITestProvider struct{ calls atomic.Int32 }

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
func (p *archiveAPITestProvider) pageCall() { p.calls.Add(1) }
func (p *archiveAPITestProvider) ListIssuesPage(context.Context, platform.RepoRef, platform.ItemPageQuery) (platform.Page[platform.Issue], error) {
	p.pageCall()
	return platform.Page[platform.Issue]{Exhausted: true}, nil
}
func (p *archiveAPITestProvider) ListMergeRequestsPage(context.Context, platform.RepoRef, platform.ItemPageQuery) (platform.Page[platform.MergeRequest], error) {
	p.pageCall()
	return platform.Page[platform.MergeRequest]{Exhausted: true}, nil
}
func (p *archiveAPITestProvider) LookupIssue(context.Context, platform.RepoRef, int) (platform.ItemLookup[platform.Issue], error) {
	p.pageCall()
	return platform.ItemLookup[platform.Issue]{Outcome: platform.LookupRemoved}, nil
}
func (p *archiveAPITestProvider) LookupMergeRequest(context.Context, platform.RepoRef, int) (platform.ItemLookup[platform.MergeRequest], error) {
	p.pageCall()
	return platform.ItemLookup[platform.MergeRequest]{Outcome: platform.LookupRemoved}, nil
}
func (p *archiveAPITestProvider) ListIssueCommentsPage(context.Context, platform.RepoRef, int, string) (platform.Page[platform.IssueEvent], error) {
	p.pageCall()
	return platform.Page[platform.IssueEvent]{Exhausted: true}, nil
}
func (p *archiveAPITestProvider) ListMergeRequestCommentsPage(context.Context, platform.RepoRef, int, string) (platform.Page[platform.MergeRequestEvent], error) {
	p.pageCall()
	return platform.Page[platform.MergeRequestEvent]{Exhausted: true}, nil
}
func (p *archiveAPITestProvider) ListSubmittedReviewsPage(context.Context, platform.RepoRef, int, string) (platform.Page[platform.MergeRequestEvent], error) {
	p.pageCall()
	return platform.Page[platform.MergeRequestEvent]{Exhausted: true}, nil
}
func (p *archiveAPITestProvider) ListReviewThreadsPage(context.Context, platform.RepoRef, int, string) (platform.Page[platform.MergeRequestReviewThread], error) {
	p.pageCall()
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
