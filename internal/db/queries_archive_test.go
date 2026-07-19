package db

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func archiveTestTime() time.Time {
	return time.Date(2026, time.July, 13, 12, 0, 0, 0, time.UTC)
}

func insertArchiveItemForTest(t *testing.T, d *DB, repoID int64, itemType ArchiveItemType, number int, createdAt time.Time) {
	t.Helper()
	_, err := d.WriteDB().ExecContext(t.Context(), `
		INSERT INTO middleman_archive_items (
			repo_id, item_type, item_number, provider_item_id,
			provider_created_at, provider_updated_at, lifecycle_state
		) VALUES (?, ?, ?, ?, ?, ?, 'active')`,
		repoID, itemType, number, string(itemType)+"-id-"+time.Unix(int64(number), 0).String(),
		createdAt, createdAt,
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

func archiveInventoryItemForTest(number int, updatedAt time.Time) ArchiveInventoryItem {
	return ArchiveInventoryItem{
		Number: number, ProviderItemID: fmt.Sprintf("issue-%d", number),
		ProviderCreatedAt: updatedAt.Add(-time.Hour), ProviderUpdatedAt: updatedAt,
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
		Items: []ArchiveInventoryItem{archiveInventoryItemForTest(1, now)},
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

	// A mismatched generation cannot advance the scan.
	staleGeneration := pageOne
	staleGeneration.InputCursor, staleGeneration.ScanGeneration = "p2", 99
	require.ErrorAs(d.CommitArchiveInventoryPage(ctx, staleGeneration), &stale)

	// An echoed cursor without exhaustion durably blocks the scan.
	echoed := ArchiveInventoryCommit{
		RepoID: repoID, ItemType: ArchiveItemTypeIssue,
		ScanGeneration: 1, InputCursor: "p2", NextCursor: "p2", Now: now,
		Items: []ArchiveInventoryItem{archiveInventoryItemForTest(2, now)},
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

	// A blocked scan rejects later pages.
	valid := ArchiveInventoryCommit{
		RepoID: repoID, ItemType: ArchiveItemTypeIssue,
		ScanGeneration: 1, InputCursor: "p2", Exhausted: true, Now: now,
	}
	require.ErrorAs(d.CommitArchiveInventoryPage(ctx, valid), &blocked)
}

func TestArchiveInventoryCompletedScanRejectsStaleDeliveries(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	now := archiveTestTime()
	repoID := insertTestRepo(t, d, "acme", "complete-cas")
	require.NoError(d.EnsureDiscoveryArchives(ctx, []int64{repoID}, now))
	require.NoError(d.StartFullArchives(ctx, []int64{repoID}, now))

	pageOne := ArchiveInventoryCommit{
		RepoID: repoID, ItemType: ArchiveItemTypeIssue,
		ScanGeneration: 1, InputCursor: "", NextCursor: "p2", Now: now,
		Items: []ArchiveInventoryItem{archiveInventoryItemForTest(1, now)},
	}
	require.NoError(d.CommitArchiveInventoryPage(ctx, pageOne))
	finalPage := ArchiveInventoryCommit{
		RepoID: repoID, ItemType: ArchiveItemTypeIssue,
		ScanGeneration: 1, InputCursor: "p2", Exhausted: true, Now: now,
		Items: []ArchiveInventoryItem{archiveInventoryItemForTest(2, now)},
	}
	require.NoError(d.CommitArchiveInventoryPage(ctx, finalPage))
	states, err := d.ListArchiveRepoStates(ctx, []int64{repoID})
	require.NoError(err)
	require.True(states[0].IssueInventory.Complete())

	itemUpdatedAt := func(number int) time.Time {
		t.Helper()
		var updatedAt time.Time
		require.NoError(d.ReadDB().QueryRowContext(ctx, `
			SELECT provider_updated_at FROM middleman_archive_items
			WHERE repo_id = ? AND item_type = 'issue' AND item_number = ?`,
			repoID, number).Scan(&updatedAt))
		return updatedAt
	}
	before := itemUpdatedAt(1)

	// Generation and cursor validation runs before a completed-scan replay can
	// update inventory state.
	newer := archiveInventoryItemForTest(1, now.Add(time.Hour))
	staleGeneration := ArchiveInventoryCommit{
		RepoID: repoID, ItemType: ArchiveItemTypeIssue,
		ScanGeneration: 99, InputCursor: "", NextCursor: "p2", Now: now.Add(time.Minute),
		Items: []ArchiveInventoryItem{newer},
	}
	var stale *StaleArchiveScanError
	require.ErrorAs(d.CommitArchiveInventoryPage(ctx, staleGeneration), &stale)
	assert.Equal(before, itemUpdatedAt(1), "stale generation must not mutate inventory")

	// A same-generation delivery with an unrelated cursor is equally stale.
	staleCursor := staleGeneration
	staleCursor.ScanGeneration, staleCursor.InputCursor = 1, "bogus"
	require.ErrorAs(d.CommitArchiveInventoryPage(ctx, staleCursor), &stale)
	assert.Equal(before, itemUpdatedAt(1), "stale cursor must not mutate inventory")

	// The exact final page (same generation and input cursor) is the one
	// permitted replay: an idempotent no-op that writes nothing.
	replay := finalPage
	replay.Items = []ArchiveInventoryItem{archiveInventoryItemForTest(2, now.Add(time.Hour))}
	require.NoError(d.CommitArchiveInventoryPage(ctx, replay))
	assert.Equal(before, itemUpdatedAt(2), "a final-page replay writes nothing")
	states, err = d.ListArchiveRepoStates(ctx, []int64{repoID})
	require.NoError(err)
	assert.True(states[0].IssueInventory.Complete())
	assert.Equal(2, states[0].IssueInventory.PageCount)
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
		Items: []ArchiveInventoryItem{archiveInventoryItemForTest(1, now)},
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
	insertArchiveItemForTest(t, d, repoID, ArchiveItemTypeIssue, 7, now.Add(-2*time.Hour))
	insertArchiveItemForTest(t, d, repoID, ArchiveItemTypeMergeRequest, 8, now.Add(-time.Hour))
	insertArchiveProgressForTest(t, d, repoID, ArchiveItemTypeIssue, 7, ArchiveDatasetLookup, ArchiveDatasetProgressComplete)
	insertArchiveProgressForTest(t, d, repoID, ArchiveItemTypeMergeRequest, 8, ArchiveDatasetLookup, ArchiveDatasetProgressFailed)

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
		archiveProgressStatusForTest(t, d, repoID, ArchiveItemTypeIssue, 7, ArchiveDatasetLookup))
	assert.Equal(ArchiveDatasetProgressPending,
		archiveProgressStatusForTest(t, d, repoID, ArchiveItemTypeMergeRequest, 8, ArchiveDatasetLookup))

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
		archiveProgressStatusForTest(t, d, repoID, ArchiveItemTypeIssue, 7, ArchiveDatasetLookup),
		"a resumed full archive must not requeue satisfied items")

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
	insertArchiveItemForTest(t, d, unconfiguredRepoID, ArchiveItemTypeIssue, 1, oldest.Add(-time.Hour))
	insertArchiveProgressForTest(t, d, unconfiguredRepoID, ArchiveItemTypeIssue, 1, ArchiveDatasetLookup, ArchiveDatasetProgressPending)
	insertArchiveItemForTest(t, d, firstRepoID, ArchiveItemTypeIssue, 1, oldest)
	insertArchiveProgressForTest(t, d, firstRepoID, ArchiveItemTypeIssue, 1, ArchiveDatasetLookup, ArchiveDatasetProgressPending)
	insertArchiveItemForTest(t, d, secondRepoID, ArchiveItemTypeIssue, 1, oldest)
	insertArchiveProgressForTest(t, d, secondRepoID, ArchiveItemTypeIssue, 1, ArchiveDatasetLookup, ArchiveDatasetProgressPending)
	insertArchiveItemForTest(t, d, firstRepoID, ArchiveItemTypeMergeRequest, 1, oldest)
	insertArchiveProgressForTest(t, d, firstRepoID, ArchiveItemTypeMergeRequest, 1, ArchiveDatasetLookup, ArchiveDatasetProgressPending)
	insertArchiveItemForTest(t, d, firstRepoID, ArchiveItemTypeIssue, 2, oldest.Add(-time.Minute))
	insertArchiveProgressForTest(t, d, firstRepoID, ArchiveItemTypeIssue, 2, ArchiveDatasetLookup, ArchiveDatasetProgressFailed)
	_, err := d.WriteDB().ExecContext(ctx, `
		UPDATE middleman_archive_dataset_progress
		SET next_retry_at = ?
		WHERE repo_id = ? AND item_type = 'issue' AND item_number = 2`,
		formatDatasetProgressTime(now.Add(time.Hour)), firstRepoID)
	require.NoError(err)
	insertArchiveItemForTest(t, d, firstRepoID, ArchiveItemTypeIssue, 3, oldest.Add(-2*time.Minute))
	insertArchiveProgressForTest(t, d, firstRepoID, ArchiveItemTypeIssue, 3, ArchiveDatasetLookup, ArchiveDatasetProgressComplete)
	insertArchiveItemForTest(t, d, firstRepoID, ArchiveItemTypeMergeRequest, 4, oldest.Add(-3*time.Minute))
	insertArchiveProgressForTest(t, d, firstRepoID, ArchiveItemTypeMergeRequest, 4, ArchiveDatasetLookup, ArchiveDatasetProgressUnsupported)
	insertArchiveItemForTest(t, d, firstRepoID, ArchiveItemTypeIssue, 5, oldest.Add(-4*time.Minute))
	insertArchiveProgressForTest(t, d, firstRepoID, ArchiveItemTypeIssue, 5, ArchiveDatasetLookup, ArchiveDatasetProgressPending)
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

func TestArchivePromptRediscoveryMakesTerminalItemClaimable(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := openTestDB(t)
	ctx := t.Context()
	now := archiveTestTime()
	repoID := insertTestRepo(t, database, "acme", "restored")
	require.NoError(database.EnsureDiscoveryArchives(ctx, []int64{repoID}, now))
	require.NoError(database.StartFullArchives(ctx, []int64{repoID}, now))
	item := archiveInventoryItemForTest(7, now)
	require.NoError(database.CommitArchiveInventoryPage(ctx, ArchiveInventoryCommit{
		RepoID: repoID, ItemType: ArchiveItemTypeIssue,
		RefreshReason: ArchiveRefreshReasonInitial, ScanGeneration: 1,
		Exhausted: true, Items: []ArchiveInventoryItem{item}, Now: now,
	}))
	require.NoError(database.CommitArchiveInventoryPage(ctx, ArchiveInventoryCommit{
		RepoID: repoID, ItemType: ArchiveItemTypeMergeRequest,
		RefreshReason: ArchiveRefreshReasonInitial, ScanGeneration: 1,
		Exhausted: true, Now: now,
	}))
	progress, err := database.GetDatasetProgress(
		ctx, repoID, ArchiveItemTypeIssue, item.Number, ArchiveDatasetLookup,
	)
	require.NoError(err)
	require.NoError(database.CommitArchiveItemSync(ctx, ArchiveItemSyncCommit{
		RepoID: repoID, ItemType: ArchiveItemTypeIssue, ItemNumber: item.Number,
		ScanGeneration: progress.ScanGeneration, Outcome: ArchiveLookupRemoved, Now: now.Add(time.Minute),
	}))
	claim, err := database.ClaimArchiveItem(ctx, ClaimArchiveItemOpts{RepoIDs: []int64{repoID}, Now: now.Add(2 * time.Minute)})
	require.NoError(err)
	assert.Nil(claim)
	state, err := database.BeginArchivePromptMaintenance(
		ctx, repoID, now, now.Add(3*time.Minute),
	)
	require.NoError(err)

	require.NoError(database.CommitArchiveInventoryPage(ctx, ArchiveInventoryCommit{
		RepoID: repoID, ItemType: ArchiveItemTypeIssue,
		RefreshReason: ArchiveRefreshReasonPrompt, ScanGeneration: state.MaintenanceIssues.Generation,
		Exhausted: true, Items: []ArchiveInventoryItem{item}, Now: now.Add(3 * time.Minute),
	}))
	refreshed, err := database.GetDatasetProgress(
		ctx, repoID, ArchiveItemTypeIssue, item.Number, ArchiveDatasetLookup,
	)
	require.NoError(err)
	assert.Equal(ArchiveDatasetProgressPending, refreshed.Status)
	assert.Greater(refreshed.ScanGeneration, progress.ScanGeneration)
	claim, err = database.ClaimArchiveItem(ctx, ClaimArchiveItemOpts{
		RepoIDs: []int64{repoID}, Now: now.Add(4 * time.Minute),
	})
	require.NoError(err)
	require.NotNil(claim)
	assert.Equal(item.Number, claim.ItemNumber)
}

func TestArchiveClaimItemExcludesDiscoveryAndEmptyEligibility(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	now := archiveTestTime()
	repoID := insertTestRepo(t, d, "acme", "widget")
	require.NoError(d.EnsureDiscoveryArchives(t.Context(), []int64{repoID}, now))
	insertArchiveItemForTest(t, d, repoID, ArchiveItemTypeIssue, 1, now)
	insertArchiveProgressForTest(t, d, repoID, ArchiveItemTypeIssue, 1, ArchiveDatasetLookup, ArchiveDatasetProgressPending)

	claim, err := d.ClaimArchiveItem(t.Context(), ClaimArchiveItemOpts{RepoIDs: []int64{repoID}, Now: now})
	require.NoError(err)
	assert.Nil(claim)
	claim, err = d.ClaimArchiveItem(t.Context(), ClaimArchiveItemOpts{Now: now})
	require.NoError(err)
	assert.Nil(claim)
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
	insertArchiveItemForTest(t, d, firstRepoID, ArchiveItemTypeIssue, 1, now.Add(-3*time.Hour))
	insertArchiveProgressForTest(t, d, firstRepoID, ArchiveItemTypeIssue, 1, ArchiveDatasetLookup, ArchiveDatasetProgressComplete)
	insertArchiveItemForTest(t, d, firstRepoID, ArchiveItemTypeMergeRequest, 2, now.Add(-2*time.Hour))
	insertArchiveProgressForTest(t, d, firstRepoID, ArchiveItemTypeMergeRequest, 2, ArchiveDatasetLookup, ArchiveDatasetProgressPending)
	insertArchiveItemForTest(t, d, firstRepoID, ArchiveItemTypeIssue, 3, now.Add(-time.Hour))
	insertArchiveProgressForTest(t, d, firstRepoID, ArchiveItemTypeIssue, 3, ArchiveDatasetLookup, ArchiveDatasetProgressFailed)
	_, err = d.WriteDB().ExecContext(ctx, `
		UPDATE middleman_archive_dataset_progress SET next_retry_at = ?
		WHERE repo_id = ? AND item_type = 'issue' AND item_number = 3`,
		formatDatasetProgressTime(now.Add(time.Hour)), firstRepoID)
	require.NoError(err)
	insertArchiveItemForTest(t, d, secondRepoID, ArchiveItemTypeMergeRequest, 4, now.Add(-time.Hour))
	insertArchiveProgressForTest(t, d, secondRepoID, ArchiveItemTypeMergeRequest, 4, ArchiveDatasetLookup, ArchiveDatasetProgressComplete)
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
		ItemCount:         3,
		CompleteItemCount: 1,
		PendingItemCount:  1,
		FailedItemCount:   1,
		DueItemCount:      1,
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
	insertArchiveItemForTest(t, d, repoID, ArchiveItemTypeIssue, 1, now)
	insertArchiveProgressForTest(t, d, repoID, ArchiveItemTypeIssue, 1, ArchiveDatasetLookup, ArchiveDatasetProgressPending)

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

	insertArchiveItemForTest(t, d, repoID, ArchiveItemTypeIssue, 1, now.UTC())
	insertArchiveProgressForTest(t, d, repoID, ArchiveItemTypeIssue, 1, ArchiveDatasetLookup, ArchiveDatasetProgressPending)
	retryAt := now.Add(4 * time.Minute)
	itemProgress, err := d.GetDatasetProgress(ctx, repoID, ArchiveItemTypeIssue, 1, ArchiveDatasetLookup)
	require.NoError(err)
	require.NoError(d.FailArchiveItemSync(ctx, ArchiveItemSyncCommit{
		RepoID: repoID, ItemType: ArchiveItemTypeIssue, ItemNumber: 1,
		ScanGeneration: itemProgress.ScanGeneration, ErrorDetail: "retry later", Now: retryAt,
	}, ArchiveErrorCodeTransient, &retryAt, false))
	failedProgress, err := d.GetDatasetProgress(ctx, repoID, ArchiveItemTypeIssue, 1, ArchiveDatasetLookup)
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

func TestScanScopedRepositoryFailureIsClaimFenced(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	now := archiveTestTime()
	repoID := insertTestRepo(t, d, "acme", "scan-failure-fence")
	require.NoError(d.EnsureDiscoveryArchives(ctx, []int64{repoID}, now))
	_, err := d.WriteDB().ExecContext(ctx, `
		UPDATE middleman_archive_repo_scans
		SET scan_generation = 6, status = 'running'
		WHERE repo_id = ? AND scan = 'issue_inventory'`, repoID)
	require.NoError(err)
	retry := now.Add(time.Hour)

	repoErrorCode := func() *string {
		states, err := d.ListArchiveRepoStates(ctx, []int64{repoID})
		require.NoError(err)
		require.Len(states, 1)
		return states[0].LastErrorCode
	}

	applied, err := d.RecordArchiveRepositoryFailureForScan(
		ctx, repoID, ArchiveScanIssueInventory, 4,
		ArchiveErrorCodeAuthentication, "stale generation", &retry, now,
	)
	require.NoError(err)
	assert.False(applied)
	assert.Nil(repoErrorCode())

	_, err = d.WriteDB().ExecContext(ctx, `
		UPDATE middleman_archive_repo_scans SET status = 'complete'
		WHERE repo_id = ? AND scan = 'issue_inventory'`, repoID)
	require.NoError(err)
	applied, err = d.RecordArchiveRepositoryFailureForScan(
		ctx, repoID, ArchiveScanIssueInventory, 6,
		ArchiveErrorCodeAuthentication, "completed scan", &retry, now,
	)
	require.NoError(err)
	assert.False(applied)
	assert.Nil(repoErrorCode())

	_, err = d.WriteDB().ExecContext(ctx, `
		UPDATE middleman_archive_repo_scans SET status = 'running'
		WHERE repo_id = ? AND scan = 'issue_inventory'`, repoID)
	require.NoError(err)
	applied, err = d.RecordArchiveRepositoryFailureForScan(
		ctx, repoID, ArchiveScanIssueInventory, 6,
		ArchiveErrorCodeAuthentication, "current claim", &retry, now,
	)
	require.NoError(err)
	assert.True(applied)
	require.NotNil(repoErrorCode())
	assert.Equal(string(ArchiveErrorCodeAuthentication), *repoErrorCode())
}
