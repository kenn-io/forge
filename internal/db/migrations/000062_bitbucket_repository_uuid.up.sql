-- Bitbucket Cloud repositories have no integer ID. Their stable key is the
-- repository UUID. Every other provider, including Bitbucket Data Center,
-- keeps platform_repo_id and leaves this column empty.
ALTER TABLE forge_repos ADD COLUMN bitbucket_repository_uuid TEXT NOT NULL DEFAULT '';

CREATE UNIQUE INDEX idx_repos_bitbucket_repository_uuid
    ON forge_repos(platform, platform_host, bitbucket_repository_uuid)
    WHERE bitbucket_repository_uuid <> '';
