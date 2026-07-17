package db

import (
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCommitMergeRequestChildSnapshotRollsBackEveryFamily(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := openTestDB(t)
	ctx := t.Context()
	now := archiveTestTime()
	repoID := insertTestRepo(t, database, "acme", "atomic-children")
	mrID, err := database.UpsertMergeRequest(ctx, &MergeRequest{
		RepoID: repoID, PlatformID: 1, Number: 1, Title: "current",
		State: MergeRequestStateOpen, ReviewDecision: "current",
		CreatedAt: now.Add(-time.Hour), UpdatedAt: now, LastActivityAt: now,
	})
	require.NoError(err)
	parent, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 1)
	require.NoError(err)
	require.NotNil(parent)
	require.NoError(database.UpsertMREvents(ctx, []MREvent{
		{MergeRequestID: mrID, EventType: "issue_comment", DedupeKey: "current-comment", CreatedAt: now},
		{MergeRequestID: mrID, EventType: "review", DedupeKey: "current-review", CreatedAt: now},
		{MergeRequestID: mrID, EventType: "review_comment", DedupeKey: "current-inline", CreatedAt: now},
	}))
	require.NoError(database.UpsertMRReviewThreads(ctx, mrID, []MRReviewThread{{
		ProviderThreadID: "current-thread", CreatedAt: now, UpdatedAt: now,
	}}))
	_, err = database.WriteDB().ExecContext(ctx, `
		CREATE TRIGGER reject_new_review_thread
		BEFORE INSERT ON middleman_mr_review_threads
		WHEN NEW.provider_thread_id = 'new-thread'
		BEGIN SELECT RAISE(ABORT, 'synthetic thread failure'); END`)
	require.NoError(err)

	applied, err := database.CommitMergeRequestChildSnapshot(ctx, MergeRequestChildSnapshot{
		MergeRequestID: mrID, ExpectedRevision: parent.SnapshotRevision,
		Comments:         []MREvent{{MergeRequestID: mrID, EventType: "issue_comment", DedupeKey: "new-comment", CreatedAt: now}},
		CommentsComplete: true,
		Reviews:          []MREvent{{MergeRequestID: mrID, EventType: "review", DedupeKey: "new-review", CreatedAt: now}},
		ReviewsComplete:  true,
		InlineComments:   []MREvent{{MergeRequestID: mrID, EventType: "review_comment", DedupeKey: "new-inline", CreatedAt: now}},
		ReviewThreads:    []MRReviewThread{{ProviderThreadID: "new-thread", CreatedAt: now, UpdatedAt: now}},
		InlineComplete:   true,
		Derived:          &MRDerivedFields{ReviewDecision: "new", LastActivityAt: now.Add(time.Hour)},
	})
	require.Error(err)
	assert.False(applied)
	events, err := database.ListMREvents(ctx, mrID)
	require.NoError(err)
	keys := make([]string, len(events))
	for i := range events {
		keys[i] = events[i].DedupeKey
	}
	assert.ElementsMatch([]string{"current-comment", "current-review", "current-inline"}, keys)
	threads, err := database.ListMRReviewThreads(ctx, mrID)
	require.NoError(err)
	require.Len(threads, 1)
	assert.Equal("current-thread", threads[0].ProviderThreadID)
	parent, err = database.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 1)
	require.NoError(err)
	require.NotNil(parent)
	assert.Equal("current", parent.ReviewDecision)
	assert.Equal(now, parent.LastActivityAt)
}

func TestCompleteReviewDatasetsRemainAdditive(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := openTestDB(t)
	ctx := t.Context()
	now := archiveTestTime()
	repoID := insertTestRepo(t, database, "acme", "additive-reviews")
	mrID, err := database.UpsertMergeRequest(ctx, &MergeRequest{
		RepoID: repoID, PlatformID: 1, Number: 1, Title: "reviews",
		State: MergeRequestStateOpen, CreatedAt: now.Add(-time.Hour), UpdatedAt: now,
	})
	require.NoError(err)
	parent, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 1)
	require.NoError(err)
	require.NotNil(parent)
	require.NoError(database.UpsertMREvents(ctx, []MREvent{
		{MergeRequestID: mrID, EventType: "review", DedupeKey: "old-review", CreatedAt: now.Add(-time.Hour)},
		{MergeRequestID: mrID, EventType: "review_comment", DedupeKey: "old-inline", CreatedAt: now.Add(-time.Hour)},
	}))
	require.NoError(database.UpsertMRReviewThreads(ctx, mrID, []MRReviewThread{{
		ProviderThreadID: "old-thread", CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour),
	}}))

	applied, err := database.CommitMergeRequestChildSnapshot(ctx, MergeRequestChildSnapshot{
		MergeRequestID: mrID, ExpectedRevision: parent.SnapshotRevision,
		Reviews: []MREvent{{
			MergeRequestID: mrID, EventType: "review", DedupeKey: "new-review", CreatedAt: now,
		}}, ReviewsComplete: true,
		InlineComments: []MREvent{{
			MergeRequestID: mrID, EventType: "review_comment", DedupeKey: "new-inline", CreatedAt: now,
		}},
		ReviewThreads:  []MRReviewThread{{ProviderThreadID: "new-thread", CreatedAt: now, UpdatedAt: now}},
		InlineComplete: true,
	})
	require.NoError(err)
	require.True(applied)
	events, err := database.ListMREvents(ctx, mrID)
	require.NoError(err)
	keys := make([]string, len(events))
	for i := range events {
		keys[i] = events[i].DedupeKey
	}
	assert.ElementsMatch([]string{"old-review", "new-review", "old-inline", "new-inline"}, keys)
	threads, err := database.ListMRReviewThreads(ctx, mrID)
	require.NoError(err)
	threadIDs := make([]string, len(threads))
	for i := range threads {
		threadIDs[i] = threads[i].ProviderThreadID
	}
	assert.ElementsMatch([]string{"old-thread", "new-thread"}, threadIDs)
}

func TestNormalSyncCompletesMatchingArchiveDatasetAndRejectsLatePage(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := openTestDB(t)
	ctx := t.Context()
	now := archiveTestTime()
	repoID := insertTestRepo(t, database, "acme", "normal-sync-archive-progress")
	require.NoError(database.EnsureDiscoveryArchives(ctx, []int64{repoID}, now))
	require.NoError(database.StartFullArchives(ctx, []int64{repoID}, now))
	insertArchiveItemForTest(
		t, database, repoID, ArchiveItemTypeMergeRequest, 7, now,
		ArchiveDatasetStatusUnsupported, ArchiveDatasetStatusPending, ArchiveDatasetStatusUnsupported,
	)
	mrID, revision, accepted, err := database.UpsertArchiveMergeRequestSnapshot(ctx, &MergeRequest{
		RepoID: repoID, PlatformID: 7, Number: 7, Title: "open MR",
		State: MergeRequestStateOpen, CreatedAt: now.Add(-time.Hour),
		UpdatedAt: now, LastActivityAt: now,
	})
	require.NoError(err)
	require.True(accepted)
	key := ArchiveDatasetKey{
		RepoID: repoID, ItemType: ArchiveItemTypeMergeRequest, ItemNumber: 7,
		Dataset: ArchiveDatasetReviews, SnapshotUpdatedAt: now, DomainRevision: revision,
	}
	_, err = database.CommitArchiveDatasetPage(ctx, ArchiveDatasetPage{
		ArchiveDatasetKey: key, NextCursor: "reviews-page-2",
		RecordCount: 1, Payload: []byte(`[{"dedupe_key":"staged-review"}]`),
		MaxPages: 10, MaxRecords: 10, MaxBytes: 1024, Now: now,
	})
	require.NoError(err)

	applied, err := database.CommitMergeRequestChildSnapshot(ctx, MergeRequestChildSnapshot{
		MergeRequestID: mrID, ExpectedRevision: revision,
		Reviews: []MREvent{{
			MergeRequestID: mrID, EventType: "review", DedupeKey: "current-review", CreatedAt: now,
		}},
		ReviewsComplete: true,
	})
	require.NoError(err)
	require.True(applied)
	item := archiveItemState(t, database, repoID, ArchiveItemTypeMergeRequest, 7)
	assert.Equal(ArchiveDatasetStatusComplete, item.ReviewsStatus)
	assert.NotNil(item.MirroredProviderUpdatedAt)
	assert.NotNil(item.HydratedAt)
	stage, err := database.GetArchiveDatasetStage(ctx, key)
	require.NoError(err)
	assert.Zero(stage.PageCount)

	_, err = database.CommitArchiveDatasetPage(ctx, ArchiveDatasetPage{
		ArchiveDatasetKey: key, InputCursor: "reviews-page-2", Exhausted: true,
		RecordCount: 1, Payload: []byte(`[{"dedupe_key":"late-review"}]`),
		MaxPages: 10, MaxRecords: 10, MaxBytes: 1024, Now: now.Add(time.Minute),
	})
	require.Error(err)
	assert.Contains(err.Error(), "already complete")
	events, err := database.ListMREvents(ctx, mrID)
	require.NoError(err)
	require.Len(events, 1)
	assert.Equal("current-review", events[0].DedupeKey)
}

func TestNormalSyncDoesNotCompleteNewerArchiveDataset(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := openTestDB(t)
	ctx := t.Context()
	now := archiveTestTime()
	repoID := insertTestRepo(t, database, "acme", "newer-archive-progress")
	require.NoError(database.EnsureDiscoveryArchives(ctx, []int64{repoID}, now))
	require.NoError(database.StartFullArchives(ctx, []int64{repoID}, now))
	mrID, revision, accepted, err := database.UpsertMergeRequestSnapshotWithLabels(ctx, &MergeRequest{
		RepoID: repoID, PlatformID: 8, Number: 8, Title: "older normal snapshot",
		State: MergeRequestStateOpen, CreatedAt: now.Add(-time.Hour),
		UpdatedAt: now, LastActivityAt: now,
	})
	require.NoError(err)
	require.True(accepted)
	newer := now.Add(time.Hour)
	insertArchiveItemForTest(
		t, database, repoID, ArchiveItemTypeMergeRequest, 8, newer,
		ArchiveDatasetStatusUnsupported, ArchiveDatasetStatusPending, ArchiveDatasetStatusUnsupported,
	)

	applied, err := database.CommitMergeRequestChildSnapshot(ctx, MergeRequestChildSnapshot{
		MergeRequestID: mrID, ExpectedRevision: revision,
		Reviews: []MREvent{{
			MergeRequestID: mrID, EventType: "review", DedupeKey: "older-review", CreatedAt: now,
		}},
		ReviewsComplete: true,
	})
	require.NoError(err)
	require.True(applied)
	item := archiveItemState(t, database, repoID, ArchiveItemTypeMergeRequest, 8)
	assert.Equal(ArchiveDatasetStatusPending, item.ReviewsStatus)
	assert.Nil(item.MirroredProviderUpdatedAt)
	assert.Nil(item.HydratedAt)
}

func TestUpdateMRWorkflowApprovalSnapshotRejectsAdvancedRevision(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := openTestDB(t)
	ctx := t.Context()
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	repoID := insertTestRepo(t, database, "acme", "workflow-race")
	mr := &MergeRequest{
		RepoID: repoID, PlatformID: 1, Number: 1, Title: "workflow",
		State: MergeRequestStateOpen, CreatedAt: now, UpdatedAt: now,
		LastActivityAt: now,
	}
	mrID, revision, accepted, err := database.UpsertMergeRequestSnapshotWithLabels(ctx, mr)
	require.NoError(err)
	require.True(accepted)
	applied, err := database.UpdateMRWorkflowApprovalSnapshot(
		ctx, mrID, revision, now, "head", true, 2,
	)
	require.NoError(err)
	require.True(applied)
	_, currentRevision, accepted, err := database.UpsertMergeRequestSnapshotWithLabels(ctx, mr)
	require.NoError(err)
	require.True(accepted)
	assert.Greater(currentRevision, revision)
	applied, err = database.UpdateMRWorkflowApprovalSnapshot(
		ctx, mrID, revision, now.Add(time.Minute), "head", false, 0,
	)
	require.NoError(err)
	assert.False(applied)

	var required bool
	var count int
	require.NoError(database.ReadDB().QueryRowContext(ctx, `
		SELECT workflow_approval_required, workflow_approval_count
		FROM middleman_merge_requests WHERE id = ?`, mrID,
	).Scan(&required, &count))
	assert.True(required)
	assert.Equal(2, count)
}

func TestPostParentSnapshotUpdatesRejectAdvancedRevision(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := openTestDB(t)
	ctx := t.Context()
	now := time.Date(2026, 7, 15, 1, 0, 0, 0, time.UTC)
	repoID := insertTestRepo(t, database, "acme", "post-parent-race")
	mr := &MergeRequest{
		RepoID: repoID, PlatformID: 1, Number: 1, Title: "old",
		State: MergeRequestStateOpen, PlatformHeadSHA: "old-head", PlatformBaseSHA: "old-base",
		CreatedAt: now.Add(-time.Hour), UpdatedAt: now, LastActivityAt: now,
	}
	mrID, oldRevision, accepted, err := database.UpsertMergeRequestSnapshotWithLabels(ctx, mr)
	require.NoError(err)
	require.True(accepted)
	mr.Title = "new"
	mr.PlatformHeadSHA = "new-head"
	mr.PlatformBaseSHA = "new-base"
	_, newRevision, accepted, err := database.UpsertMergeRequestSnapshotWithLabels(ctx, mr)
	require.NoError(err)
	require.True(accepted)
	require.Greater(newRevision, oldRevision)
	applied, err := database.UpdateMergeRequestCISnapshot(ctx, mrID, newRevision, "success", `[{"name":"current"}]`)
	require.NoError(err)
	require.True(applied)
	require.NoError(database.UpdateDiffSHAs(ctx, repoID, 1, "new-head", "new-base", "new-merge-base"))
	applied, err = database.MarkMergeRequestDetailFetchedSnapshot(ctx, mrID, newRevision, true)
	require.NoError(err)
	require.True(applied)

	applied, err = database.ClearMRCISnapshot(ctx, mrID, oldRevision, "old-head")
	require.NoError(err)
	assert.False(applied)
	applied, err = database.UpdateDiffSHAsSnapshot(
		ctx, mrID, oldRevision, "old-head", "old-base", "old-head", "old-base", "old-merge-base",
	)
	require.NoError(err)
	assert.False(applied)
	applied, err = database.ClearMRDetailFetchedSnapshot(ctx, mrID, oldRevision)
	require.NoError(err)
	assert.False(applied)

	current, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 1)
	require.NoError(err)
	require.NotNil(current)
	assert.Equal("success", current.CIStatus)
	assert.Equal("new-head", current.DiffHeadSHA)
	assert.Equal("new-base", current.DiffBaseSHA)
	assert.Equal("new-merge-base", current.MergeBaseSHA)
	assert.NotNil(current.DetailFetchedAt)

	issueID, issueRevision, accepted, err := database.UpsertIssueSnapshotWithLabels(ctx, &Issue{
		RepoID: repoID, PlatformID: 2, Number: 2, Title: "old issue", State: "open",
		CreatedAt: now.Add(-time.Hour), UpdatedAt: now, LastActivityAt: now,
	})
	require.NoError(err)
	require.True(accepted)
	_, currentIssueRevision, accepted, err := database.UpsertIssueSnapshotWithLabels(ctx, &Issue{
		RepoID: repoID, PlatformID: 2, Number: 2, Title: "new issue", State: "open",
		CreatedAt: now.Add(-time.Hour), UpdatedAt: now, LastActivityAt: now,
	})
	require.NoError(err)
	require.True(accepted)
	applied, err = database.MarkIssueDetailFetchedSnapshot(ctx, issueID, currentIssueRevision)
	require.NoError(err)
	require.True(applied)
	applied, err = database.ClearIssueDetailFetchedSnapshot(ctx, issueID, issueRevision)
	require.NoError(err)
	assert.False(applied)
	currentIssue, err := database.GetIssueByRepoIDAndNumber(ctx, repoID, 2)
	require.NoError(err)
	require.NotNil(currentIssue)
	assert.NotNil(currentIssue.DetailFetchedAt)
}

func TestArchiveDeletionHelpersSupportMaximumKeySet(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := openTestDB(t)
	ctx := t.Context()
	now := archiveTestTime()
	repoID := insertTestRepo(t, database, "acme", "maximum-keys")
	mrID, err := database.UpsertMergeRequest(ctx, &MergeRequest{
		RepoID: repoID, PlatformID: 1, Number: 1, Title: "current",
		State: MergeRequestStateOpen, CreatedAt: now.Add(-time.Hour),
		UpdatedAt: now, LastActivityAt: now,
	})
	require.NoError(err)
	require.NoError(database.UpsertMREvents(ctx, []MREvent{
		{MergeRequestID: mrID, EventType: "review", DedupeKey: "keep-review", CreatedAt: now},
		{MergeRequestID: mrID, EventType: "review_comment", DedupeKey: "keep-inline", CreatedAt: now},
	}))
	require.NoError(database.UpsertMRReviewThreads(ctx, mrID, []MRReviewThread{{
		ProviderThreadID: "keep-thread", CreatedAt: now, UpdatedAt: now,
	}}))
	threadIDs := archiveMaximumKeys("keep-thread")
	inlineKeys := archiveMaximumKeys("keep-inline")

	require.NoError(database.Tx(ctx, func(tx *sql.Tx) error {
		return deleteMissingMRReviewThreadsTx(ctx, tx, mrID, threadIDs, inlineKeys)
	}))
	events, err := database.ListMREvents(ctx, mrID)
	require.NoError(err)
	assert.Len(events, 2)
	threads, err := database.ListMRReviewThreads(ctx, mrID)
	require.NoError(err)
	require.Len(threads, 1)
	assert.Equal("keep-thread", threads[0].ProviderThreadID)
}

func TestCommitIssueChildSnapshotSatisfiesMatchingArchiveProgress(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := openTestDB(t)
	ctx := t.Context()
	now := archiveTestTime()
	repoID := insertTestRepo(t, database, "acme", "live-satisfy-issue")
	require.NoError(database.EnsureDiscoveryArchives(ctx, []int64{repoID}, now))
	issueID := insertTestIssue(t, database, repoID, 7, "issue", now)
	insertArchiveItemForTest(t, database, repoID, ArchiveItemTypeIssue, 7, now,
		ArchiveDatasetStatusPending, ArchiveDatasetStatusNotApplicable, ArchiveDatasetStatusNotApplicable)
	key := datasetProgressKeyForTest(repoID, ArchiveItemTypeIssue, 7, ArchiveDatasetComments)
	insertDatasetProgressForTest(t, database, key, 1, 2, "cursor-3", ArchiveDatasetProgressRunning, 2)

	applied, err := database.CommitIssueChildSnapshot(ctx, IssueChildSnapshot{
		IssueID: issueID, ExpectedRevision: 1,
		Comments: []IssueEvent{issueComment("c-1", "live")},
	})
	require.NoError(err)
	require.True(applied)
	assert.Equal([]string{"c-1"}, issueCommentKeysForTest(t, database, issueID))
	// The partially scanned dataset completes from the live observation: no
	// further provider pages are owed and the cursor is gone.
	progress, err := database.GetDatasetProgress(ctx, repoID, ArchiveItemTypeIssue, 7, ArchiveDatasetComments)
	require.NoError(err)
	assert.Equal(ArchiveDatasetProgressComplete, progress.Status)
	assert.Nil(progress.NextCursor)
	assert.Nil(progress.LastInputCursor)
	assert.NotNil(progress.CompletedAt)
}

func TestCommitMergeRequestChildSnapshotSatisfiesAllMatchingDatasets(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := openTestDB(t)
	ctx := t.Context()
	now := archiveTestTime()
	repoID := insertTestRepo(t, database, "acme", "live-satisfy-mr")
	require.NoError(database.EnsureDiscoveryArchives(ctx, []int64{repoID}, now))
	mrID, err := database.UpsertMergeRequest(ctx, &MergeRequest{
		RepoID: repoID, PlatformID: 7, Number: 7, Title: "mr",
		State: MergeRequestStateOpen, CreatedAt: now.Add(-time.Hour),
		UpdatedAt: now, LastActivityAt: now,
	})
	require.NoError(err)
	insertArchiveItemForTest(t, database, repoID, ArchiveItemTypeMergeRequest, 7, now,
		ArchiveDatasetStatusPending, ArchiveDatasetStatusPending, ArchiveDatasetStatusPending)
	insertDatasetProgressForTest(t, database,
		datasetProgressKeyForTest(repoID, ArchiveItemTypeMergeRequest, 7, ArchiveDatasetComments),
		1, 1, nil, ArchiveDatasetProgressPending, 0)
	insertDatasetProgressForTest(t, database,
		datasetProgressKeyForTest(repoID, ArchiveItemTypeMergeRequest, 7, ArchiveDatasetReviews),
		1, 2, "cursor-2", ArchiveDatasetProgressRunning, 1)
	insertDatasetProgressForTest(t, database,
		datasetProgressKeyForTest(repoID, ArchiveItemTypeMergeRequest, 7, ArchiveDatasetInlineComments),
		1, 1, nil, ArchiveDatasetProgressFailed, 0)

	applied, err := database.CommitMergeRequestChildSnapshot(ctx, MergeRequestChildSnapshot{
		MergeRequestID: mrID, ExpectedRevision: 1,
		Comments:         []MREvent{mrComment("c-1", "live")},
		CommentsComplete: true,
		Reviews: []MREvent{{
			MergeRequestID: mrID, EventType: "review", DedupeKey: "r-1", CreatedAt: now,
		}},
		ReviewsComplete: true,
		InlineComments: []MREvent{{
			MergeRequestID: mrID, EventType: "review_comment", DedupeKey: "i-1", CreatedAt: now,
		}},
		ReviewThreads:  []MRReviewThread{{ProviderThreadID: "t-1", CreatedAt: now, UpdatedAt: now}},
		InlineComplete: true,
	})
	require.NoError(err)
	require.True(applied)
	for _, dataset := range []ArchiveDataset{
		ArchiveDatasetComments, ArchiveDatasetReviews, ArchiveDatasetInlineComments,
	} {
		progress, err := database.GetDatasetProgress(ctx, repoID, ArchiveItemTypeMergeRequest, 7, dataset)
		require.NoError(err)
		assert.Equal(ArchiveDatasetProgressComplete, progress.Status, string(dataset))
		assert.Nil(progress.NextCursor, string(dataset))
		assert.NotNil(progress.CompletedAt, string(dataset))
	}
	assert.Equal([]string{"c-1"}, mrEventKeysForTest(t, database, mrID, "issue_comment"))
	assert.Equal([]string{"r-1"}, mrEventKeysForTest(t, database, mrID, "review"))
	assert.Equal([]string{"i-1"}, mrEventKeysForTest(t, database, mrID, "review_comment"))
}

func TestCommitChildSnapshotLiveWriteIndependentOfArchiveProgress(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := openTestDB(t)
	ctx := t.Context()
	now := archiveTestTime()
	repoID := insertTestRepo(t, database, "acme", "live-independent-children")
	require.NoError(database.EnsureDiscoveryArchives(ctx, []int64{repoID}, now))

	commitLive := func(issueID int64) {
		t.Helper()
		applied, err := database.CommitIssueChildSnapshot(ctx, IssueChildSnapshot{
			IssueID: issueID, ExpectedRevision: 1,
			Comments: []IssueEvent{issueComment("live", "live")},
		})
		require.NoError(err)
		require.True(applied)
	}

	// Missing progress row: the live write succeeds with domain rows only.
	missingID := insertTestIssue(t, database, repoID, 1, "missing", now)
	commitLive(missingID)
	assert.Equal([]string{"live"}, issueCommentKeysForTest(t, database, missingID))

	// Blocked progress: the live write lands and the block is untouched.
	blockedID := insertTestIssue(t, database, repoID, 2, "blocked", now)
	insertArchiveItemForTest(t, database, repoID, ArchiveItemTypeIssue, 2, now,
		ArchiveDatasetStatusPending, ArchiveDatasetStatusNotApplicable, ArchiveDatasetStatusNotApplicable)
	blockedKey := datasetProgressKeyForTest(repoID, ArchiveItemTypeIssue, 2, ArchiveDatasetComments)
	insertDatasetProgressForTest(t, database, blockedKey, 1, 1, "cursor", ArchiveDatasetProgressBlocked, 3)
	commitLive(blockedID)
	assert.Equal([]string{"live"}, issueCommentKeysForTest(t, database, blockedID))
	progress, err := database.GetDatasetProgress(ctx, repoID, ArchiveItemTypeIssue, 2, ArchiveDatasetComments)
	require.NoError(err)
	assert.Equal(ArchiveDatasetProgressBlocked, progress.Status)

	// Progress bound to a different parent revision stays pending untouched.
	staleID := insertTestIssue(t, database, repoID, 3, "stale", now)
	insertArchiveItemForTest(t, database, repoID, ArchiveItemTypeIssue, 3, now,
		ArchiveDatasetStatusPending, ArchiveDatasetStatusNotApplicable, ArchiveDatasetStatusNotApplicable)
	staleKey := datasetProgressKeyForTest(repoID, ArchiveItemTypeIssue, 3, ArchiveDatasetComments)
	insertDatasetProgressForTest(t, database, staleKey, 5, 2, nil, ArchiveDatasetProgressPending, 0)
	commitLive(staleID)
	assert.Equal([]string{"live"}, issueCommentKeysForTest(t, database, staleID))
	progress, err = database.GetDatasetProgress(ctx, repoID, ArchiveItemTypeIssue, 3, ArchiveDatasetComments)
	require.NoError(err)
	assert.Equal(ArchiveDatasetProgressPending, progress.Status)
	assert.Equal(int64(5), progress.ParentRevision)

	// A paused repository never has progress satisfied, but the live domain
	// write still lands.
	require.NoError(database.PauseArchives(ctx, []int64{repoID}, now))
	pausedID := insertTestIssue(t, database, repoID, 4, "paused", now)
	insertArchiveItemForTest(t, database, repoID, ArchiveItemTypeIssue, 4, now,
		ArchiveDatasetStatusPending, ArchiveDatasetStatusNotApplicable, ArchiveDatasetStatusNotApplicable)
	pausedKey := datasetProgressKeyForTest(repoID, ArchiveItemTypeIssue, 4, ArchiveDatasetComments)
	insertDatasetProgressForTest(t, database, pausedKey, 1, 1, nil, ArchiveDatasetProgressPending, 0)
	commitLive(pausedID)
	assert.Equal([]string{"live"}, issueCommentKeysForTest(t, database, pausedID))
	progress, err = database.GetDatasetProgress(ctx, repoID, ArchiveItemTypeIssue, 4, ArchiveDatasetComments)
	require.NoError(err)
	assert.Equal(ArchiveDatasetProgressPending, progress.Status)
}

func TestCommitChildSnapshotReplacesCommentsAcrossSuccessiveCommits(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := openTestDB(t)
	ctx := t.Context()
	now := archiveTestTime()
	repoID := insertTestRepo(t, database, "acme", "successive-replace")
	issueID := insertTestIssue(t, database, repoID, 1, "issue", now)

	applied, err := database.CommitIssueChildSnapshot(ctx, IssueChildSnapshot{
		IssueID: issueID, ExpectedRevision: 1,
		Comments: []IssueEvent{issueComment("a", "a"), issueComment("b", "b")},
	})
	require.NoError(err)
	require.True(applied)
	assert.Equal([]string{"a", "b"}, issueCommentKeysForTest(t, database, issueID))

	// A later complete observation without "b" deletes it.
	applied, err = database.CommitIssueChildSnapshot(ctx, IssueChildSnapshot{
		IssueID: issueID, ExpectedRevision: 1,
		Comments: []IssueEvent{issueComment("a", "a")},
	})
	require.NoError(err)
	require.True(applied)
	assert.Equal([]string{"a"}, issueCommentKeysForTest(t, database, issueID))
	issue, err := database.GetIssueByRepoIDAndNumber(ctx, repoID, 1)
	require.NoError(err)
	require.NotNil(issue)
	assert.Equal(1, issue.CommentCount)

	mrID, err := database.UpsertMergeRequest(ctx, &MergeRequest{
		RepoID: repoID, PlatformID: 1, Number: 1, Title: "mr",
		State: MergeRequestStateOpen, CreatedAt: now.Add(-time.Hour),
		UpdatedAt: now, LastActivityAt: now,
	})
	require.NoError(err)
	applied, err = database.CommitMergeRequestChildSnapshot(ctx, MergeRequestChildSnapshot{
		MergeRequestID: mrID, ExpectedRevision: 1,
		Comments:         []MREvent{mrComment("a", "a"), mrComment("b", "b")},
		CommentsComplete: true,
	})
	require.NoError(err)
	require.True(applied)

	// An incomplete comment page keeps upsert-only semantics: nothing is
	// deleted.
	incomplete := mrComment("c", "c")
	incomplete.MergeRequestID = mrID
	applied, err = database.CommitMergeRequestChildSnapshot(ctx, MergeRequestChildSnapshot{
		MergeRequestID: mrID, ExpectedRevision: 1,
		Comments: []MREvent{incomplete},
	})
	require.NoError(err)
	require.True(applied)
	assert.Equal([]string{"a", "b", "c"}, mrEventKeysForTest(t, database, mrID, "issue_comment"))

	// The next complete observation replaces the whole family.
	applied, err = database.CommitMergeRequestChildSnapshot(ctx, MergeRequestChildSnapshot{
		MergeRequestID: mrID, ExpectedRevision: 1,
		Comments:         []MREvent{mrComment("c", "c")},
		CommentsComplete: true,
	})
	require.NoError(err)
	require.True(applied)
	assert.Equal([]string{"c"}, mrEventKeysForTest(t, database, mrID, "issue_comment"))
	mr, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 1)
	require.NoError(err)
	require.NotNil(mr)
	assert.Equal(1, mr.CommentCount)
}

func TestLiveAndArchiveParentUpsertsShareRevisionCore(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	now := archiveTestTime()
	liveDB := openTestDB(t)
	archiveDB := openTestDB(t)
	liveRepoID := insertTestRepo(t, liveDB, "acme", "shared-parent-core")
	archiveRepoID := insertTestRepo(t, archiveDB, "acme", "shared-parent-core")

	issueSnapshot := func(repoID int64, title string, updatedAt time.Time) *Issue {
		return &Issue{
			RepoID: repoID, PlatformID: 7, Number: 7, Title: title, State: "open",
			CreatedAt: now.Add(-time.Hour), UpdatedAt: updatedAt, LastActivityAt: updatedAt,
			Labels: []Label{{Name: "bug", Color: "red"}},
		}
	}
	_, _, accepted, err := liveDB.UpsertIssueSnapshotWithLabels(ctx, issueSnapshot(liveRepoID, "first", now))
	require.NoError(err)
	require.True(accepted)
	_, _, accepted, err = archiveDB.UpsertIssueSnapshotWithLabels(ctx, issueSnapshot(archiveRepoID, "first", now))
	require.NoError(err)
	require.True(accepted)
	_, liveRevision, accepted, err := liveDB.UpsertIssueSnapshotWithLabels(
		ctx, issueSnapshot(liveRepoID, "second", now.Add(time.Hour)))
	require.NoError(err)
	require.True(accepted)
	_, archiveRevision, accepted, err := archiveDB.UpsertArchiveIssueSnapshot(
		ctx, issueSnapshot(archiveRepoID, "second", now.Add(time.Hour)))
	require.NoError(err)
	require.True(accepted)
	assert.Equal(liveRevision, archiveRevision)
	liveIssue, err := liveDB.GetIssueByRepoIDAndNumber(ctx, liveRepoID, 7)
	require.NoError(err)
	require.NotNil(liveIssue)
	archiveIssue, err := archiveDB.GetIssueByRepoIDAndNumber(ctx, archiveRepoID, 7)
	require.NoError(err)
	require.NotNil(archiveIssue)
	assert.Equal(liveIssue.SnapshotRevision, archiveIssue.SnapshotRevision)
	assert.Equal(liveIssue.Title, archiveIssue.Title)
	assert.Equal(liveIssue.UpdatedAt, archiveIssue.UpdatedAt)
	require.Len(archiveIssue.Labels, 1)
	assert.Equal(liveIssue.Labels[0].Name, archiveIssue.Labels[0].Name)

	mrSnapshot := func(repoID int64, title string, updatedAt time.Time) *MergeRequest {
		return &MergeRequest{
			RepoID: repoID, PlatformID: 8, Number: 8, Title: title,
			State: MergeRequestStateOpen, CreatedAt: now.Add(-time.Hour),
			UpdatedAt: updatedAt, LastActivityAt: updatedAt,
			Labels: []Label{{Name: "feature", Color: "blue"}},
		}
	}
	_, _, accepted, err = liveDB.UpsertMergeRequestSnapshotWithLabels(ctx, mrSnapshot(liveRepoID, "first", now))
	require.NoError(err)
	require.True(accepted)
	_, _, accepted, err = archiveDB.UpsertMergeRequestSnapshotWithLabels(ctx, mrSnapshot(archiveRepoID, "first", now))
	require.NoError(err)
	require.True(accepted)
	_, liveMRRevision, accepted, err := liveDB.UpsertMergeRequestSnapshotWithLabels(
		ctx, mrSnapshot(liveRepoID, "second", now.Add(time.Hour)))
	require.NoError(err)
	require.True(accepted)
	_, archiveMRRevision, accepted, err := archiveDB.UpsertArchiveMergeRequestSnapshot(
		ctx, mrSnapshot(archiveRepoID, "second", now.Add(time.Hour)))
	require.NoError(err)
	require.True(accepted)
	assert.Equal(liveMRRevision, archiveMRRevision)
	liveMR, err := liveDB.GetMergeRequestByRepoIDAndNumber(ctx, liveRepoID, 8)
	require.NoError(err)
	require.NotNil(liveMR)
	archiveMR, err := archiveDB.GetMergeRequestByRepoIDAndNumber(ctx, archiveRepoID, 8)
	require.NoError(err)
	require.NotNil(archiveMR)
	assert.Equal(liveMR.SnapshotRevision, archiveMR.SnapshotRevision)
	assert.Equal(liveMR.Title, archiveMR.Title)
	assert.Equal(liveMR.UpdatedAt, archiveMR.UpdatedAt)
	require.Len(archiveMR.Labels, 1)
	assert.Equal(liveMR.Labels[0].Name, archiveMR.Labels[0].Name)
}

func archiveMaximumKeys(retained string) []string {
	keys := make([]string, 50_000)
	keys[0] = retained
	for i := 1; i < len(keys); i++ {
		keys[i] = fmt.Sprintf("key-%05d", i)
	}
	return keys
}
