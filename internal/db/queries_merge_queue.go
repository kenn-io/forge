package db

import (
	"context"
	"database/sql"
	"encoding/json/v2"
	"errors"
	"fmt"
	"time"
)

const mergeQueueStateColumns = `
	q.number, q.base_branch, q.queue_enabled, q.merge_method, q.queued,
	q.position, q.state, q.enqueued_at, q.estimated_time_to_merge_seconds,
	q.removed_at, q.removal_reason, q.observed_at`

// UpsertMergeQueueState stores a pull request's merge queue state and reports
// whether anything a reader shows changed: whether the queue is enabled, the
// queued flag, position, state, or removal. Estimate-only changes are stored
// without counting as a change, so a moving estimate does not refresh views.
// A state without a merge method keeps the stored one for the same base branch.
func (d *DB) UpsertMergeQueueState(
	ctx context.Context, repoID int64, state MergeQueueState,
) (bool, error) {
	var changed bool
	err := d.Tx(ctx, func(tx *sql.Tx) error {
		prior, err := scanMergeQueueStateRow(tx.QueryRowContext(ctx, `
			SELECT `+mergeQueueStateColumns+`
			FROM forge_merge_queue_states q
			WHERE q.repo_id = ? AND q.number = ?`, repoID, state.Number))
		if err != nil {
			return err
		}
		if state.MergeMethod == "" && prior != nil && prior.BaseBranch == state.BaseBranch {
			state.MergeMethod = prior.MergeMethod
		}
		changed = prior == nil || mergeQueueStateShownChanged(*prior, state)
		var estimate *int64
		if state.EstimatedTimeToMerge != nil {
			estimate = new(int64(state.EstimatedTimeToMerge.Seconds()))
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO forge_merge_queue_states (
				repo_id, number, base_branch, queue_enabled, merge_method, queued,
				position, state, enqueued_at, estimated_time_to_merge_seconds,
				removed_at, removal_reason, observed_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(repo_id, number) DO UPDATE SET
				base_branch = excluded.base_branch,
				queue_enabled = excluded.queue_enabled,
				merge_method = excluded.merge_method,
				queued = excluded.queued,
				position = excluded.position,
				state = excluded.state,
				enqueued_at = excluded.enqueued_at,
				estimated_time_to_merge_seconds = excluded.estimated_time_to_merge_seconds,
				removed_at = excluded.removed_at,
				removal_reason = excluded.removal_reason,
				observed_at = excluded.observed_at`,
			repoID, state.Number, state.BaseBranch, state.Enabled, state.MergeMethod,
			state.Queued, state.Position, state.State, utcTimePtr(state.EnqueuedAt),
			estimate, utcTimePtr(state.RemovedAt), state.RemovalReason,
			state.ObservedAt.UTC(),
		); err != nil {
			return fmt.Errorf("store merge queue state: %w", err)
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	return changed, nil
}

func mergeQueueStateShownChanged(prior, next MergeQueueState) bool {
	return prior.Enabled != next.Enabled ||
		prior.BaseBranch != next.BaseBranch ||
		prior.MergeMethod != next.MergeMethod ||
		prior.Queued != next.Queued ||
		prior.Position != next.Position ||
		prior.State != next.State ||
		prior.RemovalReason != next.RemovalReason ||
		!timePtrEqual(prior.RemovedAt, next.RemovedAt)
}

// DeleteMergeQueueState forgets a pull request's merge queue state and
// reports whether there was any.
func (d *DB) DeleteMergeQueueState(ctx context.Context, repoID int64, number int) (bool, error) {
	result, err := d.rwExecContext(ctx, `
		DELETE FROM forge_merge_queue_states WHERE repo_id = ? AND number = ?`,
		repoID, number)
	if err != nil {
		return false, fmt.Errorf("delete merge queue state: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("delete merge queue state: %w", err)
	}
	return affected > 0, nil
}

// GetMergeQueueState returns an open pull request's merge queue state, or nil
// when Forge has not read it.
func (d *DB) GetMergeQueueState(
	ctx context.Context, repoID int64, number int,
) (*MergeQueueState, error) {
	return scanMergeQueueStateRow(d.roQueryRowContext(ctx, `
		SELECT `+mergeQueueStateColumns+`
		FROM forge_merge_queue_states q
		JOIN forge_merge_requests p ON p.repo_id = q.repo_id AND p.number = q.number
		WHERE q.repo_id = ? AND q.number = ? AND p.state = 'open'`, repoID, number))
}

// ListMergeQueueStatesForMRs returns the merge queue state of each open merge
// request Forge has read it for, keyed by merge request ID. The IDs are bound
// once as a JSON array so the pull list size is not limited by SQLite's
// bind-parameter ceiling.
func (d *DB) ListMergeQueueStatesForMRs(
	ctx context.Context, mrIDs []int64,
) (map[int64]MergeQueueState, error) {
	states := make(map[int64]MergeQueueState)
	if len(mrIDs) == 0 {
		return states, nil
	}
	payload, err := json.Marshal(mrIDs)
	if err != nil {
		return nil, fmt.Errorf("encode merge queue state ids: %w", err)
	}
	rows, err := d.roQueryContext(ctx, `
		SELECT p.id, `+mergeQueueStateColumns+`
		FROM forge_merge_requests p
		JOIN forge_merge_queue_states q ON q.repo_id = p.repo_id AND q.number = p.number
		WHERE p.id IN (SELECT CAST(value AS INTEGER) FROM json_each(?))
		  AND p.state = 'open'`, string(payload))
	if err != nil {
		return nil, fmt.Errorf("list merge queue states: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var mrID int64
		state, err := scanMergeQueueState(rows.Scan, &mrID)
		if err != nil {
			return nil, err
		}
		states[mrID] = state
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list merge queue states: %w", err)
	}
	return states, nil
}

// ListQueuedMergeRequestNumbers returns the open pull requests Forge last saw
// in a merge queue, so a repository pass can notice them leave without the
// activity relay.
func (d *DB) ListQueuedMergeRequestNumbers(ctx context.Context, repoID int64) ([]int, error) {
	rows, err := d.roQueryContext(ctx, `
		SELECT q.number
		FROM forge_merge_queue_states q
		JOIN forge_merge_requests p ON p.repo_id = q.repo_id AND p.number = q.number
		WHERE q.repo_id = ? AND q.queued = 1 AND p.state = 'open'
		ORDER BY q.number`, repoID)
	if err != nil {
		return nil, fmt.Errorf("list queued merge requests: %w", err)
	}
	defer rows.Close()
	var numbers []int
	for rows.Next() {
		var number int
		if err := rows.Scan(&number); err != nil {
			return nil, fmt.Errorf("scan queued merge request: %w", err)
		}
		numbers = append(numbers, number)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list queued merge requests: %w", err)
	}
	return numbers, nil
}

func scanMergeQueueStateRow(row *sql.Row) (*MergeQueueState, error) {
	state, err := scanMergeQueueState(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &state, nil
}

func scanMergeQueueState(scan func(...any) error, leading ...any) (MergeQueueState, error) {
	var state MergeQueueState
	var enqueuedAt, removedAt sql.NullTime
	var estimate sql.NullInt64
	dest := append(leading,
		&state.Number, &state.BaseBranch, &state.Enabled, &state.MergeMethod, &state.Queued,
		&state.Position, &state.State, &enqueuedAt, &estimate,
		&removedAt, &state.RemovalReason, &state.ObservedAt,
	)
	if err := scan(dest...); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return state, err
		}
		return state, fmt.Errorf("scan merge queue state: %w", err)
	}
	if enqueuedAt.Valid {
		state.EnqueuedAt = new(enqueuedAt.Time.UTC())
	}
	if removedAt.Valid {
		state.RemovedAt = new(removedAt.Time.UTC())
	}
	if estimate.Valid {
		state.EstimatedTimeToMerge = new(time.Duration(estimate.Int64) * time.Second)
	}
	state.ObservedAt = state.ObservedAt.UTC()
	return state, nil
}

func utcTimePtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	return new(t.UTC())
}

func timePtrEqual(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}
