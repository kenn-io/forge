ALTER TABLE middleman_merge_requests
    ADD COLUMN pending_provider_head_sha TEXT NOT NULL DEFAULT '';
