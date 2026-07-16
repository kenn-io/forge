package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// IssueChildSnapshot is one normal-sync child payload bound to the issue
// parent revision established before provider child I/O.
type IssueChildSnapshot struct {
	IssueID          int64
	ExpectedRevision int64
	Comments         []IssueEvent
	OtherEvents      []IssueEvent
	LastActivityAt   *time.Time
}

// MergeRequestChildSnapshot is one normal-sync child payload bound to the
// merge-request parent revision established before provider child I/O.
type MergeRequestChildSnapshot struct {
	MergeRequestID   int64
	ExpectedRevision int64
	Comments         []MREvent
	CommentsComplete bool
	Reviews          []MREvent
	ReviewsComplete  bool
	InlineComments   []MREvent
	ReviewThreads    []MRReviewThread
	InlineComplete   bool
	OtherEvents      []MREvent
	Derived          *MRDerivedFields
}

// CommitIssueChildSnapshot replaces issue comments only while the parent still
// matches the snapshot that initiated the provider fetch.
func (d *DB) CommitIssueChildSnapshot(ctx context.Context, snapshot IssueChildSnapshot) (bool, error) {
	var applied bool
	err := d.Tx(ctx, func(tx *sql.Tx) error {
		current, err := domainParentSnapshotCurrentTx(
			ctx, tx, ArchiveItemTypeIssue, snapshot.IssueID, snapshot.ExpectedRevision,
		)
		if err != nil || !current {
			return err
		}
		if err := replaceIssueCommentsDatasetTx(
			ctx, tx, snapshot.IssueID, snapshot.Comments, snapshot.LastActivityAt, true,
		); err != nil {
			return err
		}
		if err := upsertIssueEventsTx(ctx, tx, snapshot.OtherEvents); err != nil {
			return err
		}
		if err := satisfyArchiveDatasetsFromNormalSyncTx(
			ctx, tx, ArchiveItemTypeIssue, snapshot.IssueID,
			[]ArchiveDataset{ArchiveDatasetComments},
		); err != nil {
			return err
		}
		applied = true
		return nil
	})
	return applied, err
}

// CommitMergeRequestChildSnapshot atomically applies every supplied event and
// review-thread family only while its parent snapshot remains current.
func (d *DB) CommitMergeRequestChildSnapshot(
	ctx context.Context,
	snapshot MergeRequestChildSnapshot,
) (bool, error) {
	var applied bool
	err := d.Tx(ctx, func(tx *sql.Tx) error {
		current, err := domainParentSnapshotCurrentTx(
			ctx, tx, ArchiveItemTypeMergeRequest, snapshot.MergeRequestID, snapshot.ExpectedRevision,
		)
		if err != nil || !current {
			return err
		}
		if snapshot.CommentsComplete {
			if err := replaceMREventDatasetTx(
				ctx, tx, snapshot.MergeRequestID, "issue_comment", snapshot.Comments, true,
			); err != nil {
				return err
			}
		} else if err := upsertMREventsTx(ctx, tx, snapshot.Comments); err != nil {
			return err
		}
		if snapshot.ReviewsComplete {
			if err := replaceMREventDatasetTx(
				ctx, tx, snapshot.MergeRequestID, "review", snapshot.Reviews, false,
			); err != nil {
				return err
			}
		} else if err := upsertMREventsTx(ctx, tx, snapshot.Reviews); err != nil {
			return err
		}
		if snapshot.InlineComplete {
			if err := replaceMRReviewThreadsDatasetTx(
				ctx, tx, snapshot.MergeRequestID,
				snapshot.ReviewThreads, snapshot.InlineComments,
			); err != nil {
				return err
			}
		}
		if snapshot.Derived != nil {
			if _, err := tx.ExecContext(ctx, `
				UPDATE middleman_merge_requests
				SET review_decision = ?, last_activity_at = ?
				WHERE id = ?`, snapshot.Derived.ReviewDecision,
				snapshot.Derived.LastActivityAt, snapshot.MergeRequestID); err != nil {
				return fmt.Errorf("update mr review activity: %w", err)
			}
		}
		if err := upsertMREventsTx(ctx, tx, snapshot.OtherEvents); err != nil {
			return err
		}
		var completeDatasets []ArchiveDataset
		if snapshot.CommentsComplete {
			completeDatasets = append(completeDatasets, ArchiveDatasetComments)
		}
		if snapshot.ReviewsComplete {
			completeDatasets = append(completeDatasets, ArchiveDatasetReviews)
		}
		if snapshot.InlineComplete {
			completeDatasets = append(completeDatasets, ArchiveDatasetInlineComments)
		}
		if err := satisfyArchiveDatasetsFromNormalSyncTx(
			ctx, tx, ArchiveItemTypeMergeRequest, snapshot.MergeRequestID, completeDatasets,
		); err != nil {
			return err
		}
		applied = true
		return nil
	})
	return applied, err
}

func replaceIssueCommentsDatasetTx(
	ctx context.Context,
	tx *sql.Tx,
	issueID int64,
	events []IssueEvent,
	lastActivityAt *time.Time,
	updateDerived bool,
) error {
	if updateDerived {
		return replaceIssueCommentEventsTx(ctx, tx, issueID, events, lastActivityAt)
	}
	keys := make([]string, len(events))
	for i := range events {
		keys[i] = events[i].DedupeKey
	}
	if err := deleteMissingIssueCommentEventsTx(ctx, tx, issueID, keys); err != nil {
		return err
	}
	return upsertIssueEventsTx(ctx, tx, events)
}

func replaceMREventDatasetTx(
	ctx context.Context,
	tx *sql.Tx,
	mergeRequestID int64,
	eventType string,
	events []MREvent,
	updateDerived bool,
) error {
	if eventType == "issue_comment" && updateDerived {
		return replaceMRCommentEventsTx(ctx, tx, mergeRequestID, events, nil)
	}
	if eventType == "review" {
		return upsertMREventsTx(ctx, tx, events)
	}
	keys := make([]string, len(events))
	for i := range events {
		keys[i] = events[i].DedupeKey
	}
	if err := deleteMissingMREventFamilyTx(ctx, tx, mergeRequestID, eventType, keys); err != nil {
		return err
	}
	return upsertMREventsTx(ctx, tx, events)
}

func replaceMRReviewThreadsDatasetTx(
	ctx context.Context,
	tx *sql.Tx,
	mergeRequestID int64,
	threads []MRReviewThread,
	events []MREvent,
) error {
	if err := upsertMRReviewThreadsTx(ctx, tx, mergeRequestID, threads); err != nil {
		return err
	}
	return upsertMREventsTx(ctx, tx, events)
}

func satisfyArchiveDatasetsFromNormalSyncTx(
	ctx context.Context,
	tx *sql.Tx,
	itemType ArchiveItemType,
	parentID int64,
	datasets []ArchiveDataset,
) error {
	if len(datasets) == 0 {
		return nil
	}
	parentTable := "middleman_issues"
	if itemType == ArchiveItemTypeMergeRequest {
		parentTable = "middleman_merge_requests"
	}
	var repoID int64
	var itemNumber int
	var updatedAt time.Time
	var revision int64
	if err := tx.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT repo_id, number, updated_at, snapshot_revision
		FROM %s WHERE id = ?`, parentTable), parentID,
	).Scan(&repoID, &itemNumber, &updatedAt, &revision); err != nil {
		return fmt.Errorf("read normal-sync archive parent: %w", err)
	}
	if err := reconcileArchiveItemSnapshotTx(
		ctx, tx, repoID, itemType, itemNumber, updatedAt, revision,
	); err != nil {
		return err
	}
	columns := map[ArchiveDataset]string{
		ArchiveDatasetComments:       "comments_status",
		ArchiveDatasetReviews:        "reviews_status",
		ArchiveDatasetInlineComments: "inline_comments_status",
	}
	for _, dataset := range datasets {
		column := columns[dataset]
		if column == "" {
			return fmt.Errorf("satisfy archive dataset from normal sync: invalid dataset %q", dataset)
		}
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM middleman_archive_dataset_pages
			WHERE repo_id = ? AND item_type = ? AND item_number = ? AND dataset = ?
			  AND snapshot_updated_at = ?`,
			repoID, itemType, itemNumber, dataset, updatedAt,
		); err != nil {
			return fmt.Errorf("discard normal-sync-satisfied archive stage: %w", err)
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`
			UPDATE middleman_archive_items SET %s = 'complete'
			WHERE repo_id = ? AND item_type = ? AND item_number = ?
			  AND provider_updated_at = ? AND hydration_snapshot_updated_at = ?
			  AND %s NOT IN ('unsupported', 'not_applicable')`, column, column),
			repoID, itemType, itemNumber, updatedAt, updatedAt,
		); err != nil {
			return fmt.Errorf("complete normal-sync archive dataset: %w", err)
		}
	}
	now := time.Now().UTC()
	result, err := tx.ExecContext(ctx, `
		UPDATE middleman_archive_items
		SET mirrored_provider_updated_at = provider_updated_at,
			hydrated_at = ?, attempt_count = 0, next_retry_at = NULL,
			last_error_code = NULL, last_error_detail = NULL
		WHERE repo_id = ? AND item_type = ? AND item_number = ?
		  AND lifecycle_state = 'active'
		  AND provider_updated_at = ? AND hydration_snapshot_updated_at = ?
		  AND comments_status NOT IN ('pending', 'failed')
		  AND reviews_status NOT IN ('pending', 'failed')
		  AND inline_comments_status NOT IN ('pending', 'failed')`,
		now, repoID, itemType, itemNumber, updatedAt, updatedAt,
	)
	if err != nil {
		return fmt.Errorf("finalize normal-sync archive item: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read normal-sync archive finalization result: %w", err)
	}
	if changed > 0 {
		return completeArchiveInitialIfReadyTx(ctx, tx, repoID, now)
	}
	return nil
}

// UpsertMergeRequestEventsSnapshot appends non-mirrored events only while the
// parent revision that produced them is still current.
func (d *DB) UpsertMergeRequestEventsSnapshot(
	ctx context.Context,
	mergeRequestID int64,
	expectedRevision int64,
	events []MREvent,
) (bool, error) {
	var applied bool
	err := d.Tx(ctx, func(tx *sql.Tx) error {
		current, err := domainParentSnapshotCurrentTx(
			ctx, tx, ArchiveItemTypeMergeRequest, mergeRequestID, expectedRevision,
		)
		if err != nil || !current {
			return err
		}
		if err := upsertMREventsTx(ctx, tx, events); err != nil {
			return err
		}
		applied = true
		return nil
	})
	return applied, err
}

func domainParentSnapshotCurrentTx(
	ctx context.Context,
	tx *sql.Tx,
	itemType ArchiveItemType,
	id int64,
	expectedRevision int64,
) (bool, error) {
	query := ""
	switch itemType {
	case ArchiveItemTypeIssue:
		query = `SELECT snapshot_revision FROM middleman_issues WHERE id = ?`
	case ArchiveItemTypeMergeRequest:
		query = `SELECT snapshot_revision FROM middleman_merge_requests WHERE id = ?`
	default:
		return false, fmt.Errorf("read domain parent snapshot: invalid item type %q", itemType)
	}
	var current int64
	if err := tx.QueryRowContext(ctx, query, id).Scan(&current); err != nil {
		return false, fmt.Errorf("read domain parent snapshot: %w", err)
	}
	return current == expectedRevision, nil
}

// UpdateMergeRequestCISnapshot persists CI only while the merge-request
// revision that authorized the provider request still owns the parent row.
func (d *DB) UpdateMergeRequestCISnapshot(
	ctx context.Context,
	mergeRequestID int64,
	expectedRevision int64,
	status string,
	checksJSON string,
) (bool, error) {
	result, err := d.rw.ExecContext(ctx, `
		UPDATE middleman_merge_requests
		SET ci_status = ?, ci_checks_json = ?
		WHERE id = ? AND snapshot_revision = ?`,
		status, checksJSON, mergeRequestID, expectedRevision)
	if err != nil {
		return false, fmt.Errorf("update merge-request CI snapshot: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("read merge-request CI snapshot result: %w", err)
	}
	return changed > 0, nil
}

// ClearMRCISnapshot drops head-bound CI state only while the parent revision
// and platform head that observed the change still own the merge request.
func (d *DB) ClearMRCISnapshot(
	ctx context.Context,
	mergeRequestID int64,
	expectedRevision int64,
	expectedHeadSHA string,
) (bool, error) {
	result, err := d.rw.ExecContext(ctx, `
		UPDATE middleman_merge_requests
		SET ci_status = '', ci_checks_json = '', ci_had_pending = 0
		WHERE id = ? AND snapshot_revision = ? AND platform_head_sha = ?`,
		mergeRequestID, expectedRevision, expectedHeadSHA)
	if err != nil {
		return false, fmt.Errorf("clear merge-request CI snapshot: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("read cleared merge-request CI snapshot result: %w", err)
	}
	return changed > 0, nil
}

// UpdateDiffSHAsSnapshot stores a locally verified diff only while the parent
// revision and platform head/base pair used for the computation remain current.
func (d *DB) UpdateDiffSHAsSnapshot(
	ctx context.Context,
	mergeRequestID int64,
	expectedRevision int64,
	expectedHeadSHA string,
	expectedBaseSHA string,
	diffHeadSHA string,
	diffBaseSHA string,
	mergeBaseSHA string,
) (bool, error) {
	result, err := d.rw.ExecContext(ctx, `
		UPDATE middleman_merge_requests
		SET diff_head_sha = ?, diff_base_sha = ?, merge_base_sha = ?
		WHERE id = ? AND snapshot_revision = ?
		  AND platform_head_sha = ? AND platform_base_sha = ?`,
		diffHeadSHA, diffBaseSHA, mergeBaseSHA,
		mergeRequestID, expectedRevision, expectedHeadSHA, expectedBaseSHA)
	if err != nil {
		return false, fmt.Errorf("update merge-request diff snapshot: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("read merge-request diff snapshot result: %w", err)
	}
	return changed > 0, nil
}

// ClearMRDetailFetchedSnapshot requeues detail work only for the parent
// revision whose incomplete provider response requested the reset.
func (d *DB) ClearMRDetailFetchedSnapshot(
	ctx context.Context,
	mergeRequestID int64,
	expectedRevision int64,
) (bool, error) {
	result, err := d.rw.ExecContext(ctx, `
		UPDATE middleman_merge_requests
		SET detail_fetched_at = NULL
		WHERE id = ? AND snapshot_revision = ?`, mergeRequestID, expectedRevision)
	if err != nil {
		return false, fmt.Errorf("clear merge-request detail snapshot: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("read cleared merge-request detail snapshot result: %w", err)
	}
	return changed > 0, nil
}

// ClearIssueDetailFetchedSnapshot is the issue counterpart to
// ClearMRDetailFetchedSnapshot.
func (d *DB) ClearIssueDetailFetchedSnapshot(
	ctx context.Context,
	issueID int64,
	expectedRevision int64,
) (bool, error) {
	result, err := d.rw.ExecContext(ctx, `
		UPDATE middleman_issues
		SET detail_fetched_at = NULL
		WHERE id = ? AND snapshot_revision = ?`, issueID, expectedRevision)
	if err != nil {
		return false, fmt.Errorf("clear issue detail snapshot: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("read cleared issue detail snapshot result: %w", err)
	}
	return changed > 0, nil
}

// UpdateMRWorkflowApprovalSnapshot persists workflow approval state only while
// the parent revision that authorized the provider request remains current.
func (d *DB) UpdateMRWorkflowApprovalSnapshot(
	ctx context.Context,
	mergeRequestID int64,
	expectedRevision int64,
	checkedAt time.Time,
	headSHA string,
	required bool,
	count int,
) (bool, error) {
	result, err := d.rw.ExecContext(ctx, `
		UPDATE middleman_merge_requests
		SET workflow_approval_checked_at = ?,
		    workflow_approval_head_sha = ?,
		    workflow_approval_required = ?,
		    workflow_approval_count = ?
		WHERE id = ? AND snapshot_revision = ?`,
		checkedAt.UTC(), headSHA, required, count, mergeRequestID, expectedRevision)
	if err != nil {
		return false, fmt.Errorf("update merge-request workflow approval snapshot: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("read merge-request workflow approval snapshot result: %w", err)
	}
	return changed > 0, nil
}

// MarkMergeRequestDetailFetchedSnapshot marks detail complete only for the
// parent revision whose child requests all completed.
func (d *DB) MarkMergeRequestDetailFetchedSnapshot(
	ctx context.Context,
	mergeRequestID int64,
	expectedRevision int64,
	ciHadPending bool,
) (bool, error) {
	result, err := d.rw.ExecContext(ctx, `
		UPDATE middleman_merge_requests
		SET detail_fetched_at = datetime('now'), ci_had_pending = ?
		WHERE id = ? AND snapshot_revision = ?`,
		ciHadPending, mergeRequestID, expectedRevision)
	if err != nil {
		return false, fmt.Errorf("mark merge-request detail snapshot fetched: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("read merge-request detail snapshot result: %w", err)
	}
	return changed > 0, nil
}

// MarkIssueDetailFetchedSnapshot is the issue counterpart to
// MarkMergeRequestDetailFetchedSnapshot.
func (d *DB) MarkIssueDetailFetchedSnapshot(
	ctx context.Context,
	issueID int64,
	expectedRevision int64,
) (bool, error) {
	result, err := d.rw.ExecContext(ctx, `
		UPDATE middleman_issues
		SET detail_fetched_at = datetime('now')
		WHERE id = ? AND snapshot_revision = ?`, issueID, expectedRevision)
	if err != nil {
		return false, fmt.Errorf("mark issue detail snapshot fetched: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("read issue detail snapshot result: %w", err)
	}
	return changed > 0, nil
}
