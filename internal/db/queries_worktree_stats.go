package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// WorktreeGitStats is one worktree's live git stats sample: the whole-branch
// diff size against its default branch and the ahead/behind counts versus its
// upstream. The background sampler writes these; the fleet snapshot overlays
// them by path so its read path performs no git I/O. A stored row means the
// worktree was sampled and all four counts are reportable (even when zero).
type WorktreeGitStats struct {
	DiffAdded   int
	DiffRemoved int
	SyncAhead   int
	SyncBehind  int
	SampledAt   time.Time
}

// UpsertWorktreeStats records a fresh git-stats sample for a worktree path,
// replacing any prior sample at that path. It reports whether the meaningful
// counts (diff added/removed, sync ahead/behind) differ from the prior stored
// sample: a path with no prior row always counts as changed, while a re-sample
// with identical counts does not. SampledAt is always advanced and never on its
// own marks a sample as changed. The change flag is best-effort under concurrent
// writers — an interleaved write may yield a spurious changed=true — so callers
// must treat it as a hint (e.g. a refetch trigger), not a guarantee.
func (d *DB) UpsertWorktreeStats(
	ctx context.Context, path string, stats WorktreeGitStats, now time.Time,
) (changed bool, err error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return false, fmt.Errorf("worktree stats path is required")
	}
	ts := canonicalUTCTime(now)
	if ts.IsZero() {
		ts = time.Now().UTC()
	}

	var prev WorktreeGitStats
	readErr := d.ro.QueryRowContext(ctx, `
		SELECT diff_added, diff_removed, sync_ahead, sync_behind
		FROM middleman_worktree_stats WHERE path = ?`, path,
	).Scan(&prev.DiffAdded, &prev.DiffRemoved, &prev.SyncAhead, &prev.SyncBehind)
	switch {
	case errors.Is(readErr, sql.ErrNoRows):
		changed = true
	case readErr != nil:
		return false, fmt.Errorf("read prior worktree stats: %w", readErr)
	default:
		changed = !worktreeStatsCountsEqual(prev, stats)
	}

	if _, err = d.rw.ExecContext(ctx, `
		INSERT INTO middleman_worktree_stats
		    (path, diff_added, diff_removed, sync_ahead, sync_behind, sampled_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(path) DO UPDATE SET
		    diff_added   = excluded.diff_added,
		    diff_removed = excluded.diff_removed,
		    sync_ahead   = excluded.sync_ahead,
		    sync_behind  = excluded.sync_behind,
		    sampled_at   = excluded.sampled_at`,
		path, stats.DiffAdded, stats.DiffRemoved,
		stats.SyncAhead, stats.SyncBehind, ts,
	); err != nil {
		return false, fmt.Errorf("upsert worktree stats: %w", err)
	}
	return changed, nil
}

// worktreeStatsCountsEqual reports whether two samples carry identical
// meaningful counts. SampledAt is ignored: it advances on every sample and so
// must not by itself mark a sample as changed.
func worktreeStatsCountsEqual(a, b WorktreeGitStats) bool {
	return a.DiffAdded == b.DiffAdded &&
		a.DiffRemoved == b.DiffRemoved &&
		a.SyncAhead == b.SyncAhead &&
		a.SyncBehind == b.SyncBehind
}

// ListWorktreeStats returns every stored worktree git-stats sample keyed by
// normalized path, for the fleet snapshot read path to overlay in one read.
func (d *DB) ListWorktreeStats(
	ctx context.Context,
) (map[string]WorktreeGitStats, error) {
	rows, err := d.ro.QueryContext(ctx, `
		SELECT path, diff_added, diff_removed, sync_ahead, sync_behind, sampled_at
		FROM middleman_worktree_stats`,
	)
	if err != nil {
		return nil, fmt.Errorf("list worktree stats: %w", err)
	}
	defer rows.Close()

	out := map[string]WorktreeGitStats{}
	for rows.Next() {
		var (
			path  string
			stats WorktreeGitStats
		)
		if err := rows.Scan(
			&path, &stats.DiffAdded, &stats.DiffRemoved,
			&stats.SyncAhead, &stats.SyncBehind, &stats.SampledAt,
		); err != nil {
			return nil, fmt.Errorf("scan worktree stats: %w", err)
		}
		stats.SampledAt = stats.SampledAt.UTC()
		out[path] = stats
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate worktree stats: %w", err)
	}
	return out, nil
}

// PruneWorktreeStats deletes stats rows for paths no longer in the fleet
// snapshot's worktree set. An empty keep set clears the table.
func (d *DB) PruneWorktreeStats(ctx context.Context, keepPaths []string) error {
	if len(keepPaths) == 0 {
		if _, err := d.rw.ExecContext(
			ctx, `DELETE FROM middleman_worktree_stats`,
		); err != nil {
			return fmt.Errorf("prune worktree stats: %w", err)
		}
		return nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(keepPaths)), ",")
	args := make([]any, 0, len(keepPaths))
	for _, p := range keepPaths {
		args = append(args, p)
	}
	if _, err := d.rw.ExecContext(ctx,
		`DELETE FROM middleman_worktree_stats WHERE path NOT IN (`+placeholders+`)`,
		args...,
	); err != nil {
		return fmt.Errorf("prune worktree stats: %w", err)
	}
	return nil
}
