-- A workspace on a worktree Forge did not create. Deleting one never removes
-- that worktree or its branch.
ALTER TABLE forge_workspaces ADD COLUMN external_worktree BOOLEAN NOT NULL DEFAULT 0;

-- Agent sessions that report through hooks without a Forge runtime: agents
-- started outside Forge. One that opens a pull request is adopted into a
-- workspace, whose chat mirrors it until it ends.
CREATE TABLE forge_external_agent_sessions (
    agent               TEXT NOT NULL,
    session_id          TEXT NOT NULL,
    cwd                 TEXT NOT NULL,
    transcript_path     TEXT NOT NULL DEFAULT '',
    process_id          INTEGER NOT NULL DEFAULT 0,
    process_start       TEXT NOT NULL DEFAULT '',
    herdr_pane_id       TEXT NOT NULL DEFAULT '',
    herdr_socket_path   TEXT NOT NULL DEFAULT '',
    -- A prompt reached the agent, so its own record of the session holds a
    -- conversation that a load must not silently drop.
    prompted            BOOLEAN NOT NULL DEFAULT 0,
    first_seen_at       DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    ended_at            DATETIME,
    workspace_id        TEXT NOT NULL DEFAULT '',
    runtime_session_key TEXT NOT NULL DEFAULT '',
    handed_off_at       DATETIME,
    PRIMARY KEY (agent, session_id)
);

CREATE INDEX forge_external_agent_sessions_runtime_key_idx
    ON forge_external_agent_sessions (runtime_session_key)
    WHERE runtime_session_key != '';
