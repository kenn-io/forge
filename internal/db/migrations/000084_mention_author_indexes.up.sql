ALTER TABLE forge_mr_events ADD COLUMN repo_id INTEGER NOT NULL DEFAULT 0;
ALTER TABLE forge_issue_events ADD COLUMN repo_id INTEGER NOT NULL DEFAULT 0;

UPDATE forge_mr_events
SET repo_id = (SELECT repo_id FROM forge_merge_requests WHERE id = merge_request_id);
UPDATE forge_issue_events
SET repo_id = (SELECT repo_id FROM forge_issues WHERE id = issue_id);

CREATE INDEX idx_mr_repo_author_activity ON forge_merge_requests(repo_id, author, last_activity_at);
CREATE INDEX idx_issue_repo_author_activity ON forge_issues(repo_id, author, last_activity_at);
CREATE INDEX idx_mr_events_repo_author_created ON forge_mr_events(repo_id, author, created_at);
CREATE INDEX idx_issue_events_repo_author_created ON forge_issue_events(repo_id, author, created_at);
