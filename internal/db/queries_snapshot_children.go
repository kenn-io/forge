package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

type IssueChildSnapshot struct {
	IssueID          int64
	ExpectedRevision int64
	Comments         []IssueEvent
	OtherEvents      []IssueEvent
	DerivedFields    *IssueDerivedFields
}

type MergeRequestChildSnapshot struct {
	MergeRequestID         int64
	ExpectedRevision       int64
	Comments               []MREvent
	CommentsComplete       bool
	Reviews                []MREvent
	InlineComments         []MREvent
	ReviewThreads          []MRReviewThread
	InlineCommentsComplete bool
	OtherEvents            []MREvent
	DerivedFields          *MRDerivedFields
}

func domainParentSnapshotCurrentTx(
	ctx context.Context,
	tx *sql.Tx,
	table string,
	id int64,
	expectedRevision int64,
) (bool, error) {
	var revision int64
	err := tx.QueryRowContext(ctx, fmt.Sprintf(
		`SELECT snapshot_revision FROM %s WHERE id = ?`, table,
	), id).Scan(&revision)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read parent snapshot revision: %w", err)
	}
	return revision == expectedRevision, nil
}

func (d *DB) CommitIssueChildSnapshot(
	ctx context.Context,
	snapshot IssueChildSnapshot,
) (bool, error) {
	applied := false
	err := d.Tx(ctx, func(tx *sql.Tx) error {
		current, err := domainParentSnapshotCurrentTx(
			ctx, tx, "middleman_issues", snapshot.IssueID, snapshot.ExpectedRevision,
		)
		if err != nil || !current {
			return err
		}
		var lastActivityAt *time.Time
		if snapshot.DerivedFields != nil {
			lastActivityAt = &snapshot.DerivedFields.LastActivityAt
		}
		if err := replaceIssueCommentEventsTx(
			ctx, tx, snapshot.IssueID, snapshot.Comments, lastActivityAt,
		); err != nil {
			return err
		}
		if err := upsertIssueEventsTx(ctx, tx, snapshot.OtherEvents); err != nil {
			return err
		}
		applied = true
		return nil
	})
	return applied, err
}

func (d *DB) CommitMergeRequestChildSnapshot(
	ctx context.Context,
	snapshot MergeRequestChildSnapshot,
) (bool, error) {
	applied := false
	err := d.Tx(ctx, func(tx *sql.Tx) error {
		current, err := domainParentSnapshotCurrentTx(
			ctx, tx, "middleman_merge_requests", snapshot.MergeRequestID, snapshot.ExpectedRevision,
		)
		if err != nil || !current {
			return err
		}
		var lastActivityAt *time.Time
		if snapshot.DerivedFields != nil {
			lastActivityAt = &snapshot.DerivedFields.LastActivityAt
		}
		if snapshot.CommentsComplete {
			if err := replaceMRCommentEventsTx(
				ctx, tx, snapshot.MergeRequestID, snapshot.Comments, lastActivityAt,
			); err != nil {
				return err
			}
		} else if err := upsertMREventsTx(ctx, tx, snapshot.Comments); err != nil {
			return err
		}
		if err := upsertMREventsTx(ctx, tx, snapshot.Reviews); err != nil {
			return err
		}
		if snapshot.InlineCommentsComplete {
			threadIDs := make([]string, 0, len(snapshot.ReviewThreads))
			for _, thread := range snapshot.ReviewThreads {
				threadID := thread.ProviderThreadID
				if threadID == "" {
					threadID = thread.ProviderCommentID
				}
				threadIDs = append(threadIDs, threadID)
			}
			commentKeys := make([]string, 0, len(snapshot.InlineComments))
			for _, event := range snapshot.InlineComments {
				commentKeys = append(commentKeys, event.DedupeKey)
			}
			if err := deleteMissingMRReviewThreadsTx(
				ctx, tx, snapshot.MergeRequestID, threadIDs, commentKeys,
			); err != nil {
				return err
			}
			if err := upsertMRReviewThreadsTx(
				ctx, tx, snapshot.MergeRequestID, snapshot.ReviewThreads,
			); err != nil {
				return err
			}
			if err := upsertMREventsTx(ctx, tx, snapshot.InlineComments); err != nil {
				return err
			}
		}
		if snapshot.DerivedFields != nil {
			if _, err := tx.ExecContext(ctx, `
				UPDATE middleman_merge_requests
				SET review_decision = ?, last_activity_at = ?
				WHERE id = ?`, snapshot.DerivedFields.ReviewDecision,
				snapshot.DerivedFields.LastActivityAt, snapshot.MergeRequestID,
			); err != nil {
				return fmt.Errorf("update mr review activity: %w", err)
			}
		}
		if err := upsertMREventsTx(ctx, tx, snapshot.OtherEvents); err != nil {
			return err
		}
		applied = true
		return nil
	})
	return applied, err
}

func (d *DB) updateApplied(
	ctx context.Context,
	action string,
	query string,
	args ...any,
) (bool, error) {
	result, err := d.rw.ExecContext(ctx, query, args...)
	if err != nil {
		return false, fmt.Errorf("%s: %w", action, err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("read %s result: %w", action, err)
	}
	return changed > 0, nil
}

func (d *DB) UpdateMergeRequestCISnapshot(
	ctx context.Context,
	mergeRequestID int64,
	expectedRevision int64,
	status string,
	checksJSON string,
) (bool, error) {
	return d.updateApplied(ctx, "update merge-request CI", `
		UPDATE middleman_merge_requests
		SET ci_status = ?, ci_checks_json = ?
		WHERE id = ? AND snapshot_revision = ?`,
		status, checksJSON, mergeRequestID, expectedRevision)
}

func (d *DB) ClearMRCISnapshot(
	ctx context.Context,
	mergeRequestID int64,
	expectedRevision int64,
	expectedHeadSHA string,
) (bool, error) {
	return d.updateApplied(ctx, "clear merge-request CI", `
		UPDATE middleman_merge_requests
		SET ci_status = '', ci_checks_json = '', ci_had_pending = 0
		WHERE id = ? AND snapshot_revision = ? AND platform_head_sha = ?`,
		mergeRequestID, expectedRevision, expectedHeadSHA)
}

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
	return d.updateApplied(ctx, "update merge-request diff", `
		UPDATE middleman_merge_requests
		SET diff_head_sha = ?, diff_base_sha = ?, merge_base_sha = ?
		WHERE id = ? AND snapshot_revision = ? AND platform_head_sha = ? AND platform_base_sha = ?`,
		diffHeadSHA, diffBaseSHA, mergeBaseSHA,
		mergeRequestID, expectedRevision, expectedHeadSHA, expectedBaseSHA)
}

func (d *DB) ClearMRDetailFetchedSnapshot(
	ctx context.Context,
	mergeRequestID int64,
	expectedRevision int64,
) (bool, error) {
	return d.updateApplied(ctx, "clear merge-request detail", `
		UPDATE middleman_merge_requests
		SET detail_fetched_at = NULL
		WHERE id = ? AND snapshot_revision = ?`, mergeRequestID, expectedRevision)
}

func (d *DB) ClearIssueDetailFetchedSnapshot(
	ctx context.Context,
	issueID int64,
	expectedRevision int64,
) (bool, error) {
	return d.updateApplied(ctx, "clear issue detail", `
		UPDATE middleman_issues
		SET detail_fetched_at = NULL
		WHERE id = ? AND snapshot_revision = ?`, issueID, expectedRevision)
}

func (d *DB) MarkMergeRequestDetailFetchedSnapshot(
	ctx context.Context,
	mergeRequestID int64,
	expectedRevision int64,
	ciHadPending bool,
) (bool, error) {
	return d.updateApplied(ctx, "mark merge-request detail fetched", `
		UPDATE middleman_merge_requests
		SET detail_fetched_at = datetime('now'), ci_had_pending = ?
		WHERE id = ? AND snapshot_revision = ?`, ciHadPending, mergeRequestID, expectedRevision)
}

func (d *DB) MarkIssueDetailFetchedSnapshot(
	ctx context.Context,
	issueID int64,
	expectedRevision int64,
) (bool, error) {
	return d.updateApplied(ctx, "mark issue detail fetched", `
		UPDATE middleman_issues
		SET detail_fetched_at = datetime('now')
		WHERE id = ? AND snapshot_revision = ?`, issueID, expectedRevision)
}
