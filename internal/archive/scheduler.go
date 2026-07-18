package archive

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"go.kenn.io/middleman/internal/db"
)

type WorkPriority int

const (
	PriorityNormalIndex WorkPriority = iota
	PriorityNotificationRefresh
	PriorityActiveDetail
	PriorityFullArchive
	PriorityDiscoveryInventory
)

type Scheduler struct{}

func NewScheduler() *Scheduler { return &Scheduler{} }

func (s *Scheduler) Run(ctx context.Context, groups map[string][]resolvedRepository, work func(context.Context, []resolvedRepository) error) error {
	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	errCh := make(chan error, len(keys))
	var wg sync.WaitGroup
	for _, key := range keys {
		key, repos := key, groups[key]
		wg.Go(func() {
			if err := work(ctx, repos); err != nil {
				errCh <- fmt.Errorf("archive provider worker %s: %w", key, err)
			}
		})
	}
	wg.Wait()
	close(errCh)
	var errs []error
	for err := range errCh {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

var errAdmissionDeferred = errors.New("archive request deferred by admission")

func (s *Service) RunEligible(ctx context.Context) error {
	if s.configured == nil {
		return errors.New("run eligible archives: configured repository source is required")
	}
	refs, err := s.configured.ConfiguredRepositories(ctx)
	if err != nil {
		return fmt.Errorf("list configured archive repositories: %w", err)
	}
	// Configuration reconciliation runs at startup and on configuration reload.
	// The one-second worker poll must remain read-only when no work is eligible.
	resolved, err := s.resolveRepositories(ctx, refs, true)
	if err != nil {
		return err
	}
	groups := make(map[string][]resolvedRepository)
	for _, repo := range resolved {
		key := string(repo.Ref.Platform) + "\x00" + repo.Ref.Host
		groups[key] = append(groups[key], repo)
	}
	return s.scheduler.Run(ctx, groups, s.runProviderHostWork)
}

func (s *Service) runProviderHostWork(ctx context.Context, repos []resolvedRepository) error {
	states, err := s.db.ListArchiveRepoStates(ctx, resolvedRepoIDs(repos))
	if err != nil {
		return err
	}
	stateByID := make(map[int64]db.ArchiveRepoState, len(states))
	for _, state := range states {
		stateByID[state.RepoID] = state
	}

	// Give both independent inventories their first bounded page before
	// hydration so a large issue history cannot starve merge-request discovery.
	for _, repo := range repos {
		state := stateByID[repo.ID]
		if state.OperatorState != db.ArchiveOperatorStateActive ||
			state.CollectionMode != db.ArchiveCollectionModeFull || archiveRepoDeferred(state, s.now()) {
			continue
		}
		if archiveScanNotStarted(state.IssueInventory) {
			return s.swallowAdmissionDeferred(s.inventoryPage(ctx, repo, state, db.ArchiveItemTypeIssue))
		}
		if archiveScanNotStarted(state.MergeRequestInventory) {
			return s.swallowAdmissionDeferred(s.inventoryPage(ctx, repo, state, db.ArchiveItemTypeMergeRequest))
		}
	}

	// Once a maintenance scan has established its durable boundary, finish
	// both inventory streams before newly observed equality-boundary work can
	// enter hydration. Otherwise an overlapped item can consume the next
	// admitted request and indefinitely delay a persisted inventory cursor.
	// The boundary only holds priority while it can actually advance: a
	// boundary whose streams are all blocked stays durable for reporting but
	// must not consume every poll and starve hydration and other
	// repositories on the same provider host.
	for _, repo := range repos {
		state := stateByID[repo.ID]
		if state.CollectionMode == db.ArchiveCollectionModeFull &&
			state.OperatorState == db.ArchiveOperatorStateActive &&
			state.PromptScanStartedAt != nil && !archiveRepoDeferred(state, s.now()) &&
			maintenanceBoundaryActionable(state) {
			return s.swallowAdmissionDeferred(s.promptMaintenance(ctx, repo, state))
		}
	}

	fullIDs := make([]int64, 0, len(repos))
	for _, repo := range repos {
		state := stateByID[repo.ID]
		if state.CollectionMode == db.ArchiveCollectionModeFull &&
			state.OperatorState == db.ArchiveOperatorStateActive && !archiveRepoDeferred(state, s.now()) {
			fullIDs = append(fullIDs, repo.ID)
		}
	}
	item, err := s.db.ClaimArchiveItem(ctx, db.ClaimArchiveItemOpts{RepoIDs: fullIDs, Now: s.now()})
	if err != nil {
		return err
	}
	if item != nil {
		repo := resolvedRepoByID(repos, item.RepoID)
		return s.swallowAdmissionDeferred(s.hydrateItem(ctx, repo, *item))
	}

	if repo, state, itemType, ok := nextInventoryWork(repos, stateByID, db.ArchiveCollectionModeFull, s.now()); ok {
		return s.swallowAdmissionDeferred(s.inventoryPage(ctx, repo, state, itemType))
	}
	for _, repo := range repos {
		state := stateByID[repo.ID]
		if state.CollectionMode != db.ArchiveCollectionModeFull ||
			state.OperatorState != db.ArchiveOperatorStateActive || archiveRepoDeferred(state, s.now()) {
			continue
		}
		if promptMaintenanceDue(state, s.now(), s.maintenanceInterval) && maintenanceBoundaryActionable(state) {
			return s.swallowAdmissionDeferred(s.promptMaintenance(ctx, repo, state))
		}
	}
	if repo, state, itemType, ok := nextInventoryWork(repos, stateByID, db.ArchiveCollectionModeDiscovery, s.now()); ok {
		return s.swallowAdmissionDeferred(s.inventoryPage(ctx, repo, state, itemType))
	}
	return nil
}

func (s *Service) swallowAdmissionDeferred(err error) error {
	if errors.Is(err, errAdmissionDeferred) {
		return nil
	}
	return err
}

// archiveScanNotStarted reports a scan generation that has not committed any
// page yet: eligible for its bootstrap first page.
func archiveScanNotStarted(scan db.ArchiveScanState) bool {
	return scan.Status == db.ArchiveScanPending && scan.PageCount == 0
}

// archiveScanEligible reports a scan that still has pages to fetch and is not
// durably blocked.
func archiveScanEligible(scan db.ArchiveScanState) bool {
	return !scan.Complete() && !scan.Blocked()
}

// maintenanceBoundaryActionable reports whether a maintenance pass could make
// progress right now. Before a boundary exists one can always be started.
// With a durable boundary, progress requires at least one stream that can
// still advance, or both streams complete (the watermark advance itself is
// the remaining work). A boundary whose outstanding streams are all blocked
// is not actionable: it stays durable for reporting, and only an explicit
// reset revives it.
func maintenanceBoundaryActionable(state db.ArchiveRepoState) bool {
	if state.PromptScanStartedAt == nil {
		return true
	}
	if archiveScanEligible(state.MaintenanceIssues) || archiveScanEligible(state.MaintenanceMergeRequests) {
		return true
	}
	return state.MaintenanceIssues.Complete() && state.MaintenanceMergeRequests.Complete()
}

func nextInventoryWork(repos []resolvedRepository, states map[int64]db.ArchiveRepoState, mode db.ArchiveCollectionMode, now time.Time) (resolvedRepository, db.ArchiveRepoState, db.ArchiveItemType, bool) {
	for _, repo := range repos {
		state := states[repo.ID]
		if state.CollectionMode != mode || state.OperatorState != db.ArchiveOperatorStateActive || archiveRepoDeferred(state, now) {
			continue
		}
		if archiveScanEligible(state.IssueInventory) {
			return repo, state, db.ArchiveItemTypeIssue, true
		}
		if archiveScanEligible(state.MergeRequestInventory) {
			return repo, state, db.ArchiveItemTypeMergeRequest, true
		}
	}
	return resolvedRepository{}, db.ArchiveRepoState{}, "", false
}

func archiveRepoDeferred(state db.ArchiveRepoState, now time.Time) bool {
	if state.NextRetryAt != nil && state.NextRetryAt.After(now) {
		return true
	}
	if state.LastErrorCode == nil {
		return false
	}
	return *state.LastErrorCode == string(db.ArchiveErrorCodeAuthentication) ||
		*state.LastErrorCode == string(db.ArchiveErrorCodeRepoBlocked)
}

func resolvedRepoByID(repos []resolvedRepository, repoID int64) resolvedRepository {
	for _, repo := range repos {
		if repo.ID == repoID {
			return repo
		}
	}
	return resolvedRepository{}
}

func (s *Service) admit(
	ctx context.Context,
	repo resolvedRepository,
	cost int,
) (context.Context, func(), error) {
	if err := s.db.ClearArchiveRepositoryError(ctx, repo.ID, s.now()); err != nil {
		return nil, nil, err
	}
	if s.admission == nil {
		return ctx, func() {}, nil
	}
	result, err := s.admission.Admit(ctx, repo.Ref, cost)
	if err != nil {
		return nil, nil, err
	}
	if !result.Allowed {
		if result.RetryAt == nil {
			return nil, nil, errors.New("archive admission denied without retry time")
		}
		if err := s.db.DeferArchiveRepository(ctx, repo.ID, *result.RetryAt, result.Detail, s.now()); err != nil {
			return nil, nil, err
		}
		return nil, nil, errAdmissionDeferred
	}
	requestCtx := ctx
	if result.Context != nil {
		requestCtx = result.Context
	}
	release := result.Release
	if release == nil {
		release = func() {}
	}
	return requestCtx, release, nil
}

type fixedClock struct{ value time.Time }

func (c fixedClock) Now() time.Time { return c.value }

// archiveAttemptCost converts a declared logical request count into the
// admission cost archive work must reserve. Archive admission cost models
// worst-case wire attempts, not logical requests: a 401-invalidate-retry
// spends two wire attempts for one logical request, so every admit call
// must declare double its logical cost or it can overspend the admitted
// ceiling and the live floor it protects.
func archiveAttemptCost(logical int) int {
	return logical * 2
}

// archivePreempted reports whether a provider read failed because live
// work preempted the admitted request context (canceled between admit and
// release), not because the caller itself is shutting down. Preemption is
// not a failure: the caller must record nothing and leave the work
// immediately claimable. requestCtx must be sampled before release() is
// called, since release always cancels its context and would otherwise
// make every request look preempted.
func archivePreempted(outerCtx, requestCtx context.Context) bool {
	return requestCtx.Err() != nil && outerCtx.Err() == nil
}
