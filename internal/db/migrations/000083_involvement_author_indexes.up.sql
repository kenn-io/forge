CREATE INDEX idx_mr_events_involvement
    ON forge_mr_events(merge_request_id, LOWER(author))
    WHERE event_type IN ('issue_comment', 'review', 'review_comment');

CREATE INDEX idx_issue_events_involvement
    ON forge_issue_events(issue_id, LOWER(author))
    WHERE event_type = 'issue_comment';
