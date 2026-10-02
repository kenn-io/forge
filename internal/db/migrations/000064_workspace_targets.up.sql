CREATE TABLE forge_workspace_targets (
    id INTEGER PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES forge_workspaces(id) ON DELETE CASCADE,
    repo_id INTEGER NOT NULL REFERENCES forge_repos(id) ON DELETE CASCADE,
    item_type TEXT NOT NULL,
    item_number INTEGER NOT NULL,
    url TEXT NOT NULL,
    created_at DATETIME NOT NULL,
    UNIQUE(workspace_id, repo_id, item_type, item_number)
);
