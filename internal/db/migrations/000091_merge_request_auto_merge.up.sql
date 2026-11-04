-- Whether the provider will merge the pull request itself once its
-- requirements pass (GitHub auto-merge). Rows start off and refill on their
-- next sync.
ALTER TABLE forge_merge_requests ADD COLUMN auto_merge_enabled INTEGER NOT NULL DEFAULT 0;
