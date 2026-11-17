package db

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
)

type detailChangeKey struct{}

type detailChangeTracker struct {
	repoID  int64
	number  int
	issue   bool
	mu      sync.Mutex
	initial detailSnapshot
	final   detailSnapshot
	seen    bool
}

// TrackDetailChanges reports semantic writes made by this detail refresh.
func TrackDetailChanges(ctx context.Context, repoID int64, number int, issue bool) (context.Context, func() bool) {
	tracker := &detailChangeTracker{repoID: repoID, number: number, issue: issue}
	return context.WithValue(ctx, detailChangeKey{}, tracker), func() bool {
		tracker.mu.Lock()
		defer tracker.mu.Unlock()
		return tracker.seen && tracker.initial != tracker.final
	}
}

// detailChangeTx records the first and last committed projections of a refresh.
// Parent and child snapshots can temporarily replace derived fields.
func (d *DB) detailChangeTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tracker, _ := ctx.Value(detailChangeKey{}).(*detailChangeTracker)
	if tracker == nil {
		return d.Tx(ctx, fn)
	}
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	var before, after detailSnapshot
	err := d.Tx(ctx, func(tx *sql.Tx) error {
		var err error
		before, err = detailChangeSnapshot(ctx, tx, tracker)
		if err != nil {
			return err
		}
		if err := fn(tx); err != nil {
			return err
		}
		after, err = detailChangeSnapshot(ctx, tx, tracker)
		if err != nil {
			return err
		}
		return nil
	})
	if err == nil {
		if !tracker.seen {
			tracker.initial = before
			tracker.seen = true
		}
		tracker.final = after
	}
	return err
}

type detailSnapshot struct{ parent, events, threads, labels, queue string }

func detailChangeSnapshot(ctx context.Context, tx *sql.Tx, target *detailChangeTracker) (detailSnapshot, error) {
	var snap detailSnapshot
	parent := `SELECT COALESCE((SELECT json_array(platform_id, platform_external_id, state, title, body, url, author, author_display_name, author_account_type, author_association, created_at, updated_at, last_activity_at, is_draft, is_locked, head_branch, base_branch, platform_head_sha, platform_base_sha, diff_head_sha, diff_base_sha, merge_base_sha, head_repo_clone_url, additions, deletions, diff_size_history_json, files_changed, merge_commit_sha, comment_count, review_decision, review_decision_observed_at IS NOT NULL, ci_status, ci_checks_json, ci_had_pending, ci_observed_at IS NOT NULL, mergeable_state, mergeable_state_observed_at IS NOT NULL, merge_rules, auto_merge_enabled, workflow_approval_head_sha, workflow_approval_checked_at IS NOT NULL, head_repo_identity_stale, workflow_approval_required, workflow_approval_count, assignees_json, reviewers_json, merged_at, closed_at, detail_fetched_at IS NOT NULL) FROM forge_merge_requests WHERE repo_id = ? AND number = ?), '')`
	events := `SELECT COALESCE(json_group_array(json_array(platform_id, platform_external_id, event_type, author, author_association, summary, body, metadata_json, created_at, dedupe_key, direct_url, thread_id, position_json, resolvable, resolved)), '[]') FROM (SELECT e.* FROM forge_mr_events e JOIN forge_merge_requests p ON p.id = e.merge_request_id WHERE p.repo_id = ? AND p.number = ? ORDER BY e.id)`
	threads := `SELECT COALESCE(json_group_array(json_array(provider_thread_id, provider_review_id, provider_comment_id, path, old_path, side, start_side, start_line, line, old_line, new_line, line_type, diff_head_sha, commit_sha, body, author_login, resolved, created_at, updated_at, resolved_at, metadata_json)), '[]') FROM (SELECT t.* FROM forge_mr_review_threads t JOIN forge_merge_requests p ON p.id = t.merge_request_id WHERE p.repo_id = ? AND p.number = ? ORDER BY t.id)`
	labels := `SELECT COALESCE(json_group_array(json_array(name, color, description, is_default)), '[]') FROM (SELECT l.name, l.color, l.description, l.is_default FROM forge_labels l JOIN forge_merge_request_labels x ON x.label_id = l.id JOIN forge_merge_requests p ON p.id = x.merge_request_id WHERE p.repo_id = ? AND p.number = ? ORDER BY l.id)`
	queue := `SELECT COALESCE((SELECT json_array(base_branch, queue_enabled, merge_method, queued, position, state, estimated_time_to_merge_seconds, enqueued_at, removed_at, removal_reason, removal_failed_checks) FROM forge_merge_queue_states WHERE repo_id = ? AND number = ?), '')`
	if target.issue {
		parent = `SELECT COALESCE((SELECT json_array(platform_id, platform_external_id, state, title, body, url, author, author_association, created_at, updated_at, last_activity_at, comment_count, assignees_json, closed_at, detail_fetched_at IS NOT NULL) FROM forge_issues WHERE repo_id = ? AND number = ?), '')`
		events = `SELECT COALESCE(json_group_array(json_array(platform_id, platform_external_id, event_type, author, summary, body, metadata_json, created_at, dedupe_key, direct_url, thread_id)), '[]') FROM (SELECT e.* FROM forge_issue_events e JOIN forge_issues p ON p.id = e.issue_id WHERE p.repo_id = ? AND p.number = ? ORDER BY e.id)`
		labels = `SELECT COALESCE(json_group_array(json_array(name, color, description, is_default)), '[]') FROM (SELECT l.name, l.color, l.description, l.is_default FROM forge_labels l JOIN forge_issue_labels x ON x.label_id = l.id JOIN forge_issues p ON p.id = x.issue_id WHERE p.repo_id = ? AND p.number = ? ORDER BY l.id)`
		threads, queue = "", ""
	}
	for _, query := range []struct {
		sql  string
		dest *string
	}{{parent, &snap.parent}, {events, &snap.events}, {threads, &snap.threads}, {labels, &snap.labels}, {queue, &snap.queue}} {
		if query.sql == "" {
			continue
		}
		if err := tx.QueryRowContext(ctx, query.sql, target.repoID, target.number).Scan(query.dest); err != nil {
			return detailSnapshot{}, fmt.Errorf("read detail changes: %w", err)
		}
	}
	return snap, nil
}

func (d *DB) detailChangeExec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if ctx.Value(detailChangeKey{}) == nil {
		return d.execContext(ctx, query, args...)
	}
	var result sql.Result
	err := d.detailChangeTx(ctx, func(tx *sql.Tx) error {
		var err error
		result, err = tx.ExecContext(ctx, query, args...)
		return err
	})
	return result, err
}
