ALTER TABLE forge_merge_requests ADD COLUMN merge_credential_mergeable_state TEXT NOT NULL DEFAULT '';
ALTER TABLE forge_merge_requests ADD COLUMN merge_credential_mergeable_state_until INTEGER NOT NULL DEFAULT 0;
