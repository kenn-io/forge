CREATE TABLE forge_workspace_agent_launches (
    session_key TEXT PRIMARY KEY,
    target_key  TEXT NOT NULL,
    created_at  DATETIME NOT NULL
);

CREATE INDEX forge_workspace_agent_launches_created_at_idx
    ON forge_workspace_agent_launches(created_at);
