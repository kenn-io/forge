-- thread_id stores the provider-specific identifier for a comment conversation
-- or reply thread. GitLab calls this a discussion ID; other providers may call
-- the same grouping a thread, conversation, or review thread.
ALTER TABLE middleman_mr_events ADD COLUMN thread_id TEXT;
ALTER TABLE middleman_mr_events ADD COLUMN position_json TEXT NOT NULL DEFAULT '';
ALTER TABLE middleman_mr_events ADD COLUMN resolvable INTEGER NOT NULL DEFAULT 0;
ALTER TABLE middleman_mr_events ADD COLUMN resolved INTEGER NOT NULL DEFAULT 0;

-- See middleman_mr_events.thread_id.
ALTER TABLE middleman_issue_events ADD COLUMN thread_id TEXT;

CREATE INDEX idx_mr_events_thread
    ON middleman_mr_events(thread_id) WHERE thread_id IS NOT NULL;
