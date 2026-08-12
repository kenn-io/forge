package db

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// MergeRequestMergeMetrics identifies immutable lifecycle fields from a
// canonical merged pull-request response.
type MergeRequestMergeMetrics struct {
	RepoID         int64
	Number         int
	HeadSHA        string
	MergeCommitSHA *string
	FilesChanged   *int
	MergedAt       *time.Time
}

// FillMissingMergedMRMetrics repairs lifecycle fields omitted by an earlier
// merged snapshot without weakening the parent snapshot timestamp guard. The
// canonical merged response replaces merge_commit_sha because GitHub may have
// stored a noncanonical test-merge SHA before the pull request merged.
func (d *DB) FillMissingMergedMRMetrics(
	ctx context.Context,
	metrics MergeRequestMergeMetrics,
) (bool, error) {
	if metrics.RepoID <= 0 || metrics.Number <= 0 || metrics.HeadSHA == "" {
		return false, nil
	}
	setClauses := make([]string, 0, 3)
	missingClauses := make([]string, 0, 3)
	setArgs := make([]any, 0, 3)
	repairArgs := make([]any, 0, 1)
	if metrics.MergeCommitSHA != nil && *metrics.MergeCommitSHA != "" {
		setClauses = append(setClauses, "merge_commit_sha = ?")
		missingClauses = append(missingClauses, "merge_commit_sha <> ?")
		setArgs = append(setArgs, *metrics.MergeCommitSHA)
		repairArgs = append(repairArgs, *metrics.MergeCommitSHA)
	}
	if metrics.FilesChanged != nil && *metrics.FilesChanged >= 0 {
		setClauses = append(setClauses, "files_changed = COALESCE(files_changed, ?)")
		missingClauses = append(missingClauses, "files_changed IS NULL")
		setArgs = append(setArgs, *metrics.FilesChanged)
	}
	var mergedAt any
	if metrics.MergedAt != nil {
		mergedAt = canonicalUTCTime(*metrics.MergedAt)
		setClauses = append(setClauses, "merged_at = COALESCE(merged_at, ?)")
		missingClauses = append(missingClauses, "merged_at IS NULL")
		setArgs = append(setArgs, mergedAt)
	}
	if len(setClauses) == 0 {
		return false, nil
	}
	args := append(setArgs, metrics.RepoID, metrics.Number, metrics.HeadSHA)
	args = append(args, repairArgs...)
	result, err := d.execContext(ctx, fmt.Sprintf(`
		UPDATE forge_merge_requests
		SET %s
		WHERE repo_id = ? AND number = ?
		  AND (state = 'merged' OR merged_at IS NOT NULL)
		  AND platform_head_sha = ? AND platform_head_sha <> ''
		  AND (%s)`, strings.Join(setClauses, ",\n\t\t\t"),
		strings.Join(missingClauses, " OR ")),
		args...,
	)
	if err != nil {
		return false, fmt.Errorf("fill missing merged pull request metrics: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("fill missing merged pull request metrics rows affected: %w", err)
	}
	return rows == 1, nil
}
