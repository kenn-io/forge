CREATE INDEX idx_archive_items_removed_upstream ON forge_archive_items(repo_id, item_type, item_number) WHERE lifecycle_state = 'removed_upstream';
CREATE INDEX idx_issue_repo_activity ON forge_issues(repo_id, last_activity_at DESC, number);
CREATE INDEX idx_mr_open_flags ON forge_merge_requests(repo_id, is_locked, is_draft) WHERE state = 'open';
