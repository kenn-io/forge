package repobrowserapi

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"go.kenn.io/middleman/internal/config"
	"go.kenn.io/middleman/internal/gitclone"
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
	repos, err := h.resolver.List(ctx)
	if err != nil {
		slog.Warn("failed to seed repo browser refresh repos", "err", err)
		return
	}
	for _, repo := range repos {
		if strings.TrimSpace(repo.CloneURL) == "" {
			continue
		}
		repoRef := gitclone.RepoBrowserRepoRef{
			Provider:  repo.Platform,
			Host:      repo.PlatformHost,
			Owner:     repo.Owner,
			Name:      repo.Name,
			RepoPath:  repo.RepoPath,
			RemoteURL: repo.CloneURL,
		}
		registered, err := h.clones.RegisterExistingRepoBrowserClone(ctx, repoRef)
		if err != nil {
			slog.Warn("failed to seed repo browser refresh repo",
				"provider", repo.Platform,
				"host", repo.PlatformHost,
				"repo", repo.RepoPath,
				"err", err)
			continue
		}
		if registered {
			slog.Debug("seeded repo browser refresh repo",
				"provider", repo.Platform,
				"host", repo.PlatformHost,
				"repo", repo.RepoPath)
		}
	}
}

func (h *Handler) runRefreshPass(ctx context.Context) {
	if h.clones == nil {
		return
	}
	slog.Debug("refreshing repo browser clones")
	h.clones.RefreshRepoBrowserClones(ctx)
}
