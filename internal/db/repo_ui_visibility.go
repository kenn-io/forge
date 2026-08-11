package db

import (
	"context"
	"fmt"
)

// SetRepoHiddenFromUI records or clears the operator preference that keeps a
// catalog repository out of interactive repository catalogs. The preference is
// keyed by the stable internal repository id, so provider-side renames leave
// it attached to the same repository and route reuse never inherits it.
// Hiding requires an existing catalog row; clearing is always a no-op when no
// preference exists.
func (d *DB) SetRepoHiddenFromUI(
	ctx context.Context, repoID int64, hidden bool,
) error {
	if hidden {
		if _, err := d.rw.ExecContext(ctx,
			`INSERT INTO forge_hidden_repos (repo_id) VALUES (?)
			 ON CONFLICT(repo_id) DO NOTHING`,
			repoID,
		); err != nil {
			return fmt.Errorf("hide repo %d from UI: %w", repoID, err)
		}
		return nil
	}
	if _, err := d.rw.ExecContext(ctx,
		`DELETE FROM forge_hidden_repos WHERE repo_id = ?`, repoID,
	); err != nil {
		return fmt.Errorf("show repo %d in UI: %w", repoID, err)
	}
	return nil
}

// HiddenRepos returns the catalog repositories with a hidden-from-UI
// preference. Only identity and route fields are populated; callers use them
// to exclude repositories from interactive catalogs and to mark configured
// entries as hidden on the settings surface.
func (d *DB) HiddenRepos(ctx context.Context) ([]Repo, error) {
	rows, err := d.ro.QueryContext(ctx,
		`SELECT r.id, r.platform, r.platform_host, r.platform_repo_id,
		        r.owner, r.name, r.repo_path
		 FROM forge_repos r
		 JOIN forge_hidden_repos h ON h.repo_id = r.id
		 ORDER BY r.id`,
	)
	if err != nil {
		return nil, fmt.Errorf("list hidden repos: %w", err)
	}
	defer rows.Close()

	var repos []Repo
	for rows.Next() {
		var r Repo
		if err := rows.Scan(
			&r.ID, &r.Platform, &r.PlatformHost, &r.PlatformRepoID,
			&r.Owner, &r.Name, &r.RepoPath,
		); err != nil {
			return nil, fmt.Errorf("scan hidden repo: %w", err)
		}
		repos = append(repos, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate hidden repos: %w", err)
	}
	return repos, nil
}
