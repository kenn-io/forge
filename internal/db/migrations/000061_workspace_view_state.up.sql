CREATE TABLE forge_workspace_view_state (
    workspace_id TEXT PRIMARY KEY REFERENCES forge_workspaces(id) ON DELETE CASCADE,
    active_tab TEXT NOT NULL
);
