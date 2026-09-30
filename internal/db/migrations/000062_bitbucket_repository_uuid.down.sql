DROP INDEX idx_repos_bitbucket_repository_uuid;

ALTER TABLE forge_repos DROP COLUMN bitbucket_repository_uuid;
