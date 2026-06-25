DROP INDEX IF EXISTS idx_workspaces_provider_item_key;

DELETE FROM middleman_workspaces
WHERE rowid NOT IN (
    SELECT MIN(rowid)
    FROM middleman_workspaces
    GROUP BY platform, platform_host, repo_path_key, item_type, item_number
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_workspaces_provider_item_key
    ON middleman_workspaces(platform, platform_host, repo_path_key, item_type, item_number);

ALTER TABLE middleman_workspaces
    DROP COLUMN kata_metadata;

ALTER TABLE middleman_workspaces
    DROP COLUMN item_key;
