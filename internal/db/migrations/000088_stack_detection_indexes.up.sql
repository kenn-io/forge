CREATE INDEX idx_mr_repo_head_repo ON forge_merge_requests(repo_id, head_repo_clone_url, head_branch);
CREATE INDEX idx_mr_repo_base_state ON forge_merge_requests(repo_id, base_branch, state, number);
