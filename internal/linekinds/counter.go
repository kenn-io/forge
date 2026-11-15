// Package linekinds counts each pull request's changed lines by kind of file,
// so pull request lists can say how much of a change is code.
package linekinds

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/gitclone"
	"go.kenn.io/forge/platform"
)

const (
	// interval is how long Run waits for a wake before its next pass.
	interval = 10 * time.Minute
	batch    = 100
)

// Counter counts merge requests' diffs from the local clones that sync keeps.
type Counter struct {
	db     *db.DB
	clones *gitclone.Manager
	wake   chan struct{}
}

func New(database *db.DB, clones *gitclone.Manager) *Counter {
	return &Counter{db: database, clones: clones, wake: make(chan struct{}, 1)}
}

// Wake asks Run to start a pass now. It never blocks.
func (c *Counter) Wake() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// Run counts until ctx ends: once now, then after every wake or interval.
func (c *Counter) Run(ctx context.Context) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := c.Pass(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("count pull request line kinds failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-c.wake:
		}
	}
}

// Pass counts every merge request whose current diff has no counts. A diff
// the clone cannot read is left for a later pass, since the next sync may
// fetch its commits.
func (c *Counter) Pass(ctx context.Context) error {
	repos, err := c.db.ListRepos(ctx)
	if err != nil {
		return err
	}
	byID := make(map[int64]db.Repo, len(repos))
	for _, repo := range repos {
		byID[repo.ID] = repo
	}
	var after int64
	for {
		targets, err := c.db.ListMergeRequestsWithoutLineKinds(ctx, after, batch)
		if err != nil || len(targets) == 0 {
			return err
		}
		for _, target := range targets {
			after = target.ID
			repo, ok := byID[target.RepoID]
			if !ok {
				continue
			}
			identity := platform.RepositoryIdentity{Provider: repo.Platform, PlatformHost: repo.PlatformHost, Key: repo.Key}.Canonical()
			files, err := c.clones.DiffFiles(
				gitclone.WithRepositoryIdentity(ctx, identity),
				repo.Platform, repo.PlatformHost, repo.Owner, repo.Name, target.MergeBaseSHA, target.HeadSHA,
			)
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if !errors.Is(err, gitclone.ErrNotFound) {
					slog.Warn("count pull request line kinds", "repo", repo.FullName(), "merge_request_id", target.ID, "err", err)
				}
				continue
			}
			if err := c.db.SetMergeRequestLineKinds(ctx, target.ID, target.MergeBaseSHA, target.HeadSHA, gitclone.CountLineKinds(files), time.Now()); err != nil {
				return err
			}
		}
	}
}
