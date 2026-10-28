ALTER TABLE forge_merge_requests ADD COLUMN viewer_can_merge_as_admin INTEGER NOT NULL DEFAULT 0;
UPDATE forge_merge_requests SET viewer_can_merge_as_admin = 1 WHERE merge_rules = 'blocked_bypassable';
ALTER TABLE forge_merge_requests DROP COLUMN merge_rules;
