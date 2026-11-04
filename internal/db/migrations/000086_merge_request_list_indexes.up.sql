CREATE INDEX idx_mr_repo_created ON forge_merge_requests(repo_id, created_at DESC, last_activity_at DESC, id DESC);
CREATE INDEX idx_mr_repo_head ON forge_merge_requests(repo_id, head_branch, created_at DESC, last_activity_at DESC, id DESC);
CREATE INDEX idx_mr_repo_open_created ON forge_merge_requests(repo_id, created_at DESC, last_activity_at DESC, id DESC) WHERE state = 'open' AND merged_at IS NULL;
CREATE INDEX idx_mr_repo_merged_created ON forge_merge_requests(repo_id, created_at DESC, last_activity_at DESC, id DESC) WHERE state = 'merged' OR merged_at IS NOT NULL;
