CREATE INDEX idx_mr_repo_created ON forge_merge_requests(repo_id, created_at DESC, last_activity_at DESC, id DESC);
CREATE INDEX idx_mr_repo_state_created ON forge_merge_requests(repo_id, state, created_at DESC, last_activity_at DESC, id DESC);
CREATE INDEX idx_mr_repo_head ON forge_merge_requests(repo_id, head_branch, created_at DESC, last_activity_at DESC, id DESC);
