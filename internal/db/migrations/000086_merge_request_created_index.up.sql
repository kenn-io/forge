CREATE INDEX idx_mr_repo_created ON forge_merge_requests(repo_id, created_at DESC, last_activity_at DESC, id DESC);
