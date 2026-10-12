-- The conversation an agent last reported through its hooks, so recovery can
-- resume it after the agent's activity report is gone.
ALTER TABLE forge_workspace_runtime_sessions ADD COLUMN conversation_agent TEXT NOT NULL DEFAULT '';
ALTER TABLE forge_workspace_runtime_sessions ADD COLUMN conversation_id TEXT NOT NULL DEFAULT '';
