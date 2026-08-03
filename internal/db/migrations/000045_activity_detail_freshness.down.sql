DROP TRIGGER IF EXISTS forge_hot_merge_requests_terminal_eviction;
DROP INDEX IF EXISTS idx_forge_hot_merge_requests_viewed_at;
DROP TABLE IF EXISTS forge_hot_merge_requests;
DROP INDEX IF EXISTS idx_forge_notification_items_pr_activity;
