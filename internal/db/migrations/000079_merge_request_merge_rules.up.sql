-- How the rules treat the merge credential's merge, read as that credential:
-- '' (unknown), 'clear', 'blocked', or 'blocked_bypassable'. It replaces the
-- bypass flag; rows start unknown and refill on their next detail sync.
ALTER TABLE forge_merge_requests ADD COLUMN merge_rules TEXT NOT NULL DEFAULT '';
ALTER TABLE forge_merge_requests DROP COLUMN viewer_can_merge_as_admin;
