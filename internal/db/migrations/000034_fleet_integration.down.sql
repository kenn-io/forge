DROP TABLE middleman_host_runtime_sessions;

DROP TABLE middleman_worktree_stats;

ALTER TABLE middleman_workspace_runtime_sessions DROP COLUMN display_region;

ALTER TABLE middleman_project_worktrees DROP COLUMN linked_issue_numbers;
ALTER TABLE middleman_project_worktrees DROP COLUMN session_backend;
ALTER TABLE middleman_project_worktrees DROP COLUMN is_hidden;
ALTER TABLE middleman_project_worktrees DROP COLUMN is_stale;
ALTER TABLE middleman_projects DROP COLUMN repository_kind;
ALTER TABLE middleman_projects DROP COLUMN is_stale;

DROP INDEX middleman_project_worktree_runtime_sessions_worktree_id_idx;
DROP TABLE middleman_project_worktree_runtime_sessions;
