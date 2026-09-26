-- One-way data migration: deleted unverified repositories, dropped route
-- history, and decimal-to-integer conversions cannot be restored. This only
-- restores the previous column shape and an empty route-history table.

DROP INDEX idx_repos_active_route;
DROP INDEX idx_repos_platform_repo_id;

ALTER TABLE forge_repos ADD COLUMN text_repo_id TEXT NOT NULL DEFAULT '';

UPDATE forge_repos
SET text_repo_id = CASE
    WHEN platform_repo_id > 0 THEN CAST(platform_repo_id AS TEXT)
    ELSE github_node_id
END;

ALTER TABLE forge_repos DROP COLUMN platform_repo_id;
ALTER TABLE forge_repos DROP COLUMN github_node_id;
ALTER TABLE forge_repos RENAME COLUMN text_repo_id TO platform_repo_id;

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
    generation     INTEGER NOT NULL DEFAULT 1,
    UNIQUE(repo_id, platform, platform_host, repo_path_key)
);

CREATE UNIQUE INDEX idx_repo_routes_current_path
    ON forge_repo_routes(platform, platform_host, repo_path_key)
    WHERE is_current = 1;
CREATE UNIQUE INDEX idx_repo_routes_current_repo
    ON forge_repo_routes(repo_id)
    WHERE is_current = 1;
CREATE INDEX idx_repo_routes_repo
    ON forge_repo_routes(repo_id, is_current, repo_path_key);

INSERT INTO forge_repo_routes (
    repo_id, platform, platform_host, owner, name, repo_path,
    owner_key, name_key, repo_path_key, is_current, first_seen_at, last_seen_at
)
SELECT id, platform, platform_host, owner, name, repo_path,
       owner_key, name_key, repo_path_key,
       CASE WHEN lifecycle_state = 'active' THEN 1 ELSE 0 END,
       created_at, created_at
FROM forge_repos;
