package db

import (
	"context"
	"fmt"
	"time"
)

// HotMergeRequestLimit is the maximum number of recently viewed open merge
// requests retained for fast background refresh.
const HotMergeRequestLimit = 10

// RecordHotMergeRequestView moves an open merge request to the front of the
// persisted hot set. Terminal merge requests are ignored, and the set is
// trimmed in the same transaction so concurrent readers never observe more
// than HotMergeRequestLimit entries.
func (d *DB) RecordHotMergeRequestView(
	ctx context.Context,
	mergeRequestID int64,
	viewedAt time.Time,
) error {
	tx, err := d.rw.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin record hot merge request view: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	_, err = tx.ExecContext(ctx, `
		INSERT INTO forge_hot_merge_requests (merge_request_id, viewed_at)
		SELECT id, ?
		FROM forge_merge_requests
		WHERE id = ? AND state = 'open'
		ON CONFLICT(merge_request_id) DO UPDATE
		SET viewed_at = excluded.viewed_at`,
		viewedAt.UTC(), mergeRequestID,
	)
	if err != nil {
		return fmt.Errorf("record hot merge request view: %w", err)
	}

	_, err = tx.ExecContext(ctx, `
		DELETE FROM forge_hot_merge_requests
		WHERE merge_request_id NOT IN (
			SELECT merge_request_id
			FROM forge_hot_merge_requests
			ORDER BY viewed_at DESC, merge_request_id DESC
			LIMIT ?
		)`, HotMergeRequestLimit)
	if err != nil {
		return fmt.Errorf("trim hot merge request views: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit hot merge request view: %w", err)
	}
	return nil
}

// ListHotMergeRequestIDs returns persisted hot merge requests in most-recently
// viewed order. The open-state join is a defensive backstop for the terminal
// eviction trigger.
func (d *DB) ListHotMergeRequestIDs(ctx context.Context, limit int) ([]int64, error) {
	if limit <= 0 {
		return []int64{}, nil
	}
	rows, err := d.roQueryContext(ctx, `
		SELECT hot.merge_request_id
		FROM forge_hot_merge_requests hot
		JOIN forge_merge_requests mr ON mr.id = hot.merge_request_id
		WHERE mr.state = 'open'
		ORDER BY hot.viewed_at DESC, hot.merge_request_id DESC
		LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list hot merge request ids: %w", err)
	}
	defer rows.Close()

	ids := make([]int64, 0, min(limit, HotMergeRequestLimit))
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan hot merge request id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate hot merge request ids: %w", err)
	}
	return ids, nil
}
