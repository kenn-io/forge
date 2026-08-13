CREATE TABLE forge_agent_initial_message_receipts (
    workspace_id        TEXT NOT NULL
        REFERENCES forge_workspaces(id) ON DELETE CASCADE,
    runtime_session_key TEXT NOT NULL,
    agent               TEXT NOT NULL CHECK (length(agent) > 0),
    coding_session_id   TEXT NOT NULL CHECK (length(coding_session_id) > 0),
    message_bytes       INTEGER NOT NULL
        CHECK (message_bytes >= 0 AND message_bytes <= 65536),
    state               TEXT NOT NULL
        CHECK (state IN ('pending', 'delivered', 'uncertain')),
    reserved_at         DATETIME NOT NULL DEFAULT (datetime('now')),
    delivered_at        DATETIME,
    PRIMARY KEY (workspace_id, runtime_session_key),
    CHECK (
        (state = 'delivered' AND delivered_at IS NOT NULL)
        OR (state IN ('pending', 'uncertain') AND delivered_at IS NULL)
    )
);
