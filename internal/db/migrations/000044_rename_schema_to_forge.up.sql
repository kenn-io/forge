DROP INDEX idx_middleman_notification_items_ack_queue;
DROP INDEX idx_middleman_notification_items_inbox;
DROP INDEX idx_middleman_notification_items_item;
DROP INDEX idx_middleman_notification_items_reason;
DROP INDEX idx_middleman_notification_items_repo;
DROP INDEX middleman_project_worktree_runtime_sessions_worktree_id_idx;
DROP INDEX middleman_project_worktrees_project_id_idx;
DROP INDEX middleman_projects_repo_id_idx;
DROP INDEX middleman_workspace_runtime_sessions_workspace_id_idx;
DROP INDEX middleman_workspace_setup_events_workspace_id_idx;
DROP TRIGGER middleman_repos_casefold_insert;
DROP TRIGGER middleman_repos_casefold_update;
ALTER TABLE middleman_app_metadata RENAME TO forge_app_metadata;
ALTER TABLE middleman_archive_dataset_progress RENAME TO forge_archive_dataset_progress;
ALTER TABLE middleman_archive_items RENAME TO forge_archive_items;
ALTER TABLE middleman_archive_repo_scans RENAME TO forge_archive_repo_scans;
ALTER TABLE middleman_archive_repos RENAME TO forge_archive_repos;
ALTER TABLE middleman_branch_commits RENAME TO forge_branch_commits;
ALTER TABLE middleman_branch_force_pushes RENAME TO forge_branch_force_pushes;
ALTER TABLE middleman_branch_tips RENAME TO forge_branch_tips;
ALTER TABLE middleman_host_runtime_sessions RENAME TO forge_host_runtime_sessions;
ALTER TABLE middleman_http_etags RENAME TO forge_http_etags;
ALTER TABLE middleman_issue_events RENAME TO forge_issue_events;
ALTER TABLE middleman_issue_labels RENAME TO forge_issue_labels;
ALTER TABLE middleman_issues RENAME TO forge_issues;
ALTER TABLE middleman_item_workflow_state RENAME TO forge_item_workflow_state;
ALTER TABLE middleman_labels RENAME TO forge_labels;
ALTER TABLE middleman_merge_request_labels RENAME TO forge_merge_request_labels;
ALTER TABLE middleman_merge_requests RENAME TO forge_merge_requests;
ALTER TABLE middleman_mr_events RENAME TO forge_mr_events;
ALTER TABLE middleman_mr_review_draft_comments RENAME TO forge_mr_review_draft_comments;
ALTER TABLE middleman_mr_review_drafts RENAME TO forge_mr_review_drafts;
ALTER TABLE middleman_mr_review_threads RENAME TO forge_mr_review_threads;
ALTER TABLE middleman_mr_worktree_links RENAME TO forge_mr_worktree_links;
ALTER TABLE middleman_notification_items RENAME TO forge_notification_items;
ALTER TABLE middleman_notification_sync_watermarks RENAME TO forge_notification_sync_watermarks;
ALTER TABLE middleman_project_worktree_runtime_sessions RENAME TO forge_project_worktree_runtime_sessions;
ALTER TABLE middleman_project_worktrees RENAME TO forge_project_worktrees;
ALTER TABLE middleman_projects RENAME TO forge_projects;
ALTER TABLE middleman_rate_limits RENAME TO forge_rate_limits;
ALTER TABLE middleman_repo_overviews RENAME TO forge_repo_overviews;
ALTER TABLE middleman_repos RENAME TO forge_repos;
ALTER TABLE middleman_stack_members RENAME TO forge_stack_members;
ALTER TABLE middleman_stacks RENAME TO forge_stacks;
ALTER TABLE middleman_starred_items RENAME TO forge_starred_items;
ALTER TABLE middleman_workspace_runtime_sessions RENAME TO forge_workspace_runtime_sessions;
ALTER TABLE middleman_workspace_setup_events RENAME TO forge_workspace_setup_events;
ALTER TABLE middleman_workspaces RENAME TO forge_workspaces;
ALTER TABLE middleman_worktree_stats RENAME TO forge_worktree_stats;
DROP INDEX idx_workspaces_provider_item_key;
DROP TRIGGER middleman_workspaces_casefold_insert;
DROP TRIGGER middleman_workspaces_casefold_update;
DROP TRIGGER middleman_workspaces_key_fill_insert;
ALTER TABLE forge_workspaces RENAME COLUMN workspace_branch TO workspace_branch_rename_legacy;
ALTER TABLE forge_workspaces ADD COLUMN workspace_branch TEXT NOT NULL DEFAULT '__kenn_forge_unknown__';
UPDATE forge_workspaces SET workspace_branch = CASE workspace_branch_rename_legacy
    WHEN '__middleman_unknown__' THEN '__kenn_forge_unknown__'
    WHEN '__middleman_recovery_pending__..state' THEN '__kenn_forge_recovery_pending__..state'
    ELSE workspace_branch_rename_legacy
END;
ALTER TABLE forge_workspaces DROP COLUMN workspace_branch_rename_legacy;
CREATE UNIQUE INDEX idx_workspaces_provider_item_key
    ON forge_workspaces(platform, platform_host, repo_path_key, item_type, item_key);
CREATE TRIGGER forge_workspaces_casefold_insert
BEFORE INSERT ON forge_workspaces
WHEN NEW.platform <> lower(NEW.platform)
  OR NEW.platform_host <> lower(NEW.platform_host)
  OR (
      NEW.repo_path_key = ''
      AND (
          NEW.repo_owner <> lower(NEW.repo_owner)
          OR NEW.repo_name <> lower(NEW.repo_name)
      )
  )
  OR (
      NEW.repo_path_key <> ''
      AND (
          NEW.repo_owner_key <> lower(NEW.repo_owner_key)
          OR NEW.repo_name_key <> lower(NEW.repo_name_key)
          OR NEW.repo_path_key <> lower(NEW.repo_path_key)
          OR NEW.repo_path_key <> NEW.repo_owner_key || '/' || NEW.repo_name_key
      )
  )
  OR (
      NEW.repo_path_key <> ''
      AND
      NOT EXISTS (
          SELECT 1
          FROM forge_repos r
          WHERE r.platform = NEW.platform
            AND r.platform_host = NEW.platform_host
            AND r.repo_path_key = NEW.repo_path_key
            AND r.platform <> 'github'
      )
      AND (
          NEW.repo_owner <> NEW.repo_owner_key
          OR NEW.repo_name <> NEW.repo_name_key
      )
  )
BEGIN
    SELECT RAISE(ABORT, 'workspace repo identifiers must be provider-canonical');
END;
CREATE TRIGGER forge_workspaces_casefold_update
BEFORE UPDATE OF platform, platform_host, repo_owner, repo_name, repo_owner_key, repo_name_key, repo_path_key ON forge_workspaces
WHEN NEW.platform <> lower(NEW.platform)
  OR NEW.platform_host <> lower(NEW.platform_host)
  OR NEW.repo_path_key = ''
  OR NEW.repo_owner_key <> lower(NEW.repo_owner_key)
  OR NEW.repo_name_key <> lower(NEW.repo_name_key)
  OR NEW.repo_path_key <> lower(NEW.repo_path_key)
  OR NEW.repo_path_key <> NEW.repo_owner_key || '/' || NEW.repo_name_key
  OR (
      NOT EXISTS (
          SELECT 1
          FROM forge_repos r
          WHERE r.platform = NEW.platform
            AND r.platform_host = NEW.platform_host
            AND r.repo_path_key = NEW.repo_path_key
            AND r.platform <> 'github'
      )
      AND (
          NEW.repo_owner <> NEW.repo_owner_key
          OR NEW.repo_name <> NEW.repo_name_key
      )
  )
BEGIN
    SELECT RAISE(ABORT, 'workspace repo identifiers must be provider-canonical');
END;
CREATE TRIGGER forge_workspaces_key_fill_insert
AFTER INSERT ON forge_workspaces
WHEN NEW.repo_path_key = ''
BEGIN
    UPDATE forge_workspaces
    SET repo_owner_key = lower(repo_owner),
        repo_name_key = lower(repo_name),
        repo_path_key = lower(repo_owner) || '/' || lower(repo_name)
    WHERE id = NEW.id;
END;
CREATE INDEX idx_forge_notification_items_ack_queue
    ON forge_notification_items(platform, platform_host, source_ack_queued_at, source_ack_next_attempt_at, source_ack_synced_at);
CREATE INDEX idx_forge_notification_items_inbox
    ON forge_notification_items(done_at, unread, source_updated_at DESC);
CREATE INDEX idx_forge_notification_items_item
    ON forge_notification_items(platform, platform_host, repo_owner, repo_name, item_type, item_number);
CREATE INDEX idx_forge_notification_items_reason
    ON forge_notification_items(reason, source_updated_at DESC);
CREATE INDEX idx_forge_notification_items_repo
    ON forge_notification_items(platform, platform_host, repo_owner, repo_name, source_updated_at DESC);
CREATE INDEX forge_project_worktree_runtime_sessions_worktree_id_idx
    ON forge_project_worktree_runtime_sessions(worktree_id);
CREATE INDEX forge_project_worktrees_project_id_idx
    ON forge_project_worktrees (project_id);
CREATE INDEX forge_projects_repo_id_idx
    ON forge_projects (repo_id) WHERE repo_id IS NOT NULL;
CREATE INDEX forge_workspace_runtime_sessions_workspace_id_idx
    ON forge_workspace_runtime_sessions(workspace_id);
CREATE INDEX forge_workspace_setup_events_workspace_id_idx
    ON forge_workspace_setup_events (workspace_id, id);
CREATE TRIGGER forge_repos_casefold_insert
BEFORE INSERT ON forge_repos
WHEN NEW.platform <> lower(NEW.platform)
  OR NEW.platform_host <> lower(NEW.platform_host)
  OR NEW.repo_path = ''
  OR NEW.owner_key <> lower(NEW.owner)
  OR NEW.name_key <> lower(NEW.name)
  OR NEW.repo_path_key <> lower(NEW.repo_path)
  OR (
      lower(NEW.platform) = 'github'
      AND (
          NEW.owner <> lower(NEW.owner)
          OR NEW.name <> lower(NEW.name)
          OR NEW.repo_path <> lower(NEW.repo_path)
      )
  )
BEGIN
    SELECT RAISE(ABORT, 'repo identifiers must be provider-canonical');
END;
CREATE TRIGGER forge_repos_casefold_update
BEFORE UPDATE OF platform, platform_host, owner, name, repo_path, owner_key, name_key, repo_path_key ON forge_repos
WHEN NEW.platform <> lower(NEW.platform)
  OR NEW.platform_host <> lower(NEW.platform_host)
  OR NEW.repo_path = ''
  OR NEW.owner_key <> lower(NEW.owner)
  OR NEW.name_key <> lower(NEW.name)
  OR NEW.repo_path_key <> lower(NEW.repo_path)
  OR (
      lower(NEW.platform) = 'github'
      AND (
          NEW.owner <> lower(NEW.owner)
          OR NEW.name <> lower(NEW.name)
          OR NEW.repo_path <> lower(NEW.repo_path)
      )
  )
BEGIN
    SELECT RAISE(ABORT, 'repo identifiers must be provider-canonical');
END;
