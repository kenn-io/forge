CREATE INDEX idx_mr_events_commit_summary
    ON forge_mr_events(summary, merge_request_id)
    WHERE event_type = 'commit';
