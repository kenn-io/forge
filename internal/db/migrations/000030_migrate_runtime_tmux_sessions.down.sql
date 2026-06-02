CREATE TABLE IF NOT EXISTS middleman_workspace_tmux_sessions (
    workspace_id TEXT NOT NULL REFERENCES middleman_workspaces(id) ON DELETE CASCADE,
    session_name TEXT NOT NULL,
    target_key   TEXT NOT NULL,
    created_at   DATETIME NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (workspace_id, session_name),
    UNIQUE (session_name)
);

INSERT INTO middleman_workspace_tmux_sessions (
    workspace_id,
    session_name,
    target_key,
    created_at
)
SELECT
    workspace_id,
    tmux_session,
    target_key,
    created_at
FROM middleman_workspace_runtime_sessions
WHERE tmux_session != ''
ON CONFLICT(workspace_id, session_name) DO UPDATE SET
    target_key = excluded.target_key,
    created_at = excluded.created_at;

CREATE INDEX IF NOT EXISTS middleman_workspace_tmux_sessions_workspace_id_idx
    ON middleman_workspace_tmux_sessions(workspace_id);
