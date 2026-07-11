ALTER TABLE middleman_merge_requests
    ADD COLUMN pending_provider_head_sha TEXT NOT NULL DEFAULT '';

ALTER TABLE middleman_merge_requests
    ADD COLUMN pending_provider_head_generation INTEGER NOT NULL DEFAULT 0;

ALTER TABLE middleman_merge_requests
    ADD COLUMN provider_snapshot_generation INTEGER NOT NULL DEFAULT 0;

ALTER TABLE middleman_merge_requests
    ADD COLUMN provider_mutation_in_progress INTEGER NOT NULL DEFAULT 0;

INSERT INTO middleman_app_metadata (key, value)
VALUES ('provider_snapshot_generation', '0')
ON CONFLICT (key) DO NOTHING;
