package repobrowserapi

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/gitclone"
)

const defaultRepoBrowserRefreshInterval = 5 * time.Minute

func (h *Handler) RunRefreshLoop(ctx context.Context) {
	if h.clones == nil {
		return
	}
	interval := h.refreshEvery
	if interval <= 0 {
		interval = defaultRepoBrowserRefreshInterval
	}
	h.runRefreshPass(ctx)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			h.runRefreshPass(ctx)
		}
	}
}

func refreshIntervalForConfig(cfg *config.Config) time.Duration {
	if cfg == nil {
		return defaultRepoBrowserRefreshInterval
	}
	if interval := cfg.SyncDuration(); interval > 0 {
		return interval
	}
	return defaultRepoBrowserRefreshInterval
}

func (h *Handler) SeedRefreshRepos(ctx context.Context) {
	if h.clones == nil || h.resolver == nil {
		return
	}
	repos, err := h.repoBrowserRefreshRepos(ctx)
	if err != nil {
		slog.Warn("failed to seed repo browser refresh repos", "err", err)
		return
	}
	for _, repo := range repos {
		registered, err := h.clones.RegisterExistingRepoBrowserClone(ctx, repo)
		if err != nil {
			slog.Warn("failed to seed repo browser refresh repo",
				"provider", repo.Provider,
				"host", repo.Host,
				"repo", repo.RepoPath,
				"err", err)
			continue
		}
		if registered {
			slog.Debug("seeded repo browser refresh repo",
				"provider", repo.Provider,
				"host", repo.Host,
				"repo", repo.RepoPath)
		}
	}
}

func (h *Handler) runRefreshPass(ctx context.Context) {
	if h.clones == nil || h.resolver == nil {
		return
	}
	slog.Debug("refreshing repo browser clones")
	h.clones.RefreshRepoBrowserClones(ctx, h.authorizeRepoBrowserRefresh)
}

func (h *Handler) authorizeRepoBrowserRefresh(
	ctx context.Context,
	registered gitclone.RepoBrowserRepoRef,
) (gitclone.RepoBrowserRepoRef, func(), bool, error) {
	repo, release, err := h.resolver.LeaseActiveRepository(
		ctx,
		registered.RepoID,
	)
	if err != nil {
		return gitclone.RepoBrowserRepoRef{}, nil, false, err
	}
	if repo == nil || strings.TrimSpace(repo.CloneURL) == "" {
		if release != nil {
			release()
		}
		return gitclone.RepoBrowserRepoRef{}, nil, false, nil
	}
	return gitclone.RepoBrowserRepoRef{
		RepoID:    repo.ID,
		Provider:  repo.Platform,
		Host:      repo.PlatformHost,
		Owner:     repo.Owner,
		Name:      repo.Name,
		RepoPath:  repo.RepoPath,
		RemoteURL: repo.CloneURL,
	}, release, true, nil
}

func (h *Handler) repoBrowserRefreshRepos(
	ctx context.Context,
) ([]gitclone.RepoBrowserRepoRef, error) {
	repos, err := h.resolver.List(ctx)
	if err != nil {
		return nil, err
	}
	refs := make([]gitclone.RepoBrowserRepoRef, 0, len(repos))
	for _, repo := range repos {
		if strings.TrimSpace(repo.CloneURL) == "" {
			continue
		}
		refs = append(refs, gitclone.RepoBrowserRepoRef{
			RepoID:    repo.ID,
			Provider:  repo.Platform,
			Host:      repo.PlatformHost,
			Owner:     repo.Owner,
			Name:      repo.Name,
			RepoPath:  repo.RepoPath,
			RemoteURL: repo.CloneURL,
		})
	}
	return refs, nil
}
