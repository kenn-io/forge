ALTER TABLE forge_worklists ADD COLUMN link_key TEXT;

CREATE UNIQUE INDEX idx_forge_worklists_link_key ON forge_worklists(link_key);
