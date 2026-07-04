package db

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

const (
	ItemTypePR    = "pr"
	ItemTypeIssue = "issue"
)

func validateItemWorkflowType(itemType string) error {
	switch itemType {
	case ItemTypePR, ItemTypeIssue:
		return nil
	default:
		return fmt.Errorf("invalid item workflow type %q", itemType)
	}
}

func validateItemWorkflowStatus(status string) error {
	switch KanbanStatus(status) {
	case KanbanStatusNew, KanbanStatusReviewing, KanbanStatusWaiting, KanbanStatusAwaitingMerge:
		return nil
	default:
		return fmt.Errorf("invalid item workflow status %q", status)
	}
}

func (d *DB) GetItemWorkflowState(
	ctx context.Context,
	repoID int64,
	itemType string,
	number int,
) (*ItemWorkflowState, error) {
	if err := validateItemWorkflowType(itemType); err != nil {
		return nil, err
	}
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
	if err := validateItemWorkflowType(itemType); err != nil {
		return err
	}
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
	if err := validateItemWorkflowType(p.ItemType); err != nil {
		return "", err
	}
	if err := validateItemWorkflowStatus(p.Status); err != nil {
		return "", err
	}
	if p.ExpectedStatus != "" {
		if err := validateItemWorkflowStatus(p.ExpectedStatus); err != nil {
			return "", err
		}
	}

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

type workflowCursor struct {
	sortKey     string
	activityKey string
	platform    string
	host        string
	owner       string
	name        string
	itemType    string
	number      int
}

func encodeWorkflowCursor(row WorkflowStateListRow, sortKey, activityKey string) string {
	raw := strings.Join([]string{
		sortKey,
		activityKey,
		row.Platform,
		row.PlatformHost,
		row.Owner,
		row.Name,
		row.ItemType,
		strconv.Itoa(row.Number),
	}, "\x1f")
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeWorkflowCursor(cursor string) (workflowCursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return workflowCursor{}, fmt.Errorf("invalid cursor: %w", err)
	}
	parts := strings.Split(string(raw), "\x1f")
	if len(parts) != 8 {
		return workflowCursor{}, errors.New("invalid cursor")
	}
	number, err := strconv.Atoi(parts[7])
	if err != nil {
		return workflowCursor{}, fmt.Errorf("invalid cursor number: %w", err)
	}
	return workflowCursor{
		sortKey:     parts[0],
		activityKey: parts[1],
		platform:    parts[2],
		host:        parts[3],
		owner:       parts[4],
		name:        parts[5],
		itemType:    parts[6],
		number:      number,
	}, nil
}

func workflowPlaceholders(count int) string {
	if count <= 0 {
		return ""
	}
	return strings.TrimRight(strings.Repeat("?,", count), ",")
}

func workflowListLimit(limit int) int {
	switch {
	case limit <= 0:
		return 50
	case limit > 200:
		return 200
	default:
		return limit
	}
}

func (d *DB) ListItemWorkflowStates(
	ctx context.Context,
	opts ListWorkflowStatesOpts,
) ([]WorkflowStateListRow, string, error) {
	limit := workflowListLimit(opts.Limit)

	prArgs := []any{}
	prWhere := ""
	if cond := repoListFilterCondition("r", opts.RepoFilters, &prArgs); cond != "" {
		prWhere = "WHERE " + cond
	}
	issueArgs := []any{}
	issueWhere := ""
	if cond := repoListFilterCondition("r", opts.RepoFilters, &issueArgs); cond != "" {
		issueWhere = "WHERE " + cond
	}

	args := make([]any, 0, len(prArgs)+len(issueArgs)+len(opts.ItemTypes)+len(opts.States)+16)
	args = append(args, prArgs...)
	args = append(args, issueArgs...)

	var conds []string
	if len(opts.ItemTypes) > 0 {
		for _, itemType := range opts.ItemTypes {
			if err := validateItemWorkflowType(itemType); err != nil {
				return nil, "", err
			}
		}
		conds = append(conds, "t.item_type IN ("+workflowPlaceholders(len(opts.ItemTypes))+")")
		for _, itemType := range opts.ItemTypes {
			args = append(args, itemType)
		}
	}
	if len(opts.States) > 0 {
		for _, state := range opts.States {
			if err := validateItemWorkflowStatus(state); err != nil {
				return nil, "", err
			}
		}
		conds = append(conds, "t.status IN ("+workflowPlaceholders(len(opts.States))+")")
		for _, state := range opts.States {
			args = append(args, state)
		}
	}
	if !opts.IncludeClosed {
		conds = append(conds, "t.state NOT IN ('closed', 'merged')")
	}
	if opts.Cursor != "" {
		cursor, err := decodeWorkflowCursor(opts.Cursor)
		if err != nil {
			return nil, "", err
		}
		conds = append(conds, `(t.sort_key < ?
 OR (t.sort_key = ? AND t.activity_key < ?)
 OR (t.sort_key = ? AND t.activity_key = ?
     AND (t.platform, t.platform_host, t.owner, t.name, t.item_type, t.number)
         > (?, ?, ?, ?, ?, ?)))`)
		args = append(args,
			cursor.sortKey,
			cursor.sortKey, cursor.activityKey,
			cursor.sortKey, cursor.activityKey,
			cursor.platform, cursor.host, cursor.owner, cursor.name,
			cursor.itemType, cursor.number,
		)
	}

	where := ""
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ")
	}
	args = append(args, limit+1)

	query := fmt.Sprintf(`
		SELECT t.platform, t.platform_host, t.owner, t.name, t.repo_path,
		       t.item_type, t.number, t.title, t.state, t.url, t.author,
		       t.is_draft, t.last_activity_at, t.status, t.has_row,
		       t.updated_at, t.updated_source, t.updated_actor,
		       t.updated_reason, t.sort_key, t.activity_key
		FROM (
		    SELECT r.platform, r.platform_host, r.owner, r.name,
		           COALESCE(NULLIF(r.repo_path, ''), r.owner || '/' || r.name) AS repo_path,
		           'pr' AS item_type, p.number, p.title, p.state, p.url, p.author,
		           p.is_draft, p.last_activity_at,
		           COALESCE(w.status, 'new') AS status,
		           (w.repo_id IS NOT NULL) AS has_row,
		           w.updated_at, COALESCE(w.updated_source, '') AS updated_source,
		           COALESCE(w.updated_actor, '') AS updated_actor,
		           COALESCE(w.updated_reason, '') AS updated_reason,
		           CAST(COALESCE(w.updated_at, p.last_activity_at) AS TEXT) AS sort_key,
		           CAST(p.last_activity_at AS TEXT) AS activity_key
		    FROM middleman_merge_requests p
		    JOIN middleman_repos r ON r.id = p.repo_id
		    LEFT JOIN middleman_item_workflow_state w
		        ON w.repo_id = p.repo_id AND w.item_type = 'pr' AND w.item_number = p.number
		    %s
		    UNION ALL
		    SELECT r.platform, r.platform_host, r.owner, r.name,
		           COALESCE(NULLIF(r.repo_path, ''), r.owner || '/' || r.name) AS repo_path,
		           'issue' AS item_type, i.number, i.title, i.state, i.url, i.author,
		           0 AS is_draft, i.last_activity_at,
		           COALESCE(w.status, 'new') AS status,
		           (w.repo_id IS NOT NULL) AS has_row,
		           w.updated_at, COALESCE(w.updated_source, '') AS updated_source,
		           COALESCE(w.updated_actor, '') AS updated_actor,
		           COALESCE(w.updated_reason, '') AS updated_reason,
		           CAST(COALESCE(w.updated_at, i.last_activity_at) AS TEXT) AS sort_key,
		           CAST(i.last_activity_at AS TEXT) AS activity_key
		    FROM middleman_issues i
		    JOIN middleman_repos r ON r.id = i.repo_id
		    LEFT JOIN middleman_item_workflow_state w
		        ON w.repo_id = i.repo_id AND w.item_type = 'issue' AND w.item_number = i.number
		    %s
		) t
		%s
		ORDER BY t.sort_key DESC, t.activity_key DESC,
		         t.platform, t.platform_host, t.owner, t.name, t.item_type, t.number
		LIMIT ?`, prWhere, issueWhere, where)

	rows, err := d.ro.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, "", fmt.Errorf("list item workflow states: %w", err)
	}
	defer rows.Close()

	type rowWithCursor struct {
		row         WorkflowStateListRow
		sortKey     string
		activityKey string
	}
	var scanned []rowWithCursor
	for rows.Next() {
		var row WorkflowStateListRow
		var lastActivityRaw string
		var updatedAtRaw sql.NullString
		var sortKey string
		var activityKey string
		if err := rows.Scan(
			&row.Platform,
			&row.PlatformHost,
			&row.Owner,
			&row.Name,
			&row.RepoPath,
			&row.ItemType,
			&row.Number,
			&row.Title,
			&row.State,
			&row.URL,
			&row.Author,
			&row.IsDraft,
			&lastActivityRaw,
			&row.Status,
			&row.HasRow,
			&updatedAtRaw,
			&row.UpdatedSource,
			&row.UpdatedActor,
			&row.UpdatedReason,
			&sortKey,
			&activityKey,
		); err != nil {
			return nil, "", fmt.Errorf("scan workflow state row: %w", err)
		}
		lastActivity, err := parseDBTime(lastActivityRaw)
		if err != nil {
			return nil, "", fmt.Errorf("parse workflow last_activity_at: %w", err)
		}
		row.LastActivityAt = lastActivity
		if updatedAtRaw.Valid && updatedAtRaw.String != "" {
			updatedAt, err := parseDBTime(updatedAtRaw.String)
			if err != nil {
				return nil, "", fmt.Errorf("parse workflow updated_at: %w", err)
			}
			row.UpdatedAt = &updatedAt
		}
		scanned = append(scanned, rowWithCursor{
			row:         row,
			sortKey:     sortKey,
			activityKey: activityKey,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("iterate workflow state rows: %w", err)
	}

	next := ""
	if len(scanned) > limit {
		scanned = scanned[:limit]
		last := scanned[len(scanned)-1]
		next = encodeWorkflowCursor(last.row, last.sortKey, last.activityKey)
	}
	out := make([]WorkflowStateListRow, 0, len(scanned))
	for _, row := range scanned {
		out = append(out, row.row)
	}
	return out, next, nil
}
