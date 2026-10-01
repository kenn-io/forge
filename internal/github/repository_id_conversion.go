package github

import (
	"context"
	"errors"
	"log/slog"

	"go.kenn.io/forge/internal/tokenauth"
	"go.kenn.io/forge/platform"
)

// ErrNoGitHubFetcher reports that no configured GitHub credential covers the
// requested host and repository, so GitHub cannot be asked about it.
var ErrNoGitHubFetcher = errors.New("no github credential for repository")

// ResolveRepositoryNodeID asks GitHub which integer repository ID a stored node
// ID names. owner/name only select the credential, including an App
// installation token scoped to owner. found is false when GitHub
// no longer resolves the node. Federation spokes have no GitHub credentials, so
// the hub answers this for them.
func (s *Syncer) ResolveRepositoryNodeID(
	ctx context.Context, host, owner, name, nodeID string,
) (int64, bool, error) {
	ctx = tokenauth.WithGitHubOwner(ctx, owner)
	fetcher := s.fetcherForContext(ctx, RepoRef{
		Platform: platform.KindGitHub, PlatformHost: host,
		Owner: owner, Name: name,
	})
	if fetcher == nil {
		return 0, false, ErrNoGitHubFetcher
	}
	return fetcher.RepositoryDatabaseID(ctx, nodeID)
}

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
		id, found, err := s.ResolveRepositoryNodeID(
			ctx, repo.PlatformHost, repo.Owner, repo.Name, repo.NodeID,
		)
		if errors.Is(err, ErrNoGitHubFetcher) {
			continue
		}
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
