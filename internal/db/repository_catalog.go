package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

type RepositoryLifecycleState string

const (
	RepositoryLifecycleActive   RepositoryLifecycleState = "active"
	RepositoryLifecycleInactive RepositoryLifecycleState = "inactive"
)

type RepositoryRoute struct {
	ID           int64
	RepoID       int64
	Platform     string
	PlatformHost string
	Owner        string
	Name         string
	RepoPath     string
	OwnerKey     string
	NameKey      string
	RepoPathKey  string
	Current      bool
	FirstSeenAt  time.Time
	LastSeenAt   time.Time
}

type RepositoryCatalogEntry struct {
	Repository Repo
	Lifecycle  RepositoryLifecycleState
	Routes     []RepositoryRoute
}

type RepositoryCatalogFilter struct {
	Platform       string
	PlatformHost   string
	PlatformRepoID string
	RepoPath       string
	Lifecycle      RepositoryLifecycleState
}

type repositoryCatalogQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type repositoryCatalogScanner interface {
	Scan(...any) error
}

func validateRepositoryObservation(identity RepoIdentity) error {
	if strings.TrimSpace(identity.Platform) == "" {
		return errors.New("repository observation platform is required")
	}
	if strings.TrimSpace(identity.PlatformRepoID) == "" {
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

func (d *DB) GetRepositoryByProviderID(
	ctx context.Context,
	platform string,
	platformHost string,
	platformRepoID string,
) (*RepositoryCatalogEntry, error) {
	release, err := d.LockRepositoryReconciliationRead(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	return d.getRepositoryByProviderID(
		ctx, platform, platformHost, platformRepoID,
	)
}

func (d *DB) getRepositoryByProviderID(
	ctx context.Context,
	platform string,
	platformHost string,
	platformRepoID string,
) (*RepositoryCatalogEntry, error) {
	platform = strings.TrimSpace(platform)
	platformHost = strings.TrimSpace(platformHost)
	platformRepoID = strings.TrimSpace(platformRepoID)
	if platform == "" || platformHost == "" || platformRepoID == "" {
		return nil, errors.New(
			"repository provider id lookup requires platform, host, and provider id",
		)
	}
	identity := canonicalRepoIdentity(RepoIdentity{
		Platform:       platform,
		PlatformHost:   platformHost,
		PlatformRepoID: platformRepoID,
	})
	return loadRepositoryCatalogEntry(
		ctx,
		d.ro,
		`r.platform = ? AND r.platform_host = ? AND r.platform_repo_id = ?`,
		identity.Platform,
		identity.PlatformHost,
		identity.PlatformRepoID,
	)
}

func (d *DB) ResolveActiveRepositoryRoute(
	ctx context.Context,
	identity RepoIdentity,
) (*RepositoryCatalogEntry, error) {
	release, err := d.LockRepositoryReconciliationRead(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	return d.resolveActiveRepositoryRoute(ctx, identity)
}

// resolveActiveRepositoryRoute is ResolveActiveRepositoryRoute without the
// reconciliation read lock, for callers that already hold it — nested
// acquisition deadlocks behind a queued reconciliation writer.
func (d *DB) resolveActiveRepositoryRoute(
	ctx context.Context,
	identity RepoIdentity,
) (*RepositoryCatalogEntry, error) {
	identity = canonicalRepoIdentity(identity)
	if strings.TrimSpace(identity.Platform) == "" ||
		strings.TrimSpace(identity.PlatformHost) == "" ||
		strings.TrimSpace(identity.Owner) == "" ||
		strings.TrimSpace(identity.Name) == "" {
		return nil, errors.New(
			"active repository route lookup requires platform, host, owner, and name",
		)
	}
	return loadRepositoryCatalogEntry(
		ctx,
		d.ro,
		`r.lifecycle_state = 'active' AND EXISTS (
			SELECT 1
			FROM forge_repo_routes rr
			WHERE rr.repo_id = r.id
			  AND rr.is_current = 1
			  AND rr.platform = ?
			  AND rr.platform_host = ?
			  AND rr.repo_path_key = ?
		)`,
		identity.Platform,
		identity.PlatformHost,
		identity.RepoPathKey,
	)
}

func (d *DB) ListRepositoryCatalog(
	ctx context.Context,
	filter RepositoryCatalogFilter,
) ([]RepositoryCatalogEntry, error) {
	release, err := d.LockRepositoryReconciliationRead(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	filter.Platform = strings.TrimSpace(filter.Platform)
	filter.PlatformHost = strings.TrimSpace(filter.PlatformHost)
	filter.PlatformRepoID = strings.TrimSpace(filter.PlatformRepoID)
	filter.RepoPath = strings.TrimSpace(filter.RepoPath)
	if filter.PlatformRepoID != "" &&
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
	platform := canonicalRepoPlatform(filter.Platform)
	host := strings.ToLower(filter.PlatformHost)
	if filter.PlatformRepoID != "" {
		clauses = append(clauses,
			`r.platform = ? AND r.platform_host = ? AND r.platform_repo_id = ?`,
		)
		args = append(args, platform, host, filter.PlatformRepoID)
	} else if filter.RepoPath == "" {
		if filter.Platform != "" {
			clauses = append(clauses, `r.platform = ?`)
			args = append(args, platform)
		}
		if filter.PlatformHost != "" {
			clauses = append(clauses, `r.platform_host = ?`)
			args = append(args, host)
		}
	}
	if filter.RepoPath != "" {
		routeClauses := []string{`rr.repo_id = r.id`, `rr.repo_path_key = ?`}
		routeArgs := []any{canonicalRepoPathKey(filter.RepoPath)}
		if filter.Platform != "" {
			routeClauses = append(routeClauses, `rr.platform = ?`)
			routeArgs = append(routeArgs, platform)
		}
		if filter.PlatformHost != "" {
			routeClauses = append(routeClauses, `rr.platform_host = ?`)
			routeArgs = append(routeArgs, host)
		}
		clauses = append(clauses, `EXISTS (
			SELECT 1 FROM forge_repo_routes rr WHERE `+
			strings.Join(routeClauses, " AND ")+`)`)
		args = append(args, routeArgs...)
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
	rows, err := d.ro.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list repository catalog: %w", err)
	}
	defer rows.Close()

	var entries []RepositoryCatalogEntry
	var repoIDs []int64
	for rows.Next() {
		var entry RepositoryCatalogEntry
		if err := scanRepositoryCatalogEntry(rows, &entry); err != nil {
			return nil, fmt.Errorf("scan repository catalog entry: %w", err)
		}
		entries = append(entries, entry)
		repoIDs = append(repoIDs, entry.Repository.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate repository catalog: %w", err)
	}
	routes, err := loadRepositoryRoutes(ctx, d.ro, repoIDs)
	if err != nil {
		return nil, err
	}
	for i := range entries {
		entries[i].Routes = routes[entries[i].Repository.ID]
	}
	return entries, nil
}

// ReconcileRepositoryObservation records a provider-verified repository
// identity without moving or combining repository-owned data. Provider IDs
// identify catalog entries; owner/name routes identify only the current
// occupant and its historical display names.
//
// The returned bool reports whether the observation was applied. An
// observation older than the repository's latest recorded route observation
// is ignored: the current catalog entry is returned with false, and callers
// must treat any metadata captured alongside the stale observation as stale
// too.
func (d *DB) ReconcileRepositoryObservation(
	ctx context.Context,
	identity RepoIdentity,
	observedAt time.Time,
) (*RepositoryCatalogEntry, bool, error) {
	if err := validateRepositoryObservation(identity); err != nil {
		return nil, false, err
	}
	identity.PlatformRepoID = strings.TrimSpace(identity.PlatformRepoID)
	identity = canonicalRepoIdentity(identity)
	if identity.PlatformHost == "" || identity.RepoPathKey == "" {
		return nil, false, errors.New("repository observation host and path are required")
	}
	observedAt = canonicalUTCTime(observedAt)

	release := d.lockRepositoryReconciliationWrite()
	defer release()
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}

	var repoID int64
	accepted := true
	err := d.Tx(ctx, func(tx *sql.Tx) error {
		sourceID, sourceFound, err := repositoryIDByProviderIDTx(
			ctx, tx, identity,
		)
		if err != nil {
			return err
		}
		targetID, targetFound, err := currentRepositoryIDByRouteTx(
			ctx, tx, identity,
		)
		if err != nil {
			return err
		}
		if sourceFound {
			watermark, observed, err := repositoryObservationWatermarkTx(
				ctx, tx, sourceID,
			)
			if err != nil {
				return err
			}
			if observed && observedAt.Before(watermark) {
				repoID = sourceID
				accepted = false
				return nil
			}
		}
		if targetFound && (!sourceFound || targetID != sourceID) {
			lastSeenAt, _, err := currentRepositoryRouteLastSeenTx(
				ctx, tx, targetID,
			)
			if err != nil {
				return err
			}
			if observedAt.Before(lastSeenAt) {
				return fmt.Errorf(
					"repository observation at %s predates current route observation at %s",
					observedAt.Format(time.RFC3339Nano),
					lastSeenAt.Format(time.RFC3339Nano),
				)
			}
		}
		if sourceFound && targetFound && sourceID == targetID {
			repoID = sourceID
			if err := activateRepositoryRouteTx(
				ctx, tx, repoID, identity, observedAt,
			); err != nil {
				return err
			}
			return updateRepositoryDisplayTx(ctx, tx, repoID, identity)
		}

		if sourceFound {
			repoID = sourceID
			if err := deactivateCurrentRepositoryRouteTx(
				ctx, tx, repoID, observedAt,
			); err != nil {
				return err
			}
		} else if legacyID, legacyFound, legacyErr := legacyRepositoryIDByRouteTx(
			ctx, tx, identity,
		); legacyErr != nil {
			return legacyErr
		} else if legacyFound {
			// First verification of a route-only repository: adopt the
			// legacy row so rows linked to it (projects, merge requests,
			// workspaces) stay bound to the verified identity instead of
			// being stranded on an inactive duplicate.
			repoID = legacyID
			if _, err := tx.ExecContext(ctx, `
				UPDATE forge_repos
				SET platform_repo_id = ?, lifecycle_state = 'active'
				WHERE id = ?`,
				identity.PlatformRepoID, repoID,
			); err != nil {
				return fmt.Errorf("adopt legacy repository: %w", err)
			}
		} else {
			result, err := tx.ExecContext(ctx, `
				INSERT INTO forge_repos (
					platform, platform_host, platform_repo_id,
					owner, name, repo_path,
					owner_key, name_key, repo_path_key,
					lifecycle_state
				) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'active')`,
				identity.Platform, identity.PlatformHost,
				identity.PlatformRepoID,
				identity.Owner, identity.Name, identity.RepoPath,
				identity.OwnerKey, identity.NameKey, identity.RepoPathKey,
			)
			if err != nil {
				return fmt.Errorf("create canonical repository: %w", err)
			}
			repoID, err = result.LastInsertId()
			if err != nil {
				return fmt.Errorf("read reconciled repository id: %w", err)
			}
		}

		if targetFound && targetID != repoID {
			if err := deactivateCurrentRepositoryRouteTx(
				ctx, tx, targetID, observedAt,
			); err != nil {
				return err
			}
		}
		if err := activateRepositoryRouteTx(
			ctx, tx, repoID, identity, observedAt,
		); err != nil {
			return err
		}
		return updateRepositoryDisplayTx(ctx, tx, repoID, identity)
	})
	if err != nil {
		return nil, false, fmt.Errorf("reconcile repository observation: %w", err)
	}
	entry, err := d.getRepositoryByProviderID(
		ctx,
		identity.Platform,
		identity.PlatformHost,
		identity.PlatformRepoID,
	)
	if err != nil {
		return nil, false, err
	}
	if entry == nil {
		return nil, false, errors.New("reconciled repository is missing")
	}
	return entry, accepted, nil
}

// DeactivateRepositoryObservation records an authoritative provider absence.
// Provider errors and unavailable configuration must not call this method;
// they preserve the last verified route instead. An absence older than the
// repository's latest recorded route observation is ignored: the newer
// observation wins and the current entry is returned unchanged.
func (d *DB) DeactivateRepositoryObservation(
	ctx context.Context,
	platform string,
	platformHost string,
	platformRepoID string,
	observedAt time.Time,
) (*RepositoryCatalogEntry, error) {
	platform = strings.TrimSpace(platform)
	platformHost = strings.TrimSpace(platformHost)
	platformRepoID = strings.TrimSpace(platformRepoID)
	if platform == "" || platformHost == "" || platformRepoID == "" {
		return nil, errors.New(
			"repository deactivation requires platform, host, and provider id",
		)
	}
	identity := canonicalRepoIdentity(RepoIdentity{
		Platform:       platform,
		PlatformHost:   platformHost,
		PlatformRepoID: platformRepoID,
	})
	observedAt = canonicalUTCTime(observedAt)

	release := d.lockRepositoryReconciliationWrite()
	defer release()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	found := false
	err := d.Tx(ctx, func(tx *sql.Tx) error {
		repoID, repoFound, err := repositoryIDByProviderIDTx(ctx, tx, identity)
		if err != nil {
			return err
		}
		if !repoFound {
			return nil
		}
		found = true
		watermark, observed, err := repositoryObservationWatermarkTx(
			ctx, tx, repoID,
		)
		if err != nil {
			return err
		}
		if observed && observedAt.Before(watermark) {
			return nil
		}
		return deactivateCurrentRepositoryRouteTx(
			ctx, tx, repoID, observedAt,
		)
	})
	if err != nil {
		return nil, fmt.Errorf("deactivate repository observation: %w", err)
	}
	if !found {
		return nil, nil
	}
	return d.getRepositoryByProviderID(
		ctx,
		identity.Platform,
		identity.PlatformHost,
		identity.PlatformRepoID,
	)
}

func repositoryIDByProviderIDTx(
	ctx context.Context,
	tx *sql.Tx,
	identity RepoIdentity,
) (int64, bool, error) {
	var repoID int64
	err := tx.QueryRowContext(ctx, `
		SELECT id
		FROM forge_repos
		WHERE platform = ?
		  AND platform_host = ?
		  AND platform_repo_id = ?`,
		identity.Platform,
		identity.PlatformHost,
		identity.PlatformRepoID,
	).Scan(&repoID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("lookup repository by provider id: %w", err)
	}
	return repoID, true, nil
}

func currentRepositoryIDByRouteTx(
	ctx context.Context,
	tx *sql.Tx,
	identity RepoIdentity,
) (int64, bool, error) {
	var repoID int64
	err := tx.QueryRowContext(ctx, `
		SELECT repo_id
		FROM forge_repo_routes
		WHERE platform = ?
		  AND platform_host = ?
		  AND repo_path_key = ?
		  AND is_current = 1`,
		identity.Platform,
		identity.PlatformHost,
		identity.RepoPathKey,
	).Scan(&repoID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("lookup current repository route: %w", err)
	}
	return repoID, true, nil
}

// repositoryObservationWatermarkTx returns the newest observation recorded
// across all of a repository's routes, current or historical. Deactivations
// stamp last_seen_at, so the watermark also advances when a route is
// historicized.
func repositoryObservationWatermarkTx(
	ctx context.Context,
	tx *sql.Tx,
	repoID int64,
) (time.Time, bool, error) {
	var watermark time.Time
	err := tx.QueryRowContext(ctx, `
		SELECT last_seen_at
		FROM forge_repo_routes
		WHERE repo_id = ?
		ORDER BY last_seen_at DESC
		LIMIT 1`,
		repoID,
	).Scan(&watermark)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, fmt.Errorf(
			"lookup repository observation watermark: %w", err,
		)
	}
	return watermark.UTC(), true, nil
}

func currentRepositoryRouteLastSeenTx(
	ctx context.Context,
	tx *sql.Tx,
	repoID int64,
) (time.Time, bool, error) {
	var lastSeenAt time.Time
	err := tx.QueryRowContext(ctx, `
		SELECT last_seen_at
		FROM forge_repo_routes
		WHERE repo_id = ? AND is_current = 1`,
		repoID,
	).Scan(&lastSeenAt)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, fmt.Errorf(
			"lookup current repository route timestamp: %w", err,
		)
	}
	return lastSeenAt.UTC(), true, nil
}

func deactivateCurrentRepositoryRouteTx(
	ctx context.Context,
	tx *sql.Tx,
	repoID int64,
	observedAt time.Time,
) error {
	result, err := tx.ExecContext(ctx, `
		UPDATE forge_repo_routes
		SET is_current = 0, last_seen_at = ?
		WHERE repo_id = ? AND is_current = 1`, observedAt, repoID,
	)
	if err != nil {
		return fmt.Errorf("historicalize repository route: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("count historicized repository routes: %w", err)
	}
	if affected == 0 {
		// Already historical: advance the newest route so the repository
		// watermark still records this observation and older delayed
		// positives stay fenced out.
		if _, err := tx.ExecContext(ctx, `
			UPDATE forge_repo_routes
			SET last_seen_at = ?
			WHERE id = (
				SELECT id FROM forge_repo_routes
				WHERE repo_id = ?
				ORDER BY last_seen_at DESC, id DESC
				LIMIT 1
			)`, observedAt, repoID,
		); err != nil {
			return fmt.Errorf(
				"advance repository observation watermark: %w", err,
			)
		}
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE forge_repos SET lifecycle_state = 'inactive' WHERE id = ?`,
		repoID,
	); err != nil {
		return fmt.Errorf("deactivate repository: %w", err)
	}
	return nil
}

// legacyRepositoryIDByRouteTx finds a route-only repository (no provider ID)
// recorded at the given route.
func legacyRepositoryIDByRouteTx(
	ctx context.Context,
	tx *sql.Tx,
	identity RepoIdentity,
) (int64, bool, error) {
	var id int64
	err := tx.QueryRowContext(ctx, `
		SELECT id
		FROM forge_repos
		WHERE platform = ?
		  AND platform_host = ?
		  AND platform_repo_id = ''
		  AND repo_path_key = ?
		ORDER BY id
		LIMIT 1`,
		identity.Platform,
		identity.PlatformHost,
		identity.RepoPathKey,
	).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("lookup legacy repository route: %w", err)
	}
	return id, true, nil
}

func activateRepositoryRouteTx(
	ctx context.Context,
	tx *sql.Tx,
	repoID int64,
	identity RepoIdentity,
	observedAt time.Time,
) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO forge_repo_routes (
			repo_id, platform, platform_host,
			owner, name, repo_path, owner_key, name_key, repo_path_key,
			is_current, first_seen_at, last_seen_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?)
		ON CONFLICT(repo_id, platform, platform_host, repo_path_key)
		DO UPDATE SET
			owner = excluded.owner,
			name = excluded.name,
			repo_path = excluded.repo_path,
			owner_key = excluded.owner_key,
			name_key = excluded.name_key,
			is_current = 1,
			last_seen_at = excluded.last_seen_at`,
		repoID,
		identity.Platform,
		identity.PlatformHost,
		identity.Owner,
		identity.Name,
		identity.RepoPath,
		identity.OwnerKey,
		identity.NameKey,
		identity.RepoPathKey,
		observedAt,
		observedAt,
	)
	if err != nil {
		return fmt.Errorf("activate repository route: %w", err)
	}
	return nil
}

func updateRepositoryDisplayTx(
	ctx context.Context,
	tx *sql.Tx,
	repoID int64,
	identity RepoIdentity,
) error {
	if _, err := tx.ExecContext(ctx, `
		UPDATE forge_repos
		SET owner = ?, name = ?, repo_path = ?,
		    owner_key = ?, name_key = ?, repo_path_key = ?,
		    lifecycle_state = 'active'
		WHERE id = ?`,
		identity.Owner,
		identity.Name,
		identity.RepoPath,
		identity.OwnerKey,
		identity.NameKey,
		identity.RepoPathKey,
		repoID,
	); err != nil {
		return fmt.Errorf("activate canonical repository: %w", err)
	}
	return nil
}

func loadRepositoryCatalogEntry(
	ctx context.Context,
	q repositoryCatalogQueryer,
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
	routes, err := loadRepositoryRoutes(
		ctx,
		q,
		[]int64{entry.Repository.ID},
	)
	if err != nil {
		return nil, err
	}
	entry.Routes = routes[entry.Repository.ID]
	return &entry, nil
}

func scanRepositoryCatalogEntry(
	scanner repositoryCatalogScanner,
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

func loadRepositoryRoutes(
	ctx context.Context,
	q repositoryCatalogQueryer,
	repoIDs []int64,
) (map[int64][]RepositoryRoute, error) {
	routesByRepo := make(map[int64][]RepositoryRoute, len(repoIDs))
	if len(repoIDs) == 0 {
		return routesByRepo, nil
	}
	args := make([]any, len(repoIDs))
	for i, repoID := range repoIDs {
		args[i] = repoID
	}
	rows, err := q.QueryContext(ctx, `
		SELECT id, repo_id, platform, platform_host,
		       owner, name, repo_path, owner_key, name_key, repo_path_key,
		       is_current, first_seen_at, last_seen_at
		FROM forge_repo_routes
		WHERE repo_id IN (`+sqlPlaceholders(len(repoIDs))+`)
		ORDER BY first_seen_at, id`, args...)
	if err != nil {
		return nil, fmt.Errorf("load repository routes: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var route RepositoryRoute
		if err := rows.Scan(
			&route.ID,
			&route.RepoID,
			&route.Platform,
			&route.PlatformHost,
			&route.Owner,
			&route.Name,
			&route.RepoPath,
			&route.OwnerKey,
			&route.NameKey,
			&route.RepoPathKey,
			&route.Current,
			&route.FirstSeenAt,
			&route.LastSeenAt,
		); err != nil {
			return nil, fmt.Errorf("scan repository route: %w", err)
		}
		route.FirstSeenAt = route.FirstSeenAt.UTC()
		route.LastSeenAt = route.LastSeenAt.UTC()
		routesByRepo[route.RepoID] = append(routesByRepo[route.RepoID], route)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate repository routes: %w", err)
	}
	return routesByRepo, nil
}
