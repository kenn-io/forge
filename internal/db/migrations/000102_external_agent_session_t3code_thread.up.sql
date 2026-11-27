-- The T3 Code thread whose agent reported the session. T3 Code then runs
-- every turn of it, so Forge sends the person's prompts to that thread.
ALTER TABLE forge_external_agent_sessions ADD COLUMN t3code_thread_id TEXT NOT NULL DEFAULT '';
