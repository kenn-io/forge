PRAGMA foreign_keys = OFF;

CREATE TEMP TABLE middleman_workspace_tmux_sessions_legacy AS
SELECT workspace_id, session_name, target_key, created_at
FROM middleman_workspace_tmux_sessions;

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
    legacy.workspace_id,
    legacy.workspace_id || '_' || lower(hex(randomblob(8))),
    legacy.target_key,
    CASE
        WHEN legacy.target_key = 'plain_shell' THEN 'Shell'
        ELSE legacy.target_key
    END,
    CASE
        WHEN legacy.target_key = 'plain_shell' THEN 'plain_shell'
        ELSE 'agent'
    END,
    'session',
    legacy.session_name,
    legacy.created_at
FROM middleman_workspace_tmux_sessions_legacy AS legacy
WHERE EXISTS (
    SELECT 1
    FROM middleman_workspaces AS workspace
    WHERE workspace.id = legacy.workspace_id
)
AND NOT EXISTS (
    SELECT 1
    FROM middleman_workspace_runtime_sessions AS runtime
    WHERE runtime.workspace_id = legacy.workspace_id
      AND runtime.tmux_session = legacy.session_name
);

DROP INDEX IF EXISTS middleman_workspace_tmux_sessions_workspace_id_idx;
DROP TABLE IF EXISTS middleman_workspace_tmux_sessions;
DROP TABLE middleman_workspace_tmux_sessions_legacy;

PRAGMA foreign_keys = ON;
