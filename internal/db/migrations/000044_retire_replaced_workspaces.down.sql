DROP INDEX IF EXISTS idx_workspaces_provider_item_key;

-- Retired rows cannot be represented by the previous unconditional unique
-- index when a replacement workspace has claimed the same source item.
DELETE FROM middleman_workspaces
WHERE retired_at IS NOT NULL;

ALTER TABLE middleman_workspaces
    DROP COLUMN retired_at;

ALTER TABLE middleman_workspaces
    DROP COLUMN repo_incarnation_id;

CREATE UNIQUE INDEX idx_workspaces_provider_item_key
    ON middleman_workspaces(platform, platform_host, repo_path_key, item_type, item_key);
