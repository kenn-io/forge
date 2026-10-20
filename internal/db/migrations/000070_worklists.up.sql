CREATE TABLE forge_worklists (
    id INTEGER PRIMARY KEY,
    title TEXT NOT NULL,
    recipe TEXT NOT NULL,
    created_by TEXT NOT NULL,
    workspace_id TEXT NOT NULL,
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    dismissed_at DATETIME
);

CREATE TABLE forge_worklist_items (
    worklist_id INTEGER NOT NULL REFERENCES forge_worklists(id) ON DELETE CASCADE,
    position INTEGER NOT NULL,
    repo_id INTEGER NOT NULL REFERENCES forge_repos(id) ON DELETE CASCADE,
    item_type TEXT NOT NULL,
    item_number INTEGER NOT NULL,
    score INTEGER,
    section TEXT NOT NULL,
    note TEXT NOT NULL,
    caveat TEXT NOT NULL,
    written_at DATETIME NOT NULL,
    snap_state TEXT NOT NULL,
    snap_head_sha TEXT NOT NULL,
    snap_comment_count INTEGER NOT NULL,
    snap_ci_status TEXT NOT NULL,
    snap_review_decision TEXT NOT NULL,
    snap_last_activity_at DATETIME,
    PRIMARY KEY (worklist_id, position),
    UNIQUE (worklist_id, repo_id, item_type, item_number)
);

CREATE INDEX idx_forge_worklist_items_repo ON forge_worklist_items(repo_id);
