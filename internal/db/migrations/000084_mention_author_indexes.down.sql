DROP INDEX idx_issue_events_repo_author_created;
DROP INDEX idx_mr_events_repo_author_created;
DROP INDEX idx_issue_repo_author_activity;
DROP INDEX idx_mr_repo_author_activity;
ALTER TABLE forge_issue_events DROP COLUMN repo_id;
ALTER TABLE forge_mr_events DROP COLUMN repo_id;
