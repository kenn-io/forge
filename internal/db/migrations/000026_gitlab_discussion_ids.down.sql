DROP INDEX IF EXISTS idx_mr_events_thread;

ALTER TABLE middleman_mr_events DROP COLUMN thread_id;
ALTER TABLE middleman_mr_events DROP COLUMN position_json;
ALTER TABLE middleman_mr_events DROP COLUMN resolvable;
ALTER TABLE middleman_mr_events DROP COLUMN resolved;

ALTER TABLE middleman_issue_events DROP COLUMN thread_id;
