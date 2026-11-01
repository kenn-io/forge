DROP INDEX idx_mr_events_created;
CREATE INDEX idx_mr_events_created
    ON forge_mr_events(merge_request_id, created_at DESC);

DROP INDEX idx_issue_events_created;
CREATE INDEX idx_issue_events_created
    ON forge_issue_events(issue_id, created_at DESC);
