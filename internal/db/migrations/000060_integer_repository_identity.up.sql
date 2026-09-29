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

-- Issue-to-PR references name their source repository by route. Record the
-- repository that held the route so a later rename keeps the reference linked.
-- The current route owner is only trusted when no other repository ever held it.
ALTER TABLE forge_issue_pr_references
    ADD COLUMN source_repo_id INTEGER REFERENCES forge_repos(id) ON DELETE SET NULL;

UPDATE forge_issue_pr_references AS ref
SET source_repo_id = (
    SELECT MIN(route.repo_id)
    FROM forge_repo_routes AS route
    JOIN forge_repos AS repo ON repo.id = route.repo_id
    WHERE route.platform = ref.source_provider
      AND route.platform_host = ref.source_platform_host
      AND route.repo_path_key = lower(ref.source_owner || '/' || ref.source_repo)
    HAVING COUNT(DISTINCT route.repo_id) = 1
);

DROP TABLE forge_repo_routes;
