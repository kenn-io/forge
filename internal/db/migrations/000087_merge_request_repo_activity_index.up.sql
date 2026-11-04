CREATE INDEX idx_mr_repo_activity ON forge_merge_requests(repo_id, last_activity_at DESC, id DESC);
