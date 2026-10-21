package db

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

var ErrWorklistNotFound = errors.New("worklist not found")

// WorklistItemNotFoundError reports the first put item that does not name a
// visible synced pull request or issue. Index is the item's input position.
type WorklistItemNotFoundError struct {
	Index    int
	ItemType string
}

func (e *WorklistItemNotFoundError) Error() string {
	return fmt.Sprintf("worklist item %d: %s not found", e.Index, e.ItemType)
}

// WorklistItemNotOnListError reports the first item of a mark request that
// is not on the worklist. Index is the item's input position.
type WorklistItemNotOnListError struct {
	Index int
}

func (e *WorklistItemNotOnListError) Error() string {
	return fmt.Sprintf("worklist item %d is not on the list", e.Index)
}

type WorklistFreshness string

const (
	WorklistFresh   WorklistFreshness = "fresh"
	WorklistChanged WorklistFreshness = "changed"
	WorklistGone    WorklistFreshness = "gone"
)

type Worklist struct {
	ID          int64
	Title       string
	Recipe      string
	CreatedBy   string
	WorkspaceID string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	DismissedAt *time.Time
}

type WorklistSummary struct {
	Worklist
	ItemCount    int
	ChangedCount int
	GoneCount    int
	HandledCount int
	// RemainingCount counts items that are neither handled nor gone. Handled
	// and gone overlap, so it is not derivable from the other counts.
	RemainingCount int
}

type WorklistItemState struct {
	State          string
	HeadSHA        string
	CommentCount   int
	CIStatus       string
	ReviewDecision string
	LastActivityAt *time.Time
}

// WorklistChanges names what changed since the item was written. Paired
// fields are set only when the values differ. Changes are informational and
// never certify mergeability.
type WorklistChanges struct {
	StateFrom   string
	StateTo     string
	NewCommits  bool
	NewComments int
	CIFrom      string
	CITo        string
	ReviewFrom  string
	ReviewTo    string
}

type WorklistItem struct {
	Position   int
	RepoID     int64
	ItemType   string
	ItemNumber int
	Score      *int
	Section    string
	Note       string
	Caveat     string
	WrittenAt  time.Time
	// Title and URL come from the synced row even when it is no longer
	// visible; both are empty when the row is gone.
	Title    string
	URL      string
	Snapshot WorklistItemState
	// Live is nil when the item is no longer a visible synced row.
	Live      *WorklistItemState
	Freshness WorklistFreshness
	Changes   WorklistChanges
	// HandledAt is when the maintainer marked the item handled, or nil.
	HandledAt *time.Time
}

type WorklistDetail struct {
	Worklist
	Items []WorklistItem
}

// WorklistItemKey identifies an item within a worklist.
type WorklistItemKey struct {
	RepoID     int64
	ItemType   string
	ItemNumber int
}

type WorklistItemParams struct {
	RepoID     int64
	ItemType   string
	ItemNumber int
	Score      *int
	Section    string
	Note       string
	Caveat     string
}

// PutWorklistParams creates a worklist when ID is zero and replaces the
// named worklist otherwise. LinkKey identifies the link a new list came
// from; creating with a key that is already stored writes nothing and
// returns that list instead.
type PutWorklistParams struct {
	ID          int64
	Title       string
	Recipe      string
	CreatedBy   string
	WorkspaceID string
	LinkKey     string
	Items       []WorklistItemParams
	Now         time.Time
}

type PutWorklistResult struct {
	ID        int64
	ItemCount int
	Created   bool
}

// PutWorklist creates or replaces a worklist in one transaction. Replacing
// resets every item's recorded state and written time and clears a dismissal,
// so an agent rewrite brings a dismissed list back. Creating from a link that
// already made a list restores that list and leaves its items alone. Handled marks carry over
// to items that stay on the list. Every item must name a visible synced pull
// request or issue; otherwise nothing is written.
func (d *DB) PutWorklist(ctx context.Context, p PutWorklistParams) (PutWorklistResult, error) {
	now := canonicalUTCTime(p.Now)
	if now.IsZero() {
		return PutWorklistResult{}, errors.New("put worklist: time is required")
	}
	for i, item := range p.Items {
		if err := validateItemWorkflowType(item.ItemType); err != nil {
			return PutWorklistResult{}, fmt.Errorf("put worklist item %d: %w", i, err)
		}
	}
	result := PutWorklistResult{ID: p.ID, ItemCount: len(p.Items)}
	err := d.Tx(ctx, func(tx *sql.Tx) error {
		var handled map[WorklistItemKey]time.Time
		if p.ID == 0 && p.LinkKey != "" {
			err := tx.QueryRowContext(ctx, `
				UPDATE forge_worklists SET dismissed_at = NULL WHERE link_key = ?
				RETURNING id, (SELECT COUNT(*) FROM forge_worklist_items WHERE worklist_id = forge_worklists.id)`,
				p.LinkKey,
			).Scan(&result.ID, &result.ItemCount)
			if err == nil {
				return nil
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("find linked worklist: %w", err)
			}
		}
		if p.ID == 0 {
			if err := tx.QueryRowContext(ctx, `
				INSERT INTO forge_worklists
				    (title, recipe, created_by, workspace_id, created_at, updated_at, dismissed_at, link_key)
				VALUES (?, ?, ?, ?, ?, ?, NULL, NULLIF(?, ''))
				RETURNING id`,
				p.Title, p.Recipe, p.CreatedBy, p.WorkspaceID, now, now, p.LinkKey,
			).Scan(&result.ID); err != nil {
				return fmt.Errorf("insert worklist: %w", err)
			}
			result.Created = true
		} else {
			updated, err := tx.ExecContext(ctx, `
				UPDATE forge_worklists
				   SET title = ?, recipe = ?, created_by = ?, workspace_id = ?,
				       updated_at = ?, dismissed_at = NULL
				 WHERE id = ?`,
				p.Title, p.Recipe, p.CreatedBy, p.WorkspaceID, now, p.ID,
			)
			if err != nil {
				return fmt.Errorf("update worklist: %w", err)
			}
			if count, err := updated.RowsAffected(); err != nil {
				return fmt.Errorf("update worklist: %w", err)
			} else if count == 0 {
				return ErrWorklistNotFound
			}
			if handled, err = worklistHandledMarksTx(ctx, tx, p.ID); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx,
				`DELETE FROM forge_worklist_items WHERE worklist_id = ?`, p.ID,
			); err != nil {
				return fmt.Errorf("clear worklist items: %w", err)
			}
		}
		for i, item := range p.Items {
			var handledAt *time.Time
			if at, ok := handled[WorklistItemKey{
				RepoID: item.RepoID, ItemType: item.ItemType, ItemNumber: item.ItemNumber,
			}]; ok {
				handledAt = &at
			}
			if err := insertWorklistItemTx(ctx, tx, result.ID, i, item, now, handledAt); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return PutWorklistResult{}, err
	}
	return result, nil
}

func worklistHandledMarksTx(
	ctx context.Context, tx *sql.Tx, worklistID int64,
) (map[WorklistItemKey]time.Time, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT repo_id, item_type, item_number, handled_at
		  FROM forge_worklist_items
		 WHERE worklist_id = ? AND handled_at IS NOT NULL`, worklistID)
	if err != nil {
		return nil, fmt.Errorf("read worklist handled marks: %w", err)
	}
	defer rows.Close()
	marks := make(map[WorklistItemKey]time.Time)
	for rows.Next() {
		var (
			key       WorklistItemKey
			handledAt time.Time
		)
		if err := rows.Scan(&key.RepoID, &key.ItemType, &key.ItemNumber, &handledAt); err != nil {
			return nil, fmt.Errorf("scan worklist handled mark: %w", err)
		}
		marks[key] = handledAt.UTC()
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read worklist handled marks: %w", err)
	}
	return marks, nil
}

func insertWorklistItemTx(
	ctx context.Context, tx *sql.Tx, worklistID int64, position int,
	item WorklistItemParams, now time.Time, handledAt *time.Time,
) error {
	var source string
	switch item.ItemType {
	case ItemTypePR:
		source = `SELECT ?, ?, p.repo_id, 'pr', p.number, ?, ?, ?, ?, ?,
		       p.state, p.platform_head_sha, p.comment_count, p.ci_status,
		       p.review_decision, p.last_activity_at, ?
		  FROM forge_merge_requests p
		 WHERE p.repo_id = ? AND p.number = ?
		   AND NOT EXISTS (
		       SELECT 1 FROM forge_archive_items ai
		        WHERE ai.repo_id = p.repo_id AND ai.item_type = 'merge_request'
		          AND ai.item_number = p.number AND ai.lifecycle_state = 'removed_upstream')`
	default:
		source = `SELECT ?, ?, i.repo_id, 'issue', i.number, ?, ?, ?, ?, ?,
		       i.state, '', i.comment_count, '', '', i.last_activity_at, ?
		  FROM forge_issues i
		 WHERE i.repo_id = ? AND i.number = ?
		   AND NOT EXISTS (
		       SELECT 1 FROM forge_archive_items ai
		        WHERE ai.repo_id = i.repo_id AND ai.item_type = 'issue'
		          AND ai.item_number = i.number AND ai.lifecycle_state = 'removed_upstream')`
	}
	inserted, err := tx.ExecContext(ctx, `
		INSERT INTO forge_worklist_items
		    (worklist_id, position, repo_id, item_type, item_number,
		     score, section, note, caveat, written_at,
		     snap_state, snap_head_sha, snap_comment_count, snap_ci_status,
		     snap_review_decision, snap_last_activity_at, handled_at)
		`+source,
		worklistID, position, item.Score, item.Section, item.Note, item.Caveat, now,
		canonicalUTCTimePtr(handledAt), item.RepoID, item.ItemNumber,
	)
	if err != nil {
		return fmt.Errorf("insert worklist item %d: %w", position, err)
	}
	count, err := inserted.RowsAffected()
	if err != nil {
		return fmt.Errorf("insert worklist item %d: %w", position, err)
	}
	if count == 0 {
		return &WorklistItemNotFoundError{Index: position, ItemType: item.ItemType}
	}
	return nil
}

// SetWorklistDismissed dismisses or restores a worklist. Dismissing keeps
// the first dismissal time, so both directions are idempotent.
func (d *DB) SetWorklistDismissed(ctx context.Context, id int64, dismissed bool, now time.Time) error {
	var (
		result sql.Result
		err    error
	)
	if dismissed {
		result, err = d.execContext(ctx,
			`UPDATE forge_worklists SET dismissed_at = COALESCE(dismissed_at, ?) WHERE id = ?`,
			canonicalUTCTime(now), id,
		)
	} else {
		result, err = d.execContext(ctx,
			`UPDATE forge_worklists SET dismissed_at = NULL WHERE id = ?`, id,
		)
	}
	if err != nil {
		return fmt.Errorf("set worklist dismissed: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("set worklist dismissed: %w", err)
	}
	if count == 0 {
		return ErrWorklistNotFound
	}
	return nil
}

// SetWorklistItemsHandled marks or unmarks items on a worklist in one
// transaction. Marking keeps an existing mark's time, so both directions are
// idempotent. An item that is not on the list rejects the request and
// nothing changes.
func (d *DB) SetWorklistItemsHandled(
	ctx context.Context, worklistID int64, items []WorklistItemKey, handled bool, now time.Time,
) error {
	now = canonicalUTCTime(now)
	if handled && now.IsZero() {
		return errors.New("set worklist items handled: time is required")
	}
	return d.Tx(ctx, func(tx *sql.Tx) error {
		if err := requireWorklistTx(ctx, tx, worklistID); err != nil {
			return err
		}
		for i, item := range items {
			var (
				result sql.Result
				err    error
			)
			if handled {
				result, err = tx.ExecContext(ctx, `
					UPDATE forge_worklist_items SET handled_at = COALESCE(handled_at, ?)
					 WHERE worklist_id = ? AND repo_id = ? AND item_type = ? AND item_number = ?`,
					now, worklistID, item.RepoID, item.ItemType, item.ItemNumber)
			} else {
				result, err = tx.ExecContext(ctx, `
					UPDATE forge_worklist_items SET handled_at = NULL
					 WHERE worklist_id = ? AND repo_id = ? AND item_type = ? AND item_number = ?`,
					worklistID, item.RepoID, item.ItemType, item.ItemNumber)
			}
			if err != nil {
				return fmt.Errorf("set worklist item %d handled: %w", i, err)
			}
			count, err := result.RowsAffected()
			if err != nil {
				return fmt.Errorf("set worklist item %d handled: %w", i, err)
			}
			if count == 0 {
				return &WorklistItemNotOnListError{Index: i}
			}
		}
		return nil
	})
}

// ClearWorklistMarks unmarks every handled item on a worklist and returns
// how many it unmarked.
func (d *DB) ClearWorklistMarks(ctx context.Context, worklistID int64) (int, error) {
	var cleared int64
	err := d.Tx(ctx, func(tx *sql.Tx) error {
		if err := requireWorklistTx(ctx, tx, worklistID); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `
			UPDATE forge_worklist_items SET handled_at = NULL
			 WHERE worklist_id = ? AND handled_at IS NOT NULL`, worklistID)
		if err != nil {
			return fmt.Errorf("clear worklist marks: %w", err)
		}
		cleared, err = result.RowsAffected()
		if err != nil {
			return fmt.Errorf("clear worklist marks: %w", err)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return int(cleared), nil
}

func requireWorklistTx(ctx context.Context, tx *sql.Tx, worklistID int64) error {
	var exists bool
	if err := tx.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM forge_worklists WHERE id = ?)`, worklistID,
	).Scan(&exists); err != nil {
		return fmt.Errorf("read worklist: %w", err)
	}
	if !exists {
		return ErrWorklistNotFound
	}
	return nil
}

// GetWorklist returns a worklist with its items in position order and each
// item's freshness computed against the live synced row. trackedRepoIDs
// names the repositories the caller tracks; items elsewhere are gone. nil
// tracks every active repository.
func (d *DB) GetWorklist(ctx context.Context, id int64, trackedRepoIDs []int64) (*WorklistDetail, error) {
	lists, err := d.listWorklists(ctx, "WHERE id = ?", id)
	if err != nil {
		return nil, err
	}
	if len(lists) == 0 {
		return nil, ErrWorklistNotFound
	}
	items, err := d.listWorklistItems(ctx, []int64{id}, trackedRepoIDs)
	if err != nil {
		return nil, err
	}
	return &WorklistDetail{Worklist: lists[0], Items: items[id]}, nil
}

// GetWorklistSummary returns one worklist's summary, dismissed or not.
// trackedRepoIDs is as for GetWorklist.
func (d *DB) GetWorklistSummary(ctx context.Context, id int64, trackedRepoIDs []int64) (*WorklistSummary, error) {
	summaries, err := d.worklistSummaries(ctx, trackedRepoIDs, "WHERE id = ?", id)
	if err != nil {
		return nil, err
	}
	if len(summaries) == 0 {
		return nil, ErrWorklistNotFound
	}
	return &summaries[0], nil
}

// ListWorklists returns worklist summaries, most recently updated first.
// Dismissed worklists are excluded unless includeDismissed is set.
// trackedRepoIDs is as for GetWorklist.
func (d *DB) ListWorklists(
	ctx context.Context, includeDismissed bool, trackedRepoIDs []int64,
) ([]WorklistSummary, error) {
	where := "WHERE dismissed_at IS NULL"
	if includeDismissed {
		where = ""
	}
	return d.worklistSummaries(ctx, trackedRepoIDs, where)
}

func (d *DB) worklistSummaries(
	ctx context.Context, trackedRepoIDs []int64, where string, args ...any,
) ([]WorklistSummary, error) {
	lists, err := d.listWorklists(ctx, where, args...)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(lists))
	for _, list := range lists {
		ids = append(ids, list.ID)
	}
	items, err := d.listWorklistItems(ctx, ids, trackedRepoIDs)
	if err != nil {
		return nil, err
	}
	summaries := make([]WorklistSummary, 0, len(lists))
	for _, list := range lists {
		summaries = append(summaries, SummarizeWorklist(WorklistDetail{Worklist: list, Items: items[list.ID]}))
	}
	return summaries, nil
}

// SummarizeWorklist counts a worklist's items by freshness and handled mark.
// An item that is both handled and gone counts once toward each of those
// counts and not toward RemainingCount.
func SummarizeWorklist(detail WorklistDetail) WorklistSummary {
	summary := WorklistSummary{Worklist: detail.Worklist, ItemCount: len(detail.Items)}
	for _, item := range detail.Items {
		switch item.Freshness {
		case WorklistFresh:
		case WorklistChanged:
			summary.ChangedCount++
		case WorklistGone:
			summary.GoneCount++
		}
		if item.HandledAt != nil {
			summary.HandledCount++
		}
		if item.HandledAt == nil && item.Freshness != WorklistGone {
			summary.RemainingCount++
		}
	}
	return summary
}

func (d *DB) listWorklists(ctx context.Context, where string, args ...any) ([]Worklist, error) {
	rows, err := d.roQueryContext(ctx, `
		SELECT id, title, recipe, created_by, workspace_id, created_at, updated_at, dismissed_at
		  FROM forge_worklists `+where+`
		 ORDER BY updated_at DESC, id DESC`, args...)
	if err != nil {
		return nil, fmt.Errorf("list worklists: %w", err)
	}
	defer rows.Close()
	lists := make([]Worklist, 0)
	for rows.Next() {
		var list Worklist
		if err := rows.Scan(
			&list.ID, &list.Title, &list.Recipe, &list.CreatedBy, &list.WorkspaceID,
			&list.CreatedAt, &list.UpdatedAt, &list.DismissedAt,
		); err != nil {
			return nil, fmt.Errorf("scan worklist: %w", err)
		}
		list.CreatedAt = list.CreatedAt.UTC()
		list.UpdatedAt = list.UpdatedAt.UTC()
		list.DismissedAt = canonicalUTCTimePtr(list.DismissedAt)
		lists = append(lists, list)
	}
	return lists, rows.Err()
}

func (d *DB) listWorklistItems(
	ctx context.Context, worklistIDs, trackedRepoIDs []int64,
) (map[int64][]WorklistItem, error) {
	out := make(map[int64][]WorklistItem, len(worklistIDs))
	if len(worklistIDs) == 0 {
		return out, nil
	}
	var tracked map[int64]bool
	if trackedRepoIDs != nil {
		tracked = make(map[int64]bool, len(trackedRepoIDs))
		for _, id := range trackedRepoIDs {
			tracked[id] = true
		}
	}
	encoded := make([]string, 0, len(worklistIDs))
	for _, id := range worklistIDs {
		encoded = append(encoded, strconv.FormatInt(id, 10))
	}
	rows, err := d.roQueryContext(ctx, `
		SELECT wi.worklist_id, wi.position, wi.repo_id, wi.item_type, wi.item_number,
		       wi.score, wi.section, wi.note, wi.caveat, wi.written_at,
		       wi.snap_state, wi.snap_head_sha, wi.snap_comment_count, wi.snap_ci_status,
		       wi.snap_review_decision, wi.snap_last_activity_at, wi.handled_at,
		       COALESCE(p.title, i.title, ''), COALESCE(p.url, i.url, ''),
		       (r.lifecycle_state = 'active'
		        AND COALESCE(p.id, i.id) IS NOT NULL
		        AND NOT EXISTS (
		            SELECT 1 FROM forge_archive_items ai
		             WHERE ai.repo_id = wi.repo_id
		               AND ai.item_type = CASE wi.item_type WHEN 'pr' THEN 'merge_request' ELSE 'issue' END
		               AND ai.item_number = wi.item_number
		               AND ai.lifecycle_state = 'removed_upstream')) AS visible,
		       COALESCE(p.state, i.state, ''), COALESCE(p.platform_head_sha, ''),
		       COALESCE(p.comment_count, i.comment_count, 0),
		       COALESCE(p.ci_status, ''), COALESCE(p.review_decision, ''),
		       p.last_activity_at, i.last_activity_at
		  FROM forge_worklist_items wi
		  JOIN forge_repos r ON r.id = wi.repo_id
		  LEFT JOIN forge_merge_requests p
		    ON wi.item_type = 'pr' AND p.repo_id = wi.repo_id AND p.number = wi.item_number
		  LEFT JOIN forge_issues i
		    ON wi.item_type = 'issue' AND i.repo_id = wi.repo_id AND i.number = wi.item_number
		 WHERE wi.worklist_id IN (SELECT value FROM json_each(?))
		 ORDER BY wi.worklist_id, wi.position`,
		"["+strings.Join(encoded, ",")+"]",
	)
	if err != nil {
		return nil, fmt.Errorf("list worklist items: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		// Datetime columns stay bare: modernc decodes DATETIME text only for
		// columns with a declared type, which COALESCE would drop.
		var (
			worklistID      int64
			item            WorklistItem
			score           sql.NullInt64
			visible         bool
			live            WorklistItemState
			pullActivityAt  *time.Time
			issueActivityAt *time.Time
		)
		if err := rows.Scan(
			&worklistID, &item.Position, &item.RepoID, &item.ItemType, &item.ItemNumber,
			&score, &item.Section, &item.Note, &item.Caveat, &item.WrittenAt,
			&item.Snapshot.State, &item.Snapshot.HeadSHA, &item.Snapshot.CommentCount,
			&item.Snapshot.CIStatus, &item.Snapshot.ReviewDecision, &item.Snapshot.LastActivityAt,
			&item.HandledAt, &item.Title, &item.URL, &visible,
			&live.State, &live.HeadSHA, &live.CommentCount, &live.CIStatus,
			&live.ReviewDecision, &pullActivityAt, &issueActivityAt,
		); err != nil {
			return nil, fmt.Errorf("scan worklist item: %w", err)
		}
		if score.Valid {
			item.Score = new(int(score.Int64))
		}
		item.WrittenAt = item.WrittenAt.UTC()
		item.Snapshot.LastActivityAt = canonicalUTCTimePtr(item.Snapshot.LastActivityAt)
		item.HandledAt = canonicalUTCTimePtr(item.HandledAt)
		if visible && (tracked == nil || tracked[item.RepoID]) {
			live.LastActivityAt = canonicalUTCTimePtr(cmp.Or(pullActivityAt, issueActivityAt))
			item.Live = &live
		}
		item.Freshness, item.Changes = CompareWorklistItem(item.ItemType, item.Snapshot, item.Live)
		out[worklistID] = append(out[worklistID], item)
	}
	return out, rows.Err()
}

func worklistStateTerminal(state string) bool {
	return slices.Contains([]string{"closed", "merged"}, state)
}

// CompareWorklistItem derives an item's freshness and changes from its
// recorded state and its live synced row, or nil when the row is no longer
// visible. An item is gone once it is no longer visible or an open item was
// merged or closed; it is changed by new commits, new comments, a CI or
// review change, or a reopen.
func CompareWorklistItem(
	itemType string, snapshot WorklistItemState, live *WorklistItemState,
) (WorklistFreshness, WorklistChanges) {
	if live == nil {
		return WorklistGone, WorklistChanges{}
	}
	var changes WorklistChanges
	stateChanged := snapshot.State != live.State
	if stateChanged {
		changes.StateFrom, changes.StateTo = snapshot.State, live.State
	}
	changes.NewCommits = itemType == ItemTypePR &&
		snapshot.HeadSHA != "" && live.HeadSHA != "" && snapshot.HeadSHA != live.HeadSHA
	changes.NewComments = max(live.CommentCount-snapshot.CommentCount, 0)
	ciChanged := snapshot.CIStatus != live.CIStatus
	if ciChanged {
		changes.CIFrom, changes.CITo = snapshot.CIStatus, live.CIStatus
	}
	reviewChanged := snapshot.ReviewDecision != live.ReviewDecision
	if reviewChanged {
		changes.ReviewFrom, changes.ReviewTo = snapshot.ReviewDecision, live.ReviewDecision
	}
	if !worklistStateTerminal(snapshot.State) && worklistStateTerminal(live.State) {
		return WorklistGone, changes
	}
	if stateChanged || changes.NewCommits || changes.NewComments > 0 || ciChanged || reviewChanged {
		return WorklistChanged, changes
	}
	return WorklistFresh, changes
}
