package github

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"go.kenn.io/forge/internal/platformdb"

	"go.kenn.io/forge/internal/archive"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/platform"
	platformgithub "go.kenn.io/forge/platform/github"
)

// markClosedLinkedNotificationsDone sweeps every active notification for a
// closed or merged linked item. Per-item sync paths use the scoped variants
// below because they already know which item changed.
func (s *Syncer) markClosedLinkedNotificationsDone(ctx context.Context) error {
	if err := s.db.MarkClosedLinkedNotificationsDone(ctx, time.Now().UTC()); err != nil {
		return fmt.Errorf("mark closed linked notifications done: %w", err)
	}
	return nil
}

func (s *Syncer) markClosedLinkedPRNotificationsDone(ctx context.Context, repoID int64, number int) error {
	if err := s.db.MarkClosedLinkedPRNotificationsDone(ctx, time.Now().UTC(), repoID, number); err != nil {
		return fmt.Errorf("mark closed linked notifications done for PR #%d: %w", number, err)
	}
	return nil
}

func (s *Syncer) markClosedLinkedIssueNotificationsDone(ctx context.Context, repoID int64, number int) error {
	if err := s.db.MarkClosedLinkedIssueNotificationsDone(ctx, time.Now().UTC(), repoID, number); err != nil {
		return fmt.Errorf("mark closed linked notifications done for issue #%d: %w", number, err)
	}
	return nil
}

// syncRepo syncs one repository outside a pass: open PRs, timeline events, and
// stale closures. Manual and relay refreshes record the outcome like a pass
// does, so a direct success clears a failure warning while sync is paused.
func (s *Syncer) syncRepo(ctx context.Context, repo RepoRef) error {
	resolved := repo
	err := s.syncRepoResolving(ctx, repo, &resolved)
	if ctx.Err() == nil {
		s.recordRepoSync(resolved, err)
		if err == nil {
			s.saveListETags(ctx, resolved)
		}
	}
	return err
}

// syncRepoResolving is syncRepo that also stores the provider-resolved ref in
// resolved once identity resolution succeeds. The pass snapshot can name a
// route the provider has since renamed.
func (s *Syncer) syncRepoResolving(ctx context.Context, repo RepoRef, resolved *RepoRef) error {
	ctx = WithSyncSpendKind(ctx, SyncSpendDiscovery)
	bucket, err := s.bucketKeyForRepo(repo, false)
	if err != nil {
		return fmt.Errorf("resolve sync credential route for %s/%s: %w", repo.Owner, repo.Name, err)
	}
	releaseProviderWork := s.beginProviderWork(ctx, bucket, archive.PriorityNormalIndex)
	defer releaseProviderWork()
	// Split-auth repository sync also issues a viewer-permission request on
	// the write identity; register that principal too so an archive sharing
	// the write PAT is preempted rather than running concurrently.
	if writeIdentity, idErr := s.identityForRepo(repo, true); idErr == nil &&
		writeIdentity.Principal != "" {
		writeBucket := RateBucketKey(
			string(repoPlatform(repo)), writeIdentity.Host, writeIdentity.Principal,
		)
		if writeBucket != bucket {
			defer s.beginProviderWork(ctx, writeBucket, archive.PriorityNormalIndex)()
		}
	}

	resolvedRef, repoID, resolvedRepo, err := s.reconcileRepoIdentityObservation(ctx, repo, true)
	if err != nil {
		return fmt.Errorf("resolve repo identity %s/%s: %w", repo.Owner, repo.Name, err)
	}
	repo = resolvedRef
	if resolved != nil {
		*resolved = repo
	}
	if repo.Archived {
		// Identity resolution already published the archived flag, so
		// the repo drops out of future passes; stop before any live
		// clone, overview, or item syncing touches the archived repo.
		slog.Info("repo archived upstream; skipping live sync",
			"repo", repo.Owner+"/"+repo.Name,
		)
		return nil
	}
	ctx = withCloneRepositoryIdentity(ctx, repo)

	// A failed settings commit aborts the sync: a new repository row must
	// not have items indexed under it while it still advertises the
	// permissive schema defaults.
	if err := s.refreshRepoSettings(ctx, repo, repoID, resolvedRepo); err != nil {
		err = fmt.Errorf(
			"refresh repo settings for %s/%s: %w", repo.Owner, repo.Name, err,
		)
		s.recordAbortedRepoSync(ctx, repo, repoID, err)
		return err
	}

	if err := s.db.UpdateRepoSyncStarted(ctx, repoID, time.Now().UTC()); err != nil {
		return fmt.Errorf("mark sync started for %s/%s: %w", repo.Owner, repo.Name, err)
	}

	// Fetch bare clone before PR data so refs are available for merge-base.
	cloneFetchOK := false
	defaultBranch := s.defaultBranchForActivity(ctx, repoID, repo)
	var previousTip *db.BranchTip
	if defaultBranch != "" {
		tip, err := s.db.GetBranchTip(ctx, repoID, defaultBranch)
		if err != nil {
			slog.Warn("get default branch tip failed",
				"repo", repo.Owner+"/"+repo.Name,
				"branch", defaultBranch,
				"err", err,
			)
		} else {
			previousTip = tip
		}
	}
	if s.clones != nil {
		if err := s.ensureClone(ctx, repo); err != nil {
			if !isReportedGitAccessFailure(err) {
				slog.Warn("bare clone fetch failed",
					"repo", repo.Owner+"/"+repo.Name, "err", err,
				)
			}
		} else {
			cloneFetchOK = true
			s.syncDefaultBranchActivity(ctx, repo, repoID, defaultBranch, previousTip)
		}
	}

	if client, ok := s.optionalGitHubClientFor(repo); ok {
		s.syncRepoOverview(ctx, client, repo, repoID, cloneFetchOK)
	} else {
		s.syncProviderRepoOverview(ctx, repo, repoID, cloneFetchOK)
	}

	s.syncRepoLabelCatalog(ctx, repo, repoID)

	syncErr := s.indexSyncRepo(ctx, repo, repoID, cloneFetchOK)
	if err := s.markClosedLinkedNotificationsDone(ctx); err != nil {
		markErr := err
		if syncErr == nil {
			syncErr = markErr
		} else {
			syncErr = joinPartialFailureCause(syncErr, markErr)
		}
	}

	// A refused cycle has not completed a new health observation. Keep the
	// previous completed result, including a real failure, until work resumes.
	if syncBudgetWait(syncErr) {
		return syncErr
	}
	syncErrStr := ""
	if syncErr != nil {
		syncErrStr = syncErr.Error()
	}
	if err := s.db.UpdateRepoSyncCompleted(ctx, repoID, time.Now().UTC(), syncErrStr); err != nil {
		slog.Error("mark sync completed", "repo", repo.Owner+"/"+repo.Name, "err", err)
	}

	return syncErr
}

// recordAbortedRepoSync surfaces a sync that stopped before item indexing on
// the repository row's sync health, so the UI reports the failed attempt
// instead of presenting the previous outcome as current. Best effort.
func (s *Syncer) recordAbortedRepoSync(
	ctx context.Context,
	repo RepoRef,
	repoID int64,
	syncErr error,
) {
	if syncBudgetWait(syncErr) {
		return
	}
	now := time.Now().UTC()
	if err := s.db.UpdateRepoSyncStarted(ctx, repoID, now); err != nil {
		slog.Warn("record aborted sync start failed",
			"repo", repo.Owner+"/"+repo.Name, "err", err,
		)
		return
	}
	if err := s.db.UpdateRepoSyncCompleted(ctx, repoID, now, syncErr.Error()); err != nil {
		slog.Warn("record aborted sync completion failed",
			"repo", repo.Owner+"/"+repo.Name, "err", err,
		)
	}
}

// refreshRepoSettings persists the repository's provider metadata and merge
// settings from the snapshot identity resolution already fetched, or fetches
// one when resolution could not.
func (s *Syncer) refreshRepoSettings(
	ctx context.Context,
	repo RepoRef,
	repoID int64,
	resolvedRepo *platform.Repository,
) error {
	if resolvedRepo != nil {
		return s.updateRepoSettingsFromProvider(ctx, repoID, *resolvedRepo)
	}
	if client, ok := s.optionalGitHubClientFor(repo); ok {
		ghRepo, err := client.GetRepository(ctx, repo.Owner, repo.Name)
		if err != nil {
			return fmt.Errorf("get repo settings: %w", err)
		}
		return s.updateRepoSettingsFromProvider(
			ctx, repoID, platformgithub.GitHubPlatformRepository(repoHost(repo), repo.Owner, ghRepo),
		)
	}
	reader, err := s.clients.RepositoryReader(repoPlatform(repo), repoHost(repo))
	if err != nil {
		if errors.Is(err, platform.ErrUnsupportedCapability) || errors.Is(err, platform.ErrProviderNotConfigured) {
			return nil
		}
		return fmt.Errorf("resolve repo settings reader: %w", err)
	}
	providerRepo, err := reader.GetRepository(ctx, platformRepoRef(repo))
	if err != nil {
		return fmt.Errorf("get repo settings: %w", err)
	}
	return s.updateRepoSettingsFromProvider(ctx, repoID, providerRepo)
}

// reconcileRepoForDirectSync resolves repository identity for a direct item
// sync and persists the verified provider snapshot, so a new repository row
// never serves permissive schema defaults.
func (s *Syncer) reconcileRepoForDirectSync(
	ctx context.Context,
	repo RepoRef,
) (RepoRef, int64, error) {
	resolvedRef, repoID, providerRepo, err := s.reconcileRepoIdentityObservation(ctx, repo, false)
	if err != nil {
		return RepoRef{}, 0, fmt.Errorf(
			"resolve repo identity %s/%s: %w", repo.Owner, repo.Name, err,
		)
	}
	if providerRepo == nil {
		return resolvedRef, repoID, nil
	}
	if err := s.updateRepoSettingsFromProvider(ctx, repoID, *providerRepo); err != nil {
		return RepoRef{}, 0, fmt.Errorf(
			"persist repository settings for %s/%s: %w",
			resolvedRef.Owner, resolvedRef.Name, err,
		)
	}
	return resolvedRef, repoID, nil
}

func (s *Syncer) updateRepoSettingsFromProvider(
	ctx context.Context,
	repoID int64,
	repo platform.Repository,
) error {
	var settings *db.RepoMergeSettings
	if repo.MergeSettings != nil {
		settings = &db.RepoMergeSettings{
			AllowSquashMerge: repo.MergeSettings.AllowSquashMerge,
			AllowMergeCommit: repo.MergeSettings.AllowMergeCommit,
			AllowRebaseMerge: repo.MergeSettings.AllowRebaseMerge,
		}
	}
	return s.db.UpdateRepoProviderObservation(
		ctx,
		repoID,
		db.RepoProviderMetadata{
			WebURL:        repo.WebURL,
			CloneURL:      repo.CloneURL,
			DefaultBranch: repo.DefaultBranch,
		},
		settings,
		repo.ViewerCanMerge,
	)
}

func (s *Syncer) syncRepoLabelCatalog(ctx context.Context, repo RepoRef, repoID int64) {
	checkedAt := time.Now().UTC()
	reader, err := s.labelReaderFor(repo)
	if err != nil {
		if errors.Is(err, platform.ErrUnsupportedCapability) || errors.Is(err, platform.ErrProviderNotConfigured) {
			return
		}
		_ = s.db.UpdateRepoLabelCatalogCheck(ctx, repoID, checkedAt, err.Error())
		return
	}
	catalog, err := reader.ListLabels(ctx, platformRepoRef(repo))
	if err != nil {
		_ = s.db.UpdateRepoLabelCatalogCheck(ctx, repoID, checkedAt, err.Error())
		return
	}
	if catalog.NotModified {
		if err := s.db.MarkRepoLabelCatalogSynced(ctx, repoID, checkedAt); err != nil {
			slog.Warn("mark label catalog synced", "repo", repo.Owner+"/"+repo.Name, "err", err)
		}
		return
	}
	labels := platformdb.DBLabels(catalog.Labels, checkedAt)
	if err := s.db.ReplaceRepoLabelCatalog(ctx, repoID, labels, checkedAt); err != nil {
		slog.Warn("replace label catalog", "repo", repo.Owner+"/"+repo.Name, "err", err)
	}
}

func (s *Syncer) RefreshRepoLabelCatalog(ctx context.Context, repo db.Repo) error {
	ref := RepoRef{
		Platform:      platform.Kind(repo.Platform),
		PlatformHost:  repoProviderHostFromDB(repo),
		Owner:         repo.Owner,
		Name:          repo.Name,
		RepoPath:      repo.RepoPath,
		Key:           repo.Key,
		CloneURL:      repo.CloneURL,
		WebURL:        repo.WebURL,
		DefaultBranch: repo.DefaultBranch,
	}
	checkedAt := time.Now().UTC()
	reader, err := s.labelReaderFor(ref)
	if err != nil {
		if updateErr := s.db.UpdateRepoLabelCatalogCheck(ctx, repo.ID, checkedAt, err.Error()); updateErr != nil {
			if errors.Is(updateErr, db.ErrRepositoryIdentityChanged) {
				return nil
			}
			return errors.Join(err, updateErr)
		}
		return err
	}
	catalog, err := reader.ListLabels(ctx, platformRepoRef(ref))
	if err != nil {
		if updateErr := s.db.UpdateRepoLabelCatalogCheck(ctx, repo.ID, checkedAt, err.Error()); updateErr != nil {
			if errors.Is(updateErr, db.ErrRepositoryIdentityChanged) {
				return nil
			}
			return errors.Join(err, updateErr)
		}
		return err
	}
	if catalog.NotModified {
		err := s.db.MarkRepoLabelCatalogSynced(ctx, repo.ID, checkedAt)
		if errors.Is(err, db.ErrRepositoryIdentityChanged) {
			return nil
		}
		return err
	}
	err = s.db.ReplaceRepoLabelCatalog(ctx, repo.ID, platformdb.DBLabels(catalog.Labels, checkedAt), checkedAt)
	if errors.Is(err, db.ErrRepositoryIdentityChanged) {
		return nil
	}
	return err
}

// SyncRepoOnProvider performs the index sync for one configured repository.
// It is used by manual refresh paths that need fresh PR discovery before they
// can act on a specific item number.
func (s *Syncer) SyncRepoOnProvider(
	ctx context.Context,
	kind platform.Kind,
	host, owner, name string,
) error {
	repo, ok := s.trackedRepoByIdentity(kind, owner, name, host)
	if !ok {
		host = repoHost(RepoRef{Platform: kind, PlatformHost: host})
		return fmt.Errorf(
			"repo %s/%s on %s/%s is %w",
			owner, name, kind, host, ErrRepoNotTracked,
		)
	}
	repo.Owner = owner
	repo.Name = name
	repo.PlatformHost = repoHost(repo)
	return s.syncRepo(withRepositoryFeatureCooldownBypass(
		ctx, s.featureCooldowns.currentGeneration(),
	), repo)
}
