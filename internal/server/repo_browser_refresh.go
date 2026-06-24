package server

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"go.kenn.io/middleman/internal/config"
	"go.kenn.io/middleman/internal/gitclone"
)

const defaultRepoBrowserRefreshInterval = 5 * time.Minute

func (s *Server) runRepoBrowserRefreshLoop(ctx context.Context) {
	if s.clones == nil {
		return
	}
	interval := s.repoBrowserRefreshEvery
	if interval <= 0 {
		interval = defaultRepoBrowserRefreshInterval
	}
	s.runRepoBrowserRefreshPass(ctx)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runRepoBrowserRefreshPass(ctx)
		}
	}
}

func repoBrowserRefreshIntervalForConfig(cfg *config.Config) time.Duration {
	if cfg == nil {
		return defaultRepoBrowserRefreshInterval
	}
	if interval := cfg.SyncDuration(); interval > 0 {
		return interval
	}
	return defaultRepoBrowserRefreshInterval
}

func (s *Server) seedRepoBrowserRefreshRepos(ctx context.Context) {
	if s.clones == nil || s.db == nil {
		return
	}
	repos, err := s.db.ListRepos(ctx)
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
		registered, err := s.clones.RegisterExistingRepoBrowserClone(ctx, repoRef)
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

func (s *Server) runRepoBrowserRefreshPass(ctx context.Context) {
	if s.clones == nil {
		return
	}
	slog.Debug("refreshing repo browser clones")
	s.clones.RefreshRepoBrowserClones(ctx)
}
