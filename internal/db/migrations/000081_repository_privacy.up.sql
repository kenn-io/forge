-- Whether the provider reports the repository as private. NULL means it has
-- not reported; rows stay unknown until their next settings refresh.
ALTER TABLE forge_repos ADD COLUMN is_private INTEGER;
