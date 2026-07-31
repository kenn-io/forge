ALTER TABLE forge_repos
    ADD COLUMN retired_at DATETIME;

ALTER TABLE forge_repos
    ADD COLUMN retired_replacement_id INTEGER
        REFERENCES forge_repos(id);

ALTER TABLE forge_repos
    ADD COLUMN retired_owner TEXT;

ALTER TABLE forge_repos
    ADD COLUMN retired_name TEXT;

DROP INDEX idx_repos_provider_path_key;

CREATE UNIQUE INDEX idx_repos_provider_path_key
    ON forge_repos(platform, platform_host, repo_path_key)
    WHERE repo_path_key <> '' AND retired_at IS NULL;

CREATE INDEX idx_repos_retired_replacement
    ON forge_repos(retired_replacement_id)
    WHERE retired_replacement_id IS NOT NULL;

-- Exact configuration routes remain stable when the provider reports a
-- renamed display path. Persist that binding so an offline restart can seed
-- the same active repository incarnation without inventing a path-only row.
CREATE TABLE forge_repository_config_routes (
    repo_id INTEGER NOT NULL
        REFERENCES forge_repos(id) ON DELETE CASCADE,
    platform TEXT NOT NULL,
    platform_host TEXT NOT NULL,
    owner TEXT NOT NULL,
    name TEXT NOT NULL,
    repo_path TEXT NOT NULL,
    repo_path_key TEXT NOT NULL,
    PRIMARY KEY (platform, platform_host, repo_path_key)
);

CREATE INDEX idx_repository_config_routes_repo
    ON forge_repository_config_routes(repo_id);

ALTER TABLE forge_workspaces
    ADD COLUMN repo_id INTEGER
        REFERENCES forge_repos(id);

-- Preserve the route used by pre-incarnation managed clones. Workspace route
-- fields continue to follow repository renames, while these coordinates stay
-- fixed so recovery can identify the existing route-keyed Git directory.
ALTER TABLE forge_workspaces
    ADD COLUMN legacy_clone_owner TEXT;

ALTER TABLE forge_workspaces
    ADD COLUMN legacy_clone_name TEXT;

UPDATE forge_workspaces AS workspace
SET repo_id = (
    SELECT repo.id
    FROM forge_repos AS repo
    WHERE repo.platform = workspace.platform
      AND repo.platform_host = workspace.platform_host
      AND repo.repo_path_key = workspace.repo_path_key
      AND repo.retired_at IS NULL
);

UPDATE forge_workspaces
SET legacy_clone_owner = repo_owner,
    legacy_clone_name = repo_name
WHERE repo_id IS NOT NULL;

DROP INDEX idx_workspaces_provider_item_key;

CREATE UNIQUE INDEX idx_workspaces_repository_item_key
    ON forge_workspaces(repo_id, item_type, item_key)
    WHERE repo_id IS NOT NULL;

CREATE UNIQUE INDEX idx_workspaces_external_item_key
    ON forge_workspaces(
        platform,
        platform_host,
        repo_path_key,
        item_type,
        item_key
    )
    WHERE repo_id IS NULL;

-- Conditional-request state belongs to a repository incarnation, not to a
-- mutable provider route. These rows are caches, so the clean migration is to
-- discard them and let active repositories repopulate them.
DROP TABLE forge_http_etags;

CREATE TABLE forge_http_etags (
    repo_id INTEGER NOT NULL
        REFERENCES forge_repos(id) ON DELETE CASCADE,
    resource_type TEXT NOT NULL,
    resource_number INTEGER NOT NULL,
    etag TEXT NOT NULL,
    fetched_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (repo_id, resource_type, resource_number)
);

-- Notification progress is likewise incarnation-owned. Dropping prior
-- path-keyed watermarks forces one full notification pass after migration.
DROP TABLE forge_notification_sync_watermarks;

CREATE TABLE forge_notification_sync_watermarks (
    repo_id INTEGER PRIMARY KEY
        REFERENCES forge_repos(id) ON DELETE CASCADE,
    last_successful_sync_at TEXT NOT NULL,
    last_full_sync_at TEXT
);

UPDATE forge_notification_items AS notification
SET repo_id = (
    SELECT repo.id
    FROM forge_repos AS repo
    WHERE repo.platform = notification.platform
      AND repo.platform_host = notification.platform_host
      AND repo.owner_key = lower(notification.repo_owner)
      AND repo.name_key = lower(notification.repo_name)
      AND repo.retired_at IS NULL
)
WHERE repo_id IS NULL;
