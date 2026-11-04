-- Whether the provider will merge the pull request itself once its
-- requirements pass (GitHub auto-merge). Rows start off and refill on their
-- next sync.
ALTER TABLE forge_merge_requests ADD COLUMN auto_merge_enabled INTEGER NOT NULL DEFAULT 0;

-- Whether a kept workspace deletion was saved for provider auto-merge rather
-- than a merge queue enqueue. Turning auto-merge off at the provider drops it.
ALTER TABLE forge_merge_workspace_cleanups ADD COLUMN via_auto_merge INTEGER NOT NULL DEFAULT 0;
