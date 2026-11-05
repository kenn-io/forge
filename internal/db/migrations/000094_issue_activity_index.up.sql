DROP INDEX idx_mr_activity;
CREATE INDEX idx_mr_activity ON forge_merge_requests(last_activity_at DESC, id DESC, repo_id);
CREATE INDEX idx_issues_activity ON forge_issues(last_activity_at DESC, id DESC, repo_id);
