package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"go.kenn.io/forge/platform"
)

type RepositoryLifecycleState string

const (
	RepositoryLifecycleActive   RepositoryLifecycleState = "active"
	RepositoryLifecycleInactive RepositoryLifecycleState = "inactive"
)

// RepositoryCatalogEntry is one repository row keyed by its provider
// identity. Active entries own their current owner/name route; an inactive
// entry keeps its last route for display only.
type RepositoryCatalogEntry struct {
	Repository Repo
	Lifecycle  RepositoryLifecycleState
}

// ActiveRepo returns the entry's repository when the catalog reports it as
// active, or nil when it is inactive.
func (e RepositoryCatalogEntry) ActiveRepo() (*ActiveRepo, error) {
	if e.Lifecycle != RepositoryLifecycleActive {
		return nil, nil
	}
	return newActiveRepo(e.Repository)
}

type RepositoryCatalogFilter struct {
	Platform       string
	PlatformHost   string
	PlatformRepoID int64
	RepoPath       string
	Lifecycle      RepositoryLifecycleState
}

// PullDiffProviderSnapshot binds a repository to the complete set of pull SHA
// fields from one serialized parent snapshot.
type PullDiffProviderSnapshot struct {
	Repository       Repo
	PullNumber       int
	SnapshotRevision int64
	PlatformHeadSHA  string
	PlatformBaseSHA  string
	DiffHeadSHA      string
	DiffBaseSHA      string
	MergeBaseSHA     string
	State            string
}

func validateRepositoryObservation(identity RepoIdentity) error {
	if strings.TrimSpace(identity.Platform) == "" {
		return errors.New("repository observation platform is required")
	}
	if identity.PlatformRepoID <= 0 {
		return errors.New("repository observation provider id is required")
	}
	if strings.TrimSpace(identity.Owner) == "" ||
		strings.TrimSpace(identity.Name) == "" {
		return errors.New("repository observation owner and name are required")
	}
	return nil
}

const repositoryCatalogSelect = `
	SELECT r.id, r.platform, r.platform_host, r.platform_repo_id,
	       r.owner, r.name, r.repo_path,
	       r.owner_key, r.name_key, r.repo_path_key,
	       r.web_url, r.clone_url, r.default_branch,
	       r.last_sync_started_at, r.last_sync_completed_at,
	       r.last_sync_error, r.allow_squash_merge, r.allow_merge_commit,
	       r.allow_rebase_merge, r.viewer_can_merge,
	       r.label_catalog_synced_at, r.label_catalog_checked_at,
	       r.label_catalog_sync_error, r.created_at,
	       r.lifecycle_state
	FROM forge_repos r`

// GetRepositoryByProviderID returns the repository with the given provider
// identity in any lifecycle state, or nil when it is unknown.
func (d *DB) GetRepositoryByProviderID(
	ctx context.Context, identity platform.RepositoryIdentity,
) (*RepositoryCatalogEntry, error) {
	identity = identity.Canonical()
	if !identity.Valid() {
		return nil, errors.New(
			"repository provider id lookup requires platform, host, and provider id",
		)
	}
	return loadRepositoryCatalogEntry(
		ctx, d.roStmts,
		`r.platform = ? AND r.platform_host = ? AND r.platform_repo_id = ?`,
		canonicalRepoPlatform(identity.Provider),
		identity.PlatformHost,
		identity.PlatformRepoID,
	)
}

// GetActiveRepoByProviderID returns the active repository with the given
// provider identity, or nil when it is unknown or inactive.
func (d *DB) GetActiveRepoByProviderID(
	ctx context.Context, identity platform.RepositoryIdentity,
) (*ActiveRepo, error) {
	entry, err := d.GetRepositoryByProviderID(ctx, identity)
	if err != nil || entry == nil {
		return nil, err
	}
	return entry.ActiveRepo()
}

// ResolveActiveRepositoryRoute resolves an owner/name route to the active
// repository that currently occupies it. Routes are user input; resolve them
// once at the edge and pass the repository on.
func (d *DB) ResolveActiveRepositoryRoute(
	ctx context.Context, route RepoIdentity,
) (*RepositoryCatalogEntry, error) {
	route = canonicalRepoIdentity(route)
	if route.Platform == "" || route.PlatformHost == "" ||
		route.Owner == "" || route.Name == "" {
		return nil, errors.New(
			"active repository route lookup requires platform, host, owner, and name",
		)
	}
	return loadRepositoryCatalogEntry(
		ctx, d.roStmts,
		`r.lifecycle_state = 'active' AND r.platform = ?
		 AND r.platform_host = ? AND r.repo_path_key = ?`,
		route.Platform, route.PlatformHost, route.RepoPathKey,
	)
}

// GetPullDiffProviderSnapshot reads every diff descriptor field of one pull
// while holding the pull's parent-snapshot lock.
func (d *DB) GetPullDiffProviderSnapshot(
	ctx context.Context, route RepoIdentity, number int,
) (*PullDiffProviderSnapshot, error) {
	entry, err := d.ResolveActiveRepositoryRoute(ctx, route)
	if err != nil || entry == nil {
		return nil, err
	}
	releasePull, err := d.lockMergeRequestSnapshot(ctx, entry.Repository.ID, number)
	if err != nil {
		return nil, err
	}
	defer releasePull()

	snapshot := PullDiffProviderSnapshot{Repository: entry.Repository}
	err = d.roQueryRowContext(ctx, `
		SELECT p.number, p.snapshot_revision,
		       p.platform_head_sha, p.platform_base_sha,
		       p.diff_head_sha, p.diff_base_sha, p.merge_base_sha, p.state
		FROM forge_merge_requests p
		WHERE p.repo_id = ? AND p.number = ?
		  AND NOT EXISTS (
			SELECT 1 FROM forge_archive_items ai
			WHERE ai.repo_id = p.repo_id
			  AND ai.item_type = 'merge_request'
			  AND ai.item_number = p.number
			  AND ai.lifecycle_state = 'removed_upstream'
		  )`,
		entry.Repository.ID, number,
	).Scan(
		&snapshot.PullNumber, &snapshot.SnapshotRevision,
		&snapshot.PlatformHeadSHA, &snapshot.PlatformBaseSHA,
		&snapshot.DiffHeadSHA, &snapshot.DiffBaseSHA,
		&snapshot.MergeBaseSHA, &snapshot.State,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load pull diff provider snapshot: %w", err)
	}
	return &snapshot, nil
}

func (d *DB) ListRepositoryCatalog(
	ctx context.Context,
	filter RepositoryCatalogFilter,
) ([]RepositoryCatalogEntry, error) {
	filter.Platform = strings.TrimSpace(filter.Platform)
	filter.PlatformHost = strings.TrimSpace(filter.PlatformHost)
	filter.RepoPath = strings.TrimSpace(filter.RepoPath)
	if filter.PlatformRepoID != 0 &&
		(filter.Platform == "" || filter.PlatformHost == "") {
		return nil, errors.New(
			"repository provider id filter requires platform and host",
		)
	}
	if filter.Lifecycle != "" &&
		filter.Lifecycle != RepositoryLifecycleActive &&
		filter.Lifecycle != RepositoryLifecycleInactive {
		return nil, fmt.Errorf(
			"unsupported repository lifecycle %q",
			filter.Lifecycle,
		)
	}
	var clauses []string
	var args []any
	if filter.Platform != "" {
		clauses = append(clauses, `r.platform = ?`)
		args = append(args, canonicalRepoPlatform(filter.Platform))
	}
	if filter.PlatformHost != "" {
		clauses = append(clauses, `r.platform_host = ?`)
		args = append(args, strings.ToLower(filter.PlatformHost))
	}
	if filter.PlatformRepoID != 0 {
		clauses = append(clauses, `r.platform_repo_id = ?`)
		args = append(args, filter.PlatformRepoID)
	}
	if filter.RepoPath != "" {
		clauses = append(clauses, `r.repo_path_key = ?`)
		args = append(args, canonicalRepoPathKey(filter.RepoPath))
	}
	if filter.Lifecycle != "" {
		clauses = append(clauses, `r.lifecycle_state = ?`)
		args = append(args, filter.Lifecycle)
	}
	query := repositoryCatalogSelect
	if len(clauses) != 0 {
		query += " WHERE " + strings.Join(clauses, " AND ")
	}
	query += ` ORDER BY r.platform, r.platform_host, r.owner_key, r.name_key, r.id`
	rows, err := d.roQueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list repository catalog: %w", err)
	}
	defer rows.Close()

	var entries []RepositoryCatalogEntry
	for rows.Next() {
		var entry RepositoryCatalogEntry
		if err := scanRepositoryCatalogEntry(rows, &entry); err != nil {
			return nil, fmt.Errorf("scan repository catalog entry: %w", err)
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate repository catalog: %w", err)
	}
	return entries, nil
}

// ObserveRepository records what the provider reports for a repository: the
// row keyed by its provider identity becomes active at the reported route,
// so a rename or transfer only updates owner/name. Another active repository
// still holding that route is deactivated; it is re-resolved by its own ID
// the next time it is read from the provider.
func (d *DB) ObserveRepository(
	ctx context.Context, observed RepoIdentity,
) (*RepositoryCatalogEntry, error) {
	if err := validateRepositoryObservation(observed); err != nil {
		return nil, err
	}
	observed = canonicalRepoIdentity(observed)
	if observed.PlatformHost == "" || observed.RepoPathKey == "" {
		return nil, errors.New("repository observation host and path are required")
	}
	err := d.Tx(ctx, func(tx *sql.Tx) error {
		if observed.Platform == "github" {
			var pending bool
			if err := tx.QueryRowContext(ctx, `
				SELECT EXISTS (
					SELECT 1 FROM forge_repos
					WHERE platform = 'github' AND platform_host = ?
					  AND github_node_id <> ''
				)`, observed.PlatformHost,
			).Scan(&pending); err != nil {
				return fmt.Errorf("check pending github conversion: %w", err)
			}
			if pending {
				return ErrGitHubRepositoryConversionPending
			}
		}
		displaced, err := activeRouteOccupantsTx(ctx, tx, observed)
		if err != nil {
			return err
		}
		var previous RepoIdentity
		err = tx.QueryRowContext(ctx, `
			SELECT platform, platform_host, owner_key, name_key, repo_path_key
			FROM forge_repos
			WHERE platform = ? AND platform_host = ? AND platform_repo_id = ?`,
			observed.Platform, observed.PlatformHost, observed.PlatformRepoID,
		).Scan(
			&previous.Platform, &previous.PlatformHost,
			&previous.OwnerKey, &previous.NameKey, &previous.RepoPathKey,
		)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("load observed repository route: %w", err)
		}
		if err == nil && previous.RepoPathKey != observed.RepoPathKey {
			// A rename vacates the old route; its route-keyed state belonged
			// to this repository and must not pass to the route's next holder.
			if err := deleteRepositoryRouteScopedStateTx(ctx, tx, previous); err != nil {
				return err
			}
		}
		if len(displaced) != 0 {
			if err := deleteRepositoryRouteScopedStateTx(ctx, tx, observed); err != nil {
				return err
			}
			for _, id := range displaced {
				if _, err := tx.ExecContext(ctx, `
					UPDATE forge_repos SET lifecycle_state = 'inactive' WHERE id = ?`,
					id,
				); err != nil {
					return fmt.Errorf("deactivate displaced repository: %w", err)
				}
			}
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO forge_repos (
				platform, platform_host, platform_repo_id,
				owner, name, repo_path,
				owner_key, name_key, repo_path_key,
				lifecycle_state, viewer_can_merge
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'active', 0)
			ON CONFLICT(platform, platform_host, platform_repo_id)
			  WHERE platform_repo_id > 0
			DO UPDATE SET
				owner = excluded.owner,
				name = excluded.name,
				repo_path = excluded.repo_path,
				owner_key = excluded.owner_key,
				name_key = excluded.name_key,
				repo_path_key = excluded.repo_path_key,
				lifecycle_state = 'active'`,
			observed.Platform, observed.PlatformHost, observed.PlatformRepoID,
			observed.Owner, observed.Name, observed.RepoPath,
			observed.OwnerKey, observed.NameKey, observed.RepoPathKey,
		)
		if err != nil {
			return fmt.Errorf("upsert observed repository: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("observe repository: %w", err)
	}
	entry, err := d.GetRepositoryByProviderID(ctx, observed.ProviderIdentity())
	if err != nil {
		return nil, err
	}
	if entry == nil {
		return nil, errors.New("observed repository is missing")
	}
	return entry, nil
}

// activeRouteOccupantsTx returns the active repositories other than the
// observed one that still hold its route.
func activeRouteOccupantsTx(
	ctx context.Context, tx *sql.Tx, observed RepoIdentity,
) ([]int64, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT id FROM forge_repos
		WHERE lifecycle_state = 'active'
		  AND platform = ? AND platform_host = ? AND repo_path_key = ?
		  AND platform_repo_id <> ?`,
		observed.Platform, observed.PlatformHost, observed.RepoPathKey,
		observed.PlatformRepoID,
	)
	if err != nil {
		return nil, fmt.Errorf("find route occupant: %w", err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan route occupant: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate route occupants: %w", err)
	}
	return ids, nil
}

// UpdateRepoProviderObservation persists provider metadata and merge
// settings for a repository. Snapshots may omit URLs or the default branch
// (minimal payloads, list responses); stored values survive an omitted field.
// Merge settings and viewer permission are written only when the provider
// reported them.
func (d *DB) UpdateRepoProviderObservation(
	ctx context.Context,
	repoID int64,
	metadata RepoProviderMetadata,
	mergeSettings *RepoMergeSettings,
	viewerCanMerge *bool,
) error {
	query := `UPDATE forge_repos
		SET web_url = CASE WHEN ? <> '' THEN ? ELSE web_url END,
		    clone_url = CASE WHEN ? <> '' THEN ? ELSE clone_url END,
		    default_branch = CASE WHEN ? <> '' THEN ? ELSE default_branch END`
	args := []any{
		metadata.WebURL, metadata.WebURL,
		metadata.CloneURL, metadata.CloneURL,
		metadata.DefaultBranch, metadata.DefaultBranch,
	}
	if mergeSettings != nil {
		query += `, allow_squash_merge = ?, allow_merge_commit = ?, allow_rebase_merge = ?`
		args = append(args,
			mergeSettings.AllowSquashMerge,
			mergeSettings.AllowMergeCommit,
			mergeSettings.AllowRebaseMerge,
		)
	}
	if viewerCanMerge != nil {
		query += `, viewer_can_merge = ?`
		args = append(args, *viewerCanMerge)
	}
	query += ` WHERE id = ?`
	args = append(args, repoID)
	if _, err := d.rwExecContext(ctx, query, args...); err != nil {
		return fmt.Errorf("update repository provider observation: %w", err)
	}
	return nil
}

// DeactivateRepository records an authoritative provider absence, such as a
// deleted repository or lost access. Provider errors and unavailable
// configuration must not call this method; they keep the last verified row.
func (d *DB) DeactivateRepository(
	ctx context.Context, identity platform.RepositoryIdentity,
) (*RepositoryCatalogEntry, error) {
	identity = identity.Canonical()
	if !identity.Valid() {
		return nil, errors.New(
			"repository deactivation requires platform, host, and provider id",
		)
	}
	if _, err := d.rwExecContext(ctx, `
		UPDATE forge_repos SET lifecycle_state = 'inactive'
		WHERE platform = ? AND platform_host = ? AND platform_repo_id = ?`,
		canonicalRepoPlatform(identity.Provider), identity.PlatformHost,
		identity.PlatformRepoID,
	); err != nil {
		return nil, fmt.Errorf("deactivate repository: %w", err)
	}
	return d.GetRepositoryByProviderID(ctx, identity)
}

// ErrGitHubRepositoryConversionPending reports a GitHub observation for a
// host that still has repositories stored under GitHub node IDs. Recording it
// could create a second row for a repository whose history is pending.
var ErrGitHubRepositoryConversionPending = errors.New(
	"github repository identity conversion is pending for this host",
)

// PendingGitHubRepository is a GitHub repository stored before repository
// identity became the integer ID. Only its node ID and last route are known.
type PendingGitHubRepository struct {
	RepoID       int64
	PlatformHost string
	Owner        string
	Name         string
	NodeID       string
}

// ListPendingGitHubRepositories returns GitHub repositories still waiting for
// their node ID to be replaced by the integer repository ID.
func (d *DB) ListPendingGitHubRepositories(
	ctx context.Context,
) ([]PendingGitHubRepository, error) {
	rows, err := d.roQueryContext(ctx, `
		SELECT id, platform_host, owner, name, github_node_id
		FROM forge_repos
		WHERE platform = 'github' AND github_node_id <> ''
		ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list pending github repositories: %w", err)
	}
	defer rows.Close()
	var pending []PendingGitHubRepository
	for rows.Next() {
		var repo PendingGitHubRepository
		if err := rows.Scan(
			&repo.RepoID, &repo.PlatformHost, &repo.Owner, &repo.Name, &repo.NodeID,
		); err != nil {
			return nil, fmt.Errorf("scan pending github repository: %w", err)
		}
		pending = append(pending, repo)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate pending github repositories: %w", err)
	}
	return pending, nil
}

// CompleteGitHubRepositoryConversion records the integer repository ID GitHub
// reported for a pending repository's node ID, along with its workspaces'
// launch specifications. platformRepoID 0 records that
// GitHub no longer resolves the node: the row stays inactive with its history
// and stops blocking observations for its host.
func (d *DB) CompleteGitHubRepositoryConversion(
	ctx context.Context, repoID int64, platformRepoID int64,
) error {
	if platformRepoID < 0 {
		return errors.New("github repository id must not be negative")
	}
	return d.Tx(ctx, func(tx *sql.Tx) error {
		var pending bool
		if err := tx.QueryRowContext(ctx, `
			SELECT github_node_id <> '' FROM forge_repos
			WHERE id = ? AND platform = 'github'`,
			repoID,
		).Scan(&pending); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			return fmt.Errorf("load pending github repository: %w", err)
		}
		if !pending {
			return nil
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE forge_repos SET platform_repo_id = ?, github_node_id = ''
			WHERE id = ?`, platformRepoID, repoID,
		); err != nil {
			return fmt.Errorf("record github repository id: %w", err)
		}
		if platformRepoID == 0 {
			return nil
		}
		// Migration 60 zeroed the node IDs launch specifications embedded;
		// the workspace's repository row says which ones this ID replaces.
		for _, path := range []string{"$.repository.platform_repo_id", "$.pull.base_repo_id"} {
			if _, err := tx.ExecContext(ctx, `
				UPDATE forge_workspace_launch_specs
				SET spec_json = json_set(spec_json, '`+path+`', ?)
				WHERE json_extract(spec_json, '`+path+`') = 0
				  AND workspace_id IN (
					SELECT id FROM forge_workspaces WHERE repo_id = ?
				  )`,
				platformRepoID, repoID,
			); err != nil {
				return fmt.Errorf("record github repository id in launch specifications: %w", err)
			}
		}
		return nil
	})
}

// RepositoryRouteHasOtherRepository reports whether a repository other than
// repoID has held the route, currently or as an inactive row's last route.
// Local paths named after a route use it to add a suffix only on collision.
func (d *DB) RepositoryRouteHasOtherRepository(
	ctx context.Context, route RepoIdentity, repoID int64,
) (bool, error) {
	route = canonicalRepoIdentity(route)
	if repoID <= 0 {
		return false, errors.New("repository route owner id is required")
	}
	if route.Platform == "" || route.PlatformHost == "" || route.RepoPathKey == "" {
		return false, errors.New("repository route identity is required")
	}
	var shared bool
	err := d.roQueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM forge_repos
			WHERE platform = ? AND platform_host = ? AND repo_path_key = ?
			  AND id <> ?
		)`,
		route.Platform, route.PlatformHost, route.RepoPathKey, repoID,
	).Scan(&shared)
	if err != nil {
		return false, fmt.Errorf("check repository route reuse: %w", err)
	}
	return shared, nil
}

// deleteRepositoryRouteScopedStateTx removes state keyed only by a route when
// that route changes hands, so the new occupant never inherits it.
func deleteRepositoryRouteScopedStateTx(
	ctx context.Context,
	tx *sql.Tx,
	route RepoIdentity,
) error {
	steps := []struct {
		name string
		sql  string
	}{
		{
			name: "delete unlinked route notifications",
			sql: `DELETE FROM forge_notification_items
			      WHERE repo_id IS NULL
			        AND platform = ? AND platform_host = ?
			        AND repo_owner = ? AND repo_name = ?`,
		},
		{
			name: "delete route HTTP ETags",
			sql: `DELETE FROM forge_http_etags
			      WHERE platform = ? AND platform_host = ?
			        AND owner_key = ? AND name_key = ?`,
		},
		{
			name: "delete route notification watermark",
			sql: `DELETE FROM forge_notification_sync_watermarks
			      WHERE platform = ? AND platform_host = ?
			        AND repo_owner = ? AND repo_name = ?`,
		},
	}
	for _, step := range steps {
		if _, err := tx.ExecContext(ctx, step.sql,
			route.Platform, route.PlatformHost, route.OwnerKey, route.NameKey,
		); err != nil {
			return fmt.Errorf("%s: %w", step.name, err)
		}
	}
	return nil
}

func loadRepositoryCatalogEntry(
	ctx context.Context,
	q rowQueryer,
	where string,
	args ...any,
) (*RepositoryCatalogEntry, error) {
	var entry RepositoryCatalogEntry
	err := scanRepositoryCatalogEntry(
		q.QueryRowContext(ctx, repositoryCatalogSelect+" WHERE "+where, args...),
		&entry,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load repository catalog entry: %w", err)
	}
	return &entry, nil
}

func scanRepositoryCatalogEntry(
	scanner scanner,
	entry *RepositoryCatalogEntry,
) error {
	r := &entry.Repository
	if err := scanner.Scan(
		&r.ID,
		&r.Platform,
		&r.PlatformHost,
		&r.PlatformRepoID,
		&r.Owner,
		&r.Name,
		&r.RepoPath,
		&r.OwnerKey,
		&r.NameKey,
		&r.RepoPathKey,
		&r.WebURL,
		&r.CloneURL,
		&r.DefaultBranch,
		&r.LastSyncStartedAt,
		&r.LastSyncCompletedAt,
		&r.LastSyncError,
		&r.AllowSquashMerge,
		&r.AllowMergeCommit,
		&r.AllowRebaseMerge,
		&r.ViewerCanMerge,
		&r.LabelCatalogSyncedAt,
		&r.LabelCatalogCheckedAt,
		&r.LabelCatalogSyncError,
		&r.CreatedAt,
		&entry.Lifecycle,
	); err != nil {
		return err
	}
	normalizeRepoTimestamps(r)
	return nil
}
