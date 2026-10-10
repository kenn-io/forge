package db

import (
	"context"
	"fmt"
	"time"
)

// WorkspaceTarget is an explicit tracking link, never a workspace owner or Git target.
type WorkspaceTarget struct {
	ID          int64
	WorkspaceID string
	RepoID      int64
	ItemType    string
	ItemNumber  int
	URL         string
	CreatedAt   time.Time
	Hidden      bool
}

func (d *DB) AddWorkspaceTarget(ctx context.Context, target WorkspaceTarget) (int64, error) {
	if target.ItemType != WorkspaceItemTypePullRequest && target.ItemType != WorkspaceItemTypeIssue || target.ItemNumber <= 0 {
		return 0, fmt.Errorf("invalid workspace target")
	}
	var id int64
	err := d.rwQueryRowContext(ctx, `INSERT INTO forge_workspace_targets (workspace_id, repo_id, item_type, item_number, url, created_at, hidden)
 VALUES (?, ?, ?, ?, ?, ?, ?)
 ON CONFLICT(workspace_id, repo_id, item_type, item_number) DO UPDATE SET url = excluded.url, hidden = excluded.hidden
 RETURNING id`, target.WorkspaceID, target.RepoID, target.ItemType, target.ItemNumber, target.URL, time.Now().UTC(), target.Hidden).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("add workspace target: %w", err)
	}
	return id, nil
}

func (d *DB) ListWorkspaceTargets(ctx context.Context, workspaceID string) ([]WorkspaceTarget, error) {
	rows, err := d.roQueryContext(ctx, `SELECT id,workspace_id,repo_id,item_type,item_number,url,created_at,hidden FROM forge_workspace_targets WHERE workspace_id = ? ORDER BY id`, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("list workspace targets: %w", err)
	}
	defer rows.Close()
	targets := make([]WorkspaceTarget, 0)
	for rows.Next() {
		var target WorkspaceTarget
		if err := rows.Scan(&target.ID, &target.WorkspaceID, &target.RepoID, &target.ItemType, &target.ItemNumber, &target.URL, &target.CreatedAt, &target.Hidden); err != nil {
			return nil, fmt.Errorf("scan workspace target: %w", err)
		}
		target.CreatedAt = target.CreatedAt.UTC()
		targets = append(targets, target)
	}
	return targets, rows.Err()
}

func (d *DB) RemoveWorkspaceTarget(ctx context.Context, workspaceID, itemType string, targetID int64) (bool, error) {
	result, err := d.execContext(ctx, `DELETE FROM forge_workspace_targets WHERE workspace_id = ? AND item_type = ? AND id = ?`, workspaceID, itemType, targetID)
	if err != nil {
		return false, fmt.Errorf("remove workspace target: %w", err)
	}
	count, err := result.RowsAffected()
	return count > 0, err
}
