package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// MergeWorkspaceCleanup is a workspace deletion requested with a merge that
// went to the base branch's merge queue. It is kept until sync observes how
// the pull request leaves the queue.
type MergeWorkspaceCleanup struct {
	RepoID      int64
	Number      int
	WorkspaceID string
	// HostKey names the Fleet daemon that owns the workspace; "" is this
	// daemon.
	HostKey     string
	RequestedAt time.Time
}

// MergeWorkspaceCleanupOutcome is what sync has observed of a kept cleanup's
// pull request.
type MergeWorkspaceCleanupOutcome int

const (
	// MergeWorkspaceCleanupWaiting means the pull request may still merge.
	MergeWorkspaceCleanupWaiting MergeWorkspaceCleanupOutcome = iota
	// MergeWorkspaceCleanupMerged means sync recorded the pull request merged,
	// so the workspace is deleted.
	MergeWorkspaceCleanupMerged
	// MergeWorkspaceCleanupAbandoned means sync recorded the pull request
	// closed, or a queue read made after the request found it open and out of
	// the queue, so the request is dropped.
	MergeWorkspaceCleanupAbandoned
)

// ObservedMergeWorkspaceCleanup is a kept cleanup with its outcome.
type ObservedMergeWorkspaceCleanup struct {
	MergeWorkspaceCleanup
	Outcome MergeWorkspaceCleanupOutcome
}

const observedMergeWorkspaceCleanupQuery = `
	SELECT c.repo_id, c.number, c.workspace_id, c.host_key, c.requested_at,
	       COALESCE(p.state, ''), q.queued, q.observed_at
	FROM forge_merge_workspace_cleanups c
	LEFT JOIN forge_merge_requests p ON p.repo_id = c.repo_id AND p.number = c.number
	LEFT JOIN forge_merge_queue_states q ON q.repo_id = c.repo_id AND q.number = c.number`

// SaveMergeWorkspaceCleanup keeps a workspace deletion until the pull
// request's queued merge resolves, replacing an earlier request for the same
// pull request.
func (d *DB) SaveMergeWorkspaceCleanup(ctx context.Context, cleanup MergeWorkspaceCleanup) error {
	if _, err := d.rwExecContext(ctx, `
		INSERT INTO forge_merge_workspace_cleanups (
			repo_id, number, workspace_id, host_key, requested_at
		) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(repo_id, number) DO UPDATE SET
			workspace_id = excluded.workspace_id,
			host_key = excluded.host_key,
			requested_at = excluded.requested_at`,
		cleanup.RepoID, cleanup.Number, cleanup.WorkspaceID, cleanup.HostKey,
		cleanup.RequestedAt.UTC(),
	); err != nil {
		return fmt.Errorf("save merge workspace cleanup: %w", err)
	}
	return nil
}

// ListMergeWorkspaceCleanups returns every kept cleanup with its outcome.
func (d *DB) ListMergeWorkspaceCleanups(ctx context.Context) ([]ObservedMergeWorkspaceCleanup, error) {
	rows, err := d.roQueryContext(ctx, observedMergeWorkspaceCleanupQuery+`
		ORDER BY c.repo_id, c.number`)
	if err != nil {
		return nil, fmt.Errorf("list merge workspace cleanups: %w", err)
	}
	defer rows.Close()
	var cleanups []ObservedMergeWorkspaceCleanup
	for rows.Next() {
		cleanup, err := scanObservedMergeWorkspaceCleanup(rows.Scan)
		if err != nil {
			return nil, err
		}
		cleanups = append(cleanups, cleanup)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list merge workspace cleanups: %w", err)
	}
	return cleanups, nil
}

// GetMergeWorkspaceCleanup returns a pull request's kept cleanup with its
// outcome, or nil when none is kept.
func (d *DB) GetMergeWorkspaceCleanup(
	ctx context.Context, repoID int64, number int,
) (*ObservedMergeWorkspaceCleanup, error) {
	cleanup, err := scanObservedMergeWorkspaceCleanup(d.roQueryRowContext(ctx,
		observedMergeWorkspaceCleanupQuery+`
		WHERE c.repo_id = ? AND c.number = ?`, repoID, number).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &cleanup, nil
}

// DeleteMergeWorkspaceCleanup forgets a kept cleanup once it is resolved. It
// deletes only the exact request read, so a newer request saved for the same
// pull request meanwhile is kept.
func (d *DB) DeleteMergeWorkspaceCleanup(ctx context.Context, cleanup MergeWorkspaceCleanup) error {
	if _, err := d.rwExecContext(ctx, `
		DELETE FROM forge_merge_workspace_cleanups
		WHERE repo_id = ? AND number = ? AND workspace_id = ? AND host_key = ?
		  AND requested_at = ?`,
		cleanup.RepoID, cleanup.Number, cleanup.WorkspaceID, cleanup.HostKey,
		cleanup.RequestedAt.UTC(),
	); err != nil {
		return fmt.Errorf("delete merge workspace cleanup: %w", err)
	}
	return nil
}

func scanObservedMergeWorkspaceCleanup(scan func(...any) error) (ObservedMergeWorkspaceCleanup, error) {
	var cleanup ObservedMergeWorkspaceCleanup
	var state string
	var queued sql.NullBool
	var observedAt sql.NullTime
	if err := scan(
		&cleanup.RepoID, &cleanup.Number, &cleanup.WorkspaceID, &cleanup.HostKey,
		&cleanup.RequestedAt, &state, &queued, &observedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return cleanup, err
		}
		return cleanup, fmt.Errorf("scan merge workspace cleanup: %w", err)
	}
	cleanup.RequestedAt = cleanup.RequestedAt.UTC()
	switch {
	case state == string(MergeRequestStateMerged):
		cleanup.Outcome = MergeWorkspaceCleanupMerged
	case state == string(MergeRequestStateClosed):
		cleanup.Outcome = MergeWorkspaceCleanupAbandoned
	// Only a queue read made after the request reflects its enqueue.
	case state == string(MergeRequestStateOpen) && queued.Valid && !queued.Bool &&
		observedAt.Valid && observedAt.Time.After(cleanup.RequestedAt):
		cleanup.Outcome = MergeWorkspaceCleanupAbandoned
	default:
		cleanup.Outcome = MergeWorkspaceCleanupWaiting
	}
	return cleanup, nil
}
