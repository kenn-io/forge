package db

import (
	"context"
	"fmt"
)

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
	_ int64,
	status string,
	checksJSON string,
) (bool, error) {
	return d.updateApplied(ctx, "update merge-request CI", `
		UPDATE middleman_merge_requests
		SET ci_status = ?, ci_checks_json = ?
		WHERE id = ?`, status, checksJSON, mergeRequestID)
}

func (d *DB) ClearMRCISnapshot(
	ctx context.Context,
	mergeRequestID int64,
	_ int64,
	expectedHeadSHA string,
) (bool, error) {
	return d.updateApplied(ctx, "clear merge-request CI", `
		UPDATE middleman_merge_requests
		SET ci_status = '', ci_checks_json = '', ci_had_pending = 0
		WHERE id = ? AND platform_head_sha = ?`, mergeRequestID, expectedHeadSHA)
}

func (d *DB) UpdateDiffSHAsSnapshot(
	ctx context.Context,
	mergeRequestID int64,
	_ int64,
	expectedHeadSHA string,
	expectedBaseSHA string,
	diffHeadSHA string,
	diffBaseSHA string,
	mergeBaseSHA string,
) (bool, error) {
	return d.updateApplied(ctx, "update merge-request diff", `
		UPDATE middleman_merge_requests
		SET diff_head_sha = ?, diff_base_sha = ?, merge_base_sha = ?
		WHERE id = ? AND platform_head_sha = ? AND platform_base_sha = ?`,
		diffHeadSHA, diffBaseSHA, mergeBaseSHA,
		mergeRequestID, expectedHeadSHA, expectedBaseSHA)
}

func (d *DB) ClearMRDetailFetchedSnapshot(
	ctx context.Context,
	mergeRequestID int64,
	_ int64,
) (bool, error) {
	return d.updateApplied(ctx, "clear merge-request detail", `
		UPDATE middleman_merge_requests
		SET detail_fetched_at = NULL
		WHERE id = ?`, mergeRequestID)
}

func (d *DB) ClearIssueDetailFetchedSnapshot(
	ctx context.Context,
	issueID int64,
	_ int64,
) (bool, error) {
	return d.updateApplied(ctx, "clear issue detail", `
		UPDATE middleman_issues
		SET detail_fetched_at = NULL
		WHERE id = ?`, issueID)
}

func (d *DB) MarkMergeRequestDetailFetchedSnapshot(
	ctx context.Context,
	mergeRequestID int64,
	_ int64,
	ciHadPending bool,
) (bool, error) {
	return d.updateApplied(ctx, "mark merge-request detail fetched", `
		UPDATE middleman_merge_requests
		SET detail_fetched_at = datetime('now'), ci_had_pending = ?
		WHERE id = ?`, ciHadPending, mergeRequestID)
}

func (d *DB) MarkIssueDetailFetchedSnapshot(
	ctx context.Context,
	issueID int64,
	_ int64,
) (bool, error) {
	return d.updateApplied(ctx, "mark issue detail fetched", `
		UPDATE middleman_issues
		SET detail_fetched_at = datetime('now')
		WHERE id = ?`, issueID)
}
