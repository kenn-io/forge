package db

import (
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func archiveTestTime() time.Time {
	return time.Date(2026, time.July, 13, 12, 0, 0, 0, time.UTC)
}

func insertArchiveItemForTest(
	t *testing.T,
	d *DB,
	repoID int64,
	itemType ArchiveItemType,
	number int,
	createdAt time.Time,
	comments, reviews, inline ArchiveDatasetStatus,
) {
	t.Helper()
	_, err := d.WriteDB().ExecContext(t.Context(), `
		INSERT INTO middleman_archive_items (
			repo_id, item_type, item_number, provider_item_id,
			provider_created_at, provider_updated_at, hydration_snapshot_updated_at,
			lifecycle_state,
			comments_status, reviews_status, inline_comments_status
		) VALUES (?, ?, ?, ?, ?, ?, ?, 'active', ?, ?, ?)`,
		repoID, itemType, number, string(itemType)+"-id-"+time.Unix(int64(number), 0).String(),
		createdAt, createdAt, createdAt, comments, reviews, inline,
	)
	require.NoError(t, err)
}

func insertArchiveProgressForTest(
	t *testing.T,
	d *DB,
	repoID int64,
	itemType ArchiveItemType,
	number int,
	dataset ArchiveDataset,
	status ArchiveDatasetProgressStatus,
) {
	t.Helper()
	_, err := d.WriteDB().ExecContext(t.Context(), `
		INSERT INTO middleman_archive_dataset_progress (
			repo_id, item_type, item_number, dataset, status, updated_at
		) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(repo_id, item_type, item_number, dataset) DO UPDATE SET
			status = excluded.status, updated_at = excluded.updated_at`,
		repoID, itemType, number, dataset, status,
		formatDatasetProgressTime(archiveTestTime()))
	require.NoError(t, err)
}

func archiveProgressStatusForTest(
	t *testing.T,
	d *DB,
	repoID int64,
	itemType ArchiveItemType,
	number int,
	dataset ArchiveDataset,
) ArchiveDatasetProgressStatus {
	t.Helper()
	progress, err := d.GetDatasetProgress(t.Context(), repoID, itemType, number, dataset)
	require.NoError(t, err)
	return progress.Status
}

func TestArchiveDiscoveryLifecyclePreservesExistingState(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	now := archiveTestTime()
	firstRepoID := insertTestRepoWithHost(t, d, "acme", "widget", "github.com")
	secondRepoID := insertTestRepoWithHost(t, d, "acme", "widget", "github.example.com")

	require.NoError(d.EnsureDiscoveryArchives(ctx, []int64{secondRepoID, firstRepoID}, now))
	states, err := d.ListArchiveRepoStates(ctx, nil)
	require.NoError(err)
	require.Len(states, 2)
	assert.Equal(firstRepoID, states[0].RepoID)
	assert.Equal(secondRepoID, states[1].RepoID)
	assert.Equal(ArchiveCollectionModeDiscovery, states[0].CollectionMode)
	assert.Equal(ArchiveOperatorStateActive, states[0].OperatorState)
	assert.False(states[0].IssueInventory.Complete())
	assert.False(states[0].MergeRequestInventory.Complete())
	assert.Equal(ArchiveCoverageUnknown, states[0].CommentsCoverage)
	assert.Equal(now, states[0].CreatedAt)
	assert.Equal(now, states[0].UpdatedAt)

	_, err = d.WriteDB().ExecContext(ctx, `
		UPDATE middleman_archive_repos
		SET collection_mode = 'full', operator_state = 'paused',
			maintenance_watermark = ?, updated_at = ?
		WHERE repo_id = ?`, now.Add(time.Hour), now.Add(2*time.Hour), firstRepoID)
	require.NoError(err)
	_, err = d.WriteDB().ExecContext(ctx, `
		UPDATE middleman_archive_repo_scans
		SET next_cursor = 'issue-cursor', status = 'running', page_count = 1
		WHERE repo_id = ? AND scan = 'issue_inventory'`, firstRepoID)
	require.NoError(err)
	require.NoError(d.EnsureDiscoveryArchives(ctx, []int64{firstRepoID}, now.Add(3*time.Hour)))
	preserved, err := d.ListArchiveRepoStates(ctx, []int64{firstRepoID})
	require.NoError(err)
	require.Len(preserved, 1)
	assert.Equal(ArchiveCollectionModeFull, preserved[0].CollectionMode)
	assert.Equal(ArchiveOperatorStatePaused, preserved[0].OperatorState)
	assert.Equal("issue-cursor", *preserved[0].IssueInventory.NextCursor)
	assert.Equal(now.Add(time.Hour), *preserved[0].MaintenanceWatermark)
	assert.Equal(now.Add(2*time.Hour), preserved[0].UpdatedAt)
}

func TestStartFullArchiveCompletesEmptyFinishedInventory(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	now := archiveTestTime()
	repoID := insertTestRepo(t, d, "acme", "empty")
	require.NoError(d.EnsureDiscoveryArchives(t.Context(), []int64{repoID}, now))
	_, err := d.WriteDB().ExecContext(t.Context(), `
		UPDATE middleman_archive_repo_scans
		SET status = 'complete'
		WHERE repo_id = ? AND scan IN ('issue_inventory', 'merge_request_inventory')`, repoID)
	require.NoError(err)

	require.NoError(d.StartFullArchives(t.Context(), []int64{repoID}, now.Add(time.Minute)))
	states, err := d.ListArchiveRepoStates(t.Context(), []int64{repoID})
	require.NoError(err)
	require.NotNil(states[0].InitialCompletedAt)
	assert.Equal(now.Add(time.Minute), *states[0].InitialCompletedAt)
}

func TestArchiveInventoryStaleIssueSnapshotPreservesNewerLabels(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	now := archiveTestTime()
	repoID := insertTestRepo(t, d, "acme", "labels")
	require.NoError(d.EnsureDiscoveryArchives(ctx, []int64{repoID}, now))
	require.NoError(d.StartFullArchives(ctx, []int64{repoID}, now))

	commit := func(updatedAt time.Time, label string) error {
		return d.CommitArchiveInventoryPage(ctx, ArchiveInventoryCommit{
			RepoID: repoID, ItemType: ArchiveItemTypeIssue, Exhausted: true, Now: now,
			Issues: []ArchiveInventoryIssue{{
				ProviderItemID: "issue-1", CommentsStatus: ArchiveDatasetStatusPending,
				Snapshot: IssueSnapshot{
					Issue: Issue{RepoID: repoID, PlatformID: 1, PlatformExternalID: "issue-1", Number: 1,
						Title: label, State: "closed", CreatedAt: now.Add(-time.Hour), UpdatedAt: updatedAt,
						LastActivityAt: updatedAt},
					Labels: []Label{{RepoID: repoID, PlatformID: int64(len(label)), Name: label, Color: "ffffff", UpdatedAt: updatedAt}},
				},
			}},
		})
	}
	require.NoError(commit(now, "new"))
	require.NoError(commit(now.Add(-time.Hour), "old"))

	var label string
	require.NoError(d.ReadDB().QueryRowContext(ctx, `
		SELECT l.name FROM middleman_issue_labels il
		JOIN middleman_labels l ON l.id = il.label_id
		JOIN middleman_issues i ON i.id = il.issue_id
		WHERE i.repo_id = ? AND i.number = 1`, repoID).Scan(&label))
	assert.Equal("new", label)
}

func TestArchiveInventoryMergeRequestPreservesFetchedDetailFields(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	now := archiveTestTime()
	repoID := insertTestRepo(t, d, "acme", "mr-detail")
	detailFetchedAt := now.Add(-time.Minute)
	_, err := d.UpsertMergeRequest(ctx, &MergeRequest{
		RepoID: repoID, PlatformID: 1, PlatformExternalID: "mr-1", Number: 1,
		Title: "detailed", State: MergeRequestStateOpen, CreatedAt: now.Add(-time.Hour),
		UpdatedAt: now, LastActivityAt: now, Additions: 17, Deletions: 9, CommentCount: 4,
		ReviewDecision: "approved", CIStatus: "success", CIChecksJSON: `[{"name":"test"}]`,
		MergeableState: "clean", DetailFetchedAt: &detailFetchedAt,
	})
	require.NoError(err)
	require.NoError(d.EnsureDiscoveryArchives(ctx, []int64{repoID}, now))
	require.NoError(d.StartFullArchives(ctx, []int64{repoID}, now))

	require.NoError(d.CommitArchiveInventoryPage(ctx, ArchiveInventoryCommit{
		RepoID: repoID, ItemType: ArchiveItemTypeMergeRequest, Exhausted: true, Now: now.Add(time.Minute),
		MergeRequests: []ArchiveInventoryMergeRequest{{
			ProviderItemID: "mr-1", CommentsStatus: ArchiveDatasetStatusPending,
			ReviewsStatus: ArchiveDatasetStatusPending, InlineCommentsStatus: ArchiveDatasetStatusPending,
			Snapshot: MergeRequestSnapshot{MergeRequest: MergeRequest{
				RepoID: repoID, PlatformID: 1, PlatformExternalID: "mr-1", Number: 1,
				Title: "inventory", State: MergeRequestStateClosed, CreatedAt: now.Add(-time.Hour),
				UpdatedAt: now.Add(time.Minute), LastActivityAt: now.Add(time.Minute),
			}},
		}},
	}))

	var title, state, reviewDecision, ciStatus, ciChecks, mergeableState string
	var additions, deletions, commentCount int
	var storedDetailFetchedAt time.Time
	require.NoError(d.ReadDB().QueryRowContext(ctx, `
		SELECT title, state, additions, deletions, comment_count, review_decision,
			ci_status, ci_checks_json, mergeable_state, detail_fetched_at
		FROM middleman_merge_requests WHERE repo_id = ? AND number = 1`, repoID,
	).Scan(&title, &state, &additions, &deletions, &commentCount, &reviewDecision,
		&ciStatus, &ciChecks, &mergeableState, &storedDetailFetchedAt))
	assert.Equal("inventory", title)
	assert.Equal(string(MergeRequestStateClosed), state)
	assert.Equal(17, additions)
	assert.Equal(9, deletions)
	assert.Equal(4, commentCount)
	assert.Equal("approved", reviewDecision)
	assert.Equal("success", ciStatus)
	assert.JSONEq(`[{"name":"test"}]`, ciChecks)
	assert.Equal("clean", mergeableState)
	assert.Equal(detailFetchedAt, storedDetailFetchedAt)
}

func TestArchiveInventoryDiffChangePreservesCommentCount(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	now := archiveTestTime()
	repoID := insertTestRepo(t, d, "acme", "inventory-new-diff")
	_, err := d.UpsertMergeRequest(ctx, &MergeRequest{
		RepoID: repoID, PlatformID: 1, PlatformExternalID: "mr-1", Number: 1,
		Title: "old diff", State: MergeRequestStateOpen,
		PlatformHeadSHA: "old-head", PlatformBaseSHA: "old-base",
		Additions: 17, Deletions: 9, CommentCount: 4, ReviewDecision: "approved",
		CIStatus: "success", CIChecksJSON: `[{"name":"test"}]`, MergeableState: "clean",
		CreatedAt: now.Add(-time.Hour), UpdatedAt: now, LastActivityAt: now,
	})
	require.NoError(err)
	require.NoError(d.EnsureDiscoveryArchives(ctx, []int64{repoID}, now))
	require.NoError(d.StartFullArchives(ctx, []int64{repoID}, now))

	require.NoError(d.CommitArchiveInventoryPage(ctx, ArchiveInventoryCommit{
		RepoID: repoID, ItemType: ArchiveItemTypeMergeRequest, Exhausted: true, Now: now.Add(time.Minute),
		MergeRequests: []ArchiveInventoryMergeRequest{{
			ProviderItemID: "mr-1", CommentsStatus: ArchiveDatasetStatusPending,
			ReviewsStatus: ArchiveDatasetStatusPending, InlineCommentsStatus: ArchiveDatasetStatusPending,
			Snapshot: MergeRequestSnapshot{MergeRequest: MergeRequest{
				RepoID: repoID, PlatformID: 1, PlatformExternalID: "mr-1", Number: 1,
				Title: "new diff", State: MergeRequestStateOpen,
				PlatformHeadSHA: "new-head", PlatformBaseSHA: "new-base",
				CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(time.Minute),
				LastActivityAt: now.Add(time.Minute),
			}},
		}},
	}))

	stored, err := d.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 1)
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
}

func TestArchiveHydrationClearsHeadBoundDetailWhenHeadChanges(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	now := archiveTestTime()
	repoID := insertTestRepo(t, d, "acme", "mr-new-head")
	detailFetchedAt := now.Add(-time.Minute)
	_, err := d.UpsertMergeRequest(ctx, &MergeRequest{
		RepoID: repoID, PlatformID: 1, PlatformExternalID: "mr-1", Number: 1,
		Title: "old head", State: MergeRequestStateOpen,
		PlatformHeadSHA: "old-head", PlatformBaseSHA: "base",
		ReviewDecision: "approved", CIStatus: "success",
		CIChecksJSON: `[{"name":"test"}]`, CIHadPending: true,
		MergeableState: "clean", DetailFetchedAt: &detailFetchedAt,
		CreatedAt: now.Add(-time.Hour), UpdatedAt: now, LastActivityAt: now,
	})
	require.NoError(err)

	_, _, accepted, err := d.UpsertArchiveMergeRequestSnapshot(ctx, &MergeRequest{
		RepoID: repoID, PlatformID: 1, PlatformExternalID: "mr-1", Number: 1,
		Title: "new head", State: MergeRequestStateOpen,
		PlatformHeadSHA: "new-head", PlatformBaseSHA: "base",
		CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(time.Minute),
		LastActivityAt: now.Add(time.Minute),
	})
	require.NoError(err)
	require.True(accepted)
	stored, err := d.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 1)
	require.NoError(err)
	require.NotNil(stored)
	assert.Equal("new-head", stored.PlatformHeadSHA)
	assert.Empty(stored.ReviewDecision)
	assert.Empty(stored.CIStatus)
	assert.Empty(stored.CIChecksJSON)
	assert.False(stored.CIHadPending)
	assert.Empty(stored.MergeableState)
	assert.Nil(stored.DetailFetchedAt)
}

func TestArchiveHydrationRefreshesProviderDetailWhenBaseChanges(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	now := archiveTestTime()
	repoID := insertTestRepo(t, d, "acme", "mr-new-base")
	detailFetchedAt := now.Add(-time.Minute)
	_, err := d.UpsertMergeRequest(ctx, &MergeRequest{
		RepoID: repoID, PlatformID: 1, PlatformExternalID: "mr-1", Number: 1,
		Title: "old base", State: MergeRequestStateOpen,
		PlatformHeadSHA: "same-head", PlatformBaseSHA: "old-base",
		Additions: 10, Deletions: 4, CommentCount: 3,
		ReviewDecision: "approved", CIStatus: "success",
		CIChecksJSON: `[{"name":"test"}]`, CIHadPending: true,
		MergeableState: "clean", DetailFetchedAt: &detailFetchedAt,
		CreatedAt: now.Add(-time.Hour), UpdatedAt: now, LastActivityAt: now,
	})
	require.NoError(err)

	_, _, accepted, err := d.UpsertArchiveMergeRequestSnapshot(ctx, &MergeRequest{
		RepoID: repoID, PlatformID: 1, PlatformExternalID: "mr-1", Number: 1,
		Title: "new base", State: MergeRequestStateOpen,
		PlatformHeadSHA: "same-head", PlatformBaseSHA: "new-base",
		Additions: 12, Deletions: 5, CommentCount: 4, MergeableState: "dirty",
		CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(time.Minute),
		LastActivityAt: now.Add(time.Minute),
	})
	require.NoError(err)
	require.True(accepted)
	stored, err := d.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 1)
	require.NoError(err)
	require.NotNil(stored)
	assert.Equal("same-head", stored.PlatformHeadSHA)
	assert.Equal("new-base", stored.PlatformBaseSHA)
	assert.Equal(12, stored.Additions)
	assert.Equal(5, stored.Deletions)
	assert.Equal(4, stored.CommentCount)
	assert.Equal("dirty", stored.MergeableState)
	assert.Empty(stored.ReviewDecision)
	assert.Empty(stored.CIStatus)
	assert.Empty(stored.CIChecksJSON)
	assert.False(stored.CIHadPending)
	assert.Nil(stored.DetailFetchedAt)
}

func TestArchiveHydrationStaleMergeRequestSnapshotPreservesNewerLabels(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	now := archiveTestTime()
	repoID := insertTestRepo(t, d, "acme", "mr-labels")
	write := func(updatedAt time.Time, title, label string, platformID int64) error {
		_, _, _, err := d.UpsertArchiveMergeRequestSnapshot(ctx, &MergeRequest{
			RepoID: repoID, PlatformID: platformID, PlatformExternalID: "mr-1", Number: 1,
			Title: title, State: "closed", CreatedAt: now.Add(-time.Hour), UpdatedAt: updatedAt,
			LastActivityAt: updatedAt,
			Labels:         []Label{{RepoID: repoID, PlatformID: platformID, Name: label, Color: "ffffff", UpdatedAt: updatedAt}},
		})
		return err
	}
	require.NoError(write(now, "new", "new", 1))
	require.NoError(write(now.Add(-time.Hour), "old", "old", 2))

	var title, label string
	require.NoError(d.ReadDB().QueryRowContext(ctx, `
		SELECT mr.title, l.name FROM middleman_merge_requests mr
		JOIN middleman_merge_request_labels ml ON ml.merge_request_id = mr.id
		JOIN middleman_labels l ON l.id = ml.label_id
		WHERE mr.repo_id = ? AND mr.number = 1`, repoID).Scan(&title, &label))
	assert.Equal("new", title)
	assert.Equal("new", label)
}

func TestArchiveHydrationRejectedSnapshotsAdvanceWorkAndDiscardStagedPages(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	stale := archiveTestTime()
	current := stale.Add(time.Hour)
	repoID := insertTestRepo(t, d, "acme", "stale-hydration")
	require.NoError(d.EnsureDiscoveryArchives(ctx, []int64{repoID}, stale))
	require.NoError(d.StartFullArchives(ctx, []int64{repoID}, stale))
	_, err := d.UpsertIssue(ctx, &Issue{RepoID: repoID, PlatformID: 7, Number: 7,
		State: "closed", CreatedAt: stale.Add(-time.Hour), UpdatedAt: stale, LastActivityAt: stale})
	require.NoError(err)
	_, err = d.UpsertMergeRequest(ctx, &MergeRequest{RepoID: repoID, PlatformID: 8, Number: 7,
		State: MergeRequestStateClosed, CreatedAt: stale.Add(-time.Hour), UpdatedAt: stale, LastActivityAt: stale})
	require.NoError(err)

	for _, itemType := range []ArchiveItemType{ArchiveItemTypeIssue, ArchiveItemTypeMergeRequest} {
		commentsStatus := ArchiveDatasetStatusComplete
		reviewsStatus := ArchiveDatasetStatusPending
		inlineStatus := ArchiveDatasetStatusComplete
		if itemType == ArchiveItemTypeIssue {
			commentsStatus = ArchiveDatasetStatusPending
			reviewsStatus = ArchiveDatasetStatusNotApplicable
			inlineStatus = ArchiveDatasetStatusNotApplicable
		}
		insertArchiveItemForTest(t, d, repoID, itemType, 7, stale,
			commentsStatus, reviewsStatus, inlineStatus)
		revision := archiveParentRevisionForTest(t, d, repoID, itemType, 7)
		dataset := ArchiveDatasetComments
		if itemType == ArchiveItemTypeMergeRequest {
			dataset = ArchiveDatasetReviews
		}
		key := ArchiveDatasetKey{RepoID: repoID, ItemType: itemType, ItemNumber: 7,
			Dataset: dataset, SnapshotUpdatedAt: stale, DomainRevision: revision}
		_, err := d.CommitArchiveDatasetPage(ctx, ArchiveDatasetPage{ArchiveDatasetKey: key,
			Exhausted: true, Payload: []byte(`[]`), MaxPages: 1, MaxRecords: 1, MaxBytes: 16, Now: stale})
		require.NoError(err)
	}

	issue := &Issue{RepoID: repoID, PlatformID: 7, PlatformExternalID: "issue-7", Number: 7,
		Title: "current issue", State: "closed", CreatedAt: stale.Add(-time.Hour),
		UpdatedAt: current, LastActivityAt: current}
	_, err = d.UpsertIssue(ctx, issue)
	require.NoError(err)
	mr := &MergeRequest{RepoID: repoID, PlatformID: 8, PlatformExternalID: "mr-7", Number: 7,
		Title: "current mr", State: MergeRequestStateClosed, CreatedAt: stale.Add(-time.Hour),
		UpdatedAt: current, LastActivityAt: current}
	_, err = d.UpsertMergeRequest(ctx, mr)
	require.NoError(err)

	_, _, issueAccepted, err := d.UpsertArchiveIssueSnapshot(ctx, &Issue{RepoID: repoID,
		PlatformID: 7, PlatformExternalID: "issue-7", Number: 7, Title: "stale issue",
		State: "closed", CreatedAt: stale.Add(-time.Hour), UpdatedAt: stale, LastActivityAt: stale})
	require.NoError(err)
	_, _, mrAccepted, err := d.UpsertArchiveMergeRequestSnapshot(ctx, &MergeRequest{RepoID: repoID,
		PlatformID: 8, PlatformExternalID: "mr-7", Number: 7, Title: "stale mr",
		State: MergeRequestStateClosed, CreatedAt: stale.Add(-time.Hour), UpdatedAt: stale, LastActivityAt: stale})
	require.NoError(err)
	assert.False(issueAccepted)
	assert.False(mrAccepted)

	for _, itemType := range []ArchiveItemType{ArchiveItemTypeIssue, ArchiveItemTypeMergeRequest} {
		item := archiveItemState(t, d, repoID, itemType, 7)
		assert.Equal(current, item.ProviderUpdatedAt)
		assert.Equal(ArchiveDatasetStatusPending, item.CommentsStatus)
		if itemType == ArchiveItemTypeMergeRequest {
			assert.Equal(ArchiveDatasetStatusPending, item.ReviewsStatus)
			assert.Equal(ArchiveDatasetStatusPending, item.InlineCommentsStatus)
		}
		dataset := ArchiveDatasetComments
		if itemType == ArchiveItemTypeMergeRequest {
			dataset = ArchiveDatasetReviews
		}
		stage, stageErr := d.GetArchiveDatasetStage(ctx, ArchiveDatasetKey{RepoID: repoID,
			ItemType: itemType, ItemNumber: 7, Dataset: dataset,
			SnapshotUpdatedAt: stale})
		require.NoError(stageErr)
		assert.Zero(stage.PageCount)
	}
}

func TestArchiveDatasetPublishRejectsParentAdvancedAfterStage(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	stagedAt := archiveTestTime()
	current := stagedAt.Add(time.Hour)
	repoID := insertTestRepo(t, d, "acme", "publish-race")
	require.NoError(d.EnsureDiscoveryArchives(ctx, []int64{repoID}, stagedAt))
	require.NoError(d.StartFullArchives(ctx, []int64{repoID}, stagedAt))
	insertArchiveItemForTest(t, d, repoID, ArchiveItemTypeIssue, 1, stagedAt,
		ArchiveDatasetStatusPending, ArchiveDatasetStatusNotApplicable, ArchiveDatasetStatusNotApplicable)
	issueID, err := d.UpsertIssue(ctx, &Issue{RepoID: repoID, PlatformID: 1, Number: 1,
		State: "closed", CreatedAt: stagedAt.Add(-time.Hour), UpdatedAt: stagedAt, LastActivityAt: stagedAt})
	require.NoError(err)
	revision := archiveParentRevisionForTest(t, d, repoID, ArchiveItemTypeIssue, 1)
	key := ArchiveDatasetKey{RepoID: repoID, ItemType: ArchiveItemTypeIssue, ItemNumber: 1,
		Dataset: ArchiveDatasetComments, SnapshotUpdatedAt: stagedAt, DomainRevision: revision}
	_, err = d.CommitArchiveDatasetPage(ctx, ArchiveDatasetPage{ArchiveDatasetKey: key,
		Exhausted: true, Payload: []byte(`[]`), MaxPages: 1, MaxRecords: 1, MaxBytes: 16, Now: stagedAt})
	require.NoError(err)
	_, err = d.UpsertIssue(ctx, &Issue{RepoID: repoID, PlatformID: 1, Number: 1,
		State: "closed", CreatedAt: stagedAt.Add(-time.Hour), UpdatedAt: current, LastActivityAt: current})
	require.NoError(err)
	require.NoError(d.UpsertIssueEvents(ctx, []IssueEvent{{IssueID: issueID,
		PlatformExternalID: "current", EventType: "issue_comment", Body: "keep current",
		CreatedAt: current, DedupeKey: "current"}}))

	err = d.PublishArchiveIssueComments(ctx, key, issueID, nil)
	require.Error(err)
	assert.Contains(err.Error(), "domain parent snapshot changed")
	var body string
	require.NoError(d.ReadDB().QueryRowContext(ctx, `
		SELECT body FROM middleman_issue_events WHERE issue_id = ? AND dedupe_key = 'current'`, issueID,
	).Scan(&body))
	assert.Equal("keep current", body)
	stage, err := d.GetArchiveDatasetStage(ctx, key)
	require.NoError(err)
	assert.Equal(1, stage.PageCount)
}

func TestArchiveDatasetPublishRejectsEqualTimestampParentRevision(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	now := archiveTestTime()
	repoID := insertTestRepo(t, d, "acme", "equal-timestamp-publish-race")
	require.NoError(d.EnsureDiscoveryArchives(ctx, []int64{repoID}, now))
	require.NoError(d.StartFullArchives(ctx, []int64{repoID}, now))
	insertArchiveItemForTest(t, d, repoID, ArchiveItemTypeIssue, 1, now,
		ArchiveDatasetStatusPending, ArchiveDatasetStatusNotApplicable, ArchiveDatasetStatusNotApplicable)
	issueID, revision, accepted, err := d.UpsertArchiveIssueSnapshot(ctx, &Issue{
		RepoID: repoID, PlatformID: 1, PlatformExternalID: "issue-1", Number: 1,
		Title: "archive", State: "closed", CreatedAt: now.Add(-time.Hour),
		UpdatedAt: now, LastActivityAt: now,
	})
	require.NoError(err)
	require.True(accepted)
	key := ArchiveDatasetKey{
		RepoID: repoID, ItemType: ArchiveItemTypeIssue, ItemNumber: 1,
		Dataset: ArchiveDatasetComments, SnapshotUpdatedAt: now, DomainRevision: revision,
	}
	_, err = d.CommitArchiveDatasetPage(ctx, ArchiveDatasetPage{
		ArchiveDatasetKey: key, Exhausted: true, Payload: []byte(`[]`),
		MaxPages: 1, MaxRecords: 1, MaxBytes: 16, Now: now,
	})
	require.NoError(err)
	_, _, accepted, err = d.UpsertIssueSnapshotWithLabels(ctx, &Issue{
		RepoID: repoID, PlatformID: 1, PlatformExternalID: "issue-1", Number: 1,
		Title: "normal sync", State: "closed", CreatedAt: now.Add(-time.Hour),
		UpdatedAt: now, LastActivityAt: now,
	})
	require.NoError(err)
	require.True(accepted)
	currentParent, err := d.GetIssueByRepoIDAndNumber(ctx, repoID, 1)
	require.NoError(err)
	require.NotNil(currentParent)
	newRevisionStage, err := d.GetArchiveDatasetStage(ctx, ArchiveDatasetKey{
		RepoID: repoID, ItemType: ArchiveItemTypeIssue, ItemNumber: 1,
		Dataset: ArchiveDatasetComments, SnapshotUpdatedAt: now,
		DomainRevision: currentParent.SnapshotRevision,
	})
	require.NoError(err)
	assert.Zero(newRevisionStage.PageCount)
	require.NoError(d.UpsertIssueEvents(ctx, []IssueEvent{{
		IssueID: issueID, EventType: "issue_comment", DedupeKey: "current",
		Body: "keep current", CreatedAt: now,
	}}))

	err = d.PublishArchiveIssueComments(ctx, key, issueID, nil)
	require.Error(err)
	assert.Contains(err.Error(), "domain parent revision changed")
	var body string
	require.NoError(d.ReadDB().QueryRowContext(ctx, `
		SELECT body FROM middleman_issue_events
		WHERE issue_id = ? AND dedupe_key = 'current'`, issueID,
	).Scan(&body))
	assert.Equal("keep current", body)
}

func TestArchiveEqualTimestampParentAdvanceRebindsStagedFamily(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := openTestDB(t)
	ctx := t.Context()
	now := archiveTestTime()
	repoID := insertTestRepo(t, database, "acme", "rebind-staged-family")
	require.NoError(database.EnsureDiscoveryArchives(ctx, []int64{repoID}, now))
	require.NoError(database.StartFullArchives(ctx, []int64{repoID}, now))
	insertArchiveItemForTest(
		t, database, repoID, ArchiveItemTypeMergeRequest, 7, now,
		ArchiveDatasetStatusComplete, ArchiveDatasetStatusPending, ArchiveDatasetStatusPending,
	)
	mr := &MergeRequest{
		RepoID: repoID, PlatformID: 7, PlatformExternalID: "mr-7", Number: 7,
		Title: "open MR", State: MergeRequestStateOpen,
		CreatedAt: now.Add(-time.Hour), UpdatedAt: now, LastActivityAt: now,
	}
	_, firstRevision, accepted, err := database.UpsertArchiveMergeRequestSnapshot(ctx, mr)
	require.NoError(err)
	require.True(accepted)
	firstKey := ArchiveDatasetKey{
		RepoID: repoID, ItemType: ArchiveItemTypeMergeRequest, ItemNumber: 7,
		Dataset: ArchiveDatasetReviews, SnapshotUpdatedAt: now, DomainRevision: firstRevision,
	}
	_, err = database.CommitArchiveDatasetPage(ctx, ArchiveDatasetPage{
		ArchiveDatasetKey: firstKey, NextCursor: "reviews-page-2",
		RecordCount: 1, Payload: []byte(`[{"dedupe_key":"review-1"}]`),
		MaxPages: 10, MaxRecords: 10, MaxBytes: 1024, Now: now,
	})
	require.NoError(err)

	_, currentRevision, accepted, err := database.UpsertMergeRequestSnapshotWithLabels(ctx, mr)
	require.NoError(err)
	require.True(accepted)
	require.Greater(currentRevision, firstRevision)
	_, resumedRevision, accepted, err := database.UpsertArchiveMergeRequestSnapshot(ctx, mr)
	require.NoError(err)
	require.True(accepted)
	require.Greater(resumedRevision, currentRevision)
	currentKey := firstKey
	currentKey.DomainRevision = resumedRevision
	stage, err := database.GetArchiveDatasetStage(ctx, currentKey)
	require.NoError(err)
	assert.Equal(1, stage.PageCount)
	assert.Equal("reviews-page-2", stage.NextCursor)
	item := archiveItemState(t, database, repoID, ArchiveItemTypeMergeRequest, 7)
	assert.Equal(ArchiveDatasetStatusComplete, item.CommentsStatus)
	assert.Equal(ArchiveDatasetStatusPending, item.ReviewsStatus)
	assert.Equal(ArchiveDatasetStatusPending, item.InlineCommentsStatus)

	_, stableRevision, accepted, err := database.UpsertArchiveMergeRequestSnapshot(ctx, mr)
	require.NoError(err)
	require.True(accepted)
	assert.Equal(resumedRevision, stableRevision)
	stage, err = database.GetArchiveDatasetStage(ctx, currentKey)
	require.NoError(err)
	assert.Equal(1, stage.PageCount)
	assert.Equal("reviews-page-2", stage.NextCursor)
}

func TestArchiveDatasetPageEnforcesCumulativeLimitsWithoutLosingCursor(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	now := archiveTestTime()
	repoID := insertTestRepo(t, d, "acme", "bounded")
	require.NoError(d.EnsureDiscoveryArchives(ctx, []int64{repoID}, now))
	require.NoError(d.StartFullArchives(ctx, []int64{repoID}, now))
	insertArchiveItemForTest(t, d, repoID, ArchiveItemTypeIssue, 1, now,
		ArchiveDatasetStatusPending, ArchiveDatasetStatusNotApplicable, ArchiveDatasetStatusNotApplicable)
	_, err := d.UpsertIssue(ctx, &Issue{RepoID: repoID, PlatformID: 1, Number: 1,
		State: "closed", CreatedAt: now, UpdatedAt: now, LastActivityAt: now})
	require.NoError(err)
	key := ArchiveDatasetKey{RepoID: repoID, ItemType: ArchiveItemTypeIssue, ItemNumber: 1,
		Dataset: ArchiveDatasetComments, SnapshotUpdatedAt: now,
		DomainRevision: archiveParentRevisionForTest(t, d, repoID, ArchiveItemTypeIssue, 1)}
	stage, err := d.CommitArchiveDatasetPage(ctx, ArchiveDatasetPage{
		ArchiveDatasetKey: key, NextCursor: "c2", RecordCount: 1, Payload: []byte(`[1]`),
		MaxPages: 2, MaxRecords: 1, MaxBytes: 16, Now: now,
	})
	require.NoError(err)
	assert.Equal("c2", stage.NextCursor)

	_, err = d.CommitArchiveDatasetPage(ctx, ArchiveDatasetPage{
		ArchiveDatasetKey: key, InputCursor: "c2", Exhausted: true,
		RecordCount: 1, Payload: []byte(`[2]`), MaxPages: 2, MaxRecords: 1, MaxBytes: 16, Now: now,
	})
	var limit *ArchiveDatasetLimitError
	require.ErrorAs(err, &limit)
	assert.Equal("record", limit.Limit)
	stage, err = d.GetArchiveDatasetStage(ctx, key)
	require.NoError(err)
	assert.Equal("c2", stage.NextCursor)
	assert.Equal(1, stage.PageCount)

	newer := now.Add(time.Hour)
	require.NoError(d.CommitArchiveInventoryPage(ctx, ArchiveInventoryCommit{
		RepoID: repoID, ItemType: ArchiveItemTypeIssue, Exhausted: true, Now: newer,
		Issues: []ArchiveInventoryIssue{{ProviderItemID: "issue-1", CommentsStatus: ArchiveDatasetStatusPending,
			Snapshot: IssueSnapshot{Issue: Issue{RepoID: repoID, PlatformID: 1, Number: 1,
				State: "closed", CreatedAt: now, UpdatedAt: newer, LastActivityAt: newer}}}},
	}))
	stage, err = d.GetArchiveDatasetStage(ctx, key)
	require.NoError(err)
	assert.Zero(stage.PageCount)
	_, err = d.CommitArchiveDatasetPage(ctx, ArchiveDatasetPage{
		ArchiveDatasetKey: key, InputCursor: "c2", Exhausted: true, Payload: []byte(`[]`),
		MaxPages: 2, MaxRecords: 1, MaxBytes: 16, Now: newer,
	})
	assert.Error(err)
}

func TestArchiveDatasetPageByteLimitIncludesStoredCursors(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	now := archiveTestTime()
	repoID := insertTestRepo(t, d, "acme", "cursor-bounded")
	require.NoError(d.EnsureDiscoveryArchives(ctx, []int64{repoID}, now))
	require.NoError(d.StartFullArchives(ctx, []int64{repoID}, now))
	insertArchiveItemForTest(t, d, repoID, ArchiveItemTypeIssue, 1, now,
		ArchiveDatasetStatusPending, ArchiveDatasetStatusNotApplicable, ArchiveDatasetStatusNotApplicable)
	_, err := d.UpsertIssue(ctx, &Issue{RepoID: repoID, PlatformID: 1, Number: 1,
		State: "closed", CreatedAt: now, UpdatedAt: now, LastActivityAt: now})
	require.NoError(err)
	key := ArchiveDatasetKey{RepoID: repoID, ItemType: ArchiveItemTypeIssue, ItemNumber: 1,
		Dataset: ArchiveDatasetComments, SnapshotUpdatedAt: now,
		DomainRevision: archiveParentRevisionForTest(t, d, repoID, ArchiveItemTypeIssue, 1)}
	stage, err := d.CommitArchiveDatasetPage(ctx, ArchiveDatasetPage{
		ArchiveDatasetKey: key, NextCursor: "cursor", Payload: []byte(`[]`),
		MaxPages: 2, MaxRecords: 2, MaxBytes: 15, Now: now,
	})
	require.NoError(err)
	assert.Equal(int64(8), stage.Bytes)

	_, err = d.CommitArchiveDatasetPage(ctx, ArchiveDatasetPage{
		ArchiveDatasetKey: key, InputCursor: "cursor", Exhausted: true, Payload: []byte(`[]`),
		MaxPages: 2, MaxRecords: 2, MaxBytes: 15, Now: now,
	})
	var limit *ArchiveDatasetLimitError
	require.ErrorAs(err, &limit)
	assert.Equal("byte", limit.Limit)
	assert.Equal(int64(16), limit.Value)
	stage, err = d.GetArchiveDatasetStage(ctx, key)
	require.NoError(err)
	assert.Equal(int64(8), stage.Bytes)
	assert.Equal("cursor", stage.NextCursor)
}

func TestArchiveDatasetPublishRollsBackReplacementStatusAndStageCleanup(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	now := archiveTestTime()
	repoID := insertTestRepo(t, d, "acme", "atomic")
	require.NoError(d.EnsureDiscoveryArchives(ctx, []int64{repoID}, now))
	require.NoError(d.StartFullArchives(ctx, []int64{repoID}, now))
	insertArchiveItemForTest(t, d, repoID, ArchiveItemTypeIssue, 1, now,
		ArchiveDatasetStatusPending, ArchiveDatasetStatusNotApplicable, ArchiveDatasetStatusNotApplicable)
	issueID, err := d.UpsertIssue(ctx, &Issue{RepoID: repoID, PlatformID: 1, Number: 1,
		State: "closed", CreatedAt: now, UpdatedAt: now, LastActivityAt: now})
	require.NoError(err)
	require.NoError(d.UpsertIssueEvents(ctx, []IssueEvent{{IssueID: issueID,
		EventType: "issue_comment", DedupeKey: "old", Body: "keep", CreatedAt: now}}))
	key := ArchiveDatasetKey{RepoID: repoID, ItemType: ArchiveItemTypeIssue, ItemNumber: 1,
		Dataset: ArchiveDatasetComments, SnapshotUpdatedAt: now,
		DomainRevision: archiveParentRevisionForTest(t, d, repoID, ArchiveItemTypeIssue, 1)}
	_, err = d.CommitArchiveDatasetPage(ctx, ArchiveDatasetPage{ArchiveDatasetKey: key,
		Exhausted: true, Payload: []byte(`[]`), MaxPages: 1, MaxRecords: 1, MaxBytes: 16, Now: now})
	require.NoError(err)
	_, err = d.WriteDB().ExecContext(ctx, `
		UPDATE middleman_archive_dataset_pages SET snapshot_updated_at = ? WHERE repo_id = ?`,
		now.Add(-time.Hour), repoID)
	require.NoError(err)
	err = d.PublishArchiveIssueComments(ctx, key, issueID, nil)
	require.Error(err)
	_, err = d.WriteDB().ExecContext(ctx, `
		UPDATE middleman_archive_dataset_pages SET snapshot_updated_at = ? WHERE repo_id = ?`, now, repoID)
	require.NoError(err)
	_, err = d.WriteDB().ExecContext(ctx, `
		CREATE TRIGGER fail_archive_publish BEFORE INSERT ON middleman_issue_events
		WHEN NEW.dedupe_key = 'new' BEGIN SELECT RAISE(ABORT, 'publish failed'); END`)
	require.NoError(err)

	err = d.PublishArchiveIssueComments(ctx, key, issueID, []IssueEvent{{IssueID: issueID,
		EventType: "issue_comment", DedupeKey: "new", Body: "replace", CreatedAt: now}})
	require.Error(err)
	var oldCount, stageCount int
	var status string
	require.NoError(d.ReadDB().QueryRowContext(ctx, `
		SELECT COUNT(*) FROM middleman_issue_events WHERE issue_id = ? AND dedupe_key = 'old'`, issueID).Scan(&oldCount))
	require.NoError(d.ReadDB().QueryRowContext(ctx, `
		SELECT COUNT(*) FROM middleman_archive_dataset_pages WHERE repo_id = ?`, repoID).Scan(&stageCount))
	require.NoError(d.ReadDB().QueryRowContext(ctx, `
		SELECT comments_status FROM middleman_archive_items
		WHERE repo_id = ? AND item_type = 'issue' AND item_number = 1`, repoID).Scan(&status))
	assert.Equal(1, oldCount)
	assert.Equal(1, stageCount)
	assert.Equal("pending", status)
}

func TestArchiveIssuePublicationSupportsMaximumDatasetSize(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	now := archiveTestTime()
	repoID := insertTestRepo(t, d, "acme", "maximum-dataset")
	require.NoError(d.EnsureDiscoveryArchives(ctx, []int64{repoID}, now))
	require.NoError(d.StartFullArchives(ctx, []int64{repoID}, now))
	insertArchiveItemForTest(t, d, repoID, ArchiveItemTypeIssue, 1, now,
		ArchiveDatasetStatusPending, ArchiveDatasetStatusNotApplicable, ArchiveDatasetStatusNotApplicable)
	issueID, err := d.UpsertIssue(ctx, &Issue{RepoID: repoID, PlatformID: 1, Number: 1,
		State: "closed", CreatedAt: now.Add(-time.Hour), UpdatedAt: now, LastActivityAt: now})
	require.NoError(err)
	key := ArchiveDatasetKey{RepoID: repoID, ItemType: ArchiveItemTypeIssue, ItemNumber: 1,
		Dataset: ArchiveDatasetComments, SnapshotUpdatedAt: now,
		DomainRevision: archiveParentRevisionForTest(t, d, repoID, ArchiveItemTypeIssue, 1)}
	_, err = d.CommitArchiveDatasetPage(ctx, ArchiveDatasetPage{ArchiveDatasetKey: key,
		Exhausted: true, RecordCount: 50_000, Payload: []byte(`[]`),
		MaxPages: 1, MaxRecords: 50_000, MaxBytes: 16 << 20, Now: now})
	require.NoError(err)
	events := make([]IssueEvent, 50_000)
	for i := range events {
		events[i] = IssueEvent{IssueID: issueID, EventType: "issue_comment",
			DedupeKey: fmt.Sprintf("comment-%05d", i), CreatedAt: now}
	}

	require.NoError(d.PublishArchiveIssueComments(ctx, key, issueID, events))
	var count int
	require.NoError(d.ReadDB().QueryRowContext(ctx, `
		SELECT COUNT(*) FROM middleman_issue_events
		WHERE issue_id = ? AND event_type = 'issue_comment'`, issueID).Scan(&count))
	assert.Equal(50_000, count)

	_, err = d.WriteDB().ExecContext(ctx, `
		UPDATE middleman_archive_items SET comments_status = 'pending'
		WHERE repo_id = ? AND item_type = 'issue' AND item_number = 1`, repoID)
	require.NoError(err)
	_, err = d.CommitArchiveDatasetPage(ctx, ArchiveDatasetPage{ArchiveDatasetKey: key,
		Exhausted: true, RecordCount: 50_000, Payload: []byte(`[]`),
		MaxPages: 1, MaxRecords: 50_000, MaxBytes: 16 << 20, Now: now.Add(time.Minute)})
	require.NoError(err)
	require.NoError(d.PublishArchiveIssueComments(ctx, key, issueID, events))
	require.NoError(d.ReadDB().QueryRowContext(ctx, `
		SELECT COUNT(*) FROM middleman_issue_events
		WHERE issue_id = ? AND event_type = 'issue_comment'`, issueID).Scan(&count))
	assert.Equal(50_000, count)
}

func TestArchivePromptObservationRefreshesEqualTimestampDatasetsOnce(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	now := archiveTestTime()
	repoID := insertTestRepo(t, d, "acme", "equal-timestamp")
	require.NoError(d.EnsureDiscoveryArchives(ctx, []int64{repoID}, now))
	require.NoError(d.StartFullArchives(ctx, []int64{repoID}, now))
	item := ArchiveInventoryIssue{ProviderItemID: "issue-1", CommentsStatus: ArchiveDatasetStatusPending,
		Snapshot: IssueSnapshot{Issue: Issue{RepoID: repoID, PlatformID: 1, Number: 1,
			State: "closed", CreatedAt: now.Add(-time.Hour), UpdatedAt: now, LastActivityAt: now}}}
	require.NoError(d.CommitArchiveInventoryPage(ctx, ArchiveInventoryCommit{
		RepoID: repoID, ItemType: ArchiveItemTypeIssue, Exhausted: true, Now: now,
		Issues: []ArchiveInventoryIssue{item},
	}))
	parent, err := d.GetIssueByRepoIDAndNumber(ctx, repoID, 1)
	require.NoError(err)
	require.NotNil(parent)
	key := ArchiveDatasetKey{RepoID: repoID, ItemType: ArchiveItemTypeIssue, ItemNumber: 1,
		Dataset: ArchiveDatasetComments, SnapshotUpdatedAt: now, DomainRevision: parent.SnapshotRevision}
	_, err = d.CommitArchiveDatasetPage(ctx, ArchiveDatasetPage{ArchiveDatasetKey: key,
		NextCursor: "stale-page", RecordCount: 1, Payload: []byte(`[1]`),
		MaxPages: 2, MaxRecords: 2, MaxBytes: 32, Now: now.Add(time.Minute)})
	require.NoError(err)
	require.NoError(d.MarkArchiveItemHydrated(ctx, ArchiveItemHydration{
		RepoID: repoID, ItemType: ArchiveItemTypeIssue, ItemNumber: 1,
		DomainRevision: parent.SnapshotRevision,
		CommentsStatus: ArchiveDatasetStatusComplete, ReviewsStatus: ArchiveDatasetStatusNotApplicable,
		InlineStatus: ArchiveDatasetStatusNotApplicable, MirroredUpdatedAt: now, HydratedAt: now.Add(time.Minute),
	}))
	require.NoError(d.CommitArchiveInventoryPage(ctx, ArchiveInventoryCommit{
		RepoID: repoID, ItemType: ArchiveItemTypeIssue, Exhausted: true,
		Now: now.Add(90 * time.Second), Issues: []ArchiveInventoryIssue{item},
	}))
	state := archiveItemState(t, d, repoID, ArchiveItemTypeIssue, 1)
	assert.Equal(ArchiveDatasetStatusComplete, state.CommentsStatus)
	stage, err := d.GetArchiveDatasetStage(ctx, key)
	require.NoError(err)
	assert.Equal(1, stage.PageCount)

	promptState, err := d.BeginArchivePromptMaintenance(ctx, repoID, now, now.Add(2*time.Minute))
	require.NoError(err)

	require.NoError(d.CommitArchiveInventoryPage(ctx, ArchiveInventoryCommit{
		RepoID: repoID, ItemType: ArchiveItemTypeIssue, RefreshReason: ArchiveRefreshReasonPrompt,
		ScanGeneration: promptState.MaintenanceIssues.Generation,
		Exhausted:      true, Now: now.Add(2 * time.Minute), Issues: []ArchiveInventoryIssue{item},
	}))
	state = archiveItemState(t, d, repoID, ArchiveItemTypeIssue, 1)
	assert.Equal(ArchiveDatasetStatusPending, state.CommentsStatus)
	assert.Equal(ArchiveRefreshReasonPrompt, state.RefreshReason)
	stage, err = d.GetArchiveDatasetStage(ctx, key)
	require.NoError(err)
	assert.Zero(stage.PageCount)
}

func archiveInventoryIssueForTest(repoID int64, number int, updatedAt time.Time) ArchiveInventoryIssue {
	return ArchiveInventoryIssue{
		ProviderItemID: fmt.Sprintf("issue-%d", number),
		CommentsStatus: ArchiveDatasetStatusPending,
		Snapshot: IssueSnapshot{Issue: Issue{
			RepoID: repoID, PlatformID: int64(number), Number: number,
			State: "closed", CreatedAt: updatedAt.Add(-time.Hour),
			UpdatedAt: updatedAt, LastActivityAt: updatedAt,
		}},
	}
}

func TestArchiveInventoryPageCommitUsesScanCursorCompareAndSwap(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	now := archiveTestTime()
	repoID := insertTestRepo(t, d, "acme", "scan-cas")
	require.NoError(d.EnsureDiscoveryArchives(ctx, []int64{repoID}, now))
	require.NoError(d.StartFullArchives(ctx, []int64{repoID}, now))

	pageOne := ArchiveInventoryCommit{
		RepoID: repoID, ItemType: ArchiveItemTypeIssue,
		ScanGeneration: 1, InputCursor: "", NextCursor: "p2", Now: now,
		Issues: []ArchiveInventoryIssue{archiveInventoryIssueForTest(repoID, 1, now)},
	}
	require.NoError(d.CommitArchiveInventoryPage(ctx, pageOne))
	states, err := d.ListArchiveRepoStates(ctx, []int64{repoID})
	require.NoError(err)
	scan := states[0].IssueInventory
	assert.Equal("p2", *scan.NextCursor)
	assert.Equal(1, scan.PageCount)
	assert.Equal(ArchiveScanRunning, scan.Status)

	// Duplicate delivery of the committed page is an idempotent no-op.
	require.NoError(d.CommitArchiveInventoryPage(ctx, pageOne))
	states, err = d.ListArchiveRepoStates(ctx, []int64{repoID})
	require.NoError(err)
	assert.Equal(1, states[0].IssueInventory.PageCount)
	assert.Equal("p2", *states[0].IssueInventory.NextCursor)

	// An unrelated stale cursor is a typed rejection without cursor movement.
	stalePage := pageOne
	stalePage.InputCursor = "bogus"
	var stale *StaleArchiveScanError
	require.ErrorAs(d.CommitArchiveInventoryPage(ctx, stalePage), &stale)

	// A page from before an explicit reset cannot advance the new generation.
	staleGeneration := pageOne
	staleGeneration.InputCursor, staleGeneration.ScanGeneration = "p2", 99
	require.ErrorAs(d.CommitArchiveInventoryPage(ctx, staleGeneration), &stale)

	// An echoed cursor without exhaustion durably blocks the scan.
	echoed := ArchiveInventoryCommit{
		RepoID: repoID, ItemType: ArchiveItemTypeIssue,
		ScanGeneration: 1, InputCursor: "p2", NextCursor: "p2", Now: now,
		Issues: []ArchiveInventoryIssue{archiveInventoryIssueForTest(repoID, 2, now)},
	}
	var blocked *ScanBlockedError
	require.ErrorAs(d.CommitArchiveInventoryPage(ctx, echoed), &blocked)
	assert.Equal("invalid_cursor", blocked.Reason)
	states, err = d.ListArchiveRepoStates(ctx, []int64{repoID})
	require.NoError(err)
	assert.True(states[0].IssueInventory.Blocked())
	require.NotNil(states[0].IssueInventory.LastErrorCode)
	assert.Equal("invalid_cursor", *states[0].IssueInventory.LastErrorCode)
	var echoedItems int
	require.NoError(d.ReadDB().QueryRowContext(ctx, `
		SELECT COUNT(*) FROM middleman_archive_items
		WHERE repo_id = ? AND item_number = 2`, repoID).Scan(&echoedItems))
	assert.Zero(echoedItems, "a rejected page must not commit parent rows")

	// A blocked scan stays blocked until an explicit reset starts a fresh
	// generation from page one.
	valid := ArchiveInventoryCommit{
		RepoID: repoID, ItemType: ArchiveItemTypeIssue,
		ScanGeneration: 1, InputCursor: "p2", Exhausted: true, Now: now,
	}
	require.ErrorAs(d.CommitArchiveInventoryPage(ctx, valid), &blocked)
	require.NoError(d.ResetArchiveRepoScan(ctx, repoID, ArchiveScanIssueInventory))
	restarted := ArchiveInventoryCommit{
		RepoID: repoID, ItemType: ArchiveItemTypeIssue,
		ScanGeneration: 2, InputCursor: "", Exhausted: true, Now: now,
	}
	require.NoError(d.CommitArchiveInventoryPage(ctx, restarted))
	states, err = d.ListArchiveRepoStates(ctx, []int64{repoID})
	require.NoError(err)
	assert.True(states[0].IssueInventory.Complete())
}

func TestArchiveInventoryPageBoundBlocksScan(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	now := archiveTestTime()
	repoID := insertTestRepo(t, d, "acme", "scan-bound")
	require.NoError(d.EnsureDiscoveryArchives(ctx, []int64{repoID}, now))
	require.NoError(d.StartFullArchives(ctx, []int64{repoID}, now))
	_, err := d.WriteDB().ExecContext(ctx, `
		UPDATE middleman_archive_repo_scans
		SET page_count = ?, next_cursor = 'deep', status = 'running'
		WHERE repo_id = ? AND scan = 'issue_inventory'`, maxScanPages, repoID)
	require.NoError(err)

	var blocked *ScanBlockedError
	require.ErrorAs(d.CommitArchiveInventoryPage(ctx, ArchiveInventoryCommit{
		RepoID: repoID, ItemType: ArchiveItemTypeIssue,
		ScanGeneration: 1, InputCursor: "deep", NextCursor: "deeper", Now: now,
		Issues: []ArchiveInventoryIssue{archiveInventoryIssueForTest(repoID, 1, now)},
	}), &blocked)
	assert.Equal("page_bound", blocked.Reason)
	states, err := d.ListArchiveRepoStates(ctx, []int64{repoID})
	require.NoError(err)
	assert.True(states[0].IssueInventory.Blocked())
	require.NotNil(states[0].IssueInventory.LastErrorCode)
	assert.Equal("page_bound", *states[0].IssueInventory.LastErrorCode)
}

func TestArchiveEnsureDiscoveryRejectsMissingReposAtomically(t *testing.T) {
	require := require.New(t)
	d := openTestDB(t)
	repoID := insertTestRepo(t, d, "acme", "widget")

	err := d.EnsureDiscoveryArchives(t.Context(), []int64{repoID, repoID + 1000}, archiveTestTime())
	require.Error(err)
	var missing *ArchiveRepoStateNotFoundError
	require.ErrorAs(err, &missing)
	require.Equal([]int64{repoID + 1000}, missing.RepoIDs)
	states, listErr := d.ListArchiveRepoStates(t.Context(), nil)
	require.NoError(listErr)
	require.Empty(states)
}

func TestArchiveStartFullPromotesDiscoveryAndPreservesResumedProgress(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	now := archiveTestTime()
	repoID := insertTestRepo(t, d, "acme", "widget")
	require.NoError(d.EnsureDiscoveryArchives(ctx, []int64{repoID}, now))
	_, err := d.WriteDB().ExecContext(ctx, `
		UPDATE middleman_archive_repos
		SET maintenance_watermark = ?,
			last_error_code = 'transient', last_error_detail = 'retry me', next_retry_at = ?
		WHERE repo_id = ?`, now.Add(-time.Hour), now.Add(time.Hour), repoID)
	require.NoError(err)
	_, err = d.WriteDB().ExecContext(ctx, `
		UPDATE middleman_archive_repo_scans
		SET next_cursor = 'issues-next', status = 'running', page_count = 1
		WHERE repo_id = ? AND scan = 'issue_inventory'`, repoID)
	require.NoError(err)
	_, err = d.WriteDB().ExecContext(ctx, `
		UPDATE middleman_archive_repo_scans
		SET next_cursor = 'mrs-next', status = 'running', page_count = 1
		WHERE repo_id = ? AND scan = 'merge_request_inventory'`, repoID)
	require.NoError(err)
	insertArchiveItemForTest(t, d, repoID, ArchiveItemTypeIssue, 7, now.Add(-2*time.Hour),
		ArchiveDatasetStatusComplete, ArchiveDatasetStatusNotApplicable, ArchiveDatasetStatusNotApplicable)
	insertArchiveItemForTest(t, d, repoID, ArchiveItemTypeMergeRequest, 8, now.Add(-time.Hour),
		ArchiveDatasetStatusComplete, ArchiveDatasetStatusUnsupported, ArchiveDatasetStatusFailed)
	insertArchiveProgressForTest(t, d, repoID, ArchiveItemTypeIssue, 7, ArchiveDatasetComments, ArchiveDatasetProgressComplete)
	insertArchiveProgressForTest(t, d, repoID, ArchiveItemTypeMergeRequest, 8, ArchiveDatasetComments, ArchiveDatasetProgressComplete)
	insertArchiveProgressForTest(t, d, repoID, ArchiveItemTypeMergeRequest, 8, ArchiveDatasetReviews, ArchiveDatasetProgressUnsupported)
	insertArchiveProgressForTest(t, d, repoID, ArchiveItemTypeMergeRequest, 8, ArchiveDatasetInlineComments, ArchiveDatasetProgressFailed)

	require.NoError(d.StartFullArchives(ctx, []int64{repoID}, now.Add(2*time.Hour)))
	states, err := d.ListArchiveRepoStates(ctx, []int64{repoID})
	require.NoError(err)
	require.Len(states, 1)
	state := states[0]
	assert.Equal(ArchiveCollectionModeFull, state.CollectionMode)
	assert.Equal(ArchiveOperatorStateActive, state.OperatorState)
	assert.Equal(now.Add(2*time.Hour), *state.InitialStartedAt)
	assert.Equal("issues-next", *state.IssueInventory.NextCursor)
	assert.Equal("mrs-next", *state.MergeRequestInventory.NextCursor)
	assert.Nil(state.LastErrorCode)
	assert.Nil(state.LastErrorDetail)
	assert.Nil(state.NextRetryAt)

	assert.Equal(ArchiveDatasetProgressPending,
		archiveProgressStatusForTest(t, d, repoID, ArchiveItemTypeIssue, 7, ArchiveDatasetComments))
	assert.Equal(ArchiveDatasetProgressPending,
		archiveProgressStatusForTest(t, d, repoID, ArchiveItemTypeMergeRequest, 8, ArchiveDatasetComments))
	assert.Equal(ArchiveDatasetProgressUnsupported,
		archiveProgressStatusForTest(t, d, repoID, ArchiveItemTypeMergeRequest, 8, ArchiveDatasetReviews))
	assert.Equal(ArchiveDatasetProgressPending,
		archiveProgressStatusForTest(t, d, repoID, ArchiveItemTypeMergeRequest, 8, ArchiveDatasetInlineComments))

	_, err = d.WriteDB().ExecContext(ctx, `
		UPDATE middleman_archive_repos
		SET operator_state = 'paused', maintenance_succeeded_at = ?
		WHERE repo_id = ?`, now.Add(3*time.Hour), repoID)
	require.NoError(err)
	_, err = d.WriteDB().ExecContext(ctx, `
		UPDATE middleman_archive_repo_scans SET status = 'complete'
		WHERE repo_id = ? AND scan = 'issue_inventory'`, repoID)
	require.NoError(err)
	_, err = d.WriteDB().ExecContext(ctx,
		`UPDATE middleman_archive_dataset_progress SET status = 'complete' WHERE repo_id = ?`, repoID)
	require.NoError(err)
	require.NoError(d.StartFullArchives(ctx, []int64{repoID}, now.Add(5*time.Hour)))
	resumed, err := d.ListArchiveRepoStates(ctx, []int64{repoID})
	require.NoError(err)
	assert.Equal(ArchiveOperatorStateActive, resumed[0].OperatorState)
	assert.True(resumed[0].IssueInventory.Complete())
	assert.Equal(now.Add(3*time.Hour), *resumed[0].MaintenanceSucceededAt)
	assert.Equal(now.Add(2*time.Hour), *resumed[0].InitialStartedAt)
	assert.Nil(resumed[0].LastErrorCode)
	assert.Nil(resumed[0].LastErrorDetail)
	assert.Nil(resumed[0].NextRetryAt)
	assert.Equal(ArchiveDatasetProgressComplete,
		archiveProgressStatusForTest(t, d, repoID, ArchiveItemTypeIssue, 7, ArchiveDatasetComments),
		"a resumed full archive must not requeue satisfied datasets")

	activeUpdatedAt := now.Add(6 * time.Hour)
	activeRetryAt := now.Add(7 * time.Hour)
	_, err = d.WriteDB().ExecContext(ctx, `
		UPDATE middleman_archive_repos
		SET last_error_code = ?, last_error_detail = ?, next_retry_at = ?, updated_at = ?
		WHERE repo_id = ?`,
		ArchiveErrorCodeAuthentication, "credentials rejected", activeRetryAt, activeUpdatedAt, repoID)
	require.NoError(err)
	require.NoError(d.StartFullArchives(ctx, []int64{repoID}, now.Add(8*time.Hour)))
	unchanged, err := d.ListArchiveRepoStates(ctx, []int64{repoID})
	require.NoError(err)
	require.Len(unchanged, 1)
	assert.Equal(ArchiveOperatorStateActive, unchanged[0].OperatorState)
	assert.Equal(string(ArchiveErrorCodeAuthentication), *unchanged[0].LastErrorCode)
	assert.Equal("credentials rejected", *unchanged[0].LastErrorDetail)
	assert.Equal(activeRetryAt, *unchanged[0].NextRetryAt)
	assert.Equal(activeUpdatedAt, unchanged[0].UpdatedAt)
}

func TestArchiveStartAndPauseAreAtomicAndIdempotent(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	now := archiveTestTime()
	firstRepoID := insertTestRepo(t, d, "acme", "one")
	secondRepoID := insertTestRepo(t, d, "acme", "two")
	require.NoError(d.EnsureDiscoveryArchives(ctx, []int64{firstRepoID}, now))

	err := d.StartFullArchives(ctx, []int64{firstRepoID, secondRepoID}, now.Add(time.Hour))
	require.Error(err)
	var missing *ArchiveRepoStateNotFoundError
	require.ErrorAs(err, &missing)
	require.Equal([]int64{secondRepoID}, missing.RepoIDs)
	states, listErr := d.ListArchiveRepoStates(ctx, []int64{firstRepoID})
	require.NoError(listErr)
	assert.Equal(ArchiveCollectionModeDiscovery, states[0].CollectionMode)

	require.NoError(d.EnsureDiscoveryArchives(ctx, []int64{secondRepoID}, now))
	require.NoError(d.StartFullArchives(ctx, []int64{firstRepoID, secondRepoID}, now.Add(time.Hour)))
	require.NoError(d.StartFullArchives(ctx, []int64{secondRepoID, firstRepoID}, now.Add(2*time.Hour)))
	require.NoError(d.PauseArchives(ctx, []int64{firstRepoID, secondRepoID}, now.Add(3*time.Hour)))
	require.NoError(d.PauseArchives(ctx, []int64{secondRepoID, firstRepoID}, now.Add(4*time.Hour)))
	require.NoError(d.StartFullArchives(ctx, nil, now))
	require.NoError(d.PauseArchives(ctx, nil, now))

	states, err = d.ListArchiveRepoStates(ctx, nil)
	require.NoError(err)
	require.Len(states, 2)
	assert.Equal(firstRepoID, states[0].RepoID)
	assert.Equal(secondRepoID, states[1].RepoID)
	assert.Equal(ArchiveOperatorStatePaused, states[0].OperatorState)
	assert.Equal(ArchiveOperatorStatePaused, states[1].OperatorState)
	assert.Equal(now.Add(time.Hour), *states[0].InitialStartedAt)
	assert.Equal(now.Add(time.Hour), *states[1].InitialStartedAt)
}

func TestArchiveClaimItemUsesEligibleDueWorkAndStableOrder(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	now := archiveTestTime()
	firstRepoID := insertTestRepoWithHost(t, d, "acme", "widget", "github.com")
	secondRepoID := insertTestRepoWithHost(t, d, "acme", "widget", "github.example.com")
	unconfiguredRepoID := insertTestRepoWithHost(t, d, "acme", "widget", "git.example.com")
	require.NoError(d.EnsureDiscoveryArchives(ctx, []int64{firstRepoID, secondRepoID, unconfiguredRepoID}, now))
	require.NoError(d.StartFullArchives(ctx, []int64{firstRepoID, secondRepoID, unconfiguredRepoID}, now))

	oldest := now.Add(-4 * time.Hour)
	insertArchiveItemForTest(t, d, unconfiguredRepoID, ArchiveItemTypeIssue, 1, oldest.Add(-time.Hour),
		ArchiveDatasetStatusPending, ArchiveDatasetStatusNotApplicable, ArchiveDatasetStatusNotApplicable)
	insertArchiveProgressForTest(t, d, unconfiguredRepoID, ArchiveItemTypeIssue, 1, ArchiveDatasetComments, ArchiveDatasetProgressPending)
	insertArchiveItemForTest(t, d, firstRepoID, ArchiveItemTypeIssue, 1, oldest,
		ArchiveDatasetStatusPending, ArchiveDatasetStatusNotApplicable, ArchiveDatasetStatusNotApplicable)
	insertArchiveProgressForTest(t, d, firstRepoID, ArchiveItemTypeIssue, 1, ArchiveDatasetLookup, ArchiveDatasetProgressPending)
	insertArchiveProgressForTest(t, d, firstRepoID, ArchiveItemTypeIssue, 1, ArchiveDatasetComments, ArchiveDatasetProgressPending)
	insertArchiveItemForTest(t, d, secondRepoID, ArchiveItemTypeIssue, 1, oldest,
		ArchiveDatasetStatusPending, ArchiveDatasetStatusNotApplicable, ArchiveDatasetStatusNotApplicable)
	insertArchiveProgressForTest(t, d, secondRepoID, ArchiveItemTypeIssue, 1, ArchiveDatasetComments, ArchiveDatasetProgressPending)
	insertArchiveItemForTest(t, d, firstRepoID, ArchiveItemTypeMergeRequest, 1, oldest,
		ArchiveDatasetStatusPending, ArchiveDatasetStatusPending, ArchiveDatasetStatusPending)
	insertArchiveProgressForTest(t, d, firstRepoID, ArchiveItemTypeMergeRequest, 1, ArchiveDatasetComments, ArchiveDatasetProgressPending)
	insertArchiveItemForTest(t, d, firstRepoID, ArchiveItemTypeIssue, 2, oldest.Add(-time.Minute),
		ArchiveDatasetStatusFailed, ArchiveDatasetStatusNotApplicable, ArchiveDatasetStatusNotApplicable)
	insertArchiveProgressForTest(t, d, firstRepoID, ArchiveItemTypeIssue, 2, ArchiveDatasetComments, ArchiveDatasetProgressFailed)
	_, err := d.WriteDB().ExecContext(ctx, `
		UPDATE middleman_archive_dataset_progress
		SET next_retry_at = ?
		WHERE repo_id = ? AND item_type = 'issue' AND item_number = 2`,
		formatDatasetProgressTime(now.Add(time.Hour)), firstRepoID)
	require.NoError(err)
	insertArchiveItemForTest(t, d, firstRepoID, ArchiveItemTypeIssue, 3, oldest.Add(-2*time.Minute),
		ArchiveDatasetStatusComplete, ArchiveDatasetStatusNotApplicable, ArchiveDatasetStatusNotApplicable)
	insertArchiveProgressForTest(t, d, firstRepoID, ArchiveItemTypeIssue, 3, ArchiveDatasetComments, ArchiveDatasetProgressComplete)
	insertArchiveItemForTest(t, d, firstRepoID, ArchiveItemTypeMergeRequest, 4, oldest.Add(-3*time.Minute),
		ArchiveDatasetStatusUnsupported, ArchiveDatasetStatusUnsupported, ArchiveDatasetStatusUnsupported)
	insertArchiveProgressForTest(t, d, firstRepoID, ArchiveItemTypeMergeRequest, 4, ArchiveDatasetComments, ArchiveDatasetProgressUnsupported)
	insertArchiveItemForTest(t, d, firstRepoID, ArchiveItemTypeIssue, 5, oldest.Add(-4*time.Minute),
		ArchiveDatasetStatusPending, ArchiveDatasetStatusNotApplicable, ArchiveDatasetStatusNotApplicable)
	insertArchiveProgressForTest(t, d, firstRepoID, ArchiveItemTypeIssue, 5, ArchiveDatasetComments, ArchiveDatasetProgressPending)
	_, err = d.WriteDB().ExecContext(ctx, `
		UPDATE middleman_archive_items SET lifecycle_state = 'removed_upstream'
		WHERE repo_id = ? AND item_type = 'issue' AND item_number = 5`, firstRepoID)
	require.NoError(err)

	var candidateRepoIDs []int64
	claim, err := d.claimArchiveItem(ctx, ClaimArchiveItemOpts{
		RepoIDs: []int64{secondRepoID, firstRepoID},
		Now:     now,
	}, func(candidates []ArchiveItemWork) {
		for _, candidate := range candidates {
			candidateRepoIDs = append(candidateRepoIDs, candidate.RepoID)
		}
	})
	require.NoError(err)
	require.NotNil(claim)
	assert.Equal([]int64{firstRepoID, secondRepoID}, candidateRepoIDs)
	assert.Equal(firstRepoID, claim.RepoID)
	assert.Equal(ArchiveItemTypeIssue, claim.ItemType)
	assert.Equal(1, claim.ItemNumber)
	require.Len(claim.Datasets, 2)
	assert.Equal(ArchiveDatasetLookup, claim.Datasets[0].Dataset, "lookup runs before child datasets")
	assert.Equal(ArchiveDatasetComments, claim.Datasets[1].Dataset)

	again, err := d.ClaimArchiveItem(ctx, ClaimArchiveItemOpts{
		RepoIDs: []int64{firstRepoID, secondRepoID},
		Now:     now,
	})
	require.NoError(err)
	require.NotNil(again)
	assert.Equal(*claim, *again)

	require.NoError(d.PauseArchives(ctx, []int64{firstRepoID}, now))
	claim, err = d.ClaimArchiveItem(ctx, ClaimArchiveItemOpts{
		RepoIDs: []int64{firstRepoID, secondRepoID},
		Now:     now,
	})
	require.NoError(err)
	require.NotNil(claim)
	assert.Equal(secondRepoID, claim.RepoID)

	_, err = d.WriteDB().ExecContext(ctx, `
		UPDATE middleman_archive_repos SET next_retry_at = ? WHERE repo_id = ?`,
		now.Add(time.Hour), secondRepoID)
	require.NoError(err)
	claim, err = d.ClaimArchiveItem(ctx, ClaimArchiveItemOpts{
		RepoIDs: []int64{firstRepoID, secondRepoID},
		Now:     now,
	})
	require.NoError(err)
	assert.Nil(claim)
}

func TestArchiveClaimItemExcludesDiscoveryAndEmptyEligibility(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	now := archiveTestTime()
	repoID := insertTestRepo(t, d, "acme", "widget")
	require.NoError(d.EnsureDiscoveryArchives(t.Context(), []int64{repoID}, now))
	insertArchiveItemForTest(t, d, repoID, ArchiveItemTypeIssue, 1, now,
		ArchiveDatasetStatusPending, ArchiveDatasetStatusNotApplicable, ArchiveDatasetStatusNotApplicable)
	insertArchiveProgressForTest(t, d, repoID, ArchiveItemTypeIssue, 1, ArchiveDatasetComments, ArchiveDatasetProgressPending)

	claim, err := d.ClaimArchiveItem(t.Context(), ClaimArchiveItemOpts{RepoIDs: []int64{repoID}, Now: now})
	require.NoError(err)
	assert.Nil(claim)
	claim, err = d.ClaimArchiveItem(t.Context(), ClaimArchiveItemOpts{Now: now})
	require.NoError(err)
	assert.Nil(claim)
}

func TestFailDatasetProgressRecordsRetryBookkeepingOnFailableRowsOnly(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	now := archiveTestTime()
	retryAt := now.Add(15 * time.Minute)
	repoID := insertTestRepo(t, d, "acme", "widget")
	require.NoError(d.EnsureDiscoveryArchives(ctx, []int64{repoID}, now))
	require.NoError(d.StartFullArchives(ctx, []int64{repoID}, now))
	insertArchiveItemForTest(t, d, repoID, ArchiveItemTypeMergeRequest, 9, now.Add(-time.Hour),
		ArchiveDatasetStatusPending, ArchiveDatasetStatusComplete, ArchiveDatasetStatusUnsupported)
	insertArchiveProgressForTest(t, d, repoID, ArchiveItemTypeMergeRequest, 9, ArchiveDatasetComments, ArchiveDatasetProgressPending)
	insertArchiveProgressForTest(t, d, repoID, ArchiveItemTypeMergeRequest, 9, ArchiveDatasetReviews, ArchiveDatasetProgressComplete)
	insertArchiveProgressForTest(t, d, repoID, ArchiveItemTypeMergeRequest, 9, ArchiveDatasetInlineComments, ArchiveDatasetProgressUnsupported)

	key := ArchiveDatasetProgressKey{
		RepoID: repoID, ItemType: ArchiveItemTypeMergeRequest, ItemNumber: 9,
		Dataset: ArchiveDatasetComments,
	}
	require.NoError(d.FailDatasetProgress(
		ctx, key, ArchiveErrorCodeTransient, "  provider\nrequest\x00failed  ", &retryAt,
	))
	failed, err := d.GetDatasetProgress(ctx, repoID, ArchiveItemTypeMergeRequest, 9, ArchiveDatasetComments)
	require.NoError(err)
	assert.Equal(ArchiveDatasetProgressFailed, failed.Status)
	assert.Equal(1, failed.AttemptCount)
	require.NotNil(failed.NextRetryAt)
	assert.Equal(retryAt.UTC(), failed.NextRetryAt.UTC())
	require.NotNil(failed.LastErrorCode)
	assert.Equal(string(ArchiveErrorCodeTransient), *failed.LastErrorCode)
	require.NotNil(failed.LastErrorDetail)
	assert.Equal("provider request failed", *failed.LastErrorDetail)

	// Complete and unsupported rows are not failable: retry bookkeeping never
	// reopens satisfied or capability-terminal datasets.
	completeKey := key
	completeKey.Dataset = ArchiveDatasetReviews
	require.NoError(d.FailDatasetProgress(ctx, completeKey, ArchiveErrorCodeTransient, "late failure", &retryAt))
	assert.Equal(ArchiveDatasetProgressComplete,
		archiveProgressStatusForTest(t, d, repoID, ArchiveItemTypeMergeRequest, 9, ArchiveDatasetReviews))
	unsupportedKey := key
	unsupportedKey.Dataset = ArchiveDatasetInlineComments
	require.NoError(d.FailDatasetProgress(ctx, unsupportedKey, ArchiveErrorCodeTransient, "late failure", &retryAt))
	assert.Equal(ArchiveDatasetProgressUnsupported,
		archiveProgressStatusForTest(t, d, repoID, ArchiveItemTypeMergeRequest, 9, ArchiveDatasetInlineComments))

	require.NoError(d.FailDatasetProgress(ctx, key, ArchiveErrorCodeTransient, "again", &retryAt))
	again, err := d.GetDatasetProgress(ctx, repoID, ArchiveItemTypeMergeRequest, 9, ArchiveDatasetComments)
	require.NoError(err)
	assert.Equal(2, again.AttemptCount)
}

func TestArchiveDeriveProgressStatusAndOrderedPhases(t *testing.T) {
	now := archiveTestTime()
	completedAt := now.Add(-time.Hour)
	watermark := now.Add(-30 * time.Minute)
	succeededBeforeWatermark := now.Add(-2 * time.Hour)
	budgetReset := now.Add(15 * time.Minute)
	expiredBudgetReset := now.Add(-time.Minute)
	base := ArchiveRepoState{
		RepoID:                   42,
		CollectionMode:           ArchiveCollectionModeFull,
		OperatorState:            ArchiveOperatorStateActive,
		IssueInventory:           ArchiveScanState{Generation: 1, Status: ArchiveScanComplete},
		MergeRequestInventory:    ArchiveScanState{Generation: 1, Status: ArchiveScanComplete},
		MaintenanceIssues:        ArchiveScanState{Generation: 1, Status: ArchiveScanPending},
		MaintenanceMergeRequests: ArchiveScanState{Generation: 1, Status: ArchiveScanPending},
		InitialCompletedAt:       &completedAt,
	}
	tests := []struct {
		name              string
		mutateState       func(*ArchiveRepoState)
		counts            ArchiveProgressCounts
		wantStatus        ArchiveStatus
		wantPhases        []ArchivePhase
		wantBudgetWaitTil *time.Time
	}{
		{
			name: "interleaved inventories and hydration",
			mutateState: func(state *ArchiveRepoState) {
				state.IssueInventory.Status = ArchiveScanPending
				state.MergeRequestInventory.Status = ArchiveScanPending
				state.InitialCompletedAt = nil
			},
			counts:     ArchiveProgressCounts{ItemCount: 3, PendingItemCount: 1, DueItemCount: 1},
			wantStatus: ArchiveStatusRunning,
			wantPhases: []ArchivePhase{
				ArchivePhaseIssueInventory,
				ArchivePhaseMergeRequestInventory,
				ArchivePhaseHydration,
			},
		},
		{
			name: "maintenance remains gated by hydration",
			mutateState: func(state *ArchiveRepoState) {
				state.MaintenanceWatermark = &watermark
				state.MaintenanceSucceededAt = &succeededBeforeWatermark
				state.InitialCompletedAt = nil
			},
			counts:     ArchiveProgressCounts{ItemCount: 1, PendingItemCount: 1, DueItemCount: 1},
			wantStatus: ArchiveStatusRunning,
			wantPhases: []ArchivePhase{ArchivePhaseHydration},
		},
		{
			name: "prompt maintenance runs after initial work",
			mutateState: func(state *ArchiveRepoState) {
				state.MaintenanceWatermark = &watermark
				state.MaintenanceSucceededAt = &succeededBeforeWatermark
			},
			counts:     ArchiveProgressCounts{ItemCount: 1, CompleteItemCount: 1},
			wantStatus: ArchiveStatusRunning,
			wantPhases: []ArchivePhase{ArchivePhasePromptMaintenance},
		},
		{
			name: "prompt maintenance pending item uses hydration",
			mutateState: func(state *ArchiveRepoState) {
				state.MaintenanceWatermark = &watermark
				state.MaintenanceSucceededAt = &succeededBeforeWatermark
			},
			counts:     ArchiveProgressCounts{ItemCount: 1, PendingItemCount: 1, DueItemCount: 1},
			wantStatus: ArchiveStatusRunning,
			wantPhases: []ArchivePhase{ArchivePhaseHydration, ArchivePhasePromptMaintenance},
		},
		{
			name:       "unsupported terminal work is partial",
			counts:     ArchiveProgressCounts{ItemCount: 1, UnsupportedItemCount: 1},
			wantStatus: ArchiveStatusPartial,
		},
		{
			name:       "inaccessible terminal work is partial",
			counts:     ArchiveProgressCounts{ItemCount: 1, InaccessibleItemCount: 1},
			wantStatus: ArchiveStatusPartial,
		},
		{
			name: "pause takes precedence",
			mutateState: func(state *ArchiveRepoState) {
				state.OperatorState = ArchiveOperatorStatePaused
				state.IssueInventory.Status = ArchiveScanPending
			},
			counts:     ArchiveProgressCounts{PendingItemCount: 1, DueItemCount: 1},
			wantStatus: ArchiveStatusPaused,
		},
		{
			name: "repository authentication failure is blocked",
			mutateState: func(state *ArchiveRepoState) {
				code := string(ArchiveErrorCodeAuthentication)
				state.LastErrorCode = &code
				state.IssueInventory.Status = ArchiveScanPending
			},
			wantStatus: ArchiveStatusBlocked,
			wantPhases: []ArchivePhase{ArchivePhaseIssueInventory},
		},
		{
			name:       "future retry work remains running",
			counts:     ArchiveProgressCounts{ItemCount: 1, FailedItemCount: 1},
			wantStatus: ArchiveStatusRunning,
			wantPhases: []ArchivePhase{ArchivePhaseHydration},
		},
		{
			name: "budget marker with future hydration retry remains running",
			mutateState: func(state *ArchiveRepoState) {
				code := string(ArchiveErrorCodeBudgetExhausted)
				state.LastErrorCode = &code
				state.NextRetryAt = &budgetReset
			},
			counts:            ArchiveProgressCounts{ItemCount: 1, FailedItemCount: 1},
			wantStatus:        ArchiveStatusRunning,
			wantPhases:        []ArchivePhase{ArchivePhaseHydration},
			wantBudgetWaitTil: &budgetReset,
		},
		{
			name: "budget wait before initial completion",
			mutateState: func(state *ArchiveRepoState) {
				code := string(ArchiveErrorCodeBudgetExhausted)
				state.LastErrorCode = &code
				state.NextRetryAt = &budgetReset
				state.IssueInventory.Status = ArchiveScanPending
				state.InitialCompletedAt = nil
			},
			wantStatus:        ArchiveStatusWaitingForBudget,
			wantPhases:        []ArchivePhase{ArchivePhaseIssueInventory},
			wantBudgetWaitTil: &budgetReset,
		},
		{
			name: "expired budget wait resumes pending work",
			mutateState: func(state *ArchiveRepoState) {
				code := string(ArchiveErrorCodeBudgetExhausted)
				state.LastErrorCode = &code
				state.NextRetryAt = &expiredBudgetReset
				state.IssueInventory.Status = ArchiveScanPending
				state.InitialCompletedAt = nil
			},
			wantStatus:        ArchiveStatusRunning,
			wantPhases:        []ArchivePhase{ArchivePhaseIssueInventory},
			wantBudgetWaitTil: &expiredBudgetReset,
		},
		{
			name: "missing initial completion timestamp remains running",
			mutateState: func(state *ArchiveRepoState) {
				state.InitialCompletedAt = nil
			},
			wantStatus: ArchiveStatusRunning,
		},
		{
			name: "successful initial completion is current",
			counts: ArchiveProgressCounts{
				ItemCount: 1, CompleteItemCount: 1,
			},
			wantStatus: ArchiveStatusCurrent,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert := assert.New(t)
			state := base
			if tt.mutateState != nil {
				tt.mutateState(&state)
			}
			got := deriveArchiveProgress(state, tt.counts, now)
			assert.Equal(tt.wantStatus, got.Status)
			assert.Equal(tt.wantPhases, got.ActivePhases)
			assert.Equal(tt.wantBudgetWaitTil, got.BudgetWaitUntil)
			assert.Equal(tt.counts, got.Counts)
			assert.Equal(state.RepoID, got.RepoID)
		})
	}
}

func TestArchiveGetProgressDerivesCountsFromDurableRows(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	now := archiveTestTime()
	firstRepoID := insertTestRepoWithHost(t, d, "acme", "widget", "github.com")
	secondRepoID := insertTestRepoWithHost(t, d, "acme", "widget", "github.example.com")
	require.NoError(d.EnsureDiscoveryArchives(ctx, []int64{firstRepoID, secondRepoID}, now))
	require.NoError(d.StartFullArchives(ctx, []int64{firstRepoID, secondRepoID}, now))
	_, err := d.WriteDB().ExecContext(ctx, `
		UPDATE middleman_archive_repos SET initial_completed_at = ?`, now)
	require.NoError(err)
	_, err = d.WriteDB().ExecContext(ctx, `
		UPDATE middleman_archive_repo_scans SET status = 'complete'
		WHERE scan IN ('issue_inventory', 'merge_request_inventory')`)
	require.NoError(err)
	insertArchiveItemForTest(t, d, firstRepoID, ArchiveItemTypeIssue, 1, now.Add(-3*time.Hour),
		ArchiveDatasetStatusComplete, ArchiveDatasetStatusNotApplicable, ArchiveDatasetStatusNotApplicable)
	insertArchiveProgressForTest(t, d, firstRepoID, ArchiveItemTypeIssue, 1, ArchiveDatasetComments, ArchiveDatasetProgressComplete)
	insertArchiveItemForTest(t, d, firstRepoID, ArchiveItemTypeMergeRequest, 2, now.Add(-2*time.Hour),
		ArchiveDatasetStatusPending, ArchiveDatasetStatusComplete, ArchiveDatasetStatusUnsupported)
	insertArchiveProgressForTest(t, d, firstRepoID, ArchiveItemTypeMergeRequest, 2, ArchiveDatasetComments, ArchiveDatasetProgressPending)
	insertArchiveProgressForTest(t, d, firstRepoID, ArchiveItemTypeMergeRequest, 2, ArchiveDatasetReviews, ArchiveDatasetProgressComplete)
	insertArchiveProgressForTest(t, d, firstRepoID, ArchiveItemTypeMergeRequest, 2, ArchiveDatasetInlineComments, ArchiveDatasetProgressUnsupported)
	insertArchiveItemForTest(t, d, firstRepoID, ArchiveItemTypeIssue, 3, now.Add(-time.Hour),
		ArchiveDatasetStatusFailed, ArchiveDatasetStatusNotApplicable, ArchiveDatasetStatusNotApplicable)
	insertArchiveProgressForTest(t, d, firstRepoID, ArchiveItemTypeIssue, 3, ArchiveDatasetComments, ArchiveDatasetProgressFailed)
	_, err = d.WriteDB().ExecContext(ctx, `
		UPDATE middleman_archive_dataset_progress SET next_retry_at = ?
		WHERE repo_id = ? AND item_type = 'issue' AND item_number = 3`,
		formatDatasetProgressTime(now.Add(time.Hour)), firstRepoID)
	require.NoError(err)
	insertArchiveItemForTest(t, d, secondRepoID, ArchiveItemTypeMergeRequest, 4, now.Add(-time.Hour),
		ArchiveDatasetStatusUnsupported, ArchiveDatasetStatusUnsupported, ArchiveDatasetStatusUnsupported)
	insertArchiveProgressForTest(t, d, secondRepoID, ArchiveItemTypeMergeRequest, 4, ArchiveDatasetComments, ArchiveDatasetProgressUnsupported)
	_, err = d.WriteDB().ExecContext(ctx, `
		UPDATE middleman_archive_items SET lifecycle_state = 'removed_upstream'
		WHERE repo_id = ? AND item_type = 'merge_request' AND item_number = 4`, secondRepoID)
	require.NoError(err)

	selected, err := d.GetArchiveProgress(ctx, ArchiveProgressOpts{RepoIDs: []int64{firstRepoID}, Now: now})
	require.NoError(err)
	require.Len(selected, 1)
	assert.Equal(firstRepoID, selected[0].RepoID)
	assert.Equal(ArchiveStatusRunning, selected[0].Status)
	assert.Equal([]ArchivePhase{ArchivePhaseHydration}, selected[0].ActivePhases)
	assert.Equal(ArchiveProgressCounts{
		ItemCount:            3,
		CompleteItemCount:    1,
		PendingItemCount:     1,
		FailedItemCount:      1,
		UnsupportedItemCount: 1,
		DueItemCount:         1,
	}, selected[0].Counts)

	all, err := d.GetArchiveProgress(ctx, ArchiveProgressOpts{Now: now})
	require.NoError(err)
	require.Len(all, 2)
	assert.Equal(firstRepoID, all[0].RepoID)
	assert.Equal(secondRepoID, all[1].RepoID)
	assert.Equal(ArchiveStatusCurrent, all[1].Status)
	assert.Zero(all[1].Counts.UnsupportedItemCount)
}

func TestArchiveGetProgressUsesOneReadSnapshot(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	now := archiveTestTime()
	repoID := insertTestRepo(t, d, "acme", "widget")
	require.NoError(d.EnsureDiscoveryArchives(ctx, []int64{repoID}, now))
	require.NoError(d.StartFullArchives(ctx, []int64{repoID}, now))
	_, err := d.WriteDB().ExecContext(ctx, `
		UPDATE middleman_archive_repos
		SET initial_completed_at = ?
		WHERE repo_id = ?`, now, repoID)
	require.NoError(err)
	_, err = d.WriteDB().ExecContext(ctx, `
		UPDATE middleman_archive_repo_scans SET status = 'complete'
		WHERE repo_id = ? AND scan IN ('issue_inventory', 'merge_request_inventory')`, repoID)
	require.NoError(err)
	insertArchiveItemForTest(t, d, repoID, ArchiveItemTypeIssue, 1, now,
		ArchiveDatasetStatusPending, ArchiveDatasetStatusNotApplicable, ArchiveDatasetStatusNotApplicable)
	insertArchiveProgressForTest(t, d, repoID, ArchiveItemTypeIssue, 1, ArchiveDatasetComments, ArchiveDatasetProgressPending)

	progress, err := d.getArchiveProgress(ctx, ArchiveProgressOpts{RepoIDs: []int64{repoID}, Now: now}, func() error {
		_, err := d.WriteDB().ExecContext(ctx, `
			UPDATE middleman_archive_dataset_progress
			SET status = 'complete'
			WHERE repo_id = ? AND item_type = 'issue' AND item_number = 1`, repoID)
		return err
	})
	require.NoError(err)
	require.Len(progress, 1)
	assert.Equal(ArchiveStatusRunning, progress[0].Status)
	assert.Equal(1, progress[0].Counts.PendingItemCount)
	assert.Zero(progress[0].Counts.CompleteItemCount)

	after, err := d.GetArchiveProgress(ctx, ArchiveProgressOpts{RepoIDs: []int64{repoID}, Now: now})
	require.NoError(err)
	require.Len(after, 1)
	assert.Equal(ArchiveStatusCurrent, after[0].Status)
	assert.Zero(after[0].Counts.PendingItemCount)
	assert.Equal(1, after[0].Counts.CompleteItemCount)
}

func TestArchiveDBBoundariesNormalizeTimestampsToUTC(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	now, err := time.Parse(time.RFC3339, "2026-07-13T17:30:00+05:30")
	require.NoError(err)
	repoID := insertTestRepo(t, d, "acme", "widget")

	require.NoError(d.EnsureDiscoveryArchives(ctx, []int64{repoID}, now))
	states, err := d.ListArchiveRepoStates(ctx, []int64{repoID})
	require.NoError(err)
	require.Len(states, 1)
	assert.Equal(now.UTC(), states[0].CreatedAt)
	assert.Equal(time.UTC, states[0].CreatedAt.Location())

	startAt := now.Add(time.Minute)
	require.NoError(d.StartFullArchives(ctx, []int64{repoID}, startAt))
	states, err = d.ListArchiveRepoStates(ctx, []int64{repoID})
	require.NoError(err)
	require.NotNil(states[0].InitialStartedAt)
	assert.Equal(startAt.UTC(), *states[0].InitialStartedAt)
	assert.Equal(time.UTC, states[0].InitialStartedAt.Location())

	pauseAt := now.Add(2 * time.Minute)
	require.NoError(d.PauseArchives(ctx, []int64{repoID}, pauseAt))
	states, err = d.ListArchiveRepoStates(ctx, []int64{repoID})
	require.NoError(err)
	assert.Equal(pauseAt.UTC(), states[0].UpdatedAt)
	assert.Equal(time.UTC, states[0].UpdatedAt.Location())
	require.NoError(d.StartFullArchives(ctx, []int64{repoID}, now.Add(3*time.Minute)))

	insertArchiveItemForTest(t, d, repoID, ArchiveItemTypeIssue, 1, now.UTC(),
		ArchiveDatasetStatusPending, ArchiveDatasetStatusNotApplicable, ArchiveDatasetStatusNotApplicable)
	insertArchiveProgressForTest(t, d, repoID, ArchiveItemTypeIssue, 1, ArchiveDatasetComments, ArchiveDatasetProgressPending)
	retryAt := now.Add(4 * time.Minute)
	require.NoError(d.FailDatasetProgress(ctx, ArchiveDatasetProgressKey{
		RepoID: repoID, ItemType: ArchiveItemTypeIssue, ItemNumber: 1,
		Dataset: ArchiveDatasetComments,
	}, ArchiveErrorCodeTransient, "retry later", &retryAt))
	failedProgress, err := d.GetDatasetProgress(ctx, repoID, ArchiveItemTypeIssue, 1, ArchiveDatasetComments)
	require.NoError(err)
	require.NotNil(failedProgress.NextRetryAt)
	assert.Equal(retryAt.UTC(), failedProgress.NextRetryAt.UTC())
	assert.Equal(time.UTC, failedProgress.NextRetryAt.Location())

	claimAt := retryAt.Add(time.Second)
	claim, err := d.ClaimArchiveItem(ctx, ClaimArchiveItemOpts{RepoIDs: []int64{repoID}, Now: claimAt})
	require.NoError(err)
	require.NotNil(claim)
	assert.Equal(1, claim.ItemNumber)
	_, err = d.WriteDB().ExecContext(ctx, `
		UPDATE middleman_archive_repos
		SET initial_completed_at = ?
		WHERE repo_id = ?`, now.UTC(), repoID)
	require.NoError(err)
	_, err = d.WriteDB().ExecContext(ctx, `
		UPDATE middleman_archive_repo_scans SET status = 'complete'
		WHERE repo_id = ? AND scan IN ('issue_inventory', 'merge_request_inventory')`, repoID)
	require.NoError(err)
	progress, err := d.GetArchiveProgress(ctx, ArchiveProgressOpts{RepoIDs: []int64{repoID}, Now: claimAt})
	require.NoError(err)
	require.Len(progress, 1)
	assert.Equal(1, progress[0].Counts.DueItemCount)
}

func TestMarkArchiveItemHydratedSetsStatusesAndClearsRetryState(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	now := archiveTestTime()
	repoID := insertTestRepo(t, d, "acme", "widget")
	require.NoError(d.EnsureDiscoveryArchives(ctx, []int64{repoID}, now))
	require.NoError(d.StartFullArchives(ctx, []int64{repoID}, now))
	mirrored := now.Add(-30 * time.Minute)
	insertArchiveItemForTest(t, d, repoID, ArchiveItemTypeMergeRequest, 7, mirrored,
		ArchiveDatasetStatusPending, ArchiveDatasetStatusPending, ArchiveDatasetStatusPending)
	_, err := d.UpsertMergeRequest(ctx, &MergeRequest{
		RepoID: repoID, PlatformID: 7, PlatformExternalID: "mr-7", Number: 7,
		Title: "MR 7", State: MergeRequestStateOpen,
		CreatedAt: mirrored.Add(-time.Hour), UpdatedAt: mirrored, LastActivityAt: mirrored,
	})
	require.NoError(err)
	parent, err := d.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 7)
	require.NoError(err)
	require.NotNil(parent)
	retryAt := now.Add(time.Minute)
	require.NoError(d.FailArchiveItem(ctx, ArchiveItemFailure{
		RepoID: repoID, ItemType: ArchiveItemTypeMergeRequest, ItemNumber: 7,
		Datasets: []ArchiveDataset{ArchiveDatasetComments},
		Code:     "upstream_error", Detail: "boom",
		NextRetryAt: &retryAt,
	}))

	require.NoError(d.MarkArchiveItemHydrated(ctx, ArchiveItemHydration{
		RepoID: repoID, ItemType: ArchiveItemTypeMergeRequest, ItemNumber: 7,
		DomainRevision:    parent.SnapshotRevision,
		CommentsStatus:    ArchiveDatasetStatusComplete,
		ReviewsStatus:     ArchiveDatasetStatusComplete,
		InlineStatus:      ArchiveDatasetStatusUnsupported,
		MirroredUpdatedAt: mirrored, HydratedAt: now,
	}))

	item := archiveItemState(t, d, repoID, ArchiveItemTypeMergeRequest, 7)
	assert.Equal(ArchiveDatasetStatusComplete, item.CommentsStatus)
	assert.Equal(ArchiveDatasetStatusComplete, item.ReviewsStatus)
	assert.Equal(ArchiveDatasetStatusUnsupported, item.InlineCommentsStatus)
	require.NotNil(item.MirroredProviderUpdatedAt)
	assert.True(item.MirroredProviderUpdatedAt.Equal(mirrored))
	require.NotNil(item.HydratedAt)
	assert.True(item.HydratedAt.Equal(now))
	assert.Zero(item.AttemptCount)
	assert.Nil(item.NextRetryAt)
	assert.Empty(item.LastErrorCode)
	assert.Empty(item.LastErrorDetail)

	err = d.MarkArchiveItemHydrated(ctx, ArchiveItemHydration{
		RepoID: repoID, ItemType: ArchiveItemTypeIssue, ItemNumber: 99,
		DomainRevision:    1,
		CommentsStatus:    ArchiveDatasetStatusComplete,
		ReviewsStatus:     ArchiveDatasetStatusNotApplicable,
		InlineStatus:      ArchiveDatasetStatusNotApplicable,
		MirroredUpdatedAt: mirrored, HydratedAt: now,
	})
	require.Error(err)
}

func TestMarkArchiveItemHydratedRequeuesEqualTimestampRevisionLoss(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := openTestDB(t)
	ctx := t.Context()
	now := archiveTestTime()
	repoID := insertTestRepo(t, database, "acme", "finalize-revision-race")
	require.NoError(database.EnsureDiscoveryArchives(ctx, []int64{repoID}, now))
	require.NoError(database.StartFullArchives(ctx, []int64{repoID}, now))
	insertArchiveItemForTest(t, database, repoID, ArchiveItemTypeIssue, 1, now,
		ArchiveDatasetStatusPending, ArchiveDatasetStatusNotApplicable, ArchiveDatasetStatusNotApplicable)
	_, revision, accepted, err := database.UpsertArchiveIssueSnapshot(ctx, &Issue{
		RepoID: repoID, PlatformID: 1, Number: 1, State: "closed",
		CreatedAt: now.Add(-time.Hour), UpdatedAt: now, LastActivityAt: now,
	})
	require.NoError(err)
	require.True(accepted)
	require.NoError(database.MarkArchiveItemHydrated(ctx, ArchiveItemHydration{
		RepoID: repoID, ItemType: ArchiveItemTypeIssue, ItemNumber: 1,
		DomainRevision: revision, CommentsStatus: ArchiveDatasetStatusComplete,
		ReviewsStatus:     ArchiveDatasetStatusNotApplicable,
		InlineStatus:      ArchiveDatasetStatusNotApplicable,
		MirroredUpdatedAt: now, HydratedAt: now,
	}))
	_, currentRevision, accepted, err := database.UpsertIssueSnapshotWithLabels(ctx, &Issue{
		RepoID: repoID, PlatformID: 1, Number: 1, State: "closed", Title: "winner",
		CreatedAt: now.Add(-time.Hour), UpdatedAt: now, LastActivityAt: now,
	})
	require.NoError(err)
	require.True(accepted)
	assert.Greater(currentRevision, revision)

	require.NoError(database.MarkArchiveItemHydrated(ctx, ArchiveItemHydration{
		RepoID: repoID, ItemType: ArchiveItemTypeIssue, ItemNumber: 1,
		DomainRevision: revision, CommentsStatus: ArchiveDatasetStatusComplete,
		ReviewsStatus:     ArchiveDatasetStatusNotApplicable,
		InlineStatus:      ArchiveDatasetStatusNotApplicable,
		MirroredUpdatedAt: now, HydratedAt: now.Add(time.Minute),
	}))
	item := archiveItemState(t, database, repoID, ArchiveItemTypeIssue, 1)
	assert.Equal(ArchiveDatasetStatusPending, item.CommentsStatus)
	assert.Nil(item.HydratedAt)
}

func TestMarkArchiveItemTerminalLeavesDomainRowIntact(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	now := archiveTestTime()
	repoID := insertTestRepo(t, d, "acme", "widget")
	require.NoError(d.EnsureDiscoveryArchives(ctx, []int64{repoID}, now))
	require.NoError(d.StartFullArchives(ctx, []int64{repoID}, now))
	issueID, err := d.UpsertIssue(ctx, &Issue{
		RepoID: repoID, PlatformID: 900, Number: 4,
		URL:   "https://github.com/acme/widget/issues/4",
		Title: "keep this title", Author: "alice", State: "open",
		Body:      "keep this body",
		CreatedAt: now.Add(-2 * time.Hour), UpdatedAt: now.Add(-time.Hour),
		LastActivityAt: now.Add(-time.Hour),
	})
	require.NoError(err)
	insertArchiveItemForTest(t, d, repoID, ArchiveItemTypeIssue, 4, now.Add(-time.Hour),
		ArchiveDatasetStatusPending, ArchiveDatasetStatusNotApplicable, ArchiveDatasetStatusNotApplicable)
	key := ArchiveDatasetKey{RepoID: repoID, ItemType: ArchiveItemTypeIssue, ItemNumber: 4,
		Dataset: ArchiveDatasetComments, SnapshotUpdatedAt: now.Add(-time.Hour),
		DomainRevision: archiveParentRevisionForTest(t, d, repoID, ArchiveItemTypeIssue, 4)}
	_, err = d.CommitArchiveDatasetPage(ctx, ArchiveDatasetPage{ArchiveDatasetKey: key,
		NextCursor: "c2", Payload: []byte(`[]`), MaxPages: 2, MaxRecords: 1, MaxBytes: 16, Now: now})
	require.NoError(err)

	require.NoError(d.MarkArchiveItemTerminal(ctx, ArchiveItemTerminal{
		RepoID: repoID, ItemType: ArchiveItemTypeIssue, ItemNumber: 4,
		Lifecycle: ArchiveLifecycleStateRemovedUpstream,
		ErrorCode: "", ErrorDetail: "", At: now,
	}))

	item := archiveItemState(t, d, repoID, ArchiveItemTypeIssue, 4)
	assert.Equal(ArchiveLifecycleStateRemovedUpstream, item.LifecycleState)
	assert.Nil(item.NextRetryAt)
	stage, err := d.GetArchiveDatasetStage(ctx, key)
	require.NoError(err)
	assert.Zero(stage.PageCount)

	var title, body string
	err = d.ReadDB().QueryRowContext(ctx,
		`SELECT title, body FROM middleman_issues WHERE id = ?`, issueID,
	).Scan(&title, &body)
	require.NoError(err)
	assert.Equal("keep this title", title)
	assert.Equal("keep this body", body)

	require.Error(d.MarkArchiveItemTerminal(ctx, ArchiveItemTerminal{
		RepoID: repoID, ItemType: ArchiveItemTypeIssue, ItemNumber: 4,
		Lifecycle: ArchiveLifecycleStateActive, At: now,
	}))
	require.Error(d.MarkArchiveItemTerminal(ctx, ArchiveItemTerminal{
		RepoID: repoID, ItemType: ArchiveItemTypeIssue, ItemNumber: 99,
		Lifecycle: ArchiveLifecycleStateInaccessible, At: now,
	}))
}

func archiveParentRevisionForTest(
	t *testing.T,
	database *DB,
	repoID int64,
	itemType ArchiveItemType,
	number int,
) int64 {
	t.Helper()
	table := "middleman_issues"
	if itemType == ArchiveItemTypeMergeRequest {
		table = "middleman_merge_requests"
	}
	var revision int64
	require.NoError(t, database.ReadDB().QueryRowContext(t.Context(), fmt.Sprintf(`
		SELECT snapshot_revision FROM %s WHERE repo_id = ? AND number = ?`, table),
		repoID, number,
	).Scan(&revision))
	require.Positive(t, revision)
	return revision
}

func archiveItemState(t *testing.T, database *DB, repoID int64, itemType ArchiveItemType, number int) ArchiveItemState {
	t.Helper()
	row := database.ReadDB().QueryRowContext(t.Context(), `
		SELECT repo_id, item_type, item_number, provider_item_id,
			provider_created_at, provider_updated_at, lifecycle_state,
			refresh_reason,
			comments_status, reviews_status, inline_comments_status,
			mirrored_provider_updated_at, attempt_count, next_retry_at,
			last_error_code, last_error_detail, hydrated_at
		FROM middleman_archive_items
		WHERE repo_id = ? AND item_type = ? AND item_number = ?`, repoID, itemType, number)
	var state ArchiveItemState
	var mirroredAt, nextRetryAt, hydratedAt sql.NullTime
	var code, detail sql.NullString
	require.NoError(t, row.Scan(
		&state.RepoID, &state.ItemType, &state.ItemNumber, &state.ProviderItemID,
		&state.ProviderCreatedAt, &state.ProviderUpdatedAt, &state.LifecycleState,
		&state.RefreshReason,
		&state.CommentsStatus, &state.ReviewsStatus, &state.InlineCommentsStatus,
		&mirroredAt, &state.AttemptCount, &nextRetryAt, &code, &detail, &hydratedAt,
	))
	if mirroredAt.Valid {
		state.MirroredProviderUpdatedAt = &mirroredAt.Time
	}
	if nextRetryAt.Valid {
		state.NextRetryAt = &nextRetryAt.Time
	}
	if hydratedAt.Valid {
		state.HydratedAt = &hydratedAt.Time
	}
	if code.Valid {
		state.LastErrorCode = &code.String
	}
	if detail.Valid {
		state.LastErrorDetail = &detail.String
	}
	return state
}
