CREATE INDEX idx_forge_notification_items_pr_activity
    ON forge_notification_items(source_updated_at DESC, repo_id, item_number)
    WHERE item_type = 'pr' AND repo_id IS NOT NULL;
