-- Recreate the previous schema only. Deleted manual workflow values and their
-- update metadata cannot be recovered by a downgrade.
CREATE TABLE forge_item_workflow_state (
    repo_id        INTEGER NOT NULL REFERENCES forge_repos(id) ON DELETE CASCADE,
    item_type      TEXT NOT NULL,
    item_number    INTEGER NOT NULL,
    status         TEXT NOT NULL DEFAULT 'new',
    updated_at     DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_source TEXT NOT NULL DEFAULT '',
    updated_actor  TEXT NOT NULL DEFAULT '',
    updated_reason TEXT NOT NULL DEFAULT '',
    UNIQUE(repo_id, item_type, item_number)
);

CREATE INDEX idx_item_workflow_status
    ON forge_item_workflow_state(status, updated_at DESC);
