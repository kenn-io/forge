CREATE INDEX idx_mr_events_repo_created ON forge_mr_events(repo_id, created_at);
CREATE INDEX idx_issue_events_repo_created ON forge_issue_events(repo_id, created_at);
CREATE INDEX idx_issues_repo_created ON forge_issues(repo_id, created_at);
