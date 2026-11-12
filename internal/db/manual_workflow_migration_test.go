package db

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestManualWorkflowRemovalPreservesUnrelatedData(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "manual-workflow-v93.db")
	raw := migrateToVersionForTest(t, path, 93)
	require.NoError(raw.Close())

	// Seed the real previous schema without running the new migration yet.
	previous, err := OpenPreparedForTest(path)
	require.NoError(err)
	t.Cleanup(func() { previous.Close() })
	repoID := insertTestRepo(t, previous, "example", "widget")
	pull := testMR(repoID, 7)
	pull.AutoMergeEnabled = true
	mrID := insertHistoricalTestMR(t, previous, pull)
	issue := testIssue(repoID, 9)
	issue.State = "closed"
	insertTestIssueWithOptions(t, previous, issue)
	draft, err := previous.GetOrCreateMRReviewDraft(ctx, mrID)
	require.NoError(err)
	_, err = previous.CreateMRReviewDraftComment(ctx, draft.ID, MRReviewDraftCommentInput{
		Body: "Keep this inline review", Range: testReviewLineRange(),
	})
	require.NoError(err)
	_, err = previous.UpsertMergeQueueState(ctx, repoID, MergeQueueState{
		Number: 7, BaseBranch: "main", Enabled: true, Queued: true,
		Position: 2, State: "AWAITING_CHECKS", MergeMethod: "squash", ObservedAt: baseTime(),
	})
	require.NoError(err)
	list, err := previous.PutWorklist(ctx, PutWorklistParams{
		Title: "Review work", Recipe: "items to review", CreatedBy: "agent-a", Now: baseTime(),
		Items: []WorklistItemParams{
			{RepoID: repoID, ItemType: ItemTypePR, ItemNumber: 7, Note: "Review this PR"},
			{RepoID: repoID, ItemType: ItemTypeIssue, ItemNumber: 9, Note: "Keep issue context"},
		},
	})
	require.NoError(err)
	_, err = previous.WriteDB().ExecContext(ctx, `
		INSERT INTO forge_item_workflow_state
			(repo_id, item_type, item_number, status, updated_source, updated_actor, updated_reason)
		VALUES (?, 'pr', 7, 'reviewing', 'ui', 'user-a', 'review'),
		       (?, 'issue', 9, 'waiting', 'ui', 'user-a', 'triage');
		INSERT INTO forge_spoke_preparation_receipts
			(state_kind, source_key, content_digest, hub_receipt, imported_at)
		VALUES ('workflow_state', 'old-handoff', 'content-hash', 'receipt-hash', '2026-01-02T03:04:05Z')`,
		repoID, repoID,
	)
	require.NoError(err)

	beforePull, err := previous.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 7)
	require.NoError(err)
	beforeIssue, err := previous.GetIssueByRepoIDAndNumber(ctx, repoID, 9)
	require.NoError(err)
	beforeQueue, err := previous.GetMergeQueueState(ctx, repoID, 7)
	require.NoError(err)
	beforeList, err := previous.GetWorklist(ctx, list.ID, nil)
	require.NoError(err)
	beforeDraft, err := previous.GetMRReviewDraft(ctx, mrID)
	require.NoError(err)
	beforeReceipts, err := previous.ListSpokePreparationReceipts(ctx)
	require.NoError(err)
	require.Len(beforeReceipts, 1)
	require.NoError(previous.Close())

	database, err := Open(path)
	require.NoError(err)
	t.Cleanup(func() { database.Close() })
	afterPull, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 7)
	require.NoError(err)
	assert.Equal(beforePull, afterPull)
	afterIssue, err := database.GetIssueByRepoIDAndNumber(ctx, repoID, 9)
	require.NoError(err)
	assert.Equal(beforeIssue, afterIssue)
	afterQueue, err := database.GetMergeQueueState(ctx, repoID, 7)
	require.NoError(err)
	assert.Equal(beforeQueue, afterQueue)
	afterList, err := database.GetWorklist(ctx, list.ID, nil)
	require.NoError(err)
	assert.Equal(beforeList, afterList)
	afterDraft, err := database.GetMRReviewDraft(ctx, mrID)
	require.NoError(err)
	assert.Equal(beforeDraft, afterDraft)
	// Existing receipts participate in federation seals, even when their source
	// feature is retired. They contain hashes, not manual workflow values.
	afterReceipts, err := database.ListSpokePreparationReceipts(ctx)
	require.NoError(err)
	assert.Equal(beforeReceipts, afterReceipts)
	assertDatabaseIntegrityForTest(t, database.ReadDB())
}
