package github

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"go.kenn.io/forge/internal/platformdb"

	"go.kenn.io/forge/internal/archive"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/platform"
)

// excludeArchivedRepos drops provider-archived repositories from a live sync
// pass. Archived repos stay tracked in s.repos for archive collection and
// API surfaces; only live polling skips them.
// reconcileArchivedRepos refreshes provider metadata for archived
// tracked refs so an upstream unarchive is observed during normal
// sync passes. Refs the provider still reports archived (or whose
// metadata refresh fails) stay excluded from the pass; refs that
// resolved unarchived rejoin it with fresh metadata. Refs in
// credential buckets the pass would not dispatch — throttled,
// reserve-exhausted, or deferred by next-sync-after — are deferred
// without a provider call: the refresh must not spend sync budget a
// live repository's dispatch would be denied. Eligibility is read
// before the pass refreshes rate-limit snapshots, so a gate that has
// recovered upstream defers the refresh by at most one pass.
func (s *Syncer) reconcileArchivedRepos(
	ctx context.Context,
	repos []RepoRef,
	eligibility map[string]bool,
	bypassRepos []RepoRef,
	bypassEligibility map[string]bool,
) []RepoRef {
	ctx = WithSyncSpendKind(ctx, SyncSpendDiscovery)
	live := make([]RepoRef, 0, len(repos))
	skipped := make([]string, 0)
	for _, repo := range repos {
		if !repo.Archived {
			live = append(live, repo)
			continue
		}
		if ctx.Err() != nil {
			skipped = append(skipped, repo.Owner+"/"+repo.Name)
			continue
		}
		bucket, err := s.bucketKeyForRepo(repo, false)
		eligible := eligibility[bucket]
		if !eligible && repoMatchesAnyIntent(repo, bypassRepos) {
			eligible = bypassEligibility[bucket]
		}
		if err != nil || !eligible {
			skipped = append(skipped, repo.Owner+"/"+repo.Name)
			continue
		}
		// Register provider work so an admitted archive request on the
		// same credential is preempted instead of overlapping the
		// refresh, matching the coordination live repo syncs get.
		release := s.beginProviderWork(ctx, bucket, archive.PriorityNormalIndex)
		resolved, _, _, err := s.reconcileRepoIdentityObservation(ctx, repo, true)
		release()
		if err != nil {
			slog.Debug("archived repo metadata refresh failed",
				"repo", repo.Owner+"/"+repo.Name, "err", err,
			)
			skipped = append(skipped, repo.Owner+"/"+repo.Name)
			continue
		}
		if resolved.Archived {
			skipped = append(skipped, repo.Owner+"/"+repo.Name)
			continue
		}
		slog.Info("repo unarchived upstream; resuming live sync",
			"repo", resolved.Owner+"/"+resolved.Name,
		)
		live = append(live, resolved)
	}
	if len(skipped) > 0 {
		slog.Debug("skipping archived repos in live sync", "repos", skipped)
	}
	return live
}

func (s *Syncer) syncRepoIdentity(
	ctx context.Context,
	repo RepoRef,
) (db.RepoIdentity, *platform.Repository, error) {
	identity := platformdb.DBRepoIdentity(platformRepoRef(repo))
	reader, err := s.clients.RepositoryReader(repoPlatform(repo), repoHost(repo))
	if err != nil {
		if !repo.Key.IsZero() && errors.Is(err, platform.ErrUnsupportedCapability) {
			return identity, nil, nil
		}
		return db.RepoIdentity{}, nil, err
	}
	// A ref that already carries its ID resolves by ID, following renames.
	resolved, err := reader.GetRepository(ctx, platformRepoRef(repo))
	if err != nil {
		s.observeRepoFailure(ctx, repo, err)
		return db.RepoIdentity{}, nil, err
	}
	identity = platformdb.DBRepositoryIdentity(resolved)
	if !resolved.Ref.Identity().Valid() {
		err := errors.New("provider returned no repo id")
		s.observeRepoFailure(ctx, repo, err)
		return db.RepoIdentity{}, nil, err
	}
	return identity, &resolved, nil
}

func (s *Syncer) reconcileRepoIdentityObservation(
	ctx context.Context,
	repo RepoRef,
	essentialIdentity bool,
) (RepoRef, int64, *platform.Repository, error) {
	previousID := int64(0)
	previous, err := s.db.ResolveActiveRepositoryRoute(
		ctx, platformdb.DBRepoIdentity(platformRepoRef(repo)),
	)
	if err != nil {
		return RepoRef{}, 0, nil, err
	}
	if previous != nil {
		previousID = previous.Repository.ID
	}
	// Only the identity read may use the essential reserve. Archive lifecycle
	// reconciliation and any repository reads it seeds keep the caller's budget.
	identityCtx := ctx
	if essentialIdentity {
		identityCtx = WithEssentialSyncBudget(ctx)
	}
	identity, resolved, err := s.syncRepoIdentity(identityCtx, repo)
	if err != nil {
		return RepoRef{}, 0, nil, err
	}
	entry, err := s.db.ObserveRepository(ctx, identity)
	if err != nil {
		return RepoRef{}, 0, nil, err
	}
	authoritative := repoRefFromCatalog(repo, entry.Repository, resolved)
	if published, ok := s.publishResolvedRepository(
		repo, authoritative, resolved != nil,
	); ok {
		// The publication may have kept a newer tracked archived flip
		// over this snapshot's metadata; callers deciding whether to
		// keep syncing must see the value that was actually published.
		authoritative = published
	}
	if err := s.reconcileArchiveRepositoryIfNeeded(
		ctx, previousID, entry.Repository.ID,
	); err != nil {
		return RepoRef{}, 0, nil, err
	}
	return authoritative, entry.Repository.ID, resolved, nil
}

func (s *Syncer) reconcileArchiveRepositoryIfNeeded(
	ctx context.Context,
	previousID int64,
	repoID int64,
) error {
	if s.archiveLifecycle == nil || !s.AutomaticSyncEnabled() {
		return nil
	}
	needsReconcile := previousID != 0 && previousID != repoID
	if !needsReconcile {
		states, err := s.db.ListArchiveRepoStates(ctx, []int64{repoID})
		if err != nil {
			return fmt.Errorf("inspect archive repository state: %w", err)
		}
		needsReconcile = len(states) == 0
	}
	if !needsReconcile {
		return nil
	}
	s.reposMu.Lock()
	tracked := slices.Clone(s.repos)
	s.reposMu.Unlock()
	refs := make([]platform.RepoRef, 0, len(tracked))
	for _, trackedRepo := range tracked {
		refs = append(refs, platformRepoRef(trackedRepo))
	}
	if _, err := s.archiveLifecycle.EnsureConfigured(ctx, refs); err != nil {
		return fmt.Errorf("reconcile archive repository replacement: %w", err)
	}
	s.WakeArchive()
	return nil
}

func repoRefFromCatalog(previous RepoRef, stored db.Repo, resolved *platform.Repository) RepoRef {
	repo := RepoRef{
		Platform:      platform.Kind(stored.Platform),
		RepoID:        stored.ID,
		Owner:         stored.Owner,
		Name:          stored.Name,
		PlatformHost:  stored.PlatformHost,
		RepoPath:      stored.RepoPath,
		Key:           stored.Key,
		WebURL:        stored.WebURL,
		CloneURL:      stored.CloneURL,
		DefaultBranch: stored.DefaultBranch,
		// The repo catalog does not record archived state or config-entry
		// provenance; without a fresh provider resolve, the previously
		// tracked values stand.
		Archived:           previous.Archived,
		ConfiguredRepoPath: previous.ConfiguredRepoPath,
	}
	if repo.Key.IsZero() {
		repo.Key = previous.Key
	}
	if repo.WebURL == "" {
		repo.WebURL = previous.WebURL
	}
	if repo.CloneURL == "" {
		repo.CloneURL = previous.CloneURL
	}
	if repo.DefaultBranch == "" {
		repo.DefaultBranch = previous.DefaultBranch
	}
	if resolved == nil {
		return repo
	}
	repo.Archived = resolved.Archived
	repo.Key = resolved.Ref.Key
	if resolved.WebURL != "" {
		repo.WebURL = resolved.WebURL
	} else if resolved.Ref.WebURL != "" {
		repo.WebURL = resolved.Ref.WebURL
	}
	if resolved.CloneURL != "" {
		repo.CloneURL = resolved.CloneURL
	} else if resolved.Ref.CloneURL != "" {
		repo.CloneURL = resolved.Ref.CloneURL
	}
	if resolved.DefaultBranch != "" {
		repo.DefaultBranch = resolved.DefaultBranch
	} else if resolved.Ref.DefaultBranch != "" {
		repo.DefaultBranch = resolved.Ref.DefaultBranch
	}
	return repo
}

func (s *Syncer) publishResolvedRepository(
	previous, resolved RepoRef, archivedAuthoritative bool,
) (RepoRef, bool) {
	s.clearDisplacedCredentialAlias(resolved)
	s.aliasRenamedCredentialRoute(previous, resolved)
	s.moveRepoSyncProblem(previous, resolved)
	defer s.refreshRepoProblemStatus()
	s.reposMu.Lock()
	defer s.reposMu.Unlock()
	i, ok := s.trackedRepoSlotLocked(previous, resolved)
	if !ok {
		return resolved, false
	}
	// The snapshot comparison below only means anything when the
	// publication concerns the repository the snapshot named: differing
	// snapshot and resolved ids mean the data belongs to a route
	// successor — whether it landed on the successor's own entry or is
	// displacing the snapshot's — and the snapshot's archived flag says
	// nothing about it. A conflicting slot id is cross-identity even
	// when the snapshot carries no id: the slot's occupant is not the
	// repository the provider response describes.
	slotID := s.repos[i].Key
	previousID := previous.Key
	resolvedID := resolved.Key
	crossIdentity := !resolvedID.IsZero() &&
		((!previousID.IsZero() && resolvedID != previousID) ||
			(!slotID.IsZero() && resolvedID != slotID))
	sameIdentity := !crossIdentity &&
		(slotID.IsZero() || previousID.IsZero() || slotID == previousID)
	if sameIdentity && s.repos[i].Archived != previous.Archived {
		// A concurrent resolution flipped archived state after this
		// operation snapshotted the ref. The in-flight provider
		// response cannot be ordered against that flip, so the newer
		// tracked value stands even over fresh provider metadata.
		resolved.Archived = s.repos[i].Archived
	} else if !archivedAuthoritative {
		// Without fresh provider metadata the archived flag was
		// reconstructed from the operation's snapshot, which may
		// predate a newer flip on the tracked ref. The current
		// tracked state stays authoritative.
		resolved.Archived = s.repos[i].Archived
	}
	// Config-entry provenance is authored only by configuration
	// resolution; a publication built from an older snapshot must
	// not overwrite a value a concurrent reload just updated.
	resolved.ConfiguredRepoPath = s.repos[i].ConfiguredRepoPath
	s.repos[i] = resolved
	return resolved, true
}

// trackedRepoSlotLocked locates the tracked entry a publication should land
// on: stable provider identity first — a renamed route must still find its
// repository — then the route key, rejected when the ids conflict because
// that means the route was reused by another repository whose tracked state
// a stale publication must not overwrite. The resolved id outranks the
// snapshot id: the provider response says whose data this is, so a lookup
// keyed by a reused route lands on the successor, never on the repository
// the snapshot named. Callers hold reposMu.
func (s *Syncer) trackedRepoSlotLocked(previous, resolved RepoRef) (int, bool) {
	previousID := previous.Key
	resolvedID := resolved.Key
	lookupID := resolvedID
	if lookupID.IsZero() {
		lookupID = previousID
	}
	if !lookupID.IsZero() {
		for i := range s.repos {
			if repoPlatform(s.repos[i]) == repoPlatform(previous) &&
				strings.EqualFold(repoHost(s.repos[i]), repoHost(previous)) &&
				s.repos[i].Key == lookupID {
				return i, true
			}
		}
	}
	// Route fallback: landing on the entry this operation snapshotted is
	// legitimate even under a new resolved identity — a configured route
	// reused by a replacement repository displaces its tracked occupant,
	// and the archive lifecycle pauses the old repository. Landing on an
	// entry whose id conflicts with the snapshot is not: that entry is a
	// different repository this publication knows nothing about.
	for i := range s.repos {
		if repoPriorityKey(s.repos[i]) != repoPriorityKey(previous) {
			continue
		}
		trackedID := s.repos[i].Key
		if !trackedID.IsZero() && !previousID.IsZero() && trackedID != previousID {
			continue
		}
		return i, true
	}
	return 0, false
}

// aliasRenamedCredentialRoute keeps GitHub credential selection on the
// configured repository identity when reconciliation publishes a renamed or
// transferred route. Provider APIs receive the resolved owner/name; the
// credential router resolves it back to the configured route.
func (s *Syncer) aliasRenamedCredentialRoute(previous, resolved RepoRef) {
	if repoPlatform(resolved) != platform.KindGitHub {
		return
	}
	if strings.EqualFold(previous.Owner, resolved.Owner) &&
		strings.EqualFold(previous.Name, resolved.Name) {
		return
	}
	router := s.routers[repoHost(resolved)]
	if router == nil {
		return
	}
	router.RegisterRepoCredentialAlias(resolved.Owner, resolved.Name, RouteKey{
		Host:  repoHost(previous),
		Owner: previous.Owner,
		Name:  previous.Name,
	}, resolved.Key)
}

// clearDisplacedCredentialAlias drops a credential alias for the resolved
// repository's route when a different repository recorded it — a replacement
// repository on a reused route must not inherit the displaced repository's
// credential.
func (s *Syncer) clearDisplacedCredentialAlias(resolved RepoRef) {
	if repoPlatform(resolved) != platform.KindGitHub {
		return
	}
	router := s.routers[repoHost(resolved)]
	if router == nil {
		return
	}
	router.ClearDisplacedRepoCredentialAlias(
		resolved.Owner, resolved.Name, resolved.Key,
	)
}
