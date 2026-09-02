ALTER TABLE forge_workspaces
    ADD COLUMN repo_id INTEGER REFERENCES forge_repos(id) ON DELETE RESTRICT;

UPDATE forge_workspaces AS workspace
SET repo_id = (
    SELECT MIN(route.repo_id)
    FROM forge_repo_routes AS route
    JOIN forge_repos AS repo ON repo.id = route.repo_id
    WHERE route.platform = workspace.platform
      AND route.platform_host = workspace.platform_host
      AND route.repo_path_key = workspace.repo_path_key
    HAVING COUNT(DISTINCT route.repo_id) = 1
);

DELETE FROM forge_workspaces AS duplicate
WHERE duplicate.repo_id IS NOT NULL
  AND EXISTS (
      SELECT 1
      FROM forge_workspaces AS keeper
      WHERE keeper.repo_id = duplicate.repo_id
        AND keeper.item_type = duplicate.item_type
        AND keeper.item_key = duplicate.item_key
        AND (
            keeper.created_at > duplicate.created_at
            OR (
                keeper.created_at = duplicate.created_at
                AND keeper.rowid > duplicate.rowid
            )
        )
  );

DROP INDEX idx_workspaces_provider_item_key;

CREATE UNIQUE INDEX idx_workspaces_repo_item_key
    ON forge_workspaces(repo_id, item_type, item_key)
    WHERE repo_id IS NOT NULL;

CREATE UNIQUE INDEX idx_workspaces_legacy_provider_item_key
    ON forge_workspaces(
        platform, platform_host, repo_path_key, item_type, item_key
    )
    WHERE repo_id IS NULL;
