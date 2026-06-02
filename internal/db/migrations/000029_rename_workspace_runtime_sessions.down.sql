PRAGMA foreign_keys = OFF;

DROP INDEX IF EXISTS middleman_workspace_runtime_sessions_workspace_id_idx;
DROP TABLE IF EXISTS middleman_workspace_runtime_sessions;

PRAGMA foreign_keys = ON;
