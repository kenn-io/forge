package archive

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/archive/report"
	"go.kenn.io/middleman/internal/db"
	"go.kenn.io/middleman/internal/platform"
	"go.kenn.io/middleman/internal/testutil/dbtest"
)

func TestArchiveRetryClassifierTreatsAttemptBudgetRefusalAsTransient(t *testing.T) {
	assert := assert.New(t)
	now := archiveTestTime()
	// A budget-transport refusal may reach the classifier bare or wrapped by a
	// provider error mapper. GitLab maps unclassified transport errors to its
	// default invalid_repo_ref code, which would otherwise classify as a
	// repository-blocking contract error. The refusal must stay a transient
	// budget deferral in every form.
	wrapped := &platform.Error{
		Code: platform.ErrCodeInvalidRepoRef, Err: platform.ErrArchiveAttemptBudget,
	}
	for name, cause := range map[string]error{
		"bare":              platform.ErrArchiveAttemptBudget,
		"provider-contract": wrapped,
		"deeply-wrapped":    errors.Join(errors.New("list historical issues"), wrapped),
	} {
		t.Run(name, func(t *testing.T) {
			decision := defaultArchiveRetryDecision(cause, 0, now)
			assert.Equal(db.ArchiveErrorCodeTransient, decision.Code)
			assert.NotNil(decision.RetryAt)
		})
	}
}

func TestArchiveServiceStartValidatesAllRepositoriesBeforePromotion(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := dbtest.Open(t)
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	valid := archiveServiceRef(platform.KindGitHub, "github.test", "one")
	missing := archiveServiceRef(platform.KindGitLab, "gitlab.test", "two")
	validID := archiveServiceSeedRepo(t, database, valid)
	archiveServiceSeedRepo(t, database, missing)
	provider := newArchiveServiceProvider(valid.Platform, valid.Host)
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)
	service := newArchiveTestService(t, database, registry, []platform.RepoRef{valid, missing}, nil, now)
	require.NoError(service.EnsureConfigured(t.Context(), []platform.RepoRef{valid}))

	_, err = service.Start(t.Context(), []platform.RepoRef{valid, missing})
	require.Error(err)
	states, err := database.ListArchiveRepoStates(t.Context(), []int64{validID})
	require.NoError(err)
	assert.Equal(db.ArchiveCollectionModeDiscovery, states[0].CollectionMode)
}

func TestArchiveServiceEnsureConfiguredSeedsFreshRepository(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := dbtest.Open(t)
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	ref := archiveServiceRef(platform.KindGitHub, "github.test", "fresh")
	provider := newArchiveServiceProvider(ref.Platform, ref.Host)
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)
	service := newArchiveTestService(t, database, registry, []platform.RepoRef{ref}, nil, now)

	require.NoError(service.EnsureConfigured(t.Context(), []platform.RepoRef{ref}))
	repo, err := database.GetRepoByIdentity(t.Context(), platform.DBRepoIdentity(ref))
	require.NoError(err)
	require.NotNil(repo)
	states, err := database.ListArchiveRepoStates(t.Context(), []int64{repo.ID})
	require.NoError(err)
	require.Len(states, 1)
	assert.Equal(db.ArchiveCollectionModeDiscovery, states[0].CollectionMode)
	assert.Equal(db.ArchiveOperatorStateActive, states[0].OperatorState)
}

func TestArchiveServiceAllScopeAndWakeLifecycle(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := dbtest.Open(t)
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	first := archiveServiceRef(platform.KindGitHub, "github.test", "one")
	second := archiveServiceRef(platform.KindGitHub, "enterprise.test", "two")
	archiveServiceSeedRepo(t, database, first)
	archiveServiceSeedRepo(t, database, second)
	firstProvider := newArchiveServiceProvider(first.Platform, first.Host)
	secondProvider := newArchiveServiceProvider(second.Platform, second.Host)
	registry, err := platform.NewRegistry(firstProvider, secondProvider)
	require.NoError(err)
	service := newArchiveTestService(
		t, database, registry, []platform.RepoRef{second, first}, nil, now,
	)
	wakeCount := 0
	service.SetWake(func() { wakeCount++ })
	require.NoError(service.EnsureConfigured(t.Context(), []platform.RepoRef{first, second}))

	started, err := service.StartAll(t.Context())
	require.NoError(err)
	require.Len(started, 2)
	assert.Equal("enterprise.test", started[0].Repo.Host)
	assert.Equal("github.test", started[1].Repo.Host)
	assert.Equal(1, wakeCount)

	statuses, err := service.Status(t.Context(), nil)
	require.NoError(err)
	require.Len(statuses, 2)
	assert.Equal("enterprise.test", statuses[0].Repo.Host)
	assert.Equal("github.test", statuses[1].Repo.Host)

	paused, err := service.PauseAll(t.Context())
	require.NoError(err)
	require.Len(paused, 2)
	assert.Equal(db.ArchiveStatusPaused, paused[0].Progress.Status)
	assert.Equal(db.ArchiveStatusPaused, paused[1].Progress.Status)
}

func TestArchiveServicePauseRejectsInFlightInventoryCommit(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := dbtest.Open(t)
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	ref := archiveServiceRef(platform.KindGitHub, "github.test", "repo")
	repoID := archiveServiceSeedRepo(t, database, ref)
	provider := newArchiveServiceProvider(ref.Platform, ref.Host)
	provider.issueInventoryStarted = make(chan struct{})
	provider.issueInventoryRelease = make(chan struct{})
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)
	service := newArchiveTestService(t, database, registry, []platform.RepoRef{ref}, nil, now)
	require.NoError(service.EnsureConfigured(t.Context(), []platform.RepoRef{ref}))
	_, err = service.Start(t.Context(), []platform.RepoRef{ref})
	require.NoError(err)

	runDone := make(chan error, 1)
	go func() { runDone <- service.RunEligible(t.Context()) }()
	<-provider.issueInventoryStarted
	paused, err := service.Pause(t.Context(), []platform.RepoRef{ref})
	require.NoError(err)
	require.Len(paused, 1)
	assert.Equal(db.ArchiveStatusPaused, paused[0].Progress.Status)
	close(provider.issueInventoryRelease)
	require.NoError(<-runDone)

	states, err := database.ListArchiveRepoStates(t.Context(), []int64{repoID})
	require.NoError(err)
	require.Len(states, 1)
	assert.False(states[0].IssueInventoryComplete)
	assert.Nil(states[0].IssueCursor)
	var itemCount int
	require.NoError(database.ReadDB().QueryRowContext(t.Context(), `
		SELECT COUNT(*) FROM middleman_archive_items WHERE repo_id = ?`, repoID,
	).Scan(&itemCount))
	assert.Zero(itemCount)
}

func TestArchiveServiceRetryAuthenticationPreservesProgress(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := dbtest.Open(t)
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	ref := archiveServiceRef(platform.KindGitHub, "github.test", "repo")
	repoID := archiveServiceSeedRepo(t, database, ref)
	provider := newArchiveServiceProvider(ref.Platform, ref.Host)
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)
	service := newArchiveTestService(t, database, registry, []platform.RepoRef{ref}, nil, now)
	require.NoError(service.EnsureConfigured(t.Context(), []platform.RepoRef{ref}))
	cursor := "issue-page-2"
	watermark := now.Add(-time.Hour)
	_, err = database.WriteDB().ExecContext(t.Context(), `
		UPDATE middleman_archive_repos
		SET issue_cursor = ?,
			maintenance_watermark = ?, last_error_code = 'authentication_failed',
			last_error_detail = 'expired token', next_retry_at = ?
		WHERE repo_id = ?`, cursor, watermark, now.Add(time.Hour), repoID)
	require.NoError(err)

	require.NoError(service.RetryAuthentication(t.Context(), []platform.RepoRef{ref}))
	states, err := database.ListArchiveRepoStates(t.Context(), []int64{repoID})
	require.NoError(err)
	require.Len(states, 1)
	assert.Equal(&cursor, states[0].IssueCursor)
	assert.Equal(&watermark, states[0].MaintenanceWatermark)
	assert.Nil(states[0].LastErrorCode)
	assert.Nil(states[0].LastErrorDetail)
	assert.Nil(states[0].NextRetryAt)
}

func TestArchiveAuthenticationFailureDefersPendingHydration(t *testing.T) {
	require := require.New(t)
	database := dbtest.Open(t)
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	ref := archiveServiceRef(platform.KindGitHub, "github.test", "repo")
	repoID := archiveServiceSeedRepo(t, database, ref)
	provider := newArchiveServiceProvider(ref.Platform, ref.Host)
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)
	service := newArchiveTestService(t, database, registry, []platform.RepoRef{ref}, nil, now)
	require.NoError(service.EnsureConfigured(t.Context(), []platform.RepoRef{ref}))
	_, err = service.Start(t.Context(), []platform.RepoRef{ref})
	require.NoError(err)
	require.NoError(service.RunEligible(t.Context()))

	_, err = database.WriteDB().ExecContext(t.Context(), `
		UPDATE middleman_archive_repos
		SET last_error_code = 'authentication_failed', last_error_detail = 'expired token',
			next_retry_at = NULL
		WHERE repo_id = ?`, repoID)
	require.NoError(err)
	provider.mu.Lock()
	provider.calls = nil
	provider.mu.Unlock()

	require.NoError(service.RunEligible(t.Context()))
	provider.mu.Lock()
	calls := append([]string(nil), provider.calls...)
	provider.mu.Unlock()
	assert.Empty(t, calls)
}

func TestArchiveIdlePollDoesNotReconcileConfiguredRepositories(t *testing.T) {
	require := require.New(t)
	database := dbtest.Open(t)
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	ref := archiveServiceRef(platform.KindGitHub, "github.test", "idle")
	archiveServiceSeedRepo(t, database, ref)
	provider := newArchiveServiceProvider(ref.Platform, ref.Host)
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)
	service := newArchiveTestService(t, database, registry, []platform.RepoRef{ref}, nil, now)
	require.NoError(service.EnsureConfigured(t.Context(), []platform.RepoRef{ref}))
	_, err = service.Pause(t.Context(), []platform.RepoRef{ref})
	require.NoError(err)
	_, err = database.WriteDB().ExecContext(t.Context(), `
		CREATE TRIGGER reject_idle_repo_reconcile BEFORE UPDATE ON middleman_repos
		BEGIN SELECT RAISE(ABORT, 'idle poll wrote repository'); END`)
	require.NoError(err)
	_, err = database.WriteDB().ExecContext(t.Context(), `
		CREATE TRIGGER reject_idle_archive_reconcile BEFORE UPDATE ON middleman_archive_repos
		BEGIN SELECT RAISE(ABORT, 'idle poll wrote archive state'); END`)
	require.NoError(err)

	require.NoError(service.RunEligible(t.Context()))
}

func TestArchiveServiceRemovedRepositoryStopsWorkAndReaddResumesState(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := dbtest.Open(t)
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	ref := archiveServiceRef(platform.KindGitHub, "github.test", "repo")
	repoID := archiveServiceSeedRepo(t, database, ref)
	provider := newArchiveServiceProvider(ref.Platform, ref.Host)
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)
	source := &archiveMutableSource{refs: []platform.RepoRef{ref}}
	service, err := NewService(database, registry, nil, source, nil, fixedClock{value: now})
	require.NoError(err)
	require.NoError(service.EnsureConfigured(t.Context(), source.refs))
	_, err = service.Start(t.Context(), source.refs)
	require.NoError(err)
	cursor := "durable-cursor"
	_, err = database.WriteDB().ExecContext(t.Context(), `
		UPDATE middleman_archive_repos SET issue_cursor = ? WHERE repo_id = ?`, cursor, repoID)
	require.NoError(err)

	source.refs = nil
	require.NoError(service.EnsureConfigured(t.Context(), source.refs))
	require.NoError(service.RunEligible(t.Context()))
	assert.Empty(provider.calls)
	states, err := database.ListArchiveRepoStates(t.Context(), []int64{repoID})
	require.NoError(err)
	assert.Equal(db.ArchiveCollectionModeFull, states[0].CollectionMode)
	assert.Equal(db.ArchiveOperatorStatePaused, states[0].OperatorState)
	require.NotNil(states[0].LastErrorCode)
	assert.Equal(string(db.ArchiveErrorCodeConfigurationRemoved), *states[0].LastErrorCode)
	assert.Equal(&cursor, states[0].IssueCursor)

	source.refs = []platform.RepoRef{ref}
	require.NoError(service.EnsureConfigured(t.Context(), source.refs))
	states, err = database.ListArchiveRepoStates(t.Context(), []int64{repoID})
	require.NoError(err)
	assert.Equal(db.ArchiveCollectionModeFull, states[0].CollectionMode)
	assert.Equal(db.ArchiveOperatorStateActive, states[0].OperatorState)
	assert.Nil(states[0].LastErrorCode)
	assert.Equal(&cursor, states[0].IssueCursor)

	require.NoError(database.PauseArchives(t.Context(), []int64{repoID}, now.Add(time.Minute)))
	source.refs = nil
	require.NoError(service.EnsureConfigured(t.Context(), source.refs))
	source.refs = []platform.RepoRef{ref}
	require.NoError(service.EnsureConfigured(t.Context(), source.refs))
	states, err = database.ListArchiveRepoStates(t.Context(), []int64{repoID})
	require.NoError(err)
	assert.Equal(db.ArchiveOperatorStatePaused, states[0].OperatorState)
	assert.Nil(states[0].LastErrorCode)
}

func TestArchiveServiceInventoriesThenCommitsCompleteHydration(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := dbtest.Open(t)
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	ref := archiveServiceRef(platform.KindGitHub, "github.test", "repo")
	repoID := archiveServiceSeedRepo(t, database, ref)
	provider := newArchiveServiceProvider(ref.Platform, ref.Host)
	provider.caps.Archive.SubmittedReviews = false
	provider.caps.Archive.InlineReviewComments = false
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)
	admission := &archiveTestAdmission{}
	service := newArchiveTestService(t, database, registry, []platform.RepoRef{ref}, admission, now)
	require.NoError(service.EnsureConfigured(t.Context(), []platform.RepoRef{ref}))
	_, err = service.Start(t.Context(), []platform.RepoRef{ref})
	require.NoError(err)

	require.NoError(service.RunEligible(t.Context())) // issue inventory
	require.NoError(service.RunEligible(t.Context())) // merge-request inventory
	require.NoError(service.RunEligible(t.Context())) // issue hydration
	require.NoError(service.RunEligible(t.Context())) // merge-request hydration

	states, err := database.ListArchiveRepoStates(t.Context(), []int64{repoID})
	require.NoError(err)
	assert.True(states[0].IssueInventoryComplete)
	assert.True(states[0].MergeRequestInventoryComplete)
	var comments, reviews, inline string
	var attempts int
	require.NoError(database.ReadDB().QueryRowContext(t.Context(), `
		SELECT comments_status, reviews_status, inline_comments_status, attempt_count
		FROM middleman_archive_items
		WHERE repo_id = ? AND item_type = 'issue' AND item_number = 1`, repoID,
	).Scan(&comments, &reviews, &inline, &attempts))
	assert.Equal("complete", comments)
	assert.Equal("not_applicable", reviews)
	assert.Equal("not_applicable", inline)
	assert.Zero(attempts)
	var eventCount int
	require.NoError(database.ReadDB().QueryRowContext(t.Context(), `
		SELECT COUNT(*) FROM middleman_issue_events WHERE event_type = 'issue_comment'`,
	).Scan(&eventCount))
	assert.Equal(2, eventCount)
	status, err := service.Status(t.Context(), []platform.RepoRef{ref})
	require.NoError(err)
	assert.Equal(db.ArchiveStatusPartial, status[0].Progress.Status)
	assert.Equal([]string{"issues", "merge_requests", "get_issue", "issue_comments:", "issue_comments:c2", "get_mr", "mr_comments:"}, provider.calls)
	assert.Equal(7, admission.calls)
	// Every admission declares 2x its logical request count so a
	// 401-invalidate-retry can never overspend the admitted ceiling:
	// inventory and dataset pages are 1 logical request (cost 2), issue
	// lookups are 2 logical requests (lookup + repository probe, cost 4),
	// and merge-request lookups are 3 (lookup + probe + best-effort fork
	// head clone-URL enrichment, cost 6).
	assert.Equal([]int{2, 2, 4, 2, 2, 6, 2}, admission.costs)
}

func TestArchiveOpenMergeRequestResumesAcrossBudgetAndDatasetDeferrals(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := dbtest.Open(t)
	now := archiveTestTime()
	ref := archiveServiceRef(platform.KindGitHub, "github.test", "resumable-mr")
	repoID := archiveServiceSeedRepo(t, database, ref)
	mr := archiveTestMergeRequest(ref)
	mr.State = "open"
	provider := newArchiveServiceProvider(ref.Platform, ref.Host)
	provider.mrLookupItem = &mr
	provider.mrCommentPages = map[string]platform.Page[platform.MergeRequestEvent]{
		"": {
			Items: []platform.MergeRequestEvent{{
				Repo: ref, PlatformID: 20, PlatformExternalID: "comment-20",
				MergeRequestNumber: mr.Number, EventType: "issue_comment",
				CreatedAt: now, DedupeKey: "mr-comment-20",
			}},
			NextCursor: "comments-page-2",
		},
		"comments-page-2": {
			Items: []platform.MergeRequestEvent{{
				Repo: ref, PlatformID: 21, PlatformExternalID: "comment-21",
				MergeRequestNumber: mr.Number, EventType: "issue_comment",
				CreatedAt: now, DedupeKey: "mr-comment-21",
			}},
			Exhausted: true,
		},
	}
	provider.reviewPages = map[string]platform.Page[platform.MergeRequestEvent]{
		"": {
			Items: []platform.MergeRequestEvent{{
				Repo: ref, PlatformID: 30, PlatformExternalID: "review-30",
				MergeRequestNumber: mr.Number, EventType: "review",
				CreatedAt: now, DedupeKey: "review-30",
			}},
			Exhausted: true,
		},
	}
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)
	admission := &archiveTestAdmission{denyAfter: 2, retryAt: now.Add(time.Hour)}
	service := newArchiveTestService(t, database, registry, []platform.RepoRef{ref}, admission, now)
	require.NoError(service.EnsureConfigured(t.Context(), []platform.RepoRef{ref}))
	_, err = service.Start(t.Context(), []platform.RepoRef{ref})
	require.NoError(err)
	require.NoError(database.CommitArchiveInventoryPage(t.Context(), db.ArchiveInventoryCommit{
		RepoID: repoID, ItemType: db.ArchiveItemTypeIssue, Exhausted: true, Now: now,
	}))
	parent := platform.DBMergeRequest(repoID, mr)
	require.NoError(database.CommitArchiveInventoryPage(t.Context(), db.ArchiveInventoryCommit{
		RepoID: repoID, ItemType: db.ArchiveItemTypeMergeRequest, Exhausted: true, Now: now,
		MergeRequests: []db.ArchiveInventoryMergeRequest{{
			ProviderItemID:       parent.PlatformExternalID,
			Snapshot:             db.MergeRequestSnapshot{MergeRequest: *parent, Labels: parent.Labels},
			CommentsStatus:       db.ArchiveDatasetStatusPending,
			ReviewsStatus:        db.ArchiveDatasetStatusPending,
			InlineCommentsStatus: db.ArchiveDatasetStatusPending,
		}},
	}))

	require.NoError(service.RunEligible(t.Context()))
	assert.Equal([]string{"get_mr", "mr_comments:"}, provider.calls)
	var stagedCursor string
	require.NoError(database.ReadDB().QueryRowContext(t.Context(), `
		SELECT next_cursor FROM middleman_archive_dataset_pages
		WHERE repo_id = ? AND item_type = 'merge_request' AND item_number = ?
		  AND dataset = 'comments'`, repoID, mr.Number,
	).Scan(&stagedCursor))
	assert.Equal("comments-page-2", stagedCursor)
	_, _, accepted, err := database.UpsertMergeRequestSnapshotWithLabels(t.Context(), parent)
	require.NoError(err)
	require.True(accepted)
	_, err = database.WriteDB().ExecContext(t.Context(), `
		UPDATE middleman_archive_repos SET next_retry_at = NULL WHERE repo_id = ?`, repoID)
	require.NoError(err)
	provider.calls = nil
	admission.calls = 0
	admission.denyAfter = 2

	require.NoError(service.RunEligible(t.Context()))
	assert.Equal([]string{"get_mr", "mr_comments:comments-page-2"}, provider.calls)
	_, err = database.WriteDB().ExecContext(t.Context(), `
		UPDATE middleman_archive_repos SET next_retry_at = NULL WHERE repo_id = ?`, repoID)
	require.NoError(err)
	item, err := database.ClaimArchiveItem(t.Context(), db.ClaimArchiveItemOpts{RepoIDs: []int64{repoID}, Now: now})
	require.NoError(err)
	require.NotNil(item)
	assert.Equal(db.ArchiveDatasetStatusComplete, item.CommentsStatus)
	assert.Equal(db.ArchiveDatasetStatusPending, item.ReviewsStatus)
	_, _, accepted, err = database.UpsertMergeRequestSnapshotWithLabels(t.Context(), parent)
	require.NoError(err)
	require.True(accepted)
	provider.calls = nil
	admission.calls = 0
	admission.denyAfter = 3

	require.NoError(service.RunEligible(t.Context()))
	assert.Equal([]string{"get_mr", "reviews:", "threads"}, provider.calls)
	var commentsStatus, reviewsStatus, inlineStatus string
	require.NoError(database.ReadDB().QueryRowContext(t.Context(), `
		SELECT comments_status, reviews_status, inline_comments_status
		FROM middleman_archive_items
		WHERE repo_id = ? AND item_type = 'merge_request' AND item_number = ?`, repoID, mr.Number,
	).Scan(&commentsStatus, &reviewsStatus, &inlineStatus))
	assert.Equal("complete", commentsStatus)
	assert.Equal("complete", reviewsStatus)
	assert.Equal("complete", inlineStatus)
}

func TestArchiveHydrationFailurePreservesPriorDatasetAndRecordsRetry(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := dbtest.Open(t)
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	ref := archiveServiceRef(platform.KindGitHub, "github.test", "repo")
	repoID := archiveServiceSeedRepo(t, database, ref)
	provider := newArchiveServiceProvider(ref.Platform, ref.Host)
	provider.failSecondCommentPage = true
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)
	service := newArchiveTestService(t, database, registry, []platform.RepoRef{ref}, nil, now)
	require.NoError(service.EnsureConfigured(t.Context(), []platform.RepoRef{ref}))
	_, err = service.Start(t.Context(), []platform.RepoRef{ref})
	require.NoError(err)
	require.NoError(service.RunEligible(t.Context()))
	require.NoError(service.RunEligible(t.Context()))

	err = service.RunEligible(t.Context())
	require.Error(err)
	var status string
	var attempts, eventCount int
	require.NoError(database.ReadDB().QueryRowContext(t.Context(), `
		SELECT comments_status, attempt_count FROM middleman_archive_items
		WHERE repo_id = ? AND item_type = 'issue' AND item_number = 1`, repoID,
	).Scan(&status, &attempts))
	require.NoError(database.ReadDB().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM middleman_issue_events`).Scan(&eventCount))
	assert.Equal("failed", status)
	assert.Equal(1, attempts)
	assert.Zero(eventCount)
	var stagedCursor string
	require.NoError(database.ReadDB().QueryRowContext(t.Context(), `
		SELECT next_cursor FROM middleman_archive_dataset_pages
		WHERE repo_id = ? AND item_type = 'issue' AND item_number = 1
			AND dataset = 'comments'`, repoID).Scan(&stagedCursor))
	assert.Equal("c2", stagedCursor)

	provider.failSecondCommentPage = false
	advanced := archiveTestIssue(ref)
	advanced.UpdatedAt = advanced.UpdatedAt.Add(time.Hour)
	advanced.LastActivityAt = advanced.UpdatedAt
	provider.issueLookupItem = &advanced
	provider.issueCommentPages = map[string]platform.Page[platform.IssueEvent]{
		"": {Items: []platform.IssueEvent{archiveIssueComment(ref, 12, "new snapshot")}, Exhausted: true},
	}
	provider.calls = nil
	_, err = database.WriteDB().ExecContext(t.Context(), `
		UPDATE middleman_archive_items SET next_retry_at = NULL WHERE repo_id = ?`, repoID)
	require.NoError(err)
	require.NoError(service.RunEligible(t.Context()))
	assert.Equal([]string{"get_issue", "issue_comments:"}, provider.calls)
	require.NoError(database.ReadDB().QueryRowContext(t.Context(), `
		SELECT COUNT(*), MAX(body) FROM middleman_issue_events
		WHERE event_type = 'issue_comment'`,
	).Scan(&eventCount, &status))
	assert.Equal(1, eventCount)
	assert.Equal("new snapshot", status)
}

func TestArchiveHydrationPreemptedByLiveWorkRecordsNoFailureAndStaysClaimable(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := dbtest.Open(t)
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	ref := archiveServiceRef(platform.KindGitHub, "github.test", "repo")
	repoID := archiveServiceSeedRepo(t, database, ref)
	provider := newArchiveServiceProvider(ref.Platform, ref.Host)
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)
	service := newArchiveTestService(t, database, registry, []platform.RepoRef{ref}, nil, now)
	require.NoError(service.EnsureConfigured(t.Context(), []platform.RepoRef{ref}))
	_, err = service.Start(t.Context(), []platform.RepoRef{ref})
	require.NoError(err)

	require.NoError(service.RunEligible(t.Context())) // issue inventory
	require.NoError(service.RunEligible(t.Context())) // merge-request inventory

	// Live work preempts the admitted request context during the issue
	// hydration lookup: the read fails, but this is not a hydration
	// failure. Nothing about the item or the read was actually wrong;
	// live work just needed the provider-host lease first.
	service.admission = archivePreemptingAdmission{}
	require.NoError(service.RunEligible(t.Context())) // issue hydration, preempted

	var status string
	var attempts int
	require.NoError(database.ReadDB().QueryRowContext(t.Context(), `
		SELECT comments_status, attempt_count FROM middleman_archive_items
		WHERE repo_id = ? AND item_type = 'issue' AND item_number = 1`, repoID,
	).Scan(&status, &attempts))
	assert.Equal("pending", status)
	assert.Zero(attempts)

	var lastErrorCode *string
	require.NoError(database.ReadDB().QueryRowContext(t.Context(), `
		SELECT last_error_code FROM middleman_archive_repos WHERE repo_id = ?`, repoID,
	).Scan(&lastErrorCode))
	assert.Nil(lastErrorCode)

	// No lease or retry timer was written, so the item is immediately
	// claimable again on the very next claim.
	item, err := database.ClaimArchiveItem(t.Context(), db.ClaimArchiveItemOpts{RepoIDs: []int64{repoID}, Now: now})
	require.NoError(err)
	require.NotNil(item)
	assert.Equal(db.ArchiveItemTypeIssue, item.ItemType)
}

func TestArchiveDatasetLimitBlocksWithoutRetry(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := dbtest.Open(t)
	now := archiveTestTime()
	ref := archiveServiceRef(platform.KindGitHub, "github.test", "bounded")
	repoID := archiveServiceSeedRepo(t, database, ref)
	provider := newArchiveServiceProvider(ref.Platform, ref.Host)
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)
	service := newArchiveTestService(t, database, registry, []platform.RepoRef{ref}, nil, now)
	require.NoError(service.EnsureConfigured(t.Context(), []platform.RepoRef{ref}))
	_, err = service.Start(t.Context(), []platform.RepoRef{ref})
	require.NoError(err)
	insert := db.ArchiveInventoryCommit{RepoID: repoID, ItemType: db.ArchiveItemTypeIssue, Exhausted: true, Now: now,
		Issues: []db.ArchiveInventoryIssue{{ProviderItemID: "issue-1", CommentsStatus: db.ArchiveDatasetStatusPending,
			Snapshot: db.IssueSnapshot{Issue: *platform.DBIssue(repoID, archiveTestIssue(ref))}}}}
	require.NoError(database.CommitArchiveInventoryPage(t.Context(), insert))
	item, err := database.ClaimArchiveItem(t.Context(), db.ClaimArchiveItemOpts{RepoIDs: []int64{repoID}, Now: now})
	require.NoError(err)
	require.NotNil(item)

	err = service.recordHydrationFailure(t.Context(), *item, &db.ArchiveDatasetLimitError{
		Dataset: db.ArchiveDatasetComments, Limit: "record", Value: 50_001, Max: 50_000,
	})
	require.Error(err)
	states, err := database.ListArchiveRepoStates(t.Context(), []int64{repoID})
	require.NoError(err)
	require.NotNil(states[0].LastErrorCode)
	assert.Equal(string(db.ArchiveErrorCodeRepoBlocked), *states[0].LastErrorCode)
	assert.Nil(states[0].NextRetryAt)
}

func TestArchivePageLimitBlocksWithoutRetry(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := dbtest.Open(t)
	now := archiveTestTime()
	ref := archiveServiceRef(platform.KindGitHub, "github.test", "oversized")
	repoID := archiveServiceSeedRepo(t, database, ref)
	provider := newArchiveServiceProvider(ref.Platform, ref.Host)
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)
	service := newArchiveTestService(t, database, registry, []platform.RepoRef{ref}, nil, now)
	require.NoError(service.EnsureConfigured(t.Context(), []platform.RepoRef{ref}))
	_, err = service.Start(t.Context(), []platform.RepoRef{ref})
	require.NoError(err)
	insert := db.ArchiveInventoryCommit{RepoID: repoID, ItemType: db.ArchiveItemTypeIssue, Exhausted: true, Now: now,
		Issues: []db.ArchiveInventoryIssue{{ProviderItemID: "issue-1", CommentsStatus: db.ArchiveDatasetStatusPending,
			Snapshot: db.IssueSnapshot{Issue: *platform.DBIssue(repoID, archiveTestIssue(ref))}}}}
	require.NoError(database.CommitArchiveInventoryPage(t.Context(), insert))
	item, err := database.ClaimArchiveItem(t.Context(), db.ClaimArchiveItemOpts{RepoIDs: []int64{repoID}, Now: now})
	require.NoError(err)
	require.NotNil(item)

	err = service.recordHydrationFailure(t.Context(), *item, platform.ErrPageLimit)
	require.ErrorIs(err, platform.ErrPageLimit)
	states, err := database.ListArchiveRepoStates(t.Context(), []int64{repoID})
	require.NoError(err)
	require.NotNil(states[0].LastErrorCode)
	assert.Equal(string(db.ArchiveErrorCodeRepoBlocked), *states[0].LastErrorCode)
	assert.Nil(states[0].NextRetryAt)
	status, err := service.Status(t.Context(), []platform.RepoRef{ref})
	require.NoError(err)
	assert.Equal(db.ArchiveStatusBlocked, status[0].Progress.Status)
}

func TestArchiveRepositoryAccessFailureIsBlockedAndNonDestructive(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := dbtest.Open(t)
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	ref := archiveServiceRef(platform.KindGitHub, "github.test", "repo")
	repoID := archiveServiceSeedRepo(t, database, ref)
	provider := newArchiveServiceProvider(ref.Platform, ref.Host)
	provider.issueLookupErr = platform.PermissionDenied(ref.Platform, ref.Host, errors.New("repository hidden"))
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)
	service := newArchiveTestService(t, database, registry, []platform.RepoRef{ref}, nil, now)
	require.NoError(service.EnsureConfigured(t.Context(), []platform.RepoRef{ref}))
	_, err = service.Start(t.Context(), []platform.RepoRef{ref})
	require.NoError(err)
	require.NoError(service.RunEligible(t.Context()))
	require.NoError(service.RunEligible(t.Context()))

	err = service.RunEligible(t.Context())
	require.ErrorIs(err, platform.ErrPermissionDenied)
	status, err := service.Status(t.Context(), []platform.RepoRef{ref})
	require.NoError(err)
	assert.Equal(db.ArchiveStatusBlocked, status[0].Progress.Status)
	issue, err := database.GetIssueByRepoIDAndNumber(t.Context(), repoID, 1)
	require.NoError(err)
	require.NotNil(issue)
	assert.Equal("closed", string(issue.State))
}

func TestArchiveHydrationCommitsTerminalLookupOutcomesWithoutFetchingDatasets(t *testing.T) {
	tests := []struct {
		name          string
		outcome       platform.LookupOutcome
		wantLifecycle string
	}{
		{name: "removed", outcome: platform.LookupRemoved, wantLifecycle: "removed_upstream"},
		{name: "moved", outcome: platform.LookupMoved, wantLifecycle: "removed_upstream"},
		{name: "inaccessible", outcome: platform.LookupInaccessible, wantLifecycle: "inaccessible"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			database := dbtest.Open(t)
			now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
			ref := archiveServiceRef(platform.KindGitHub, "github.test", "repo")
			repoID := archiveServiceSeedRepo(t, database, ref)
			provider := newArchiveServiceProvider(ref.Platform, ref.Host)
			provider.issueLookupOutcome = tt.outcome
			var destinationRepoID int64
			if tt.outcome == platform.LookupMoved {
				destination := archiveServiceRef(platform.KindGitHub, "github.test", "destination")
				provider.issueDestination = &destination
				destinationRepoID = archiveServiceSeedRepo(t, database, destination)
				require.NoError(database.EnsureDiscoveryArchives(t.Context(), []int64{destinationRepoID}, now))
				require.NoError(database.StartFullArchives(t.Context(), []int64{destinationRepoID}, now))
				_, err := database.WriteDB().ExecContext(t.Context(), `
					UPDATE middleman_archive_repos
					SET issue_inventory_complete = 1, merge_request_inventory_complete = 1,
						initial_completed_at = ?, maintenance_watermark = ?, maintenance_succeeded_at = ?
					WHERE repo_id = ?`, now.Add(-2*time.Hour), now.Add(-time.Hour), now.Add(-time.Minute), destinationRepoID)
				require.NoError(err)
			}
			registry, err := platform.NewRegistry(provider)
			require.NoError(err)
			service := newArchiveTestService(t, database, registry, []platform.RepoRef{ref}, nil, now)
			require.NoError(service.EnsureConfigured(t.Context(), []platform.RepoRef{ref}))
			_, err = service.Start(t.Context(), []platform.RepoRef{ref})
			require.NoError(err)
			require.NoError(service.RunEligible(t.Context()))
			require.NoError(service.RunEligible(t.Context()))
			require.NoError(service.RunEligible(t.Context()))

			var lifecycle string
			require.NoError(database.ReadDB().QueryRowContext(t.Context(), `
				SELECT lifecycle_state FROM middleman_archive_items
				WHERE repo_id = ? AND item_type = 'issue' AND item_number = 1`, repoID,
			).Scan(&lifecycle))
			assert.Equal(tt.wantLifecycle, lifecycle)
			assert.NotContains(provider.calls, "issue_comments:")
			if tt.outcome == platform.LookupMoved {
				states, err := database.ListArchiveRepoStates(t.Context(), []int64{destinationRepoID})
				require.NoError(err)
				require.Len(states, 1)
				assert.Equal(db.ArchiveOperatorStatePaused, states[0].OperatorState)
				require.NotNil(states[0].LastErrorCode)
				assert.Equal(string(db.ArchiveErrorCodeConfigurationRemoved), *states[0].LastErrorCode)
			}
		})
	}
}

func TestArchiveTerminalOutcomesCanCompleteInitialArchive(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := dbtest.Open(t)
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	ref := archiveServiceRef(platform.KindGitHub, "github.test", "repo")
	archiveServiceSeedRepo(t, database, ref)
	provider := newArchiveServiceProvider(ref.Platform, ref.Host)
	provider.issueLookupOutcome = platform.LookupRemoved
	provider.mrLookupOutcome = platform.LookupRemoved
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)
	service := newArchiveTestService(t, database, registry, []platform.RepoRef{ref}, nil, now)
	require.NoError(service.EnsureConfigured(t.Context(), []platform.RepoRef{ref}))
	_, err = service.Start(t.Context(), []platform.RepoRef{ref})
	require.NoError(err)
	for range 4 {
		require.NoError(service.RunEligible(t.Context()))
	}

	status, err := service.Status(t.Context(), []platform.RepoRef{ref})
	require.NoError(err)
	assert.Equal(db.ArchiveStatusCurrent, status[0].Progress.Status)
}

func TestArchiveResumesDiscoveryAppliesMaintenanceAndReportsDeterministically(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := dbtest.Open(t)
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	ref := archiveServiceRef(platform.KindGitHub, "github.test", "repo")
	repoID := archiveServiceSeedRepo(t, database, ref)
	provider := newArchiveServiceProvider(ref.Platform, ref.Host)
	provider.historicalIssuePages = map[string]platform.Page[platform.Issue]{
		"":             {Items: []platform.Issue{archiveTestIssue(ref)}, NextCursor: "issue-page-2"},
		"issue-page-2": {Exhausted: true},
	}
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)
	service := newArchiveTestService(t, database, registry, []platform.RepoRef{ref}, nil, now)
	require.NoError(service.EnsureConfigured(t.Context(), []platform.RepoRef{ref}))
	_, err = service.Start(t.Context(), []platform.RepoRef{ref})
	require.NoError(err)
	require.NoError(service.RunEligible(t.Context()))
	states, err := database.ListArchiveRepoStates(t.Context(), []int64{repoID})
	require.NoError(err)
	require.NotNil(states[0].IssueCursor)
	assert.Equal("issue-page-2", *states[0].IssueCursor)

	service = newArchiveTestService(t, database, registry, []platform.RepoRef{ref}, nil, now)
	for range 10 {
		status, statusErr := service.Status(t.Context(), []platform.RepoRef{ref})
		require.NoError(statusErr)
		if status[0].Progress.Status == db.ArchiveStatusCurrent {
			break
		}
		require.NoError(service.RunEligible(t.Context()))
	}
	status, err := service.Status(t.Context(), []platform.RepoRef{ref})
	require.NoError(err)
	assert.Equal(db.ArchiveStatusCurrent, status[0].Progress.Status)
	assert.Equal([]string{"", "issue-page-2"}, provider.issueInventoryCursors)

	edited := archiveTestIssue(ref)
	edited.UpdatedAt = edited.UpdatedAt.Add(time.Hour)
	edited.LastActivityAt = edited.UpdatedAt
	provider.updatedIssuePages = map[string]platform.Page[platform.Issue]{
		"": {Items: []platform.Issue{edited}, Exhausted: true},
	}
	provider.issueCommentPages = map[string]platform.Page[platform.IssueEvent]{
		"": {Items: []platform.IssueEvent{archiveIssueComment(ref, 10, "edited during maintenance")}, Exhausted: true},
	}
	provider.issueLookupItem = &edited
	service.clock = fixedClock{value: now.Add(5 * time.Minute)}
	initialStartedAt := now.Add(-2 * time.Hour)
	_, err = database.WriteDB().ExecContext(t.Context(), `
		UPDATE middleman_archive_repos
		SET initial_started_at = ?, initial_completed_at = ?,
			maintenance_watermark = NULL, maintenance_succeeded_at = NULL
		WHERE repo_id = ?`, initialStartedAt, now.Add(-time.Hour), repoID)
	require.NoError(err)
	require.NoError(service.RunEligible(t.Context())) // prompt maintenance queues the edit
	require.NotEmpty(provider.updatedIssueSince)
	assert.Equal(initialStartedAt, provider.updatedIssueSince[0])
	require.NoError(service.RunEligible(t.Context())) // hydrate the edited issue

	var body string
	require.NoError(database.ReadDB().QueryRowContext(t.Context(), `
		SELECT body FROM middleman_issue_events WHERE event_type = 'issue_comment'
		ORDER BY id DESC LIMIT 1`).Scan(&body))
	assert.Equal("edited during maintenance", body)
	beforeCalls := len(provider.calls)
	first, err := service.Report(t.Context(), ReportOptions{
		Start: archiveTestTime().Add(-time.Hour), End: archiveTestTime().Add(2 * time.Hour), Detailed: true,
	})
	require.NoError(err)
	second, err := service.Report(t.Context(), ReportOptions{
		Start: archiveTestTime().Add(-time.Hour), End: archiveTestTime().Add(2 * time.Hour), Detailed: true,
	})
	require.NoError(err)
	assert.Equal(first, second)
	assert.Len(provider.calls, beforeCalls, "reporting must remain provider-offline")
	firstMarkdown, err := report.RenderMarkdown(first)
	require.NoError(err)
	secondMarkdown, err := report.RenderMarkdown(second)
	require.NoError(err)
	assert.Equal(firstMarkdown, secondMarkdown)
	firstJSON, err := json.MarshalIndent(first, "", "  ")
	require.NoError(err)
	secondJSON, err := json.MarshalIndent(second, "", "  ")
	require.NoError(err)
	assert.JSONEq(string(firstJSON), string(secondJSON))
}

func TestArchiveInventoryFailureIsDurableAndClearedBySuccessfulProgress(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := dbtest.Open(t)
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	ref := archiveServiceRef(platform.KindGitHub, "github.test", "repo")
	repoID := archiveServiceSeedRepo(t, database, ref)
	provider := newArchiveServiceProvider(ref.Platform, ref.Host)
	provider.issueInventoryErr = errors.New("inventory unavailable")
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)
	service := newArchiveTestService(t, database, registry, []platform.RepoRef{ref}, nil, now)
	service.retries = archiveTestRetryClassifier{}
	require.NoError(service.EnsureConfigured(t.Context(), []platform.RepoRef{ref}))
	_, err = service.Start(t.Context(), []platform.RepoRef{ref})
	require.NoError(err)

	err = service.RunEligible(t.Context())
	require.Error(err)
	states, err := database.ListArchiveRepoStates(t.Context(), []int64{repoID})
	require.NoError(err)
	require.NotNil(states[0].LastErrorCode)
	assert.Equal(string(db.ArchiveErrorCodeTransient), *states[0].LastErrorCode)
	require.NotNil(states[0].LastErrorDetail)
	assert.Contains(*states[0].LastErrorDetail, "inventory unavailable")

	provider.issueInventoryErr = nil
	require.NoError(service.RunEligible(t.Context()))
	states, err = database.ListArchiveRepoStates(t.Context(), []int64{repoID})
	require.NoError(err)
	assert.Nil(states[0].LastErrorCode)
	assert.Nil(states[0].NextRetryAt)
}

func TestArchiveStartRejectsMissingRequiredInventoryCapability(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := dbtest.Open(t)
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	ref := archiveServiceRef(platform.KindGitHub, "github.test", "repo")
	repoID := archiveServiceSeedRepo(t, database, ref)
	provider := newArchiveServiceProvider(ref.Platform, ref.Host)
	provider.caps.Archive.HistoricalMergeRequests = false
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)
	service := newArchiveTestService(t, database, registry, []platform.RepoRef{ref}, nil, now)
	require.NoError(service.EnsureConfigured(t.Context(), []platform.RepoRef{ref}))

	_, err = service.Start(t.Context(), []platform.RepoRef{ref})
	require.ErrorIs(err, platform.ErrUnsupportedCapability)
	states, err := database.ListArchiveRepoStates(t.Context(), []int64{repoID})
	require.NoError(err)
	assert.Equal(db.ArchiveCollectionModeDiscovery, states[0].CollectionMode)
}

func TestArchiveDiscoverySkipsUnsupportedInventoryStream(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := dbtest.Open(t)
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	ref := archiveServiceRef(platform.KindGitHub, "github.test", "repo")
	repoID := archiveServiceSeedRepo(t, database, ref)
	provider := newArchiveServiceProvider(ref.Platform, ref.Host)
	provider.caps.Archive.HistoricalMergeRequests = false
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)
	service := newArchiveTestService(t, database, registry, []platform.RepoRef{ref}, nil, now)
	require.NoError(service.EnsureConfigured(t.Context(), []platform.RepoRef{ref}))
	require.NoError(service.RunEligible(t.Context()))
	require.NoError(service.RunEligible(t.Context()))

	states, err := database.ListArchiveRepoStates(t.Context(), []int64{repoID})
	require.NoError(err)
	assert.True(states[0].IssueInventoryComplete)
	assert.True(states[0].MergeRequestInventoryComplete)
	assert.Equal([]string{"issues"}, provider.calls)
}

func TestDefaultArchiveRetryClassifierDistinguishesTerminalProviderErrors(t *testing.T) {
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		err       error
		wantCode  db.ArchiveErrorCode
		wantRetry bool
	}{
		{name: "authentication", err: platform.ErrPermissionDenied, wantCode: db.ArchiveErrorCodeAuthentication},
		{name: "contract", err: platform.ErrProviderContract, wantCode: db.ArchiveErrorCodeRepoBlocked},
		{name: "page limit", err: platform.ErrPageLimit, wantCode: db.ArchiveErrorCodeRepoBlocked},
		{name: "transient", err: errors.New("temporary"), wantCode: db.ArchiveErrorCodeTransient, wantRetry: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			decision := (defaultRetryClassifier{}).Classify(tt.err, 0, now)
			assert.Equal(t, tt.wantCode, decision.Code)
			assert.Equal(t, tt.wantRetry, decision.RetryAt != nil)
		})
	}
}

func TestArchiveBudgetDeferralDoesNotIncrementAttempts(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := dbtest.Open(t)
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	ref := archiveServiceRef(platform.KindGitHub, "github.test", "repo")
	archiveServiceSeedRepo(t, database, ref)
	provider := newArchiveServiceProvider(ref.Platform, ref.Host)
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)
	retryAt := now.Add(time.Hour)
	admission := &archiveTestAdmission{deny: true, retryAt: retryAt}
	service := newArchiveTestService(t, database, registry, []platform.RepoRef{ref}, admission, now)
	require.NoError(service.EnsureConfigured(t.Context(), []platform.RepoRef{ref}))
	_, err = service.Start(t.Context(), []platform.RepoRef{ref})
	require.NoError(err)

	require.NoError(service.RunEligible(t.Context()))
	status, err := service.Status(t.Context(), []platform.RepoRef{ref})
	require.NoError(err)
	assert.Equal(db.ArchiveStatusWaitingForBudget, status[0].Progress.Status)
	require.NotNil(status[0].Progress.BudgetWaitUntil)
	assert.Equal(retryAt, *status[0].Progress.BudgetWaitUntil)
	assert.Empty(provider.calls)
}

func TestArchivePausePreventsFutureProviderReads(t *testing.T) {
	require := require.New(t)
	database := dbtest.Open(t)
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	ref := archiveServiceRef(platform.KindGitHub, "github.test", "repo")
	archiveServiceSeedRepo(t, database, ref)
	provider := newArchiveServiceProvider(ref.Platform, ref.Host)
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)
	service := newArchiveTestService(t, database, registry, []platform.RepoRef{ref}, nil, now)
	require.NoError(service.EnsureConfigured(t.Context(), []platform.RepoRef{ref}))
	_, err = service.Start(t.Context(), []platform.RepoRef{ref})
	require.NoError(err)
	_, err = service.Pause(t.Context(), []platform.RepoRef{ref})
	require.NoError(err)
	require.NoError(service.RunEligible(t.Context()))
	assert.Empty(t, provider.calls)
}

type archiveTestSource struct{ refs []platform.RepoRef }

func (s archiveTestSource) ConfiguredRepositories(context.Context) ([]platform.RepoRef, error) {
	return s.refs, nil
}

type archiveMutableSource struct{ refs []platform.RepoRef }

func (s *archiveMutableSource) ConfiguredRepositories(context.Context) ([]platform.RepoRef, error) {
	return append([]platform.RepoRef(nil), s.refs...), nil
}

type archiveTestAdmission struct {
	mu        sync.Mutex
	calls     int
	deny      bool
	denyAfter int
	retryAt   time.Time
	costs     []int
}

type archiveTestRetryClassifier struct{}

func (archiveTestRetryClassifier) Classify(error, int, time.Time) RetryDecision {
	return RetryDecision{Code: db.ArchiveErrorCodeTransient}
}

func (a *archiveTestAdmission) Admit(ctx context.Context, _ platform.RepoRef, cost int) (AdmissionResult, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls++
	a.costs = append(a.costs, cost)
	if a.deny || a.denyAfter > 0 && a.calls > a.denyAfter {
		return AdmissionResult{RetryAt: &a.retryAt, Detail: "test budget exhausted"}, nil
	}
	return AdmissionResult{Allowed: true, Context: ctx}, nil
}

// archivePreemptingAdmission simulates live work preempting an admitted
// archive request: it always returns a request context that is already
// canceled, distinct from the caller's outer context, mirroring what
// Syncer.Admit does when beginProviderWork cancels an in-flight archive
// lease.
type archivePreemptingAdmission struct{}

func (archivePreemptingAdmission) Admit(ctx context.Context, _ platform.RepoRef, _ int) (AdmissionResult, error) {
	requestCtx, cancel := context.WithCancel(ctx)
	cancel()
	return AdmissionResult{Allowed: true, Context: requestCtx, Release: func() {}}, nil
}

type archiveServiceProvider struct {
	kind                  platform.Kind
	host                  string
	caps                  platform.Capabilities
	mu                    sync.Mutex
	calls                 []string
	failSecondCommentPage bool
	issueInventoryErr     error
	historicalIssuePages  map[string]platform.Page[platform.Issue]
	issueInventoryCursors []string
	issueInventoryStarted chan struct{}
	issueInventoryRelease chan struct{}
	issueLookupOutcome    platform.LookupOutcome
	issueLookupErr        error
	issueLookupItem       *platform.Issue
	issueDestination      *platform.RepoRef
	mrLookupOutcome       platform.LookupOutcome
	mrLookupItem          *platform.MergeRequest
	updatedIssuePages     map[string]platform.Page[platform.Issue]
	updatedIssueErrors    map[string]error
	updatedMRPages        map[string]platform.Page[platform.MergeRequest]
	updatedMRErrors       map[string]error
	updatedIssueSince     []time.Time
	updatedMRSince        []time.Time
	issueCommentPages     map[string]platform.Page[platform.IssueEvent]
	mrCommentPages        map[string]platform.Page[platform.MergeRequestEvent]
	reviewPages           map[string]platform.Page[platform.MergeRequestEvent]
	mrComments            []platform.MergeRequestEvent
	reviews               []platform.MergeRequestEvent
	threads               []platform.MergeRequestReviewThread
}

func newArchiveServiceProvider(kind platform.Kind, host string) *archiveServiceProvider {
	return &archiveServiceProvider{
		kind: kind, host: host,
		caps: platform.Capabilities{Archive: platform.ArchiveCapabilities{
			HistoricalIssues: true, HistoricalMergeRequests: true,
			OrdinaryComments: true, SubmittedReviews: true, InlineReviewComments: true,
		}},
	}
}

func (p *archiveServiceProvider) Platform() platform.Kind             { return p.kind }
func (p *archiveServiceProvider) Host() string                        { return p.host }
func (p *archiveServiceProvider) Capabilities() platform.Capabilities { return p.caps }
func (p *archiveServiceProvider) record(call string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, call)
}

func (p *archiveServiceProvider) ListHistoricalIssues(ctx context.Context, ref platform.RepoRef, cursor string) (platform.Page[platform.Issue], error) {
	p.record("issues")
	p.issueInventoryCursors = append(p.issueInventoryCursors, cursor)
	if p.issueInventoryStarted != nil {
		select {
		case <-p.issueInventoryStarted:
		default:
			close(p.issueInventoryStarted)
		}
		select {
		case <-p.issueInventoryRelease:
		case <-ctx.Done():
			return platform.Page[platform.Issue]{}, ctx.Err()
		}
	}
	if p.issueInventoryErr != nil {
		return platform.Page[platform.Issue]{}, p.issueInventoryErr
	}
	if page, ok := p.historicalIssuePages[cursor]; ok {
		return page, nil
	}
	return platform.Page[platform.Issue]{Items: []platform.Issue{archiveTestIssue(ref)}, Exhausted: true}, nil
}
func (p *archiveServiceProvider) ListHistoricalMergeRequests(_ context.Context, ref platform.RepoRef, _ string) (platform.Page[platform.MergeRequest], error) {
	p.record("merge_requests")
	return platform.Page[platform.MergeRequest]{Items: []platform.MergeRequest{archiveTestMergeRequest(ref)}, Exhausted: true}, nil
}
func (p *archiveServiceProvider) ListUpdatedIssues(_ context.Context, _ platform.RepoRef, since time.Time, cursor string) (platform.Page[platform.Issue], error) {
	p.record("updated_issues:" + cursor)
	p.updatedIssueSince = append(p.updatedIssueSince, since)
	if err := p.updatedIssueErrors[cursor]; err != nil {
		return platform.Page[platform.Issue]{}, err
	}
	if page, ok := p.updatedIssuePages[cursor]; ok {
		return page, nil
	}
	return platform.Page[platform.Issue]{Exhausted: true}, nil
}
func (p *archiveServiceProvider) ListUpdatedMergeRequests(_ context.Context, _ platform.RepoRef, since time.Time, cursor string) (platform.Page[platform.MergeRequest], error) {
	p.record("updated_mrs:" + cursor)
	p.updatedMRSince = append(p.updatedMRSince, since)
	if err := p.updatedMRErrors[cursor]; err != nil {
		return platform.Page[platform.MergeRequest]{}, err
	}
	if page, ok := p.updatedMRPages[cursor]; ok {
		return page, nil
	}
	return platform.Page[platform.MergeRequest]{Exhausted: true}, nil
}
func (p *archiveServiceProvider) GetArchiveIssue(ctx context.Context, ref platform.RepoRef, _ int) (platform.ItemLookup[platform.Issue], error) {
	p.record("get_issue")
	if err := ctx.Err(); err != nil {
		return platform.ItemLookup[platform.Issue]{}, err
	}
	if p.issueLookupErr != nil {
		return platform.ItemLookup[platform.Issue]{}, p.issueLookupErr
	}
	if p.issueLookupOutcome != "" && p.issueLookupOutcome != platform.LookupPresent {
		return platform.ItemLookup[platform.Issue]{
			Outcome: p.issueLookupOutcome, Destination: p.issueDestination,
		}, nil
	}
	if p.issueLookupItem != nil {
		return platform.ItemLookup[platform.Issue]{Outcome: platform.LookupPresent, Item: *p.issueLookupItem}, nil
	}
	return platform.ItemLookup[platform.Issue]{Outcome: platform.LookupPresent, Item: archiveTestIssue(ref)}, nil
}
func (p *archiveServiceProvider) GetArchiveMergeRequest(_ context.Context, ref platform.RepoRef, _ int) (platform.ItemLookup[platform.MergeRequest], error) {
	p.record("get_mr")
	if p.mrLookupOutcome != "" && p.mrLookupOutcome != platform.LookupPresent {
		return platform.ItemLookup[platform.MergeRequest]{Outcome: p.mrLookupOutcome}, nil
	}
	if p.mrLookupItem != nil {
		return platform.ItemLookup[platform.MergeRequest]{Outcome: platform.LookupPresent, Item: *p.mrLookupItem}, nil
	}
	return platform.ItemLookup[platform.MergeRequest]{Outcome: platform.LookupPresent, Item: archiveTestMergeRequest(ref)}, nil
}
func (p *archiveServiceProvider) ListArchiveIssueComments(_ context.Context, ref platform.RepoRef, _ int, cursor string) (platform.Page[platform.IssueEvent], error) {
	p.record("issue_comments:" + cursor)
	if page, ok := p.issueCommentPages[cursor]; ok {
		return page, nil
	}
	if cursor == "c2" && p.failSecondCommentPage {
		return platform.Page[platform.IssueEvent]{}, errors.New("comment page failed")
	}
	event := platform.IssueEvent{Repo: ref, PlatformID: 10, PlatformExternalID: "comment-10", IssueNumber: 1, EventType: "issue_comment", Author: "alice", CreatedAt: archiveTestTime(), DedupeKey: "comment:" + cursor}
	if cursor == "" {
		return platform.Page[platform.IssueEvent]{Items: []platform.IssueEvent{event}, NextCursor: "c2"}, nil
	}
	event.PlatformID, event.PlatformExternalID, event.DedupeKey = 11, "comment-11", "comment:c2"
	return platform.Page[platform.IssueEvent]{Items: []platform.IssueEvent{event}, Exhausted: true}, nil
}
func (p *archiveServiceProvider) ListArchiveMergeRequestComments(_ context.Context, ref platform.RepoRef, _ int, cursor string) (platform.Page[platform.MergeRequestEvent], error) {
	p.record("mr_comments:" + cursor)
	if page, ok := p.mrCommentPages[cursor]; ok {
		return page, nil
	}
	if p.mrComments != nil {
		return platform.Page[platform.MergeRequestEvent]{Items: p.mrComments, Exhausted: true}, nil
	}
	return platform.Page[platform.MergeRequestEvent]{Items: []platform.MergeRequestEvent{{Repo: ref, PlatformID: 20, PlatformExternalID: "comment-20", MergeRequestNumber: 2, EventType: "issue_comment", CreatedAt: archiveTestTime(), DedupeKey: "mr-comment-20"}}, Exhausted: true}, nil
}
func (p *archiveServiceProvider) ListArchiveSubmittedReviews(_ context.Context, _ platform.RepoRef, _ int, cursor string) (platform.Page[platform.MergeRequestEvent], error) {
	p.record("reviews:" + cursor)
	if page, ok := p.reviewPages[cursor]; ok {
		return page, nil
	}
	return platform.Page[platform.MergeRequestEvent]{Items: p.reviews, Exhausted: true}, nil
}
func (p *archiveServiceProvider) ListArchiveReviewThreads(context.Context, platform.RepoRef, int, string) (platform.Page[platform.MergeRequestReviewThread], error) {
	p.record("threads")
	return platform.Page[platform.MergeRequestReviewThread]{Items: p.threads, Exhausted: true}, nil
}

func newArchiveTestService(t *testing.T, database *db.DB, registry *platform.Registry, refs []platform.RepoRef, admission Admission, now time.Time) *Service {
	t.Helper()
	service, err := NewService(database, registry, admission, archiveTestSource{refs: refs}, nil, fixedClock{value: now})
	require.NoError(t, err)
	return service
}

func archiveServiceRef(kind platform.Kind, host, name string) platform.RepoRef {
	return platform.RepoRef{Platform: kind, Host: host, Owner: "owner", Name: name, RepoPath: "owner/" + name}
}

func archiveServiceSeedRepo(t *testing.T, database *db.DB, ref platform.RepoRef) int64 {
	t.Helper()
	id, err := database.UpsertRepo(t.Context(), platform.DBRepoIdentity(ref))
	require.NoError(t, err)
	return id
}

func archiveTestTime() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) }
func archiveTestIssue(ref platform.RepoRef) platform.Issue {
	return platform.Issue{Repo: ref, PlatformID: 1, PlatformExternalID: "issue-1", Number: 1, State: "closed", CreatedAt: archiveTestTime(), UpdatedAt: archiveTestTime(), LastActivityAt: archiveTestTime()}
}
func archiveTestMergeRequest(ref platform.RepoRef) platform.MergeRequest {
	created := archiveTestTime().Add(time.Minute)
	return platform.MergeRequest{Repo: ref, PlatformID: 2, PlatformExternalID: "mr-2", Number: 2, State: "closed", CreatedAt: created, UpdatedAt: created, LastActivityAt: created}
}
