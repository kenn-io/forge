ALTER TABLE middleman_workspaces
    ADD COLUMN retired_at DATETIME;

ALTER TABLE middleman_workspaces
    ADD COLUMN repo_incarnation_id INTEGER;

UPDATE middleman_workspaces
SET repo_incarnation_id = (
    SELECT r.id
    FROM middleman_repos AS r
    WHERE r.platform = middleman_workspaces.platform
      AND r.platform_host = middleman_workspaces.platform_host
      AND r.repo_path_key = middleman_workspaces.repo_path_key
);

DROP INDEX IF EXISTS idx_workspaces_provider_item_key;

CREATE UNIQUE INDEX idx_workspaces_provider_item_key
    ON middleman_workspaces(platform, platform_host, repo_path_key, item_type, item_key)
    WHERE retired_at IS NULL;
