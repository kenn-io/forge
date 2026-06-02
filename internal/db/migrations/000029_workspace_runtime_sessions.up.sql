CREATE TABLE IF NOT EXISTS middleman_workspace_runtime_sessions (
    workspace_id  TEXT NOT NULL REFERENCES middleman_workspaces(id) ON DELETE CASCADE,
    session_key   TEXT NOT NULL,
    target_key    TEXT NOT NULL,
    label         TEXT NOT NULL,
    kind          TEXT NOT NULL,
    scope         TEXT NOT NULL DEFAULT 'session',
    tmux_session  TEXT NOT NULL DEFAULT '',
    created_at    DATETIME NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (workspace_id, session_key),
    UNIQUE (session_key)
);

CREATE INDEX IF NOT EXISTS middleman_workspace_runtime_sessions_workspace_id_idx
    ON middleman_workspace_runtime_sessions(workspace_id);
