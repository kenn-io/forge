package github

import (
	"context"
	"log/slog"
	"net/url"
	"strings"
	"sync"
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
// A repository sync saves only the validators its own list reads answered
// with, and only after it succeeded, so a saved one always describes a list
// the database already holds. A validator another, overlapping sync stored
// in the shared cache is never saved for it. A seed keeps its original time
// and vouches no longer than etagTTL, as in one process.

type listETagObservationsKey struct{}

// observedListETag is the validator one list read answered with.
// fromFullRead marks a validator whose list this sync read and applied; one
// seen only through a 304 vouches for a list some other read applied.
type observedListETag struct {
	etag         platformgithub.ListETag
	fromFullRead bool
}

// listETagObservations collects, per list resource, the validator one
// repository sync's list reads answered with; nil means the list was read
// with no validator to keep, or was left incomplete.
type listETagObservations struct {
	mu    sync.Mutex
	etags map[string]*observedListETag
}

func withListETagObservations(ctx context.Context) (context.Context, *listETagObservations) {
	observed := &listETagObservations{etags: make(map[string]*observedListETag)}
	return context.WithValue(ctx, listETagObservationsKey{}, observed), observed
}

func (o *listETagObservations) set(u *url.URL, etag *observedListETag) {
	_, _, resource, ok := listResource(u)
	if !ok {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if etag != nil {
		etag.etag.Resource = resource
	}
	o.etags[resource] = etag
}

// observeListETag records the validator a list read answered with, for the
// repository sync that made the read.
func observeListETag(ctx context.Context, u *url.URL, entry etagEntry, fromFullRead bool) {
	observed, ok := ctx.Value(listETagObservationsKey{}).(*listETagObservations)
	if !ok {
		return
	}
	var etag *observedListETag
	switch {
	case entry.paginated && entry.detectorETag != "":
		etag = &observedListETag{etag: platformgithub.ListETag{
			ETag: entry.detectorETag, Detector: true, CachedAt: entry.cachedAt,
		}, fromFullRead: fromFullRead}
	case !entry.paginated && entry.etag != "":
		etag = &observedListETag{etag: platformgithub.ListETag{
			ETag: entry.etag, CachedAt: entry.cachedAt,
		}, fromFullRead: fromFullRead}
	}
	observed.set(u, etag)
}

// observeIncompleteList drops a list's validator from the sync that read
// it when a later page failed, so the sync never saves it.
func observeIncompleteList(ctx context.Context, u *url.URL) {
	if observed, ok := ctx.Value(listETagObservationsKey{}).(*listETagObservations); ok {
		observed.set(u, nil)
	}
}

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
	// Native stack confirmations live in memory. A restored pull request
	// validator answering 304 would leave a restarted process with none, so
	// branch inference would own every stack until the list is read again.
	nativeStacks := s.PrefersGitHubNativeStacks()
	for _, etag := range saved {
		if time.Since(etag.CachedAt) >= etagTTL ||
			(nativeStacks && strings.HasPrefix(etag.Resource, "pulls?")) {
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

// saveListETags saves the validators one repository sync answered with,
// after it succeeded. A validator the sync saw only through a 304 is kept
// only if it is already saved: a 304 can answer with a validator another
// sync stored but has not applied yet.
func (s *Syncer) saveListETags(ctx context.Context, repo RepoRef, observed *listETagObservations) {
	if repoPlatform(repo) != platform.KindGitHub {
		return
	}
	host := repoHost(repo)
	saved, err := s.db.ListRepoListETagsForRepo(ctx, string(platform.KindGitHub), host, repo.Owner, repo.Name)
	if err != nil {
		slog.Warn("load saved list validators failed", "repo", repo.Owner+"/"+repo.Name, "err", err)
		return
	}
	committed := make(map[string]string, len(saved))
	for _, etag := range saved {
		committed[etag.Resource] = etag.ETag
	}
	observed.mu.Lock()
	etags := make([]db.RepoListETag, 0, len(observed.etags))
	for resource, seen := range observed.etags {
		if seen == nil || (!seen.fromFullRead && committed[resource] != seen.etag.ETag) {
			continue
		}
		etags = append(etags, db.RepoListETag{
			Resource: resource, ETag: seen.etag.ETag, Detector: seen.etag.Detector, CachedAt: seen.etag.CachedAt,
		})
	}
	observed.mu.Unlock()
	if err := s.db.ReplaceRepoListETags(
		ctx, string(platform.KindGitHub), host, repo.Owner, repo.Name, etags,
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
