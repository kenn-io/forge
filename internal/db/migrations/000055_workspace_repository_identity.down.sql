-- Duplicate route-era workspaces deleted by the up migration cannot be restored.
DROP INDEX idx_workspaces_legacy_provider_item_key;
DROP INDEX idx_workspaces_repo_item_key;

-- Recreating the route-keyed unique index will reject a downgrade when
-- distinct repositories created the same workspace identity while sharing a
-- route at different times. Those identities cannot be collapsed safely.
ALTER TABLE forge_workspaces DROP COLUMN repo_id;

CREATE UNIQUE INDEX idx_workspaces_provider_item_key
    ON forge_workspaces(platform, platform_host, repo_path_key, item_type, item_key);
