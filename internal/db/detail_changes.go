package db

import (
	"context"
	"fmt"
)

type DetailSnapshot string

// DetailChangeSnapshot reads the stored fields visible in a detail view.
func (d *DB) DetailChangeSnapshot(ctx context.Context, repoID int64, number int) (DetailSnapshot, error) {
	var snap DetailSnapshot
	parent := `SELECT COALESCE((SELECT json_array(platform_id, platform_external_id, state, title, body, url, author, author_display_name, author_account_type, author_association, created_at, updated_at, last_activity_at, is_draft, is_locked, head_branch, base_branch, platform_head_sha, platform_base_sha, diff_head_sha, diff_base_sha, merge_base_sha, head_repo_clone_url, additions, deletions, diff_size_history_json, files_changed, merge_commit_sha, comment_count, review_decision, review_decision_observed_at IS NOT NULL, ci_status, ci_checks_json, ci_had_pending, ci_observed_at IS NOT NULL, mergeable_state, mergeable_state_observed_at IS NOT NULL, merge_rules, auto_merge_enabled, workflow_approval_head_sha, workflow_approval_checked_at IS NOT NULL, head_repo_identity_stale, workflow_approval_required, workflow_approval_count, assignees_json, reviewers_json, merged_at, closed_at, detail_fetched_at IS NOT NULL) FROM forge_merge_requests WHERE repo_id = ? AND number = ?), '')`
	// JSON object key order is not meaningful, including in legacy metadata.
	metadata := `CASE WHEN json_valid(metadata_json) THEN (SELECT json_group_array(json_array(fullkey, type, atom)) FROM (SELECT fullkey, type, atom FROM json_tree(metadata_json) ORDER BY fullkey)) ELSE metadata_json END`
	events := fmt.Sprintf(`SELECT COALESCE(json_group_array(json_array(platform_id, platform_external_id, event_type, author, author_association, summary, body, %s, created_at, dedupe_key, direct_url, thread_id, position_json, resolvable, resolved)), '[]') FROM (SELECT e.* FROM forge_mr_events e JOIN forge_merge_requests p ON p.id = e.merge_request_id WHERE p.repo_id = ? AND p.number = ? ORDER BY e.id)`, metadata)
	threads := fmt.Sprintf(`SELECT COALESCE(json_group_array(json_array(provider_thread_id, provider_review_id, provider_comment_id, path, old_path, side, start_side, start_line, line, old_line, new_line, line_type, diff_head_sha, commit_sha, body, author_login, resolved, created_at, updated_at, resolved_at, %s)), '[]') FROM (SELECT t.* FROM forge_mr_review_threads t JOIN forge_merge_requests p ON p.id = t.merge_request_id WHERE p.repo_id = ? AND p.number = ? ORDER BY t.id)`, metadata)
	labels := `SELECT COALESCE(json_group_array(json_array(name, color, description, is_default)), '[]') FROM (SELECT l.name, l.color, l.description, l.is_default FROM forge_labels l JOIN forge_merge_request_labels x ON x.label_id = l.id JOIN forge_merge_requests p ON p.id = x.merge_request_id WHERE p.repo_id = ? AND p.number = ? ORDER BY l.id)`
	queue := `SELECT COALESCE((SELECT json_array(base_branch, queue_enabled, merge_method, queued, position, state, estimated_time_to_merge_seconds, enqueued_at, removed_at, removal_reason, removal_failed_checks) FROM forge_merge_queue_states WHERE repo_id = ? AND number = ?), '')`
	notifications := `WITH target(repo_id, number) AS (VALUES (?, ?))
SELECT COALESCE(json_group_array(json_array(id, done_at IS NOT NULL, done_reason)), '[]') FROM (
 SELECT n.id, n.done_at, n.done_reason FROM forge_notification_items n, target t
 WHERE n.repo_id = t.repo_id AND n.item_type = 'pr' AND n.item_number = t.number
 UNION ALL
 SELECT n.id, n.done_at, n.done_reason FROM forge_notification_items n, target t
 WHERE n.repo_id IS NULL AND n.item_type = 'pr' AND n.item_number = t.number
 AND (n.platform, n.platform_host, n.repo_owner, n.repo_name) =
     (SELECT platform, platform_host, owner_key, name_key FROM forge_repos WHERE id = t.repo_id)
 ORDER BY id
)`
	query := fmt.Sprintf("SELECT json_array((%s), (%s), (%s), (%s), (%s), (%s))", parent, events, threads, labels, queue, notifications)
	if err := d.ro.QueryRowContext(ctx, query, repoID, number, repoID, number, repoID, number, repoID, number, repoID, number, repoID, number).Scan(&snap); err != nil {
		return "", fmt.Errorf("read detail changes: %w", err)
	}
	return snap, nil
}
