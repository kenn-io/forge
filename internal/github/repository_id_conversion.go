package github

import (
	"context"
	"log/slog"

	"go.kenn.io/forge/platform"
)

// convertPendingGitHubRepositories replaces the GitHub node IDs stored before
// repository identity became the integer ID. GitHub decides which repository
// each node ID names, so history stays attached to it. It runs at the start
// of every sync pass until nothing is pending; until a host converts, the
// catalog refuses new GitHub observations for it.
func (s *Syncer) convertPendingGitHubRepositories(ctx context.Context) {
	pending, err := s.db.ListPendingGitHubRepositories(ctx)
	if err != nil {
		slog.Warn("list pending github repository conversions", "err", err)
		return
	}
	for _, repo := range pending {
		if ctx.Err() != nil {
			return
		}
		fetcher := s.fetcherForContext(ctx, RepoRef{
			Platform: platform.KindGitHub, PlatformHost: repo.PlatformHost,
			Owner: repo.Owner, Name: repo.Name,
		})
		if fetcher == nil {
			continue
		}
		id, found, err := fetcher.RepositoryDatabaseID(ctx, repo.NodeID)
		if err != nil {
			slog.Warn("resolve github repository id",
				"repo", repo.Owner+"/"+repo.Name, "host", repo.PlatformHost, "err", err,
			)
			continue
		}
		if !found {
			slog.Warn("github no longer resolves stored repository; keeping it inactive",
				"repo", repo.Owner+"/"+repo.Name, "host", repo.PlatformHost,
			)
		}
		if err := s.db.CompleteGitHubRepositoryConversion(ctx, repo.RepoID, id); err != nil {
			slog.Warn("record github repository id",
				"repo", repo.Owner+"/"+repo.Name, "host", repo.PlatformHost, "err", err,
			)
		}
	}
}
