ALTER TABLE middleman_workspaces
    ADD COLUMN item_key TEXT NOT NULL DEFAULT '';

ALTER TABLE middleman_workspaces
    ADD COLUMN kata_metadata TEXT NOT NULL DEFAULT '';

UPDATE middleman_workspaces
SET item_key = CAST(item_number AS TEXT)
WHERE item_key = '';

DROP INDEX IF EXISTS idx_workspaces_provider_item_key;

CREATE UNIQUE INDEX idx_workspaces_provider_item_key
    ON middleman_workspaces(platform, platform_host, repo_path_key, item_type, item_key);

CREATE TRIGGER middleman_workspaces_item_key_fill_insert
AFTER INSERT ON middleman_workspaces
WHEN NEW.item_key = ''
BEGIN
    UPDATE middleman_workspaces
    SET item_key = CAST(item_number AS TEXT)
    WHERE id = NEW.id;
END;
