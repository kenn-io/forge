package db

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// PlatformIdentity captures a project's optional VCS-platform identity. It is
// derived from the linked middleman_repos row at read time; middleman_projects
// itself stores only the FK in repo_id. A project may be registered without a
// linked repo (a local-only directory with no parseable remote), in which case
// PlatformIdentity is nil.
type PlatformIdentity struct {
	Platform string `json:"platform"`
	Host     string `json:"platform_host"`
	Owner    string `json:"owner"`
	Name     string `json:"name"`
}

// Project is the registry record for a local repository checkout middleman
// knows about. Identity (host/owner/name), when present, lives in
// middleman_repos and is joined in via repo_id.
type Project struct {
	ID               string
	DisplayName      string
	LocalPath        string
	PlatformIdentity *PlatformIdentity
	DefaultBranch    string
	RepositoryKind   string
	IsStale          bool
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// ProjectWorktree is the registry record for a worktree of a Project. The
// caller performs the filesystem mutation (`git worktree add`); middleman only
// persists the metadata.
type ProjectWorktree struct {
	ID                 string
	ProjectID          string
	Branch             string
	Path               string
	IsStale            bool
	IsHidden           bool
	SessionBackend     string
	LinkedIssueNumbers []int
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// ProjectWorktreeTmuxSession records a tmux session owned by runtime launched
// from a registered project worktree. SessionKey is the runtime-assigned
// session key (random per session); routing and cleanup key off it.
type ProjectWorktreeTmuxSession struct {
	WorktreeID   string
	SessionKey   string
	SessionName  string
	TargetKey    string
	Label        string
	CreatedAt    time.Time
	WorktreePath string
	ProjectID    string
}

// ErrProjectNotFound is returned by GetProject* when no record matches.
var ErrProjectNotFound = errors.New("project not found")

// ErrProjectPathTaken is returned by CreateProject when local_path is already
// registered.
var ErrProjectPathTaken = errors.New("project local_path already registered")

// ErrWorktreePathTaken is returned by CreateProjectWorktree when path is
// already registered for any project.
var ErrWorktreePathTaken = errors.New("worktree path already registered")

// CreateProjectInput collects the fields required to register a new project.
// RepoID links the project to a middleman_repos row that owns the platform
// identity; pass an unset NullInt64 to register a local-only project.
type CreateProjectInput struct {
	DisplayName   string
	LocalPath     string
	RepoID        sql.NullInt64
	DefaultBranch string
}

// CreateProject persists a new project. The ID is generated server-side.
func (d *DB) CreateProject(ctx context.Context, in CreateProjectInput) (*Project, error) {
	displayName := strings.TrimSpace(in.DisplayName)
	if displayName == "" {
		return nil, fmt.Errorf("display_name is required")
	}
	localPath := strings.TrimSpace(in.LocalPath)
	if localPath == "" {
		return nil, fmt.Errorf("local_path is required")
	}

	id, err := newProjectID()
	if err != nil {
		return nil, err
	}

	defaultBranch := strings.TrimSpace(in.DefaultBranch)

	_, err = d.rw.ExecContext(ctx,
		`INSERT INTO middleman_projects (
		    id, display_name, local_path, repo_id, default_branch
		 ) VALUES (?, ?, ?, ?, ?)`,
		id, displayName, localPath, in.RepoID, defaultBranch,
	)
	if err != nil {
		if isUniqueConstraintErr(err, "middleman_projects.local_path") {
			return nil, ErrProjectPathTaken
		}
		return nil, fmt.Errorf("insert project: %w", err)
	}

	return d.GetProjectByID(ctx, id)
}

const projectSelectColumns = `p.id, p.display_name, p.local_path,
        p.default_branch, p.repository_kind, p.is_stale,
        p.created_at, p.updated_at,
        r.platform, r.platform_host, r.owner, r.name`

const projectFromJoin = `FROM middleman_projects p
        LEFT JOIN middleman_repos r ON r.id = p.repo_id`

// GetProjectByID returns one project by its server-assigned id, joining the
// linked middleman_repos row to populate PlatformIdentity when present.
func (d *DB) GetProjectByID(ctx context.Context, id string) (*Project, error) {
	row := d.ro.QueryRowContext(ctx,
		`SELECT `+projectSelectColumns+` `+projectFromJoin+`
		 WHERE p.id = ?`,
		id,
	)
	return scanProject(row)
}

// GetProjectByLocalPath returns the project registered at the given absolute
// path, or ErrProjectNotFound if no record matches.
func (d *DB) GetProjectByLocalPath(ctx context.Context, localPath string) (*Project, error) {
	row := d.ro.QueryRowContext(ctx,
		`SELECT `+projectSelectColumns+` `+projectFromJoin+`
		 WHERE p.local_path = ?`,
		localPath,
	)
	return scanProject(row)
}

// ListProjects returns all registered projects ordered by display_name.
func (d *DB) ListProjects(ctx context.Context) ([]Project, error) {
	rows, err := d.ro.QueryContext(ctx,
		`SELECT `+projectSelectColumns+` `+projectFromJoin+`
		 ORDER BY p.display_name COLLATE NOCASE, p.id`,
	)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	defer rows.Close()

	var projects []Project
	for rows.Next() {
		project, err := scanProjectRow(rows)
		if err != nil {
			return nil, err
		}
		projects = append(projects, *project)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate projects: %w", err)
	}
	return projects, nil
}

// DeleteProject removes a registered project and, through ON DELETE CASCADE, its
// worktrees and their stored runtime tmux sessions. Returns ErrProjectNotFound
// when no project matches the id so the caller can map the result to a 404
// rather than a misleading success.
func (d *DB) DeleteProject(ctx context.Context, id string) error {
	res, err := d.rw.ExecContext(ctx,
		`DELETE FROM middleman_projects WHERE id = ?`, id,
	)
	if err != nil {
		return fmt.Errorf("delete project: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete project rows affected: %w", err)
	}
	if affected == 0 {
		return ErrProjectNotFound
	}
	return nil
}

// DeleteProjectWorktree removes a registered worktree record. The caller must
// have already run `git worktree remove`; middleman only drops its metadata.
// The delete is scoped to the owning project so a worktree id under a different
// project is treated as not found. Returns ErrProjectNotFound when no matching
// worktree exists, so a host write-through can surface a 404 instead of a
// misleading 204.
func (d *DB) DeleteProjectWorktree(ctx context.Context, projectID, worktreeID string) error {
	res, err := d.rw.ExecContext(ctx,
		`DELETE FROM middleman_project_worktrees WHERE id = ? AND project_id = ?`,
		worktreeID, projectID,
	)
	if err != nil {
		return fmt.Errorf("delete worktree: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete worktree rows affected: %w", err)
	}
	if affected == 0 {
		return ErrProjectNotFound
	}
	return nil
}

// CreateProjectWorktreeInput collects fields for registering a worktree the
// caller has already created on disk.
type CreateProjectWorktreeInput struct {
	ProjectID string
	Branch    string
	Path      string
}

// CreateProjectWorktree persists a worktree record. The caller must have
// already run `git worktree add`; middleman only records metadata.
func (d *DB) CreateProjectWorktree(ctx context.Context, in CreateProjectWorktreeInput) (*ProjectWorktree, error) {
	projectID := strings.TrimSpace(in.ProjectID)
	if projectID == "" {
		return nil, fmt.Errorf("project_id is required")
	}
	branch := strings.TrimSpace(in.Branch)
	if branch == "" {
		return nil, fmt.Errorf("branch is required")
	}
	path := strings.TrimSpace(in.Path)
	if path == "" {
		return nil, fmt.Errorf("path is required")
	}

	if _, err := d.GetProjectByID(ctx, projectID); err != nil {
		return nil, err
	}

	id, err := newWorktreeID()
	if err != nil {
		return nil, err
	}

	_, err = d.rw.ExecContext(ctx,
		`INSERT INTO middleman_project_worktrees (
		    id, project_id, branch, path
		 ) VALUES (?, ?, ?, ?)`,
		id, projectID, branch, path,
	)
	if err != nil {
		if isUniqueConstraintErr(err, "middleman_project_worktrees.path") {
			return d.adoptProjectWorktreeByPath(ctx, projectID, branch, path)
		}
		return nil, fmt.Errorf("insert project worktree: %w", err)
	}

	return d.GetProjectWorktreeByID(ctx, id)
}

// adoptProjectWorktreeByPath converges an explicit registration with a worktree
// row already present at path — typically one the background discovery pass
// created. The row keeps its id (and any linked tmux sessions); its branch is
// refreshed and its stale flag cleared. A path owned by a different project is a
// genuine conflict.
func (d *DB) adoptProjectWorktreeByPath(
	ctx context.Context, projectID, branch, path string,
) (*ProjectWorktree, error) {
	var id, owner string
	if err := d.ro.QueryRowContext(ctx,
		`SELECT id, project_id FROM middleman_project_worktrees WHERE path = ?`,
		path,
	).Scan(&id, &owner); err != nil {
		return nil, fmt.Errorf("lookup worktree by path: %w", err)
	}
	if owner != projectID {
		return nil, ErrWorktreePathTaken
	}
	if _, err := d.rw.ExecContext(ctx,
		`UPDATE middleman_project_worktrees
		 SET branch = ?, is_stale = 0, updated_at = (datetime('now'))
		 WHERE id = ?`,
		branch, id,
	); err != nil {
		return nil, fmt.Errorf("adopt worktree at %s: %w", path, err)
	}
	return d.GetProjectWorktreeByID(ctx, id)
}

// GetProjectWorktreeByID returns one worktree by id.
func (d *DB) GetProjectWorktreeByID(ctx context.Context, id string) (*ProjectWorktree, error) {
	row := d.ro.QueryRowContext(ctx,
		`SELECT id, project_id, branch, path, is_stale, is_hidden, session_backend, linked_issue_numbers, created_at, updated_at
		 FROM middleman_project_worktrees WHERE id = ?`,
		id,
	)
	return scanProjectWorktree(row)
}

// GetProjectWorktreeByPath returns one worktree by its on-disk path. Paths are
// unique across the registry, so the owning project need not be known — the
// stale-removal route resolves a worktree from its fleet scoped key this way.
func (d *DB) GetProjectWorktreeByPath(ctx context.Context, path string) (*ProjectWorktree, error) {
	row := d.ro.QueryRowContext(ctx,
		`SELECT id, project_id, branch, path, is_stale, is_hidden, session_backend, linked_issue_numbers, created_at, updated_at
		 FROM middleman_project_worktrees WHERE path = ?`,
		path,
	)
	return scanProjectWorktree(row)
}

// ListProjectWorktrees returns the worktrees for a project ordered by
// created_at.
func (d *DB) ListProjectWorktrees(ctx context.Context, projectID string) ([]ProjectWorktree, error) {
	if _, err := d.GetProjectByID(ctx, projectID); err != nil {
		return nil, err
	}
	rows, err := d.ro.QueryContext(ctx,
		`SELECT id, project_id, branch, path, is_stale, is_hidden, session_backend, linked_issue_numbers, created_at, updated_at
		 FROM middleman_project_worktrees
		 WHERE project_id = ?
		 ORDER BY created_at, id`,
		projectID,
	)
	if err != nil {
		return nil, fmt.Errorf("list project worktrees: %w", err)
	}
	defer rows.Close()

	var worktrees []ProjectWorktree
	for rows.Next() {
		wt, err := scanProjectWorktreeRow(rows)
		if err != nil {
			return nil, err
		}
		worktrees = append(worktrees, *wt)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate project worktrees: %w", err)
	}
	return worktrees, nil
}

// SetProjectWorktreeHidden toggles a worktree's hidden flag, returning the
// updated row. The update is scoped to the owning project so a worktree id that
// belongs to a different project is treated as not found. Discovery
// reconciliation never touches is_hidden, so the flag persists across discovery
// passes. Returns ErrProjectNotFound when no matching worktree exists.
func (d *DB) SetProjectWorktreeHidden(
	ctx context.Context, projectID, worktreeID string, hidden bool, now time.Time,
) (*ProjectWorktree, error) {
	ts := canonicalUTCTime(now)
	if ts.IsZero() {
		ts = time.Now().UTC()
	}
	hiddenInt := int64(0)
	if hidden {
		hiddenInt = 1
	}
	res, err := d.rw.ExecContext(ctx, `
		UPDATE middleman_project_worktrees
		SET is_hidden = ?, updated_at = ?
		WHERE id = ? AND project_id = ?`,
		hiddenInt, ts, worktreeID, projectID,
	)
	if err != nil {
		return nil, fmt.Errorf("set worktree hidden: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("set worktree hidden rows affected: %w", err)
	}
	if affected == 0 {
		return nil, ErrProjectNotFound
	}
	return d.GetProjectWorktreeByID(ctx, worktreeID)
}

// SetProjectWorktreeSessionBackend records a worktree's session-backend
// override, returning the updated row. An empty backend clears the override so
// the snapshot producer's empty->localPTY default applies again. The update is
// scoped to the owning project so a worktree id under a different project is
// treated as not found. Discovery reconciliation never touches this column, so
// the override persists across discovery passes. Returns ErrProjectNotFound
// when no matching worktree exists.
func (d *DB) SetProjectWorktreeSessionBackend(
	ctx context.Context, projectID, worktreeID, backend string, now time.Time,
) (*ProjectWorktree, error) {
	ts := canonicalUTCTime(now)
	if ts.IsZero() {
		ts = time.Now().UTC()
	}
	res, err := d.rw.ExecContext(ctx, `
		UPDATE middleman_project_worktrees
		SET session_backend = ?, updated_at = ?
		WHERE id = ? AND project_id = ?`,
		strings.TrimSpace(backend), ts, worktreeID, projectID,
	)
	if err != nil {
		return nil, fmt.Errorf("set worktree session backend: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("set worktree session backend rows affected: %w", err)
	}
	if affected == 0 {
		return nil, ErrProjectNotFound
	}
	return d.GetProjectWorktreeByID(ctx, worktreeID)
}

// DiscoveredWorktree is one worktree found on disk during an inventory
// discovery pass. The project root checkout is included like any linked
// worktree, so the primary has a registry row of its own.
type DiscoveredWorktree struct {
	Path   string
	Branch string
}

// ProjectInventory is the result of a successful on-disk discovery pass for a
// registered project: its live repository kind and default branch plus every
// worktree reported by `git worktree list` (root checkout included).
type ProjectInventory struct {
	RepositoryKind string
	DefaultBranch  string
	Worktrees      []DiscoveredWorktree
}

// ReconcileProjectInventory folds a successful discovery pass into the registry
// in one transaction. It clears the project's stale flag and refreshes its
// repository kind (and default branch, when discovery resolved one), upserts
// each discovered worktree by its stable path so an existing row keeps its id
// and tmux-session links, and marks any previously known worktree absent from
// this pass as stale rather than deleting it. A path that reappears clears its
// stale flag through the upsert. A path already owned by a different project is
// left untouched so one project's discovery pass can never steal another
// project's worktree row (and its tmux-session links) on a path collision.
func (d *DB) ReconcileProjectInventory(
	ctx context.Context,
	projectID string,
	inv ProjectInventory,
	now time.Time,
) error {
	ts := canonicalUTCTime(now)
	if ts.IsZero() {
		ts = time.Now().UTC()
	}
	repoKind := strings.TrimSpace(inv.RepositoryKind)
	if repoKind == "" {
		repoKind = "standard"
	}
	defaultBranch := strings.TrimSpace(inv.DefaultBranch)

	return d.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			UPDATE middleman_projects
			SET repository_kind = ?,
			    default_branch = CASE WHEN ? != '' THEN ? ELSE default_branch END,
			    is_stale = 0,
			    updated_at = ?
			WHERE id = ?`,
			repoKind, defaultBranch, defaultBranch, ts, projectID,
		); err != nil {
			return fmt.Errorf("update project inventory: %w", err)
		}

		discoveredPaths := make([]string, 0, len(inv.Worktrees))
		for _, wt := range inv.Worktrees {
			path := strings.TrimSpace(wt.Path)
			if path == "" {
				continue
			}
			discoveredPaths = append(discoveredPaths, path)
			id, err := newWorktreeID()
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO middleman_project_worktrees
				    (id, project_id, branch, path, is_stale, updated_at)
				VALUES (?, ?, ?, ?, 0, ?)
				ON CONFLICT(path) DO UPDATE SET
				    branch     = excluded.branch,
				    is_stale   = 0,
				    updated_at = excluded.updated_at
				WHERE project_id = excluded.project_id`,
				id, projectID, strings.TrimSpace(wt.Branch), path, ts,
			); err != nil {
				return fmt.Errorf("upsert discovered worktree: %w", err)
			}
		}

		markArgs := []any{ts, projectID}
		markQuery := `
			UPDATE middleman_project_worktrees
			SET is_stale = 1, updated_at = ?
			WHERE project_id = ? AND is_stale = 0`
		if len(discoveredPaths) > 0 {
			markQuery += " AND path NOT IN (" +
				strings.TrimSuffix(strings.Repeat("?,", len(discoveredPaths)), ",") + ")"
			for _, path := range discoveredPaths {
				markArgs = append(markArgs, path)
			}
		}
		if _, err := tx.ExecContext(ctx, markQuery, markArgs...); err != nil {
			return fmt.Errorf("mark missing worktrees stale: %w", err)
		}
		return nil
	})
}

// MarkProjectStale flags a project whose on-disk discovery failed (for example,
// the checkout was moved or deleted) so the fleet snapshot can surface it.
// Worktree rows keep their last-known state and links.
func (d *DB) MarkProjectStale(
	ctx context.Context,
	projectID string,
	now time.Time,
) error {
	ts := canonicalUTCTime(now)
	if ts.IsZero() {
		ts = time.Now().UTC()
	}
	if _, err := d.rw.ExecContext(ctx, `
		UPDATE middleman_projects
		SET is_stale = 1, updated_at = ?
		WHERE id = ?`, ts, projectID,
	); err != nil {
		return fmt.Errorf("mark project stale: %w", err)
	}
	return nil
}

// UpsertProjectWorktreeTmuxSession records a tmux-backed runtime launch for a
// registered project worktree, keyed by the runtime session key. Restoring an
// existing session key refreshes its target and tmux session name.
func (d *DB) UpsertProjectWorktreeTmuxSession(
	ctx context.Context,
	session *ProjectWorktreeTmuxSession,
) error {
	createdAt := canonicalUTCTime(session.CreatedAt)
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	_, err := d.rw.ExecContext(ctx, `
		INSERT INTO middleman_project_worktree_runtime_sessions
		    (worktree_id, session_key, target_key, label, runtime_backend,
		     backend_session_key,
		     created_at)
		VALUES (?, ?, ?, ?, 'tmux', ?, ?)
		ON CONFLICT(worktree_id, session_key) DO UPDATE SET
		    target_key = excluded.target_key,
		    label = excluded.label,
		    runtime_backend = excluded.runtime_backend,
		    backend_session_key = excluded.backend_session_key,
		    created_at = excluded.created_at`,
		session.WorktreeID, session.SessionKey, session.TargetKey,
		session.Label, session.SessionName, createdAt,
	)
	if err != nil {
		return fmt.Errorf("upsert project worktree tmux session: %w", err)
	}
	return nil
}

// ListProjectWorktreeTmuxSessions returns stored runtime tmux sessions for a
// registered project worktree ordered by target key and creation time.
func (d *DB) ListProjectWorktreeTmuxSessions(
	ctx context.Context,
	worktreeID string,
) ([]ProjectWorktreeTmuxSession, error) {
	rows, err := d.ro.QueryContext(ctx, `
		SELECT s.worktree_id, s.session_key, COALESCE(s.backend_session_key, ''),
		       s.target_key,
		       s.label, s.created_at, w.path, w.project_id
		FROM middleman_project_worktree_runtime_sessions s
		JOIN middleman_project_worktrees w ON w.id = s.worktree_id
		WHERE s.worktree_id = ?
		  AND s.runtime_backend = 'tmux'
		ORDER BY s.created_at, s.session_key`, worktreeID,
	)
	if err != nil {
		return nil, fmt.Errorf("list project worktree tmux sessions: %w", err)
	}
	defer rows.Close()

	var out []ProjectWorktreeTmuxSession
	for rows.Next() {
		session, err := scanProjectWorktreeTmuxSessionRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *session)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate project worktree tmux sessions: %w", err)
	}
	return out, nil
}

// ListAllProjectWorktreeTmuxSessions returns every stored registered-worktree
// runtime tmux session for fleet snapshot construction.
func (d *DB) ListAllProjectWorktreeTmuxSessions(
	ctx context.Context,
) ([]ProjectWorktreeTmuxSession, error) {
	rows, err := d.ro.QueryContext(ctx, `
		SELECT s.worktree_id, s.session_key, COALESCE(s.backend_session_key, ''),
		       s.target_key,
		       s.label, s.created_at, w.path, w.project_id
		FROM middleman_project_worktree_runtime_sessions s
		JOIN middleman_project_worktrees w ON w.id = s.worktree_id
		WHERE s.runtime_backend = 'tmux'
		ORDER BY w.path, s.created_at, s.session_key`,
	)
	if err != nil {
		return nil, fmt.Errorf("list all project worktree tmux sessions: %w", err)
	}
	defer rows.Close()

	var out []ProjectWorktreeTmuxSession
	for rows.Next() {
		session, err := scanProjectWorktreeTmuxSessionRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *session)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate project worktree tmux sessions: %w", err)
	}
	return out, nil
}

// DeleteProjectWorktreeTmuxSession removes one stored registered-worktree
// runtime tmux session by its runtime session key.
func (d *DB) DeleteProjectWorktreeTmuxSession(
	ctx context.Context,
	worktreeID string,
	sessionKey string,
) error {
	_, err := d.rw.ExecContext(ctx, `
		DELETE FROM middleman_project_worktree_runtime_sessions
		WHERE worktree_id = ?
		  AND session_key = ?
		  AND runtime_backend = 'tmux'`, worktreeID, sessionKey,
	)
	if err != nil {
		return fmt.Errorf("delete project worktree tmux session: %w", err)
	}
	return nil
}

// DeleteProjectWorktreeTmuxSessionCreatedAt removes one stored
// registered-worktree runtime tmux session only if it still belongs to the
// same session generation. Caller-owned command session keys are reusable,
// so an exited generation's async cleanup must not delete the row a newer
// live session with the same key has since written.
func (d *DB) DeleteProjectWorktreeTmuxSessionCreatedAt(
	ctx context.Context,
	worktreeID string,
	sessionKey string,
	createdAt time.Time,
) (bool, error) {
	result, err := d.rw.ExecContext(ctx, `
		DELETE FROM middleman_project_worktree_runtime_sessions
		WHERE worktree_id = ?
		  AND session_key = ?
		  AND runtime_backend = 'tmux'
		  AND created_at = ?`,
		worktreeID, sessionKey, canonicalUTCTime(createdAt),
	)
	if err != nil {
		return false, fmt.Errorf("delete project worktree tmux session: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf(
			"delete project worktree tmux session rows: %w", err,
		)
	}
	return rows > 0, nil
}

func scanProject(row *sql.Row) (*Project, error) {
	project, err := scanProjectFields(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrProjectNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan project: %w", err)
	}
	return project, nil
}

func scanProjectRow(rows *sql.Rows) (*Project, error) {
	project, err := scanProjectFields(rows)
	if err != nil {
		return nil, fmt.Errorf("scan project: %w", err)
	}
	return project, nil
}

func scanProjectFields(scanner interface{ Scan(...any) error }) (*Project, error) {
	var (
		p            Project
		defaultBr    sql.NullString
		repoKind     sql.NullString
		isStale      int64
		platform     sql.NullString
		platformHost sql.NullString
		repoOwner    sql.NullString
		repoName     sql.NullString
	)
	err := scanner.Scan(
		&p.ID, &p.DisplayName, &p.LocalPath,
		&defaultBr, &repoKind, &isStale, &p.CreatedAt, &p.UpdatedAt,
		&platform, &platformHost, &repoOwner, &repoName,
	)
	if err != nil {
		return nil, err
	}
	if defaultBr.Valid {
		p.DefaultBranch = defaultBr.String
	}
	p.RepositoryKind = repoKind.String
	if p.RepositoryKind == "" {
		p.RepositoryKind = "standard"
	}
	p.IsStale = isStale != 0
	if platformHost.Valid {
		p.PlatformIdentity = &PlatformIdentity{
			Platform: platform.String,
			Host:     platformHost.String,
			Owner:    repoOwner.String,
			Name:     repoName.String,
		}
	}
	return &p, nil
}

func scanProjectWorktree(row *sql.Row) (*ProjectWorktree, error) {
	worktree, err := scanProjectWorktreeFields(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrProjectNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan project worktree: %w", err)
	}
	return worktree, nil
}

func scanProjectWorktreeRow(rows *sql.Rows) (*ProjectWorktree, error) {
	worktree, err := scanProjectWorktreeFields(rows)
	if err != nil {
		return nil, fmt.Errorf("scan project worktree: %w", err)
	}
	return worktree, nil
}

func scanProjectWorktreeFields(
	scanner interface{ Scan(...any) error },
) (*ProjectWorktree, error) {
	var (
		w            ProjectWorktree
		isStale      int64
		isHidden     int64
		linkedIssues string
	)
	if err := scanner.Scan(
		&w.ID, &w.ProjectID, &w.Branch, &w.Path,
		&isStale, &isHidden, &w.SessionBackend, &linkedIssues,
		&w.CreatedAt, &w.UpdatedAt,
	); err != nil {
		return nil, err
	}
	w.IsStale = isStale != 0
	w.IsHidden = isHidden != 0
	issues, err := decodeIssueNumbers(linkedIssues)
	if err != nil {
		return nil, fmt.Errorf("decode linked issue numbers for worktree %s: %w", w.ID, err)
	}
	w.LinkedIssueNumbers = issues
	return &w, nil
}

// decodeIssueNumbers parses the JSON array stored in linked_issue_numbers.
// An empty or "[]" column yields an empty (non-nil) slice so callers never
// distinguish "no links" from "unset".
func decodeIssueNumbers(raw string) ([]int, error) {
	if raw == "" {
		return []int{}, nil
	}
	var nums []int
	if err := json.Unmarshal([]byte(raw), &nums); err != nil {
		return nil, err
	}
	if nums == nil {
		return []int{}, nil
	}
	return nums, nil
}

// normalizeIssueNumbers returns a sorted, deduped copy of the input so stored
// and emitted linked-issue lists are canonical. Values are not otherwise
// validated: the host write path does not reject non-positive numbers, so
// neither does middleman.
func normalizeIssueNumbers(in []int) []int {
	out := slices.Clone(in)
	slices.Sort(out)
	out = slices.Compact(out)
	if out == nil {
		return []int{}
	}
	return out
}

// SetProjectWorktreeLinkedIssues replaces a worktree's explicit linked issue
// numbers, returning the updated row. The list is normalized (sorted, deduped)
// and stored as a JSON array; an empty list clears the links. The update is
// scoped to the owning project so a worktree id under a different project is
// treated as not found. Discovery reconciliation never touches this column, so
// the links persist across discovery passes. Returns ErrProjectNotFound when no
// matching worktree exists.
func (d *DB) SetProjectWorktreeLinkedIssues(
	ctx context.Context, projectID, worktreeID string, issues []int, now time.Time,
) (*ProjectWorktree, error) {
	ts := canonicalUTCTime(now)
	if ts.IsZero() {
		ts = time.Now().UTC()
	}
	encoded, err := json.Marshal(normalizeIssueNumbers(issues))
	if err != nil {
		return nil, fmt.Errorf("marshal linked issue numbers: %w", err)
	}
	res, err := d.rw.ExecContext(ctx, `
		UPDATE middleman_project_worktrees
		SET linked_issue_numbers = ?, updated_at = ?
		WHERE id = ? AND project_id = ?`,
		string(encoded), ts, worktreeID, projectID,
	)
	if err != nil {
		return nil, fmt.Errorf("set worktree linked issues: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("set worktree linked issues rows affected: %w", err)
	}
	if affected == 0 {
		return nil, ErrProjectNotFound
	}
	return d.GetProjectWorktreeByID(ctx, worktreeID)
}

func scanProjectWorktreeTmuxSessionRow(
	rows *sql.Rows,
) (*ProjectWorktreeTmuxSession, error) {
	session, err := scanProjectWorktreeTmuxSessionFields(rows)
	if err != nil {
		return nil, fmt.Errorf("scan project worktree tmux session: %w", err)
	}
	return session, nil
}

func scanProjectWorktreeTmuxSessionFields(
	scanner interface{ Scan(...any) error },
) (*ProjectWorktreeTmuxSession, error) {
	var session ProjectWorktreeTmuxSession
	if err := scanner.Scan(
		&session.WorktreeID, &session.SessionKey, &session.SessionName,
		&session.TargetKey, &session.Label, &session.CreatedAt,
		&session.WorktreePath, &session.ProjectID,
	); err != nil {
		return nil, err
	}
	session.CreatedAt = session.CreatedAt.UTC()
	return &session, nil
}

func newProjectID() (string, error) {
	return newPrefixedID("prj_")
}

func newWorktreeID() (string, error) {
	return newPrefixedID("wtr_")
}

func newPrefixedID(prefix string) (string, error) {
	var buf [12]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("generate id: %w", err)
	}
	return prefix + hex.EncodeToString(buf[:]), nil
}

func isUniqueConstraintErr(err error, suffix string) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") &&
		strings.Contains(msg, suffix)
}
