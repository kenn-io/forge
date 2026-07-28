ALTER TABLE middleman_merge_requests
ADD COLUMN head_repo_identity_stale INTEGER NOT NULL DEFAULT 0;
