package db

import (
	"context"
	"fmt"
)

type UntrackedRepoOpen struct {
	Provider     string `json:"provider"`
	Host         string `json:"host"`
	Owner        string `json:"owner"`
	Name         string `json:"name"`
	Count        int    `json:"count"`
	LastOpenedAt string `json:"last_opened_at"`
}

func (d *DB) RecordUntrackedRepoOpen(ctx context.Context, repo UntrackedRepoOpen) error {
	_, err := d.execContext(ctx, `INSERT INTO untracked_repo_opens (provider, host, owner, name, open_count, last_opened_at)
        VALUES (?, ?, ?, ?, 1, strftime('%Y-%m-%dT%H:%M:%fZ','now'))
        ON CONFLICT (provider, host, owner, name) DO UPDATE SET open_count = open_count + 1, last_opened_at = excluded.last_opened_at`,
		repo.Provider, repo.Host, repo.Owner, repo.Name)
	if err != nil {
		return fmt.Errorf("record untracked repository open: %w", err)
	}
	return nil
}

func (d *DB) ListUntrackedRepoOpens(ctx context.Context) ([]UntrackedRepoOpen, error) {
	rows, err := d.roQueryContext(ctx, `SELECT provider, host, owner, name, open_count, last_opened_at FROM untracked_repo_opens ORDER BY open_count DESC, last_opened_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list untracked repository opens: %w", err)
	}
	defer rows.Close()
	repos := []UntrackedRepoOpen{}
	for rows.Next() {
		var repo UntrackedRepoOpen
		if err := rows.Scan(&repo.Provider, &repo.Host, &repo.Owner, &repo.Name, &repo.Count, &repo.LastOpenedAt); err != nil {
			return nil, err
		}
		repos = append(repos, repo)
	}
	return repos, rows.Err()
}
