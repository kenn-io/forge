package db

import (
	"context"
	"fmt"
)

// MergeRequestMergeMetrics identifies immutable metrics from a canonical
// merged pull-request response.
type MergeRequestMergeMetrics struct {
	RepoID         int64
	Number         int
	HeadSHA        string
	MergeCommitSHA string
	FilesChanged   int
}

// FillMissingMergedMRMetrics fills lifecycle metrics omitted by an earlier
// merged snapshot without weakening the parent snapshot timestamp guard.
func (d *DB) FillMissingMergedMRMetrics(
	ctx context.Context,
	metrics MergeRequestMergeMetrics,
) (bool, error) {
	if metrics.RepoID <= 0 || metrics.Number <= 0 || metrics.HeadSHA == "" ||
		metrics.MergeCommitSHA == "" || metrics.FilesChanged < 0 {
		return false, nil
	}
	result, err := d.execContext(ctx, `
		UPDATE forge_merge_requests
		SET merge_commit_sha = CASE
				WHEN merge_commit_sha = '' THEN ? ELSE merge_commit_sha
			END,
			files_changed = COALESCE(files_changed, ?)
		WHERE repo_id = ? AND number = ?
		  AND state = 'merged' AND merged_at IS NOT NULL
		  AND platform_head_sha = ? AND platform_head_sha <> ''
		  AND (merge_commit_sha = '' OR files_changed IS NULL)`,
		metrics.MergeCommitSHA, metrics.FilesChanged,
		metrics.RepoID, metrics.Number, metrics.HeadSHA,
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
