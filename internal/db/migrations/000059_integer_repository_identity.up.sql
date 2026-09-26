-- Repository identity becomes the provider's integer repository ID. Every
-- supported provider assigns one per host and keeps it across renames and
-- transfers, so owner/name is only the repository's current route and the
-- route-history table is no longer needed.

DROP INDEX idx_repos_platform_repo_id;

ALTER TABLE forge_repos ADD COLUMN provider_repo_id INTEGER NOT NULL DEFAULT 0;

-- GitLab, Forgejo, and Gitea already stored their integer ID as decimal text.
-- GitHub node IDs are never all digits, so a decimal GitHub value is already
-- the integer ID.
UPDATE forge_repos
SET provider_repo_id = CAST(platform_repo_id AS INTEGER)
WHERE platform_repo_id <> ''
  AND platform_repo_id NOT GLOB '*[^0-9]*'
  AND platform_repo_id NOT GLOB '0*';

-- GitHub stored GraphQL node IDs, which GitHub spells in more than one format
-- for the same repository. Keep each node ID only until one GitHub lookup at
-- startup replaces it with the integer ID; the row stays inactive until then.
ALTER TABLE forge_repos ADD COLUMN github_node_id TEXT NOT NULL DEFAULT '';

UPDATE forge_repos
SET github_node_id = platform_repo_id, lifecycle_state = 'inactive'
WHERE platform = 'github' AND platform_repo_id <> '' AND provider_repo_id = 0;

-- Rows without a verified provider ID cannot be keyed. Detach any workspace
-- that still points at one, then delete the row and the data it owns.
UPDATE forge_workspaces
SET repo_id = NULL
WHERE repo_id IN (
    SELECT id FROM forge_repos
    WHERE provider_repo_id = 0 AND github_node_id = ''
);

DELETE FROM forge_repos WHERE provider_repo_id = 0 AND github_node_id = '';

ALTER TABLE forge_repos DROP COLUMN platform_repo_id;
ALTER TABLE forge_repos RENAME COLUMN provider_repo_id TO platform_repo_id;

CREATE UNIQUE INDEX idx_repos_platform_repo_id
    ON forge_repos(platform, platform_host, platform_repo_id)
    WHERE platform_repo_id > 0;

CREATE UNIQUE INDEX idx_repos_active_route
    ON forge_repos(platform, platform_host, repo_path_key)
    WHERE lifecycle_state = 'active';

-- Launch specifications embed the repository ID in their JSON.
UPDATE forge_workspace_launch_specs
SET spec_json = json_set(
    spec_json, '$.repository.platform_repo_id',
    CAST(json_extract(spec_json, '$.repository.platform_repo_id') AS INTEGER)
)
WHERE json_type(spec_json, '$.repository.platform_repo_id') = 'text'
  AND json_extract(spec_json, '$.repository.platform_repo_id') NOT GLOB '*[^0-9]*'
  AND json_extract(spec_json, '$.repository.platform_repo_id') NOT GLOB '0*'
  AND json_extract(spec_json, '$.repository.platform_repo_id') <> '';

UPDATE forge_workspace_launch_specs
SET spec_json = json_set(
    spec_json, '$.pull.base_repo_id',
    CAST(json_extract(spec_json, '$.pull.base_repo_id') AS INTEGER)
)
WHERE json_type(spec_json, '$.pull.base_repo_id') = 'text'
  AND json_extract(spec_json, '$.pull.base_repo_id') NOT GLOB '*[^0-9]*'
  AND json_extract(spec_json, '$.pull.base_repo_id') NOT GLOB '0*'
  AND json_extract(spec_json, '$.pull.base_repo_id') <> '';

-- Remaining text IDs are GitHub node IDs. Zero keeps the JSON decodable and
-- marks the specification unverified until its repository converts.
UPDATE forge_workspace_launch_specs
SET spec_json = json_set(spec_json, '$.repository.platform_repo_id', 0)
WHERE json_type(spec_json, '$.repository.platform_repo_id') = 'text';

UPDATE forge_workspace_launch_specs
SET spec_json = json_set(spec_json, '$.pull.base_repo_id', 0)
WHERE json_type(spec_json, '$.pull.base_repo_id') = 'text';

DROP TABLE forge_repo_routes;
