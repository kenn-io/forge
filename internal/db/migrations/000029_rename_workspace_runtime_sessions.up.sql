PRAGMA foreign_keys = OFF;

-- middleman_workspace_runtime_sessions is introduced by this migration and
-- does not exist in any prior released schema. This defensive drop only clears
-- an abandoned branch-local table before replacing the existing tmux table.
DROP INDEX IF EXISTS middleman_workspace_runtime_sessions_workspace_id_idx;
DROP TABLE IF EXISTS middleman_workspace_runtime_sessions;
DROP INDEX IF EXISTS middleman_workspace_tmux_sessions_workspace_id_idx;

CREATE TABLE middleman_workspace_runtime_sessions (
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

INSERT INTO middleman_workspace_runtime_sessions (
    workspace_id,
    session_key,
    target_key,
    label,
    kind,
    scope,
    tmux_session,
    created_at
)
SELECT
    workspace_id,
    workspace_id || '_' || lower(hex(randomblob(8))) AS session_key,
    target_key,
    CASE
        WHEN target_key = 'plain_shell' THEN 'Shell'
        ELSE target_key
    END AS label,
    CASE
        WHEN target_key = 'plain_shell' THEN 'plain_shell'
        ELSE 'agent'
    END AS kind,
    'session' AS scope,
    session_name AS tmux_session,
    created_at
FROM middleman_workspace_tmux_sessions;

DROP TABLE middleman_workspace_tmux_sessions;

CREATE INDEX IF NOT EXISTS middleman_workspace_runtime_sessions_workspace_id_idx
    ON middleman_workspace_runtime_sessions(workspace_id);

PRAGMA foreign_keys = ON;
