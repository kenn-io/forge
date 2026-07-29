ALTER TABLE middleman_workspaces
    ADD COLUMN retired_at DATETIME;

DROP INDEX IF EXISTS idx_workspaces_provider_item_key;

CREATE UNIQUE INDEX idx_workspaces_provider_item_key
    ON middleman_workspaces(platform, platform_host, repo_path_key, item_type, item_key)
    WHERE retired_at IS NULL;
