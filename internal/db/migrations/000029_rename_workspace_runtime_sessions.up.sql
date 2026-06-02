PRAGMA foreign_keys = OFF;

DROP INDEX IF EXISTS middleman_workspace_runtime_sessions_workspace_id_idx;
DROP TABLE IF EXISTS middleman_workspace_runtime_sessions;
DROP INDEX IF EXISTS middleman_workspace_tmux_sessions_workspace_id_idx;
ALTER TABLE middleman_workspace_tmux_sessions
    RENAME TO middleman_workspace_runtime_sessions;
ALTER TABLE middleman_workspace_runtime_sessions
    RENAME COLUMN session_name TO session_key;

CREATE INDEX IF NOT EXISTS middleman_workspace_runtime_sessions_workspace_id_idx
    ON middleman_workspace_runtime_sessions(workspace_id);

DELETE FROM middleman_workspace_runtime_sessions;

ALTER TABLE middleman_workspace_runtime_sessions
    ADD COLUMN label TEXT NOT NULL DEFAULT '';
ALTER TABLE middleman_workspace_runtime_sessions
    ADD COLUMN kind TEXT NOT NULL DEFAULT '';
ALTER TABLE middleman_workspace_runtime_sessions
    ADD COLUMN scope TEXT NOT NULL DEFAULT 'session';
ALTER TABLE middleman_workspace_runtime_sessions
    ADD COLUMN tmux_session TEXT NOT NULL DEFAULT '';

PRAGMA foreign_keys = ON;
