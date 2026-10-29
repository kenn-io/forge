package github

import (
	"context"
	"log/slog"
	"time"

	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/platform"
	platformgithub "go.kenn.io/forge/platform/github"
)

// List validators outlive a restart so the first pass after one does not
// read every repository's open lists in full. Without a validator, an open
// list reads as changed, which for most repositories means a full GraphQL
// refetch of every open pull request.
//
// A validator is saved only after its repository synced without error, so
// a saved one always describes a list the database already holds. One a
// failed or interrupted sync received is never saved. A seed keeps its
// original time and vouches no longer than etagTTL, as in one process.

// seedListETags restores the list validators saved before a restart.
func (s *Syncer) seedListETags(ctx context.Context) {
	saved, err := s.db.ListRepoListETags(ctx, string(platform.KindGitHub))
	if err != nil {
		slog.Warn("load saved list validators failed", "err", err)
		return
	}
	type repoKey struct{ host, owner, name string }
	byRepo := make(map[repoKey][]platformgithub.ListETag)
	var order []repoKey
	for _, etag := range saved {
		if time.Since(etag.CachedAt) >= etagTTL {
			continue
		}
		key := repoKey{etag.PlatformHost, etag.Owner, etag.Name}
		if _, ok := byRepo[key]; !ok {
			order = append(order, key)
		}
		byRepo[key] = append(byRepo[key], platformgithub.ListETag{
			Resource: etag.Resource, ETag: etag.ETag, Detector: etag.Detector, CachedAt: etag.CachedAt,
		})
	}
	for _, key := range order {
		repo := RepoRef{Platform: platform.KindGitHub, PlatformHost: key.host, Owner: key.owner, Name: key.name}
		if store := s.listETagStoreFor(repo); store != nil {
			store.SeedListETags(key.owner, key.name, byRepo[key])
		}
	}
}

// saveListETags records a repository's list validators after it synced
// without error.
func (s *Syncer) saveListETags(ctx context.Context, repo RepoRef) {
	store := s.listETagStoreFor(repo)
	if store == nil {
		return
	}
	exported := store.ExportListETags(repo.Owner, repo.Name)
	etags := make([]db.RepoListETag, 0, len(exported))
	for _, etag := range exported {
		etags = append(etags, db.RepoListETag{
			Resource: etag.Resource, ETag: etag.ETag, Detector: etag.Detector, CachedAt: etag.CachedAt,
		})
	}
	if err := s.db.ReplaceRepoListETags(
		ctx, string(platform.KindGitHub), repoHost(repo), repo.Owner, repo.Name, etags,
	); err != nil {
		slog.Warn("save list validators failed", "repo", repo.Owner+"/"+repo.Name, "err", err)
	}
}

func (s *Syncer) listETagStoreFor(repo RepoRef) platformgithub.ListETagStore {
	if repoPlatform(repo) != platform.KindGitHub {
		return nil
	}
	client, ok := s.optionalGitHubClientFor(repo)
	if !ok {
		return nil
	}
	store, _ := client.(platformgithub.ListETagStore)
	return store
}
