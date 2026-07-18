package db

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func datasetProgressKeyForTest(repoID int64, itemType ArchiveItemType, number int, dataset ArchiveDataset) ArchiveDatasetProgressKey {
	return ArchiveDatasetProgressKey{
		RepoID: repoID, ItemType: itemType, ItemNumber: number, Dataset: dataset,
	}
}

func insertDatasetProgressForTest(
	t *testing.T,
	d *DB,
	key ArchiveDatasetProgressKey,
	parentRevision int64,
	generation int64,
	nextCursor any,
	status ArchiveDatasetProgressStatus,
	pageCount int,
) {
	t.Helper()
	_, err := d.WriteDB().ExecContext(t.Context(), `
		INSERT INTO middleman_archive_dataset_progress (
			repo_id, item_type, item_number, dataset, parent_revision,
			scan_generation, next_cursor, page_count, status, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		key.RepoID, key.ItemType, key.ItemNumber, key.Dataset, parentRevision,
		generation, nextCursor, pageCount, status,
		formatDatasetProgressTime(archiveTestTime()))
	require.NoError(t, err)
}

func insertIssueCommentEventForTest(
	t *testing.T,
	d *DB,
	issueID int64,
	dedupeKey string,
	externalID string,
	generation any,
) {
	t.Helper()
	_, err := d.WriteDB().ExecContext(t.Context(), `
		INSERT INTO middleman_issue_events (
			issue_id, event_type, created_at, dedupe_key,
			platform_external_id, ingest_generation
		) VALUES (?, 'issue_comment', ?, ?, ?, ?)`,
		issueID, archiveTestTime(), dedupeKey, externalID, generation)
	require.NoError(t, err)
}

func insertMREventForTest(
	t *testing.T,
	d *DB,
	mrID int64,
	eventType string,
	dedupeKey string,
	generation any,
) {
	t.Helper()
	_, err := d.WriteDB().ExecContext(t.Context(), `
		INSERT INTO middleman_mr_events (
			merge_request_id, event_type, created_at, dedupe_key, ingest_generation
		) VALUES (?, ?, ?, ?, ?)`,
		mrID, eventType, archiveTestTime(), dedupeKey, generation)
	require.NoError(t, err)
}

func issueCommentKeysForTest(t *testing.T, d *DB, issueID int64) []string {
	t.Helper()
	rows, err := d.ReadDB().QueryContext(t.Context(), `
		SELECT dedupe_key FROM middleman_issue_events
		WHERE issue_id = ? AND event_type = 'issue_comment'
		ORDER BY dedupe_key`, issueID)
	require.NoError(t, err)
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var key string
		require.NoError(t, rows.Scan(&key))
		keys = append(keys, key)
	}
	require.NoError(t, rows.Err())
	return keys
}

func mrEventKeysForTest(t *testing.T, d *DB, mrID int64, eventType string) []string {
	t.Helper()
	rows, err := d.ReadDB().QueryContext(t.Context(), `
		SELECT dedupe_key FROM middleman_mr_events
		WHERE merge_request_id = ? AND event_type = ?
		ORDER BY dedupe_key`, mrID, eventType)
	require.NoError(t, err)
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var key string
		require.NoError(t, rows.Scan(&key))
		keys = append(keys, key)
	}
	require.NoError(t, rows.Err())
	return keys
}

func issueSnapshotRevisionForTest(t *testing.T, d *DB, issueID int64) int64 {
	t.Helper()
	var revision int64
	require.NoError(t, d.ReadDB().QueryRowContext(t.Context(),
		`SELECT snapshot_revision FROM middleman_issues WHERE id = ?`, issueID,
	).Scan(&revision))
	return revision
}

func issueComment(dedupeKey, body string) IssueEvent {
	return IssueEvent{
		EventType: "issue_comment",
		Author:    "alice",
		Body:      body,
		CreatedAt: archiveTestTime(),
		DedupeKey: dedupeKey,
	}
}

func mrComment(dedupeKey, body string) MREvent {
	return MREvent{
		EventType: "issue_comment",
		Author:    "alice",
		Body:      body,
		CreatedAt: archiveTestTime(),
		DedupeKey: dedupeKey,
	}
}

func TestCommitDatasetPageAppliesRowsAndCursorAtomically(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()

	repoID := insertTestRepo(t, d, "acme", "atomic-page")
	issueID := insertTestIssue(t, d, repoID, 7, "issue", archiveTestTime())
	insertArchiveItemForTest(t, d, repoID, ArchiveItemTypeIssue, 7, archiveTestTime())
	key := datasetProgressKeyForTest(repoID, ArchiveItemTypeIssue, 7, ArchiveDatasetComments)
	insertDatasetProgressForTest(t, d, key, 1, 1, nil, ArchiveDatasetProgressPending, 0)

	err := d.CommitDatasetPage(ctx, DatasetPageCommit{
		Parent:           DomainParentRef{ItemType: ArchiveItemTypeIssue, ID: issueID},
		ExpectedRevision: 1,
		Dataset:          ArchiveDatasetComments,
		ScanGeneration:   1,
		Rows: DatasetRows{IssueComments: []IssueEvent{
			issueComment("c-1", "first"),
			issueComment("c-2", "second"),
		}},
		Progress: &DatasetProgressAdvance{
			RepoID: repoID, ItemNumber: 7, InputCursor: "", NextCursor: "cursor-2",
		},
	})
	require.NoError(err)

	assert.Equal([]string{"c-1", "c-2"}, issueCommentKeysForTest(t, d, issueID))
	progress, err := d.GetDatasetProgress(ctx, repoID, ArchiveItemTypeIssue, 7, ArchiveDatasetComments)
	require.NoError(err)
	assert.Equal(ArchiveDatasetProgressRunning, progress.Status)
	assert.Equal(1, progress.PageCount)
	assert.Equal(2, progress.ObservedCount)
	require.NotNil(progress.NextCursor)
	assert.Equal("cursor-2", *progress.NextCursor)
	require.NotNil(progress.LastInputCursor)
	assert.Empty(*progress.LastInputCursor)
	assert.NotNil(progress.StartedAt)
	assert.Nil(progress.CompletedAt)

	var generation int64
	require.NoError(d.ReadDB().QueryRowContext(ctx, `
		SELECT ingest_generation FROM middleman_issue_events
		WHERE issue_id = ? AND dedupe_key = 'c-1'`, issueID).Scan(&generation))
	assert.Equal(int64(1), generation)
}

func TestCommitDatasetPageFailedTransactionCommitsNeitherRowsNorCursor(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()

	repoID := insertTestRepo(t, d, "acme", "atomic-failure")
	issueID := insertTestIssue(t, d, repoID, 7, "issue", archiveTestTime())
	insertArchiveItemForTest(t, d, repoID, ArchiveItemTypeIssue, 7, archiveTestTime())
	key := datasetProgressKeyForTest(repoID, ArchiveItemTypeIssue, 7, ArchiveDatasetComments)
	insertDatasetProgressForTest(t, d, key, 1, 1, nil, ArchiveDatasetProgressPending, 0)
	// A stored comment already owns this provider external ID under another
	// dedupe key, so the second upsert below violates the partial unique
	// index after the first row was written.
	insertIssueCommentEventForTest(t, d, issueID, "existing", "ext-dup", nil)

	conflicting := issueComment("c-2", "second")
	conflicting.PlatformExternalID = "ext-dup"
	err := d.CommitDatasetPage(ctx, DatasetPageCommit{
		Parent:           DomainParentRef{ItemType: ArchiveItemTypeIssue, ID: issueID},
		ExpectedRevision: 1,
		Dataset:          ArchiveDatasetComments,
		ScanGeneration:   1,
		Rows: DatasetRows{IssueComments: []IssueEvent{
			issueComment("c-1", "first"),
			conflicting,
		}},
		Progress: &DatasetProgressAdvance{
			RepoID: repoID, ItemNumber: 7, InputCursor: "", NextCursor: "cursor-2",
		},
	})
	require.Error(err)

	assert.Equal([]string{"existing"}, issueCommentKeysForTest(t, d, issueID))
	progress, err := d.GetDatasetProgress(ctx, repoID, ArchiveItemTypeIssue, 7, ArchiveDatasetComments)
	require.NoError(err)
	assert.Equal(ArchiveDatasetProgressPending, progress.Status)
	assert.Zero(progress.PageCount)
	assert.Nil(progress.NextCursor)
	assert.Nil(progress.LastInputCursor)
}

func TestCommitDatasetPageIdempotentReplayWritesNothing(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()

	repoID := insertTestRepo(t, d, "acme", "replay")
	issueID := insertTestIssue(t, d, repoID, 7, "issue", archiveTestTime())
	insertArchiveItemForTest(t, d, repoID, ArchiveItemTypeIssue, 7, archiveTestTime())
	key := datasetProgressKeyForTest(repoID, ArchiveItemTypeIssue, 7, ArchiveDatasetComments)
	insertDatasetProgressForTest(t, d, key, 1, 1, nil, ArchiveDatasetProgressPending, 0)

	commit := DatasetPageCommit{
		Parent:           DomainParentRef{ItemType: ArchiveItemTypeIssue, ID: issueID},
		ExpectedRevision: 1,
		Dataset:          ArchiveDatasetComments,
		ScanGeneration:   1,
		Rows:             DatasetRows{IssueComments: []IssueEvent{issueComment("c-1", "original")}},
		Progress: &DatasetProgressAdvance{
			RepoID: repoID, ItemNumber: 7, InputCursor: "", NextCursor: "cursor-2",
		},
	}
	require.NoError(d.CommitDatasetPage(ctx, commit))

	replay := commit
	replay.Rows = DatasetRows{IssueComments: []IssueEvent{issueComment("c-1", "changed on replay")}}
	require.NoError(d.CommitDatasetPage(ctx, replay))

	progress, err := d.GetDatasetProgress(ctx, repoID, ArchiveItemTypeIssue, 7, ArchiveDatasetComments)
	require.NoError(err)
	assert.Equal(1, progress.PageCount)
	assert.Equal(1, progress.ObservedCount)

	var body string
	require.NoError(d.ReadDB().QueryRowContext(ctx, `
		SELECT body FROM middleman_issue_events
		WHERE issue_id = ? AND dedupe_key = 'c-1'`, issueID).Scan(&body))
	assert.Equal("original", body)
}

func TestCommitDatasetPageRejectsStaleCursorAndGeneration(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()

	repoID := insertTestRepo(t, d, "acme", "stale-cas")
	issueID := insertTestIssue(t, d, repoID, 7, "issue", archiveTestTime())
	insertArchiveItemForTest(t, d, repoID, ArchiveItemTypeIssue, 7, archiveTestTime())
	key := datasetProgressKeyForTest(repoID, ArchiveItemTypeIssue, 7, ArchiveDatasetComments)
	insertDatasetProgressForTest(t, d, key, 1, 3, "cursor-5", ArchiveDatasetProgressRunning, 4)
	bareIssueID := insertTestIssue(t, d, repoID, 8, "no progress", archiveTestTime())

	for name, commit := range map[string]DatasetPageCommit{
		"wrong generation": {
			Parent:           DomainParentRef{ItemType: ArchiveItemTypeIssue, ID: issueID},
			ExpectedRevision: 1,
			Dataset:          ArchiveDatasetComments,
			ScanGeneration:   2,
			Rows:             DatasetRows{IssueComments: []IssueEvent{issueComment("stale", "stale")}},
			Progress: &DatasetProgressAdvance{
				RepoID: repoID, ItemNumber: 7, InputCursor: "cursor-5", NextCursor: "cursor-6",
			},
		},
		"unrelated cursor": {
			Parent:           DomainParentRef{ItemType: ArchiveItemTypeIssue, ID: issueID},
			ExpectedRevision: 1,
			Dataset:          ArchiveDatasetComments,
			ScanGeneration:   3,
			Rows:             DatasetRows{IssueComments: []IssueEvent{issueComment("stale", "stale")}},
			Progress: &DatasetProgressAdvance{
				RepoID: repoID, ItemNumber: 7, InputCursor: "cursor-99", NextCursor: "cursor-100",
			},
		},
		"missing progress row": {
			Parent:           DomainParentRef{ItemType: ArchiveItemTypeIssue, ID: bareIssueID},
			ExpectedRevision: 1,
			Dataset:          ArchiveDatasetComments,
			ScanGeneration:   1,
			Rows:             DatasetRows{IssueComments: []IssueEvent{issueComment("stale", "stale")}},
			Progress: &DatasetProgressAdvance{
				RepoID: repoID, ItemNumber: 8, InputCursor: "", NextCursor: "cursor-2",
			},
		},
	} {
		err := d.CommitDatasetPage(ctx, commit)
		var stale *StaleDatasetProgressError
		require.ErrorAs(err, &stale, name)
	}

	assert.Empty(issueCommentKeysForTest(t, d, issueID))
	assert.Empty(issueCommentKeysForTest(t, d, bareIssueID))
	progress, err := d.GetDatasetProgress(ctx, repoID, ArchiveItemTypeIssue, 7, ArchiveDatasetComments)
	require.NoError(err)
	assert.Equal(int64(3), progress.ScanGeneration)
	assert.Equal(4, progress.PageCount)
	require.NotNil(progress.NextCursor)
	assert.Equal("cursor-5", *progress.NextCursor)
}

func TestCommitDatasetPageStaleParentRevisionReopensDataset(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()

	repoID := insertTestRepo(t, d, "acme", "stale-revision")
	issueID := insertTestIssue(t, d, repoID, 7, "issue", archiveTestTime())
	insertArchiveItemForTest(t, d, repoID, ArchiveItemTypeIssue, 7, archiveTestTime())
	key := datasetProgressKeyForTest(repoID, ArchiveItemTypeIssue, 7, ArchiveDatasetComments)
	insertDatasetProgressForTest(t, d, key, 1, 2, "cursor-3", ArchiveDatasetProgressRunning, 2)
	insertIssueCommentEventForTest(t, d, issueID, "kept", "", 2)

	// The parent advances after the scan page was fetched.
	_, err := d.UpsertIssue(ctx, testIssue(repoID, 7,
		withIssueTitle("newer"), withIssueActivity(archiveTestTime().Add(time.Hour))))
	require.NoError(err)
	require.Equal(int64(2), issueSnapshotRevisionForTest(t, d, issueID))

	err = d.CommitDatasetPage(ctx, DatasetPageCommit{
		Parent:           DomainParentRef{ItemType: ArchiveItemTypeIssue, ID: issueID},
		ExpectedRevision: 1,
		Dataset:          ArchiveDatasetComments,
		ScanGeneration:   2,
		Rows:             DatasetRows{IssueComments: []IssueEvent{issueComment("late", "late page")}},
		Progress: &DatasetProgressAdvance{
			RepoID: repoID, ItemNumber: 7, InputCursor: "cursor-3", NextCursor: "cursor-4",
		},
	})
	var stale *StaleDatasetProgressError
	require.ErrorAs(err, &stale)
	assert.Equal(int64(1), stale.ExpectedRevision)
	assert.Equal(int64(2), stale.GotRevision)

	// The dataset rebinds to the new revision without advancing its cursor,
	// and already-ingested rows are retained. The reopened generation is the
	// next even value: archive generations stay in the even namespace so they
	// never collide with odd live ingest stamps.
	progress, err := d.GetDatasetProgress(ctx, repoID, ArchiveItemTypeIssue, 7, ArchiveDatasetComments)
	require.NoError(err)
	assert.Equal(ArchiveDatasetProgressPending, progress.Status)
	assert.Equal(int64(4), progress.ScanGeneration)
	assert.Equal(int64(2), progress.ParentRevision)
	assert.Zero(progress.PageCount)
	assert.Nil(progress.NextCursor)
	assert.Nil(progress.LastInputCursor)
	assert.Equal([]string{"kept"}, issueCommentKeysForTest(t, d, issueID))
}

func TestCommitDatasetPageReconcilesCommentsOnlyAtExhaustion(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()

	repoID := insertTestRepo(t, d, "acme", "reconcile")
	issueID := insertTestIssue(t, d, repoID, 7, "issue", archiveTestTime())
	insertArchiveItemForTest(t, d, repoID, ArchiveItemTypeIssue, 7, archiveTestTime())
	key := datasetProgressKeyForTest(repoID, ArchiveItemTypeIssue, 7, ArchiveDatasetComments)
	insertDatasetProgressForTest(t, d, key, 1, 2, nil, ArchiveDatasetProgressPending, 0)
	insertIssueCommentEventForTest(t, d, issueID, "old-null", "", nil)
	insertIssueCommentEventForTest(t, d, issueID, "old-gen1", "", 1)

	parent := DomainParentRef{ItemType: ArchiveItemTypeIssue, ID: issueID}
	require.NoError(d.CommitDatasetPage(ctx, DatasetPageCommit{
		Parent: parent, ExpectedRevision: 1,
		Dataset: ArchiveDatasetComments, ScanGeneration: 2,
		Rows: DatasetRows{IssueComments: []IssueEvent{issueComment("new-1", "page one")}},
		Progress: &DatasetProgressAdvance{
			RepoID: repoID, ItemNumber: 7, InputCursor: "", NextCursor: "cursor-2",
		},
	}))
	assert.Equal(
		[]string{"new-1", "old-gen1", "old-null"},
		issueCommentKeysForTest(t, d, issueID),
		"a partial scan must not delete unobserved comments",
	)

	require.NoError(d.CommitDatasetPage(ctx, DatasetPageCommit{
		Parent: parent, ExpectedRevision: 1,
		Dataset: ArchiveDatasetComments, ScanGeneration: 2,
		Rows:  DatasetRows{IssueComments: []IssueEvent{issueComment("new-2", "page two")}},
		Final: true,
		Progress: &DatasetProgressAdvance{
			RepoID: repoID, ItemNumber: 7, InputCursor: "cursor-2",
		},
	}))
	assert.Equal([]string{"new-1", "new-2"}, issueCommentKeysForTest(t, d, issueID))

	progress, err := d.GetDatasetProgress(ctx, repoID, ArchiveItemTypeIssue, 7, ArchiveDatasetComments)
	require.NoError(err)
	assert.Equal(ArchiveDatasetProgressComplete, progress.Status)
	assert.Equal(2, progress.PageCount)
	assert.Nil(progress.NextCursor)
	require.NotNil(progress.LastInputCursor,
		"completion retains the final input cursor for replay detection")
	assert.Equal("cursor-2", *progress.LastInputCursor)
	assert.NotNil(progress.CompletedAt)
}

func TestCommitDatasetPageRetainsAdditiveHistory(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()

	repoID := insertTestRepo(t, d, "acme", "additive")
	mrID := insertTestMR(t, d, repoID, 9, "pull request", archiveTestTime())
	insertArchiveItemForTest(t, d, repoID, ArchiveItemTypeMergeRequest, 9, archiveTestTime())
	reviewKey := datasetProgressKeyForTest(repoID, ArchiveItemTypeMergeRequest, 9, ArchiveDatasetReviews)
	insertDatasetProgressForTest(t, d, reviewKey, 1, 1, nil, ArchiveDatasetProgressPending, 0)
	inlineKey := datasetProgressKeyForTest(repoID, ArchiveItemTypeMergeRequest, 9, ArchiveDatasetInlineComments)
	insertDatasetProgressForTest(t, d, inlineKey, 1, 1, nil, ArchiveDatasetProgressPending, 0)
	insertMREventForTest(t, d, mrID, "review", "review-old", nil)

	parent := DomainParentRef{ItemType: ArchiveItemTypeMergeRequest, ID: mrID}
	require.NoError(d.CommitDatasetPage(ctx, DatasetPageCommit{
		Parent: parent, ExpectedRevision: 1,
		Dataset: ArchiveDatasetReviews, ScanGeneration: 1,
		Rows: DatasetRows{Reviews: []MREvent{{
			EventType: "review", Author: "bob", CreatedAt: archiveTestTime(), DedupeKey: "review-new",
		}}},
		Final: true,
		Progress: &DatasetProgressAdvance{
			RepoID: repoID, ItemNumber: 9, InputCursor: "",
		},
	}))
	assert.Equal(
		[]string{"review-new", "review-old"},
		mrEventKeysForTest(t, d, mrID, "review"),
		"a final review page must never delete absent history",
	)

	require.NoError(d.CommitDatasetPage(ctx, DatasetPageCommit{
		Parent: parent, ExpectedRevision: 1,
		Dataset: ArchiveDatasetInlineComments, ScanGeneration: 1,
		Rows: DatasetRows{
			ReviewThreads: []MRReviewThread{{
				ProviderThreadID: "thread-1", Body: "thread", AuthorLogin: "bob",
				CreatedAt: archiveTestTime(), UpdatedAt: archiveTestTime(),
			}},
			ThreadEvents: []MREvent{{
				EventType: "inline_comment", Author: "bob",
				CreatedAt: archiveTestTime(), DedupeKey: "inline-1",
			}},
		},
		Final: true,
		Progress: &DatasetProgressAdvance{
			RepoID: repoID, ItemNumber: 9, InputCursor: "",
		},
	}))
	assert.Equal([]string{"inline-1"}, mrEventKeysForTest(t, d, mrID, "inline_comment"))
	var threadCount int
	require.NoError(d.ReadDB().QueryRowContext(ctx, `
		SELECT COUNT(*) FROM middleman_mr_review_threads
		WHERE merge_request_id = ?`, mrID).Scan(&threadCount))
	assert.Equal(1, threadCount)

	for _, dataset := range []ArchiveDataset{ArchiveDatasetReviews, ArchiveDatasetInlineComments} {
		progress, err := d.GetDatasetProgress(ctx, repoID, ArchiveItemTypeMergeRequest, 9, dataset)
		require.NoError(err)
		assert.Equal(ArchiveDatasetProgressComplete, progress.Status, dataset)
	}
}

func TestCommitDatasetPageBlocksScanAtPageBoundAndEchoedCursor(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()

	repoID := insertTestRepo(t, d, "acme", "page-bound")
	issueID := insertTestIssue(t, d, repoID, 7, "issue", archiveTestTime())
	insertArchiveItemForTest(t, d, repoID, ArchiveItemTypeIssue, 7, archiveTestTime())
	boundKey := datasetProgressKeyForTest(repoID, ArchiveItemTypeIssue, 7, ArchiveDatasetComments)
	insertDatasetProgressForTest(t, d, boundKey, 1, 1, "cursor-max", ArchiveDatasetProgressRunning, maxScanPages)

	err := d.CommitDatasetPage(ctx, DatasetPageCommit{
		Parent:           DomainParentRef{ItemType: ArchiveItemTypeIssue, ID: issueID},
		ExpectedRevision: 1,
		Dataset:          ArchiveDatasetComments,
		ScanGeneration:   1,
		Rows:             DatasetRows{IssueComments: []IssueEvent{issueComment("over-bound", "over")}},
		Progress: &DatasetProgressAdvance{
			RepoID: repoID, ItemNumber: 7, InputCursor: "cursor-max", NextCursor: "cursor-next",
		},
	})
	var blocked *ScanBlockedError
	require.ErrorAs(err, &blocked)
	assert.Equal("page_bound", blocked.Reason)
	assert.Empty(issueCommentKeysForTest(t, d, issueID))
	progress, err := d.GetDatasetProgress(ctx, repoID, ArchiveItemTypeIssue, 7, ArchiveDatasetComments)
	require.NoError(err)
	assert.Equal(ArchiveDatasetProgressBlocked, progress.Status)
	require.NotNil(progress.LastErrorCode)
	assert.Equal("page_bound", *progress.LastErrorCode)
	assert.Equal(maxScanPages, progress.PageCount, "progress is retained for diagnostics")

	// A commit onto an already-blocked scan spends no writes.
	err = d.CommitDatasetPage(ctx, DatasetPageCommit{
		Parent:           DomainParentRef{ItemType: ArchiveItemTypeIssue, ID: issueID},
		ExpectedRevision: 1,
		Dataset:          ArchiveDatasetComments,
		ScanGeneration:   1,
		Rows:             DatasetRows{IssueComments: []IssueEvent{issueComment("post-block", "post")}},
		Progress: &DatasetProgressAdvance{
			RepoID: repoID, ItemNumber: 7, InputCursor: "cursor-max", NextCursor: "cursor-next",
		},
	})
	require.ErrorAs(err, &blocked)
	assert.Empty(issueCommentKeysForTest(t, d, issueID))

	mrID := insertTestMR(t, d, repoID, 9, "pull request", archiveTestTime())
	insertArchiveItemForTest(t, d, repoID, ArchiveItemTypeMergeRequest, 9, archiveTestTime())
	echoKey := datasetProgressKeyForTest(repoID, ArchiveItemTypeMergeRequest, 9, ArchiveDatasetComments)
	insertDatasetProgressForTest(t, d, echoKey, 1, 1, "cursor-1", ArchiveDatasetProgressRunning, 1)

	err = d.CommitDatasetPage(ctx, DatasetPageCommit{
		Parent:           DomainParentRef{ItemType: ArchiveItemTypeMergeRequest, ID: mrID},
		ExpectedRevision: 1,
		Dataset:          ArchiveDatasetComments,
		ScanGeneration:   1,
		Rows:             DatasetRows{MRComments: []MREvent{mrComment("echo", "echo")}},
		Progress: &DatasetProgressAdvance{
			RepoID: repoID, ItemNumber: 9, InputCursor: "cursor-1", NextCursor: "cursor-1",
		},
	})
	require.ErrorAs(err, &blocked)
	assert.Equal("invalid_cursor", blocked.Reason)
	assert.Empty(mrEventKeysForTest(t, d, mrID, "issue_comment"))
	progress, err = d.GetDatasetProgress(ctx, repoID, ArchiveItemTypeMergeRequest, 9, ArchiveDatasetComments)
	require.NoError(err)
	assert.Equal(ArchiveDatasetProgressBlocked, progress.Status)
	require.NotNil(progress.LastErrorCode)
	assert.Equal("invalid_cursor", *progress.LastErrorCode)
}

func TestCommitDatasetPageLiveCommitIsIndependentOfArchiveProgress(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	now := archiveTestTime()

	repoID := insertTestRepo(t, d, "acme", "live-independent")
	require.NoError(d.EnsureDiscoveryArchives(ctx, []int64{repoID}, now))

	// Absent progress row: the live write succeeds with domain rows only.
	absentID := insertTestIssue(t, d, repoID, 1, "absent", now)
	require.NoError(d.CommitDatasetPage(ctx, DatasetPageCommit{
		Parent:           DomainParentRef{ItemType: ArchiveItemTypeIssue, ID: absentID},
		ExpectedRevision: 1,
		Dataset:          ArchiveDatasetComments,
		ScanGeneration:   1,
		Rows:             DatasetRows{IssueComments: []IssueEvent{issueComment("live-1", "live")}},
		Final:            true,
	}))
	assert.Equal([]string{"live-1"}, issueCommentKeysForTest(t, d, absentID))

	// Blocked progress: the live write still lands, the block is untouched.
	blockedID := insertTestIssue(t, d, repoID, 2, "blocked", now)
	insertArchiveItemForTest(t, d, repoID, ArchiveItemTypeIssue, 2, now)
	blockedKey := datasetProgressKeyForTest(repoID, ArchiveItemTypeIssue, 2, ArchiveDatasetComments)
	insertDatasetProgressForTest(t, d, blockedKey, 1, 1, "cursor", ArchiveDatasetProgressBlocked, 3)
	require.NoError(d.CommitDatasetPage(ctx, DatasetPageCommit{
		Parent:           DomainParentRef{ItemType: ArchiveItemTypeIssue, ID: blockedID},
		ExpectedRevision: 1,
		Dataset:          ArchiveDatasetComments,
		ScanGeneration:   1,
		Rows:             DatasetRows{IssueComments: []IssueEvent{issueComment("live-2", "live")}},
		Final:            true,
	}))
	assert.Equal([]string{"live-2"}, issueCommentKeysForTest(t, d, blockedID))
	progress, err := d.GetDatasetProgress(ctx, repoID, ArchiveItemTypeIssue, 2, ArchiveDatasetComments)
	require.NoError(err)
	assert.Equal(ArchiveDatasetProgressBlocked, progress.Status)

	// Matching pending progress on an active repository is satisfied.
	satisfiedID := insertTestIssue(t, d, repoID, 3, "satisfied", now)
	insertArchiveItemForTest(t, d, repoID, ArchiveItemTypeIssue, 3, now)
	satisfiedKey := datasetProgressKeyForTest(repoID, ArchiveItemTypeIssue, 3, ArchiveDatasetComments)
	insertDatasetProgressForTest(t, d, satisfiedKey, 1, 1, nil, ArchiveDatasetProgressPending, 0)
	require.NoError(d.CommitDatasetPage(ctx, DatasetPageCommit{
		Parent:           DomainParentRef{ItemType: ArchiveItemTypeIssue, ID: satisfiedID},
		ExpectedRevision: 1,
		Dataset:          ArchiveDatasetComments,
		ScanGeneration:   1,
		Rows:             DatasetRows{IssueComments: []IssueEvent{issueComment("live-3", "live")}},
		Final:            true,
	}))
	progress, err = d.GetDatasetProgress(ctx, repoID, ArchiveItemTypeIssue, 3, ArchiveDatasetComments)
	require.NoError(err)
	assert.Equal(ArchiveDatasetProgressComplete, progress.Status)
	assert.NotNil(progress.CompletedAt)

	// A paused repository never has its archive progress satisfied by live
	// sync, but the live domain write still lands.
	require.NoError(d.PauseArchives(ctx, []int64{repoID}, now))
	pausedID := insertTestIssue(t, d, repoID, 4, "paused", now)
	insertArchiveItemForTest(t, d, repoID, ArchiveItemTypeIssue, 4, now)
	pausedKey := datasetProgressKeyForTest(repoID, ArchiveItemTypeIssue, 4, ArchiveDatasetComments)
	insertDatasetProgressForTest(t, d, pausedKey, 1, 1, nil, ArchiveDatasetProgressPending, 0)
	require.NoError(d.CommitDatasetPage(ctx, DatasetPageCommit{
		Parent:           DomainParentRef{ItemType: ArchiveItemTypeIssue, ID: pausedID},
		ExpectedRevision: 1,
		Dataset:          ArchiveDatasetComments,
		ScanGeneration:   1,
		Rows:             DatasetRows{IssueComments: []IssueEvent{issueComment("live-4", "live")}},
		Final:            true,
	}))
	assert.Equal([]string{"live-4"}, issueCommentKeysForTest(t, d, pausedID))
	progress, err = d.GetDatasetProgress(ctx, repoID, ArchiveItemTypeIssue, 4, ArchiveDatasetComments)
	require.NoError(err)
	assert.Equal(ArchiveDatasetProgressPending, progress.Status)
}

func TestLiveParentUpsertSurvivesArchiveReopenFailure(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	now := archiveTestTime()

	repoID := insertTestRepo(t, d, "acme", "reopen-injection")
	require.NoError(d.EnsureDiscoveryArchives(ctx, []int64{repoID}, now))
	require.NoError(d.StartFullArchives(ctx, []int64{repoID}, now))
	issueID := insertTestIssue(t, d, repoID, 7, "issue", now)
	insertArchiveItemForTest(t, d, repoID, ArchiveItemTypeIssue, 7, now)
	key := datasetProgressKeyForTest(repoID, ArchiveItemTypeIssue, 7, ArchiveDatasetComments)
	insertDatasetProgressForTest(t, d, key, 1, 2, nil, ArchiveDatasetProgressPending, 0)

	// Every archive-progress UPDATE now fails: an injected bookkeeping error
	// in the dataset-reopen step of the shared parent upsert core.
	_, err := d.WriteDB().ExecContext(ctx, `
		CREATE TRIGGER fail_progress_updates BEFORE UPDATE ON middleman_archive_dataset_progress
		BEGIN SELECT RAISE(ABORT, 'injected archive bookkeeping failure'); END`)
	require.NoError(err)

	// A live parent snapshot must still commit: archive bookkeeping is
	// best-effort for live writers and must never roll back a valid live
	// parent write.
	id, revision, accepted, err := d.UpsertIssueSnapshotWithLabels(ctx, testIssue(repoID, 7,
		withIssueTitle("live update"), withIssueActivity(now.Add(time.Hour))))
	require.NoError(err, "an archive-progress failure must not reject the live parent write")
	require.True(accepted)
	assert.Equal(issueID, id)
	assert.Equal(int64(2), revision)
	issue, err := d.GetIssueByRepoIDAndNumber(ctx, repoID, 7)
	require.NoError(err)
	require.NotNil(issue)
	assert.Equal("live update", issue.Title)

	// The reopen itself rolled back: the progress row stays bound to the old
	// revision until a later pass rebinds it.
	progress, err := d.GetDatasetProgress(ctx, repoID, ArchiveItemTypeIssue, 7, ArchiveDatasetComments)
	require.NoError(err)
	assert.Equal(int64(1), progress.ParentRevision)
	assert.Equal(int64(2), progress.ScanGeneration)

	// Archive parent commits stay atomic: the same bookkeeping failure
	// rejects the whole inventory page instead of committing partial state.
	err = d.CommitArchiveInventoryPage(ctx, ArchiveInventoryCommit{
		RepoID: repoID, ItemType: ArchiveItemTypeIssue, ScanGeneration: 1,
		Exhausted: true, Now: now,
		Issues: []ArchiveInventoryIssue{{
			ProviderItemID: "issue-7",
			CommentsStatus: ArchiveDatasetStatusPending,
			Snapshot: IssueSnapshot{Issue: Issue{
				RepoID: repoID, PlatformID: 7, Number: 7, State: "closed",
				CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(2 * time.Hour),
				LastActivityAt: now.Add(2 * time.Hour),
			}},
		}},
	})
	require.ErrorContains(err, "injected archive bookkeeping failure")
	issue, err = d.GetIssueByRepoIDAndNumber(ctx, repoID, 7)
	require.NoError(err)
	require.NotNil(issue)
	assert.Equal("live update", issue.Title, "the rejected archive commit must not keep partial parent state")
}

func TestArchiveCompletionReconcilesLiveStampedOmissions(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	now := archiveTestTime()

	repoID := insertTestRepo(t, d, "acme", "generation-namespaces")
	issueID := insertTestIssue(t, d, repoID, 7, "issue", now)

	liveCommit := func(keys ...string) {
		t.Helper()
		revision := issueSnapshotRevisionForTest(t, d, issueID)
		comments := make([]IssueEvent, 0, len(keys))
		for _, key := range keys {
			comments = append(comments, issueComment(key, key))
		}
		applied, err := d.CommitIssueChildSnapshot(ctx, IssueChildSnapshot{
			IssueID: issueID, ExpectedRevision: revision, Comments: comments,
		})
		require.NoError(err)
		require.True(applied)
	}
	// Two complete live passes stamp both comments with a live ingest
	// generation before the repository is ever archived.
	liveCommit("c-keep", "c-omitted")
	liveCommit("c-keep", "c-omitted")

	// Archiving the repository seeds and rebinds dataset progress for the
	// already-live parent. The generation the archive scan claims must never
	// equal a generation previously stamped by live ingestion, or the final
	// reconciliation below cannot tell an omitted live row from its own.
	require.NoError(d.EnsureDiscoveryArchives(ctx, []int64{repoID}, now))
	require.NoError(d.StartFullArchives(ctx, []int64{repoID}, now))
	var updatedAt time.Time
	require.NoError(d.ReadDB().QueryRowContext(ctx,
		`SELECT updated_at FROM middleman_issues WHERE id = ?`, issueID).Scan(&updatedAt))
	snapshot := Issue{
		RepoID: repoID, PlatformID: 7, Number: 7, State: "closed",
		CreatedAt: now.Add(-time.Hour), UpdatedAt: updatedAt.Add(time.Hour),
		LastActivityAt: updatedAt.Add(time.Hour),
	}
	require.NoError(d.CommitArchiveInventoryPage(ctx, ArchiveInventoryCommit{
		RepoID: repoID, ItemType: ArchiveItemTypeIssue, ScanGeneration: 1,
		Exhausted: true, Now: now,
		Issues: []ArchiveInventoryIssue{{
			ProviderItemID: "issue-7",
			CommentsStatus: ArchiveDatasetStatusPending,
			Snapshot:       IssueSnapshot{Issue: snapshot},
		}},
	}))
	progress, err := d.GetDatasetProgress(ctx, repoID, ArchiveItemTypeIssue, 7, ArchiveDatasetComments)
	require.NoError(err)
	require.Equal(ArchiveDatasetProgressPending, progress.Status)

	// The initial archive scan completes with a page set that omits one
	// live-stamped comment. The omission must be reconciled away.
	require.NoError(d.CommitDatasetPage(ctx, DatasetPageCommit{
		Parent:           DomainParentRef{ItemType: ArchiveItemTypeIssue, ID: issueID},
		ExpectedRevision: progress.ParentRevision,
		Dataset:          ArchiveDatasetComments,
		ScanGeneration:   progress.ScanGeneration,
		Rows:             DatasetRows{IssueComments: []IssueEvent{issueComment("c-keep", "kept")}},
		Final:            true,
		Progress: &DatasetProgressAdvance{
			RepoID: repoID, ItemNumber: 7, InputCursor: "",
		},
	}))
	assert.Equal([]string{"c-keep"}, issueCommentKeysForTest(t, d, issueID),
		"the omitted live-stamped comment must not survive archive reconciliation")
}

func TestCommitDatasetPageStatusGateMatrix(t *testing.T) {
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()

	repoID := insertTestRepo(t, d, "acme", "status-gate")
	for i, tc := range []struct {
		status  ArchiveDatasetProgressStatus
		advance bool
		blocked bool
	}{
		{status: ArchiveDatasetProgressPending, advance: true},
		{status: ArchiveDatasetProgressRunning, advance: true},
		{status: ArchiveDatasetProgressFailed, advance: true},
		{status: ArchiveDatasetProgressComplete},
		{status: ArchiveDatasetProgressUnsupported},
		{status: ArchiveDatasetProgressTerminal},
		{status: ArchiveDatasetProgressBlocked, blocked: true},
	} {
		number := i + 1
		issueID := insertTestIssue(t, d, repoID, number, "issue", archiveTestTime())
		insertArchiveItemForTest(t, d, repoID, ArchiveItemTypeIssue, number, archiveTestTime())
		key := datasetProgressKeyForTest(repoID, ArchiveItemTypeIssue, number, ArchiveDatasetComments)
		insertDatasetProgressForTest(t, d, key, 1, 1, nil, tc.status, 0)

		err := d.CommitDatasetPage(ctx, DatasetPageCommit{
			Parent:           DomainParentRef{ItemType: ArchiveItemTypeIssue, ID: issueID},
			ExpectedRevision: 1,
			Dataset:          ArchiveDatasetComments,
			ScanGeneration:   1,
			Rows:             DatasetRows{IssueComments: []IssueEvent{issueComment("c-1", "page")}},
			Progress: &DatasetProgressAdvance{
				RepoID: repoID, ItemNumber: number, InputCursor: "", NextCursor: "cursor-2",
			},
		})
		switch {
		case tc.advance:
			require.NoError(err, string(tc.status))
			require.Equal([]string{"c-1"}, issueCommentKeysForTest(t, d, issueID), string(tc.status))
		case tc.blocked:
			var blocked *ScanBlockedError
			require.ErrorAs(err, &blocked, string(tc.status))
			require.Empty(issueCommentKeysForTest(t, d, issueID), string(tc.status))
		default:
			var stale *StaleDatasetProgressError
			require.ErrorAs(err, &stale, string(tc.status))
			require.Empty(issueCommentKeysForTest(t, d, issueID), string(tc.status))
			progress, progressErr := d.GetDatasetProgress(ctx, repoID, ArchiveItemTypeIssue, number, ArchiveDatasetComments)
			require.NoError(progressErr, string(tc.status))
			require.Equal(tc.status, progress.Status, "a rejected page must not change the status")
		}
	}
}

func TestCommitDatasetPageFinalReplayAndDelayedFirstPage(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()

	repoID := insertTestRepo(t, d, "acme", "final-replay")
	issueID := insertTestIssue(t, d, repoID, 7, "issue", archiveTestTime())
	insertArchiveItemForTest(t, d, repoID, ArchiveItemTypeIssue, 7, archiveTestTime())
	key := datasetProgressKeyForTest(repoID, ArchiveItemTypeIssue, 7, ArchiveDatasetComments)
	insertDatasetProgressForTest(t, d, key, 1, 1, nil, ArchiveDatasetProgressPending, 0)

	parent := DomainParentRef{ItemType: ArchiveItemTypeIssue, ID: issueID}
	require.NoError(d.CommitDatasetPage(ctx, DatasetPageCommit{
		Parent: parent, ExpectedRevision: 1,
		Dataset: ArchiveDatasetComments, ScanGeneration: 1,
		Rows: DatasetRows{IssueComments: []IssueEvent{issueComment("c-1", "page one")}},
		Progress: &DatasetProgressAdvance{
			RepoID: repoID, ItemNumber: 7, InputCursor: "", NextCursor: "cursor-2",
		},
	}))
	final := DatasetPageCommit{
		Parent: parent, ExpectedRevision: 1,
		Dataset: ArchiveDatasetComments, ScanGeneration: 1,
		Rows:  DatasetRows{IssueComments: []IssueEvent{issueComment("c-2", "final")}},
		Final: true,
		Progress: &DatasetProgressAdvance{
			RepoID: repoID, ItemNumber: 7, InputCursor: "cursor-2",
		},
	}
	require.NoError(d.CommitDatasetPage(ctx, final))

	progress, err := d.GetDatasetProgress(ctx, repoID, ArchiveItemTypeIssue, 7, ArchiveDatasetComments)
	require.NoError(err)
	assert.Equal(ArchiveDatasetProgressComplete, progress.Status)
	require.NotNil(progress.LastInputCursor,
		"completion must retain the final input cursor so a replay is distinguishable")
	assert.Equal("cursor-2", *progress.LastInputCursor)

	// The exact final page delivered again is the already-committed page: an
	// idempotent no-op that must not reconcile or advance anything.
	replay := final
	replay.Rows = DatasetRows{IssueComments: []IssueEvent{issueComment("c-2", "changed on replay")}}
	require.NoError(d.CommitDatasetPage(ctx, replay))
	assert.Equal([]string{"c-1", "c-2"}, issueCommentKeysForTest(t, d, issueID))
	var body string
	require.NoError(d.ReadDB().QueryRowContext(ctx, `
		SELECT body FROM middleman_issue_events
		WHERE issue_id = ? AND dedupe_key = 'c-2'`, issueID).Scan(&body))
	assert.Equal("final", body)
	progress, err = d.GetDatasetProgress(ctx, repoID, ArchiveItemTypeIssue, 7, ArchiveDatasetComments)
	require.NoError(err)
	assert.Equal(2, progress.PageCount)
	assert.Equal(2, progress.ObservedCount)

	// A delayed duplicate of the empty-cursor first page must not match the
	// completed row: it is stale delivery, not a replay of the final page.
	delayed := DatasetPageCommit{
		Parent: parent, ExpectedRevision: 1,
		Dataset: ArchiveDatasetComments, ScanGeneration: 1,
		Rows: DatasetRows{IssueComments: []IssueEvent{issueComment("c-late", "late first page")}},
		Progress: &DatasetProgressAdvance{
			RepoID: repoID, ItemNumber: 7, InputCursor: "", NextCursor: "cursor-2",
		},
	}
	err = d.CommitDatasetPage(ctx, delayed)
	var stale *StaleDatasetProgressError
	require.ErrorAs(err, &stale)
	assert.Equal([]string{"c-1", "c-2"}, issueCommentKeysForTest(t, d, issueID))
	progress, err = d.GetDatasetProgress(ctx, repoID, ArchiveItemTypeIssue, 7, ArchiveDatasetComments)
	require.NoError(err)
	assert.Equal(ArchiveDatasetProgressComplete, progress.Status)
}

func TestLiveSatisfactionFencesInFlightArchiveScan(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	now := archiveTestTime()

	repoID := insertTestRepo(t, d, "acme", "live-wins-race")
	require.NoError(d.EnsureDiscoveryArchives(ctx, []int64{repoID}, now))
	issueID := insertTestIssue(t, d, repoID, 7, "issue", now)
	insertArchiveItemForTest(t, d, repoID, ArchiveItemTypeIssue, 7, now)
	key := datasetProgressKeyForTest(repoID, ArchiveItemTypeIssue, 7, ArchiveDatasetComments)
	// The archive claimed this dataset (generation 1, first page in flight
	// with an empty input cursor) before live sync completed the dataset.
	insertDatasetProgressForTest(t, d, key, 1, 1, nil, ArchiveDatasetProgressRunning, 0)

	parent := DomainParentRef{ItemType: ArchiveItemTypeIssue, ID: issueID}
	require.NoError(d.CommitDatasetPage(ctx, DatasetPageCommit{
		Parent: parent, ExpectedRevision: 1,
		Dataset: ArchiveDatasetComments,
		Rows:    DatasetRows{IssueComments: []IssueEvent{issueComment("live-1", "live")}},
		Final:   true,
	}))
	progress, err := d.GetDatasetProgress(ctx, repoID, ArchiveItemTypeIssue, 7, ArchiveDatasetComments)
	require.NoError(err)
	require.Equal(ArchiveDatasetProgressComplete, progress.Status)
	assert.Equal(int64(2), progress.ScanGeneration,
		"live satisfaction must claim the scan by incrementing the generation")

	// The in-flight archive first page now loses the compare-and-swap: its
	// generation is superseded even though its empty input cursor matches the
	// cleared cursor of the satisfied row.
	err = d.CommitDatasetPage(ctx, DatasetPageCommit{
		Parent: parent, ExpectedRevision: 1,
		Dataset: ArchiveDatasetComments, ScanGeneration: 1,
		Rows:  DatasetRows{IssueComments: []IssueEvent{issueComment("archive-1", "stale scan")}},
		Final: true,
		Progress: &DatasetProgressAdvance{
			RepoID: repoID, ItemNumber: 7, InputCursor: "",
		},
	})
	var stale *StaleDatasetProgressError
	require.ErrorAs(err, &stale)
	assert.Equal([]string{"live-1"}, issueCommentKeysForTest(t, d, issueID),
		"the stale archive page must neither write nor reconcile away live comments")
	progress, err = d.GetDatasetProgress(ctx, repoID, ArchiveItemTypeIssue, 7, ArchiveDatasetComments)
	require.NoError(err)
	assert.Equal(ArchiveDatasetProgressComplete, progress.Status)
	assert.Equal(int64(2), progress.ScanGeneration)
}

func TestCommitParentLookupPresentBindsProgressAndReopensChildren(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	now := archiveTestTime()

	repoID := insertTestRepo(t, d, "acme", "lookup-present")
	issueID := insertTestIssue(t, d, repoID, 7, "before", now)
	insertArchiveItemForTest(t, d, repoID, ArchiveItemTypeIssue, 7, now)
	commentsKey := datasetProgressKeyForTest(repoID, ArchiveItemTypeIssue, 7, ArchiveDatasetComments)
	insertDatasetProgressForTest(t, d, commentsKey, 1, 1, nil, ArchiveDatasetProgressComplete, 3)
	insertIssueCommentEventForTest(t, d, issueID, "kept", "", 1)

	refreshed := testIssue(repoID, 7,
		withIssueTitle("after"), withIssueActivity(now.Add(time.Hour)))
	require.NoError(d.CommitParentLookup(ctx, ParentLookupCommit{
		RepoID:           repoID,
		ItemType:         ArchiveItemTypeIssue,
		ItemNumber:       7,
		ScanGeneration:   1,
		ExpectedRevision: 1,
		Outcome:          ArchiveLookupPresent,
		Issue:            refreshed,
		Now:              now.Add(time.Hour),
	}))

	assert.Equal(int64(2), issueSnapshotRevisionForTest(t, d, issueID))
	var title string
	require.NoError(d.ReadDB().QueryRowContext(ctx,
		`SELECT title FROM middleman_issues WHERE id = ?`, issueID).Scan(&title))
	assert.Equal("after", title)

	lookup, err := d.GetDatasetProgress(ctx, repoID, ArchiveItemTypeIssue, 7, ArchiveDatasetLookup)
	require.NoError(err)
	assert.Equal(ArchiveDatasetProgressComplete, lookup.Status)
	assert.Equal(int64(2), lookup.ParentRevision)
	assert.NotNil(lookup.CompletedAt)

	comments, err := d.GetDatasetProgress(ctx, repoID, ArchiveItemTypeIssue, 7, ArchiveDatasetComments)
	require.NoError(err)
	assert.Equal(ArchiveDatasetProgressPending, comments.Status, "superseded child dataset reopens")
	assert.Equal(int64(2), comments.ScanGeneration)
	assert.Equal(int64(2), comments.ParentRevision)
	assert.Zero(comments.PageCount)
	assert.Nil(comments.NextCursor)
	assert.Equal([]string{"kept"}, issueCommentKeysForTest(t, d, issueID),
		"existing rows are retained until the new generation reconciles")
}

func TestCommitParentLookupTerminalOutcomes(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	now := archiveTestTime()

	repoID := insertTestRepo(t, d, "acme", "lookup-terminal")
	for _, tc := range []struct {
		number    int
		outcome   ArchiveLookupOutcome
		lifecycle ArchiveLifecycleState
		code      string
	}{
		{number: 1, outcome: ArchiveLookupRemoved, lifecycle: ArchiveLifecycleStateRemovedUpstream, code: "removed_upstream"},
		{number: 2, outcome: ArchiveLookupInaccessible, lifecycle: ArchiveLifecycleStateInaccessible, code: "access_denied"},
	} {
		issueID := insertTestIssue(t, d, repoID, tc.number, "issue", now)
		insertArchiveItemForTest(t, d, repoID, ArchiveItemTypeIssue, tc.number, now)
		insertIssueCommentEventForTest(t, d, issueID, "kept", "", nil)

		require.NoError(d.CommitParentLookup(ctx, ParentLookupCommit{
			RepoID:           repoID,
			ItemType:         ArchiveItemTypeIssue,
			ItemNumber:       tc.number,
			ScanGeneration:   1,
			ExpectedRevision: 1,
			Outcome:          tc.outcome,
			ErrorCode:        tc.code,
			ErrorDetail:      "provider says no",
			Now:              now,
		}))

		var lifecycle ArchiveLifecycleState
		require.NoError(d.ReadDB().QueryRowContext(ctx, `
			SELECT lifecycle_state FROM middleman_archive_items
			WHERE repo_id = ? AND item_type = 'issue' AND item_number = ?`,
			repoID, tc.number).Scan(&lifecycle))
		assert.Equal(tc.lifecycle, lifecycle)

		lookup, err := d.GetDatasetProgress(ctx, repoID, ArchiveItemTypeIssue, tc.number, ArchiveDatasetLookup)
		require.NoError(err)
		assert.Equal(ArchiveDatasetProgressTerminal, lookup.Status)
		require.NotNil(lookup.LastErrorCode)
		assert.Equal(tc.code, *lookup.LastErrorCode)

		assert.Equal([]string{"kept"}, issueCommentKeysForTest(t, d, issueID),
			"archived content is deliberately retained")
	}
}

func TestCommitParentLookupMovedQueuesDestinationPrompt(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	now := archiveTestTime()

	sourceRepoID := insertTestRepo(t, d, "acme", "moved-source")
	destinationRepoID := insertTestRepo(t, d, "acme", "moved-destination")
	require.NoError(d.EnsureDiscoveryArchives(ctx, []int64{sourceRepoID, destinationRepoID}, now))
	require.NoError(d.StartFullArchives(ctx, []int64{destinationRepoID}, now))

	insertTestIssue(t, d, sourceRepoID, 7, "moved away", now)
	insertArchiveItemForTest(t, d, sourceRepoID, ArchiveItemTypeIssue, 7, now)

	require.NoError(d.CommitParentLookup(ctx, ParentLookupCommit{
		RepoID:           sourceRepoID,
		ItemType:         ArchiveItemTypeIssue,
		ItemNumber:       7,
		ScanGeneration:   1,
		ExpectedRevision: 1,
		Outcome:          ArchiveLookupMoved,
		Destination: &RepoIdentity{
			Platform: "github", PlatformHost: "github.com",
			Owner: "acme", Name: "moved-destination",
		},
		ErrorCode:   "moved",
		ErrorDetail: "item moved to acme/moved-destination",
		Now:         now,
	}))

	var lifecycle ArchiveLifecycleState
	require.NoError(d.ReadDB().QueryRowContext(ctx, `
		SELECT lifecycle_state FROM middleman_archive_items
		WHERE repo_id = ? AND item_type = 'issue' AND item_number = 7`,
		sourceRepoID).Scan(&lifecycle))
	assert.Equal(ArchiveLifecycleStateRemovedUpstream, lifecycle)

	lookup, err := d.GetDatasetProgress(ctx, sourceRepoID, ArchiveItemTypeIssue, 7, ArchiveDatasetLookup)
	require.NoError(err)
	assert.Equal(ArchiveDatasetProgressTerminal, lookup.Status)

	states, err := d.ListArchiveRepoStates(ctx, []int64{destinationRepoID})
	require.NoError(err)
	require.Len(states, 1)
	assert.NotNil(states[0].MaintenanceWatermark, "destination prompt scan is queued")
	assert.Nil(states[0].MaintenanceSucceededAt)
}

func TestCommitParentLookupStaleGenerationAfterResetWritesNothing(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	now := archiveTestTime()

	repoID := insertTestRepo(t, d, "acme", "lookup-stale")
	issueID := insertTestIssue(t, d, repoID, 7, "before", now)
	insertArchiveItemForTest(t, d, repoID, ArchiveItemTypeIssue, 7, now)
	lookupKey := datasetProgressKeyForTest(repoID, ArchiveItemTypeIssue, 7, ArchiveDatasetLookup)
	insertDatasetProgressForTest(t, d, lookupKey, 1, 1, nil, ArchiveDatasetProgressPending, 0)
	require.NoError(d.ResetDatasetProgress(ctx, lookupKey))

	err := d.CommitParentLookup(ctx, ParentLookupCommit{
		RepoID:         repoID,
		ItemType:       ArchiveItemTypeIssue,
		ItemNumber:     7,
		ScanGeneration: 1,
		Outcome:        ArchiveLookupPresent,
		Issue: testIssue(repoID, 7,
			withIssueTitle("stale lookup"), withIssueActivity(now.Add(time.Hour))),
		Now: now.Add(time.Hour),
	})
	var stale *StaleDatasetProgressError
	require.ErrorAs(err, &stale)
	assert.Equal(int64(1), stale.ExpectedGeneration)
	assert.Equal(int64(2), stale.GotGeneration)

	assert.Equal(int64(1), issueSnapshotRevisionForTest(t, d, issueID),
		"a stale lookup must not upsert the parent")
	var title string
	require.NoError(d.ReadDB().QueryRowContext(ctx,
		`SELECT title FROM middleman_issues WHERE id = ?`, issueID).Scan(&title))
	assert.Equal("before", title)
	lookup, err := d.GetDatasetProgress(ctx, repoID, ArchiveItemTypeIssue, 7, ArchiveDatasetLookup)
	require.NoError(err)
	assert.Equal(ArchiveDatasetProgressPending, lookup.Status)
	assert.Equal(int64(2), lookup.ScanGeneration)
}

func TestCommitParentLookupDuplicatePresentIsIdempotentReplay(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	now := archiveTestTime()

	repoID := insertTestRepo(t, d, "acme", "lookup-duplicate")
	issueID := insertTestIssue(t, d, repoID, 7, "before", now)
	insertArchiveItemForTest(t, d, repoID, ArchiveItemTypeIssue, 7, now)
	lookupKey := datasetProgressKeyForTest(repoID, ArchiveItemTypeIssue, 7, ArchiveDatasetLookup)
	insertDatasetProgressForTest(t, d, lookupKey, 1, 1, nil, ArchiveDatasetProgressPending, 0)

	commit := ParentLookupCommit{
		RepoID:           repoID,
		ItemType:         ArchiveItemTypeIssue,
		ItemNumber:       7,
		ScanGeneration:   1,
		ExpectedRevision: 1,
		Outcome:          ArchiveLookupPresent,
		Issue: testIssue(repoID, 7,
			withIssueTitle("after"), withIssueActivity(now.Add(time.Hour))),
		Now: now.Add(time.Hour),
	}
	require.NoError(d.CommitParentLookup(ctx, commit))
	require.Equal(int64(2), issueSnapshotRevisionForTest(t, d, issueID))
	comments, err := d.GetDatasetProgress(ctx, repoID, ArchiveItemTypeIssue, 7, ArchiveDatasetComments)
	require.NoError(err)
	firstGeneration := comments.ScanGeneration

	// The same completed lookup delivered again is an idempotent replay: no
	// parent re-upsert, no revision churn, no child reopen.
	require.NoError(d.CommitParentLookup(ctx, commit))
	assert.Equal(int64(2), issueSnapshotRevisionForTest(t, d, issueID),
		"a duplicate lookup must not advance the parent revision again")
	comments, err = d.GetDatasetProgress(ctx, repoID, ArchiveItemTypeIssue, 7, ArchiveDatasetComments)
	require.NoError(err)
	assert.Equal(firstGeneration, comments.ScanGeneration,
		"a duplicate lookup must not reopen child datasets again")
	lookup, err := d.GetDatasetProgress(ctx, repoID, ArchiveItemTypeIssue, 7, ArchiveDatasetLookup)
	require.NoError(err)
	assert.Equal(ArchiveDatasetProgressComplete, lookup.Status)
	assert.Equal(int64(2), lookup.ParentRevision)
}

func TestCommitParentLookupLateConflictingOutcomeRejected(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	now := archiveTestTime()

	repoID := insertTestRepo(t, d, "acme", "lookup-conflict")

	// A terminal outcome delivered after the same generation already
	// completed present must not bury the item.
	insertTestIssue(t, d, repoID, 1, "present", now)
	insertArchiveItemForTest(t, d, repoID, ArchiveItemTypeIssue, 1, now)
	insertDatasetProgressForTest(t, d,
		datasetProgressKeyForTest(repoID, ArchiveItemTypeIssue, 1, ArchiveDatasetLookup),
		1, 1, nil, ArchiveDatasetProgressPending, 0)
	require.NoError(d.CommitParentLookup(ctx, ParentLookupCommit{
		RepoID: repoID, ItemType: ArchiveItemTypeIssue, ItemNumber: 1,
		ScanGeneration: 1, ExpectedRevision: 1,
		Outcome: ArchiveLookupPresent,
		Issue: testIssue(repoID, 1,
			withIssueTitle("refreshed"), withIssueActivity(now.Add(time.Hour))),
		Now: now.Add(time.Hour),
	}))
	err := d.CommitParentLookup(ctx, ParentLookupCommit{
		RepoID: repoID, ItemType: ArchiveItemTypeIssue, ItemNumber: 1,
		ScanGeneration: 1, ExpectedRevision: 2,
		Outcome: ArchiveLookupRemoved, ErrorCode: "removed_upstream",
		Now: now.Add(2 * time.Hour),
	})
	var stale *StaleDatasetProgressError
	require.ErrorAs(err, &stale)
	var lifecycle ArchiveLifecycleState
	require.NoError(d.ReadDB().QueryRowContext(ctx, `
		SELECT lifecycle_state FROM middleman_archive_items
		WHERE repo_id = ? AND item_type = 'issue' AND item_number = 1`,
		repoID).Scan(&lifecycle))
	assert.Equal(ArchiveLifecycleStateActive, lifecycle)
	lookup, err := d.GetDatasetProgress(ctx, repoID, ArchiveItemTypeIssue, 1, ArchiveDatasetLookup)
	require.NoError(err)
	assert.Equal(ArchiveDatasetProgressComplete, lookup.Status)

	// A present outcome delivered after the same generation already recorded
	// terminal is equally conflicting.
	terminalID := insertTestIssue(t, d, repoID, 2, "terminal", now)
	insertArchiveItemForTest(t, d, repoID, ArchiveItemTypeIssue, 2, now)
	insertDatasetProgressForTest(t, d,
		datasetProgressKeyForTest(repoID, ArchiveItemTypeIssue, 2, ArchiveDatasetLookup),
		1, 1, nil, ArchiveDatasetProgressPending, 0)
	require.NoError(d.CommitParentLookup(ctx, ParentLookupCommit{
		RepoID: repoID, ItemType: ArchiveItemTypeIssue, ItemNumber: 2,
		ScanGeneration: 1, ExpectedRevision: 1,
		Outcome: ArchiveLookupRemoved, ErrorCode: "removed_upstream",
		Now: now,
	}))
	err = d.CommitParentLookup(ctx, ParentLookupCommit{
		RepoID: repoID, ItemType: ArchiveItemTypeIssue, ItemNumber: 2,
		ScanGeneration: 1, ExpectedRevision: 1,
		Outcome: ArchiveLookupPresent,
		Issue: testIssue(repoID, 2,
			withIssueTitle("conflicting present"), withIssueActivity(now.Add(time.Hour))),
		Now: now.Add(time.Hour),
	})
	require.ErrorAs(err, &stale)
	assert.Equal(int64(1), issueSnapshotRevisionForTest(t, d, terminalID),
		"a conflicting present outcome must not upsert the parent")
	require.NoError(d.ReadDB().QueryRowContext(ctx, `
		SELECT lifecycle_state FROM middleman_archive_items
		WHERE repo_id = ? AND item_type = 'issue' AND item_number = 2`,
		repoID).Scan(&lifecycle))
	assert.Equal(ArchiveLifecycleStateRemovedUpstream, lifecycle)
}

func TestCommitParentLookupBlockedRowRejects(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	now := archiveTestTime()

	repoID := insertTestRepo(t, d, "acme", "lookup-blocked")
	issueID := insertTestIssue(t, d, repoID, 7, "before", now)
	insertArchiveItemForTest(t, d, repoID, ArchiveItemTypeIssue, 7, now)
	lookupKey := datasetProgressKeyForTest(repoID, ArchiveItemTypeIssue, 7, ArchiveDatasetLookup)
	insertDatasetProgressForTest(t, d, lookupKey, 1, 1, nil, ArchiveDatasetProgressBlocked, 0)

	err := d.CommitParentLookup(ctx, ParentLookupCommit{
		RepoID: repoID, ItemType: ArchiveItemTypeIssue, ItemNumber: 7,
		ScanGeneration: 1, ExpectedRevision: 1,
		Outcome: ArchiveLookupPresent,
		Issue: testIssue(repoID, 7,
			withIssueTitle("after"), withIssueActivity(now.Add(time.Hour))),
		Now: now.Add(time.Hour),
	})
	var blocked *ScanBlockedError
	require.ErrorAs(err, &blocked)
	assert.Equal(int64(1), issueSnapshotRevisionForTest(t, d, issueID),
		"a blocked lookup must not upsert the parent")
	lookup, err := d.GetDatasetProgress(ctx, repoID, ArchiveItemTypeIssue, 7, ArchiveDatasetLookup)
	require.NoError(err)
	assert.Equal(ArchiveDatasetProgressBlocked, lookup.Status)
}

func TestCommitParentLookupRejectsSupersededDomainRevision(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	now := archiveTestTime()

	repoID := insertTestRepo(t, d, "acme", "lookup-revision-fence")
	issueID := insertTestIssue(t, d, repoID, 7, "before", now)
	insertArchiveItemForTest(t, d, repoID, ArchiveItemTypeIssue, 7, now)
	lookupKey := datasetProgressKeyForTest(repoID, ArchiveItemTypeIssue, 7, ArchiveDatasetLookup)
	insertDatasetProgressForTest(t, d, lookupKey, 1, 1, nil, ArchiveDatasetProgressPending, 0)

	// Live sync advances the parent between the lookup claim and its commit.
	_, err := d.UpsertIssue(ctx, testIssue(repoID, 7,
		withIssueTitle("live wins"), withIssueActivity(now.Add(2*time.Hour))))
	require.NoError(err)
	require.Equal(int64(2), issueSnapshotRevisionForTest(t, d, issueID))

	err = d.CommitParentLookup(ctx, ParentLookupCommit{
		RepoID: repoID, ItemType: ArchiveItemTypeIssue, ItemNumber: 7,
		ScanGeneration: 1, ExpectedRevision: 1,
		Outcome: ArchiveLookupPresent,
		Issue: testIssue(repoID, 7,
			withIssueTitle("stale lookup snapshot"), withIssueActivity(now.Add(time.Hour))),
		Now: now.Add(time.Hour),
	})
	var stale *StaleDatasetProgressError
	require.ErrorAs(err, &stale)
	assert.Equal(int64(1), stale.ExpectedRevision)
	assert.Equal(int64(2), stale.GotRevision)
	var title string
	require.NoError(d.ReadDB().QueryRowContext(ctx,
		`SELECT title FROM middleman_issues WHERE id = ?`, issueID).Scan(&title))
	assert.Equal("live wins", title)
	assert.Equal(int64(2), issueSnapshotRevisionForTest(t, d, issueID))

	// A terminal conclusion drawn before the live observation is equally
	// superseded: the item observably exists.
	err = d.CommitParentLookup(ctx, ParentLookupCommit{
		RepoID: repoID, ItemType: ArchiveItemTypeIssue, ItemNumber: 7,
		ScanGeneration: 1, ExpectedRevision: 1,
		Outcome: ArchiveLookupRemoved, ErrorCode: "removed_upstream",
		Now: now.Add(time.Hour),
	})
	require.ErrorAs(err, &stale)
	var lifecycle ArchiveLifecycleState
	require.NoError(d.ReadDB().QueryRowContext(ctx, `
		SELECT lifecycle_state FROM middleman_archive_items
		WHERE repo_id = ? AND item_type = 'issue' AND item_number = 7`,
		repoID).Scan(&lifecycle))
	assert.Equal(ArchiveLifecycleStateActive, lifecycle)
	lookup, err := d.GetDatasetProgress(ctx, repoID, ArchiveItemTypeIssue, 7, ArchiveDatasetLookup)
	require.NoError(err)
	assert.Equal(ArchiveDatasetProgressPending, lookup.Status,
		"the rejected lookup stays claimable for a rescan at the new revision")
}

func TestPresentObservationReactivatesBothTerminalLifecycles(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	now := archiveTestTime()

	repoID := insertTestRepo(t, d, "acme", "terminal-recovery")
	for i, tc := range []struct {
		outcome   ArchiveLookupOutcome
		lifecycle ArchiveLifecycleState
		code      string
	}{
		{outcome: ArchiveLookupRemoved, lifecycle: ArchiveLifecycleStateRemovedUpstream, code: "removed_upstream"},
		{outcome: ArchiveLookupInaccessible, lifecycle: ArchiveLifecycleStateInaccessible, code: "access_denied"},
	} {
		number := i + 1
		insertTestIssue(t, d, repoID, number, "issue", now)
		insertArchiveItemForTest(t, d, repoID, ArchiveItemTypeIssue, number, now)
		lookupKey := datasetProgressKeyForTest(repoID, ArchiveItemTypeIssue, number, ArchiveDatasetLookup)
		insertDatasetProgressForTest(t, d, lookupKey, 1, 1, nil, ArchiveDatasetProgressPending, 0)
		require.NoError(d.CommitParentLookup(ctx, ParentLookupCommit{
			RepoID: repoID, ItemType: ArchiveItemTypeIssue, ItemNumber: number,
			ScanGeneration: 1, ExpectedRevision: 1,
			Outcome: tc.outcome, ErrorCode: tc.code,
			Now: now,
		}))
		var lifecycle ArchiveLifecycleState
		require.NoError(d.ReadDB().QueryRowContext(ctx, `
			SELECT lifecycle_state FROM middleman_archive_items
			WHERE repo_id = ? AND item_type = 'issue' AND item_number = ?`,
			repoID, number).Scan(&lifecycle))
		require.Equal(tc.lifecycle, lifecycle)

		// An operator reset returns the lookup to pending; the next present
		// observation must recover the item regardless of which terminal
		// lifecycle it was in and regardless of the provider timestamp — the
		// recovered snapshot deliberately carries the same updated_at.
		require.NoError(d.ResetDatasetProgress(ctx, lookupKey))
		require.NoError(d.CommitParentLookup(ctx, ParentLookupCommit{
			RepoID: repoID, ItemType: ArchiveItemTypeIssue, ItemNumber: number,
			ScanGeneration: 2, ExpectedRevision: 1,
			Outcome: ArchiveLookupPresent,
			Issue: testIssue(repoID, number,
				withIssueTitle("recovered"), withIssueActivity(now)),
			Now: now.Add(time.Hour),
		}), string(tc.lifecycle))
		require.NoError(d.ReadDB().QueryRowContext(ctx, `
			SELECT lifecycle_state FROM middleman_archive_items
			WHERE repo_id = ? AND item_type = 'issue' AND item_number = ?`,
			repoID, number).Scan(&lifecycle))
		assert.Equal(ArchiveLifecycleStateActive, lifecycle, string(tc.lifecycle))
		lookup, err := d.GetDatasetProgress(ctx, repoID, ArchiveItemTypeIssue, number, ArchiveDatasetLookup)
		require.NoError(err)
		assert.Equal(ArchiveDatasetProgressComplete, lookup.Status, string(tc.lifecycle))
	}
}

func TestInventoryCommitSeedsDatasetProgressForPageCommits(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	now := archiveTestTime()

	repoID := insertTestRepo(t, d, "acme", "inventory-seeds")
	require.NoError(d.EnsureDiscoveryArchives(ctx, []int64{repoID}, now))
	require.NoError(d.StartFullArchives(ctx, []int64{repoID}, now))

	issue := *testIssue(repoID, 7, withIssueTitle("issue"), withIssueActivity(now))
	require.NoError(d.CommitArchiveInventoryPage(ctx, ArchiveInventoryCommit{
		RepoID: repoID, ItemType: ArchiveItemTypeIssue, Exhausted: true, Now: now,
		Issues: []ArchiveInventoryIssue{{
			ProviderItemID: "issue-7",
			Snapshot:       IssueSnapshot{Issue: issue},
			CommentsStatus: ArchiveDatasetStatusPending,
		}},
	}))
	require.NoError(d.CommitArchiveInventoryPage(ctx, ArchiveInventoryCommit{
		RepoID: repoID, ItemType: ArchiveItemTypeMergeRequest, Exhausted: true, Now: now,
		MergeRequests: []ArchiveInventoryMergeRequest{{
			ProviderItemID: "mr-9",
			Snapshot: MergeRequestSnapshot{MergeRequest: MergeRequest{
				RepoID: repoID, PlatformID: 9, Number: 9, Title: "mr",
				State: MergeRequestStateOpen, CreatedAt: now.Add(-time.Hour),
				UpdatedAt: now, LastActivityAt: now,
			}},
			CommentsStatus:       ArchiveDatasetStatusPending,
			ReviewsStatus:        ArchiveDatasetStatusPending,
			InlineCommentsStatus: ArchiveDatasetStatusUnsupported,
		}},
	}))

	// Every applicable dataset progress row exists after the parent
	// observation, without any manual progress inserts.
	for _, tc := range []struct {
		itemType ArchiveItemType
		number   int
		dataset  ArchiveDataset
		status   ArchiveDatasetProgressStatus
	}{
		{ArchiveItemTypeIssue, 7, ArchiveDatasetLookup, ArchiveDatasetProgressPending},
		{ArchiveItemTypeIssue, 7, ArchiveDatasetComments, ArchiveDatasetProgressPending},
		{ArchiveItemTypeMergeRequest, 9, ArchiveDatasetLookup, ArchiveDatasetProgressPending},
		{ArchiveItemTypeMergeRequest, 9, ArchiveDatasetComments, ArchiveDatasetProgressPending},
		{ArchiveItemTypeMergeRequest, 9, ArchiveDatasetReviews, ArchiveDatasetProgressPending},
		{ArchiveItemTypeMergeRequest, 9, ArchiveDatasetInlineComments, ArchiveDatasetProgressUnsupported},
	} {
		progress, err := d.GetDatasetProgress(ctx, repoID, tc.itemType, tc.number, tc.dataset)
		require.NoError(err, "%s %d %s", tc.itemType, tc.number, tc.dataset)
		assert.Equal(tc.status, progress.Status, "%s %d %s", tc.itemType, tc.number, tc.dataset)
	}

	// The seeded rows accept an archive page commit directly.
	issueID, revision, err := d.GetDomainParent(ctx, repoID, ArchiveItemTypeIssue, 7)
	require.NoError(err)
	seeded, err := d.GetDatasetProgress(ctx, repoID, ArchiveItemTypeIssue, 7, ArchiveDatasetComments)
	require.NoError(err)
	require.NoError(d.CommitDatasetPage(ctx, DatasetPageCommit{
		Parent:           DomainParentRef{ItemType: ArchiveItemTypeIssue, ID: issueID},
		ExpectedRevision: revision,
		Dataset:          ArchiveDatasetComments,
		ScanGeneration:   seeded.ScanGeneration,
		Rows:             DatasetRows{IssueComments: []IssueEvent{issueComment("c-1", "archive page")}},
		Final:            true,
		Progress: &DatasetProgressAdvance{
			RepoID: repoID, ItemNumber: 7, InputCursor: "",
		},
	}))
	progress, err := d.GetDatasetProgress(ctx, repoID, ArchiveItemTypeIssue, 7, ArchiveDatasetComments)
	require.NoError(err)
	assert.Equal(ArchiveDatasetProgressComplete, progress.Status)
	assert.Equal([]string{"c-1"}, issueCommentKeysForTest(t, d, issueID))
}

func TestCommitDatasetPageRejectsFinalPageWithNextCursor(t *testing.T) {
	require := require.New(t)
	database := openTestDB(t)
	ctx := t.Context()

	err := database.CommitDatasetPage(ctx, DatasetPageCommit{
		Parent: DomainParentRef{
			ItemType: ArchiveItemTypeIssue,
			ID:       1,
		},
		Dataset:        ArchiveDatasetComments,
		ScanGeneration: 1,
		Final:          true,
		Progress: &DatasetProgressAdvance{
			NextCursor: "page-2",
		},
	})
	require.ErrorContains(err, "final page must not carry a next cursor")
}

func TestRepositoryFailureFromSupersededDatasetClaimIsStaleNoOp(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	now := archiveTestTime()
	repoID := insertTestRepo(t, d, "acme", "repo-failure-fence")
	require.NoError(d.EnsureDiscoveryArchives(ctx, []int64{repoID}, now))
	insertTestIssue(t, d, repoID, 7, "issue", now)
	insertArchiveItemForTest(t, d, repoID, ArchiveItemTypeIssue, 7, now)
	key := datasetProgressKeyForTest(repoID, ArchiveItemTypeIssue, 7, ArchiveDatasetComments)
	insertDatasetProgressForTest(t, d, key, 1, 4, nil, ArchiveDatasetProgressRunning, 0)
	retry := now.Add(time.Hour)

	applied, err := d.FailDatasetProgressRecordingRepositoryFailure(
		ctx, key, 2, 1, ArchiveErrorCodeAuthentication, "stale auth", &retry, now,
	)
	require.NoError(err)
	assert.False(applied)
	progress, err := d.GetDatasetProgress(ctx, repoID, ArchiveItemTypeIssue, 7, ArchiveDatasetComments)
	require.NoError(err)
	assert.Equal(ArchiveDatasetProgressRunning, progress.Status)
	states, err := d.ListArchiveRepoStates(ctx, []int64{repoID})
	require.NoError(err)
	require.Len(states, 1)
	assert.Nil(states[0].LastErrorCode)

	applied, err = d.FailDatasetProgressRecordingRepositoryFailure(
		ctx, key, 4, 1, ArchiveErrorCodeAuthentication, "current auth", &retry, now,
	)
	require.NoError(err)
	assert.True(applied)
	progress, err = d.GetDatasetProgress(ctx, repoID, ArchiveItemTypeIssue, 7, ArchiveDatasetComments)
	require.NoError(err)
	assert.Equal(ArchiveDatasetProgressFailed, progress.Status)
	states, err = d.ListArchiveRepoStates(ctx, []int64{repoID})
	require.NoError(err)
	require.Len(states, 1)
	require.NotNil(states[0].LastErrorCode)
	assert.Equal(string(ArchiveErrorCodeAuthentication), *states[0].LastErrorCode)
}

func TestEqualTimestampParentObservationRepairsStrandedReopen(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	now := archiveTestTime()
	repoID := insertTestRepo(t, d, "acme", "stranded-reopen-repair")
	mr := &MergeRequest{
		RepoID: repoID, PlatformID: 9, Number: 9, Title: "mr",
		State: MergeRequestStateOpen, CreatedAt: now.Add(-2 * time.Hour),
		UpdatedAt: now, LastActivityAt: now,
	}
	_, err := d.UpsertMergeRequest(ctx, mr)
	require.NoError(err)
	parent, err := d.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 9)
	require.NoError(err)
	require.NotNil(parent)
	preWriteRevision := parent.SnapshotRevision

	// A previously discarded best-effort reopen left comments stranded below
	// the pre-write revision while reviews sit in the healthy satisfied state
	// bound exactly to it.
	insertArchiveItemForTest(t, d, repoID, ArchiveItemTypeMergeRequest, 9, now)
	stranded := datasetProgressKeyForTest(repoID, ArchiveItemTypeMergeRequest, 9, ArchiveDatasetComments)
	insertDatasetProgressForTest(t, d, stranded, preWriteRevision-1, 4, nil, ArchiveDatasetProgressComplete, 1)
	healthy := datasetProgressKeyForTest(repoID, ArchiveItemTypeMergeRequest, 9, ArchiveDatasetReviews)
	insertDatasetProgressForTest(t, d, healthy, preWriteRevision, 6, nil, ArchiveDatasetProgressComplete, 1)

	// Equal-timestamp accepted refresh: no strictly-newer content, so the old
	// gate never retried the reopen and the stranded row persisted forever.
	refresh := *mr
	_, err = d.UpsertMergeRequest(ctx, &refresh)
	require.NoError(err)
	parent, err = d.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 9)
	require.NoError(err)
	require.NotNil(parent)

	repaired, err := d.GetDatasetProgress(ctx, repoID, ArchiveItemTypeMergeRequest, 9, ArchiveDatasetComments)
	require.NoError(err)
	assert.Equal(ArchiveDatasetProgressPending, repaired.Status)
	assert.Equal(parent.SnapshotRevision, repaired.ParentRevision)
	untouched, err := d.GetDatasetProgress(ctx, repoID, ArchiveItemTypeMergeRequest, 9, ArchiveDatasetReviews)
	require.NoError(err)
	assert.Equal(ArchiveDatasetProgressComplete, untouched.Status)
	assert.Equal(preWriteRevision, untouched.ParentRevision)
}
