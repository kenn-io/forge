DROP TABLE forge_notification_sync_watermarks;

CREATE TABLE forge_notification_sync_watermarks (
    platform TEXT NOT NULL,
    platform_host TEXT NOT NULL,
    repo_owner TEXT NOT NULL,
    repo_name TEXT NOT NULL,
    last_successful_sync_at TEXT NOT NULL,
    last_full_sync_at TEXT,
    PRIMARY KEY (platform, platform_host, repo_owner, repo_name)
);

DROP TABLE forge_http_etags;

CREATE TABLE forge_http_etags (
    platform TEXT NOT NULL,
    platform_host TEXT NOT NULL,
    owner_key TEXT NOT NULL,
    name_key TEXT NOT NULL,
    resource_type TEXT NOT NULL,
    resource_number INTEGER NOT NULL,
    etag TEXT NOT NULL,
    fetched_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (
        platform,
        platform_host,
        owner_key,
        name_key,
        resource_type,
        resource_number
    )
);

DROP TABLE forge_repository_config_routes;

DELETE FROM forge_workspaces
WHERE repo_id IN (
    SELECT id
    FROM forge_repos
    WHERE retired_at IS NOT NULL
);

DELETE FROM forge_repos
WHERE retired_at IS NOT NULL;

DROP INDEX idx_workspaces_external_item_key;
DROP INDEX idx_workspaces_repository_item_key;

CREATE UNIQUE INDEX idx_workspaces_provider_item_key
    ON forge_workspaces(
        platform,
        platform_host,
        repo_path_key,
        item_type,
        item_key
    );

ALTER TABLE forge_workspaces DROP COLUMN repo_id;
ALTER TABLE forge_workspaces DROP COLUMN legacy_clone_name;
ALTER TABLE forge_workspaces DROP COLUMN legacy_clone_owner;

DROP INDEX idx_repos_retired_replacement;
DROP INDEX idx_repos_provider_path_key;

CREATE UNIQUE INDEX idx_repos_provider_path_key
    ON forge_repos(platform, platform_host, repo_path_key)
    WHERE repo_path_key <> '';

ALTER TABLE forge_repos DROP COLUMN retired_name;
ALTER TABLE forge_repos DROP COLUMN retired_owner;
ALTER TABLE forge_repos DROP COLUMN retired_replacement_id;
ALTER TABLE forge_repos DROP COLUMN retired_at;
