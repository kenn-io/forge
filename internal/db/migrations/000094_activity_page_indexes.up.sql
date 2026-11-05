DROP INDEX idx_mr_activity;
-- Cover the global pages' +repo_id filter without making ordinary repo_id filters favor a global scan.
CREATE INDEX idx_mr_activity ON forge_merge_requests(last_activity_at DESC, id DESC, +repo_id);
CREATE INDEX idx_issues_activity ON forge_issues(last_activity_at DESC, id DESC, +repo_id);
