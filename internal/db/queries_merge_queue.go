package db

import (
	"context"
	"database/sql"
	"encoding/json/v2"
	"errors"
	"fmt"
	"slices"
	"time"
)

// ReplaceMergeQueue replaces a repository's merge queue configuration and
// entry snapshot. A pull request entering the queue clears its recorded
// removal, since the new attempt supersedes it. Estimate-only changes are
// stored but not reported, so a moving estimate does not refresh every queued
// pull request on each pass.
func (d *DB) ReplaceMergeQueue(
	ctx context.Context, repoID int64, snapshot MergeQueueSnapshot,
) (MergeQueueChanges, error) {
	var changes MergeQueueChanges
	err := d.Tx(ctx, func(tx *sql.Tx) error {
		previous, err := loadMergeQueueEntriesTx(ctx, tx, repoID)
		if err != nil {
			return err
		}

		if snapshot.Configured {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO forge_repo_merge_queues (repo_id, branch, merge_method, observed_at)
				VALUES (?, ?, ?, ?)
				ON CONFLICT(repo_id) DO UPDATE SET
					branch = excluded.branch,
					merge_method = excluded.merge_method,
					observed_at = excluded.observed_at`,
				repoID, snapshot.Branch, snapshot.MergeMethod, snapshot.ObservedAt.UTC(),
			); err != nil {
				return fmt.Errorf("store merge queue configuration: %w", err)
			}
		} else if _, err := tx.ExecContext(ctx,
			`DELETE FROM forge_repo_merge_queues WHERE repo_id = ?`, repoID,
		); err != nil {
			return fmt.Errorf("delete merge queue configuration: %w", err)
		}

		if _, err := tx.ExecContext(ctx,
			`DELETE FROM forge_merge_queue_entries WHERE repo_id = ?`, repoID,
		); err != nil {
			return fmt.Errorf("delete merge queue entries: %w", err)
		}
		for _, entry := range snapshot.Entries {
			var estimate *int64
			if entry.EstimatedTimeToMerge != nil {
				estimate = new(int64(entry.EstimatedTimeToMerge.Seconds()))
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO forge_merge_queue_entries (
					repo_id, number, position, state, enqueued_at,
					estimated_time_to_merge_seconds, observed_at
				) VALUES (?, ?, ?, ?, ?, ?, ?)`,
				repoID, entry.Number, entry.Position, entry.State,
				entry.EnqueuedAt.UTC(), estimate, snapshot.ObservedAt.UTC(),
			); err != nil {
				return fmt.Errorf("insert merge queue entry: %w", err)
			}
			if _, err := tx.ExecContext(ctx, `
				DELETE FROM forge_merge_queue_removals
				WHERE repo_id = ? AND number = ?`,
				repoID, entry.Number,
			); err != nil {
				return fmt.Errorf("clear merge queue removal: %w", err)
			}
			prior, ok := previous[entry.Number]
			delete(previous, entry.Number)
			if !ok || prior.Position != entry.Position || prior.State != entry.State {
				changes.Changed = append(changes.Changed, entry.Number)
			}
		}
		for number, prior := range previous {
			changes.Changed = append(changes.Changed, number)
			changes.Departed = append(changes.Departed, prior)
			// Record the departure in the same transaction as the snapshot
			// that observed it, so a failed reason lookup is retried on a
			// later pass instead of being lost with the replaced entry.
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO forge_merge_queue_removals (
					repo_id, number, enqueued_at, departed_at, removed_at, reason
				) VALUES (?, ?, ?, ?, NULL, '')
				ON CONFLICT(repo_id, number) DO UPDATE SET
					enqueued_at = excluded.enqueued_at,
					departed_at = excluded.departed_at,
					removed_at = NULL,
					reason = ''`,
				repoID, number, prior.EnqueuedAt.UTC(), snapshot.ObservedAt.UTC(),
			); err != nil {
				return fmt.Errorf("record merge queue departure: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return MergeQueueChanges{}, err
	}
	slices.Sort(changes.Changed)
	slices.SortFunc(changes.Departed, func(a, b MergeQueueEntry) int {
		return a.Number - b.Number
	})
	return changes, nil
}

func loadMergeQueueEntriesTx(
	ctx context.Context, tx *sql.Tx, repoID int64,
) (map[int]MergeQueueEntry, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT number, position, state, enqueued_at
		FROM forge_merge_queue_entries
		WHERE repo_id = ?`, repoID)
	if err != nil {
		return nil, fmt.Errorf("load merge queue entries: %w", err)
	}
	defer rows.Close()
	previous := make(map[int]MergeQueueEntry)
	for rows.Next() {
		var prior MergeQueueEntry
		if err := rows.Scan(
			&prior.Number, &prior.Position, &prior.State, &prior.EnqueuedAt,
		); err != nil {
			return nil, fmt.Errorf("scan merge queue entry: %w", err)
		}
		prior.EnqueuedAt = prior.EnqueuedAt.UTC()
		previous[prior.Number] = prior
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load merge queue entries: %w", err)
	}
	return previous, nil
}

// ListPendingMergeQueueDepartures returns the repository's queue departures
// whose removal reason has not been resolved yet.
func (d *DB) ListPendingMergeQueueDepartures(
	ctx context.Context, repoID int64,
) ([]MergeQueueDeparture, error) {
	rows, err := d.roQueryContext(ctx, `
		SELECT number, enqueued_at, departed_at
		FROM forge_merge_queue_removals
		WHERE repo_id = ? AND removed_at IS NULL
		ORDER BY number`, repoID)
	if err != nil {
		return nil, fmt.Errorf("list pending merge queue departures: %w", err)
	}
	defer rows.Close()
	var departures []MergeQueueDeparture
	for rows.Next() {
		var departure MergeQueueDeparture
		if err := rows.Scan(&departure.Number, &departure.EnqueuedAt, &departure.DepartedAt); err != nil {
			return nil, fmt.Errorf("scan pending merge queue departure: %w", err)
		}
		departure.EnqueuedAt = departure.EnqueuedAt.UTC()
		departure.DepartedAt = departure.DepartedAt.UTC()
		departures = append(departures, departure)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list pending merge queue departures: %w", err)
	}
	return departures, nil
}

// ResolveMergeQueueDeparture records the provider's reason for a pending
// departure, making it a visible removal. It applies only while that exact
// departure is still pending, so a delayed lookup cannot resolve a later queue
// attempt of the same pull request. The removal stays until the pull request
// is queued again or closed.
func (d *DB) ResolveMergeQueueDeparture(
	ctx context.Context, repoID int64, departure MergeQueueDeparture, removal MergeQueueRemoval,
) error {
	if _, err := d.rwExecContext(ctx, `
		UPDATE forge_merge_queue_removals
		SET removed_at = ?, reason = ?
		WHERE repo_id = ? AND number = ? AND removed_at IS NULL
		  AND enqueued_at = ? AND departed_at = ?`,
		removal.RemovedAt.UTC(), removal.Reason, repoID, departure.Number,
		departure.EnqueuedAt.UTC(), departure.DepartedAt.UTC(),
	); err != nil {
		return fmt.Errorf("resolve merge queue departure: %w", err)
	}
	return nil
}

// DropMergeQueueDeparture forgets a pending departure that is not a removal
// to show, such as one the queue merged. Like ResolveMergeQueueDeparture, it
// applies only to that exact departure.
func (d *DB) DropMergeQueueDeparture(
	ctx context.Context, repoID int64, departure MergeQueueDeparture,
) error {
	if _, err := d.rwExecContext(ctx, `
		DELETE FROM forge_merge_queue_removals
		WHERE repo_id = ? AND number = ? AND removed_at IS NULL
		  AND enqueued_at = ? AND departed_at = ?`,
		repoID, departure.Number, departure.EnqueuedAt.UTC(), departure.DepartedAt.UTC(),
	); err != nil {
		return fmt.Errorf("drop merge queue departure: %w", err)
	}
	return nil
}

// RecordProvisionalMergeQueueEntry stores that Forge enqueued a pull request
// the latest queue read did not show, so a later read that misses it records a
// departure. Position 0 means sync has not observed its position yet. An
// observed entry is left unchanged.
func (d *DB) RecordProvisionalMergeQueueEntry(
	ctx context.Context, repoID int64, number int, enqueuedAt time.Time,
) error {
	return d.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO forge_merge_queue_entries (
				repo_id, number, position, state, enqueued_at,
				estimated_time_to_merge_seconds, observed_at
			) VALUES (?, ?, 0, 'QUEUED', ?, NULL, ?)
			ON CONFLICT(repo_id, number) DO NOTHING`,
			repoID, number, enqueuedAt.UTC(), enqueuedAt.UTC(),
		); err != nil {
			return fmt.Errorf("record provisional merge queue entry: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM forge_merge_queue_removals WHERE repo_id = ? AND number = ?`,
			repoID, number,
		); err != nil {
			return fmt.Errorf("clear merge queue removal: %w", err)
		}
		return nil
	})
}

// GetRepoMergeQueue returns the repository's default-branch merge queue
// configuration, or nil when the last observation found no merge queue.
func (d *DB) GetRepoMergeQueue(ctx context.Context, repoID int64) (*RepoMergeQueue, error) {
	var queue RepoMergeQueue
	err := d.roQueryRowContext(ctx, `
		SELECT branch, merge_method, observed_at
		FROM forge_repo_merge_queues
		WHERE repo_id = ?`, repoID,
	).Scan(&queue.Branch, &queue.MergeMethod, &queue.ObservedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get repository merge queue: %w", err)
	}
	queue.ObservedAt = queue.ObservedAt.UTC()
	return &queue, nil
}

// ListMergeQueueEntriesForMRs returns the merge queue entry of each open merge
// request that is currently queued. The IDs are bound once as a JSON array so
// the pull list size is not limited by SQLite's bind-parameter ceiling.
func (d *DB) ListMergeQueueEntriesForMRs(
	ctx context.Context, mrIDs []int64,
) (map[int64]MergeQueueEntry, error) {
	entries := make(map[int64]MergeQueueEntry)
	if len(mrIDs) == 0 {
		return entries, nil
	}
	payload, err := json.Marshal(mrIDs)
	if err != nil {
		return nil, fmt.Errorf("encode merge queue ids: %w", err)
	}
	rows, err := d.roQueryContext(ctx, `
		SELECT p.id, q.number, q.position, q.state, q.enqueued_at,
		       q.estimated_time_to_merge_seconds, q.observed_at
		FROM forge_merge_requests p
		JOIN forge_merge_queue_entries q
		  ON q.repo_id = p.repo_id AND q.number = p.number
		WHERE p.id IN (SELECT CAST(value AS INTEGER) FROM json_each(?))
		  AND p.state = 'open'`, string(payload))
	if err != nil {
		return nil, fmt.Errorf("list merge queue entries: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var mrID int64
		entry, err := scanMergeQueueEntry(rows, &mrID)
		if err != nil {
			return nil, err
		}
		entries[mrID] = entry
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list merge queue entries: %w", err)
	}
	return entries, nil
}

// GetMergeQueueEntry returns the merge queue entry of an open merge request,
// or nil when it is not queued.
func (d *DB) GetMergeQueueEntry(
	ctx context.Context, repoID int64, number int,
) (*MergeQueueEntry, error) {
	rows, err := d.roQueryContext(ctx, `
		SELECT p.id, q.number, q.position, q.state, q.enqueued_at,
		       q.estimated_time_to_merge_seconds, q.observed_at
		FROM forge_merge_queue_entries q
		JOIN forge_merge_requests p
		  ON p.repo_id = q.repo_id AND p.number = q.number
		WHERE q.repo_id = ? AND q.number = ? AND p.state = 'open'`,
		repoID, number)
	if err != nil {
		return nil, fmt.Errorf("get merge queue entry: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("get merge queue entry: %w", err)
		}
		return nil, nil
	}
	var mrID int64
	entry, err := scanMergeQueueEntry(rows, &mrID)
	if err != nil {
		return nil, err
	}
	return &entry, nil
}

// ListMergeQueueRemovalsForMRs returns the recorded merge queue removal of
// each open merge request, keyed by merge request ID.
func (d *DB) ListMergeQueueRemovalsForMRs(
	ctx context.Context, mrIDs []int64,
) (map[int64]MergeQueueRemoval, error) {
	removals := make(map[int64]MergeQueueRemoval)
	if len(mrIDs) == 0 {
		return removals, nil
	}
	payload, err := json.Marshal(mrIDs)
	if err != nil {
		return nil, fmt.Errorf("encode merge queue removal ids: %w", err)
	}
	rows, err := d.roQueryContext(ctx, `
		SELECT p.id, r.number, r.removed_at, r.reason
		FROM forge_merge_requests p
		JOIN forge_merge_queue_removals r
		  ON r.repo_id = p.repo_id AND r.number = p.number
		WHERE p.id IN (SELECT CAST(value AS INTEGER) FROM json_each(?))
		  AND p.state = 'open' AND r.removed_at IS NOT NULL`, string(payload))
	if err != nil {
		return nil, fmt.Errorf("list merge queue removals: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var mrID int64
		var removal MergeQueueRemoval
		if err := rows.Scan(&mrID, &removal.Number, &removal.RemovedAt, &removal.Reason); err != nil {
			return nil, fmt.Errorf("scan merge queue removal: %w", err)
		}
		removal.RemovedAt = removal.RemovedAt.UTC()
		removals[mrID] = removal
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list merge queue removals: %w", err)
	}
	return removals, nil
}

// GetMergeQueueRemoval returns the recorded merge queue removal of an open
// merge request, or nil when there is none.
func (d *DB) GetMergeQueueRemoval(
	ctx context.Context, repoID int64, number int,
) (*MergeQueueRemoval, error) {
	var removal MergeQueueRemoval
	err := d.roQueryRowContext(ctx, `
		SELECT r.number, r.removed_at, r.reason
		FROM forge_merge_queue_removals r
		JOIN forge_merge_requests p
		  ON p.repo_id = r.repo_id AND p.number = r.number
		WHERE r.repo_id = ? AND r.number = ? AND p.state = 'open'
		  AND r.removed_at IS NOT NULL`,
		repoID, number,
	).Scan(&removal.Number, &removal.RemovedAt, &removal.Reason)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get merge queue removal: %w", err)
	}
	removal.RemovedAt = removal.RemovedAt.UTC()
	return &removal, nil
}

func scanMergeQueueEntry(rows *sql.Rows, mrID *int64) (MergeQueueEntry, error) {
	var entry MergeQueueEntry
	var estimate sql.NullInt64
	if err := rows.Scan(
		mrID, &entry.Number, &entry.Position, &entry.State, &entry.EnqueuedAt,
		&estimate, &entry.ObservedAt,
	); err != nil {
		return entry, fmt.Errorf("scan merge queue entry: %w", err)
	}
	entry.EnqueuedAt = entry.EnqueuedAt.UTC()
	entry.ObservedAt = entry.ObservedAt.UTC()
	if estimate.Valid {
		entry.EstimatedTimeToMerge = new(time.Duration(estimate.Int64) * time.Second)
	}
	return entry, nil
}
