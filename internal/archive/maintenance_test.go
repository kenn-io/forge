package archive

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/middleman/internal/db"
	"go.kenn.io/middleman/internal/platform"
	"go.kenn.io/middleman/internal/testutil/dbtest"
)

func TestPromptMaintenanceCommitsPagesBeforeAdvancingScanWatermark(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := dbtest.Open(t)
	now := time.Date(2026, 7, 13, 12, 0, 0, 123, time.UTC)
	ref := archiveServiceRef(platform.KindGitHub, "github.test", "repo")
	repoID := archiveServiceSeedRepo(t, database, ref)
	provider := newArchiveServiceProvider(ref.Platform, ref.Host)
	service := archiveMaintenanceService(t, database, provider, ref, now)
	completeArchiveInitial(t, service)

	advanced := archiveTestIssue(ref)
	advanced.UpdatedAt = advanced.UpdatedAt.Add(time.Hour)
	advanced.LastActivityAt = advanced.UpdatedAt
	newIssue := advanced
	newIssue.PlatformID = 3
	newIssue.PlatformExternalID = "issue-3"
	newIssue.Number = 3
	newIssue.UpdatedAt = advanced.UpdatedAt.Add(time.Minute)
	newIssue.LastActivityAt = newIssue.UpdatedAt
	provider.updatedIssuePages = map[string]platform.Page[platform.Issue]{
		"":   {Items: []platform.Issue{advanced}, NextCursor: "u2"},
		"u2": {Items: []platform.Issue{newIssue}, Exhausted: true},
	}
	provider.updatedMRPages = map[string]platform.Page[platform.MergeRequest]{
		"": {Items: []platform.MergeRequest{archiveTestMergeRequest(ref)}, Exhausted: true},
	}

	require.NoError(service.RunEligible(t.Context()))
	states, err := database.ListArchiveRepoStates(t.Context(), []int64{repoID})
	require.NoError(err)
	require.Len(states, 1)
	require.NotNil(states[0].MaintenanceWatermark)
	assert.Equal(now, *states[0].MaintenanceWatermark)
	require.NotNil(states[0].MaintenanceSucceededAt)
	assert.Equal(now, *states[0].MaintenanceSucceededAt)
	assert.Equal([]time.Time{now, now}, provider.updatedIssueSince)
	assert.Equal([]time.Time{now}, provider.updatedMRSince)

	advanced2, err := database.GetDatasetProgress(
		t.Context(), repoID, db.ArchiveItemTypeIssue, 1, db.ArchiveDatasetComments,
	)
	require.NoError(err)
	assert.Equal(db.ArchiveDatasetProgressPending, advanced2.Status)
	for _, dataset := range []db.ArchiveDataset{
		db.ArchiveDatasetComments, db.ArchiveDatasetReviews, db.ArchiveDatasetInlineComments,
	} {
		boundary, err := database.GetDatasetProgress(
			t.Context(), repoID, db.ArchiveItemTypeMergeRequest, 2, dataset,
		)
		require.NoError(err)
		assert.Equal(db.ArchiveDatasetProgressPending, boundary.Status,
			"inclusive boundary rows must refresh for coarse provider timestamps: %s", dataset)
	}
}

func TestPromptMaintenanceFailureRetainsPriorWatermarkAndCommittedPages(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := dbtest.Open(t)
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	ref := archiveServiceRef(platform.KindGitHub, "github.test", "repo")
	repoID := archiveServiceSeedRepo(t, database, ref)
	provider := newArchiveServiceProvider(ref.Platform, ref.Host)
	service := archiveMaintenanceService(t, database, provider, ref, now)
	completeArchiveInitial(t, service)
	newIssue := archiveTestIssue(ref)
	newIssue.PlatformID, newIssue.PlatformExternalID, newIssue.Number = 3, "issue-3", 3
	provider.updatedIssuePages = map[string]platform.Page[platform.Issue]{
		"": {Items: []platform.Issue{newIssue}, NextCursor: "u2"},
	}
	provider.updatedIssueErrors = map[string]error{"u2": errors.New("updated issue page failed")}

	err := service.RunEligible(t.Context())
	require.Error(err)
	states, stateErr := database.ListArchiveRepoStates(t.Context(), []int64{repoID})
	require.NoError(stateErr)
	assert.Nil(states[0].MaintenanceWatermark)
	assert.Nil(states[0].MaintenanceSucceededAt)
	var count int
	require.NoError(database.ReadDB().QueryRowContext(t.Context(), `
		SELECT COUNT(*) FROM middleman_archive_items
		WHERE repo_id = ? AND item_type = 'issue' AND item_number = 3`, repoID).Scan(&count))
	assert.Equal(1, count, "the first page must remain durably committed")
}

func TestPromptMaintenanceResumesDurableCursorAfterBudgetDeferral(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := dbtest.Open(t)
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	ref := archiveServiceRef(platform.KindGitHub, "github.test", "repo")
	repoID := archiveServiceSeedRepo(t, database, ref)
	provider := newArchiveServiceProvider(ref.Platform, ref.Host)
	service := archiveMaintenanceService(t, database, provider, ref, now)
	completeArchiveInitial(t, service)
	provider.updatedIssuePages = map[string]platform.Page[platform.Issue]{
		"":   {Items: []platform.Issue{archiveTestIssue(ref)}, NextCursor: "u2"},
		"u2": {Exhausted: true},
	}
	retryAt := now.Add(time.Hour)
	service.admission = &archiveTestAdmission{denyAfter: 1, retryAt: retryAt}

	require.NoError(service.RunEligible(t.Context()))
	states, err := database.ListArchiveRepoStates(t.Context(), []int64{repoID})
	require.NoError(err)
	require.NotNil(states[0].MaintenanceIssues.NextCursor)
	assert.Equal("u2", *states[0].MaintenanceIssues.NextCursor)
	require.NotNil(states[0].PromptScanStartedAt)
	assert.Nil(states[0].MaintenanceSucceededAt)

	registry, err := platform.NewRegistry(provider)
	require.NoError(err)
	service = newArchiveTestService(t, database, registry, []platform.RepoRef{ref}, nil, retryAt.Add(time.Minute))
	require.NoError(service.RunEligible(t.Context()))
	assert.Contains(provider.calls, "updated_issues:u2")
	states, err = database.ListArchiveRepoStates(t.Context(), []int64{repoID})
	require.NoError(err)
	assert.Nil(states[0].PromptScanStartedAt)
	assert.Nil(states[0].MaintenanceIssues.NextCursor)
	require.NotNil(states[0].MaintenanceSucceededAt)
}

func archiveMaintenanceService(
	t *testing.T,
	database *db.DB,
	provider *archiveServiceProvider,
	ref platform.RepoRef,
	now time.Time,
) *Service {
	t.Helper()
	registry, err := platform.NewRegistry(provider)
	require.NoError(t, err)
	service := newArchiveTestService(t, database, registry, []platform.RepoRef{ref}, nil, now)
	require.NoError(t, service.EnsureConfigured(t.Context(), []platform.RepoRef{ref}))
	_, err = service.Start(t.Context(), []platform.RepoRef{ref})
	require.NoError(t, err)
	return service
}

func completeArchiveInitial(t *testing.T, service *Service) {
	t.Helper()
	for range 4 {
		require.NoError(t, service.RunEligible(t.Context()))
	}
}

func archiveIssueComment(ref platform.RepoRef, id int64, body string) platform.IssueEvent {
	return platform.IssueEvent{
		Repo: ref, PlatformID: id, PlatformExternalID: "issue-comment-" + body,
		IssueNumber: 1, EventType: "issue_comment", Body: body,
		CreatedAt: archiveTestTime(), DedupeKey: "issue-comment:" + body,
	}
}
