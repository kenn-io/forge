package archive

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"go.kenn.io/middleman/internal/db"
	"go.kenn.io/middleman/internal/platform"
)

type ConfiguredRepositorySource interface {
	ConfiguredRepositories(context.Context) ([]platform.RepoRef, error)
}

// ItemSyncer is the existing live item-ingestion path used by archive
// hydration. Archive owns discovery, scheduling, and progress; it must not
// grow a second provider read/normalize/persist pipeline.
type ItemSyncer interface {
	ArchiveItemSyncCost(platform.Kind, db.ArchiveItemType) int
	SyncArchiveItem(
		context.Context, platform.RepoRef, db.ArchiveItemType, int,
	) (providerAttempted bool, err error)
}

type AdmissionResult struct {
	Allowed         bool
	RetryAt         *time.Time
	Context         context.Context
	FeatureDeferred *FeatureDeferral
	Complete        AdmissionComplete
	Detail          string
}

type AdmissionComplete func(cause error, providerAttempted bool) *FeatureDeferral

type FeatureDeferral struct {
	RetryAt time.Time
	Detail  string
}

type Admission interface {
	Admit(context.Context, platform.RepoRef, db.ArchiveItemType, int) (AdmissionResult, error)
}

type RetryDecision struct {
	Code    db.ArchiveErrorCode
	RetryAt *time.Time
}

type RetryClassifier interface {
	Classify(error, int, time.Time) RetryDecision
}

type Clock interface{ Now() time.Time }

type Service struct {
	db                  *db.DB
	registry            *platform.Registry
	admission           Admission
	configured          ConfiguredRepositorySource
	items               ItemSyncer
	retries             RetryClassifier
	clock               Clock
	scheduler           *Scheduler
	maintenanceInterval time.Duration
	wake                func()
}

type Status struct {
	Repo     platform.RepoRef
	RepoID   int64
	State    db.ArchiveRepoState
	Progress db.ArchiveRepoProgress
}

func NewService(
	database *db.DB,
	registry *platform.Registry,
	admission Admission,
	configured ConfiguredRepositorySource,
	retries RetryClassifier,
	clock Clock,
) (*Service, error) {
	if database == nil || registry == nil {
		return nil, errors.New("create archive service: database and registry are required")
	}
	if clock == nil {
		clock = wallClock{}
	}
	if retries == nil {
		retries = defaultRetryClassifier{}
	}
	service := &Service{
		db: database, registry: registry,
		admission: admission, configured: configured, retries: retries, clock: clock,
		scheduler:           NewScheduler(),
		maintenanceInterval: 5 * time.Minute,
	}
	if items, ok := configured.(ItemSyncer); ok {
		service.items = items
	} else if items, ok := admission.(ItemSyncer); ok {
		service.items = items
	}
	return service, nil
}

func (s *Service) SetMaintenanceInterval(interval time.Duration) {
	if interval > 0 {
		s.maintenanceInterval = interval
	}
}

func (s *Service) SetWake(wake func()) { s.wake = wake }

func (s *Service) EnsureConfigured(ctx context.Context, refs []platform.RepoRef) error {
	for _, ref := range refs {
		if err := validateArchiveRepoRef(ref); err != nil {
			return err
		}
		if _, err := s.registry.Provider(ref.Platform, ref.Host); err != nil {
			return err
		}
	}
	for _, ref := range refs {
		identity := platform.DBRepoIdentity(ref)
		var err error
		if identity.PlatformRepoID != "" {
			_, err = s.db.UpsertRepoByProviderID(ctx, identity)
		} else {
			_, err = s.db.UpsertRepo(ctx, identity)
		}
		if err != nil {
			return fmt.Errorf("seed archive repository %s: %w", archiveRepoIdentityKey(ref), err)
		}
	}
	resolved, err := s.resolveRepositories(ctx, refs, false)
	if err != nil {
		return err
	}
	ids := resolvedRepoIDs(resolved)
	if err := s.db.ReconcileDiscoveryArchives(ctx, ids, s.now()); err != nil {
		return err
	}
	for _, repo := range resolved {
		if err := s.db.ReconcileArchiveCoverage(
			ctx, repo.ID, archiveCoverage(repo.Capabilities), s.now(),
		); err != nil {
			return err
		}
	}
	return s.db.RequeueArchiveLifecycleDetails(ctx, ids, s.now())
}

// RetryAuthentication makes credential-blocked repositories eligible after a
// config reload without resetting their durable archive progress.
func (s *Service) RetryAuthentication(ctx context.Context, refs []platform.RepoRef) error {
	resolved, err := s.resolveRepositories(ctx, refs, false)
	if err != nil {
		return err
	}
	return s.db.RetryArchiveAuthentication(ctx, resolvedRepoIDs(resolved), s.now())
}

func (s *Service) Start(ctx context.Context, refs []platform.RepoRef) ([]Status, error) {
	resolved, err := s.resolveRepositories(ctx, refs, true)
	if err != nil {
		return nil, err
	}
	for _, repo := range resolved {
		if !repo.Capabilities.HasHistoricalInventory() {
			return nil, platform.UnsupportedCapability(
				repo.Ref.Platform, repo.Ref.Host, "historical_inventory",
			)
		}
	}
	if err := s.db.StartFullArchives(ctx, resolvedRepoIDs(resolved), s.now()); err != nil {
		return nil, err
	}
	for _, repo := range resolved {
		if err := s.db.ReconcileArchiveCoverage(ctx, repo.ID, archiveCoverage(repo.Capabilities), s.now()); err != nil {
			return nil, err
		}
	}
	if len(resolved) > 0 && s.wake != nil {
		s.wake()
	}
	return s.statusResolved(ctx, resolved)
}

func (s *Service) StartAll(ctx context.Context) ([]Status, error) {
	refs, err := s.configuredRepositories(ctx)
	if err != nil {
		return nil, err
	}
	return s.Start(ctx, refs)
}

func (s *Service) Pause(ctx context.Context, refs []platform.RepoRef) ([]Status, error) {
	resolved, err := s.resolveRepositories(ctx, refs, false)
	if err != nil {
		return nil, err
	}
	if err := s.db.PauseArchives(ctx, resolvedRepoIDs(resolved), s.now()); err != nil {
		return nil, err
	}
	return s.statusResolved(ctx, resolved)
}

func (s *Service) PauseAll(ctx context.Context) ([]Status, error) {
	refs, err := s.configuredRepositories(ctx)
	if err != nil {
		return nil, err
	}
	return s.Pause(ctx, refs)
}

func (s *Service) Status(ctx context.Context, refs []platform.RepoRef) ([]Status, error) {
	if len(refs) == 0 {
		return s.statusAll(ctx)
	}
	resolved, err := s.resolveRepositories(ctx, refs, false)
	if err != nil {
		return nil, err
	}
	return s.statusResolved(ctx, resolved)
}

func (s *Service) configuredRepositories(ctx context.Context) ([]platform.RepoRef, error) {
	if s.configured == nil {
		return nil, errors.New("archive configured repository source is required")
	}
	refs, err := s.configured.ConfiguredRepositories(ctx)
	if err != nil {
		return nil, fmt.Errorf("list configured archive repositories: %w", err)
	}
	return refs, nil
}

func (s *Service) statusAll(ctx context.Context) ([]Status, error) {
	states, err := s.db.ListArchiveRepoStates(ctx, nil)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, len(states))
	for i := range states {
		ids[i] = states[i].RepoID
	}
	progress, err := s.db.GetArchiveProgress(ctx, db.ArchiveProgressOpts{RepoIDs: ids, Now: s.now()})
	if err != nil {
		return nil, err
	}
	byID := make(map[int64]db.ArchiveRepoProgress, len(progress))
	for _, item := range progress {
		byID[item.RepoID] = item
	}
	statuses := make([]Status, 0, len(states))
	for _, state := range states {
		repo, err := s.db.GetRepoByID(ctx, state.RepoID)
		if err != nil {
			return nil, err
		}
		if repo == nil {
			continue
		}
		statuses = append(statuses, Status{
			Repo: platform.RepoRef{
				Platform: platform.Kind(repo.Platform), Host: repo.PlatformHost,
				Owner: repo.Owner, Name: repo.Name, RepoPath: repo.RepoPath,
				PlatformExternalID: repo.PlatformRepoID, WebURL: repo.WebURL,
				CloneURL: repo.CloneURL, DefaultBranch: repo.DefaultBranch,
			},
			RepoID: state.RepoID, State: state, Progress: byID[state.RepoID],
		})
	}
	sort.Slice(statuses, func(i, j int) bool {
		return archiveRepoIdentityKey(statuses[i].Repo) < archiveRepoIdentityKey(statuses[j].Repo)
	})
	return statuses, nil
}

func (s *Service) statusResolved(ctx context.Context, repos []resolvedRepository) ([]Status, error) {
	progress, err := s.db.GetArchiveProgress(ctx, db.ArchiveProgressOpts{RepoIDs: resolvedRepoIDs(repos), Now: s.now()})
	if err != nil {
		return nil, err
	}
	byID := make(map[int64]db.ArchiveRepoProgress, len(progress))
	for _, item := range progress {
		byID[item.RepoID] = item
	}
	states, err := s.db.ListArchiveRepoStates(ctx, resolvedRepoIDs(repos))
	if err != nil {
		return nil, err
	}
	stateByID := make(map[int64]db.ArchiveRepoState, len(states))
	for _, state := range states {
		stateByID[state.RepoID] = state
	}
	statuses := make([]Status, 0, len(repos))
	for _, repo := range repos {
		item, ok := byID[repo.ID]
		if !ok {
			return nil, &db.ArchiveRepoStateNotFoundError{RepoIDs: []int64{repo.ID}}
		}
		statuses = append(statuses, Status{
			Repo: repo.Ref, RepoID: repo.ID, State: stateByID[repo.ID], Progress: item,
		})
	}
	return statuses, nil
}

type resolvedRepository struct {
	ID            int64
	Ref           platform.RepoRef
	Issues        platform.IssuePageReader
	MergeRequests platform.MergeRequestPageReader
	Capabilities  platform.ArchiveCapabilities
}

func (s *Service) resolveRepositories(ctx context.Context, refs []platform.RepoRef, requireArchive bool) ([]resolvedRepository, error) {
	if len(refs) == 0 {
		return []resolvedRepository{}, nil
	}
	resolved := make([]resolvedRepository, 0, len(refs))
	seen := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		if err := validateArchiveRepoRef(ref); err != nil {
			return nil, err
		}
		key := archiveRepoIdentityKey(ref)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		provider, err := s.registry.Provider(ref.Platform, ref.Host)
		if err != nil {
			return nil, err
		}
		issues, err := s.registry.IssuePageReader(ref.Platform, ref.Host)
		if err != nil && requireArchive {
			return nil, err
		}
		mergeRequests, err := s.registry.MergeRequestPageReader(ref.Platform, ref.Host)
		if err != nil && requireArchive {
			return nil, err
		}
		repo, err := s.db.GetRepoByIdentity(ctx, platform.DBRepoIdentity(ref))
		if err != nil {
			return nil, fmt.Errorf("resolve archive repository %s: %w", key, err)
		}
		if repo == nil {
			return nil, &platform.Error{
				Code: platform.ErrCodeInvalidRepoRef, Provider: ref.Platform,
				PlatformHost: ref.Host, Field: "repository",
				Err: errors.New("repository is not configured"),
			}
		}
		resolved = append(resolved, resolvedRepository{
			ID: repo.ID, Ref: ref, Issues: issues, MergeRequests: mergeRequests,
			Capabilities: provider.Capabilities().Archive,
		})
	}
	sort.Slice(resolved, func(i, j int) bool {
		return archiveRepoIdentityKey(resolved[i].Ref) < archiveRepoIdentityKey(resolved[j].Ref)
	})
	return resolved, nil
}

func validateArchiveRepoRef(ref platform.RepoRef) error {
	if ref.Platform == "" || strings.TrimSpace(ref.Host) == "" || strings.TrimSpace(ref.Owner) == "" || strings.TrimSpace(ref.Name) == "" {
		return platform.ProviderContract(ref.Platform, ref.Host, "archive_repository_identity", errors.New("provider, host, owner, and repository are required"))
	}
	return nil
}

func archiveRepoIdentityKey(ref platform.RepoRef) string {
	return strings.Join([]string{string(ref.Platform), ref.Host, ref.Owner, ref.Name}, "\x00")
}

func resolvedRepoIDs(repos []resolvedRepository) []int64 {
	ids := make([]int64, 0, len(repos))
	for _, repo := range repos {
		ids = append(ids, repo.ID)
	}
	return ids
}

func archiveCoverage(caps platform.ArchiveCapabilities) db.ArchiveCoverageSet {
	return db.ArchiveCoverageSet{
		Issues:         coverageValue(caps.HistoricalIssues),
		MergeRequests:  coverageValue(caps.HistoricalMergeRequests),
		Comments:       coverageValue(caps.OrdinaryComments),
		Reviews:        coverageValue(caps.SubmittedReviews),
		InlineComments: coverageValue(caps.InlineReviewComments),
	}
}

func coverageValue(supported bool) db.ArchiveCoverage {
	if supported {
		return db.ArchiveCoverageSupported
	}
	return db.ArchiveCoverageUnsupported
}

func (s *Service) now() time.Time { return s.clock.Now().UTC() }

type wallClock struct{}

func (wallClock) Now() time.Time { return time.Now().UTC() }

// pageScopedProviderFailure reports provider errors that indict only the
// current scan or dataset rather than the repository: page-contract
// violations from the validating wrapper (echoed cursor, malformed page,
// wrong-parent item, malformed lookup outcome) and page-limit exhaustion.
// Repository-wide contract failures — wrong repository identity, capability
// misdeclaration, authentication — stay repository-scoped.
func pageScopedProviderFailure(err error) bool {
	if errors.Is(err, platform.ErrPageLimit) {
		return true
	}
	var platformErr *platform.Error
	if !errors.As(err, &platformErr) {
		return false
	}
	if platformErr.Code != platform.ErrCodeProviderContract {
		return false
	}
	switch platformErr.Field {
	case "archive_page", "archive_page_cursor",
		"item_number", "event_number", "thread_number",
		"archive_lookup_outcome", "archive_lookup_destination", "lookup_destination":
		return true
	default:
		return false
	}
}

// scanBlockCode names the durable block reason for a page-scoped failure.
// Page-limit exhaustion is the deliberately ambiguous page_bound; everything
// else detected on the page itself is an invalid-cursor style contract break.
func scanBlockCode(err error) string {
	if errors.Is(err, platform.ErrPageLimit) {
		return "page_bound"
	}
	return "invalid_cursor"
}

type defaultRetryClassifier struct{}

func (defaultRetryClassifier) Classify(err error, attempt int, now time.Time) RetryDecision {
	return defaultArchiveRetryDecision(err, attempt, now)
}

func defaultArchiveRetryDecision(err error, attempt int, now time.Time) RetryDecision {
	switch {
	case errors.Is(err, platform.ErrArchiveAttemptBudget):
		// A refused wire attempt means this admitted request ran out of its
		// per-attempt allowance, not that the repository or its contract is
		// broken. Some providers (GitLab) wrap the transport error as a
		// default invalid_repo_ref, so this case must precede the
		// contract-error branch below to keep the refusal a transient budget
		// deferral rather than a repository block.
		delay := time.Minute << min(attempt, 6)
		retryAt := now.Add(delay)
		return RetryDecision{Code: db.ArchiveErrorCodeTransient, RetryAt: &retryAt}
	case errors.Is(err, platform.ErrMissingToken), errors.Is(err, platform.ErrPermissionDenied):
		return RetryDecision{Code: db.ArchiveErrorCodeAuthentication}
	case errors.Is(err, platform.ErrUnsupportedCapability), errors.Is(err, platform.ErrProviderContract),
		errors.Is(err, platform.ErrPageLimit),
		errors.Is(err, platform.ErrInvalidRepoRef), errors.Is(err, platform.ErrInvalidArgument):
		return RetryDecision{Code: db.ArchiveErrorCodeRepoBlocked}
	case errors.Is(err, platform.ErrRateLimited):
		var platformErr *platform.Error
		if errors.As(err, &platformErr) && platformErr.ResetAt != nil {
			reset := platformErr.ResetAt.UTC()
			return RetryDecision{Code: db.ArchiveErrorCodeBudgetExhausted, RetryAt: &reset}
		}
	}
	delay := time.Minute << min(attempt, 6)
	retryAt := now.Add(delay)
	return RetryDecision{Code: db.ArchiveErrorCodeTransient, RetryAt: &retryAt}
}
