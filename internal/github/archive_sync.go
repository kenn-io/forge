package github

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/cenkalti/backoff/v7"

	"go.kenn.io/forge/internal/archive"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/platform"
)

type archiveProviderRequest struct {
	cancel context.CancelFunc
	done   chan struct{}
}

type archiveRunner interface {
	// RunPass performs one worker pass and reports whether it attempted
	// provider work.
	RunPass(context.Context) (bool, error)
}

type archiveRepositoryLifecycle interface {
	EnsureConfigured(context.Context, []platform.RepoRef) ([]platform.RepoRef, error)
	RetryAuthentication(context.Context, []platform.RepoRef) error
}

// SetArchiveService attaches the provider-neutral archive worker before Start.
// It also extends provider window-reset callbacks so budget resets wake queued
// archive work immediately.
func (s *Syncer) SetArchiveService(runner archiveRunner) {
	s.archiveRunner = runner
	s.archiveLifecycle, _ = runner.(archiveRepositoryLifecycle)
	for key, tracker := range s.rateTrackers {
		if tracker == nil {
			continue
		}
		budget := s.budgets[key]
		tracker.SetOnWindowReset(func() {
			if budget != nil {
				budget.Reset()
			}
			s.WakeArchive()
		})
	}
}

func (s *Syncer) WakeArchive() {
	if !s.SyncEnabled() {
		return
	}
	select {
	case s.archiveWake <- struct{}{}:
	default:
	}
}

// SetArchivePollIntervalForTesting shortens the pacing between archive worker
// passes. It is the wait after a pass that attempted work or failed and the
// starting point of the idle backoff.
func (s *Syncer) SetArchivePollIntervalForTesting(interval time.Duration) {
	if interval > 0 {
		s.archivePollInterval = interval
	}
}

func (s *Syncer) ConfiguredRepositories(context.Context) ([]platform.RepoRef, error) {
	s.reposMu.Lock()
	repos := slices.Clone(s.repos)
	s.reposMu.Unlock()
	refs := make([]platform.RepoRef, 0, len(repos))
	for _, repo := range repos {
		kind := repoPlatform(repo)
		host := repoHost(repo)
		refs = append(refs, platform.RepoRef{
			Platform: kind, Host: host, Owner: repo.Owner, Name: repo.Name,
			RepoPath: repo.Owner + "/" + repo.Name,
			Key:      repo.Key,
		})
	}
	return refs, nil
}

func (s *Syncer) Admit(
	ctx context.Context,
	ref platform.RepoRef,
	itemType db.ArchiveItemType,
	cost int,
) (archive.AdmissionResult, error) {
	feature := ""
	switch itemType {
	case db.ArchiveItemTypeIssue:
		feature = platform.RepositoryFeatureIssues
	case db.ArchiveItemTypeMergeRequest:
		feature = platform.RepositoryFeatureMergeRequests
	default:
		return archive.AdmissionResult{}, fmt.Errorf("archive admission: invalid item type %q", itemType)
	}
	repo := RepoRef{
		Platform: ref.Platform, PlatformHost: ref.Host,
		Owner: ref.Owner, Name: ref.Name, RepoPath: ref.RepoPath,
		Key: ref.Key,
	}
	if archive.InventoryProbeRequested(ctx) {
		ctx = withRepositoryFeatureCooldownBypass(
			ctx, s.featureCooldowns.currentGeneration(),
		)
	}
	probe, due, featureRetryAt := s.beginRepositoryFeatureProbeWithRetry(ctx, repo, feature)
	if !due {
		return archive.AdmissionResult{FeatureDeferred: &archive.FeatureDeferral{
			RetryAt: featureRetryAt,
			Detail:  "repository feature cooldown active",
		}}, nil
	}
	now := s.now().UTC()
	if s.running.Load() {
		probe.abandon()
		retryAt := now.Add(time.Second)
		return archive.AdmissionResult{RetryAt: &retryAt, Detail: "normal sync is active"}, nil
	}
	keyRepo := RepoRef{
		Platform:     ref.Platform,
		PlatformHost: ref.Host,
		Owner:        ref.Owner,
		Name:         ref.Name,
		RepoPath:     ref.RepoPath,
		Key:          ref.Key,
	}
	identity, err := s.archiveIdentityForRepo(keyRepo)
	if err != nil {
		return archive.AdmissionResult{}, err
	}
	key := RateBucketKey(string(repoPlatform(keyRepo)), identity.Host, identity.Principal)
	if s.higherPriorityProviderWorkActive(key, archive.PriorityFullArchive) {
		probe.abandon()
		s.noteArchiveDenied(key)
		retryAt := now.Add(time.Second)
		return archive.AdmissionResult{RetryAt: &retryAt, Detail: "higher-priority sync work is active"}, nil
	}
	tracker := s.rateTrackers[key]
	var providerResetAt *time.Time
	var providerPacingWindow *QuotaPacingWindow
	var providerResources []QuotaResource
	identity, identityErr := s.archiveIdentityForRepo(repo)
	if ref.Platform == platform.KindGitHub && s.quotaRegistry != nil && identityErr == nil {
		providerResources = []QuotaResource{QuotaResourceREST, QuotaResourceGraphQL}
		pacingWindow, pacingKnown := s.quotaRegistry.PacingWindow(identity, providerResources)
		if !pacingKnown || pacingWindow.ArchiveHeadroom < cost {
			probe.abandon()
			retryAt := now.Add(time.Minute)
			detail := "provider rate reserve reached"
			if !pacingKnown {
				detail = "provider quota unknown"
			} else if reset := pacingWindow.ArchiveRetryAt(cost); reset.After(now) {
				retryAt = reset.UTC()
			}
			return archive.AdmissionResult{RetryAt: &retryAt, Detail: detail}, nil
		}
		providerPacingWindow = &pacingWindow
		providerResetAt = &providerPacingWindow.ResetAt
	} else if tracker != nil && (tracker.IsPaused() ||
		tracker.Known() && tracker.Remaining()-cost < RateReserveBuffer) {
		probe.abandon()
		retryAt := now.Add(time.Minute)
		if reset := tracker.ResetAt(); reset != nil && reset.After(now) {
			retryAt = reset.UTC()
		}
		return archive.AdmissionResult{RetryAt: &retryAt, Detail: "provider rate reserve reached"}, nil
	}
	budget := s.budgets[key]
	resetAt := providerResetAt
	if resetAt == nil {
		resetAt = archiveBudgetResetAt(tracker)
	}
	available := 0
	if providerPacingWindow != nil {
		// Provider quota is authoritative: archive may spend everything above
		// each pool's own archive reserve, and the local sync budget meters
		// live sync only.
		available = max(providerPacingWindow.ArchiveHeadroom, 0)
	} else if budget != nil {
		liveFloor := archiveLiveFloor(ref.Platform)
		if resetAt == nil &&
			(ref.Platform == platform.KindGitea || ref.Platform == platform.KindForgejo) {
			available = budget.LocalArchiveSpendAvailable(liveFloor)
		} else {
			available = budget.ArchiveSpendAvailable(now, resetAt, liveFloor)
		}
	}
	if available < cost {
		probe.abandon()
		retryAt := now.Add(time.Minute)
		if resetAt != nil && resetAt.After(now) && resetAt.Before(retryAt) {
			retryAt = resetAt.UTC()
		}
		return archive.AdmissionResult{RetryAt: &retryAt, Detail: "archive surplus budget unavailable"}, nil
	}
	requestCtx, releaseProviderRequest, allowed := s.tryBeginArchiveProviderRequest(ctx, key)
	if !allowed {
		probe.abandon()
		retryAt := now.Add(time.Second)
		return archive.AdmissionResult{RetryAt: &retryAt, Detail: "higher-priority sync work is active"}, nil
	}
	requestCtx = WithArchiveSyncBudget(requestCtx)
	completeGitealikeMR := itemType == db.ArchiveItemTypeMergeRequest &&
		(ref.Platform == platform.KindGitea || ref.Platform == platform.KindForgejo)
	if !completeGitealikeMR {
		if providerPacingWindow != nil {
			requestCtx = WithArchiveProviderAttemptAllowance(
				requestCtx, available, identity, providerResources,
			)
		} else {
			requestCtx = WithArchiveAttemptAllowance(requestCtx, available)
		}
	}
	var completeOnce sync.Once
	var featureDeferred *archive.FeatureDeferral
	complete := func(cause error, providerAttempted bool) *archive.FeatureDeferral {
		completeOnce.Do(func() {
			if !providerAttempted {
				probe.abandon()
			} else if disabled := repositoryFeatureDisabledError(repo, feature, cause); disabled != nil {
				nextProbe, _ := s.recordRepositoryFeatureDisabledUntil(repo, feature, disabled)
				featureDeferred = &archive.FeatureDeferral{
					RetryAt: nextProbe,
					Detail:  disabled.Error(),
				}
			} else {
				probe.release()
			}
			releaseProviderRequest()
		})
		return featureDeferred
	}
	// The declared cost is the admission minimum. Gitealike merge-request reads
	// are atomic and data-complete, so they remain preemptible but are not cut
	// off mid-dataset by the admission estimate. Other archive reads may use the
	// currently available surplus without crossing the live floor.
	return archive.AdmissionResult{
		Allowed:  true,
		Context:  requestCtx,
		Complete: complete,
	}, nil
}

// noteArchiveDenied records that live work on key turned an archive request
// away, so releasing the host later wakes the archive worker.
func (s *Syncer) noteArchiveDenied(key string) {
	s.providerWorkMu.Lock()
	s.markArchiveDeniedLocked(key)
	s.providerWorkMu.Unlock()
}

func (s *Syncer) markArchiveDeniedLocked(key string) {
	if s.archiveDeniedHosts == nil {
		s.archiveDeniedHosts = make(map[string]struct{})
	}
	s.archiveDeniedHosts[key] = struct{}{}
}

func (s *Syncer) tryBeginArchiveProviderRequest(
	ctx context.Context,
	key string,
) (context.Context, func(), bool) {
	s.providerWorkMu.Lock()
	if active := s.providerWork[key]; len(active) > 0 {
		s.providerWorkMu.Unlock()
		return nil, nil, false
	}
	if s.providerWork == nil {
		s.providerWork = make(map[string]map[archive.WorkPriority]int)
	}
	s.providerWork[key] = map[archive.WorkPriority]int{archive.PriorityFullArchive: 1}
	requestCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	s.archiveProviderRequests[key] = archiveProviderRequest{cancel: cancel, done: done}
	s.providerWorkMu.Unlock()

	var once sync.Once
	return requestCtx, func() {
		once.Do(func() {
			cancel()
			s.providerWorkMu.Lock()
			active := s.providerWork[key]
			delete(active, archive.PriorityFullArchive)
			if len(active) == 0 {
				delete(s.providerWork, key)
			}
			delete(s.archiveProviderRequests, key)
			close(done)
			s.providerWorkMu.Unlock()
		})
	}, true
}

func archiveBudgetResetAt(tracker *RateTracker) *time.Time {
	if tracker == nil {
		return nil
	}
	return tracker.ResetAt()
}

func archiveLiveFloor(kind platform.Kind) int {
	if kind == platform.KindGitea || kind == platform.KindForgejo {
		return detailWorstCaseAttemptCost(kind, QueueItemPR)
	}
	floor := detailWorstCaseAttemptCost(kind, QueueItemPR) + wireAttemptsPerRequest
	if kind == platform.KindGitHub {
		floor += wireAttemptsPerRequest // one notification page
	}
	return floor
}

func (s *Syncer) beginProviderWork(ctx context.Context, key string, priority archive.WorkPriority) func() {
	s.providerWorkMu.Lock()
	if s.providerWork == nil {
		s.providerWork = make(map[string]map[archive.WorkPriority]int)
	}
	active := s.providerWork[key]
	if active == nil {
		active = make(map[archive.WorkPriority]int)
		s.providerWork[key] = active
	}
	active[priority]++
	archiveRequest, waitForArchive := s.archiveProviderRequests[key]
	if waitForArchive {
		archiveRequest.cancel()
		s.markArchiveDeniedLocked(key)
	}
	s.providerWorkMu.Unlock()
	if waitForArchive {
		select {
		case <-archiveRequest.done:
		case <-ctx.Done():
		}
	}

	var once sync.Once
	return func() {
		once.Do(func() {
			s.providerWorkMu.Lock()
			active := s.providerWork[key]
			active[priority]--
			if active[priority] == 0 {
				delete(active, priority)
			}
			hostFree := len(active) == 0
			if hostFree {
				delete(s.providerWork, key)
			}
			_, wake := s.archiveDeniedHosts[key]
			if hostFree && wake {
				delete(s.archiveDeniedHosts, key)
			}
			s.providerWorkMu.Unlock()
			if hostFree && wake {
				// This host turned archive work away while live work held it
				// and the worker backed off; tell it the host is free again.
				// Hosts that never denied archive work stay quiet, so a normal
				// sync's stream of releases does not trigger denied passes.
				s.WakeArchive()
			}
		})
	}
}

func (s *Syncer) higherPriorityProviderWorkActive(key string, threshold archive.WorkPriority) bool {
	s.providerWorkMu.Lock()
	defer s.providerWorkMu.Unlock()
	for priority, count := range s.providerWork[key] {
		if count > 0 && priority < threshold {
			return true
		}
	}
	return false
}

func (s *Syncer) runArchiveLoop(ctx context.Context, ready <-chan struct{}) {
	select {
	case <-ready:
	case <-s.stopCh:
		return
	case <-ctx.Done():
		return
	}
	interval := s.archivePollInterval
	if interval <= 0 {
		interval = time.Second
	}
	// Idle passes back off exponentially from the pacing interval to
	// archiveIdleWait. A pass that attempted work or failed, or a wake from
	// config reload, sync completion, a budget reset, or an archive start,
	// returns the loop to the pacing interval.
	idle := backoff.NewExponentialBackOff()
	idle.InitialInterval = interval
	idle.MaxInterval = archiveIdleWait
	idle.Multiplier = 2
	idle.RandomizationFactor = 0
	idle.Reset()
	for {
		var worked bool
		var err error
		if s.AutomaticSyncEnabled() {
			worked, err = s.archiveRunner.RunPass(ctx)
		}
		if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			slog.Warn("archive worker iteration failed", "err", err)
		}
		if worked || err != nil {
			idle.Reset()
		}
		timer := time.NewTimer(idle.NextBackOff())
		select {
		case <-timer.C:
		case <-s.archiveWake:
			timer.Stop()
			idle.Reset()
		case <-s.stopCh:
			timer.Stop()
			return
		case <-ctx.Done():
			timer.Stop()
			return
		}
	}
}

// archiveIdleWait caps the archive worker's idle backoff. Every source of
// time-driven archive work waits at least a minute, so a bounded delay in
// noticing it costs little, while an idle daemon stops paying for a pass
// every second.
const archiveIdleWait = 5 * time.Minute

// ArchiveItemSyncCost returns the provider-aware admission estimate for the
// existing full item sync. Gitealike merge-request hydration may exceed this
// estimate after admission because its complete dataset is committed atomically.
func (s *Syncer) ArchiveItemSyncCost(kind platform.Kind, itemType db.ArchiveItemType) int {
	if itemType == db.ArchiveItemTypeMergeRequest {
		return detailWorstCaseAttemptCost(kind, QueueItemPR)
	}
	return detailWorstCaseAttemptCost(kind, QueueItemIssue)
}

// SyncArchiveItem runs archive hydration through the canonical live item
// sync. The caller has already acquired archive admission, so the archive
// budget context also prevents the live entry point from taking a nested
// provider-work lease.
func (s *Syncer) SyncArchiveItem(
	ctx context.Context,
	ref platform.RepoRef,
	itemType db.ArchiveItemType,
	number int,
) (archive.ItemSyncResult, error) {
	result := archive.ItemSyncResult{}
	repo, ok := s.trackedRepoByIdentity(ref.Platform, ref.Owner, ref.Name, ref.Host)
	if !ok {
		return result, fmt.Errorf(
			"repo %s/%s on %s/%s is %w",
			ref.Owner, ref.Name, ref.Platform, ref.Host, ErrRepoNotTracked,
		)
	}
	repo.Owner = ref.Owner
	repo.Name = ref.Name
	repo.PlatformHost = repoHost(repo)
	switch itemType {
	case db.ArchiveItemTypeIssue:
		providerAttempted := false
		err := s.syncIssueForRepo(ctx, repo, number, &providerAttempted)
		result.ProviderAttempted = providerAttempted
		return result, err
	case db.ArchiveItemTypeMergeRequest:
		providerAttempted := false
		var resolvedRepoID int64
		var fetchedEvidence mergeRequestFetchEvidence
		var lifecyclePersisted bool
		err := s.syncMRForRepoResolved(
			ctx, repo, number, mrDetailFull, &providerAttempted, &resolvedRepoID, &fetchedEvidence,
			&lifecyclePersisted, nil, nil,
		)
		if _, onlyDiffFailed := err.(*DiffSyncError); onlyDiffFailed { //nolint:errorlint // joined hard failures must propagate
			err = nil
		}
		if err == nil && repoPlatform(repo) == platform.KindGitHub {
			err = s.requireGitHubArchiveMergedMRMetrics(
				ctx, resolvedRepoID, number, fetchedEvidence, lifecyclePersisted,
			)
			if err == nil {
				var filesChanged *int
				if fetchedEvidence.filesChanged != nil {
					value := *fetchedEvidence.filesChanged
					filesChanged = &value
				}
				result.MergeRequestEvidence = &db.ArchiveMergeRequestEvidence{
					Merged: fetchedEvidence.merged, HeadSHA: fetchedEvidence.headSHA,
					MergeCommitSHA: fetchedEvidence.mergeCommitSHA,
					FilesChanged:   filesChanged,
				}
			}
		}
		result.ProviderAttempted = providerAttempted
		return result, err
	default:
		return result, fmt.Errorf("sync archive item: invalid item type %q", itemType)
	}
}

// FinalizeArchiveItemSync applies persistence that depends on the archive
// lifecycle commit. A concurrent terminal observation can land after the item
// is claimed, so parent-snapshot reclassification may still see the item as
// removed. The successful archive commit reactivates it before this callback.
func (s *Syncer) FinalizeArchiveItemSync(
	ctx context.Context,
	repoID int64,
	itemType db.ArchiveItemType,
	number int,
) {
	if itemType != db.ArchiveItemTypeMergeRequest {
		return
	}
	stored, err := s.db.GetRepoByID(ctx, repoID)
	if err != nil {
		slog.Error("look up repository for archive item finalization failed",
			"repo_id", repoID, "number", number, "err", err,
		)
		return
	}
	if stored == nil {
		return
	}
	s.reclassifyWorkspaceHeadRepoTrust(ctx, RepoRef{
		Platform: platform.Kind(stored.Platform), PlatformHost: stored.PlatformHost,
		Owner: stored.Owner, Name: stored.Name, RepoPath: stored.RepoPath,
		Key: stored.Key,
	}, repoID, number)
}

func (s *Syncer) requireGitHubArchiveMergedMRMetrics(
	ctx context.Context,
	repoID int64,
	number int,
	fetched mergeRequestFetchEvidence,
	lifecyclePersisted bool,
) error {
	if repoID == 0 {
		return fmt.Errorf("verify GitHub archive MR #%d: repository was not resolved", number)
	}
	storedRepo, err := s.db.GetRepoByID(ctx, repoID)
	if err != nil {
		return fmt.Errorf("verify GitHub archive MR #%d repository: %w", number, err)
	}
	if storedRepo == nil {
		return fmt.Errorf("verify GitHub archive MR #%d: repository is not stored", number)
	}
	if fetched.merged && !lifecyclePersisted {
		return fmt.Errorf(
			"verify GitHub archive MR %s/%s#%d: lifecycle persistence incomplete",
			storedRepo.Owner, storedRepo.Name, number,
		)
	}
	mr, err := s.db.GetMergeRequestByRepoIDAndNumber(ctx, repoID, number)
	if err != nil {
		return fmt.Errorf("verify GitHub archive MR #%d metrics: %w", number, err)
	}
	if mr == nil {
		return fmt.Errorf("verify GitHub archive MR #%d metrics: pull request is not stored", number)
	}
	storedMerged := mr.State == db.MergeRequestStateMerged || mr.MergedAt != nil
	if fetched.merged != storedMerged {
		return fmt.Errorf(
			"verify GitHub archive MR %s/%s#%d metrics: incomplete or mismatched merge_state",
			storedRepo.Owner, storedRepo.Name, number,
		)
	}
	if !fetched.merged {
		return nil
	}
	incomplete := make([]string, 0, 4)
	if mr.MergedAt == nil {
		incomplete = append(incomplete, "merged_at")
	}
	if fetched.merged {
		if fetched.headSHA == "" || mr.PlatformHeadSHA != fetched.headSHA {
			incomplete = append(incomplete, "platform_head_sha")
		}
		if fetched.mergeCommitSHA == "" || mr.MergeCommitSHA != fetched.mergeCommitSHA {
			incomplete = append(incomplete, "merge_commit_sha")
		}
		if fetched.filesChanged == nil || mr.FilesChanged == nil ||
			*mr.FilesChanged != *fetched.filesChanged {
			incomplete = append(incomplete, "files_changed")
		}
	} else {
		if mr.MergeCommitSHA == "" {
			incomplete = append(incomplete, "merge_commit_sha")
		}
		if mr.FilesChanged == nil {
			incomplete = append(incomplete, "files_changed")
		}
	}
	if len(incomplete) > 0 {
		return fmt.Errorf(
			"verify GitHub archive MR %s/%s#%d metrics: incomplete or mismatched %s",
			storedRepo.Owner, storedRepo.Name, number, strings.Join(incomplete, ", "),
		)
	}
	return nil
}
