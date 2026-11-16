package db

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"time"

	"go.kenn.io/forge/internal/gitclone"
)

// LineKindsTarget is a merge request whose current diff has no line counts.
type LineKindsTarget struct {
	ID           int64
	RepoID       int64
	MergeBaseSHA string
	HeadSHA      string
}

// ListMergeRequestsWithoutLineKinds lists, in ID order after afterID, merge
// requests whose diff SHAs are known and whose line counts are missing or
// were counted for another diff.
func (d *DB) ListMergeRequestsWithoutLineKinds(ctx context.Context, afterID int64, limit int) ([]LineKindsTarget, error) {
	rows, err := d.roQueryContext(ctx, `
		SELECT p.id, p.repo_id, p.merge_base_sha, p.diff_head_sha
		FROM forge_merge_requests p
		LEFT JOIN forge_merge_request_line_kinds k ON k.merge_request_id = p.id
		WHERE p.id > ? AND p.merge_base_sha != '' AND p.diff_head_sha != ''
		  AND (k.merge_request_id IS NULL OR k.merge_base_sha != p.merge_base_sha OR k.head_sha != p.diff_head_sha)
		ORDER BY p.id
		LIMIT ?`, afterID, limit)
	if err != nil {
		return nil, fmt.Errorf("list merge requests without line kinds: %w", err)
	}
	defer rows.Close()
	var targets []LineKindsTarget
	for rows.Next() {
		var target LineKindsTarget
		if err := rows.Scan(&target.ID, &target.RepoID, &target.MergeBaseSHA, &target.HeadSHA); err != nil {
			return nil, fmt.Errorf("scan merge request without line kinds: %w", err)
		}
		targets = append(targets, target)
	}
	return targets, rows.Err()
}

// SetMergeRequestLineKinds stores a merge request's line counts for the diff
// from mergeBase to head.
func (d *DB) SetMergeRequestLineKinds(
	ctx context.Context, mrID int64, mergeBase, head string, kinds gitclone.LineKinds, countedAt time.Time,
) error {
	_, err := d.execContext(ctx, `
		INSERT INTO forge_merge_request_line_kinds (
			merge_request_id, merge_base_sha, head_sha,
			code_additions, code_deletions, docs_additions, docs_deletions,
			data_additions, data_deletions, generated_additions, generated_deletions, counted_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(merge_request_id) DO UPDATE SET
			merge_base_sha = excluded.merge_base_sha, head_sha = excluded.head_sha,
			code_additions = excluded.code_additions, code_deletions = excluded.code_deletions,
			docs_additions = excluded.docs_additions, docs_deletions = excluded.docs_deletions,
			data_additions = excluded.data_additions, data_deletions = excluded.data_deletions,
			generated_additions = excluded.generated_additions, generated_deletions = excluded.generated_deletions,
			counted_at = excluded.counted_at`,
		mrID, mergeBase, head,
		kinds.Code.Additions, kinds.Code.Deletions, kinds.Docs.Additions, kinds.Docs.Deletions,
		kinds.Data.Additions, kinds.Data.Deletions, kinds.Generated.Additions, kinds.Generated.Deletions,
		countedAt.UTC(),
	)
	if err != nil {
		return fmt.Errorf("set line kinds for merge request %d: %w", mrID, err)
	}
	return nil
}

// ListLineKindsForMRs returns the line counts of the merge requests whose
// current diff has been counted. A diff that the provider's head or base has
// moved past is stale, as DiffSHAs.Stale decides, so its counts are left out.
func (d *DB) ListLineKindsForMRs(ctx context.Context, mrIDs []int64) (map[int64]gitclone.LineKinds, error) {
	kinds := make(map[int64]gitclone.LineKinds)
	if len(mrIDs) == 0 {
		return kinds, nil
	}
	payload, err := json.Marshal(mrIDs)
	if err != nil {
		return nil, fmt.Errorf("encode line kinds ids: %w", err)
	}
	rows, err := d.roQueryContext(ctx, `
		SELECT k.merge_request_id,
		       k.code_additions, k.code_deletions, k.docs_additions, k.docs_deletions,
		       k.data_additions, k.data_deletions, k.generated_additions, k.generated_deletions
		FROM json_each(?) ids
		JOIN forge_merge_requests p ON p.id = CAST(ids.value AS INTEGER)
		JOIN forge_merge_request_line_kinds k ON k.merge_request_id = p.id
		 AND k.merge_base_sha = p.merge_base_sha AND k.head_sha = p.diff_head_sha
		WHERE p.diff_head_sha = p.platform_head_sha
		  AND (p.state = 'merged' OR p.diff_base_sha = p.platform_base_sha)`, string(payload))
	if err != nil {
		return nil, fmt.Errorf("list line kinds: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var counted gitclone.LineKinds
		if err := rows.Scan(&id,
			&counted.Code.Additions, &counted.Code.Deletions, &counted.Docs.Additions, &counted.Docs.Deletions,
			&counted.Data.Additions, &counted.Data.Deletions, &counted.Generated.Additions, &counted.Generated.Deletions,
		); err != nil {
			return nil, fmt.Errorf("scan line kinds: %w", err)
		}
		kinds[id] = counted
	}
	return kinds, rows.Err()
}
