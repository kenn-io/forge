DROP TABLE IF EXISTS middleman_host_runtime_tmux_sessions;

DROP TABLE IF EXISTS middleman_worktree_stats;

ALTER TABLE middleman_project_worktrees DROP COLUMN linked_issue_numbers;
ALTER TABLE middleman_project_worktrees DROP COLUMN session_backend;
ALTER TABLE middleman_project_worktrees DROP COLUMN is_hidden;
ALTER TABLE middleman_project_worktrees DROP COLUMN is_stale;
ALTER TABLE middleman_projects DROP COLUMN repository_kind;
ALTER TABLE middleman_projects DROP COLUMN is_stale;

DROP INDEX IF EXISTS middleman_project_worktree_tmux_sessions_worktree_id_idx;
DROP TABLE IF EXISTS middleman_project_worktree_tmux_sessions;
