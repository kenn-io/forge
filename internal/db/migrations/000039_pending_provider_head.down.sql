DELETE FROM middleman_app_metadata WHERE key = 'provider_snapshot_generation';

ALTER TABLE middleman_merge_requests DROP COLUMN provider_snapshot_generation;
ALTER TABLE middleman_merge_requests DROP COLUMN pending_provider_head_generation;
ALTER TABLE middleman_merge_requests DROP COLUMN pending_provider_head_sha;
