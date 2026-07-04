package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

const (
	ItemTypePR    = "pr"
	ItemTypeIssue = "issue"
)

func (d *DB) GetItemWorkflowState(
	ctx context.Context,
	repoID int64,
	itemType string,
	number int,
) (*ItemWorkflowState, error) {
	var w ItemWorkflowState
	err := d.ro.QueryRowContext(ctx,
		`SELECT repo_id, item_type, item_number, status, updated_at,
		        updated_source, updated_actor, updated_reason
		   FROM middleman_item_workflow_state
		  WHERE repo_id = ? AND item_type = ? AND item_number = ?`,
		repoID, itemType, number,
	).Scan(
		&w.RepoID,
		&w.ItemType,
		&w.ItemNumber,
		&w.Status,
		&w.UpdatedAt,
		&w.UpdatedSource,
		&w.UpdatedActor,
		&w.UpdatedReason,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get item workflow state: %w", err)
	}
	return &w, nil
}

func (d *DB) EnsureItemWorkflowState(
	ctx context.Context,
	repoID int64,
	itemType string,
	number int,
) error {
	_, err := d.rw.ExecContext(ctx,
		`INSERT INTO middleman_item_workflow_state (repo_id, item_type, item_number, status)
		 VALUES (?, ?, ?, 'new')
		 ON CONFLICT(repo_id, item_type, item_number) DO NOTHING`,
		repoID, itemType, number,
	)
	if err != nil {
		return fmt.Errorf("ensure item workflow state: %w", err)
	}
	return nil
}

func (d *DB) SetItemWorkflowState(
	ctx context.Context,
	p SetItemWorkflowStateParams,
) (string, error) {
	tx, err := d.rw.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("set item workflow state: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	current := "new"
	err = tx.QueryRowContext(ctx,
		`SELECT status
		   FROM middleman_item_workflow_state
		  WHERE repo_id = ? AND item_type = ? AND item_number = ?`,
		p.RepoID, p.ItemType, p.ItemNumber,
	).Scan(&current)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("set item workflow state: read current: %w", err)
	}
	if p.ExpectedStatus != "" && p.ExpectedStatus != current {
		return "", &WorkflowStateConflictError{
			Expected: p.ExpectedStatus,
			Current:  current,
		}
	}

	_, err = tx.ExecContext(ctx,
		`INSERT INTO middleman_item_workflow_state (
		     repo_id, item_type, item_number, status, updated_at,
		     updated_source, updated_actor, updated_reason
		 )
		 VALUES (?, ?, ?, ?, datetime('now'), ?, ?, ?)
		 ON CONFLICT(repo_id, item_type, item_number) DO UPDATE SET
		     status = excluded.status,
		     updated_at = excluded.updated_at,
		     updated_source = excluded.updated_source,
		     updated_actor = excluded.updated_actor,
		     updated_reason = excluded.updated_reason`,
		p.RepoID,
		p.ItemType,
		p.ItemNumber,
		p.Status,
		p.Source,
		p.Actor,
		p.Reason,
	)
	if err != nil {
		return "", fmt.Errorf("set item workflow state: upsert: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("set item workflow state: commit: %w", err)
	}
	return current, nil
}
