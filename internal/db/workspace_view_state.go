package db

import (
	"context"
	"fmt"
)

// GetWorkspaceActiveTab returns the saved tab, or an empty string when unset.
// A missing workspace returns sql.ErrNoRows.
func (d *DB) GetWorkspaceActiveTab(ctx context.Context, workspaceID string) (string, error) {
	var activeTab string
	err := d.roQueryRowContext(ctx, `
		SELECT COALESCE(state.active_tab, '')
		FROM forge_workspaces AS workspace
		LEFT JOIN forge_workspace_view_state AS state ON state.workspace_id = workspace.id
		WHERE workspace.id = ?`, workspaceID).Scan(&activeTab)
	if err != nil {
		return "", fmt.Errorf("get workspace active tab: %w", err)
	}
	return activeTab, nil
}

// SetWorkspaceActiveTab persists the selection on the owning workspace.
// A missing workspace returns sql.ErrNoRows.
func (d *DB) SetWorkspaceActiveTab(ctx context.Context, workspaceID, activeTab string) error {
	var saved string
	err := d.rwQueryRowContext(ctx, `
		INSERT INTO forge_workspace_view_state (workspace_id, active_tab)
		SELECT id, ? FROM forge_workspaces WHERE id = ?
		ON CONFLICT(workspace_id) DO UPDATE SET active_tab = excluded.active_tab
		RETURNING active_tab`, activeTab, workspaceID).Scan(&saved)
	if err != nil {
		return fmt.Errorf("set workspace active tab: %w", err)
	}
	return nil
}
