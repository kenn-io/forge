ALTER TABLE middleman_mr_events ADD COLUMN discussion_id TEXT;
ALTER TABLE middleman_mr_events ADD COLUMN position_json TEXT NOT NULL DEFAULT '';
ALTER TABLE middleman_mr_events ADD COLUMN resolvable INTEGER NOT NULL DEFAULT 0;
ALTER TABLE middleman_mr_events ADD COLUMN resolved INTEGER NOT NULL DEFAULT 0;

ALTER TABLE middleman_issue_events ADD COLUMN discussion_id TEXT;

CREATE INDEX idx_mr_events_discussion
    ON middleman_mr_events(discussion_id) WHERE discussion_id IS NOT NULL;
