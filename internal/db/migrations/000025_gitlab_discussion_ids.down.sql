DROP INDEX IF EXISTS idx_mr_events_discussion;

ALTER TABLE middleman_mr_events DROP COLUMN discussion_id;
ALTER TABLE middleman_mr_events DROP COLUMN position_json;
ALTER TABLE middleman_mr_events DROP COLUMN resolvable;
ALTER TABLE middleman_mr_events DROP COLUMN resolved;

ALTER TABLE middleman_issue_events DROP COLUMN discussion_id;
