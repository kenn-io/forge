-- Activity recency walks each parent's events newest first and stops at the
-- first event type the feed renders. With event_type in the index that walk
-- reads only index pages instead of seeking every skipped event's table row.
DROP INDEX idx_mr_events_created;
CREATE INDEX idx_mr_events_created
    ON forge_mr_events(merge_request_id, created_at DESC, event_type);

DROP INDEX idx_issue_events_created;
CREATE INDEX idx_issue_events_created
    ON forge_issue_events(issue_id, created_at DESC, event_type);
