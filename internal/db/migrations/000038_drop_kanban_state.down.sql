CREATE TABLE IF NOT EXISTS middleman_kanban_state (
    merge_request_id INTEGER PRIMARY KEY REFERENCES middleman_merge_requests(id) ON DELETE CASCADE,
    status           TEXT NOT NULL DEFAULT 'new',
    updated_at       DATETIME NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_kanban_status
    ON middleman_kanban_state(status, updated_at DESC);

INSERT INTO middleman_kanban_state (merge_request_id, status, updated_at)
SELECT mr.id, w.status, w.updated_at
FROM middleman_item_workflow_state w
JOIN middleman_merge_requests mr
    ON mr.repo_id = w.repo_id AND mr.number = w.item_number
WHERE w.item_type = 'pr';
