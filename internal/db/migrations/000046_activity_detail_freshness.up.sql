CREATE INDEX idx_forge_notification_items_pr_activity
    ON forge_notification_items(source_updated_at DESC, repo_id, item_number)
    WHERE item_type = 'pr' AND repo_id IS NOT NULL;

CREATE TABLE forge_hot_merge_requests (
    merge_request_id INTEGER PRIMARY KEY
        REFERENCES forge_merge_requests(id) ON DELETE CASCADE,
    viewed_at DATETIME NOT NULL
);

CREATE INDEX idx_forge_hot_merge_requests_viewed_at
    ON forge_hot_merge_requests(viewed_at DESC, merge_request_id DESC);

CREATE TRIGGER forge_hot_merge_requests_terminal_eviction
AFTER UPDATE OF state ON forge_merge_requests
WHEN NEW.state IN ('closed', 'merged')
BEGIN
    DELETE FROM forge_hot_merge_requests
    WHERE merge_request_id = NEW.id;
END;
