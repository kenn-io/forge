CREATE TABLE IF NOT EXISTS middleman_item_workflow_state (
    repo_id        INTEGER NOT NULL REFERENCES middleman_repos(id) ON DELETE CASCADE,
    item_type      TEXT NOT NULL,
    item_number    INTEGER NOT NULL,
    status         TEXT NOT NULL DEFAULT 'new',
    updated_at     DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_source TEXT NOT NULL DEFAULT '',
    updated_actor  TEXT NOT NULL DEFAULT '',
    updated_reason TEXT NOT NULL DEFAULT '',
    UNIQUE(repo_id, item_type, item_number)
);

CREATE INDEX IF NOT EXISTS idx_item_workflow_status
    ON middleman_item_workflow_state(status, updated_at DESC);

INSERT INTO middleman_item_workflow_state
    (repo_id, item_type, item_number, status, updated_at)
SELECT mr.repo_id, 'pr', mr.number, k.status, k.updated_at
FROM middleman_kanban_state k
JOIN middleman_merge_requests mr ON mr.id = k.merge_request_id;
