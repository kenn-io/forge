CREATE INDEX idx_mr_repo_head_repo ON forge_merge_requests(repo_id, lower(head_repo_clone_url), head_branch) WHERE state IN ('open', 'merged');
CREATE INDEX idx_mr_repo_base_state ON forge_merge_requests(repo_id, base_branch, number) WHERE state = 'merged';
