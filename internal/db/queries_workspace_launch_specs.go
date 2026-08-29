package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

func canonicalWorkspaceLaunchSpec(spec WorkspaceLaunchSpec) WorkspaceLaunchSpec {
	spec.IssuedAt = spec.IssuedAt.UTC()
	spec.SourceVisibleUntil = spec.SourceVisibleUntil.UTC()
	return spec
}

func marshalWorkspaceLaunchSpec(spec WorkspaceLaunchSpec) ([]byte, error) {
	spec = canonicalWorkspaceLaunchSpec(spec)
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(spec)
	if err != nil {
		return nil, fmt.Errorf("encode workspace launch specification: %w", err)
	}
	return encoded, nil
}

func insertWorkspaceLaunchSpec(
	ctx context.Context,
	executor workspaceInsertExecutor,
	workspaceID string,
	spec WorkspaceLaunchSpec,
) error {
	encoded, err := marshalWorkspaceLaunchSpec(spec)
	if err != nil {
		return err
	}
	spec = canonicalWorkspaceLaunchSpec(spec)
	_, err = executor.ExecContext(ctx, `
		INSERT INTO forge_workspace_launch_specs (
			workspace_id, version, spec_json, source_visible_until, created_at
		) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(workspace_id) DO UPDATE SET
			version = excluded.version,
			spec_json = excluded.spec_json,
			source_visible_until = excluded.source_visible_until,
			created_at = excluded.created_at`,
		workspaceID, spec.Version, string(encoded),
		spec.SourceVisibleUntil.Format(time.RFC3339Nano),
		spec.IssuedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("persist workspace launch specification: %w", err)
	}
	return nil
}

// CreateWorkspaceWithLaunchSpec inserts a provider-backed workspace and its
// immutable launch facts in one transaction. Validation happens before either
// row is written.
func (d *DB) CreateWorkspaceWithLaunchSpec(
	ctx context.Context,
	workspace *Workspace,
	spec WorkspaceLaunchSpec,
) error {
	if workspace == nil {
		return errors.New("workspace is required")
	}
	if err := spec.ValidateWorkspace(*workspace); err != nil {
		return err
	}
	release, err := d.LockRepositoryReconciliationRead(ctx)
	if err != nil {
		return err
	}
	defer release()
	prepared, err := d.prepareWorkspaceInsert(ctx, workspace)
	if err != nil {
		return err
	}
	if prepared.itemKey != spec.ItemKey {
		return errors.New("workspace launch specification item key does not match persisted workspace key")
	}
	tx, err := d.rw.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("create workspace with launch specification: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := insertPreparedWorkspace(ctx, tx, workspace, prepared); err != nil {
		return err
	}
	if err := insertWorkspaceLaunchSpec(ctx, tx, workspace.ID, spec); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("create workspace with launch specification: commit: %w", err)
	}
	workspace.ItemKey = prepared.itemKey
	return nil
}

func (d *DB) PutWorkspaceLaunchSpec(
	ctx context.Context,
	workspaceID string,
	spec WorkspaceLaunchSpec,
) error {
	workspace, err := d.GetWorkspace(ctx, workspaceID)
	if err != nil {
		return err
	}
	if workspace == nil {
		return sql.ErrNoRows
	}
	if err := spec.ValidateWorkspace(*workspace); err != nil {
		return err
	}
	return insertWorkspaceLaunchSpec(ctx, d.rw, workspaceID, spec)
}

// GetWorkspaceByLaunchSpecIdentity finds a provider-backed workspace by the
// stable repository and provider-item identity persisted in its launch spec.
// Repository display routes are deliberately excluded because they can change.
func (d *DB) GetWorkspaceByLaunchSpecIdentity(
	ctx context.Context,
	platform, platformHost, platformRepoID, itemType, itemKey string,
) (*Workspace, error) {
	platform = strings.ToLower(strings.TrimSpace(platform))
	platformHost = strings.ToLower(strings.TrimSpace(platformHost))
	platformRepoID = strings.TrimSpace(platformRepoID)
	itemType = strings.TrimSpace(itemType)
	itemKey = strings.TrimSpace(itemKey)
	if platform == "" || platformHost == "" || platformRepoID == "" ||
		itemType == "" || itemKey == "" {
		return nil, nil
	}
	workspace, err := scanWorkspace(d.ro.QueryRowContext(ctx, `
		SELECT w.id, w.platform, w.platform_host, w.repo_owner, w.repo_name,
		       w.item_type, w.item_number, w.item_key, w.associated_pr_number,
		       w.git_head_ref, w.mr_head_repo, w.workspace_branch,
		       w.worktree_path, w.tmux_session, w.terminal_backend, w.status,
		       w.error_message, w.created_at, w.kata_metadata
		FROM forge_workspaces w
		JOIN forge_workspace_launch_specs launch ON launch.workspace_id = w.id
		WHERE lower(json_extract(launch.spec_json, '$.repository.provider')) = ?
		  AND lower(json_extract(launch.spec_json, '$.repository.platform_host')) = ?
		  AND json_extract(launch.spec_json, '$.repository.platform_repo_id') = ?
		  AND json_extract(launch.spec_json, '$.item_type') = ?
		  AND json_extract(launch.spec_json, '$.item_key') = ?
		ORDER BY w.created_at, w.id
		LIMIT 1`,
		platform, platformHost, platformRepoID, itemType, itemKey,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get workspace by stable launch identity: %w", err)
	}
	return workspace, nil
}

// ResolveUnambiguousHistoricalWorkspaceRepoID returns the stable identity for
// a current or historical workspace route only when that route has belonged to
// exactly one catalog repository. Reused routes remain unresolved.
func (d *DB) ResolveUnambiguousHistoricalWorkspaceRepoID(
	ctx context.Context, platform, platformHost, owner, name string,
) (string, error) {
	platform = strings.ToLower(strings.TrimSpace(platform))
	platformHost, owner, name = canonicalRepoLookupIdentifier(platformHost, owner, name)
	var platformRepoID string
	err := d.ro.QueryRowContext(ctx, `
		SELECT r.platform_repo_id
		FROM forge_repo_routes route
		JOIN forge_repos r ON r.id = route.repo_id
		WHERE route.platform = ?
		  AND route.platform_host = ?
		  AND route.repo_path_key = ?
		  AND r.lifecycle_state = 'active'
		  AND trim(r.platform_repo_id) <> ''
		  AND NOT EXISTS (
		      SELECT 1
		      FROM forge_repo_routes other
		      WHERE other.platform = route.platform
		        AND other.platform_host = route.platform_host
		        AND other.repo_path_key = route.repo_path_key
		        AND other.repo_id <> route.repo_id
		  )
		LIMIT 1`, platform, platformHost, owner+"/"+name).Scan(&platformRepoID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("resolve historical workspace repository identity: %w", err)
	}
	return platformRepoID, nil
}

// PutRefreshedWorkspaceLaunchSpec persists refreshed provider facts and, when
// the same stable repository has been renamed, moves the workspace route in
// the same transaction. Route adoption is allowed only for the catalog's
// active identity and never across a route with historical occupants.
func (d *DB) PutRefreshedWorkspaceLaunchSpec(
	ctx context.Context,
	workspaceID string,
	spec WorkspaceLaunchSpec,
) (*Workspace, error) {
	release, err := d.LockRepositoryReconciliationRead(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	workspace, err := d.GetWorkspace(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	if workspace == nil {
		return nil, sql.ErrNoRows
	}
	current, err := d.GetWorkspaceLaunchSpec(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	originalPlatform := workspace.Platform
	originalHost := workspace.PlatformHost
	originalOwner := workspace.RepoOwner
	originalName := workspace.RepoName
	routeChanged := !strings.EqualFold(
		canonicalWorkspacePlatform(workspace.Platform), spec.Repository.Provider,
	) ||
		!strings.EqualFold(workspace.PlatformHost, spec.Repository.PlatformHost) ||
		!strings.EqualFold(workspace.RepoOwner, spec.Repository.Owner) ||
		!strings.EqualFold(workspace.RepoName, spec.Repository.Name)
	if routeChanged {
		if current == nil ||
			current.Repository.PlatformRepoID != spec.Repository.PlatformRepoID ||
			!strings.EqualFold(current.Repository.Provider, spec.Repository.Provider) ||
			!strings.EqualFold(current.Repository.PlatformHost, spec.Repository.PlatformHost) {
			return nil, errors.New("refreshed workspace repository identity changed")
		}
		entry, lookupErr := d.GetRepositoryByProviderIDUnderRepositoryReconciliationRead(
			ctx, spec.Repository.Provider, spec.Repository.PlatformHost,
			spec.Repository.PlatformRepoID,
		)
		if lookupErr != nil {
			return nil, lookupErr
		}
		if entry == nil || entry.Lifecycle != RepositoryLifecycleActive ||
			!strings.EqualFold(entry.Repository.Owner, spec.Repository.Owner) ||
			!strings.EqualFold(entry.Repository.Name, spec.Repository.Name) {
			return nil, errors.New("refreshed workspace route is not the active repository route")
		}
		collision, collisionErr := d.workspaceRouteHasHistoricalOccupants(
			ctx, entry.Repository.Platform, entry.Repository.PlatformHost,
			entry.Repository.RepoPathKey,
		)
		if collisionErr != nil {
			return nil, collisionErr
		}
		if collision {
			return nil, errors.New("refreshed workspace route has historical occupants")
		}
		workspace.Platform = entry.Repository.Platform
		workspace.PlatformHost = entry.Repository.PlatformHost
		workspace.RepoOwner = entry.Repository.Owner
		workspace.RepoName = entry.Repository.Name
	}
	if err := spec.ValidateWorkspace(*workspace); err != nil {
		return nil, err
	}

	tx, err := d.rw.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("refresh workspace launch specification: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var result sql.Result
	if routeChanged {
		ownerKey := strings.ToLower(strings.TrimSpace(workspace.RepoOwner))
		nameKey := strings.ToLower(strings.TrimSpace(workspace.RepoName))
		result, err = tx.ExecContext(ctx, `
			UPDATE forge_workspaces
			SET platform = ?, platform_host = ?, repo_owner = ?, repo_name = ?,
			    repo_owner_key = ?, repo_name_key = ?, repo_path_key = ?
			WHERE id = ? AND platform = ? AND platform_host = ?
			  AND repo_owner = ? AND repo_name = ?`,
			workspace.Platform, workspace.PlatformHost,
			workspace.RepoOwner, workspace.RepoName,
			ownerKey, nameKey, ownerKey+"/"+nameKey, workspace.ID,
			originalPlatform, originalHost, originalOwner, originalName,
		)
		if err != nil {
			return nil, fmt.Errorf("refresh workspace repository route: %w", err)
		}
	} else {
		result, err = tx.ExecContext(ctx, `
			UPDATE forge_workspaces SET status = status
			WHERE id = ? AND platform = ? AND platform_host = ?
			  AND repo_owner = ? AND repo_name = ?`,
			workspace.ID, originalPlatform, originalHost, originalOwner, originalName,
		)
		if err != nil {
			return nil, fmt.Errorf("guard workspace repository route: %w", err)
		}
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("read workspace repository route update: %w", err)
	}
	if rowsAffected != 1 {
		return nil, errors.New("workspace repository route changed during refresh")
	}
	if err := insertWorkspaceLaunchSpec(ctx, tx, workspace.ID, spec); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("refresh workspace launch specification: commit: %w", err)
	}
	return workspace, nil
}

func (d *DB) GetWorkspaceLaunchSpec(
	ctx context.Context,
	workspaceID string,
) (*WorkspaceLaunchSpec, error) {
	var version int
	var encoded string
	var visibleUntil, createdAt string
	err := d.ro.QueryRowContext(ctx, `
		SELECT version, spec_json, source_visible_until, created_at
		FROM forge_workspace_launch_specs
		WHERE workspace_id = ?`, workspaceID,
	).Scan(&version, &encoded, &visibleUntil, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read workspace launch specification: %w", err)
	}
	var spec WorkspaceLaunchSpec
	if err := json.Unmarshal([]byte(encoded), &spec); err != nil {
		return nil, fmt.Errorf("decode workspace launch specification: %w", err)
	}
	if spec.Version != version {
		return nil, errors.New("workspace launch specification version columns disagree")
	}
	storedVisibleUntil, err := parseDBTime(visibleUntil)
	if err != nil {
		return nil, fmt.Errorf("parse workspace launch specification visibility deadline: %w", err)
	}
	storedCreatedAt, err := parseDBTime(createdAt)
	if err != nil {
		return nil, fmt.Errorf("parse workspace launch specification creation time: %w", err)
	}
	if !spec.SourceVisibleUntil.UTC().Equal(storedVisibleUntil.UTC()) ||
		!spec.IssuedAt.UTC().Equal(storedCreatedAt.UTC()) {
		return nil, errors.New("workspace launch specification timestamps disagree with indexed columns")
	}
	if err := spec.Validate(); err != nil {
		return nil, fmt.Errorf("validate stored workspace launch specification: %w", err)
	}
	spec = canonicalWorkspaceLaunchSpec(spec)
	return &spec, nil
}

func (d *DB) ListUnpreparedProviderWorkspaces(
	ctx context.Context,
) ([]UnpreparedWorkspace, error) {
	return d.ListUnpreparedProviderWorkspacesAt(ctx, time.Now().UTC())
}

func (d *DB) ListUnpreparedProviderWorkspacesAt(
	ctx context.Context,
	now time.Time,
) ([]UnpreparedWorkspace, error) {
	rows, err := d.ro.QueryContext(ctx, `
		SELECT id, platform, platform_host, repo_owner, repo_name,
		       item_type, item_number, item_key, associated_pr_number,
		       git_head_ref, mr_head_repo, workspace_branch,
		       worktree_path, tmux_session, terminal_backend, status,
		       error_message, created_at, kata_metadata
		FROM forge_workspaces
		WHERE item_type IN ('pull_request', 'issue')
		ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list provider-backed workspaces for preparation: %w", err)
	}
	defer rows.Close()
	var unprepared []UnpreparedWorkspace
	for rows.Next() {
		workspace, err := scanWorkspace(rows)
		if err != nil {
			return nil, err
		}
		spec, err := d.GetWorkspaceLaunchSpec(ctx, workspace.ID)
		reason := ""
		switch {
		case err != nil:
			reason = err.Error()
		case spec == nil:
			reason = "launchSpecMissing"
		case spec.ValidateWorkspace(*workspace) != nil:
			reason = "launchSpecMismatch"
		case errors.Is(spec.RequireVisible(now), ErrLaunchSpecSourceHidden):
			reason = "sourceNotVisible"
		case errors.Is(spec.RequireVisible(now), ErrLaunchSpecRefreshRequired):
			reason = "sourceVisibilityExpired"
		}
		if reason != "" {
			platformRepoID := ""
			if spec != nil {
				platformRepoID = spec.Repository.PlatformRepoID
			} else {
				platformRepoID, err = d.ResolveUnambiguousHistoricalWorkspaceRepoID(
					ctx, workspace.Platform, workspace.PlatformHost,
					workspace.RepoOwner, workspace.RepoName,
				)
				if err != nil {
					return nil, err
				}
			}
			unprepared = append(unprepared, UnpreparedWorkspace{
				Workspace: *workspace, Reason: reason,
				PlatformRepoID: platformRepoID,
			})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return unprepared, nil
}

func (d *DB) CountProviderBackedWorkspaces(ctx context.Context) (int, error) {
	var count int
	err := d.ro.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM forge_workspaces
		WHERE item_type IN ('pull_request', 'issue')`).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count provider-backed workspaces: %w", err)
	}
	return count, nil
}
