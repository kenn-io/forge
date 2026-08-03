DROP TRIGGER forge_repos_casefold_insert;
DROP TRIGGER forge_repos_casefold_update;
DROP TRIGGER forge_workspaces_casefold_insert;
DROP TRIGGER forge_workspaces_casefold_update;
DROP INDEX idx_repos_provider_path_key;
DROP INDEX idx_repos_platform_repo_id;

CREATE TABLE forge_repos_v45 (
    id                         INTEGER PRIMARY KEY AUTOINCREMENT,
    platform                   TEXT NOT NULL DEFAULT 'github',
    platform_host              TEXT NOT NULL DEFAULT 'github.com',
    owner                      TEXT NOT NULL,
    name                       TEXT NOT NULL,
    last_sync_started_at       DATETIME,
    last_sync_completed_at     DATETIME,
    last_sync_error            TEXT DEFAULT '',
    allow_squash_merge         INTEGER NOT NULL DEFAULT 1,
    allow_merge_commit         INTEGER NOT NULL DEFAULT 1,
    allow_rebase_merge         INTEGER NOT NULL DEFAULT 1,
    created_at                 DATETIME NOT NULL DEFAULT (datetime('now')),
    platform_repo_id           TEXT NOT NULL DEFAULT '',
    repo_path                  TEXT NOT NULL DEFAULT '',
    owner_key                  TEXT NOT NULL DEFAULT '',
    name_key                   TEXT NOT NULL DEFAULT '',
    repo_path_key              TEXT NOT NULL DEFAULT '',
    web_url                    TEXT NOT NULL DEFAULT '',
    clone_url                  TEXT NOT NULL DEFAULT '',
    default_branch             TEXT NOT NULL DEFAULT '',
    label_catalog_synced_at    DATETIME,
    label_catalog_checked_at   DATETIME,
    label_catalog_sync_error   TEXT NOT NULL DEFAULT '',
    viewer_can_merge           INTEGER NOT NULL DEFAULT 1,
    lifecycle_state            TEXT NOT NULL DEFAULT 'active'
        CHECK (lifecycle_state IN ('active', 'inactive'))
);

INSERT INTO forge_repos_v45 (
    id, platform, platform_host, owner, name,
    last_sync_started_at, last_sync_completed_at, last_sync_error,
    allow_squash_merge, allow_merge_commit, allow_rebase_merge, created_at,
    platform_repo_id, repo_path, owner_key, name_key, repo_path_key,
    web_url, clone_url, default_branch,
    label_catalog_synced_at, label_catalog_checked_at,
    label_catalog_sync_error, viewer_can_merge, lifecycle_state
)
SELECT
    id, platform, platform_host, owner, name,
    last_sync_started_at, last_sync_completed_at, last_sync_error,
    allow_squash_merge, allow_merge_commit, allow_rebase_merge, created_at,
    platform_repo_id, repo_path, owner_key, name_key, repo_path_key,
    web_url, clone_url, default_branch,
    label_catalog_synced_at, label_catalog_checked_at,
    label_catalog_sync_error, viewer_can_merge,
    CASE WHEN platform_repo_id = '' THEN 'inactive' ELSE 'active' END
FROM forge_repos;

DROP TABLE forge_repos;
ALTER TABLE forge_repos_v45 RENAME TO forge_repos;

CREATE UNIQUE INDEX idx_repos_platform_repo_id
    ON forge_repos(platform, platform_host, platform_repo_id)
    WHERE platform_repo_id <> '';

CREATE TABLE forge_repo_routes (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    repo_id        INTEGER NOT NULL REFERENCES forge_repos(id) ON DELETE CASCADE,
    platform       TEXT NOT NULL,
    platform_host  TEXT NOT NULL,
    owner          TEXT NOT NULL,
    name           TEXT NOT NULL,
    repo_path      TEXT NOT NULL,
    owner_key      TEXT NOT NULL,
    name_key       TEXT NOT NULL,
    repo_path_key  TEXT NOT NULL,
    is_current     INTEGER NOT NULL DEFAULT 0 CHECK (is_current IN (0, 1)),
    first_seen_at  DATETIME NOT NULL,
    last_seen_at   DATETIME NOT NULL,
    UNIQUE(repo_id, platform, platform_host, repo_path_key)
);

INSERT INTO forge_repo_routes (
    repo_id, platform, platform_host, owner, name, repo_path,
    owner_key, name_key, repo_path_key,
    is_current, first_seen_at, last_seen_at
)
SELECT
    id, platform, platform_host, owner, name, repo_path,
    owner_key, name_key, repo_path_key,
    CASE WHEN platform_repo_id = '' THEN 0 ELSE 1 END,
    created_at, datetime('now')
FROM forge_repos;

CREATE UNIQUE INDEX idx_repo_routes_current_path
    ON forge_repo_routes(platform, platform_host, repo_path_key)
    WHERE is_current = 1;
CREATE UNIQUE INDEX idx_repo_routes_current_repo
    ON forge_repo_routes(repo_id)
    WHERE is_current = 1;
CREATE INDEX idx_repo_routes_repo
    ON forge_repo_routes(repo_id, is_current, repo_path_key);

CREATE TRIGGER forge_workspaces_casefold_insert
BEFORE INSERT ON forge_workspaces
WHEN NEW.platform <> lower(NEW.platform)
  OR NEW.platform_host <> lower(NEW.platform_host)
  OR (
      NEW.repo_path_key = ''
      AND (
          NEW.repo_owner <> lower(NEW.repo_owner)
          OR NEW.repo_name <> lower(NEW.repo_name)
      )
  )
  OR (
      NEW.repo_path_key <> ''
      AND (
          NEW.repo_owner_key <> lower(NEW.repo_owner_key)
          OR NEW.repo_name_key <> lower(NEW.repo_name_key)
          OR NEW.repo_path_key <> lower(NEW.repo_path_key)
          OR NEW.repo_path_key <> NEW.repo_owner_key || '/' || NEW.repo_name_key
      )
  )
  OR (
      NEW.repo_path_key <> ''
      AND
      NOT EXISTS (
          SELECT 1
          FROM forge_repos r
          WHERE r.platform = NEW.platform
            AND r.platform_host = NEW.platform_host
            AND r.repo_path_key = NEW.repo_path_key
            AND r.platform <> 'github'
      )
      AND (
          NEW.repo_owner <> NEW.repo_owner_key
          OR NEW.repo_name <> NEW.repo_name_key
      )
  )
BEGIN
    SELECT RAISE(ABORT, 'workspace repo identifiers must be provider-canonical');
END;

CREATE TRIGGER forge_workspaces_casefold_update
BEFORE UPDATE OF platform, platform_host, repo_owner, repo_name, repo_owner_key, repo_name_key, repo_path_key ON forge_workspaces
WHEN NEW.platform <> lower(NEW.platform)
  OR NEW.platform_host <> lower(NEW.platform_host)
  OR NEW.repo_path_key = ''
  OR NEW.repo_owner_key <> lower(NEW.repo_owner_key)
  OR NEW.repo_name_key <> lower(NEW.repo_name_key)
  OR NEW.repo_path_key <> lower(NEW.repo_path_key)
  OR NEW.repo_path_key <> NEW.repo_owner_key || '/' || NEW.repo_name_key
  OR (
      NOT EXISTS (
          SELECT 1
          FROM forge_repos r
          WHERE r.platform = NEW.platform
            AND r.platform_host = NEW.platform_host
            AND r.repo_path_key = NEW.repo_path_key
            AND r.platform <> 'github'
      )
      AND (
          NEW.repo_owner <> NEW.repo_owner_key
          OR NEW.repo_name <> NEW.repo_name_key
      )
  )
BEGIN
    SELECT RAISE(ABORT, 'workspace repo identifiers must be provider-canonical');
END;

CREATE TRIGGER forge_repos_casefold_insert
BEFORE INSERT ON forge_repos
WHEN NEW.platform <> lower(NEW.platform)
  OR NEW.platform_host <> lower(NEW.platform_host)
  OR NEW.repo_path = ''
  OR NEW.owner_key <> lower(NEW.owner)
  OR NEW.name_key <> lower(NEW.name)
  OR NEW.repo_path_key <> lower(NEW.repo_path)
  OR (
      lower(NEW.platform) = 'github'
      AND (
          NEW.owner <> lower(NEW.owner)
          OR NEW.name <> lower(NEW.name)
          OR NEW.repo_path <> lower(NEW.repo_path)
      )
  )
BEGIN
    SELECT RAISE(ABORT, 'repo identifiers must be provider-canonical');
END;

CREATE TRIGGER forge_repos_casefold_update
BEFORE UPDATE OF platform, platform_host, owner, name, repo_path, owner_key, name_key, repo_path_key ON forge_repos
WHEN NEW.platform <> lower(NEW.platform)
  OR NEW.platform_host <> lower(NEW.platform_host)
  OR NEW.repo_path = ''
  OR NEW.owner_key <> lower(NEW.owner)
  OR NEW.name_key <> lower(NEW.name)
  OR NEW.repo_path_key <> lower(NEW.repo_path)
  OR (
      lower(NEW.platform) = 'github'
      AND (
          NEW.owner <> lower(NEW.owner)
          OR NEW.name <> lower(NEW.name)
          OR NEW.repo_path <> lower(NEW.repo_path)
      )
  )
BEGIN
    SELECT RAISE(ABORT, 'repo identifiers must be provider-canonical');
END;
