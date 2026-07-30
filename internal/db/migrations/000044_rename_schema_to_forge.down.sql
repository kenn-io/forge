DROP INDEX idx_forge_notification_items_ack_queue;
DROP INDEX idx_forge_notification_items_inbox;
DROP INDEX idx_forge_notification_items_item;
DROP INDEX idx_forge_notification_items_reason;
DROP INDEX idx_forge_notification_items_repo;
DROP INDEX forge_project_worktree_runtime_sessions_worktree_id_idx;
DROP INDEX forge_project_worktrees_project_id_idx;
DROP INDEX forge_projects_repo_id_idx;
DROP INDEX forge_workspace_runtime_sessions_workspace_id_idx;
DROP INDEX forge_workspace_setup_events_workspace_id_idx;
DROP TRIGGER forge_repos_casefold_insert;
DROP TRIGGER forge_repos_casefold_update;
DROP INDEX idx_workspaces_provider_item_key;
DROP TRIGGER forge_workspaces_casefold_insert;
DROP TRIGGER forge_workspaces_casefold_update;
DROP TRIGGER forge_workspaces_key_fill_insert;
ALTER TABLE forge_workspaces RENAME COLUMN workspace_branch TO workspace_branch_rename_legacy;
ALTER TABLE forge_workspaces ADD COLUMN workspace_branch TEXT NOT NULL DEFAULT '__middleman_unknown__';
UPDATE forge_workspaces SET workspace_branch = CASE workspace_branch_rename_legacy
    WHEN '__kenn_forge_unknown__' THEN '__middleman_unknown__'
    WHEN '__kenn_forge_recovery_pending__..state' THEN '__middleman_recovery_pending__..state'
    ELSE workspace_branch_rename_legacy
END;
ALTER TABLE forge_workspaces DROP COLUMN workspace_branch_rename_legacy;
ALTER TABLE forge_worktree_stats RENAME TO middleman_worktree_stats;
ALTER TABLE forge_workspaces RENAME TO middleman_workspaces;
ALTER TABLE forge_workspace_setup_events RENAME TO middleman_workspace_setup_events;
ALTER TABLE forge_workspace_runtime_sessions RENAME TO middleman_workspace_runtime_sessions;
ALTER TABLE forge_starred_items RENAME TO middleman_starred_items;
ALTER TABLE forge_stacks RENAME TO middleman_stacks;
ALTER TABLE forge_stack_members RENAME TO middleman_stack_members;
ALTER TABLE forge_repos RENAME TO middleman_repos;
ALTER TABLE forge_repo_overviews RENAME TO middleman_repo_overviews;
ALTER TABLE forge_rate_limits RENAME TO middleman_rate_limits;
ALTER TABLE forge_projects RENAME TO middleman_projects;
ALTER TABLE forge_project_worktrees RENAME TO middleman_project_worktrees;
ALTER TABLE forge_project_worktree_runtime_sessions RENAME TO middleman_project_worktree_runtime_sessions;
ALTER TABLE forge_notification_sync_watermarks RENAME TO middleman_notification_sync_watermarks;
ALTER TABLE forge_notification_items RENAME TO middleman_notification_items;
ALTER TABLE forge_mr_worktree_links RENAME TO middleman_mr_worktree_links;
ALTER TABLE forge_mr_review_threads RENAME TO middleman_mr_review_threads;
ALTER TABLE forge_mr_review_drafts RENAME TO middleman_mr_review_drafts;
ALTER TABLE forge_mr_review_draft_comments RENAME TO middleman_mr_review_draft_comments;
ALTER TABLE forge_mr_events RENAME TO middleman_mr_events;
ALTER TABLE forge_merge_requests RENAME TO middleman_merge_requests;
ALTER TABLE forge_merge_request_labels RENAME TO middleman_merge_request_labels;
ALTER TABLE forge_labels RENAME TO middleman_labels;
ALTER TABLE forge_item_workflow_state RENAME TO middleman_item_workflow_state;
ALTER TABLE forge_issues RENAME TO middleman_issues;
ALTER TABLE forge_issue_labels RENAME TO middleman_issue_labels;
ALTER TABLE forge_issue_events RENAME TO middleman_issue_events;
ALTER TABLE forge_http_etags RENAME TO middleman_http_etags;
ALTER TABLE forge_host_runtime_sessions RENAME TO middleman_host_runtime_sessions;
ALTER TABLE forge_branch_tips RENAME TO middleman_branch_tips;
ALTER TABLE forge_branch_force_pushes RENAME TO middleman_branch_force_pushes;
ALTER TABLE forge_branch_commits RENAME TO middleman_branch_commits;
ALTER TABLE forge_archive_repos RENAME TO middleman_archive_repos;
ALTER TABLE forge_archive_repo_scans RENAME TO middleman_archive_repo_scans;
ALTER TABLE forge_archive_items RENAME TO middleman_archive_items;
ALTER TABLE forge_archive_dataset_progress RENAME TO middleman_archive_dataset_progress;
ALTER TABLE forge_app_metadata RENAME TO middleman_app_metadata;
CREATE TRIGGER middleman_workspaces_key_fill_insert
AFTER INSERT ON middleman_workspaces
WHEN NEW.repo_path_key = ''
BEGIN
    UPDATE middleman_workspaces
    SET repo_owner_key = lower(repo_owner),
        repo_name_key = lower(repo_name),
        repo_path_key = lower(repo_owner) || '/' || lower(repo_name)
    WHERE id = NEW.id;
END;
CREATE TRIGGER middleman_workspaces_casefold_update
BEFORE UPDATE OF platform, platform_host, repo_owner, repo_name, repo_owner_key, repo_name_key, repo_path_key ON middleman_workspaces
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
          FROM middleman_repos r
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
CREATE TRIGGER middleman_workspaces_casefold_insert
BEFORE INSERT ON middleman_workspaces
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
          FROM middleman_repos r
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
CREATE UNIQUE INDEX idx_workspaces_provider_item_key
    ON middleman_workspaces(platform, platform_host, repo_path_key, item_type, item_key);
CREATE TRIGGER middleman_repos_casefold_update
BEFORE UPDATE OF platform, platform_host, owner, name, repo_path, owner_key, name_key, repo_path_key ON middleman_repos
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
CREATE TRIGGER middleman_repos_casefold_insert
BEFORE INSERT ON middleman_repos
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
CREATE INDEX middleman_workspace_setup_events_workspace_id_idx
    ON middleman_workspace_setup_events (workspace_id, id);
CREATE INDEX middleman_workspace_runtime_sessions_workspace_id_idx
    ON middleman_workspace_runtime_sessions(workspace_id);
CREATE INDEX middleman_projects_repo_id_idx
    ON middleman_projects (repo_id) WHERE repo_id IS NOT NULL;
CREATE INDEX middleman_project_worktrees_project_id_idx
    ON middleman_project_worktrees (project_id);
CREATE INDEX middleman_project_worktree_runtime_sessions_worktree_id_idx
    ON middleman_project_worktree_runtime_sessions(worktree_id);
CREATE INDEX idx_middleman_notification_items_repo
    ON middleman_notification_items(platform, platform_host, repo_owner, repo_name, source_updated_at DESC);
CREATE INDEX idx_middleman_notification_items_reason
    ON middleman_notification_items(reason, source_updated_at DESC);
CREATE INDEX idx_middleman_notification_items_item
    ON middleman_notification_items(platform, platform_host, repo_owner, repo_name, item_type, item_number);
CREATE INDEX idx_middleman_notification_items_inbox
    ON middleman_notification_items(done_at, unread, source_updated_at DESC);
CREATE INDEX idx_middleman_notification_items_ack_queue
    ON middleman_notification_items(platform, platform_host, source_ack_queued_at, source_ack_next_attempt_at, source_ack_synced_at);
