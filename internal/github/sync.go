package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	gh "github.com/google/go-github/v84/github"
	"go.kenn.io/middleman/internal/config"
	"go.kenn.io/middleman/internal/db"
	"go.kenn.io/middleman/internal/gitclone"
	"go.kenn.io/middleman/internal/platform"
	platformgithub "go.kenn.io/middleman/internal/platform/github"
	"golang.org/x/sync/singleflight"
)

func parseInt64(raw string) (int64, error) {
	return strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
}

// SyncStatus holds the current state of the sync engine.
type SyncStatus struct {
	Running     bool      `json:"running"`
	CurrentRepo string    `json:"current_repo,omitempty"`
	Progress    string    `json:"progress,omitempty"`
	LastRunAt   time.Time `json:"last_run_at,omitzero"`
	LastError   string    `json:"last_error,omitempty"`
}

// DiffSyncErrorCode categorizes the reason a diff sync failed. The frontend
// uses this category to render a user-facing message that does not leak local
// clone paths, refs, SHAs, or git stderr.
type DiffSyncErrorCode string

const (
	// DiffSyncCodeCloneUnavailable means the local bare clone could not be
	// created or updated (network failure, disk full, permission denied).
	DiffSyncCodeCloneUnavailable DiffSyncErrorCode = "clone_unavailable"
	// DiffSyncCodeCommitUnreachable means a commit needed to compute the diff
	// (PR head, merge commit, or its first parent) is not present in the local
	// clone and could not be fetched.
	DiffSyncCodeCommitUnreachable DiffSyncErrorCode = "commit_unreachable"
	// DiffSyncCodeMergeBaseFailed means git merge-base could not compute the
	// fork point between the PR head and the base.
	DiffSyncCodeMergeBaseFailed DiffSyncErrorCode = "merge_base_failed"
	// DiffSyncCodeInternal covers database failures and other unexpected
	// internal errors during diff computation.
	DiffSyncCodeInternal DiffSyncErrorCode = "internal"
)

// DiffSyncError reports a non-fatal failure to compute or update the diff SHAs
// for a PR. SyncMR returns this when only the diff portion of the sync failed:
// the PR row, timeline, and CI status were updated successfully, so callers
// should still treat the PR data as fresh, but the diff view will be stale or
// missing until the underlying problem is fixed.
//
// Code categorizes the failure for client-facing messaging via UserMessage.
// Err preserves the underlying detail for server-side logging only — never
// expose Err.Error() to API clients, since it can contain clone paths, refs,
// SHAs, and git stderr.
type DiffSyncError struct {
	Code DiffSyncErrorCode
	Err  error
}

func (e *DiffSyncError) Error() string {
	return fmt.Sprintf("diff sync failed (%s): %v", e.Code, e.Err)
}

func (e *DiffSyncError) Unwrap() error {
	return e.Err
}

// UserMessage returns a sanitized message safe to surface to API clients.
// It never includes clone paths, refs, SHAs, or other internal details from
// the underlying error.
func (e *DiffSyncError) UserMessage() string {
	switch e.Code {
	case DiffSyncCodeCloneUnavailable:
		return "Diff data is unavailable: the local repository clone could not be prepared."
	case DiffSyncCodeCommitUnreachable:
		return "Diff data is unavailable: a required commit is missing from the local clone."
	case DiffSyncCodeMergeBaseFailed:
		return "Diff data is unavailable: could not determine the merge base for this pull request."
	case DiffSyncCodeInternal:
		return "Diff data is unavailable: internal error while updating diff data."
	default:
		return "Diff data is unavailable."
	}
}

// RepoRef identifies a repository on a configured provider.
type RepoRef struct {
	Platform           platform.Kind
	Owner              string
	Name               string
	PlatformHost       string
	RepoPath           string
	PlatformRepoID     int64
	PlatformExternalID string
	WebURL             string
	CloneURL           string
	DefaultBranch      string
}

// RepoSyncResult holds the outcome of syncing a single repo.
type RepoSyncResult struct {
	Platform     platform.Kind
	Owner        string
	Name         string
	PlatformHost string
	Error        string // empty on success
}

// WatchedMR identifies a merge request to sync on a fast interval.
type WatchedMR struct {
	Owner        string
	Name         string
	Number       int
	Platform     platform.Kind
	PlatformHost string
}

// defaultParallelism is the worker pool size used by RunOnce when
// SetParallelism has not been called. Bounded so we don't burst the
// per-host GitHub rate limit / abuse-detection thresholds.
const defaultParallelism = 4

// Display-name cache parameters. Display names rarely change,
// so the success TTL is long enough to skip lookups across many
// sync passes; failures use a shorter TTL so a transient 404
// does not suppress a real retry for hours. The size bound is
// well above any realistic author set for a fixed repo list.
const (
	displayNameCacheSize  = 1024
	displayNameSuccessTTL = 24 * time.Hour
	displayNameFailureTTL = 15 * time.Minute
)

const syncProgressLogInterval = 100
const largeRepoBulkGraphQLThreshold = syncProgressLogInterval
const (
	defaultBranchActivityRetention  = 90 * 24 * time.Hour
	defaultBranchActivityMaxCommits = 5000
)

type itemSyncProgressLogger struct {
	repo   RepoRef
	source string
	item   string
	total  int
}

type listFetchProgressLogger struct {
	repo    RepoRef
	source  string
	item    string
	total   int
	fetched int
	started bool
}

func newIssueSyncProgressLogger(repo RepoRef, source string, total int) itemSyncProgressLogger {
	return newItemSyncProgressLogger(repo, source, "issue", total)
}

func newMergeRequestSyncProgressLogger(repo RepoRef, source string, total int) itemSyncProgressLogger {
	return newItemSyncProgressLogger(repo, source, "merge request", total)
}

func newItemSyncProgressLogger(
	repo RepoRef,
	source string,
	item string,
	total int,
) itemSyncProgressLogger {
	progress := itemSyncProgressLogger{repo: repo, source: source, item: item, total: total}
	if progress.enabled() {
		progress.log(progress.item+" sync started", 0)
	}
	return progress
}

func (p itemSyncProgressLogger) record(processed int) {
	if !p.enabled() || processed >= p.total || processed%syncProgressLogInterval != 0 {
		return
	}
	p.log(p.item+" sync progress", processed)
}

func (p itemSyncProgressLogger) done() {
	if p.enabled() {
		p.log(p.item+" sync completed", p.total)
	}
}

func (p itemSyncProgressLogger) enabled() bool {
	return p.total >= syncProgressLogInterval
}

func (p itemSyncProgressLogger) log(message string, processed int) {
	slog.Info(message,
		"repo", p.repo.Owner+"/"+p.repo.Name,
		"platform", string(repoPlatform(p.repo)),
		"host", repoHost(p.repo),
		"source", p.source,
		"processed", processed,
		"total", p.total,
	)
}

func newIssueListFetchProgressLogger(repo RepoRef, source string) *listFetchProgressLogger {
	return newListFetchProgressLogger(repo, source, "issue")
}

func newMergeRequestListFetchProgressLogger(repo RepoRef, source string) *listFetchProgressLogger {
	return newListFetchProgressLogger(repo, source, "merge request")
}

func newListFetchProgressLogger(repo RepoRef, source, item string) *listFetchProgressLogger {
	return &listFetchProgressLogger{repo: repo, source: source, item: item}
}

func (p *listFetchProgressLogger) setTotal(total int) {
	if p != nil && total > 0 {
		p.total = total
	}
}

func (p *listFetchProgressLogger) recordPage(fetched int, hasMore bool) {
	if p == nil || fetched <= 0 {
		return
	}
	p.fetched += fetched
	if !p.started {
		if !hasMore && p.fetched < syncProgressLogInterval {
			return
		}
		p.started = true
		p.log(p.item + " list fetch started")
		return
	}
	if hasMore {
		p.log(p.item + " list fetch progress")
	}
}

func (p *listFetchProgressLogger) done() {
	if p != nil && p.started {
		if p.total == 0 {
			p.total = p.fetched
		}
		p.log(p.item + " list fetch completed")
	}
}

func (p *listFetchProgressLogger) log(message string) {
	attrs := []any{
		"repo", p.repo.Owner + "/" + p.repo.Name,
		"platform", string(repoPlatform(p.repo)),
		"host", repoHost(p.repo),
		"source", p.source,
		"fetched", p.fetched,
	}
	if p.total > 0 {
		attrs = append(attrs, "total", p.total)
	}
	slog.Info(message, attrs...)
}

// Syncer periodically pulls PR data from GitHub into SQLite.
type Syncer struct {
	clients                  *platform.Registry
	db                       *db.DB
	clones                   *gitclone.Manager
	rateTrackers             map[string]*RateTracker    // provider/host bucket -> tracker
	budgets                  map[string]*SyncBudget     // provider/host bucket -> budget
	fetchers                 map[string]*GraphQLFetcher // host -> GraphQL fetcher
	repos                    []RepoRef
	reposMu                  sync.Mutex
	interval                 time.Duration
	watchInterval            time.Duration
	watchedMRs               []WatchedMR
	watchMu                  sync.Mutex
	branchActivityMu         sync.RWMutex
	branchActivityRetention  time.Duration
	branchActivityMaxCommits int
	parallelism              atomic.Int32
	running                  atomic.Bool
	status                   atomic.Value // stores *SyncStatus
	stopCh                   chan struct{}
	stopOnce                 sync.Once
	wg                       sync.WaitGroup
	// lifecycleMu serializes TriggerRun registration with Stop so
	// no wg.Add can happen after Stop begins wg.Wait.
	lifecycleMu        sync.Mutex
	stopped            bool                 // guarded by lifecycleMu
	nextSyncAfter      map[string]time.Time // provider/host bucket -> next eligible background sync time
	nextWatchSyncAfter map[string]time.Time // provider/host bucket -> next eligible watch-sync time
	// displayNames is a bounded TTL + LRU cache for resolved
	// GitHub display names. It spans the Syncer's lifetime so
	// cache hits survive across sync runs; per-entry TTL
	// handles profile-name changes without an explicit flush.
	displayNames     *displayNameCache
	displayNameGroup singleflight.Group // dedups concurrent GetUser calls
	onMRSynced       func(owner, name string, mr *db.MergeRequest)
	onSyncCompleted  func(results []RepoSyncResult)
	onStatusChange   func(status *SyncStatus)
	// statusMu serializes publishStatus so worker goroutines
	// can't interleave updates and deliver out-of-order snapshots
	// to SSE subscribers.
	statusMu sync.Mutex

	// failedRepos tracks repos whose last sync had a partial failure
	// (a per-PR, per-issue, or closure-detection step failed after
	// the ETag cache was populated by a successful 200 list fetch).
	// Values are failScope bitmasks indicating which path(s) failed.
	// The next sync cycle consults this set at the top of doSyncRepo
	// and forces an unconditional refetch of the list endpoints so
	// the failed items get re-applied from a fresh 200 response
	// instead of being skipped by a silent 304. Keyed by
	// "host/owner/name". Cleared on the next successful sync.
	failedRepos sync.Map

	// runCtx is the syncer's lifetime context. It is canceled in
	// Stop so in-flight RunOnce / TriggerRun goroutines observe
	// cancellation and unblock any long-running GitHub calls. Both
	// Start and TriggerRun derive their goroutine context from
	// runCtx (merged with any caller context), so Stop can unblock
	// the work it spawned regardless of whether the caller's ctx
	// is still live. runCtxMu guards lazy init and the Stop
	// handoff.
	runCtx    context.Context
	runCancel context.CancelFunc
	runCtxMu  sync.Mutex

	commentRefreshMu         sync.Mutex
	pendingPRCommentSyncs    []queuedPRCommentSync
	pendingIssueCommentSyncs []queuedIssueCommentSync
}

type queuedPRCommentSync struct {
	repo   RepoRef
	number int
}

type queuedIssueCommentSync struct {
	repo   RepoRef
	number int
}

// ensureRunCtx lazily initializes runCtx/runCancel. Safe to call
// multiple times; the first caller wins and later calls are no-ops.
func (s *Syncer) ensureRunCtx() context.Context {
	s.runCtxMu.Lock()
	defer s.runCtxMu.Unlock()
	if s.runCtx == nil {
		s.runCtx, s.runCancel = context.WithCancel(context.Background())
	}
	return s.runCtx
}

// mergeWithRunCtx returns a context that is canceled when either the
// caller's ctx or the syncer's lifetime ctx is canceled. The returned
// cancel function must be called to release resources. Used by
// TriggerRun so ad-hoc runs respect both the caller's deadline and
// Stop's global cancellation signal.
func (s *Syncer) mergeWithRunCtx(caller context.Context) (context.Context, context.CancelFunc) {
	runCtx := s.ensureRunCtx()
	merged, cancel := context.WithCancel(caller)
	go func() {
		select {
		case <-runCtx.Done():
			cancel()
		case <-merged.Done():
		}
	}()
	return merged, cancel
}

// failScope is a bitmask indicating which sync paths failed.
type failScope uint8

const (
	failMR     failScope = 1 << iota // PR/MR sync path failed
	failIssues                       // issue sync path failed
)

// markRepoFailed records that the most recent sync of this repo hit
// a partial failure after the ETag cache may have been populated, so
// the next cycle must force an unconditional refetch of the affected
// list endpoints. Matched by clearRepoFailed on a clean cycle.
func (s *Syncer) markRepoFailed(repo RepoRef, scope failScope) {
	key := repoFailKey(repo)
	for {
		prev, ok := s.failedRepos.Load(key)
		merged := scope
		if ok {
			merged |= prev.(failScope)
		}
		if ok {
			if s.failedRepos.CompareAndSwap(key, prev, merged) {
				return
			}
		} else {
			if _, loaded := s.failedRepos.LoadOrStore(key, merged); !loaded {
				return
			}
		}
		// Another goroutine raced us; retry.
	}
}

// clearRepoFailed clears the partial-failure flag after a clean
// doSyncRepo pass.
func (s *Syncer) clearRepoFailed(repo RepoRef) {
	s.failedRepos.Delete(repoFailKey(repo))
}

// repoFailKey returns the sync.Map key for a repo. Includes provider
// and host so multi-provider and multi-host setups don't cross-invalidate.
func repoFailKey(repo RepoRef) string {
	return string(repoPlatform(repo)) + "/" + repoHost(repo) + "/" +
		strings.ToLower(repo.Owner) + "/" + strings.ToLower(repo.Name)
}

func (s *Syncer) replaceMergeRequestLabels(
	ctx context.Context,
	repoID, mrID int64,
	labels []db.Label,
) error {
	if err := s.db.ReplaceMergeRequestLabels(ctx, repoID, mrID, labels); err != nil {
		return fmt.Errorf("replace merge request labels: %w", err)
	}
	return nil
}

func (s *Syncer) replaceIssueLabels(
	ctx context.Context,
	repoID, issueID int64,
	labels []db.Label,
) error {
	if err := s.db.ReplaceIssueLabels(ctx, repoID, issueID, labels); err != nil {
		return fmt.Errorf("replace issue labels: %w", err)
	}
	return nil
}

// consumeRepoFailed returns the failScope bitmask for a previously
// failed repo. Returns 0 if the repo had no failure. The flag remains
// set until a subsequent successful sync explicitly clears it.
func (s *Syncer) consumeRepoFailed(repo RepoRef) failScope {
	v, ok := s.failedRepos.Load(repoFailKey(repo))
	if !ok {
		return 0
	}
	return v.(failScope)
}

// publishStatus stores a status snapshot and invokes the
// onStatusChange callback if one is registered. Used in place of
// s.status.Store so SSE subscribers see every state transition.
func (s *Syncer) publishStatus(status *SyncStatus) {
	s.statusMu.Lock()
	defer s.statusMu.Unlock()
	s.status.Store(status)
	if s.onStatusChange != nil {
		s.onStatusChange(status)
	}
}

// NewSyncer creates a Syncer that polls the given repos on the
// given interval. clients maps host -> Client; rateTrackers maps
// host -> RateTracker. Both may contain nil values. clones may
// be nil. budgets maps host -> SyncBudget; nil or empty disables
// detail drain and backfill. Budgets are created by the caller
// (typically main.go) and wired into each Client's HTTP transport
// at construction time so every sync-context RoundTrip is
// automatically counted.
func NewSyncer(
	clients map[string]Client,
	database *db.DB,
	clones *gitclone.Manager,
	repos []RepoRef,
	interval time.Duration,
	rateTrackers map[string]*RateTracker,
	budgets map[string]*SyncBudget,
) *Syncer {
	return NewSyncerWithRegistry(
		registryFromGitHubClients(clients),
		database,
		clones,
		repos,
		interval,
		rateTrackers,
		budgets,
	)
}

func NewSyncerWithRegistry(
	registry *platform.Registry,
	database *db.DB,
	clones *gitclone.Manager,
	repos []RepoRef,
	interval time.Duration,
	rateTrackers map[string]*RateTracker,
	budgets map[string]*SyncBudget,
) *Syncer {
	if registry == nil {
		registry, _ = platform.NewRegistry()
	}
	if rateTrackers == nil {
		rateTrackers = make(map[string]*RateTracker)
	}
	if budgets == nil {
		budgets = make(map[string]*SyncBudget)
	}

	s := &Syncer{
		clients:                  registry,
		db:                       database,
		clones:                   clones,
		rateTrackers:             rateTrackers,
		budgets:                  budgets,
		repos:                    repos,
		interval:                 interval,
		branchActivityRetention:  defaultBranchActivityRetention,
		branchActivityMaxCommits: defaultBranchActivityMaxCommits,
		nextSyncAfter:            make(map[string]time.Time),
		nextWatchSyncAfter:       make(map[string]time.Time),
		stopCh:                   make(chan struct{}),
		displayNames: newDisplayNameCache(
			displayNameCacheSize,
			displayNameSuccessTTL,
			displayNameFailureTTL,
		),
	}
	s.parallelism.Store(defaultParallelism)
	s.status.Store(&SyncStatus{})

	// Wire budget reset to rate tracker window resets.
	for h, rt := range rateTrackers {
		if b, ok := budgets[h]; ok && rt != nil {
			rt.SetOnWindowReset(b.Reset)
		}
	}

	return s
}

type gitHubClientProvider struct {
	host   string
	client Client
}

type githubLabelClient interface {
	ListRepoLabels(ctx context.Context, owner, repo string) ([]*gh.Label, error)
	ReplaceIssueLabels(ctx context.Context, owner, repo string, number int, names []string) ([]*gh.Label, error)
}

func registryFromGitHubClients(clients map[string]Client) *platform.Registry {
	registry, err := platform.NewRegistry()
	if err != nil {
		panic(fmt.Sprintf("create empty provider registry: %v", err))
	}
	for host, client := range clients {
		if client == nil {
			continue
		}
		provider := gitHubClientProvider{
			host:   canonicalRepoHost(host),
			client: client,
		}
		_ = registry.Register(provider)
	}
	return registry
}

func NewProviderRegistry(
	clients map[string]Client,
	providers ...platform.Provider,
) (*platform.Registry, error) {
	registry := registryFromGitHubClients(clients)
	for _, provider := range providers {
		if err := registry.Register(provider); err != nil {
			return nil, err
		}
	}
	return registry, nil
}

func (p gitHubClientProvider) Platform() platform.Kind {
	return platform.KindGitHub
}

func (p gitHubClientProvider) Host() string {
	return p.host
}

func (p gitHubClientProvider) Capabilities() platform.Capabilities {
	_, labels := p.client.(githubLabelClient)
	return platform.Capabilities{
		ReadRepositories:      true,
		ReadMergeRequests:     true,
		ReadIssues:            true,
		ReadComments:          true,
		ReadReleases:          true,
		ReadCI:                true,
		ReadLabels:            labels,
		CommentMutation:       true,
		StateMutation:         true,
		MergeMutation:         true,
		ReviewMutation:        true,
		WorkflowApproval:      true,
		ReadyForReview:        true,
		IssueMutation:         true,
		LabelMutation:         labels,
		ReviewDraftMutation:   true,
		NativeMultilineRanges: true,
		SupportedReviewActions: []platform.ReviewAction{
			platform.ReviewActionComment,
			platform.ReviewActionApprove,
			platform.ReviewActionRequestChanges,
		},
	}
}

func (p gitHubClientProvider) GitHubClient() Client {
	return p.client
}

func (p gitHubClientProvider) GetRepository(
	ctx context.Context,
	ref platform.RepoRef,
) (platform.Repository, error) {
	repo, err := p.client.GetRepository(ctx, ref.Owner, ref.Name)
	if err != nil {
		return platform.Repository{}, err
	}
	owner := ref.Owner
	if repo.GetOwner().GetLogin() != "" {
		owner = repo.GetOwner().GetLogin()
	}
	viewerCanMerge := gitHubViewerCanMerge(repo)
	return platform.Repository{
		Ref: platform.RepoRef{
			Platform:           platform.KindGitHub,
			Host:               p.host,
			Owner:              canonicalRepoOwner(owner),
			Name:               canonicalRepoName(repo.GetName()),
			RepoPath:           canonicalRepoOwner(owner) + "/" + canonicalRepoName(repo.GetName()),
			PlatformID:         repo.GetID(),
			PlatformExternalID: repo.GetNodeID(),
			WebURL:             repo.GetHTMLURL(),
			CloneURL:           repo.GetCloneURL(),
			DefaultBranch:      repo.GetDefaultBranch(),
		},
		PlatformID:         repo.GetID(),
		PlatformExternalID: repo.GetNodeID(),
		Description:        repo.GetDescription(),
		Private:            repo.GetPrivate(),
		Archived:           repo.GetArchived(),
		MergeSettings: &platform.RepositoryMergeSettings{
			AllowSquashMerge: repo.GetAllowSquashMerge(),
			AllowMergeCommit: repo.GetAllowMergeCommit(),
			AllowRebaseMerge: repo.GetAllowRebaseMerge(),
		},
		ViewerCanMerge: viewerCanMerge,
		DefaultBranch:  repo.GetDefaultBranch(),
		WebURL:         repo.GetHTMLURL(),
		CloneURL:       repo.GetCloneURL(),
	}, nil
}

func gitHubViewerCanMerge(repo *gh.Repository) *bool {
	if repo == nil || repo.Permissions == nil {
		return nil
	}
	canMerge := repo.Permissions.GetPush() ||
		repo.Permissions.GetMaintain() ||
		repo.Permissions.GetAdmin()
	return &canMerge
}

func (p gitHubClientProvider) ListRepositories(
	ctx context.Context,
	owner string,
	_ platform.RepositoryListOptions,
) ([]platform.Repository, error) {
	repos, err := p.client.ListRepositoriesByOwner(ctx, owner)
	if err != nil {
		return nil, err
	}
	out := make([]platform.Repository, 0, len(repos))
	for _, repo := range repos {
		repoOwner := owner
		if repo.GetOwner().GetLogin() != "" {
			repoOwner = repo.GetOwner().GetLogin()
		}
		repoName := repo.GetName()
		out = append(out, platform.Repository{
			Ref: platform.RepoRef{
				Platform:           platform.KindGitHub,
				Host:               p.host,
				Owner:              canonicalRepoOwner(repoOwner),
				Name:               canonicalRepoName(repoName),
				RepoPath:           canonicalRepoOwner(repoOwner) + "/" + canonicalRepoName(repoName),
				PlatformID:         repo.GetID(),
				PlatformExternalID: repo.GetNodeID(),
				WebURL:             repo.GetHTMLURL(),
				CloneURL:           repo.GetCloneURL(),
				DefaultBranch:      repo.GetDefaultBranch(),
			},
			PlatformID:         repo.GetID(),
			PlatformExternalID: repo.GetNodeID(),
			Description:        repo.GetDescription(),
			Private:            repo.GetPrivate(),
			Archived:           repo.GetArchived(),
			DefaultBranch:      repo.GetDefaultBranch(),
			WebURL:             repo.GetHTMLURL(),
			CloneURL:           repo.GetCloneURL(),
		})
	}
	return out, nil
}

func (p gitHubClientProvider) ListOpenMergeRequests(
	ctx context.Context,
	ref platform.RepoRef,
) ([]platform.MergeRequest, error) {
	prs, err := p.client.ListOpenPullRequests(ctx, ref.Owner, ref.Name)
	if err != nil {
		return nil, err
	}
	out := make([]platform.MergeRequest, 0, len(prs))
	for _, pr := range prs {
		mr, err := platformgithub.NormalizePullRequest(ref, pr)
		if err != nil {
			return nil, err
		}
		out = append(out, mr)
	}
	return out, nil
}

func (p gitHubClientProvider) GetMergeRequest(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
) (platform.MergeRequest, error) {
	_, mr, err := p.GetGitHubPullRequest(ctx, ref, number)
	return mr, err
}

func (p gitHubClientProvider) GetGitHubPullRequest(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
) (*gh.PullRequest, platform.MergeRequest, error) {
	pr, err := p.client.GetPullRequest(ctx, ref.Owner, ref.Name, number)
	if err != nil {
		return nil, platform.MergeRequest{}, err
	}
	mr, err := platformgithub.NormalizePullRequest(ref, pr)
	if err != nil {
		return nil, platform.MergeRequest{}, err
	}
	return pr, mr, nil
}

func (p gitHubClientProvider) ListMergeRequestEvents(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
) ([]platform.MergeRequestEvent, error) {
	comments, err := p.client.ListIssueComments(ctx, ref.Owner, ref.Name, number)
	if err != nil {
		return nil, err
	}
	reviews, err := p.client.ListReviews(ctx, ref.Owner, ref.Name, number)
	if err != nil {
		return nil, err
	}
	commits, err := p.client.ListCommits(ctx, ref.Owner, ref.Name, number)
	if err != nil {
		return nil, err
	}
	timelineEvents, err := p.client.ListPullRequestTimelineEvents(ctx, ref.Owner, ref.Name, number)
	if err != nil {
		slog.Warn("github provider timeline event fetch failed",
			"repo", ref.DisplayName(),
			"number", number,
			"err", err,
		)
		timelineEvents = nil
	}

	out := make([]platform.MergeRequestEvent, 0, len(comments)+len(reviews)+len(commits)+len(timelineEvents))
	for _, comment := range comments {
		out = append(out, platformgithub.NormalizeCommentEvent(ref, number, comment))
	}
	for _, review := range reviews {
		out = append(out, platformgithub.NormalizeReviewEvent(ref, number, review))
	}
	for _, commit := range commits {
		out = append(out, platformgithub.NormalizeCommitEvent(ref, number, commit))
	}
	for _, timelineEvent := range timelineEvents {
		event := platformgithub.NormalizeTimelineEvent(ref, number, platformgithub.PullRequestTimelineEvent{
			NodeID:               timelineEvent.NodeID,
			EventType:            timelineEvent.EventType,
			Actor:                timelineEvent.Actor,
			Assignee:             timelineEvent.Assignee,
			CreatedAt:            timelineEvent.CreatedAt,
			DeletedCommentAuthor: timelineEvent.DeletedCommentAuthor,
			BeforeSHA:            timelineEvent.BeforeSHA,
			AfterSHA:             timelineEvent.AfterSHA,
			Ref:                  timelineEvent.Ref,
			PreviousTitle:        timelineEvent.PreviousTitle,
			CurrentTitle:         timelineEvent.CurrentTitle,
			PreviousRefName:      timelineEvent.PreviousRefName,
			CurrentRefName:       timelineEvent.CurrentRefName,
			SourceType:           timelineEvent.SourceType,
			SourceOwner:          timelineEvent.SourceOwner,
			SourceRepo:           timelineEvent.SourceRepo,
			SourceNumber:         timelineEvent.SourceNumber,
			SourceTitle:          timelineEvent.SourceTitle,
			SourceURL:            timelineEvent.SourceURL,
			IsCrossRepository:    timelineEvent.IsCrossRepository,
			WillCloseTarget:      timelineEvent.WillCloseTarget,
		})
		if event != nil {
			out = append(out, *event)
		}
	}
	return out, nil
}

func (p gitHubClientProvider) ListOpenIssues(
	ctx context.Context,
	ref platform.RepoRef,
) ([]platform.Issue, error) {
	issues, err := p.ListOpenGitHubIssues(ctx, ref)
	if err != nil {
		return nil, err
	}
	out := make([]platform.Issue, 0, len(issues))
	for _, issue := range issues {
		normalized, err := platformgithub.NormalizeIssue(ref, issue)
		if err != nil {
			return nil, err
		}
		out = append(out, normalized)
	}
	return out, nil
}

func (p gitHubClientProvider) ListOpenGitHubIssues(
	ctx context.Context,
	ref platform.RepoRef,
) ([]*gh.Issue, error) {
	return p.client.ListOpenIssues(ctx, ref.Owner, ref.Name)
}

func (p gitHubClientProvider) ListLabels(
	ctx context.Context,
	ref platform.RepoRef,
) (platform.LabelCatalog, error) {
	client, ok := p.client.(githubLabelClient)
	if !ok {
		return platform.LabelCatalog{}, platform.UnsupportedCapability(platform.KindGitHub, p.host, "read_labels")
	}
	labels, err := client.ListRepoLabels(ctx, ref.Owner, ref.Name)
	if err != nil {
		return platform.LabelCatalog{}, err
	}
	return platform.LabelCatalog{Labels: platformgithub.NormalizeLabels(ref, labels)}, nil
}

func (p gitHubClientProvider) GetIssue(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
) (platform.Issue, error) {
	issue, err := p.GetGitHubIssue(ctx, ref, number)
	if err != nil {
		return platform.Issue{}, err
	}
	return platformgithub.NormalizeIssue(ref, issue)
}

func (p gitHubClientProvider) GetGitHubIssue(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
) (*gh.Issue, error) {
	return p.client.GetIssue(ctx, ref.Owner, ref.Name, number)
}

func (p gitHubClientProvider) ListIssueEvents(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
) ([]platform.IssueEvent, error) {
	comments, err := p.client.ListIssueComments(ctx, ref.Owner, ref.Name, number)
	if err != nil {
		return nil, err
	}
	var timelineEvents []PullRequestTimelineEvent
	if timelineClient, ok := p.client.(issueTimelineLister); ok {
		timelineEvents, err = timelineClient.ListIssueTimelineEvents(ctx, ref.Owner, ref.Name, number)
		if err != nil {
			slog.Warn("github provider issue timeline event fetch failed",
				"repo", ref.DisplayName(),
				"number", number,
				"err", err,
			)
			timelineEvents = nil
		}
	}

	out := make([]platform.IssueEvent, 0, len(comments)+len(timelineEvents))
	for _, comment := range comments {
		out = append(out, platformgithub.NormalizeIssueCommentEvent(ref, number, comment))
	}
	for _, timelineEvent := range timelineEvents {
		event := platformgithub.NormalizeIssueTimelineEvent(ref, number, platformgithub.PullRequestTimelineEvent{
			NodeID:    timelineEvent.NodeID,
			EventType: timelineEvent.EventType,
			Actor:     timelineEvent.Actor,
			Assignee:  timelineEvent.Assignee,
			CreatedAt: timelineEvent.CreatedAt,
		})
		if event != nil {
			out = append(out, *event)
		}
	}
	return out, nil
}

func (p gitHubClientProvider) CreateMergeRequestComment(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
	body string,
) (platform.MergeRequestEvent, error) {
	comment, err := p.client.CreateIssueComment(ctx, ref.Owner, ref.Name, number, body)
	if err != nil {
		return platform.MergeRequestEvent{}, err
	}
	if comment == nil {
		return platform.MergeRequestEvent{}, fmt.Errorf("provider returned no comment")
	}
	return platformgithub.NormalizeCommentEvent(ref, number, comment), nil
}

func (p gitHubClientProvider) EditMergeRequestComment(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
	commentID int64,
	body string,
) (platform.MergeRequestEvent, error) {
	comment, err := p.client.EditIssueComment(ctx, ref.Owner, ref.Name, commentID, body)
	if err != nil {
		return platform.MergeRequestEvent{}, err
	}
	if comment == nil {
		return platform.MergeRequestEvent{}, fmt.Errorf("provider returned no comment")
	}
	return platformgithub.NormalizeCommentEvent(ref, number, comment), nil
}

func (p gitHubClientProvider) CreateIssueComment(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
	body string,
) (platform.IssueEvent, error) {
	comment, err := p.client.CreateIssueComment(ctx, ref.Owner, ref.Name, number, body)
	if err != nil {
		return platform.IssueEvent{}, err
	}
	if comment == nil {
		return platform.IssueEvent{}, fmt.Errorf("provider returned no comment")
	}
	return platformgithub.NormalizeIssueCommentEvent(ref, number, comment), nil
}

func (p gitHubClientProvider) EditIssueComment(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
	commentID int64,
	body string,
) (platform.IssueEvent, error) {
	comment, err := p.client.EditIssueComment(ctx, ref.Owner, ref.Name, commentID, body)
	if err != nil {
		return platform.IssueEvent{}, err
	}
	if comment == nil {
		return platform.IssueEvent{}, fmt.Errorf("provider returned no comment")
	}
	return platformgithub.NormalizeIssueCommentEvent(ref, number, comment), nil
}

func (p gitHubClientProvider) SetMergeRequestState(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
	state string,
) (platform.MergeRequest, error) {
	ghPR, err := p.client.EditPullRequest(
		ctx, ref.Owner, ref.Name, number, EditPullRequestOpts{State: &state},
	)
	if err != nil {
		return platform.MergeRequest{}, err
	}
	if ghPR == nil {
		return platform.MergeRequest{}, fmt.Errorf("provider returned no pull request")
	}
	return platformgithub.NormalizePullRequest(ref, ghPR)
}

func (p gitHubClientProvider) SetIssueState(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
	state string,
) (platform.Issue, error) {
	ghIssue, err := p.client.EditIssue(ctx, ref.Owner, ref.Name, number, state)
	if err != nil {
		return platform.Issue{}, err
	}
	if ghIssue == nil {
		return platform.Issue{}, fmt.Errorf("provider returned no issue")
	}
	return platformgithub.NormalizeIssue(ref, ghIssue)
}

func (p gitHubClientProvider) MergeMergeRequest(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
	commitTitle string,
	commitMessage string,
	method string,
) (platform.MergeResult, error) {
	result, err := p.client.MergePullRequest(
		ctx, ref.Owner, ref.Name, number, commitTitle, commitMessage, method,
	)
	if err != nil {
		return platform.MergeResult{}, err
	}
	if result == nil {
		return platform.MergeResult{}, fmt.Errorf("provider returned no merge result")
	}
	return platform.MergeResult{
		Merged:  result.GetMerged(),
		SHA:     result.GetSHA(),
		Message: result.GetMessage(),
	}, nil
}

func (p gitHubClientProvider) ApproveWorkflow(
	ctx context.Context,
	ref platform.RepoRef,
	runID string,
) error {
	parsed, err := parseInt64(runID)
	if err != nil {
		return err
	}
	return p.client.ApproveWorkflowRun(ctx, ref.Owner, ref.Name, parsed)
}

func (p gitHubClientProvider) MarkReadyForReview(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
) (platform.MergeRequest, error) {
	pr, err := p.client.MarkPullRequestReadyForReview(ctx, ref.Owner, ref.Name, number)
	if err != nil {
		return platform.MergeRequest{}, err
	}
	if pr == nil {
		return platform.MergeRequest{}, fmt.Errorf("provider returned no pull request")
	}
	return platformgithub.NormalizePullRequest(ref, pr)
}

func (p gitHubClientProvider) CreateIssue(
	ctx context.Context,
	ref platform.RepoRef,
	title string,
	body string,
) (platform.Issue, error) {
	issue, err := p.client.CreateIssue(ctx, ref.Owner, ref.Name, title, body)
	if err != nil {
		return platform.Issue{}, err
	}
	if issue == nil {
		return platform.Issue{}, fmt.Errorf("provider returned no issue")
	}
	return platformgithub.NormalizeIssue(ref, issue)
}

func (p gitHubClientProvider) SetMergeRequestLabels(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
	names []string,
) ([]platform.Label, error) {
	return p.setIssueLikeLabels(ctx, ref, number, names)
}

func (p gitHubClientProvider) SetIssueLabels(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
	names []string,
) ([]platform.Label, error) {
	return p.setIssueLikeLabels(ctx, ref, number, names)
}

func (p gitHubClientProvider) setIssueLikeLabels(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
	names []string,
) ([]platform.Label, error) {
	client, ok := p.client.(githubLabelClient)
	if !ok {
		return nil, platform.UnsupportedCapability(platform.KindGitHub, p.host, "label_mutation")
	}
	labels, err := client.ReplaceIssueLabels(ctx, ref.Owner, ref.Name, number, names)
	if err != nil {
		return nil, err
	}
	return platformgithub.NormalizeLabels(ref, labels), nil
}

func (p gitHubClientProvider) ApproveMergeRequest(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
	body string,
) (platform.MergeRequestEvent, error) {
	review, err := p.client.CreateReview(ctx, ref.Owner, ref.Name, number, "APPROVE", body)
	if err != nil {
		return platform.MergeRequestEvent{}, err
	}
	if review == nil {
		return platform.MergeRequestEvent{}, fmt.Errorf("provider returned no review")
	}
	return platformgithub.NormalizeReviewEvent(ref, number, review), nil
}

func (p gitHubClientProvider) PublishDiffReviewDraft(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
	input platform.PublishDiffReviewDraftInput,
) (*platform.PublishedDiffReview, error) {
	event, err := githubReviewEvent(input.Action)
	if err != nil {
		return nil, err
	}
	comments := make([]*gh.DraftReviewComment, 0, len(input.Comments))
	for _, comment := range input.Comments {
		comments = append(comments, githubDraftReviewComment(comment))
	}
	review, err := p.client.CreateReviewWithComments(
		ctx,
		ref.Owner,
		ref.Name,
		number,
		event,
		input.Body,
		githubReviewHeadSHA(input),
		comments,
	)
	if err != nil {
		return nil, err
	}
	if review == nil {
		return nil, fmt.Errorf("provider returned no review")
	}
	submittedAt := review.GetSubmittedAt() // zero Timestamp when GitHub omits submitted_at
	return &platform.PublishedDiffReview{
		ProviderReviewID: strconv.FormatInt(review.GetID(), 10),
		SubmittedAt:      submittedAt.Time,
	}, nil
}

func githubReviewEvent(action platform.ReviewAction) (string, error) {
	switch action {
	case platform.ReviewActionComment:
		return "COMMENT", nil
	case platform.ReviewActionApprove:
		return "APPROVE", nil
	case platform.ReviewActionRequestChanges:
		return "REQUEST_CHANGES", nil
	default:
		return "", fmt.Errorf("unsupported github review action %q", action)
	}
}

func githubReviewHeadSHA(input platform.PublishDiffReviewDraftInput) string {
	if input.HeadSHA != "" {
		return input.HeadSHA
	}
	for _, comment := range input.Comments {
		if comment.Range.DiffHeadSHA != "" {
			return comment.Range.DiffHeadSHA
		}
		if comment.Range.CommitSHA != "" {
			return comment.Range.CommitSHA
		}
	}
	return ""
}

func githubDraftReviewComment(comment platform.LocalDiffReviewDraftComment) *gh.DraftReviewComment {
	lineRange := comment.Range
	side := strings.ToUpper(lineRange.Side)
	next := &gh.DraftReviewComment{
		Path: &lineRange.Path,
		Body: &comment.Body,
		Side: &side,
		Line: &lineRange.Line,
	}
	if lineRange.StartLine != nil && lineRange.StartSide != "" {
		startSide := strings.ToUpper(lineRange.StartSide)
		next.StartSide = &startSide
		next.StartLine = lineRange.StartLine
	}
	return next
}

func (p gitHubClientProvider) EditMergeRequestContent(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
	title *string,
	body *string,
) (platform.MergeRequest, error) {
	pr, err := p.client.EditPullRequest(
		ctx, ref.Owner, ref.Name, number, EditPullRequestOpts{Title: title, Body: body},
	)
	if err != nil {
		return platform.MergeRequest{}, err
	}
	if pr == nil {
		return platform.MergeRequest{}, fmt.Errorf("provider returned no pull request")
	}
	return platformgithub.NormalizePullRequest(ref, pr)
}

func (p gitHubClientProvider) EditIssueContent(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
	title *string,
	body *string,
) (platform.Issue, error) {
	ghIssue, err := p.client.EditIssueContent(
		ctx, ref.Owner, ref.Name, number, title, body,
	)
	if err != nil {
		return platform.Issue{}, err
	}
	if ghIssue == nil {
		return platform.Issue{}, fmt.Errorf("provider returned no issue")
	}
	return platformgithub.NormalizeIssue(ref, ghIssue)
}

// SetWatchInterval sets the fast-sync interval for watched MRs.
// Must be called before Start.
func (s *Syncer) SetWatchInterval(d time.Duration) {
	s.watchInterval = d
}

// HasDiffSync reports whether the syncer has a clone manager configured
// and is therefore expected to populate diff SHAs for tracked PRs. The
// HTTP layer uses this to decide whether a missing diff is a sync issue
// worth warning about, or simply a deployment that opted out of diffs.
func (s *Syncer) HasDiffSync() bool {
	return s.clones != nil
}

// SetWatchedMRs replaces the fast-sync watch list. Each watched
// MR is synced on the watch interval via SyncMR, independent of
// the bulk sync cycle.
func (s *Syncer) SetWatchedMRs(mrs []WatchedMR) {
	s.watchMu.Lock()
	s.watchedMRs = slices.Clone(mrs)
	s.watchMu.Unlock()
}

// SetOnMRSynced registers a callback invoked after each MR
// is upserted during a sync pass.
//
// Concurrency: RunOnce processes repos in parallel (see
// SetParallelism), so the callback may be invoked from up to
// `parallelism` goroutines concurrently. Implementations must
// be safe for concurrent use. The callback also runs on the
// goroutine that is mid-sync for a repo, so it must not block
// indefinitely or it will stall sync progress.
//
// Call SetOnMRSynced before Start/RunOnce. Mutating the hook
// while a sync is in flight is not safe.
func (s *Syncer) SetOnMRSynced(
	fn func(owner, name string, mr *db.MergeRequest),
) {
	s.onMRSynced = fn
}

// SetOnSyncCompleted registers a callback invoked at the end
// of each RunOnce pass with per-repo sync results.
//
// Concurrency: this hook fires once per RunOnce pass on the
// goroutine that drives RunOnce, so it is not invoked
// concurrently with itself. Call SetOnSyncCompleted before
// Start/RunOnce; mutating the hook while a sync is in flight
// is not safe.
func (s *Syncer) SetOnSyncCompleted(
	fn func(results []RepoSyncResult),
) {
	s.onSyncCompleted = fn
}

// SetParallelism sets the maximum number of repos synced
// concurrently in RunOnce. Values <= 0 are clamped to 1
// (sequential).
func (s *Syncer) SetParallelism(n int) {
	if n < 1 {
		n = 1
	}
	s.parallelism.Store(int32(n))
}

// SetBranchActivityLimits configures how much default-branch commit
// activity the syncer persists.
func (s *Syncer) SetBranchActivityLimits(
	retention time.Duration,
	maxCommits int,
) {
	if retention <= 0 {
		retention = defaultBranchActivityRetention
	}
	if maxCommits <= 0 {
		maxCommits = defaultBranchActivityMaxCommits
	}
	s.branchActivityMu.Lock()
	s.branchActivityRetention = retention
	s.branchActivityMaxCommits = maxCommits
	s.branchActivityMu.Unlock()
}

func (s *Syncer) branchActivityLimits() (time.Duration, int) {
	s.branchActivityMu.RLock()
	retention := s.branchActivityRetention
	maxCommits := s.branchActivityMaxCommits
	s.branchActivityMu.RUnlock()
	if retention <= 0 {
		retention = defaultBranchActivityRetention
	}
	if maxCommits <= 0 {
		maxCommits = defaultBranchActivityMaxCommits
	}
	return retention, maxCommits
}

// BranchActivityLimits reports the configured default-branch activity
// retention and per-branch commit cap.
func (s *Syncer) BranchActivityLimits() (time.Duration, int) {
	return s.branchActivityLimits()
}

// SetOnStatusChange registers a callback invoked whenever the
// sync status transitions (start, per-repo progress, rate-limit
// wait, completion). Used by the server to broadcast live sync
// state over SSE.
func (s *Syncer) SetOnStatusChange(fn func(status *SyncStatus)) {
	s.onStatusChange = fn
}

// SetFetchers registers GitHub GraphQL fetchers keyed by platform host.
func (s *Syncer) SetFetchers(fetchers map[string]*GraphQLFetcher) {
	s.fetchers = fetchers
}

// fetcherFor returns the GitHub GraphQL fetcher for a repo's host,
// or nil if none is configured.
func (s *Syncer) fetcherFor(repo RepoRef) *GraphQLFetcher {
	if s.fetchers == nil {
		return nil
	}
	if repoPlatform(repo) != platform.KindGitHub {
		return nil
	}
	host := repoHost(repo)
	return s.fetchers[host]
}

// TriggerRun kicks off a non-blocking ad-hoc sync on the Syncer's
// wait group so callers can request an immediate run without
// blocking the caller. Ad-hoc runs bypass the normal nextSyncAfter
// cadence gate, but still respect hard rate-limit pauses and the
// syncer's lifecycle: Stop cancels the merged context so any
// in-flight GitHub call unblocks, then waits for the goroutine to
// exit. The caller's ctx is honored too, so per-request deadlines
// still apply.
func (s *Syncer) TriggerRun(ctx context.Context) {
	s.lifecycleMu.Lock()
	if s.stopped {
		s.lifecycleMu.Unlock()
		return
	}
	merged, cancel := s.mergeWithRunCtx(ctx)
	s.wg.Add(1)
	s.lifecycleMu.Unlock()

	go func() {
		defer s.wg.Done()
		defer cancel()
		s.runOnce(merged, true)
	}()
}

func repoPlatform(repo RepoRef) platform.Kind {
	if repo.Platform != "" {
		return repo.Platform
	}
	return platform.KindGitHub
}

func repoHost(repo RepoRef) string {
	if repo.PlatformHost != "" {
		return canonicalRepoHost(repo.PlatformHost)
	}
	if host, ok := platform.DefaultHost(repoPlatform(repo)); ok {
		return host
	}
	return platform.DefaultGitHubHost
}

func rateBucketKeyFor(kind platform.Kind, host string) string {
	return RateBucketKey(string(kind), host)
}

func repoRateBucketKey(repo RepoRef) string {
	return rateBucketKeyFor(repoPlatform(repo), repoHost(repo))
}

func watchedMRRateBucketKey(mr WatchedMR) string {
	return rateBucketKeyFor(watchedMRPlatform(mr), watchedMRHost(mr))
}

func platformRepoRef(repo RepoRef) platform.RepoRef {
	repoPath := repo.RepoPath
	if repoPath == "" {
		repoPath = repo.Owner + "/" + repo.Name
	}
	return platform.RepoRef{
		Platform:           repoPlatform(repo),
		Host:               repoHost(repo),
		Owner:              repo.Owner,
		Name:               repo.Name,
		RepoPath:           repoPath,
		PlatformID:         repo.PlatformRepoID,
		PlatformExternalID: repo.PlatformExternalID,
		WebURL:             repo.WebURL,
		CloneURL:           repo.CloneURL,
		DefaultBranch:      repo.DefaultBranch,
	}
}

func cloneRemoteURL(repo RepoRef) string {
	if repo.CloneURL != "" {
		return repo.CloneURL
	}
	repoPath := repo.RepoPath
	if repoPath == "" {
		repoPath = strings.Trim(repo.Owner+"/"+repo.Name, "/")
	}
	return fmt.Sprintf("https://%s/%s.git", repoHost(repo), strings.Trim(repoPath, "/"))
}

func (s *Syncer) optionalGitHubClientFor(repo RepoRef) (Client, bool) {
	client, err := s.clientFor(repo)
	if err != nil {
		return nil, false
	}
	return client, true
}

// clientFor returns the legacy GitHub Client for the given repo's host.
// Repos with an empty host default to "github.com".
func (s *Syncer) clientFor(repo RepoRef) (Client, error) {
	host := repoHost(repo)
	provider, err := s.clients.Provider(repoPlatform(repo), host)
	if err != nil {
		return nil, fmt.Errorf("no client configured for host %s", host)
	}
	legacy, ok := provider.(interface{ GitHubClient() Client })
	if !ok || legacy.GitHubClient() == nil {
		return nil, fmt.Errorf("no GitHub client configured for host %s", host)
	}
	return legacy.GitHubClient(), nil
}

func (s *Syncer) mergeRequestReaderFor(repo RepoRef) (platform.MergeRequestReader, error) {
	return s.clients.MergeRequestReader(repoPlatform(repo), repoHost(repo))
}

func (s *Syncer) issueReaderFor(repo RepoRef) (platform.IssueReader, error) {
	return s.clients.IssueReader(repoPlatform(repo), repoHost(repo))
}

func (s *Syncer) labelReaderFor(repo RepoRef) (platform.LabelReader, error) {
	return s.clients.LabelReader(repoPlatform(repo), repoHost(repo))
}

func (s *Syncer) releaseReaderFor(repo RepoRef) (platform.ReleaseReader, error) {
	return s.clients.ReleaseReader(repoPlatform(repo), repoHost(repo))
}

func (s *Syncer) tagReaderFor(repo RepoRef) (platform.TagReader, error) {
	return s.clients.TagReader(repoPlatform(repo), repoHost(repo))
}

func (s *Syncer) ciReaderFor(repo RepoRef) (platform.CIReader, error) {
	return s.clients.CIReader(repoPlatform(repo), repoHost(repo))
}

// ClientForRepo returns the Client for a tracked repo by
// owner/name, or an error if the repo is not tracked.
func (s *Syncer) ClientForRepo(
	owner, name string,
) (Client, error) {
	s.reposMu.Lock()
	defer s.reposMu.Unlock()
	for _, r := range s.repos {
		if strings.EqualFold(r.Owner, owner) &&
			strings.EqualFold(r.Name, name) {
			return s.clientFor(r)
		}
	}
	return nil, fmt.Errorf(
		"repo %s/%s is not tracked", owner, name,
	)
}

// ClientForHost returns the Client for a specific host,
// or an error if no client is configured for that host.
func (s *Syncer) ClientForHost(
	host string,
) (Client, error) {
	return s.clientFor(RepoRef{PlatformHost: host})
}

func (s *Syncer) ProviderCapabilities(
	kind platform.Kind,
	host string,
) (platform.Capabilities, error) {
	if kind == "" {
		kind = platform.KindGitHub
	}
	if strings.TrimSpace(host) == "" {
		defaultHost, ok := platform.DefaultHost(kind)
		if !ok {
			return platform.Capabilities{}, platform.ProviderNotConfigured(kind, "")
		}
		host = defaultHost
	}
	return s.clients.Capabilities(kind, canonicalRepoHost(host))
}

func (s *Syncer) RepositoryReader(
	kind platform.Kind,
	host string,
) (platform.RepositoryReader, error) {
	return s.clients.RepositoryReader(kind, canonicalRepoHost(host))
}

// Registry returns the boot-time platform registry. Callers must not
// mutate the returned registry; it is shared by every sync codepath and
// rebuilt only on daemon restart.
func (s *Syncer) Registry() *platform.Registry {
	return s.clients
}

func (s *Syncer) LabelReader(
	kind platform.Kind,
	host string,
) (platform.LabelReader, error) {
	return s.clients.LabelReader(kind, canonicalRepoHost(host))
}

func (s *Syncer) CommentMutator(
	kind platform.Kind,
	host string,
) (platform.CommentMutator, error) {
	return s.clients.CommentMutator(kind, canonicalRepoHost(host))
}

func (s *Syncer) StateMutator(
	kind platform.Kind,
	host string,
) (platform.StateMutator, error) {
	return s.clients.StateMutator(kind, canonicalRepoHost(host))
}

func (s *Syncer) MergeMutator(
	kind platform.Kind,
	host string,
) (platform.MergeMutator, error) {
	return s.clients.MergeMutator(kind, canonicalRepoHost(host))
}

func (s *Syncer) WorkflowApprovalMutator(
	kind platform.Kind,
	host string,
) (platform.WorkflowApprovalMutator, error) {
	return s.clients.WorkflowApprovalMutator(kind, canonicalRepoHost(host))
}

func (s *Syncer) ReadyForReviewMutator(
	kind platform.Kind,
	host string,
) (platform.ReadyForReviewMutator, error) {
	return s.clients.ReadyForReviewMutator(kind, canonicalRepoHost(host))
}

func (s *Syncer) IssueMutator(
	kind platform.Kind,
	host string,
) (platform.IssueMutator, error) {
	return s.clients.IssueMutator(kind, canonicalRepoHost(host))
}

func (s *Syncer) LabelMutator(
	kind platform.Kind,
	host string,
) (platform.LabelMutator, error) {
	return s.clients.LabelMutator(kind, canonicalRepoHost(host))
}

func (s *Syncer) ReviewMutator(
	kind platform.Kind,
	host string,
) (platform.ReviewMutator, error) {
	return s.clients.ReviewMutator(kind, canonicalRepoHost(host))
}

func (s *Syncer) DiffReviewDraftMutator(
	kind platform.Kind,
	host string,
) (platform.DiffReviewDraftMutator, error) {
	return s.clients.DiffReviewDraftMutator(kind, canonicalRepoHost(host))
}

func (s *Syncer) DiffReviewThreadResolver(
	kind platform.Kind,
	host string,
) (platform.DiffReviewThreadResolver, error) {
	return s.clients.DiffReviewThreadResolver(kind, canonicalRepoHost(host))
}

func (s *Syncer) MergeRequestReviewThreadReader(
	kind platform.Kind,
	host string,
) (platform.MergeRequestReviewThreadReader, error) {
	return s.clients.MergeRequestReviewThreadReader(kind, canonicalRepoHost(host))
}

func (s *Syncer) MergeRequestContentMutator(
	kind platform.Kind,
	host string,
) (platform.MergeRequestContentMutator, error) {
	return s.clients.MergeRequestContentMutator(kind, canonicalRepoHost(host))
}

func (s *Syncer) IssueContentMutator(
	kind platform.Kind,
	host string,
) (platform.IssueContentMutator, error) {
	return s.clients.IssueContentMutator(kind, canonicalRepoHost(host))
}

func (s *Syncer) ResolveConfiguredRepo(
	ctx context.Context,
	repo config.Repo,
) (ConfiguredRepoStatus, []RepoRef, error) {
	return ResolveConfiguredRepoWithRegistry(ctx, s.clients, repo)
}

func (s *Syncer) trackedRepoOnHost(owner, name, host string) (RepoRef, bool) {
	if host == "" {
		host = "github.com"
	}
	s.reposMu.Lock()
	defer s.reposMu.Unlock()
	for _, r := range s.repos {
		rHost := repoHost(r)
		if strings.EqualFold(r.Owner, owner) &&
			strings.EqualFold(r.Name, name) &&
			strings.EqualFold(rHost, host) {
			return r, true
		}
	}
	return RepoRef{}, false
}

func (s *Syncer) trackedRepo(owner, name string) (RepoRef, bool, error) {
	s.reposMu.Lock()
	defer s.reposMu.Unlock()

	var matched RepoRef
	count := 0
	for _, r := range s.repos {
		if strings.EqualFold(r.Owner, owner) &&
			strings.EqualFold(r.Name, name) {
			matched = r
			count++
		}
	}
	if count == 0 {
		return RepoRef{}, false, nil
	}
	if count > 1 {
		return RepoRef{}, false, fmt.Errorf(
			"repo %s/%s is ambiguous across configured providers",
			owner, name,
		)
	}
	return matched, true, nil
}

func (s *Syncer) trackedRepoOnHostUnique(
	owner, name, host string,
) (RepoRef, bool, error) {
	if host == "" {
		host = "github.com"
	}
	s.reposMu.Lock()
	defer s.reposMu.Unlock()

	var matched RepoRef
	count := 0
	for _, r := range s.repos {
		rHost := repoHost(r)
		if strings.EqualFold(r.Owner, owner) &&
			strings.EqualFold(r.Name, name) &&
			strings.EqualFold(rHost, host) {
			matched = r
			count++
		}
	}
	if count == 0 {
		return RepoRef{}, false, nil
	}
	if count > 1 {
		return RepoRef{}, false, fmt.Errorf(
			"repo %s/%s on %s is ambiguous across configured providers",
			owner, name, host,
		)
	}
	return matched, true, nil
}

func (s *Syncer) trackedRepoByIdentity(
	kind platform.Kind,
	owner, name, host string,
) (RepoRef, bool) {
	if kind == "" {
		kind = platform.KindGitHub
	}
	host = repoHost(RepoRef{Platform: kind, PlatformHost: host})
	s.reposMu.Lock()
	defer s.reposMu.Unlock()
	for _, r := range s.repos {
		rHost := repoHost(r)
		if repoPlatform(r) == kind &&
			strings.EqualFold(r.Owner, owner) &&
			strings.EqualFold(r.Name, name) &&
			strings.EqualFold(rHost, host) {
			return r, true
		}
	}
	return RepoRef{}, false
}

func detailRepoKey(kind platform.Kind, host, owner, name string) string {
	if kind == "" {
		kind = platform.KindGitHub
	}
	host = repoHost(RepoRef{Platform: kind, PlatformHost: host})
	return string(kind) + "\x00" + host + "\x00" +
		strings.ToLower(owner) + "/" + strings.ToLower(name)
}

// hostFor returns the platform host for a repo identified by
// owner/name. Returns "github.com" if not found. Thread-safe.
func (s *Syncer) hostFor(owner, name string) string {
	s.reposMu.Lock()
	defer s.reposMu.Unlock()
	for _, r := range s.repos {
		if strings.EqualFold(r.Owner, owner) &&
			strings.EqualFold(r.Name, name) {
			if r.PlatformHost != "" {
				return r.PlatformHost
			}
			return "github.com"
		}
	}
	return "github.com"
}

// HostForRepo returns the platform host for a tracked repo.
// Thread-safe.
func (s *Syncer) HostForRepo(owner, name string) string {
	return s.hostFor(owner, name)
}

// SetRepos atomically replaces the list of repositories to sync.
func (s *Syncer) SetRepos(repos []RepoRef) {
	s.reposMu.Lock()
	s.repos = slices.Clone(repos)
	s.reposMu.Unlock()
}

// Start runs an immediate sync then launches a background ticker.
// It returns as soon as the goroutine is started; call Stop to shut it down.
// A second goroutine runs watched-MR fast-syncs on a shorter interval.
//
// The caller's ctx and the syncer's internal lifetime ctx (canceled
// by Stop) are both honored: either one unblocks any in-flight work.
func (s *Syncer) Start(ctx context.Context) {
	s.lifecycleMu.Lock()
	if s.stopped {
		s.lifecycleMu.Unlock()
		return
	}

	startMerged, startCancel := s.mergeWithRunCtx(ctx)
	s.wg.Add(1)

	watchInt := s.watchInterval
	if watchInt <= 0 {
		watchInt = 30 * time.Second
	}
	watchMerged, watchCancel := s.mergeWithRunCtx(ctx)
	s.wg.Add(1)
	s.lifecycleMu.Unlock()

	go func() {
		defer s.wg.Done()
		defer startCancel()
		s.RunOnce(startMerged)
		ticker := time.NewTicker(s.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				s.RunOnce(startMerged)
			case <-s.stopCh:
				return
			case <-startMerged.Done():
				return
			}
		}
	}()

	go func() {
		defer s.wg.Done()
		defer watchCancel()
		ticker := time.NewTicker(watchInt)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				s.syncWatchedMRs(watchMerged)
			case <-s.stopCh:
				return
			case <-watchMerged.Done():
				return
			}
		}
	}()
}

// syncWatchedMRs syncs each MR on the watch list via SyncMR.
// Fires onMRSynced (inside SyncMR) but not onSyncCompleted.
// Checks per-host rate limits before issuing API calls.
// hostEligibility computes which hosts are eligible for sync
// based on rate tracker state and the next-sync-after gate.
// hosts may contain duplicates; they are deduplicated internally.
func (s *Syncer) hostEligibility(
	hosts []string,
	nextAfter map[string]time.Time,
) map[string]bool {
	now := time.Now().UTC()
	eligible := make(map[string]bool, len(hosts))
	for _, host := range hosts {
		if _, checked := eligible[host]; checked {
			continue
		}
		rt := s.rateTrackers[host]
		if rt == nil {
			eligible[host] = true
			continue
		}
		if rt.IsPaused() {
			eligible[host] = false
			continue
		}
		if after, ok := nextAfter[host]; ok && now.Before(after) {
			eligible[host] = false
			continue
		}
		eligible[host] = true
	}
	return eligible
}

// advanceNextSync updates the next-sync-after gate for hosts
// that were eligible, using each host's current throttle factor.
func (s *Syncer) advanceNextSync(
	eligible map[string]bool,
	nextAfter map[string]time.Time,
	interval time.Duration,
) {
	now := time.Now()
	for host, ok := range eligible {
		if !ok {
			continue
		}
		rt := s.rateTrackers[host]
		if rt == nil {
			continue
		}
		nextAfter[host] = now.Add(
			interval * time.Duration(rt.ThrottleFactor()),
		)
	}
}

func (s *Syncer) syncWatchedMRs(ctx context.Context) {
	ctx = WithSyncBudget(ctx)

	s.watchMu.Lock()
	mrs := slices.Clone(s.watchedMRs)
	s.watchMu.Unlock()

	if len(mrs) == 0 {
		return
	}

	watchInt := s.watchInterval
	if watchInt <= 0 {
		watchInt = 30 * time.Second
	}
	watchBuckets := make([]string, len(mrs))
	for i, mr := range mrs {
		watchBuckets[i] = watchedMRRateBucketKey(mr)
	}
	eligibleBuckets := s.hostEligibility(
		watchBuckets, s.nextWatchSyncAfter,
	)

	// Check backoff once per provider/host bucket to avoid redundant checks.
	blockedBuckets := make(map[string]bool)
	for _, mr := range mrs {
		bucket := watchedMRRateBucketKey(mr)
		if _, checked := blockedBuckets[bucket]; checked {
			continue
		}
		if rt := s.rateTrackers[bucket]; rt != nil {
			if backoff, _ := rt.ShouldBackoff(); backoff {
				blockedBuckets[bucket] = true
				continue
			}
		}
		blockedBuckets[bucket] = false
	}

	for _, mr := range mrs {
		host := watchedMRHost(mr)
		bucket := watchedMRRateBucketKey(mr)
		if !eligibleBuckets[bucket] {
			slog.Debug("skipping fast-sync for throttled host",
				"host", host,
				"owner", mr.Owner,
				"name", mr.Name,
				"number", mr.Number,
			)
			continue
		}
		if blockedBuckets[bucket] {
			slog.Debug("skipping fast-sync for rate-limited host",
				"host", host,
				"owner", mr.Owner,
				"name", mr.Name,
				"number", mr.Number,
			)
			continue
		}
		if err := s.syncMRWithWatchedRef(ctx, mr); err != nil {
			slog.Warn("fast-sync watched MR failed",
				"owner", mr.Owner,
				"name", mr.Name,
				"number", mr.Number,
				"err", err,
			)
		}
	}

	s.advanceNextSync(
		eligibleBuckets, s.nextWatchSyncAfter, watchInt,
	)
}

func watchedMRPlatform(mr WatchedMR) platform.Kind {
	if mr.Platform != "" {
		return mr.Platform
	}
	return platform.KindGitHub
}

func watchedMRHost(mr WatchedMR) string {
	return repoHost(RepoRef{
		Platform:     watchedMRPlatform(mr),
		PlatformHost: mr.PlatformHost,
	})
}

func watchedMRKey(mr WatchedMR) string {
	return detailRepoKey(
		watchedMRPlatform(mr), watchedMRHost(mr), mr.Owner, mr.Name,
	) + fmt.Sprintf("#%d", mr.Number)
}

// stopGracePeriod bounds how long Stop will wait for in-flight work
// to exit after the syncer's lifetime context is canceled. If a
// misbehaving dependency ignores ctx, Stop gives up and logs a
// warning rather than deadlocking the caller.
const stopGracePeriod = 30 * time.Second

// Stop signals the background goroutine to exit. Safe to call
// multiple times. Cancels the syncer's lifetime context first so
// blocked RunOnce and TriggerRun goroutines can observe the
// cancellation and unwind their GitHub calls, then waits for the
// wait group up to stopGracePeriod. The bounded wait prevents Stop
// from hanging the process in pathological cases where a client
// ignores ctx.
func (s *Syncer) Stop() {
	s.stopOnce.Do(func() {
		s.lifecycleMu.Lock()
		s.stopped = true
		s.lifecycleMu.Unlock()

		close(s.stopCh)
		s.runCtxMu.Lock()
		cancel := s.runCancel
		s.runCtxMu.Unlock()
		if cancel != nil {
			cancel()
		}
	})

	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(stopGracePeriod):
		slog.Warn("syncer stop timed out; returning while work is still in flight",
			"grace", stopGracePeriod)
	}
}

// Status returns a snapshot of the current sync state.
func (s *Syncer) Status() *SyncStatus {
	return s.status.Load().(*SyncStatus)
}

// RateTrackers returns the per-host rate trackers map.
func (s *Syncer) RateTrackers() map[string]*RateTracker {
	return s.rateTrackers
}

// Budgets returns the per-host sync budgets map.
func (s *Syncer) Budgets() map[string]*SyncBudget {
	return s.budgets
}

// GQLRateTrackers returns per-provider/host GraphQL rate trackers
// extracted from the registered GraphQL fetchers. Hosts with
// nil fetchers or trackers are skipped.
func (s *Syncer) GQLRateTrackers() map[string]*RateTracker {
	result := make(map[string]*RateTracker, len(s.fetchers))
	for _, f := range s.fetchers {
		if f == nil {
			continue
		}
		if rt := f.RateTracker(); rt != nil {
			result[rt.BucketKey()] = rt
		}
	}
	return result
}

// runState holds the per-RunOnce mutable state shared by the
// worker pool. Extracted into a struct so runWorker can be a
// directly testable method instead of an inline closure.
type runState struct {
	completed *atomic.Int32
	maxShown  *atomic.Int32
	errMu     *sync.Mutex
	lastErr   *string
	// canceled is latched to true at the moment any goroutine
	// observes ctx cancellation while work is still outstanding.
	// RunOnce uses this flag (rather than a completed-count
	// heuristic) to decide whether the run was canceled, so a
	// misbehaving syncRepo that ignores ctx and returns success
	// cannot mask cancellation.
	canceled *atomic.Bool
	total    int
	// results is a preallocated slice indexed by repo position so
	// OnSyncCompleted receives results in the configured repo order
	// regardless of worker completion order. Each index is written
	// by exactly one worker, so no mutex is needed.
	results []RepoSyncResult
}

// repoWork pairs a repo with its index in the configured repo list
// so workers can write results to the correct preallocated slot.
type repoWork struct {
	index int
	repo  RepoRef
}

// runWorker drains the work channel until it is closed or ctx
// is canceled. It is the body of each goroutine spawned by
// RunOnce. Extracted from the inline closure so cancellation
// behavior can be unit-tested directly without racing against
// the dispatch loop.
func (s *Syncer) runWorker(
	ctx context.Context,
	work <-chan repoWork,
	state *runState,
) {
	for item := range work {
		repo := item.repo
		// Defense-in-depth against the dispatch race: the
		// dispatch loop pre-checks ctx before its select, but
		// a cancel can still land in the micro-window between
		// the pre-check and the select, in which case Go's
		// select may pick the send branch and hand this worker
		// a repo that should never have been enqueued. Bail
		// here before logging or starting any work, and latch
		// the canceled flag so RunOnce reports the run as
		// canceled regardless of how many repos happened to
		// finish in parallel.
		if ctx.Err() != nil {
			state.canceled.Store(true)
			return
		}
		if rt := s.rateTrackers[repoRateBucketKey(repo)]; rt != nil {
			if backoff, wait := rt.ShouldBackoff(); backoff {
				s.publishStatus(&SyncStatus{
					Running: true,
					Progress: fmt.Sprintf(
						"rate limited, waiting %s", wait,
					),
				})
				select {
				case <-time.After(wait):
				case <-ctx.Done():
					state.canceled.Store(true)
					return
				}
			}
		}
		repoName := repo.Owner + "/" + repo.Name
		slog.Info("syncing repo", "repo", repoName)
		if err := s.syncRepo(ctx, repo); err != nil {
			// Bail without counting this repo only when the
			// *run* context itself is canceled and the error
			// reflects that. Per-request timeouts also come
			// back as wrapped context.DeadlineExceeded but
			// must reach the normal error path so they're
			// captured in lastErr instead of being silently
			// dropped.
			if ctx.Err() != nil &&
				(errors.Is(err, context.Canceled) ||
					errors.Is(err, context.DeadlineExceeded)) {
				state.canceled.Store(true)
				return
			}
			errStr := err.Error()
			slog.Error("sync repo failed",
				"repo", repoName, "err", err,
			)
			state.errMu.Lock()
			*state.lastErr = errStr
			state.errMu.Unlock()
			// Each index is written by exactly one worker.
			state.results[item.index].Error = errStr
		}
		// Latch the canceled flag if ctx was canceled during
		// syncRepo. A misbehaving Client implementation can
		// ignore ctx and return nil (or a non-context error)
		// even after cancellation; without this check the run
		// would fall through to the success path and fire
		// onSyncCompleted for what the user asked to cancel.
		if ctx.Err() != nil {
			state.canceled.Store(true)
			return
		}
		done := state.completed.Add(1)
		s.publishMonotonicProgress(state, done)
	}
}

// publishMonotonicProgress publishes a progress update only if done
// is the highest value seen so far. Skips the final "total/total"
// because detail drain and backfill still run after index completes.
// Both worker completions and throttled-repo skips use this to
// guarantee SSE progress never regresses.
func (s *Syncer) publishMonotonicProgress(
	state *runState, done int32,
) {
	if int(done) >= state.total {
		return
	}
	for {
		cur := state.maxShown.Load()
		if done <= cur {
			return
		}
		if state.maxShown.CompareAndSwap(cur, done) {
			s.publishStatus(&SyncStatus{
				Running:  true,
				Progress: fmt.Sprintf("%d/%d", done, state.total),
			})
			return
		}
	}
}

// RunOnce performs a single sync pass across all configured repos.
// If a sync is already in progress it returns immediately (single-flight).
//
// Repos are synced in parallel using a bounded worker pool sized by
// SetParallelism (default defaultParallelism). The bound keeps the
// per-host GitHub rate limit and abuse-detection thresholds happy
// while still capturing most of the wall-clock win on network I/O.
func (s *Syncer) RunOnce(ctx context.Context) {
	s.runOnce(ctx, false)
}

func (s *Syncer) runOnce(
	ctx context.Context,
	bypassNextSyncAfter bool,
) {
	if !s.running.CompareAndSwap(false, true) {
		return
	}
	defer s.running.Store(false)

	// Mark context so the budget transport counts HTTP calls
	// made during background sync. User-initiated server
	// handler paths do not carry this key and are not counted.
	ctx = WithSyncBudget(ctx)

	s.reposMu.Lock()
	repos := slices.Clone(s.repos)
	s.reposMu.Unlock()

	total := len(repos)
	s.publishStatus(&SyncStatus{
		Running:  true,
		Progress: fmt.Sprintf("0/%d", total),
	})
	slog.Info("sync started", "repos", total)
	s.resetPendingCommentSyncs()

	workers := min(max(int(s.parallelism.Load()), 1), total)

	work := make(chan repoWork)
	results := make([]RepoSyncResult, total)
	for i, r := range repos {
		host := r.PlatformHost
		if host == "" {
			host = "github.com"
		}
		results[i] = RepoSyncResult{
			Platform:     repoPlatform(r),
			Owner:        r.Owner,
			Name:         r.Name,
			PlatformHost: host,
		}
	}

	repoBuckets := make([]string, len(repos))
	for i, r := range repos {
		repoBuckets[i] = repoRateBucketKey(r)
	}
	nextAfter := s.nextSyncAfter
	if bypassNextSyncAfter {
		nextAfter = nil
	}
	eligibleBuckets := s.hostEligibility(repoBuckets, nextAfter)

	var (
		completed atomic.Int32
		maxShown  atomic.Int32
		errMu     sync.Mutex
		lastErr   string
		canceled  atomic.Bool
		wg        sync.WaitGroup
	)

	state := &runState{
		completed: &completed,
		maxShown:  &maxShown,
		errMu:     &errMu,
		lastErr:   &lastErr,
		canceled:  &canceled,
		total:     total,
		results:   results,
	}
	for range workers {
		wg.Go(func() {
			s.runWorker(ctx, work, state)
		})
	}

dispatch:
	for i, r := range repos {
		bucket := repoRateBucketKey(r)
		if !eligibleBuckets[bucket] {
			results[i].Error = "skipped: rate limit throttled"
			done := completed.Add(1)
			s.publishMonotonicProgress(state, done)
			continue
		}
		// Check ctx before entering the select. Go's select picks
		// pseudo-randomly when both branches are ready, so a naked
		// `select { case work <- r: case <-ctx.Done(): }` can still
		// hand a repo to a ready worker after the run has been
		// canceled. The pre-check biases the loop toward cancel so
		// the dispatch reliably stops once ctx is done.
		if ctx.Err() != nil {
			canceled.Store(true)
			break dispatch
		}
		item := repoWork{index: i, repo: r}
		select {
		case work <- item:
		case <-ctx.Done():
			canceled.Store(true)
			break dispatch
		}
	}
	close(work)
	wg.Wait()

	s.advanceNextSync(
		eligibleBuckets, s.nextSyncAfter, s.interval,
	)

	// Detail drain: fetch full details for highest-priority items
	// within the per-host budget. Runs after index scan completes.
	if !canceled.Load() && ctx.Err() == nil {
		s.drainDetailQueue(ctx, eligibleBuckets)
	}

	// Backfill discovery: fetch closed items if budget allows.
	if !canceled.Load() && ctx.Err() == nil {
		for bucket, ok := range eligibleBuckets {
			if !ok {
				continue
			}
			s.runBackfillDiscovery(ctx, bucket, repos)
		}
	}

	if !canceled.Load() && ctx.Err() == nil {
		s.drainPendingCommentSyncs(ctx, eligibleBuckets)
	}

	// Use a latched flag (set by the dispatch loop and workers at
	// the moment they observe ctx cancellation) rather than a
	// completed-count heuristic. A misbehaving syncRepo that
	// ignores ctx and returns success would otherwise let the
	// run fall through to onSyncCompleted even though the user
	// asked to cancel. A cancel that races in strictly *after*
	// every worker finished and returned never latches the flag,
	// so the late-cancel-after-clean-sync case still reports
	// success.
	if canceled.Load() {
		err := ctx.Err()
		if err == nil {
			err = context.Canceled
		}
		slog.Info("sync canceled", "repos", total, "err", err)
		s.publishStatus(&SyncStatus{
			Running:   false,
			LastRunAt: time.Now().UTC(),
			LastError: err.Error(),
		})
		return
	}

	slog.Info("sync complete", "repos", total)

	if s.onSyncCompleted != nil {
		s.onSyncCompleted(results)
	}

	s.publishStatus(&SyncStatus{
		Running:   false,
		LastRunAt: time.Now().UTC(),
		LastError: lastErr,
	})
}

func (s *Syncer) syncRepoIdentity(ctx context.Context, repo RepoRef) (db.RepoIdentity, *platform.Repository, error) {
	identity := platform.DBRepoIdentity(platformRepoRef(repo))
	if identity.PlatformRepoID != "" {
		return identity, nil, nil
	}
	reader, err := s.clients.RepositoryReader(repoPlatform(repo), repoHost(repo))
	if err != nil {
		return db.RepoIdentity{}, nil, err
	}
	resolved, err := reader.GetRepository(ctx, platformRepoRef(repo))
	if err != nil {
		return db.RepoIdentity{}, nil, err
	}
	identity = platform.DBRepositoryIdentity(resolved)
	if identity.PlatformRepoID == "" {
		return db.RepoIdentity{}, nil, fmt.Errorf("provider returned no repo id")
	}
	return identity, &resolved, nil
}

// syncRepo syncs one repository: open PRs, timeline events, and stale closures.
func (s *Syncer) syncRepo(ctx context.Context, repo RepoRef) error {
	repoIdentity, resolvedRepo, err := s.syncRepoIdentity(ctx, repo)
	if err != nil {
		return fmt.Errorf("resolve repo identity %s/%s: %w", repo.Owner, repo.Name, err)
	}
	repoID, err := s.db.UpsertRepoByProviderID(ctx, repoIdentity)
	if err != nil {
		return fmt.Errorf("upsert repo %s/%s by provider id: %w", repo.Owner, repo.Name, err)
	}

	s.refreshRepoSettings(ctx, repo, repoID, resolvedRepo)

	if err := s.db.UpdateRepoSyncStarted(ctx, repoID, time.Now().UTC()); err != nil {
		return fmt.Errorf("mark sync started for %s/%s: %w", repo.Owner, repo.Name, err)
	}

	// Fetch bare clone before PR data so refs are available for merge-base.
	host := repoHost(repo)
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
		if err := s.clones.EnsureClone(ctx, host, repo.Owner, repo.Name, cloneRemoteURL(repo)); err != nil {
			slog.Warn("bare clone fetch failed",
				"repo", repo.Owner+"/"+repo.Name, "err", err,
			)
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

	syncErrStr := ""
	if syncErr != nil {
		syncErrStr = syncErr.Error()
	}
	if err := s.db.UpdateRepoSyncCompleted(ctx, repoID, time.Now().UTC(), syncErrStr); err != nil {
		slog.Error("mark sync completed", "repo", repo.Owner+"/"+repo.Name, "err", err)
	}

	return syncErr
}

func (s *Syncer) defaultBranchForActivity(ctx context.Context, repoID int64, repo RepoRef) string {
	repoRow, err := s.db.GetRepoByID(ctx, repoID)
	if err != nil {
		slog.Warn("get repo default branch failed",
			"repo", repo.Owner+"/"+repo.Name,
			"err", err,
		)
		return strings.TrimSpace(repo.DefaultBranch)
	}
	if repoRow != nil && strings.TrimSpace(repoRow.DefaultBranch) != "" {
		return strings.TrimSpace(repoRow.DefaultBranch)
	}
	return strings.TrimSpace(repo.DefaultBranch)
}

func (s *Syncer) syncDefaultBranchActivity(
	ctx context.Context,
	repo RepoRef,
	repoID int64,
	preferredBranch string,
	previousTip *db.BranchTip,
) {
	if s.clones == nil {
		return
	}
	host := repoHost(repo)
	branch, currentTip, err := s.clones.ResolveDefaultBranch(
		ctx,
		host,
		repo.Owner,
		repo.Name,
		preferredBranch,
	)
	if err != nil {
		slog.Warn("resolve default branch activity ref failed",
			"repo", repo.Owner+"/"+repo.Name,
			"branch", preferredBranch,
			"err", err,
		)
		return
	}
	if branch == "" || currentTip == "" {
		slog.Warn("default branch activity skipped: no branch resolved",
			"repo", repo.Owner+"/"+repo.Name,
			"branch", preferredBranch,
		)
		return
	}
	if previousTip == nil || previousTip.BranchName != branch {
		previousTip, err = s.db.GetBranchTip(ctx, repoID, branch)
		if err != nil {
			slog.Warn("get resolved default branch tip failed",
				"repo", repo.Owner+"/"+repo.Name,
				"branch", branch,
				"err", err,
			)
			return
		}
	}

	now := time.Now().UTC()
	retention, maxCommits := s.branchActivityLimits()
	retentionStart := now.Add(-retention)
	afterSHA := ""
	var beforeObservedAt time.Time
	forcePush := false
	if previousTip != nil && previousTip.TipSHA != "" {
		afterSHA = previousTip.TipSHA
		beforeObservedAt = previousTip.ObservedAt
		if previousTip.TipSHA != currentTip {
			ancestor, err := s.clones.IsAncestor(
				ctx,
				host,
				repo.Owner,
				repo.Name,
				previousTip.TipSHA,
				currentTip,
			)
			if err != nil {
				slog.Warn("check default branch ancestry failed",
					"repo", repo.Owner+"/"+repo.Name,
					"branch", branch,
					"err", err,
				)
				return
			}
			forcePush = !ancestor
		}
	}

	gitCommits, err := s.clones.ListBranchCommitsSince(
		ctx,
		host,
		repo.Owner,
		repo.Name,
		branch,
		retentionStart,
		afterSHA,
		maxCommits,
	)
	if err != nil {
		slog.Warn("list default branch commits failed",
			"repo", repo.Owner+"/"+repo.Name,
			"branch", branch,
			"err", err,
		)
		return
	}
	if err := s.db.UpsertBranchCommits(
		ctx,
		dbBranchCommits(repoID, branch, gitCommits),
	); err != nil {
		slog.Warn("upsert default branch commits failed",
			"repo", repo.Owner+"/"+repo.Name,
			"branch", branch,
			"err", err,
		)
		return
	}
	if forcePush {
		if err := s.db.InsertBranchForcePush(ctx, db.BranchForcePush{
			RepoID:           repoID,
			BranchName:       branch,
			BeforeSHA:        afterSHA,
			AfterSHA:         currentTip,
			BeforeObservedAt: beforeObservedAt,
			DetectedAt:       now,
		}); err != nil {
			slog.Warn("insert default branch force push failed",
				"repo", repo.Owner+"/"+repo.Name,
				"branch", branch,
				"err", err,
			)
			return
		}
	}
	if err := s.db.UpsertBranchTip(ctx, db.BranchTip{
		RepoID:     repoID,
		BranchName: branch,
		TipSHA:     currentTip,
		ObservedAt: now,
	}); err != nil {
		slog.Warn("upsert default branch tip failed",
			"repo", repo.Owner+"/"+repo.Name,
			"branch", branch,
			"err", err,
		)
		return
	}
	if err := s.db.PruneBranchActivity(ctx, retentionStart, maxCommits); err != nil {
		slog.Warn("prune default branch activity failed",
			"repo", repo.Owner+"/"+repo.Name,
			"branch", branch,
			"err", err,
		)
	}
}

func dbBranchCommits(
	repoID int64,
	branch string,
	commits []gitclone.Commit,
) []db.BranchCommit {
	out := make([]db.BranchCommit, 0, len(commits))
	for _, commit := range commits {
		out = append(out, db.BranchCommit{
			RepoID:         repoID,
			BranchName:     branch,
			CommitSHA:      commit.SHA,
			AuthorName:     commit.AuthorName,
			AuthorEmail:    commit.AuthorEmail,
			AuthoredAt:     commit.AuthoredAt,
			CommitterName:  commit.CommitterName,
			CommitterEmail: commit.CommitterEmail,
			CommittedAt:    commit.CommittedAt,
			Subject:        commit.Message,
		})
	}
	return out
}

func (s *Syncer) refreshRepoSettings(
	ctx context.Context,
	repo RepoRef,
	repoID int64,
	resolvedRepo *platform.Repository,
) {
	if resolvedRepo != nil {
		s.updateRepoSettingsFromProviderRepo(ctx, repoID, *resolvedRepo)
		return
	}

	if client, ok := s.optionalGitHubClientFor(repo); ok {
		ghRepo, err := client.GetRepository(ctx, repo.Owner, repo.Name)
		if err != nil {
			slog.Warn("get repo settings failed",
				"repo", repo.Owner+"/"+repo.Name, "err", err,
			)
			return
		}
		if canMerge := gitHubViewerCanMerge(ghRepo); canMerge != nil {
			_ = s.db.UpdateRepoSettings(ctx, repoID,
				ghRepo.GetAllowSquashMerge(),
				ghRepo.GetAllowMergeCommit(),
				ghRepo.GetAllowRebaseMerge(),
				*canMerge,
			)
			return
		}
		_ = s.db.UpdateRepoMergeSettings(ctx, repoID,
			ghRepo.GetAllowSquashMerge(),
			ghRepo.GetAllowMergeCommit(),
			ghRepo.GetAllowRebaseMerge(),
		)
		return
	}

	reader, err := s.clients.RepositoryReader(repoPlatform(repo), repoHost(repo))
	if err != nil {
		if errors.Is(err, platform.ErrUnsupportedCapability) || errors.Is(err, platform.ErrProviderNotConfigured) {
			return
		}
		slog.Warn("get repo settings reader failed",
			"repo", repo.Owner+"/"+repo.Name, "err", err,
		)
		return
	}
	providerRepo, err := reader.GetRepository(ctx, platformRepoRef(repo))
	if err != nil {
		slog.Warn("get repo settings failed",
			"repo", repo.Owner+"/"+repo.Name, "err", err,
		)
		return
	}
	s.updateRepoSettingsFromProviderRepo(ctx, repoID, providerRepo)
}

func (s *Syncer) updateRepoSettingsFromProviderRepo(
	ctx context.Context,
	repoID int64,
	repo platform.Repository,
) {
	if err := s.db.UpdateRepoProviderMetadata(ctx, repoID, db.RepoProviderMetadata{
		PlatformRepoID: repo.PlatformExternalID,
		WebURL:         repo.WebURL,
		CloneURL:       repo.CloneURL,
		DefaultBranch:  repo.DefaultBranch,
	}); err != nil {
		slog.Warn("update repo provider metadata failed",
			"repo", repo.Ref.DisplayName(), "err", err,
		)
	}
	if repo.MergeSettings != nil {
		settings := repo.MergeSettings
		if repo.ViewerCanMerge == nil {
			_ = s.db.UpdateRepoMergeSettings(ctx, repoID,
				settings.AllowSquashMerge,
				settings.AllowMergeCommit,
				settings.AllowRebaseMerge,
			)
			return
		}
		_ = s.db.UpdateRepoSettings(ctx, repoID,
			settings.AllowSquashMerge,
			settings.AllowMergeCommit,
			settings.AllowRebaseMerge,
			*repo.ViewerCanMerge,
		)
		return
	}
	if repo.ViewerCanMerge != nil {
		_ = s.db.UpdateRepoViewerCanMerge(ctx, repoID, *repo.ViewerCanMerge)
	}
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
	labels := platform.DBLabels(catalog.Labels, checkedAt)
	if err := s.db.ReplaceRepoLabelCatalog(ctx, repoID, labels, checkedAt); err != nil {
		slog.Warn("replace label catalog", "repo", repo.Owner+"/"+repo.Name, "err", err)
	}
}

func (s *Syncer) RefreshRepoLabelCatalog(ctx context.Context, repo db.Repo) error {
	ref := RepoRef{
		Platform:           platform.Kind(repo.Platform),
		PlatformHost:       repoProviderHostFromDB(repo),
		Owner:              repo.Owner,
		Name:               repo.Name,
		RepoPath:           repo.RepoPath,
		PlatformExternalID: repo.PlatformRepoID,
		CloneURL:           repo.CloneURL,
		WebURL:             repo.WebURL,
		DefaultBranch:      repo.DefaultBranch,
	}
	checkedAt := time.Now().UTC()
	reader, err := s.labelReaderFor(ref)
	if err != nil {
		_ = s.db.UpdateRepoLabelCatalogCheck(ctx, repo.ID, checkedAt, err.Error())
		return err
	}
	catalog, err := reader.ListLabels(ctx, platformRepoRef(ref))
	if err != nil {
		_ = s.db.UpdateRepoLabelCatalogCheck(ctx, repo.ID, checkedAt, err.Error())
		return err
	}
	if catalog.NotModified {
		return s.db.MarkRepoLabelCatalogSynced(ctx, repo.ID, checkedAt)
	}
	return s.db.ReplaceRepoLabelCatalog(ctx, repo.ID, platform.DBLabels(catalog.Labels, checkedAt), checkedAt)
}

const repoOverviewTimelineLimit = 30

func repoProviderHostFromDB(repo db.Repo) string {
	if repo.PlatformHost != "" {
		return repo.PlatformHost
	}
	kind := platform.Kind(repo.Platform)
	if kind == "" {
		kind = platform.KindGitHub
	}
	if host, ok := platform.DefaultHost(kind); ok {
		return host
	}
	return platform.DefaultGitHubHost
}

func (s *Syncer) syncRepoOverview(
	ctx context.Context,
	client Client,
	repo RepoRef,
	repoID int64,
	cloneFetchOK bool,
) {
	releases, err := client.ListReleases(ctx, repo.Owner, repo.Name, 10)
	if err != nil {
		slog.Warn("list repo releases failed",
			"repo", repo.Owner+"/"+repo.Name, "err", err,
		)
		return
	}

	selectedReleases := displayReleases(releases, 3)
	overview := db.RepoOverview{}
	for _, release := range selectedReleases {
		overview.Releases = append(overview.Releases, repoReleaseFromGitHub(release))
	}
	if len(overview.Releases) > 0 {
		overview.LatestRelease = &overview.Releases[0]
	}

	var timelineTags []string
	selectedTags := []*gh.RepositoryTag(nil)
	if len(selectedReleases) == 0 {
		tags, err := client.ListTags(ctx, repo.Owner, repo.Name, 3)
		if err != nil {
			slog.Warn("list repo tags failed",
				"repo", repo.Owner+"/"+repo.Name, "err", err,
			)
		} else {
			selectedTags = displayTags(tags, 3)
			for _, tag := range selectedTags {
				overview.Releases = append(overview.Releases, repoReleaseFromTag(
					repo.PlatformHost,
					repo.Owner,
					repo.Name,
					tag,
				))
			}
			if len(overview.Releases) > 0 {
				overview.LatestRelease = &overview.Releases[0]
			}
			for _, tag := range selectedTags {
				timelineTags = append(timelineTags, tag.GetName())
			}
		}
	} else {
		for _, release := range selectedReleases {
			timelineTags = append(timelineTags, release.GetTagName())
		}
	}

	s.addRepoOverviewTimeline(ctx, repo, cloneFetchOK, timelineTags, &overview)

	if err := s.db.UpsertRepoOverview(ctx, repoID, overview); err != nil {
		slog.Warn("store repo overview failed",
			"repo", repo.Owner+"/"+repo.Name, "err", err,
		)
	}
}

func (s *Syncer) syncProviderRepoOverview(
	ctx context.Context,
	repo RepoRef,
	repoID int64,
	cloneFetchOK bool,
) {
	releaseReader, err := s.releaseReaderFor(repo)
	if err != nil {
		if !errors.Is(err, platform.ErrUnsupportedCapability) {
			slog.Warn("resolve repo release reader failed",
				"repo", repo.Owner+"/"+repo.Name, "err", err,
			)
		}
		return
	}

	releases, err := releaseReader.ListReleases(ctx, platformRepoRef(repo))
	if err != nil {
		slog.Warn("list repo releases failed",
			"repo", repo.Owner+"/"+repo.Name, "err", err,
		)
		return
	}

	selectedReleases := displayPlatformReleases(releases, 3)
	overview := db.RepoOverview{}
	for _, release := range selectedReleases {
		overview.Releases = append(overview.Releases, repoReleaseFromPlatform(release))
	}
	if len(overview.Releases) > 0 {
		overview.LatestRelease = &overview.Releases[0]
	}

	timelineTags := make([]string, 0, len(selectedReleases))
	if len(selectedReleases) == 0 {
		tagReader, tagErr := s.tagReaderFor(repo)
		if tagErr != nil {
			if !errors.Is(tagErr, platform.ErrUnsupportedCapability) {
				slog.Warn("resolve repo tag reader failed",
					"repo", repo.Owner+"/"+repo.Name, "err", tagErr,
				)
			}
		} else {
			tags, tagErr := tagReader.ListTags(ctx, platformRepoRef(repo))
			if tagErr != nil {
				slog.Warn("list repo tags failed",
					"repo", repo.Owner+"/"+repo.Name, "err", tagErr,
				)
			} else {
				selectedTags := displayPlatformTags(tags, 3)
				for _, tag := range selectedTags {
					overview.Releases = append(overview.Releases, repoReleaseFromPlatformTag(tag))
					timelineTags = append(timelineTags, tag.Name)
				}
				if len(overview.Releases) > 0 {
					overview.LatestRelease = &overview.Releases[0]
				}
			}
		}
	} else {
		for _, release := range selectedReleases {
			timelineTags = append(timelineTags, release.TagName)
		}
	}

	s.addRepoOverviewTimeline(ctx, repo, cloneFetchOK, timelineTags, &overview)

	if err := s.db.UpsertRepoOverview(ctx, repoID, overview); err != nil {
		slog.Warn("store repo overview failed",
			"repo", repo.Owner+"/"+repo.Name, "err", err,
		)
	}
}

func (s *Syncer) addRepoOverviewTimeline(
	ctx context.Context,
	repo RepoRef,
	cloneFetchOK bool,
	tags []string,
	overview *db.RepoOverview,
) {
	if len(tags) == 0 || s.clones == nil || !cloneFetchOK {
		return
	}
	host := repo.PlatformHost
	if host == "" {
		host = "github.com"
	}
	latestTag := tags[0]
	count, _, countErr := s.clones.CommitTimelineSinceTag(
		ctx, host, repo.Owner, repo.Name, latestTag, 1,
	)
	if countErr != nil {
		slog.Warn("count commits since latest version failed",
			"repo", repo.Owner+"/"+repo.Name,
			"tag", latestTag, "err", countErr,
		)
	} else {
		overview.CommitsSinceRelease = &count
	}

	timelineTag := tags[len(tags)-1]
	_, points, err := s.clones.CommitTimelineSinceTag(
		ctx, host, repo.Owner, repo.Name,
		timelineTag, repoOverviewTimelineLimit,
	)
	if err != nil {
		slog.Warn("build repo commit timeline failed",
			"repo", repo.Owner+"/"+repo.Name,
			"tag", timelineTag, "err", err,
		)
		return
	}
	overview.CommitTimeline = make([]db.RepoCommitTimelinePoint, 0, len(points))
	for _, point := range points {
		overview.CommitTimeline = append(overview.CommitTimeline, db.RepoCommitTimelinePoint{
			SHA:         point.SHA,
			Message:     point.Message,
			CommittedAt: point.CommittedAt.UTC(),
		})
	}
	now := time.Now().UTC()
	overview.TimelineUpdatedAt = &now
}

func displayReleases(
	releases []*gh.RepositoryRelease,
	limit int,
) []*gh.RepositoryRelease {
	if limit < 1 {
		limit = 1
	}
	out := make([]*gh.RepositoryRelease, 0, limit)
	for _, release := range releases {
		if release == nil || release.GetDraft() || release.GetTagName() == "" {
			continue
		}
		out = append(out, release)
		if len(out) == limit {
			return out
		}
	}
	return out
}

func displayTags(
	tags []*gh.RepositoryTag,
	limit int,
) []*gh.RepositoryTag {
	if limit < 1 {
		limit = 1
	}
	out := make([]*gh.RepositoryTag, 0, limit)
	for _, tag := range tags {
		if tag == nil || tag.GetName() == "" {
			continue
		}
		out = append(out, tag)
		if len(out) == limit {
			return out
		}
	}
	return out
}

func displayPlatformReleases(releases []platform.Release, limit int) []platform.Release {
	if limit < 1 {
		limit = 1
	}
	out := make([]platform.Release, 0, limit)
	for _, release := range releases {
		if release.TagName == "" {
			continue
		}
		out = append(out, release)
		if len(out) == limit {
			return out
		}
	}
	return out
}

func displayPlatformTags(tags []platform.Tag, limit int) []platform.Tag {
	if limit < 1 {
		limit = 1
	}
	out := make([]platform.Tag, 0, limit)
	for _, tag := range tags {
		if tag.Name == "" {
			continue
		}
		out = append(out, tag)
		if len(out) == limit {
			return out
		}
	}
	return out
}

func repoReleaseFromGitHub(release *gh.RepositoryRelease) db.RepoRelease {
	out := db.RepoRelease{
		TagName:         release.GetTagName(),
		Name:            release.GetName(),
		URL:             release.GetHTMLURL(),
		TargetCommitish: release.GetTargetCommitish(),
		Prerelease:      release.GetPrerelease(),
	}
	publishedAt := release.GetPublishedAt().Time
	if !publishedAt.IsZero() {
		publishedAt = publishedAt.UTC()
		out.PublishedAt = &publishedAt
	}
	return out
}

func repoReleaseFromPlatform(release platform.Release) db.RepoRelease {
	out := db.RepoRelease{
		TagName:         release.TagName,
		Name:            release.Name,
		URL:             release.URL,
		TargetCommitish: release.TargetCommitish,
		Prerelease:      release.Prerelease,
	}
	if release.PublishedAt != nil && !release.PublishedAt.IsZero() {
		publishedAt := release.PublishedAt.UTC()
		out.PublishedAt = &publishedAt
	}
	return out
}

func repoReleaseFromTag(platformHost, owner, repo string, tag *gh.RepositoryTag) db.RepoRelease {
	host := platformHost
	if host == "" {
		host = "github.com"
	}
	tagName := tag.GetName()
	return db.RepoRelease{
		TagName:         tagName,
		Name:            tagName,
		URL:             "https://" + host + "/" + owner + "/" + repo + "/tree/" + url.PathEscape(tagName),
		TargetCommitish: tag.GetCommit().GetSHA(),
	}
}

func repoReleaseFromPlatformTag(tag platform.Tag) db.RepoRelease {
	return db.RepoRelease{
		TagName:         tag.Name,
		Name:            tag.Name,
		URL:             tag.URL,
		TargetCommitish: tag.SHA,
	}
}

// indexSyncRepo performs the cheap index scan: list endpoints only,
// upserting basic data without detail fetches. This runs every cycle.
func (s *Syncer) indexSyncRepo(
	ctx context.Context,
	repo RepoRef,
	repoID int64,
	cloneFetchOK bool,
) error {
	caps, err := s.ProviderCapabilities(repoPlatform(repo), repoHost(repo))
	if err != nil {
		return fmt.Errorf("resolve provider capabilities for %s/%s: %w", repo.Owner, repo.Name, err)
	}
	gitHubClient, hasGitHubClient := s.optionalGitHubClientFor(repo)
	platformRef := platformRepoRef(repo)

	// If the previous sync of this repo partially failed after the
	// ETag cache was populated by a 200 list response, a naive next
	// cycle would see 304 and skip the per-item upserts that failed
	// last time, leaving the DB stale until the TTL expired. Evict
	// the repo's list ETags so the following calls are
	// unconditional, forcing a fresh 200 that we can re-apply.
	priorFail := s.consumeRepoFailed(repo)
	forceMR := priorFail&failMR != 0
	forceIssues := priorFail&failIssues != 0
	if priorFail != 0 {
		var endpoints []string
		if forceMR {
			endpoints = append(endpoints, "pulls")
		}
		if forceIssues {
			endpoints = append(endpoints, "issues")
		}
		if hasGitHubClient {
			gitHubClient.InvalidateListETagsForRepo(repo.Owner, repo.Name, endpoints...)
		}
	}

	// Track partial-failure signals per path so the next cycle only
	// forces refresh on the paths that actually failed.
	var failedScope failScope

	prListUnchanged := false
	if caps.ReadMergeRequests {
		mrReader, err := s.mergeRequestReaderFor(repo)
		if err != nil {
			return fmt.Errorf("resolve merge request reader for %s/%s: %w", repo.Owner, repo.Name, err)
		}
		openMRs, err := mrReader.ListOpenMergeRequests(ctx, platformRef)
		if err != nil {
			// 304 Not Modified means the open-PR list is byte-identical
			// to the previous fetch. No PR opened, no PR closed, no
			// metadata on any open PR changed. Skip per-PR upserts and
			// closure detection — both ran on the previous sync that
			// produced the cached etag.
			if IsNotModified(err) {
				prListUnchanged = true
			} else {
				s.markRepoFailed(repo, failMR)
				return fmt.Errorf("list open PRs: %w", err)
			}
		}

		if prListUnchanged {
			// 304 — nothing to do. The detail drain handles CI
			// updates for PRs with pending checks via priority scoring.
		} else {
			// GraphQL path: if fetcher available and not rate-limited,
			// do a bulk fetch that replaces both index upsert and
			// detail drain for complete PRs. For large repos that
			// already have indexed rows, keep the refresh incremental:
			// the list phase updates timestamps and the detail drain
			// conditionally fetches individual stale PRs.
			graphQLDone := false
			if fetcher := s.fetcherFor(repo); fetcher != nil &&
				s.shouldUseBulkGraphQLForMRs(ctx, repo, repoID, len(openMRs)) {
				if backoff, _ := fetcher.ShouldBackoff(); !backoff {
					result, gqlErr := fetcher.FetchRepoPRs(
						ctx, repo.Owner, repo.Name,
					)
					if gqlErr != nil {
						slog.Warn("GraphQL fetch failed, falling back to REST index",
							"repo", repo.Owner+"/"+repo.Name,
							"err", gqlErr,
						)
					} else {
						if err := s.doSyncRepoGraphQL(
							ctx, repo, repoID, result, cloneFetchOK,
						); err != nil {
							failedScope |= failMR
						}
						graphQLDone = true
					}
				}
			}

			if !graphQLDone {
				if err := s.syncMergeRequestsFromList(
					ctx, mrReader, repo, repoID, openMRs, cloneFetchOK,
				); err != nil {
					slog.Error("merge request sync failed",
						"repo", repo.Owner+"/"+repo.Name,
						"err", err,
					)
					failedScope |= failMR
				}
			}
		}
	}

	// Index issues — ETag-gated, with GraphQL when available.
	// Same structure as PR sync: REST list first (ETag gate),
	// then GraphQL if available, REST fallback if not.
	issueListUnchanged := false
	if caps.ReadIssues {
		issueReader, err := s.issueReaderFor(repo)
		if err != nil {
			slog.Error("resolve issue reader failed",
				"repo", repo.Owner+"/"+repo.Name,
				"err", err,
			)
			failedScope |= failIssues
			if failedScope != 0 {
				s.markRepoFailed(repo, failedScope)
			}
			return fmt.Errorf("resolve issue reader for %s/%s: %w", repo.Owner, repo.Name, err)
		}

		var openIssues []platform.Issue
		var ghIssues []*gh.Issue
		_, useGitHubIssuePath := issueReader.(interface {
			ListOpenGitHubIssues(context.Context, platform.RepoRef) ([]*gh.Issue, error)
		})
		var issueListErr error
		if rawIssueReader, ok := issueReader.(interface {
			ListOpenGitHubIssues(context.Context, platform.RepoRef) ([]*gh.Issue, error)
		}); ok && hasGitHubClient {
			ghIssues, issueListErr = rawIssueReader.ListOpenGitHubIssues(ctx, platformRef)
		} else {
			openIssues, issueListErr = issueReader.ListOpenIssues(ctx, platformRef)
		}
		if issueListErr != nil {
			if IsNotModified(issueListErr) {
				// 304: open issue list unchanged, skip.
				issueListUnchanged = true
			} else {
				slog.Error("list open issues failed",
					"repo", repo.Owner+"/"+repo.Name,
					"err", issueListErr,
				)
				failedScope |= failIssues
			}
		} else {
			graphQLIssuesDone := false
			if fetcher := s.fetcherFor(repo); fetcher != nil &&
				s.shouldUseBulkGraphQLForIssues(ctx, repo, repoID, len(openIssues)+len(ghIssues)) {
				if backoff, _ := fetcher.ShouldBackoff(); !backoff {
					issueResult, gqlErr := fetcher.FetchRepoIssues(
						ctx, repo.Owner, repo.Name,
					)
					if gqlErr != nil {
						slog.Warn("GraphQL issue fetch failed, falling back to REST",
							"repo", repo.Owner+"/"+repo.Name,
							"err", gqlErr,
						)
					} else {
						if err := s.doSyncRepoGraphQLIssues(
							ctx, repo, repoID, issueResult,
						); err != nil {
							failedScope |= failIssues
						}
						graphQLIssuesDone = true
					}
				}
			}

			if !graphQLIssuesDone {
				if useGitHubIssuePath && hasGitHubClient {
					if err := s.syncIssuesFromList(
						ctx, gitHubClient, repo, repoID, ghIssues, forceIssues,
					); err != nil {
						slog.Error("REST issue sync failed",
							"repo", repo.Owner+"/"+repo.Name,
							"err", err,
						)
						failedScope |= failIssues
					}
				} else {
					if err := s.syncPlatformIssuesFromList(
						ctx, issueReader, repo, repoID, openIssues, forceIssues,
					); err != nil {
						slog.Error("issue sync failed",
							"repo", repo.Owner+"/"+repo.Name,
							"err", err,
						)
						failedScope |= failIssues
					}
				}
			}
		}
	}

	if failedScope != 0 {
		// One or more per-item steps failed. Record which paths
		// failed so the next cycle forces an unconditional refetch
		// only for the affected list endpoints.
		s.markRepoFailed(repo, failedScope)
	} else {
		// Clean pass — drop any leftover flag from a prior cycle.
		s.clearRepoFailed(repo)
	}

	if caps.ReadMergeRequests && prListUnchanged && failedScope&failMR == 0 {
		s.refreshRepoPRComments(ctx, repo)
	}
	if caps.ReadIssues && issueListUnchanged && failedScope&failIssues == 0 {
		s.refreshRepoIssueComments(ctx, repo)
	}

	return nil
}

func (s *Syncer) syncMergeRequestsFromList(
	ctx context.Context,
	reader platform.MergeRequestReader,
	repo RepoRef,
	repoID int64,
	mrs []platform.MergeRequest,
	cloneFetchOK bool,
) error {
	stillOpen := make(map[int]bool, len(mrs))
	for _, mr := range mrs {
		stillOpen[mr.Number] = true
	}

	var hadItemFailure bool
	progress := newMergeRequestSyncProgressLogger(repo, "provider", len(mrs))
	for i, mr := range mrs {
		if err := s.indexUpsertMergeRequest(ctx, repo, repoID, mr); err != nil {
			slog.Error("index upsert MR failed",
				"repo", repo.Owner+"/"+repo.Name,
				"number", mr.Number,
				"err", err,
			)
			hadItemFailure = true
		}
		progress.record(i + 1)
	}

	closedNumbers, err := s.db.GetPreviouslyOpenMRNumbers(
		ctx, repoID, stillOpen,
	)
	if err != nil {
		s.markRepoFailed(repo, failMR)
		return fmt.Errorf("get previously open MRs: %w", err)
	}
	for _, number := range closedNumbers {
		if err := s.fetchAndUpdateClosedMergeRequest(
			ctx, reader, repo, repoID, number, cloneFetchOK,
		); err != nil {
			slog.Error("update closed MR failed",
				"repo", repo.Owner+"/"+repo.Name,
				"number", number,
				"err", err,
			)
			hadItemFailure = true
		}
	}

	if hadItemFailure {
		return fmt.Errorf("one or more merge request sync items failed")
	}
	progress.done()
	return nil
}

func (s *Syncer) shouldUseBulkGraphQLForMRs(
	ctx context.Context,
	repo RepoRef,
	repoID int64,
	listCount int,
) bool {
	localOpenCount, err := s.db.CountOpenMergeRequestsForRepo(ctx, repoID)
	if err != nil {
		slog.Warn("count existing merge requests before GraphQL bulk fetch failed",
			"repo", repo.Owner+"/"+repo.Name,
			"err", err,
		)
		return true
	}
	if localOpenCount < largeRepoBulkGraphQLThreshold {
		return true
	}
	slog.Info("skipping GraphQL merge request bulk fetch for large existing repo",
		"repo", repo.Owner+"/"+repo.Name,
		"platform", repoPlatform(repo),
		"host", repoHost(repo),
		"local_open_total", localOpenCount,
		"fetched_total", listCount,
	)
	return false
}

func (s *Syncer) shouldUseBulkGraphQLForIssues(
	ctx context.Context,
	repo RepoRef,
	repoID int64,
	listCount int,
) bool {
	localOpenCount, err := s.db.CountOpenIssuesForRepo(ctx, repoID)
	if err != nil {
		slog.Warn("count existing issues before GraphQL bulk fetch failed",
			"repo", repo.Owner+"/"+repo.Name,
			"err", err,
		)
		return true
	}
	if localOpenCount < largeRepoBulkGraphQLThreshold {
		return true
	}
	slog.Info("skipping GraphQL issue bulk fetch for large existing repo",
		"repo", repo.Owner+"/"+repo.Name,
		"platform", repoPlatform(repo),
		"host", repoHost(repo),
		"local_open_total", localOpenCount,
		"fetched_total", listCount,
	)
	return false
}

func (s *Syncer) indexUpsertMergeRequest(
	ctx context.Context,
	repo RepoRef,
	repoID int64,
	mr platform.MergeRequest,
) error {
	normalized := platform.DBMergeRequest(repoID, mr)

	existing, err := s.db.GetMergeRequestByRepoIDAndNumber(
		ctx, repoID, mr.Number,
	)
	if err != nil {
		return fmt.Errorf(
			"get existing MR #%d: %w", mr.Number, err,
		)
	}

	// Preserve fields list endpoints commonly omit.
	needsCIDetailRefresh := false
	if existing != nil {
		normalized.Additions = existing.Additions
		normalized.Deletions = existing.Deletions
		preserveMergeableStateIfOmitted(normalized, existing)
		needsCIDetailRefresh = preserveCIStateIfOmitted(normalized, existing)
	}

	if normalized.Author != "" &&
		normalized.AuthorDisplayName == "" {
		if client, ok := s.optionalGitHubClientFor(repo); ok {
			if name, found := s.resolveDisplayName(
				ctx, client, repoHost(repo), normalized.Author,
			); found {
				normalized.AuthorDisplayName = name
			}
		}
		if normalized.AuthorDisplayName == "" && existing != nil {
			normalized.AuthorDisplayName =
				existing.AuthorDisplayName
		}
	}

	mrID, err := s.db.UpsertMergeRequest(ctx, normalized)
	if err != nil {
		return fmt.Errorf(
			"upsert MR #%d: %w", mr.Number, err,
		)
	}
	if needsCIDetailRefresh {
		if err := s.clearMRDetailFetchedByRepoID(ctx, repoID, mr.Number); err != nil {
			return fmt.Errorf(
				"clear detail fetch marker for MR #%d: %w",
				mr.Number, err,
			)
		}
	}
	if err := s.replaceMergeRequestLabels(ctx, repoID, mrID, normalized.Labels); err != nil {
		return fmt.Errorf("persist labels for MR #%d: %w", mr.Number, err)
	}

	if err := s.db.EnsureKanbanState(ctx, mrID); err != nil {
		return fmt.Errorf(
			"ensure kanban state for MR #%d: %w",
			mr.Number, err,
		)
	}

	if existing != nil &&
		existing.DetailFetchedAt != nil &&
		existing.UpdatedAt.Equal(normalized.UpdatedAt) {
		s.queuePRCommentSync(repo, existing.Number)
	}

	return nil
}

// indexUpsertMR upserts a PR from list endpoint data only. No
// GetPullRequest, no timeline, no CI. Preserves fields that the
// list endpoint does not return (additions, deletions,
// mergeable_state, cached CI) from the existing DB row.
func (s *Syncer) indexUpsertMR(
	ctx context.Context,
	client Client,
	repo RepoRef,
	repoID int64,
	ghPR *gh.PullRequest,
) error {
	normalized, err := NormalizePR(repoID, ghPR)
	if err != nil {
		return fmt.Errorf("normalize MR #%d: %w", ghPR.GetNumber(), err)
	}

	existing, err := s.db.GetMergeRequestByRepoIDAndNumber(
		ctx, repoID, ghPR.GetNumber(),
	)
	if err != nil {
		return fmt.Errorf(
			"get existing MR #%d: %w", ghPR.GetNumber(), err,
		)
	}

	// Preserve fields the list endpoint doesn't return.
	needsCIDetailRefresh := false
	if existing != nil {
		normalized.Additions = existing.Additions
		normalized.Deletions = existing.Deletions
		preserveMergeableStateIfOmitted(normalized, existing)
		needsCIDetailRefresh = preserveCIStateIfOmitted(normalized, existing)
	}

	if normalized.Author != "" &&
		normalized.AuthorDisplayName == "" {
		host := repo.PlatformHost
		if host == "" {
			host = "github.com"
		}
		if name, ok := s.resolveDisplayName(
			ctx, client, host, normalized.Author,
		); ok {
			normalized.AuthorDisplayName = name
		} else if existing != nil {
			normalized.AuthorDisplayName =
				existing.AuthorDisplayName
		}
	}

	mrID, err := s.db.UpsertMergeRequest(ctx, normalized)
	if err != nil {
		return fmt.Errorf(
			"upsert MR #%d: %w", ghPR.GetNumber(), err,
		)
	}
	if needsCIDetailRefresh {
		if err := s.clearMRDetailFetchedByRepoID(ctx, repoID, ghPR.GetNumber()); err != nil {
			return fmt.Errorf(
				"clear detail fetch marker for MR #%d: %w",
				ghPR.GetNumber(), err,
			)
		}
	}
	if err := s.replaceMergeRequestLabels(ctx, repoID, mrID, normalized.Labels); err != nil {
		return fmt.Errorf("persist labels for MR #%d: %w", ghPR.GetNumber(), err)
	}

	if err := s.db.EnsureKanbanState(ctx, mrID); err != nil {
		return fmt.Errorf(
			"ensure kanban state for MR #%d: %w",
			ghPR.GetNumber(), err,
		)
	}

	if existing != nil &&
		existing.DetailFetchedAt != nil &&
		existing.UpdatedAt.Equal(normalized.UpdatedAt) {
		s.queuePRCommentSync(repo, existing.Number)
	}

	return nil
}

const largeCommentThreadThreshold = 100

func (s *Syncer) listCommentsForRefresh(
	ctx context.Context,
	client Client,
	repo RepoRef,
	number int,
	knownCount int,
) ([]*gh.IssueComment, error) {
	if knownCount >= largeCommentThreadThreshold {
		return client.ListIssueComments(
			ctx, repo.Owner, repo.Name, number,
		)
	}
	return client.ListIssueCommentsIfChanged(
		ctx, repo.Owner, repo.Name, number,
	)
}

func (s *Syncer) refreshPRCommentsForItem(
	ctx context.Context,
	client Client,
	repo RepoRef,
	pr *db.MergeRequest,
) {
	if pr == nil || pr.DetailFetchedAt == nil {
		return
	}
	if !s.canSpendCommentRefresh(repo) {
		return
	}

	comments, err := s.listCommentsForRefresh(
		ctx, client, repo, pr.Number, pr.CommentCount,
	)
	if err != nil {
		if IsNotModified(err) {
			return
		}
		slog.Warn("comment refresh: list PR comments failed",
			"repo", repo.Owner+"/"+repo.Name,
			"number", pr.Number,
			"err", err,
		)
		return
	}
	if err := s.persistPRComments(ctx, pr, comments); err != nil {
		client.InvalidateListETagsForRepo(repo.Owner, repo.Name, "comments")
		slog.Warn("comment refresh: persist PR comments failed",
			"repo", repo.Owner+"/"+repo.Name,
			"number", pr.Number,
			"err", err,
		)
	}
}

func (s *Syncer) refreshIssueCommentsForItem(
	ctx context.Context,
	client Client,
	repo RepoRef,
	issue *db.Issue,
) {
	if issue == nil || issue.DetailFetchedAt == nil {
		return
	}
	if !s.canSpendCommentRefresh(repo) {
		return
	}

	comments, err := s.listCommentsForRefresh(
		ctx, client, repo, issue.Number, issue.CommentCount,
	)
	if err != nil {
		if IsNotModified(err) {
			return
		}
		slog.Warn("comment refresh: list issue comments failed",
			"repo", repo.Owner+"/"+repo.Name,
			"number", issue.Number,
			"err", err,
		)
		return
	}
	if err := s.persistIssueComments(ctx, issue, comments); err != nil {
		client.InvalidateListETagsForRepo(repo.Owner, repo.Name, "comments")
		slog.Warn("comment refresh: persist issue comments failed",
			"repo", repo.Owner+"/"+repo.Name,
			"number", issue.Number,
			"err", err,
		)
	}
}

func (s *Syncer) resetPendingCommentSyncs() {
	s.commentRefreshMu.Lock()
	defer s.commentRefreshMu.Unlock()
	s.pendingPRCommentSyncs = nil
	s.pendingIssueCommentSyncs = nil
}

func (s *Syncer) queuePRCommentSync(repo RepoRef, number int) {
	s.commentRefreshMu.Lock()
	defer s.commentRefreshMu.Unlock()
	s.pendingPRCommentSyncs = append(s.pendingPRCommentSyncs, queuedPRCommentSync{
		repo:   repo,
		number: number,
	})
}

func (s *Syncer) queueIssueCommentSync(repo RepoRef, number int) {
	s.commentRefreshMu.Lock()
	defer s.commentRefreshMu.Unlock()
	s.pendingIssueCommentSyncs = append(s.pendingIssueCommentSyncs, queuedIssueCommentSync{
		repo:   repo,
		number: number,
	})
}

func (s *Syncer) drainPendingCommentSyncs(
	ctx context.Context,
	eligibleHosts map[string]bool,
) {
	s.commentRefreshMu.Lock()
	prs := slices.Clone(s.pendingPRCommentSyncs)
	issues := slices.Clone(s.pendingIssueCommentSyncs)
	s.pendingPRCommentSyncs = nil
	s.pendingIssueCommentSyncs = nil
	s.commentRefreshMu.Unlock()

	for _, item := range prs {
		if ctx.Err() != nil {
			return
		}
		bucket := repoRateBucketKey(item.repo)
		if !eligibleHosts[bucket] {
			continue
		}
		client, err := s.clientFor(item.repo)
		if err != nil {
			slog.Warn("comment refresh: resolve client failed",
				"repo", item.repo.Owner+"/"+item.repo.Name,
				"number", item.number,
				"err", err,
			)
			continue
		}
		repoRow, err := s.db.GetRepoByIdentity(
			ctx, platform.DBRepoIdentity(platformRepoRef(item.repo)),
		)
		if err != nil || repoRow == nil {
			slog.Warn("comment refresh: get PR repo failed",
				"repo", item.repo.Owner+"/"+item.repo.Name,
				"number", item.number,
				"err", err,
			)
			continue
		}
		pr, err := s.db.GetMergeRequestByRepoIDAndNumber(
			ctx, repoRow.ID, item.number,
		)
		if err != nil {
			slog.Warn("comment refresh: get PR failed",
				"repo", item.repo.Owner+"/"+item.repo.Name,
				"number", item.number,
				"err", err,
			)
			continue
		}
		s.refreshPRCommentsForItem(ctx, client, item.repo, pr)
	}

	for _, item := range issues {
		if ctx.Err() != nil {
			return
		}
		bucket := repoRateBucketKey(item.repo)
		if !eligibleHosts[bucket] {
			continue
		}
		client, err := s.clientFor(item.repo)
		if err != nil {
			slog.Warn("comment refresh: resolve client failed",
				"repo", item.repo.Owner+"/"+item.repo.Name,
				"number", item.number,
				"err", err,
			)
			continue
		}
		repoRow, err := s.db.GetRepoByIdentity(
			ctx, platform.DBRepoIdentity(platformRepoRef(item.repo)),
		)
		if err != nil || repoRow == nil {
			slog.Warn("comment refresh: get issue repo failed",
				"repo", item.repo.Owner+"/"+item.repo.Name,
				"number", item.number,
				"err", err,
			)
			continue
		}
		issue, err := s.db.GetIssueByRepoIDAndNumber(
			ctx, repoRow.ID, item.number,
		)
		if err != nil {
			slog.Warn("comment refresh: get issue failed",
				"repo", item.repo.Owner+"/"+item.repo.Name,
				"number", item.number,
				"err", err,
			)
			continue
		}
		s.refreshIssueCommentsForItem(ctx, client, item.repo, issue)
	}
}

// doSyncRepoGraphQL processes bulk GraphQL results for a repo.
func (s *Syncer) doSyncRepoGraphQL(
	ctx context.Context,
	repo RepoRef,
	repoID int64,
	result *RepoBulkResult,
	cloneFetchOK bool,
) error {
	var failedScope failScope
	stillOpen := make(map[int]bool, len(result.PullRequests))
	progress := newMergeRequestSyncProgressLogger(repo, "graphql", len(result.PullRequests))

	for i := range result.PullRequests {
		bulk := &result.PullRequests[i]
		number := bulk.PR.GetNumber()
		stillOpen[number] = true

		if err := s.syncOpenMRFromBulk(
			ctx, repo, repoID, bulk, cloneFetchOK,
		); err != nil {
			slog.Error("GraphQL sync MR failed",
				"repo", repo.Owner+"/"+repo.Name,
				"number", number,
				"err", err,
			)
			failedScope |= failMR
		}
		progress.record(i + 1)
	}

	// Detect closed PRs — same as REST path.
	closedNumbers, err := s.db.GetPreviouslyOpenMRNumbers(
		ctx, repoID, stillOpen,
	)
	if err != nil {
		return fmt.Errorf("get previously open MRs: %w", err)
	}
	for _, number := range closedNumbers {
		if err := s.fetchAndUpdateClosed(
			ctx, repo, repoID, number, cloneFetchOK,
		); err != nil {
			slog.Error("update closed MR failed",
				"repo", repo.Owner+"/"+repo.Name,
				"number", number,
				"err", err,
			)
			failedScope |= failMR
		}
	}

	if failedScope != 0 {
		return fmt.Errorf("GraphQL sync had partial failures")
	}
	progress.done()
	return nil
}

// doSyncRepoGraphQLIssues processes bulk GraphQL results for issues.
func (s *Syncer) doSyncRepoGraphQLIssues(
	ctx context.Context,
	repo RepoRef,
	repoID int64,
	result *RepoBulkResult,
) error {
	var failedScope failScope
	stillOpen := make(map[int]bool, len(result.Issues))
	progress := newIssueSyncProgressLogger(repo, "graphql", len(result.Issues))

	for i := range result.Issues {
		bulk := &result.Issues[i]
		number := bulk.Issue.GetNumber()
		stillOpen[number] = true

		if err := s.syncOpenIssueFromBulk(
			ctx, repo, repoID, bulk,
		); err != nil {
			slog.Error("GraphQL sync issue failed",
				"repo", repo.Owner+"/"+repo.Name,
				"number", number,
				"err", err,
			)
			failedScope |= failIssues
		}
		progress.record(i + 1)
	}

	// Detect closed issues — same as REST path.
	closedNumbers, err := s.db.GetPreviouslyOpenIssueNumbers(
		ctx, repoID, stillOpen,
	)
	if err != nil {
		return fmt.Errorf("get previously open issues: %w", err)
	}
	for _, number := range closedNumbers {
		if err := s.fetchAndUpdateClosedIssue(
			ctx, repo, repoID, number,
		); err != nil {
			slog.Error("update closed issue failed",
				"repo", repo.Owner+"/"+repo.Name,
				"number", number,
				"err", err,
			)
			failedScope |= failIssues
		}
	}

	if failedScope != 0 {
		return fmt.Errorf("GraphQL issue sync had partial failures")
	}
	progress.done()
	return nil
}

// syncOpenIssueFromBulk processes a single issue from GraphQL bulk
// results. Uses pre-fetched data instead of per-issue REST calls.
func (s *Syncer) syncOpenIssueFromBulk(
	ctx context.Context,
	repo RepoRef,
	repoID int64,
	bulk *BulkIssue,
) error {
	number := bulk.Issue.GetNumber()
	normalized, err := NormalizeIssue(repoID, bulk.Issue)
	if err != nil {
		return fmt.Errorf("normalize issue #%d: %w", number, err)
	}

	// Preserve derived fields that NormalizeIssue doesn't populate
	// from bulk data. Without this, upsert overwrites them with
	// zero values.
	existing, err := s.db.GetIssueByRepoIDAndNumber(
		ctx, repoID, number,
	)
	if err != nil {
		return fmt.Errorf(
			"get existing issue #%d: %w", number, err,
		)
	}
	if existing != nil {
		// Only preserve DetailFetchedAt when timeline data is complete.
		// When incomplete, clear it so the detail drain re-queues
		// this issue if the REST fallback fails.
		if bulk.CommentsComplete && bulk.TimelineComplete {
			normalized.DetailFetchedAt = existing.DetailFetchedAt
		}
		// CommentCount comes from GraphQL Comments.TotalCount via
		// adaptIssue, so trust the fresh GraphQL value.
	}

	issueID, err := s.db.UpsertIssue(ctx, normalized)
	if err != nil {
		return fmt.Errorf("upsert issue #%d: %w", number, err)
	}

	// UpsertIssue uses COALESCE to preserve existing detail_fetched_at,
	// so passing nil doesn't clear it. When comments are incomplete,
	// explicitly clear it so the detail drain re-queues this issue
	// if the REST fallback fails.
	if !bulk.CommentsComplete || !bulk.TimelineComplete {
		_, err = s.db.WriteDB().ExecContext(ctx,
			`UPDATE middleman_issues SET detail_fetched_at = NULL WHERE id = ?`,
			issueID,
		)
		if err != nil {
			return fmt.Errorf(
				"clear detail_fetched_at for issue #%d: %w", number, err,
			)
		}
	}

	if err := s.replaceIssueLabels(
		ctx, repoID, issueID, normalized.Labels,
	); err != nil {
		return fmt.Errorf(
			"persist labels for issue #%d: %w", number, err,
		)
	}

	if bulk.CommentsComplete && bulk.TimelineComplete {
		if err := s.replaceIssueCommentEvents(ctx, issueID, bulk.Comments); err != nil {
			return fmt.Errorf(
				"replace issue comment events for #%d: %w", number, err,
			)
		}
		if err := s.upsertIssueTimelineEvents(ctx, issueID, bulk.TimelineEvents); err != nil {
			return fmt.Errorf(
				"upsert issue timeline events for #%d: %w", number, err,
			)
		}
		if err := s.db.UpdateIssueDerivedFields(
			ctx, repoID, number, db.IssueDerivedFields{
				CommentCount:   normalized.CommentCount,
				LastActivityAt: computeIssueCommentLastActivity(bulk.Issue, bulk.Comments),
			},
		); err != nil {
			return fmt.Errorf(
				"update issue #%d derived fields: %w", number, err,
			)
		}

		// Mark detail as fetched so the detail drain doesn't
		// re-queue this issue for REST detail fetches.
		if err := s.updateIssueDetailFetchedByRepoID(
			ctx, repoID, number,
		); err != nil {
			slog.Warn("mark GraphQL issue detail fetched failed",
				"repo", repo.Owner+"/"+repo.Name,
				"number", number, "err", err,
			)
		}
	} else {
		// Timeline data truncated — fall back to detail fetch.
		if err := s.refreshIssueTimeline(
			ctx, repo, issueID, bulk.Issue,
		); err != nil {
			return fmt.Errorf(
				"refresh timeline for issue #%d: %w", number, err,
			)
		}
		// REST fallback succeeded — mark detail as fetched.
		if err := s.updateIssueDetailFetchedByRepoID(
			ctx, repoID, number,
		); err != nil {
			slog.Warn("mark issue detail fetched after REST fallback failed",
				"repo", repo.Owner+"/"+repo.Name,
				"number", number, "err", err,
			)
		}
	}

	return nil
}

// syncOpenMRFromBulk processes a single PR from GraphQL bulk
// results. It performs the same operations as fetchMRDetail but
// using pre-fetched data instead of per-PR REST calls.
func (s *Syncer) syncOpenMRFromBulk(
	ctx context.Context,
	repo RepoRef,
	repoID int64,
	bulk *BulkPR,
	cloneFetchOK bool,
) error {
	number := bulk.PR.GetNumber()
	normalized, err := NormalizePR(repoID, bulk.PR)
	if err != nil {
		return fmt.Errorf("normalize MR #%d: %w", number, err)
	}

	// Preserve derived fields that NormalizePR doesn't populate.
	// Without this, upsert overwrites them with zero values; if
	// nested connections are truncated the later allComplete guard
	// skips restoring them and correct data is lost.
	existing, err := s.db.GetMergeRequestByRepoIDAndNumber(
		ctx, repoID, number,
	)
	if err != nil {
		return fmt.Errorf(
			"get existing MR #%d: %w", number, err,
		)
	}
	headChanged := existing != nil &&
		existing.PlatformHeadSHA != normalized.PlatformHeadSHA
	if existing != nil {
		normalized.CommentCount = existing.CommentCount
		normalized.ReviewDecision = existing.ReviewDecision
		// CI is tied to the head SHA. If the head moved we must clear
		// the previous values; otherwise an incomplete bulk CI fetch
		// (CIComplete=false skips the UpdateMRCIStatus write below)
		// would leave stale checks attached to the new commit.
		if !headChanged {
			normalized.CIStatus = existing.CIStatus
			normalized.CIChecksJSON = existing.CIChecksJSON
			normalized.CIHadPending = existing.CIHadPending
		}
		normalized.DetailFetchedAt = existing.DetailFetchedAt
		if normalized.AuthorDisplayName == "" {
			normalized.AuthorDisplayName =
				existing.AuthorDisplayName
		}
	}

	// Resolve display name if missing.
	if normalized.Author != "" &&
		normalized.AuthorDisplayName == "" {
		host := repo.PlatformHost
		if host == "" {
			host = "github.com"
		}
		client, clientErr := s.clientFor(repo)
		if clientErr == nil {
			if name, ok := s.resolveDisplayName(
				ctx, client, host, normalized.Author,
			); ok {
				normalized.AuthorDisplayName = name
			}
		}
	}

	mrID, err := s.db.UpsertMergeRequest(ctx, normalized)
	if err != nil {
		return fmt.Errorf("upsert MR #%d: %w", number, err)
	}

	// UpsertMergeRequest preserves ci_had_pending across upserts, so
	// the head-changed reset above doesn't actually persist that field
	// without an explicit clear. Drop the stale CI state here so it
	// doesn't outlive the old commit.
	if headChanged {
		if err := s.db.ClearMRCI(ctx, repoID, number); err != nil {
			return fmt.Errorf(
				"clear stale CI for MR #%d: %w", number, err,
			)
		}
	}

	if err := s.db.EnsureKanbanState(ctx, mrID); err != nil {
		return fmt.Errorf(
			"ensure kanban state for MR #%d: %w", number, err,
		)
	}

	if err := s.replaceMergeRequestLabels(
		ctx, repoID, mrID, normalized.Labels,
	); err != nil {
		return fmt.Errorf(
			"persist labels for MR #%d: %w", number, err,
		)
	}

	// Diff SHAs.
	repoHost := repo.PlatformHost
	if repoHost == "" {
		repoHost = "github.com"
	}
	if s.clones != nil && cloneFetchOK {
		headSHA := normalized.PlatformHeadSHA
		baseSHA := normalized.PlatformBaseSHA
		if headSHA != "" && baseSHA != "" {
			mb, mbErr := s.clones.MergeBase(
				ctx, repoHost, repo.Owner,
				repo.Name, baseSHA, headSHA,
			)
			if mbErr != nil {
				slog.Warn("merge-base computation failed",
					"repo", repo.Owner+"/"+repo.Name,
					"number", number, "err", mbErr,
				)
			} else {
				if dbErr := s.db.UpdateDiffSHAs(
					ctx, repoID, number,
					headSHA, baseSHA, mb,
				); dbErr != nil {
					slog.Warn("update diff SHAs failed",
						"repo", repo.Owner+"/"+repo.Name,
						"number", number, "err", dbErr,
					)
				}
			}
		}
	}

	// Timeline events — comments, reviews, commits, and system events.
	// Events use ON CONFLICT DO NOTHING, so partial data is safe.
	var events []db.MREvent
	for _, c := range bulk.Comments {
		events = append(events, NormalizeCommentEvent(mrID, c))
	}
	for _, r := range bulk.Reviews {
		events = append(events, NormalizeReviewEvent(mrID, r))
	}
	for _, c := range bulk.Commits {
		events = append(events, NormalizeCommitEvent(mrID, c))
	}
	for _, timelineEvent := range bulk.TimelineEvents {
		if event := NormalizeTimelineEvent(mrID, timelineEvent); event != nil {
			events = append(events, *event)
		}
	}
	if bulk.CommentsComplete {
		if err := s.replacePRCommentEvents(ctx, mrID, bulk.Comments); err != nil {
			return fmt.Errorf(
				"replace comment events for MR #%d: %w", number, err,
			)
		}
	}
	if err := s.db.UpsertMREvents(ctx, events); err != nil {
		return fmt.Errorf(
			"upsert events for MR #%d: %w", number, err,
		)
	}

	// CI status — only write if complete (don't write
	// truncated CI data that could hide failures).
	var ciChecks []db.CICheck
	var ciJSON []byte
	if bulk.CIComplete {
		ciChecks = normalizeBulkCI(bulk)
		if ciChecks == nil {
			ciChecks = []db.CICheck{}
		}
		ciJSON, _ = json.Marshal(ciChecks)
		ciStatus := deriveCIStatusFromChecks(ciChecks)
		if err := s.db.UpdateMRCIStatus(
			ctx, repoID, number,
			ciStatus, string(ciJSON),
		); err != nil {
			slog.Warn("update CI status failed",
				"repo", repo.Owner+"/"+repo.Name,
				"number", number, "err", err,
			)
		}
	}

	// Mark detail as fetched and update derived fields only when
	// ALL connections are complete. Incomplete PRs leave
	// DetailFetchedAt stale so the detail drain picks them up for
	// a full REST fetch. Derived fields from truncated data would
	// overwrite correct values with partial counts.
	if bulk.CommentsComplete {
		lastActivity := computeLastActivity(bulk.PR, bulk.Comments, nil, nil)
		// When reviews/commits are truncated this cycle, any stored
		// review/commit/force-push event with a newer timestamp must
		// still win so the dashboard ordering doesn't regress.
		nonCommentLatest, nErr := s.db.GetMRLatestNonCommentEventTime(ctx, mrID)
		if nErr != nil {
			slog.Warn("latest non-comment event lookup failed",
				"repo", repo.Owner+"/"+repo.Name,
				"number", number, "err", nErr,
			)
		} else if nonCommentLatest.After(lastActivity) {
			lastActivity = nonCommentLatest
		}
		if err := s.db.UpdateMRDerivedFields(
			ctx, repoID, number, db.MRDerivedFields{
				ReviewDecision: normalized.ReviewDecision,
				CommentCount:   len(bulk.Comments),
				LastActivityAt: lastActivity,
			},
		); err != nil {
			slog.Warn("update comment-derived fields failed",
				"repo", repo.Owner+"/"+repo.Name,
				"number", number, "err", err,
			)
		}
	}

	allComplete := bulk.CommentsComplete &&
		bulk.ReviewsComplete &&
		bulk.CommitsComplete &&
		bulk.TimelineComplete &&
		bulk.CIComplete
	if allComplete {
		reviewDecision := DeriveReviewDecision(bulk.Reviews)
		lastActivity := computeLastActivity(
			bulk.PR, bulk.Comments, bulk.Reviews, bulk.Commits,
		)
		if err := s.db.UpdateMRDerivedFields(
			ctx, repoID, number, db.MRDerivedFields{
				ReviewDecision: reviewDecision,
				CommentCount:   len(bulk.Comments),
				LastActivityAt: lastActivity,
			},
		); err != nil {
			slog.Warn("update derived fields failed",
				"repo", repo.Owner+"/"+repo.Name,
				"number", number, "err", err,
			)
		}
	}
	if allComplete {
		pending := ciHasPending(string(ciJSON))
		if err := s.updateMRDetailFetchedByRepoID(
			ctx, repoID, number, pending,
		); err != nil {
			slog.Warn("mark GraphQL detail fetched failed",
				"repo", repo.Owner+"/"+repo.Name,
				"number", number, "err", err,
			)
		}
		// Refresh workflow approval state so the DB-only detail GET
		// can render the Approve workflows button without a foreground
		// sync. GraphQL doesn't return action_required runs, so this
		// stays a one-extra REST call per fully-synced PR, gated by
		// the same per-host budget as the REST detail drain. The
		// sync-budget transport spends the actual REST call; this is
		// only the admission check.
		if s.canSpendWorkflowApprovalRefresh(repo) {
			s.refreshWorkflowApproval(
				ctx, repo, repoID, number,
				normalized.PlatformHeadSHA, bulk.PR, normalized,
			)
		}
	}

	// Fire onMRSynced hook.
	if s.onMRSynced != nil {
		fresh, fErr := s.db.GetMergeRequestByRepoIDAndNumber(
			ctx, repoID, number,
		)
		if fErr != nil {
			slog.Warn("get MR for onMRSynced hook failed",
				"repo", repo.Owner+"/"+repo.Name,
				"number", number, "err", fErr,
			)
		} else {
			s.onMRSynced(repo.Owner, repo.Name, fresh)
		}
	}

	return nil
}

// deriveCIStatusFromChecks computes the overall CI status from
// a []db.CICheck. Mirrors DeriveOverallCIStatus but works on the
// normalized CICheck format produced by normalizeBulkCI.
func deriveCIStatusFromChecks(checks []db.CICheck) string {
	if len(checks) == 0 {
		return ""
	}
	hasPending := false
	hasFailed := false
	for _, c := range checks {
		if c.Status != "completed" {
			hasPending = true
			continue
		}
		switch c.Conclusion {
		case "success", "neutral", "skipped":
			// OK
		default:
			if c.Conclusion != "" {
				hasFailed = true
			}
		}
	}
	if hasFailed {
		return "failure"
	}
	if hasPending {
		return "pending"
	}
	return "success"
}

// normalizeBulkCI converts GraphQL check runs and statuses to
// the db.CICheck slice format used by the rest of the codebase.
func normalizeBulkCI(bulk *BulkPR) []db.CICheck {
	return normalizeCIChecks(bulk.CheckRuns, bulk.Statuses)
}

// fetchMRDetail performs a full detail fetch for a single MR:
// GetPullRequest, refreshTimeline, refreshCIStatus. Returns the
// number of API calls made.
func (s *Syncer) fetchMRDetail(
	ctx context.Context,
	repo RepoRef,
	repoID int64,
	number int,
	cloneFetchOK bool,
) (int, error) {
	calls := 0
	mrReader, err := s.mergeRequestReaderFor(repo)
	if err != nil {
		return calls, fmt.Errorf("resolve merge request reader for %s/%s: %w", repo.Owner, repo.Name, err)
	}
	if _, ok := mrReader.(interface {
		GetGitHubPullRequest(context.Context, platform.RepoRef, int) (*gh.PullRequest, platform.MergeRequest, error)
	}); !ok {
		return s.fetchProviderMRDetail(ctx, mrReader, repo, repoID, number)
	}

	client, err := s.clientFor(repo)
	if err != nil {
		return calls, fmt.Errorf("resolve client for %s/%s: %w", repo.Owner, repo.Name, err)
	}

	existing, err := s.db.GetMergeRequestByRepoIDAndNumber(
		ctx, repoID, number,
	)
	if err != nil {
		return calls, fmt.Errorf(
			"get existing MR #%d: %w", number, err,
		)
	}

	fullPR, newETag, notModified, err := s.getPullRequestForDetail(
		ctx, client, repo, number,
	)
	calls++
	if err == nil && fullPR == nil {
		if notModified && existing != nil {
			return s.markUnchangedMRDetailFetched(
				ctx, repo, repoID, number, existing, calls,
			)
		}
		err = fmt.Errorf("client returned nil pull request")
	}
	if err != nil {
		return calls, fmt.Errorf(
			"get full PR #%d: %w", number, err,
		)
	}
	normalized, err := NormalizePR(repoID, fullPR)
	if err != nil {
		return calls, fmt.Errorf("normalize full PR #%d: %w", number, err)
	}
	preserveMergeableStateIfOmitted(normalized, existing)

	if normalized.Author != "" &&
		normalized.AuthorDisplayName == "" {
		host := repo.PlatformHost
		if host == "" {
			host = "github.com"
		}
		if name, ok := s.resolveDisplayName(
			ctx, client, host, normalized.Author,
		); ok {
			normalized.AuthorDisplayName = name
		}
		calls++ // GetUser
	}

	mrID, err := s.db.UpsertMergeRequest(ctx, normalized)
	if err != nil {
		return calls, fmt.Errorf(
			"upsert MR #%d: %w", number, err,
		)
	}
	if err := s.replaceMergeRequestLabels(ctx, repoID, mrID, normalized.Labels); err != nil {
		return calls, fmt.Errorf("persist labels for MR #%d: %w", number, err)
	}

	if err := s.db.EnsureKanbanState(ctx, mrID); err != nil {
		return calls, fmt.Errorf(
			"ensure kanban state for MR #%d: %w", number, err,
		)
	}

	// Diff SHAs if clone available.
	cloneRepoHost := repo.PlatformHost
	if cloneRepoHost == "" {
		cloneRepoHost = "github.com"
	}
	if s.clones != nil && cloneFetchOK {
		headSHA := normalized.PlatformHeadSHA
		baseSHA := normalized.PlatformBaseSHA
		if headSHA != "" && baseSHA != "" {
			mb, mbErr := s.clones.MergeBase(
				ctx, cloneRepoHost, repo.Owner,
				repo.Name, baseSHA, headSHA,
			)
			if mbErr != nil {
				slog.Warn("merge-base computation failed",
					"repo", repo.Owner+"/"+repo.Name,
					"number", number, "err", mbErr,
				)
			} else {
				if dbErr := s.db.UpdateDiffSHAs(
					ctx, repoID, number,
					headSHA, baseSHA, mb,
				); dbErr != nil {
					slog.Warn("update diff SHAs failed",
						"repo", repo.Owner+"/"+repo.Name,
						"number", number, "err", dbErr,
					)
				}
			}
		}
	}

	if err := s.refreshTimeline(
		ctx, repo, repoID, mrID, fullPR,
	); err != nil {
		// Timeline = 4 base calls (comments + reviews + commits + force-push);
		// provider review-thread sync is handled inside refreshTimeline.
		calls += 4
		return calls, err
	}
	calls += 4

	ciHeadSHA := ""
	if fullPR.GetHead() != nil {
		ciHeadSHA = fullPR.GetHead().GetSHA()
	}
	if err := s.refreshCIStatus(
		ctx, repo, repoID, number, ciHeadSHA,
	); err != nil {
		// CI = 2 calls (combined status + check runs).
		calls += 2
		return calls, err
	}
	calls += 2

	// Refresh workflow approval state so the DB-only detail GET
	// can render the Approve workflows button without a foreground
	// sync. Same path as syncMRForRepo, but the budgeted detail
	// drain needs to count this call too.
	s.refreshWorkflowApproval(
		ctx, repo, repoID, number, ciHeadSHA, fullPR, normalized,
	)
	calls++

	// Determine whether CI had pending checks for scoring by
	// reading the DB row that refreshCIStatus just wrote. Use
	// ciHasPending (checks individual statuses) rather than the
	// aggregate CIStatus, which becomes "failure" when any check
	// fails even if others are still running.
	pending := false
	freshMR, freshErr := s.db.GetMergeRequestByRepoIDAndNumber(
		ctx, repoID, number,
	)
	if freshErr == nil && freshMR != nil {
		pending = ciHasPending(freshMR.CIChecksJSON)
	}

	if err := s.updateMRDetailFetchedByRepoID(
		ctx, repoID, number, pending,
	); err != nil {
		return calls, fmt.Errorf(
			"mark detail fetched for MR #%d: %w", number, err,
		)
	}

	// Fire onMRSynced hook.
	if s.onMRSynced != nil {
		fresh, fErr := s.db.GetMergeRequestByRepoIDAndNumber(
			ctx, repoID, number,
		)
		if fErr != nil {
			slog.Warn("get MR for onMRSynced hook failed",
				"repo", repo.Owner+"/"+repo.Name,
				"number", number, "err", fErr,
			)
		} else {
			s.onMRSynced(repo.Owner, repo.Name, fresh)
		}
	}

	if newETag != "" {
		if err := s.db.UpsertHTTPEtag(
			ctx, string(repoPlatform(repo)), repoHost(repo),
			repo.Owner, repo.Name, "pull_request", number, newETag,
		); err != nil {
			slog.Warn("persist pull request ETag failed",
				"repo", repo.Owner+"/"+repo.Name,
				"number", number,
				"err", err,
			)
		}
	}

	return calls, nil
}

func (s *Syncer) getPullRequestForDetail(
	ctx context.Context,
	client Client,
	repo RepoRef,
	number int,
) (*gh.PullRequest, string, bool, error) {
	conditional, ok := client.(conditionalPullRequestGetter)
	if !ok {
		pr, err := client.GetPullRequest(ctx, repo.Owner, repo.Name, number)
		return pr, "", false, err
	}

	etag, err := s.db.GetHTTPEtag(
		ctx, string(repoPlatform(repo)), repoHost(repo),
		repo.Owner, repo.Name, "pull_request", number,
	)
	if err != nil {
		slog.Warn("load pull request ETag failed",
			"repo", repo.Owner+"/"+repo.Name,
			"number", number,
			"err", err,
		)
		pr, err := client.GetPullRequest(ctx, repo.Owner, repo.Name, number)
		return pr, "", false, err
	}
	return conditional.GetPullRequestIfChanged(
		ctx, repo.Owner, repo.Name, number, etag,
	)
}

func (s *Syncer) markUnchangedMRDetailFetched(
	ctx context.Context,
	repo RepoRef,
	repoID int64,
	number int,
	existing *db.MergeRequest,
	calls int,
) (int, error) {
	pending := existing.CIHadPending
	if existing.CIHadPending && existing.PlatformHeadSHA != "" {
		if err := s.refreshCIStatus(
			ctx, repo, repoID, number, existing.PlatformHeadSHA,
		); err != nil {
			calls += 2
			return calls, err
		}
		calls += 2
		fresh, err := s.db.GetMergeRequestByRepoIDAndNumber(ctx, repoID, number)
		if err == nil && fresh != nil {
			pending = ciHasPending(fresh.CIChecksJSON)
		}
	}
	if err := s.updateMRDetailFetchedByRepoID(ctx, repoID, number, pending); err != nil {
		return calls, fmt.Errorf("mark unchanged detail fetched for MR #%d: %w", number, err)
	}
	if s.onMRSynced != nil {
		fresh, fErr := s.db.GetMergeRequestByRepoIDAndNumber(ctx, repoID, number)
		if fErr != nil {
			slog.Warn("get MR for onMRSynced hook failed",
				"repo", repo.Owner+"/"+repo.Name,
				"number", number, "err", fErr,
			)
		} else {
			s.onMRSynced(repo.Owner, repo.Name, fresh)
		}
	}
	return calls, nil
}

func (s *Syncer) fetchProviderMRDetail(
	ctx context.Context,
	reader platform.MergeRequestReader,
	repo RepoRef,
	repoID int64,
	number int,
) (int, error) {
	calls := 0
	mr, err := reader.GetMergeRequest(ctx, platformRepoRef(repo), number)
	calls++
	if err != nil {
		return calls, fmt.Errorf("get full MR #%d: %w", number, err)
	}

	normalized := platform.DBMergeRequest(repoID, mr)
	existing, err := s.db.GetMergeRequestByRepoIDAndNumber(
		ctx, repoID, number,
	)
	if err != nil {
		return calls, fmt.Errorf(
			"get existing MR #%d: %w", number, err,
		)
	}
	preserveMergeableStateIfOmitted(normalized, existing)

	mrID, err := s.db.UpsertMergeRequest(ctx, normalized)
	if err != nil {
		return calls, fmt.Errorf(
			"upsert MR #%d: %w", number, err,
		)
	}
	if err := s.replaceMergeRequestLabels(ctx, repoID, mrID, normalized.Labels); err != nil {
		return calls, fmt.Errorf("persist labels for MR #%d: %w", number, err)
	}
	if err := s.db.EnsureKanbanState(ctx, mrID); err != nil {
		return calls, fmt.Errorf(
			"ensure kanban state for MR #%d: %w", number, err,
		)
	}

	detailCalls, pending, err := s.syncProviderMRDetailExtras(
		ctx, reader, repo, repoID, mrID, number, normalized.PlatformHeadSHA,
	)
	calls += detailCalls
	if err != nil {
		return calls, err
	}

	if err := s.updateMRDetailFetchedByRepoID(ctx, repoID, number, pending); err != nil {
		return calls, fmt.Errorf("mark detail fetched for MR #%d: %w", number, err)
	}

	if s.onMRSynced != nil {
		fresh, fErr := s.db.GetMergeRequestByRepoIDAndNumber(ctx, repoID, number)
		if fErr != nil {
			slog.Warn("get MR for onMRSynced hook failed",
				"repo", repo.Owner+"/"+repo.Name,
				"number", number, "err", fErr,
			)
		} else {
			s.onMRSynced(repo.Owner, repo.Name, fresh)
		}
	}

	return calls, nil
}

func (s *Syncer) syncProviderMRDetailExtras(
	ctx context.Context,
	reader platform.MergeRequestReader,
	repo RepoRef,
	repoID int64,
	mrID int64,
	number int,
	headSHA string,
) (int, bool, error) {
	calls := 0
	events, err := reader.ListMergeRequestEvents(ctx, platformRepoRef(repo), number)
	calls++
	if err != nil && !errors.Is(err, platform.ErrUnsupportedCapability) {
		return calls, false, fmt.Errorf("list MR events for #%d: %w", number, err)
	}
	if err == nil {
		dbEvents := make([]db.MREvent, 0, len(events))
		commentDedupeKeys := make([]string, 0, len(events))
		for _, event := range events {
			dbEvent := platform.DBMREvent(mrID, event)
			dbEvents = append(dbEvents, dbEvent)
			if dbEvent.EventType == "issue_comment" {
				commentDedupeKeys = append(commentDedupeKeys, dbEvent.DedupeKey)
			}
		}
		if err := s.db.DeleteMissingMRCommentEvents(ctx, mrID, commentDedupeKeys); err != nil {
			return calls, false, fmt.Errorf("delete missing comment events for MR #%d: %w", number, err)
		}
		if err := s.db.UpsertMREvents(ctx, dbEvents); err != nil {
			return calls, false, fmt.Errorf("upsert events for MR #%d: %w", number, err)
		}
	}

	reviewThreadCalls, err := s.syncProviderMRReviewThreads(ctx, repo, mrID, number)
	calls += reviewThreadCalls
	if err != nil {
		return calls, false, fmt.Errorf("sync review threads for MR #%d: %w", number, err)
	}

	pending := false
	if headSHA == "" {
		return calls, pending, nil
	}
	ciReader, err := s.ciReaderFor(repo)
	if err != nil {
		if errors.Is(err, platform.ErrUnsupportedCapability) {
			return calls, pending, nil
		}
		return calls, false, fmt.Errorf("resolve CI reader for %s/%s: %w", repo.Owner, repo.Name, err)
	}
	checks, err := ciReader.ListCIChecks(ctx, platformRepoRef(repo), headSHA)
	calls++
	if err != nil && !errors.Is(err, platform.ErrUnsupportedCapability) {
		return calls, false, fmt.Errorf("list CI checks for MR #%d: %w", number, err)
	}
	if err != nil {
		return calls, pending, nil
	}
	dbChecks := platform.DBCIChecks(checks)
	if dbChecks == nil {
		dbChecks = []db.CICheck{}
	}
	ciJSON, _ := json.Marshal(dbChecks)
	ciStatus := deriveCIStatusFromChecks(dbChecks)
	if err := s.db.UpdateMRCIStatus(ctx, repoID, number, ciStatus, string(ciJSON)); err != nil {
		return calls, false, fmt.Errorf("update CI status for MR #%d: %w", number, err)
	}
	pending = ciHasPending(string(ciJSON))
	return calls, pending, nil
}

func (s *Syncer) syncProviderMRReviewThreads(
	ctx context.Context,
	repo RepoRef,
	mrID int64,
	number int,
) (int, error) {
	caps, err := s.clients.Capabilities(repoPlatform(repo), repoHost(repo))
	if err != nil {
		if errors.Is(err, platform.ErrUnsupportedCapability) {
			return 0, nil
		}
		return 0, err
	}
	if !caps.ReadReviewThreads {
		return 0, nil
	}
	reader, err := s.clients.MergeRequestReviewThreadReader(repoPlatform(repo), repoHost(repo))
	if err != nil {
		return 0, err
	}
	threads, err := reader.ListMergeRequestReviewThreads(ctx, platformRepoRef(repo), number)
	calls := 1
	if err != nil {
		return calls, err
	}

	dbThreads := make([]db.MRReviewThread, 0, len(threads))
	events := make([]db.MREvent, 0, len(threads))
	providerThreadIDs := make([]string, 0, len(threads))
	seenProviderThreadIDs := make(map[string]struct{}, len(threads))
	for _, thread := range threads {
		providerThreadID := thread.ProviderThreadID
		if providerThreadID == "" {
			providerThreadID = thread.ProviderCommentID
		}
		if providerThreadID == "" {
			continue
		}
		if _, ok := seenProviderThreadIDs[providerThreadID]; ok {
			continue
		}
		seenProviderThreadIDs[providerThreadID] = struct{}{}
		providerThreadIDs = append(providerThreadIDs, providerThreadID)
		dbThreads = append(dbThreads, db.MRReviewThread{
			ProviderThreadID:  providerThreadID,
			ProviderReviewID:  thread.ProviderReviewID,
			ProviderCommentID: thread.ProviderCommentID,
			Body:              thread.Body,
			AuthorLogin:       thread.AuthorLogin,
			Range:             dbReviewLineRangeFromPlatform(thread.Range),
			Resolved:          thread.Resolved,
			CreatedAt:         thread.CreatedAt,
			UpdatedAt:         thread.UpdatedAt,
			ResolvedAt:        thread.ResolvedAt,
			MetadataJSON:      thread.MetadataJSON,
		})
		createdAt := thread.CreatedAt
		if createdAt.IsZero() {
			createdAt = time.Now().UTC()
		}
		events = append(events, db.MREvent{
			MergeRequestID:     mrID,
			PlatformExternalID: providerThreadID,
			EventType:          "review_comment",
			Author:             thread.AuthorLogin,
			Body:               thread.Body,
			CreatedAt:          createdAt,
			DedupeKey:          "review_comment:" + providerThreadID,
		})
	}
	if err := s.db.DeleteMissingMRReviewThreads(ctx, mrID, providerThreadIDs); err != nil {
		return calls, err
	}
	if err := s.db.UpsertMRReviewThreads(ctx, mrID, dbThreads); err != nil {
		return calls, err
	}
	if len(events) > 0 {
		if err := s.db.UpsertMREvents(ctx, events); err != nil {
			return calls, err
		}
	}
	return calls, nil
}

func dbReviewLineRangeFromPlatform(input platform.DiffReviewLineRange) db.ReviewLineRange {
	return db.ReviewLineRange{
		Path:        input.Path,
		OldPath:     input.OldPath,
		Side:        input.Side,
		StartSide:   input.StartSide,
		StartLine:   input.StartLine,
		Line:        input.Line,
		OldLine:     input.OldLine,
		NewLine:     input.NewLine,
		LineType:    input.LineType,
		DiffHeadSHA: input.DiffHeadSHA,
		CommitSHA:   input.CommitSHA,
	}
}

// fetchIssueDetail performs a full detail fetch for a single
// issue: GetIssue + refreshIssueTimeline. Returns the number
// of API calls made.
func (s *Syncer) fetchIssueDetail(
	ctx context.Context,
	repo RepoRef,
	repoID int64,
	number int,
) (int, error) {
	calls := 0
	issueReader, err := s.issueReaderFor(repo)
	if err != nil {
		return calls, fmt.Errorf("resolve issue reader for %s/%s: %w", repo.Owner, repo.Name, err)
	}
	if _, ok := issueReader.(interface {
		GetGitHubIssue(context.Context, platform.RepoRef, int) (*gh.Issue, error)
	}); !ok {
		return s.fetchProviderIssueDetail(ctx, issueReader, repo, repoID, number)
	}

	client, err := s.clientFor(repo)
	if err != nil {
		return calls, fmt.Errorf("resolve client for %s/%s: %w", repo.Owner, repo.Name, err)
	}

	ghIssue, newETag, notModified, err := s.getIssueForDetail(
		ctx, client, repo, number,
	)
	calls++
	if err == nil && ghIssue == nil {
		if notModified {
			if err := s.updateIssueDetailFetchedByRepoID(
				ctx, repoID, number,
			); err != nil {
				return calls, fmt.Errorf(
					"mark unchanged detail fetched for issue #%d: %w", number, err,
				)
			}
			return calls, nil
		}
		err = fmt.Errorf("client returned nil issue")
	}
	if err != nil {
		return calls, fmt.Errorf(
			"get issue #%d: %w", number, err,
		)
	}
	normalized, err := NormalizeIssue(repoID, ghIssue)
	if err != nil {
		return calls, fmt.Errorf("normalize issue #%d: %w", number, err)
	}
	issueID, err := s.db.UpsertIssue(ctx, normalized)
	if err != nil {
		return calls, fmt.Errorf(
			"upsert issue #%d: %w", number, err,
		)
	}
	if err := s.replaceIssueLabels(ctx, repoID, issueID, normalized.Labels); err != nil {
		return calls, fmt.Errorf("persist labels for issue #%d: %w", number, err)
	}

	if err := s.refreshIssueTimeline(
		ctx, repo, issueID, ghIssue,
	); err != nil {
		calls++ // comments
		return calls, err
	}
	calls++ // comments

	if err := s.updateIssueDetailFetchedByRepoID(
		ctx, repoID, number,
	); err != nil {
		return calls, fmt.Errorf(
			"mark detail fetched for issue #%d: %w", number, err,
		)
	}

	if newETag != "" {
		if err := s.db.UpsertHTTPEtag(
			ctx, string(repoPlatform(repo)), repoHost(repo),
			repo.Owner, repo.Name, "issue", number, newETag,
		); err != nil {
			slog.Warn("persist issue ETag failed",
				"repo", repo.Owner+"/"+repo.Name,
				"number", number,
				"err", err,
			)
		}
	}

	return calls, nil
}

func (s *Syncer) getIssueForDetail(
	ctx context.Context,
	client Client,
	repo RepoRef,
	number int,
) (*gh.Issue, string, bool, error) {
	conditional, ok := client.(conditionalIssueGetter)
	if !ok {
		issue, err := client.GetIssue(ctx, repo.Owner, repo.Name, number)
		return issue, "", false, err
	}

	etag, err := s.db.GetHTTPEtag(
		ctx, string(repoPlatform(repo)), repoHost(repo),
		repo.Owner, repo.Name, "issue", number,
	)
	if err != nil {
		slog.Warn("load issue ETag failed",
			"repo", repo.Owner+"/"+repo.Name,
			"number", number,
			"err", err,
		)
		issue, err := client.GetIssue(ctx, repo.Owner, repo.Name, number)
		return issue, "", false, err
	}
	return conditional.GetIssueIfChanged(
		ctx, repo.Owner, repo.Name, number, etag,
	)
}

func (s *Syncer) fetchProviderIssueDetail(
	ctx context.Context,
	reader platform.IssueReader,
	repo RepoRef,
	repoID int64,
	number int,
) (int, error) {
	calls := 0
	issue, err := reader.GetIssue(ctx, platformRepoRef(repo), number)
	calls++
	if err != nil {
		return calls, fmt.Errorf("get issue #%d: %w", number, err)
	}

	normalized := platform.DBIssue(repoID, issue)
	issueID, err := s.db.UpsertIssue(ctx, normalized)
	if err != nil {
		return calls, fmt.Errorf(
			"upsert issue #%d: %w", number, err,
		)
	}
	if err := s.replaceIssueLabels(ctx, repoID, issueID, normalized.Labels); err != nil {
		return calls, fmt.Errorf("persist labels for issue #%d: %w", number, err)
	}

	events, err := reader.ListIssueEvents(ctx, platformRepoRef(repo), number)
	calls++
	if err != nil && !errors.Is(err, platform.ErrUnsupportedCapability) {
		return calls, fmt.Errorf("list issue events for #%d: %w", number, err)
	}
	if err == nil {
		dbEvents := make([]db.IssueEvent, 0, len(events))
		for _, event := range events {
			dbEvents = append(dbEvents, platform.DBIssueEvent(issueID, event))
		}
		if err := s.db.UpsertIssueEvents(ctx, dbEvents); err != nil {
			return calls, fmt.Errorf("upsert issue events for #%d: %w", number, err)
		}
	}

	if err := s.updateIssueDetailFetchedByRepoID(
		ctx, repoID, number,
	); err != nil {
		return calls, fmt.Errorf(
			"mark detail fetched for issue #%d: %w", number, err,
		)
	}

	return calls, nil
}

func (s *Syncer) updateMRDetailFetchedByRepoID(
	ctx context.Context,
	repoID int64,
	number int,
	ciHadPending bool,
) error {
	return s.db.UpdateMRDetailFetchedByRepoID(
		ctx, repoID, number, ciHadPending,
	)
}

func (s *Syncer) clearMRDetailFetchedByRepoID(
	ctx context.Context,
	repoID int64,
	number int,
) error {
	return s.db.ClearMRDetailFetchedByRepoID(ctx, repoID, number)
}

func (s *Syncer) updateIssueDetailFetchedByRepoID(
	ctx context.Context,
	repoID int64,
	number int,
) error {
	return s.db.UpdateIssueDetailFetchedByRepoID(ctx, repoID, number)
}

// refreshTimeline fetches comments, reviews, and commits for a PR and
// updates its derived fields (ReviewDecision, CommentCount, LastActivityAt, CIStatus).
func (s *Syncer) refreshTimeline(
	ctx context.Context,
	repo RepoRef,
	repoID int64,
	mrID int64,
	ghPR *gh.PullRequest,
) error {
	if ghPR == nil {
		return fmt.Errorf("nil pull request")
	}
	number := ghPR.GetNumber()
	client, err := s.clientFor(repo)
	if err != nil {
		return fmt.Errorf("resolve client for %s/%s: %w", repo.Owner, repo.Name, err)
	}

	comments, err := client.ListIssueComments(ctx, repo.Owner, repo.Name, number)
	if err != nil {
		return fmt.Errorf("list comments for MR #%d: %w", number, err)
	}

	reviews, err := client.ListReviews(ctx, repo.Owner, repo.Name, number)
	if err != nil {
		return fmt.Errorf("list reviews for MR #%d: %w", number, err)
	}

	commits, err := client.ListCommits(ctx, repo.Owner, repo.Name, number)
	if err != nil {
		return fmt.Errorf("list commits for MR #%d: %w", number, err)
	}

	timelineEvents, err := client.ListPullRequestTimelineEvents(ctx, repo.Owner, repo.Name, number)
	if err != nil {
		slog.Warn("timeline event fetch failed during timeline refresh",
			"repo", repo.Owner+"/"+repo.Name,
			"number", number,
			"err", err,
		)
		timelineEvents = nil
	}

	var events []db.MREvent
	for _, c := range comments {
		events = append(events, NormalizeCommentEvent(mrID, c))
	}
	for _, r := range reviews {
		events = append(events, NormalizeReviewEvent(mrID, r))
	}
	for _, c := range commits {
		events = append(events, NormalizeCommitEvent(mrID, c))
	}
	for _, timelineEvent := range timelineEvents {
		event := NormalizeTimelineEvent(mrID, timelineEvent)
		if event == nil {
			continue
		}
		events = append(events, *event)
	}

	if err := s.replacePRCommentEvents(ctx, mrID, comments); err != nil {
		return fmt.Errorf("replace comment events for MR #%d: %w", number, err)
	}
	if err := s.db.UpsertMREvents(ctx, events); err != nil {
		return fmt.Errorf("upsert events for MR #%d: %w", number, err)
	}
	if _, err := s.syncProviderMRReviewThreads(ctx, repo, mrID, number); err != nil {
		return fmt.Errorf("sync review threads for MR #%d: %w", number, err)
	}

	reviewDecision := DeriveReviewDecision(reviews)
	lastActivityAt := computeLastActivity(ghPR, comments, reviews, commits)

	return s.db.UpdateMRDerivedFields(ctx, repoID, number, db.MRDerivedFields{
		ReviewDecision: reviewDecision,
		CommentCount:   len(comments),
		LastActivityAt: lastActivityAt,
	})
}

// RefreshMRCIStatusOnProvider fetches only CI checks for a PR's head SHA and
// persists the derived CI fields. It intentionally skips the heavier PR detail
// sync path (timeline, diff, review, and body refreshes).
func (s *Syncer) RefreshMRCIStatusOnProvider(
	ctx context.Context,
	repo RepoRef,
	repoID int64,
	number int,
	headSHA string,
) ([]string, error) {
	if headSHA == "" {
		return nil, nil
	}
	if repoPlatform(repo) == platform.KindGitHub {
		result, err := s.fetchGitHubCIStatus(ctx, repo, number, headSHA)
		if err != nil {
			return nil, err
		}
		if result.Warning != "" {
			return []string{result.Warning}, nil
		}
		if !result.Updated {
			return nil, nil
		}
		return nil, s.db.UpdateMRCIStatusForHead(
			ctx, repoID, number, headSHA,
			result.Status, result.ChecksJSON, ciHasPending(result.ChecksJSON),
		)
	}

	ciReader, err := s.ciReaderFor(repo)
	if err != nil {
		if errors.Is(err, platform.ErrUnsupportedCapability) {
			return nil, nil
		}
		return nil, fmt.Errorf("resolve CI reader for %s/%s: %w", repo.Owner, repo.Name, err)
	}
	checks, err := ciReader.ListCIChecks(ctx, platformRepoRef(repo), headSHA)
	if err != nil {
		if errors.Is(err, platform.ErrUnsupportedCapability) {
			return nil, nil
		}
		slog.Warn("list CI checks failed",
			"repo", repo.Owner+"/"+repo.Name,
			"number", number,
			"err", err,
		)
		return []string{ciRefreshWarning}, nil
	}
	dbChecks := platform.DBCIChecks(checks)
	if dbChecks == nil {
		dbChecks = []db.CICheck{}
	}
	ciJSON, _ := json.Marshal(dbChecks)
	ciStatus := deriveCIStatusFromChecks(dbChecks)
	if err := s.db.UpdateMRCIStatusForHead(
		ctx, repoID, number, headSHA,
		ciStatus, string(ciJSON), ciHasPending(string(ciJSON)),
	); err != nil {
		return nil, fmt.Errorf("update CI status for MR #%d: %w", number, err)
	}
	return nil, nil
}

// refreshCIStatus fetches combined status and check runs for a PR's head SHA.
// Called on every sync cycle for open PRs, since check runs change independently
// of the PR's updated_at field. Takes headSHA and number directly so it can be
// invoked from the 304 code path, where the caller holds DB rows rather than
// a *gh.PullRequest.
func (s *Syncer) refreshCIStatus(
	ctx context.Context,
	repo RepoRef,
	repoID int64,
	number int,
	headSHA string,
) error {
	result, err := s.fetchGitHubCIStatus(ctx, repo, number, headSHA)
	if err != nil {
		return err
	}
	if !result.Updated {
		return nil
	}
	return s.db.UpdateMRCIStatus(ctx, repoID, number, result.Status, result.ChecksJSON)
}

const ciRefreshWarning = "Could not refresh CI checks; showing last known status."

type ciStatusFetchResult struct {
	Status     string
	ChecksJSON string
	Updated    bool
	Warning    string
}

func (s *Syncer) fetchGitHubCIStatus(
	ctx context.Context,
	repo RepoRef,
	number int,
	headSHA string,
) (ciStatusFetchResult, error) {
	if headSHA == "" {
		return ciStatusFetchResult{}, nil
	}

	// Fetch both sources. On failure, skip the DB write to preserve
	// existing data rather than wiping it with empty values.
	client, err := s.clientFor(repo)
	if err != nil {
		return ciStatusFetchResult{}, fmt.Errorf("resolve client for %s/%s: %w", repo.Owner, repo.Name, err)
	}
	checkRuns, err := client.ListCheckRunsForRef(ctx, repo.Owner, repo.Name, headSHA)
	if err != nil {
		slog.Warn("list check runs failed",
			"repo", repo.Owner+"/"+repo.Name,
			"number", number,
			"err", err,
		)
		return ciStatusFetchResult{Warning: ciRefreshWarning}, nil
	}

	combined, err := client.GetCombinedStatus(ctx, repo.Owner, repo.Name, headSHA)
	if err != nil {
		slog.Warn("get combined status failed",
			"repo", repo.Owner+"/"+repo.Name,
			"number", number,
			"err", err,
		)
		return ciStatusFetchResult{Warning: ciRefreshWarning}, nil
	}

	return ciStatusFetchResult{
		Status:     DeriveOverallCIStatus(checkRuns, combined),
		ChecksJSON: NormalizeCIChecks(checkRuns, combined),
		Updated:    true,
	}, nil
}

// refreshWorkflowApproval fetches action_required workflow runs at the
// given head SHA and persists the result on the merge request row.
//
// The persisted snapshot is keyed by head SHA so that a later DB-only
// read can ignore the snapshot once the PR's head moves (force-push),
// preventing a stale "Approve workflows" button on a fresh commit.
//
// Failures (no client, network errors, closed PR) are intentionally
// silent: this is a refresh, not a precondition. The previous
// persisted state stays in place rather than being clobbered.
func (s *Syncer) refreshWorkflowApproval(
	ctx context.Context,
	repo RepoRef,
	repoID int64,
	number int,
	headSHA string,
	ghPR *gh.PullRequest,
	normalized *db.MergeRequest,
) {
	if headSHA == "" {
		return
	}
	state := ""
	switch {
	case ghPR != nil:
		state = ghPR.GetState()
	case normalized != nil:
		state = string(normalized.State)
	}
	if state != "open" {
		return
	}

	client, err := s.clientFor(repo)
	if err != nil {
		return
	}
	runs, err := client.ListWorkflowRunsForHeadSHA(ctx, repo.Owner, repo.Name, headSHA)
	if err != nil {
		slog.Warn("list workflow runs for approval refresh failed",
			"repo", repo.Owner+"/"+repo.Name,
			"number", number,
			"err", err,
		)
		return
	}

	headRepoFullName := ""
	headRef := ""
	if ghPR != nil && ghPR.GetHead() != nil {
		headRepoFullName = ghPR.GetHead().GetRepo().GetFullName()
		headRef = ghPR.GetHead().GetRef()
	}
	// GraphQL bulk fetch populates clone URL but not full name on the
	// head repo struct, so fall back to parsing the persisted clone
	// URL. Without this, fork PRs synced via bulk would lose the head
	// repo identity needed to match fork-triggered workflow runs whose
	// pull_requests array is empty.
	if headRepoFullName == "" && normalized != nil {
		headRepoFullName = ParseHeadRepoFullName(normalized.HeadRepoCloneURL)
	}
	if headRef == "" && normalized != nil {
		headRef = normalized.HeadBranch
	}

	approval := WorkflowApprovalStateFromRuns(
		FilterWorkflowRunsAwaitingApproval(runs, PRSource{
			Number:           number,
			HeadSHA:          headSHA,
			HeadRepoFullName: headRepoFullName,
			HeadRef:          headRef,
		}),
	)
	if err := s.db.UpdateMRWorkflowApproval(
		ctx, repoID, number, time.Now().UTC(), headSHA, approval.Required, approval.Count,
	); err != nil {
		slog.Warn("persist workflow approval state failed",
			"repo", repo.Owner+"/"+repo.Name,
			"number", number,
			"err", err,
		)
	}
}

// ciHasPending parses the CI checks JSON and returns true if any
// check has a status other than "completed".
func ciHasPending(ciChecksJSON string) bool {
	if ciChecksJSON == "" {
		return false
	}
	var checks []db.CICheck
	if err := json.Unmarshal([]byte(ciChecksJSON), &checks); err != nil {
		return false
	}
	for _, c := range checks {
		if c.Status != "completed" {
			return true
		}
	}
	return false
}

// computeLastActivity returns the most recent timestamp across the PR and its events.
func computeLastActivity(
	ghPR *gh.PullRequest,
	comments []*gh.IssueComment,
	reviews []*gh.PullRequestReview,
	commits []*gh.RepositoryCommit,
) time.Time {
	latest := time.Time{}
	if ghPR.UpdatedAt != nil {
		latest = ghPR.UpdatedAt.Time
	}

	for _, c := range comments {
		if c.UpdatedAt != nil && c.UpdatedAt.After(latest) {
			latest = c.UpdatedAt.Time
		}
	}
	for _, r := range reviews {
		if r.SubmittedAt != nil && r.SubmittedAt.After(latest) {
			latest = r.SubmittedAt.Time
		}
	}
	for _, c := range commits {
		if c.GetCommit() != nil && c.GetCommit().Author != nil &&
			c.GetCommit().Author.Date != nil &&
			c.GetCommit().Author.Date.After(latest) {
			latest = c.GetCommit().Author.Date.Time
		}
	}
	return latest
}

// computePRCommentRefreshLastActivity derives last_activity_at for a
// comment-only refresh. nonCommentLatest should be the most recent
// timestamp among stored review/commit/force-push events so a refresh
// that only sees comments can't regress activity captured by those
// events when GitHub's PR.UpdatedAt is stale.
func computePRCommentRefreshLastActivity(
	pr *db.MergeRequest,
	comments []*gh.IssueComment,
	nonCommentLatest time.Time,
) time.Time {
	latest := pr.UpdatedAt
	if latest.IsZero() || pr.CreatedAt.After(latest) {
		latest = pr.CreatedAt
	}
	if nonCommentLatest.After(latest) {
		latest = nonCommentLatest
	}
	for _, c := range comments {
		switch {
		case c.UpdatedAt != nil && c.UpdatedAt.After(latest):
			latest = c.UpdatedAt.Time
		case c.CreatedAt != nil && c.CreatedAt.After(latest):
			latest = c.CreatedAt.Time
		}
	}
	return latest
}

func computeIssueCommentLastActivity(
	ghIssue *gh.Issue,
	comments []*gh.IssueComment,
) time.Time {
	var latest time.Time
	if ghIssue != nil {
		if ghIssue.UpdatedAt != nil {
			latest = ghIssue.UpdatedAt.Time
		}
		if latest.IsZero() && ghIssue.CreatedAt != nil {
			latest = ghIssue.CreatedAt.Time
		}
	}
	for _, c := range comments {
		switch {
		case c.UpdatedAt != nil && c.UpdatedAt.After(latest):
			latest = c.UpdatedAt.Time
		case c.CreatedAt != nil && c.CreatedAt.After(latest):
			latest = c.CreatedAt.Time
		}
	}
	return latest
}

func computeIssueCommentRefreshLastActivity(
	issue *db.Issue,
	comments []*gh.IssueComment,
) time.Time {
	return computeIssueCommentLastActivity(&gh.Issue{
		CreatedAt: &gh.Timestamp{Time: issue.CreatedAt},
		UpdatedAt: &gh.Timestamp{Time: issue.UpdatedAt},
	}, comments)
}

func (s *Syncer) replacePRCommentEvents(
	ctx context.Context,
	mrID int64,
	comments []*gh.IssueComment,
) error {
	events := make([]db.MREvent, 0, len(comments))
	dedupeKeys := make([]string, 0, len(comments))
	for _, c := range comments {
		event := NormalizeCommentEvent(mrID, c)
		events = append(events, event)
		dedupeKeys = append(dedupeKeys, event.DedupeKey)
	}
	if err := s.db.DeleteMissingMRCommentEvents(ctx, mrID, dedupeKeys); err != nil {
		return fmt.Errorf("delete missing mr comment events: %w", err)
	}
	if err := s.db.UpsertMREvents(ctx, events); err != nil {
		return fmt.Errorf("upsert mr comment events: %w", err)
	}
	return nil
}

func (s *Syncer) replaceIssueCommentEvents(
	ctx context.Context,
	issueID int64,
	comments []*gh.IssueComment,
) error {
	events := make([]db.IssueEvent, 0, len(comments))
	dedupeKeys := make([]string, 0, len(comments))
	for _, c := range comments {
		event := NormalizeIssueCommentEvent(issueID, c)
		events = append(events, event)
		dedupeKeys = append(dedupeKeys, event.DedupeKey)
	}
	if err := s.db.DeleteMissingIssueCommentEvents(ctx, issueID, dedupeKeys); err != nil {
		return fmt.Errorf("delete missing issue comment events: %w", err)
	}
	if err := s.db.UpsertIssueEvents(ctx, events); err != nil {
		return fmt.Errorf("upsert issue comment events: %w", err)
	}
	return nil
}

// resolveDisplayName returns the GitHub display name for a
// login and whether the lookup succeeded. Returns ("", false)
// on API failure so callers can preserve existing data. Uses a
// TTL + LRU cache that spans the Syncer's lifetime plus
// singleflight dedup so concurrent workers racing on the same
// author only trigger one GetUser call. When a refetch fails
// but a stale cache entry exists, the stale value is returned
// (stale-while-error).
//
// Bot logins (ending with "[bot]") are returned as-is since bot
// accounts have no display name on the GitHub API.
func (s *Syncer) resolveDisplayName(
	ctx context.Context, client Client, host, login string,
) (string, bool) {
	key := host + "\x00" + login
	if cached, fresh := s.displayNames.get(key); fresh {
		return cached.name, cached.ok
	}
	if strings.HasSuffix(login, "[bot]") {
		s.displayNames.putSuccess(key, login)
		return login, true
	}

	v, err, _ := s.displayNameGroup.Do(key, func() (any, error) {
		// Re-check the cache inside the singleflight slot:
		// another caller may have populated a fresh entry
		// while this one was waiting for its turn to run.
		if cached, fresh := s.displayNames.get(key); fresh {
			return cached, nil
		}
		user, err := client.GetUser(ctx, login)
		if err != nil {
			return displayNameEntry{}, err
		}
		name := nameOrEmpty(user)
		s.displayNames.putSuccess(key, name)
		return displayNameEntry{name: name, ok: true}, nil
	})
	if err != nil {
		// Fall back to a stale cached name if one exists so a
		// transient network error does not blank out an
		// already-known name. A zero entry has ok=false, so a
		// total miss falls through to the failure path below.
		//
		// Also back off the retry window: re-use the stored
		// name but with failureTTL so repeated failures do not
		// hit /users every sync for the life of successTTL.
		if stale, _ := s.displayNames.get(key); stale.ok {
			s.displayNames.putStaleFallback(key, stale.name)
			return stale.name, true
		}
		slog.Warn("get user display name failed",
			"login", login, "err", err,
		)
		s.displayNames.putFailure(key)
		return "", false
	}
	result := v.(displayNameEntry)
	return result.name, result.ok
}

// --- Issue sync ---

// syncIssuesFromList processes a pre-fetched list of open issues
// via the REST path. Handles per-issue upsert and closure detection.
func (s *Syncer) syncIssuesFromList(
	ctx context.Context,
	client Client,
	repo RepoRef,
	repoID int64,
	ghIssues []*gh.Issue,
	forceRefresh bool,
) error {
	stillOpen := make(map[int]bool, len(ghIssues))
	for _, issue := range ghIssues {
		stillOpen[issue.GetNumber()] = true
	}

	var hadItemFailure bool
	progress := newIssueSyncProgressLogger(repo, "rest", len(ghIssues))
	for i, ghIssue := range ghIssues {
		if err := s.syncOpenIssue(ctx, client, repo, repoID, ghIssue, forceRefresh); err != nil {
			slog.Error("sync issue failed",
				"repo", repo.Owner+"/"+repo.Name,
				"number", ghIssue.GetNumber(),
				"err", err,
			)
			hadItemFailure = true
		}
		progress.record(i + 1)
	}

	closedNumbers, err := s.db.GetPreviouslyOpenIssueNumbers(
		ctx, repoID, stillOpen,
	)
	if err != nil {
		return fmt.Errorf("get previously open issues: %w", err)
	}
	for _, number := range closedNumbers {
		if err := s.fetchAndUpdateClosedIssue(
			ctx, repo, repoID, number,
		); err != nil {
			slog.Error("update closed issue failed",
				"repo", repo.Owner+"/"+repo.Name,
				"number", number,
				"err", err,
			)
			hadItemFailure = true
		}
	}

	if hadItemFailure {
		return fmt.Errorf("one or more issue sync items failed")
	}
	progress.done()
	return nil
}

func (s *Syncer) syncPlatformIssuesFromList(
	ctx context.Context,
	reader platform.IssueReader,
	repo RepoRef,
	repoID int64,
	issues []platform.Issue,
	forceRefresh bool,
) error {
	stillOpen := make(map[int]bool, len(issues))
	for _, issue := range issues {
		stillOpen[issue.Number] = true
	}

	var hadItemFailure bool
	progress := newIssueSyncProgressLogger(repo, "provider", len(issues))
	for i, issue := range issues {
		if err := s.syncOpenPlatformIssue(ctx, reader, repo, repoID, issue, forceRefresh); err != nil {
			slog.Error("sync issue failed",
				"repo", repo.Owner+"/"+repo.Name,
				"number", issue.Number,
				"err", err,
			)
			hadItemFailure = true
		}
		progress.record(i + 1)
	}

	closedNumbers, err := s.db.GetPreviouslyOpenIssueNumbers(
		ctx, repoID, stillOpen,
	)
	if err != nil {
		return fmt.Errorf("get previously open issues: %w", err)
	}
	for _, number := range closedNumbers {
		if err := s.fetchAndUpdateClosedPlatformIssue(
			ctx, reader, repo, repoID, number,
		); err != nil {
			slog.Error("update closed issue failed",
				"repo", repo.Owner+"/"+repo.Name,
				"number", number,
				"err", err,
			)
			hadItemFailure = true
		}
	}

	if hadItemFailure {
		return fmt.Errorf("one or more issue sync items failed")
	}
	progress.done()
	return nil
}

func (s *Syncer) syncOpenPlatformIssue(
	ctx context.Context,
	reader platform.IssueReader,
	repo RepoRef,
	repoID int64,
	issue platform.Issue,
	forceRefresh bool,
) error {
	normalized := platform.DBIssue(repoID, issue)

	existing, err := s.db.GetIssueByRepoIDAndNumber(
		ctx, repoID, issue.Number,
	)
	if err != nil {
		return fmt.Errorf(
			"get existing issue #%d: %w", issue.Number, err,
		)
	}

	needsTimeline := forceRefresh || existing == nil ||
		!existing.UpdatedAt.Equal(normalized.UpdatedAt)

	issueID, err := s.db.UpsertIssue(ctx, normalized)
	if err != nil {
		return fmt.Errorf(
			"upsert issue #%d: %w", issue.Number, err,
		)
	}
	if err := s.replaceIssueLabels(ctx, repoID, issueID, normalized.Labels); err != nil {
		return fmt.Errorf("persist labels for issue #%d: %w", issue.Number, err)
	}

	if !needsTimeline {
		if existing != nil && existing.DetailFetchedAt != nil {
			s.queueIssueCommentSync(repo, existing.Number)
		}
		return nil
	}

	events, err := reader.ListIssueEvents(ctx, platformRepoRef(repo), issue.Number)
	if err != nil {
		if errors.Is(err, platform.ErrUnsupportedCapability) {
			return nil
		}
		return fmt.Errorf("list issue events for #%d: %w", issue.Number, err)
	}
	dbEvents := make([]db.IssueEvent, 0, len(events))
	for _, event := range events {
		dbEvents = append(dbEvents, platform.DBIssueEvent(issueID, event))
	}
	if err := s.db.UpsertIssueEvents(ctx, dbEvents); err != nil {
		return fmt.Errorf("upsert issue events for #%d: %w", issue.Number, err)
	}
	return nil
}

func (s *Syncer) syncOpenIssue(
	ctx context.Context,
	client Client,
	repo RepoRef,
	repoID int64,
	ghIssue *gh.Issue,
	forceRefresh bool,
) error {
	normalized, err := NormalizeIssue(repoID, ghIssue)
	if err != nil {
		return fmt.Errorf("normalize issue #%d: %w", ghIssue.GetNumber(), err)
	}

	existing, err := s.db.GetIssueByRepoIDAndNumber(
		ctx, repoID, ghIssue.GetNumber(),
	)
	if err != nil {
		return fmt.Errorf(
			"get existing issue #%d: %w", ghIssue.GetNumber(), err,
		)
	}

	needsTimeline := forceRefresh || existing == nil ||
		!existing.UpdatedAt.Equal(normalized.UpdatedAt)

	issueID, err := s.db.UpsertIssue(ctx, normalized)
	if err != nil {
		return fmt.Errorf(
			"upsert issue #%d: %w", ghIssue.GetNumber(), err,
		)
	}
	if err := s.replaceIssueLabels(ctx, repoID, issueID, normalized.Labels); err != nil {
		return fmt.Errorf("persist labels for issue #%d: %w", ghIssue.GetNumber(), err)
	}

	if !needsTimeline {
		if existing != nil && existing.DetailFetchedAt != nil {
			s.queueIssueCommentSync(repo, existing.Number)
		}
		return nil
	}

	return s.refreshIssueTimeline(ctx, repo, issueID, ghIssue)
}

func (s *Syncer) refreshIssueTimeline(
	ctx context.Context,
	repo RepoRef,
	issueID int64,
	ghIssue *gh.Issue,
) error {
	if ghIssue == nil {
		return fmt.Errorf("nil issue")
	}
	number := ghIssue.GetNumber()
	client, err := s.clientFor(repo)
	if err != nil {
		return fmt.Errorf("resolve client for %s/%s: %w", repo.Owner, repo.Name, err)
	}

	comments, err := client.ListIssueComments(
		ctx, repo.Owner, repo.Name, number,
	)
	if err != nil {
		return fmt.Errorf(
			"list comments for issue #%d: %w", number, err,
		)
	}

	if err := s.replaceIssueCommentEvents(ctx, issueID, comments); err != nil {
		return fmt.Errorf(
			"replace issue events for #%d: %w", number, err,
		)
	}

	if timelineClient, ok := client.(issueTimelineLister); ok {
		timelineEvents, err := timelineClient.ListIssueTimelineEvents(
			ctx, repo.Owner, repo.Name, number,
		)
		if err != nil {
			slog.Warn("issue timeline event fetch failed during timeline refresh",
				"repo", repo.Owner+"/"+repo.Name,
				"number", number,
				"err", err,
			)
		} else if err := s.upsertIssueTimelineEvents(ctx, issueID, timelineEvents); err != nil {
			return fmt.Errorf(
				"upsert issue timeline events for #%d: %w", number, err,
			)
		}
	}

	lastActivity := computeIssueCommentLastActivity(ghIssue, comments)

	_, err = s.db.WriteDB().ExecContext(ctx,
		`UPDATE middleman_issues SET comment_count = ?, last_activity_at = ?
		 WHERE id = ?`,
		len(comments), lastActivity, issueID,
	)
	return err
}

func (s *Syncer) upsertIssueTimelineEvents(
	ctx context.Context,
	issueID int64,
	timelineEvents []PullRequestTimelineEvent,
) error {
	events := make([]db.IssueEvent, 0, len(timelineEvents))
	for _, timelineEvent := range timelineEvents {
		event := NormalizeIssueTimelineEvent(issueID, timelineEvent)
		if event == nil {
			continue
		}
		events = append(events, *event)
	}
	if err := s.db.UpsertIssueEvents(ctx, events); err != nil {
		return fmt.Errorf("upsert issue timeline events: %w", err)
	}
	return nil
}

func (s *Syncer) refreshRepoPRComments(
	ctx context.Context,
	repo RepoRef,
) {
	prs, err := s.db.ListMergeRequests(ctx, db.ListMergeRequestsOpts{
		PlatformHost: repoHost(repo),
		RepoOwner:    repo.Owner,
		RepoName:     repo.Name,
		State:        "open",
	})
	if err != nil {
		slog.Warn("comment refresh: list open PRs failed",
			"repo", repo.Owner+"/"+repo.Name,
			"err", err,
		)
		return
	}

	client, err := s.clientFor(repo)
	if err != nil {
		slog.Warn("comment refresh: resolve client failed",
			"repo", repo.Owner+"/"+repo.Name,
			"err", err,
		)
		return
	}

	for i := range prs {
		if ctx.Err() != nil {
			return
		}
		s.refreshPRCommentsForItem(ctx, client, repo, &prs[i])
	}
}

func (s *Syncer) refreshRepoIssueComments(
	ctx context.Context,
	repo RepoRef,
) {
	issues, err := s.db.ListIssues(ctx, db.ListIssuesOpts{
		PlatformHost: repoHost(repo),
		RepoOwner:    repo.Owner,
		RepoName:     repo.Name,
		State:        "open",
	})
	if err != nil {
		slog.Warn("comment refresh: list open issues failed",
			"repo", repo.Owner+"/"+repo.Name,
			"err", err,
		)
		return
	}

	client, err := s.clientFor(repo)
	if err != nil {
		slog.Warn("comment refresh: resolve client failed",
			"repo", repo.Owner+"/"+repo.Name,
			"err", err,
		)
		return
	}

	for i := range issues {
		if ctx.Err() != nil {
			return
		}
		s.refreshIssueCommentsForItem(ctx, client, repo, &issues[i])
	}
}

func (s *Syncer) canSpendCommentRefresh(repo RepoRef) bool {
	budget := s.budgets[repoRateBucketKey(repo)]
	return budget == nil || budget.CanSpend(1)
}

func (s *Syncer) canSpendWorkflowApprovalRefresh(repo RepoRef) bool {
	budget := s.budgets[repoRateBucketKey(repo)]
	return budget == nil || budget.CanSpend(1)
}

func (s *Syncer) persistPRComments(
	ctx context.Context,
	pr *db.MergeRequest,
	comments []*gh.IssueComment,
) error {
	if err := s.replacePRCommentEvents(ctx, pr.ID, comments); err != nil {
		return fmt.Errorf("replace PR comment events: %w", err)
	}

	nonCommentLatest, err := s.db.GetMRLatestNonCommentEventTime(ctx, pr.ID)
	if err != nil {
		return fmt.Errorf("latest non-comment event for PR #%d: %w", pr.Number, err)
	}

	return s.db.UpdateMRDerivedFields(ctx, pr.RepoID, pr.Number, db.MRDerivedFields{
		ReviewDecision: pr.ReviewDecision,
		CommentCount:   len(comments),
		LastActivityAt: computePRCommentRefreshLastActivity(pr, comments, nonCommentLatest),
	})
}

func (s *Syncer) persistIssueComments(
	ctx context.Context,
	issue *db.Issue,
	comments []*gh.IssueComment,
) error {
	if err := s.replaceIssueCommentEvents(ctx, issue.ID, comments); err != nil {
		return fmt.Errorf("replace issue comment events: %w", err)
	}

	return s.db.UpdateIssueDerivedFields(ctx, issue.RepoID, issue.Number, db.IssueDerivedFields{
		CommentCount:   len(comments),
		LastActivityAt: computeIssueCommentRefreshLastActivity(issue, comments),
	})
}

func (s *Syncer) fetchAndUpdateClosedIssue(
	ctx context.Context, repo RepoRef, repoID int64, number int,
) error {
	client, err := s.clientFor(repo)
	if err != nil {
		return fmt.Errorf("resolve client for %s/%s: %w", repo.Owner, repo.Name, err)
	}
	ghIssue, err := client.GetIssue(
		ctx, repo.Owner, repo.Name, number,
	)
	if err != nil {
		return fmt.Errorf("get closed issue #%d: %w", number, err)
	}
	if ghIssue == nil {
		return fmt.Errorf("get closed issue #%d: client returned nil issue", number)
	}

	var closedAt *time.Time
	if ghIssue.ClosedAt != nil {
		t := ghIssue.ClosedAt.Time
		closedAt = &t
	}

	if err := s.db.UpdateIssueState(
		ctx, repoID, number, ghIssue.GetState(), closedAt,
	); err != nil {
		return err
	}

	issue, err := s.db.GetIssueByRepoIDAndNumber(ctx, repoID, number)
	if err != nil {
		return fmt.Errorf("get closed issue #%d for labels: %w", number, err)
	}
	if issue != nil {
		normalized, err := NormalizeIssue(repoID, ghIssue)
		if err != nil {
			return fmt.Errorf("normalize closed issue #%d: %w", number, err)
		}
		if err := s.replaceIssueLabels(ctx, repoID, issue.ID, normalized.Labels); err != nil {
			return fmt.Errorf("persist labels for closed issue #%d: %w", number, err)
		}
	}

	return nil
}

func (s *Syncer) fetchAndUpdateClosedPlatformIssue(
	ctx context.Context,
	reader platform.IssueReader,
	repo RepoRef,
	repoID int64,
	number int,
) error {
	issue, err := reader.GetIssue(ctx, platformRepoRef(repo), number)
	if err != nil {
		return fmt.Errorf("get closed issue #%d: %w", number, err)
	}
	normalized := platform.DBIssue(repoID, issue)
	issueID, err := s.db.UpsertIssue(ctx, normalized)
	if err != nil {
		return fmt.Errorf("upsert closed issue #%d: %w", number, err)
	}
	if err := s.replaceIssueLabels(ctx, repoID, issueID, normalized.Labels); err != nil {
		return fmt.Errorf("persist labels for closed issue #%d: %w", number, err)
	}
	return nil
}

// --- Detail Drain ---

// drainDetailQueue builds a priority queue of items needing detail
// fetches and processes them within the per-provider/host budget.
func (s *Syncer) drainDetailQueue(
	ctx context.Context,
	eligibleBuckets map[string]bool,
) {
	if len(s.budgets) == 0 {
		return
	}

	items := s.buildDetailQueueItems(ctx)
	if len(items) == 0 {
		return
	}

	queue := BuildQueue(items, time.Now())
	if len(queue) == 0 {
		return
	}

	// Track which hosts are exhausted so we skip quickly.
	exhausted := make(map[string]bool)

	for i := range queue {
		if ctx.Err() != nil {
			return
		}
		qi := &queue[i]
		host := qi.PlatformHost
		if host == "" {
			host = "github.com"
		}
		bucket := rateBucketKeyFor(qi.Platform, host)

		if !eligibleBuckets[bucket] {
			continue
		}
		if exhausted[bucket] {
			continue
		}

		budget := s.budgets[bucket]
		if budget == nil {
			continue
		}

		// Soft admission gate: check if the budget has nominal
		// capacity for this item. The transport layer handles
		// actual per-RoundTrip accounting; this prevents starting
		// work we almost certainly can't afford.
		worstCase := qi.WorstCaseCost()
		if !budget.CanSpend(worstCase) {
			exhausted[bucket] = true
			continue
		}

		repo := RepoRef{
			Platform:     qi.Platform,
			Owner:        qi.RepoOwner,
			Name:         qi.RepoName,
			PlatformHost: qi.PlatformHost,
		}
		if tracked, ok := s.trackedRepoByIdentity(qi.Platform, qi.RepoOwner, qi.RepoName, host); ok {
			repo = tracked
			repo.Owner = qi.RepoOwner
			repo.Name = qi.RepoName
			repo.PlatformHost = host
		}
		repoID, err := s.db.UpsertRepo(ctx, platform.DBRepoIdentity(platformRepoRef(repo)))
		if err != nil {
			slog.Warn("detail drain: upsert repo failed",
				"repo", qi.RepoOwner+"/"+qi.RepoName,
				"err", err,
			)
			continue
		}

		// Compute diff SHAs if clone available.
		cloneFetchOK := false
		if s.clones != nil {
			if cloneErr := s.clones.EnsureClone(
				ctx, host, qi.RepoOwner, qi.RepoName,
				cloneRemoteURL(repo),
			); cloneErr != nil {
				slog.Warn("detail drain: bare clone failed",
					"repo", qi.RepoOwner+"/"+qi.RepoName,
					"err", cloneErr,
				)
			} else {
				cloneFetchOK = true
			}
		}

		if qi.Type == QueueItemPR {
			_, err = s.fetchMRDetail(
				ctx, repo, repoID, qi.Number, cloneFetchOK,
			)
		} else {
			_, err = s.fetchIssueDetail(
				ctx, repo, repoID, qi.Number,
			)
		}

		if err != nil {
			slog.Warn("detail drain: fetch failed",
				"repo", qi.RepoOwner+"/"+qi.RepoName,
				"number", qi.Number,
				"type", qi.Type,
				"err", err,
			)
		}
	}
}

// buildDetailQueueItems queries the DB for open PRs and issues
// that may need a detail fetch, combining with starred/watched
// state to build queue items for scoring.
func (s *Syncer) buildDetailQueueItems(
	ctx context.Context,
) []QueueItem {
	// Build set of tracked repos to filter out stale DB rows
	// from removed repos.
	s.reposMu.Lock()
	trackedRepos := make(map[string]bool, len(s.repos))
	for _, r := range s.repos {
		trackedRepos[detailRepoKey(repoPlatform(r), repoHost(r), r.Owner, r.Name)] = true
	}
	s.reposMu.Unlock()

	// Gather watched MR numbers for matching.
	s.watchMu.Lock()
	watched := make(map[string]bool, len(s.watchedMRs))
	for _, w := range s.watchedMRs {
		watched[watchedMRKey(w)] = true
	}
	s.watchMu.Unlock()

	var items []QueueItem

	// Open PRs.
	prs, err := s.db.ListMergeRequests(
		ctx, db.ListMergeRequestsOpts{State: "open"},
	)
	if err != nil {
		slog.Warn("detail drain: list open PRs failed",
			"err", err,
		)
		return nil
	}
	prCountsByRepoID := make(map[int64]int, len(prs))
	for _, pr := range prs {
		prCountsByRepoID[pr.RepoID]++
	}
	for _, pr := range prs {
		repo, rErr := s.db.GetRepoByID(ctx, pr.RepoID)
		if rErr != nil || repo == nil {
			continue
		}
		repoKey := detailRepoKey(platform.Kind(repo.Platform), repo.PlatformHost, repo.Owner, repo.Name)
		if !trackedRepos[repoKey] {
			continue
		}
		watchKey := detailRepoKey(
			platform.Kind(repo.Platform), repo.PlatformHost,
			repo.Owner, repo.Name,
		) + fmt.Sprintf("#%d", pr.Number)
		ciHadPending := pr.CIHadPending || ciHasPending(pr.CIChecksJSON)
		items = append(items, QueueItem{
			Type:            QueueItemPR,
			Platform:        platform.Kind(repo.Platform),
			RepoOwner:       repo.Owner,
			RepoName:        repo.Name,
			Number:          pr.Number,
			PlatformHost:    repo.PlatformHost,
			UpdatedAt:       pr.UpdatedAt,
			DetailFetchedAt: pr.DetailFetchedAt,
			CIHadPending:    ciHadPending,
			Starred:         pr.Starred,
			Watched:         watched[watchKey],
			IsOpen:          true,
			LargeRepo:       prCountsByRepoID[pr.RepoID] >= largeRepoBulkGraphQLThreshold,
		})
	}

	// Open issues.
	issues, err := s.db.ListIssues(
		ctx, db.ListIssuesOpts{State: "open"},
	)
	if err != nil {
		slog.Warn("detail drain: list open issues failed",
			"err", err,
		)
		return items
	}
	issueCountsByRepoID := make(map[int64]int, len(issues))
	for _, issue := range issues {
		issueCountsByRepoID[issue.RepoID]++
	}
	for _, issue := range issues {
		repo, rErr := s.db.GetRepoByID(ctx, issue.RepoID)
		if rErr != nil || repo == nil {
			continue
		}
		repoKey := detailRepoKey(platform.Kind(repo.Platform), repo.PlatformHost, repo.Owner, repo.Name)
		if !trackedRepos[repoKey] {
			continue
		}
		items = append(items, QueueItem{
			Type:            QueueItemIssue,
			Platform:        platform.Kind(repo.Platform),
			RepoOwner:       repo.Owner,
			RepoName:        repo.Name,
			Number:          issue.Number,
			PlatformHost:    repo.PlatformHost,
			UpdatedAt:       issue.UpdatedAt,
			DetailFetchedAt: issue.DetailFetchedAt,
			Starred:         issue.Starred,
			IsOpen:          true,
			LargeRepo:       issueCountsByRepoID[issue.RepoID] >= largeRepoBulkGraphQLThreshold,
		})
	}

	return items
}

// --- Backfill Discovery ---

// backfillMaxPagesPerRepo limits how many closed-item pages
// we fetch per repo per cycle to stay gentle on the API.
const backfillMaxPagesPerRepo = 2

// runBackfillDiscovery fetches closed PRs/issues for repos in
// the given provider/host bucket, advancing backfill cursors stored in the DB.
// Only runs when >50% of the bucket's budget remains.
func (s *Syncer) runBackfillDiscovery(
	ctx context.Context,
	bucket string,
	repos []RepoRef,
) {
	budget := s.budgets[bucket]
	if budget == nil {
		return
	}
	if budget.Remaining() < budget.Limit()/2 {
		return
	}

	for _, repo := range repos {
		if ctx.Err() != nil {
			return
		}
		if repoRateBucketKey(repo) != bucket {
			continue
		}

		repoRow, err := s.db.GetRepoByIdentity(
			ctx, platform.DBRepoIdentity(platformRepoRef(repo)),
		)
		if err != nil || repoRow == nil {
			continue
		}

		s.backfillRepo(ctx, repo, repoRow, budget)
	}
}

func (s *Syncer) backfillRepo(
	ctx context.Context,
	repo RepoRef,
	repoRow *db.Repo,
	budget *SyncBudget,
) {
	client, ok := s.optionalGitHubClientFor(repo)
	if !ok {
		return
	}
	repoID := repoRow.ID
	now := time.Now()

	// PR backfill.
	prPage := repoRow.BackfillPRPage
	prComplete := repoRow.BackfillPRComplete
	prCompletedAt := repoRow.BackfillPRCompletedAt

	if prComplete && prCompletedAt != nil &&
		now.Sub(*prCompletedAt) < 24*time.Hour {
		// Skip -- completed recently.
	} else {
		if prComplete {
			// Reset for re-scan.
			prPage = 0
			prComplete = false
			prCompletedAt = nil
		}
		for range backfillMaxPagesPerRepo {
			if ctx.Err() != nil || !budget.CanSpend(1) {
				break
			}
			prPage++
			pageFailed := false
			prs, hasMore, err := client.ListPullRequestsPage(
				ctx, repo.Owner, repo.Name,
				"closed", prPage,
			)
			if err != nil {
				slog.Warn("backfill PRs failed",
					"repo", repo.Owner+"/"+repo.Name,
					"page", prPage, "err", err,
				)
				break
			}
			for _, ghPR := range prs {
				normalized, err := NormalizePR(repoID, ghPR)
				if err != nil {
					slog.Warn("backfill normalize PR failed",
						"repo", repo.Owner+"/"+repo.Name,
						"number", ghPR.GetNumber(),
						"err", err,
					)
					pageFailed = true
					break
				}
				if mrID, uErr := s.db.UpsertMergeRequest(
					ctx, normalized,
				); uErr != nil {
					slog.Warn("backfill upsert PR failed",
						"repo", repo.Owner+"/"+repo.Name,
						"number", ghPR.GetNumber(),
						"err", uErr,
					)
					pageFailed = true
					break
				} else if err := s.replaceMergeRequestLabels(ctx, repoID, mrID, normalized.Labels); err != nil {
					slog.Warn("backfill replace PR labels failed",
						"repo", repo.Owner+"/"+repo.Name,
						"number", ghPR.GetNumber(),
						"err", err,
					)
					pageFailed = true
					break
				}
			}
			if pageFailed {
				prPage--
				break
			}
			if !hasMore {
				prComplete = true
				t := now
				prCompletedAt = &t
				break
			}
		}
	}

	// Issue backfill.
	issuePage := repoRow.BackfillIssuePage
	issueComplete := repoRow.BackfillIssueComplete
	issueCompletedAt := repoRow.BackfillIssueCompletedAt

	if issueComplete && issueCompletedAt != nil &&
		now.Sub(*issueCompletedAt) < 24*time.Hour {
		// Skip.
	} else {
		if issueComplete {
			issuePage = 0
			issueComplete = false
			issueCompletedAt = nil
		}
		for range backfillMaxPagesPerRepo {
			if ctx.Err() != nil || !budget.CanSpend(1) {
				break
			}
			issuePage++
			pageFailed := false
			issues, hasMore, err := client.ListIssuesPage(
				ctx, repo.Owner, repo.Name,
				"closed", issuePage,
			)
			if err != nil {
				slog.Warn("backfill issues failed",
					"repo", repo.Owner+"/"+repo.Name,
					"page", issuePage, "err", err,
				)
				break
			}
			for _, ghIssue := range issues {
				normalized, err := NormalizeIssue(repoID, ghIssue)
				if err != nil {
					slog.Warn("backfill normalize issue failed",
						"repo", repo.Owner+"/"+repo.Name,
						"number", ghIssue.GetNumber(),
						"err", err,
					)
					pageFailed = true
					break
				}
				if issueID, uErr := s.db.UpsertIssue(
					ctx, normalized,
				); uErr != nil {
					slog.Warn("backfill upsert issue failed",
						"repo", repo.Owner+"/"+repo.Name,
						"number", ghIssue.GetNumber(),
						"err", uErr,
					)
					pageFailed = true
					break
				} else if err := s.replaceIssueLabels(ctx, repoID, issueID, normalized.Labels); err != nil {
					slog.Warn("backfill replace issue labels failed",
						"repo", repo.Owner+"/"+repo.Name,
						"number", ghIssue.GetNumber(),
						"err", err,
					)
					pageFailed = true
					break
				}
			}
			if pageFailed {
				issuePage--
				break
			}
			if !hasMore {
				issueComplete = true
				t := now
				issueCompletedAt = &t
				break
			}
		}
	}

	// Persist cursor state.
	if err := s.db.UpdateBackfillCursor(
		ctx, repoID,
		prPage, prComplete, prCompletedAt,
		issuePage, issueComplete, issueCompletedAt,
	); err != nil {
		slog.Warn("update backfill cursor failed",
			"repo", repo.Owner+"/"+repo.Name, "err", err,
		)
	}
}

// IsTrackedRepo checks whether the given repo is in the configured list.
func (s *Syncer) IsTrackedRepo(owner, name string) bool {
	s.reposMu.Lock()
	repos := s.repos
	s.reposMu.Unlock()
	for _, r := range repos {
		if strings.EqualFold(r.Owner, owner) &&
			strings.EqualFold(r.Name, name) {
			return true
		}
	}
	return false
}

// TrackedRepos returns a snapshot of the tracked repositories.
func (s *Syncer) TrackedRepos() []RepoRef {
	s.reposMu.Lock()
	defer s.reposMu.Unlock()

	return slices.Clone(s.repos)
}

// isTrackedRepoOnHost checks whether the given repo on a specific host
// is in the configured list. Used by the watched-MR path where the
// host is known and must match exactly.
func (s *Syncer) isTrackedRepoOnHost(owner, name, host string) bool {
	_, ok := s.trackedRepoOnHost(owner, name, host)
	return ok
}

// IsTrackedRepoOnHost checks whether the given repo on a specific host
// is in the configured list.
func (s *Syncer) IsTrackedRepoOnHost(owner, name, host string) bool {
	return s.isTrackedRepoOnHost(owner, name, host)
}

// SyncMR fetches fresh data for a single MR from GitHub and updates the DB.
// Unlike the periodic sync, this always does a full fetch (details, timeline, CI).
// Returns an error if the repo is not in the configured repo list.
func (s *Syncer) SyncMR(ctx context.Context, owner, name string, number int) error {
	return s.syncMRWithHost(ctx, owner, name, number, "")
}

// SyncMROnProvider fetches fresh data for a single MR from a specific
// configured provider host.
func (s *Syncer) SyncMROnProvider(
	ctx context.Context,
	kind platform.Kind,
	host, owner, name string,
	number int,
) error {
	repo, ok := s.trackedRepoByIdentity(kind, owner, name, host)
	if !ok {
		host = repoHost(RepoRef{Platform: kind, PlatformHost: host})
		return fmt.Errorf(
			"repo %s/%s on %s/%s is not tracked",
			owner, name, kind, host,
		)
	}
	repo.Owner = owner
	repo.Name = name
	repo.PlatformHost = repoHost(repo)
	return s.syncMRForRepo(ctx, repo, number)
}

// syncMRWithHost is the internal implementation of SyncMR.
// When hostHint is non-empty it is used instead of resolving via
// s.hostFor, avoiding ambiguity when the same owner/name exists on
// multiple hosts.
func (s *Syncer) syncMRWithHost(
	ctx context.Context,
	owner, name string,
	number int,
	hostHint string,
) error {
	var (
		repo RepoRef
		ok   bool
		err  error
	)
	if hostHint == "" {
		repo, ok, err = s.trackedRepo(owner, name)
	} else {
		repo, ok, err = s.trackedRepoOnHostUnique(owner, name, hostHint)
	}
	if err != nil {
		return err
	}
	if !ok {
		host := hostHint
		if host == "" {
			host = s.hostFor(owner, name)
		}
		return fmt.Errorf(
			"repo %s/%s on %s is not tracked", owner, name, host,
		)
	}
	repo.Owner = owner
	repo.Name = name
	repo.PlatformHost = repoHost(repo)
	return s.syncMRForRepo(ctx, repo, number)
}

func (s *Syncer) syncMRWithWatchedRef(
	ctx context.Context,
	mr WatchedMR,
) error {
	kind := watchedMRPlatform(mr)
	repo, ok := s.trackedRepoByIdentity(
		kind, mr.Owner, mr.Name, mr.PlatformHost,
	)
	if !ok {
		host := repoHost(RepoRef{Platform: kind, PlatformHost: mr.PlatformHost})
		return fmt.Errorf(
			"repo %s/%s on %s/%s is not tracked",
			mr.Owner, mr.Name, kind, host,
		)
	}
	return s.syncMRForRepo(ctx, repo, mr.Number)
}

func (s *Syncer) syncMRForRepo(
	ctx context.Context,
	repo RepoRef,
	number int,
) error {
	owner := repo.Owner
	name := repo.Name
	mrReader, err := s.mergeRequestReaderFor(repo)
	if err != nil {
		return fmt.Errorf("resolve merge request reader for %s/%s: %w", owner, name, err)
	}

	repoID, err := s.db.UpsertRepo(ctx, platform.DBRepoIdentity(platformRepoRef(repo)))
	if err != nil {
		return fmt.Errorf("upsert repo %s/%s: %w", owner, name, err)
	}

	var ghPR *gh.PullRequest
	var platformMR platform.MergeRequest
	if rawReader, ok := mrReader.(interface {
		GetGitHubPullRequest(context.Context, platform.RepoRef, int) (*gh.PullRequest, platform.MergeRequest, error)
	}); ok {
		ghPR, platformMR, err = rawReader.GetGitHubPullRequest(ctx, platformRepoRef(repo), number)
	} else {
		platformMR, err = mrReader.GetMergeRequest(ctx, platformRepoRef(repo), number)
	}
	if err != nil {
		if errors.Is(err, ErrNilPullRequest) {
			return fmt.Errorf(
				"get MR %s/%s#%d: client returned nil pull request",
				owner, name, number,
			)
		}
		return fmt.Errorf("get MR %s/%s#%d: %w", owner, name, number, err)
	}
	normalized := platform.DBMergeRequest(repoID, platformMR)

	// Preserve derived fields that provider detail doesn't populate. CI is
	// refreshed later in this sync path; keeping the previous values here
	// prevents detail reads from briefly seeing "no CI" during refresh.
	existing, err := s.db.GetMergeRequestByRepoIDAndNumber(
		ctx, repoID, number,
	)
	if err != nil {
		return fmt.Errorf("get existing MR #%d: %w", number, err)
	}
	headChanged := existing != nil &&
		existing.PlatformHeadSHA != normalized.PlatformHeadSHA
	if existing != nil {
		normalized.CommentCount = existing.CommentCount
		normalized.ReviewDecision = existing.ReviewDecision
		preserveMergeableStateIfOmitted(normalized, existing)
		// CI is tied to the head SHA. If the head moved we must clear the
		// previous values; otherwise a failed CI refresh would leave stale
		// checks attached to the new commit.
		if !headChanged {
			normalized.CIStatus = existing.CIStatus
			normalized.CIChecksJSON = existing.CIChecksJSON
			normalized.CIHadPending = existing.CIHadPending
		}
		normalized.DetailFetchedAt = existing.DetailFetchedAt
		if normalized.AuthorDisplayName == "" {
			normalized.AuthorDisplayName = existing.AuthorDisplayName
		}
	}

	if normalized.Author != "" && normalized.AuthorDisplayName == "" {
		// Resolve directly instead of using s.resolveDisplayName to
		// preserve existing display names on failure.
		if client, ok := s.optionalGitHubClientFor(repo); ok {
			if displayName, found := s.resolveDisplayName(ctx, client, repo.PlatformHost, normalized.Author); found {
				normalized.AuthorDisplayName = displayName
			}
		}
		if normalized.AuthorDisplayName == "" && existing != nil {
			normalized.AuthorDisplayName = existing.AuthorDisplayName
		}
	}

	mrID, err := s.db.UpsertMergeRequest(ctx, normalized)
	if err != nil {
		return fmt.Errorf("upsert MR #%d: %w", number, err)
	}
	// UpsertMergeRequest preserves ci_had_pending across upserts. Clear
	// it here when the head SHA changed so a stale pending flag from
	// the previous head doesn't survive across the refresh.
	if headChanged {
		if err := s.db.ClearMRCI(ctx, repoID, number); err != nil {
			return fmt.Errorf("clear stale CI for MR #%d: %w", number, err)
		}
	}
	if err := s.replaceMergeRequestLabels(ctx, repoID, mrID, normalized.Labels); err != nil {
		return fmt.Errorf("persist labels for MR #%d: %w", number, err)
	}

	if err := s.db.EnsureKanbanState(ctx, mrID); err != nil {
		return fmt.Errorf("ensure kanban state for MR #%d: %w", number, err)
	}

	var diffErr error
	if ghPR != nil {
		// Run the diff sync, but don't let its failure abort the rest of SyncMR:
		// timeline and CI status are independent and the user still wants them
		// fresh. Capture the error and surface it via DiffSyncError at the end.
		diffErr = s.syncMRDiff(ctx, repo, repoID, number, ghPR, normalized)

		if err := s.refreshTimeline(ctx, repo, repoID, mrID, ghPR); err != nil {
			return fmt.Errorf("refresh timeline for MR #%d: %w", number, err)
		}

		syncMRHeadSHA := ""
		if ghPR.GetHead() != nil {
			syncMRHeadSHA = ghPR.GetHead().GetSHA()
		}
		if err := s.refreshCIStatus(ctx, repo, repoID, number, syncMRHeadSHA); err != nil {
			return err
		}

		// Refresh workflow approval state for the current head SHA.
		// Persisting it here (instead of computing live on every GET)
		// means the DB-only detail path the frontend uses by default
		// can show the Approve Workflows button without a foreground
		// sync round-trip. The result is tied to syncMRHeadSHA so a
		// later read can detect a stale snapshot after force-push.
		s.refreshWorkflowApproval(
			ctx, repo, repoID, number, syncMRHeadSHA, ghPR, normalized,
		)

		// Update ci_had_pending after refreshing CI status.
		fresh, freshErr := s.db.GetMergeRequestByRepoIDAndNumber(ctx, repoID, number)
		if freshErr == nil && fresh != nil {
			pending := ciHasPending(fresh.CIChecksJSON)
			_ = s.updateMRDetailFetchedByRepoID(ctx, repoID, number, pending)
		}
	} else {
		pending := false
		_, pending, err = s.syncProviderMRDetailExtras(
			ctx, mrReader, repo, repoID, mrID, number, normalized.PlatformHeadSHA,
		)
		if err != nil {
			return err
		}
		if err := s.updateMRDetailFetchedByRepoID(ctx, repoID, number, pending); err != nil {
			return fmt.Errorf("mark detail fetched for MR #%d: %w", number, err)
		}
	}

	if s.onMRSynced != nil {
		fresh, err := s.db.GetMergeRequestByRepoIDAndNumber(ctx, repoID, number)
		if err != nil {
			slog.Warn("get MR for onMRSynced hook in SyncMR",
				"repo", owner+"/"+name,
				"number", number,
				"err", err,
			)
		} else {
			s.onMRSynced(owner, name, fresh)
		}
	}

	if diffErr != nil {
		return diffErr
	}
	return nil
}

func preserveMergeableStateIfOmitted(
	normalized *db.MergeRequest,
	existing *db.MergeRequest,
) {
	if normalized == nil || existing == nil {
		return
	}
	if normalized.PlatformHeadSHA == "" ||
		existing.PlatformHeadSHA == "" ||
		normalized.PlatformHeadSHA != existing.PlatformHeadSHA {
		return
	}
	if normalized.PlatformBaseSHA == "" ||
		existing.PlatformBaseSHA == "" ||
		normalized.PlatformBaseSHA != existing.PlatformBaseSHA {
		return
	}
	if normalized.MergeableState == "" ||
		(normalized.MergeableState == "unknown" && existing.MergeableState != "") {
		normalized.MergeableState = existing.MergeableState
	}
}

func preserveCIStateIfOmitted(
	normalized *db.MergeRequest,
	existing *db.MergeRequest,
) bool {
	if normalized == nil || existing == nil {
		return false
	}
	if normalized.PlatformHeadSHA == "" ||
		existing.PlatformHeadSHA == "" ||
		normalized.PlatformHeadSHA != existing.PlatformHeadSHA {
		return false
	}
	ciStatusOmitted := normalized.CIStatus == ""
	ciStatusChanged := !ciStatusOmitted &&
		normalized.CIStatus != existing.CIStatus
	if normalized.CIStatus == "" {
		normalized.CIStatus = existing.CIStatus
	}
	if normalized.CIChecksJSON == "" && !ciStatusChanged {
		normalized.CIChecksJSON = existing.CIChecksJSON
	}
	return ciStatusChanged && normalized.CIChecksJSON == ""
}

// syncMRDiff fetches the bare clone and computes diff SHAs for a single PR.
// Returns nil when there is no clone manager (the caller has already opted
// out of diff support); otherwise returns an error wrapping a
// *DiffSyncError that describes the first failure encountered along the
// clone or diff path. Callers can recover the structured categorization via
// errors.As.
func (s *Syncer) syncMRDiff(
	ctx context.Context, repo RepoRef, repoID int64, number int,
	ghPR *gh.PullRequest, normalized *db.MergeRequest,
) error {
	if s.clones == nil {
		return nil
	}
	host := repoHost(repo)
	if err := s.clones.EnsureClone(ctx, host, repo.Owner, repo.Name, cloneRemoteURL(repo)); err != nil {
		return &DiffSyncError{
			Code: DiffSyncCodeCloneUnavailable,
			Err:  fmt.Errorf("ensure bare clone for #%d: %w", number, err),
		}
	}

	if ghPR.GetMerged() {
		// Merged MRs need special merge-base logic via the pull ref.
		// Force recomputation to repair any previously incorrect SHAs.
		return s.computeMergedMRDiffSHAs(ctx, repo, repoID, number, ghPR.GetMergeCommitSHA(), true)
	}

	if normalized.PlatformHeadSHA == "" || normalized.PlatformBaseSHA == "" {
		return nil
	}
	mb, err := s.clones.MergeBase(ctx, host, repo.Owner, repo.Name, normalized.PlatformBaseSHA, normalized.PlatformHeadSHA)
	if err != nil {
		return &DiffSyncError{
			Code: DiffSyncCodeMergeBaseFailed,
			Err:  fmt.Errorf("merge-base for #%d: %w", number, err),
		}
	}
	if err := s.db.UpdateDiffSHAs(ctx, repoID, number, normalized.PlatformHeadSHA, normalized.PlatformBaseSHA, mb); err != nil {
		return &DiffSyncError{
			Code: DiffSyncCodeInternal,
			Err:  fmt.Errorf("update diff SHAs for #%d: %w", number, err),
		}
	}
	return nil
}

// SyncIssue fetches fresh data for a single issue from GitHub and updates the DB.
// Returns an error if the repo is not in the configured repo list.
func (s *Syncer) SyncIssue(ctx context.Context, owner, name string, number int) error {
	return s.syncIssueWithHost(ctx, owner, name, number, "")
}

// SyncIssueOnHost fetches fresh issue data for a specific tracked host.
func (s *Syncer) SyncIssueOnHost(
	ctx context.Context,
	host, owner, name string,
	number int,
) error {
	return s.syncIssueWithHost(ctx, owner, name, number, host)
}

// SyncIssueOnProvider fetches fresh issue data for a specific configured
// provider host.
func (s *Syncer) SyncIssueOnProvider(
	ctx context.Context,
	kind platform.Kind,
	host, owner, name string,
	number int,
) error {
	repo, ok := s.trackedRepoByIdentity(kind, owner, name, host)
	if !ok {
		host = repoHost(RepoRef{Platform: kind, PlatformHost: host})
		return fmt.Errorf(
			"repo %s/%s on %s/%s is not tracked",
			owner, name, kind, host,
		)
	}
	repo.Owner = owner
	repo.Name = name
	repo.PlatformHost = repoHost(repo)

	repoID, err := s.db.UpsertRepo(ctx, platform.DBRepoIdentity(platformRepoRef(repo)))
	if err != nil {
		return fmt.Errorf("upsert repo %s/%s: %w", owner, name, err)
	}

	if _, err := s.fetchIssueDetail(ctx, repo, repoID, number); err != nil {
		return err
	}
	return nil
}

func (s *Syncer) syncIssueWithHost(
	ctx context.Context,
	owner, name string,
	number int,
	hostHint string,
) error {
	var (
		repo RepoRef
		ok   bool
		err  error
	)
	if hostHint == "" {
		repo, ok, err = s.trackedRepo(owner, name)
	} else {
		repo, ok, err = s.trackedRepoOnHostUnique(owner, name, hostHint)
	}
	if err != nil {
		return err
	}
	if !ok {
		host := hostHint
		if host == "" {
			host = s.hostFor(owner, name)
		}
		return fmt.Errorf(
			"repo %s/%s on %s is not tracked", owner, name, host,
		)
	}
	repo.Owner = owner
	repo.Name = name
	repo.PlatformHost = repoHost(repo)

	repoID, err := s.db.UpsertRepo(ctx, platform.DBRepoIdentity(platformRepoRef(repo)))
	if err != nil {
		return fmt.Errorf("upsert repo %s/%s: %w", owner, name, err)
	}

	if _, err := s.fetchIssueDetail(ctx, repo, repoID, number); err != nil {
		return err
	}
	return nil
}

// SyncItemByNumber fetches an item by number from GitHub, determines
// whether it is a PR or issue, syncs it into the DB, and returns the
// item type ("pr" or "issue").
// Returns an error if the repo is not in the configured repo list.
func (s *Syncer) SyncItemByNumber(
	ctx context.Context, owner, name string, number int,
) (string, error) {
	repo, ok, err := s.trackedRepo(owner, name)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("repo %s/%s is not tracked", owner, name)
	}
	repo.Owner = owner
	repo.Name = name
	repo.PlatformHost = repoHost(repo)

	if repoPlatform(repo) != platform.KindGitHub {
		return "", fmt.Errorf(
			"sync item by number for %s/%s on %s/%s requires an item type",
			owner, name, repoPlatform(repo), repo.PlatformHost,
		)
	}

	// GitHub's Issues API returns both issues and PRs. If the
	// response has PullRequestLinks, it's a PR.
	client, err := s.clientFor(repo)
	if err != nil {
		return "", fmt.Errorf("resolve client for %s/%s: %w", owner, name, err)
	}
	ghIssue, err := client.GetIssue(ctx, owner, name, number)
	if err != nil {
		return "", fmt.Errorf(
			"get item %s/%s#%d: %w", owner, name, number, err,
		)
	}
	if ghIssue == nil {
		return "", fmt.Errorf(
			"get item %s/%s#%d: client returned nil issue", owner, name, number,
		)
	}

	if ghIssue.PullRequestLinks != nil {
		if err := s.SyncMR(ctx, owner, name, number); err != nil {
			// A DiffSyncError means the PR row, timeline, and CI status
			// were upserted successfully and only the diff computation
			// failed. The item type is known, so resolution can still
			// succeed; surface the error so callers that care about diff
			// freshness can react, but report itemType so callers that
			// just need to route the user (e.g. /items/{n}/resolve) can
			// proceed.
			var diffErr *DiffSyncError
			if errors.As(err, &diffErr) {
				return "pr", err
			}
			return "", fmt.Errorf(
				"sync MR %s/%s#%d: %w", owner, name, number, err,
			)
		}
		return "pr", nil
	}

	if err := s.SyncIssue(ctx, owner, name, number); err != nil {
		return "", fmt.Errorf(
			"sync issue %s/%s#%d: %w", owner, name, number, err,
		)
	}
	return "issue", nil
}

// fetchAndUpdateClosed retrieves the final state of a now-closed PR from GitHub.
func (s *Syncer) fetchAndUpdateClosed(ctx context.Context, repo RepoRef, repoID int64, number int, cloneFetchOK bool) error {
	client, err := s.clientFor(repo)
	if err != nil {
		return fmt.Errorf("resolve client for %s/%s: %w", repo.Owner, repo.Name, err)
	}
	ghPR, err := client.GetPullRequest(ctx, repo.Owner, repo.Name, number)
	if err != nil {
		return fmt.Errorf("get closed PR #%d: %w", number, err)
	}
	if ghPR == nil {
		return fmt.Errorf(
			"get closed PR #%d: client returned nil pull request",
			number,
		)
	}

	state := ghPR.GetState()
	if pullRequestWasMerged(ghPR) {
		state = "merged"
	}

	var mergedAt, closedAt *time.Time
	if ghPR.MergedAt != nil {
		t := ghPR.MergedAt.Time
		mergedAt = &t
	}
	if ghPR.ClosedAt != nil {
		t := ghPR.ClosedAt.Time
		closedAt = &t
	}

	if err := s.db.UpdateClosedMRState(
		ctx, repoID, number, state,
		ghPR.GetUpdatedAt().Time,
		mergedAt, closedAt,
		ghPR.GetHead().GetSHA(), ghPR.GetBase().GetSHA(),
	); err != nil {
		return fmt.Errorf("update closed MR #%d: %w", number, err)
	}

	mr, err := s.db.GetMergeRequestByRepoIDAndNumber(ctx, repoID, number)
	if err != nil {
		return fmt.Errorf("get closed MR #%d for labels: %w", number, err)
	}
	if mr != nil {
		normalized, err := NormalizePR(repoID, ghPR)
		if err != nil {
			return fmt.Errorf("normalize closed PR #%d: %w", number, err)
		}
		if err := s.replaceMergeRequestLabels(ctx, repoID, mr.ID, normalized.Labels); err != nil {
			return fmt.Errorf("persist labels for closed MR #%d: %w", number, err)
		}
	}

	// Compute diff SHAs so the diff endpoint works.
	// For closed-but-not-merged PRs, use GitHub's head/base SHAs directly.
	// For merged PRs, use merge-base(merge_commit^1, refs/pull/<number>/head)
	// to find the fork point. This works for all merge strategies because ^1
	// is always a pre-merge commit on the base branch lineage, and the pull
	// ref always points to the original PR head. We only do this when no diff
	// SHAs exist yet; PRs synced while open already have valid diff SHAs.
	closedHost := repo.PlatformHost
	if closedHost == "" {
		closedHost = "github.com"
	}
	if s.clones != nil && cloneFetchOK {
		headSHA := ghPR.GetHead().GetSHA()
		baseSHA := ghPR.GetBase().GetSHA()

		if pullRequestWasMerged(ghPR) {
			if err := s.computeMergedMRDiffSHAs(ctx, repo, repoID, number, ghPR.GetMergeCommitSHA(), false); err != nil {
				slog.Warn("compute merged PR diff SHAs failed",
					"repo", repo.Owner+"/"+repo.Name,
					"number", number, "err", err,
				)
			}
		} else if headSHA != "" && baseSHA != "" {
			mb, err := s.clones.MergeBase(ctx, closedHost, repo.Owner, repo.Name, baseSHA, headSHA)
			if err != nil {
				slog.Warn("merge-base for closed PR failed",
					"repo", repo.Owner+"/"+repo.Name,
					"number", number, "err", err,
				)
			} else {
				if err := s.db.UpdateDiffSHAs(ctx, repoID, number, headSHA, baseSHA, mb); err != nil {
					slog.Warn("update diff SHAs for closed PR failed",
						"repo", repo.Owner+"/"+repo.Name,
						"number", number, "err", err,
					)
				}
			}
		}
	}
	return nil
}

func (s *Syncer) fetchAndUpdateClosedMergeRequest(
	ctx context.Context,
	reader platform.MergeRequestReader,
	repo RepoRef,
	repoID int64,
	number int,
	cloneFetchOK bool,
) error {
	if _, ok := reader.(interface {
		GetGitHubPullRequest(context.Context, platform.RepoRef, int) (*gh.PullRequest, platform.MergeRequest, error)
	}); ok {
		return s.fetchAndUpdateClosed(ctx, repo, repoID, number, cloneFetchOK)
	}

	mr, err := reader.GetMergeRequest(ctx, platformRepoRef(repo), number)
	if err != nil {
		return fmt.Errorf("get closed MR #%d: %w", number, err)
	}
	normalized := platform.DBMergeRequest(repoID, mr)
	mrID, err := s.db.UpsertMergeRequest(ctx, normalized)
	if err != nil {
		return fmt.Errorf("upsert closed MR #%d: %w", number, err)
	}
	if err := s.replaceMergeRequestLabels(ctx, repoID, mrID, normalized.Labels); err != nil {
		return fmt.Errorf("persist labels for closed MR #%d: %w", number, err)
	}
	return nil
}

// computeMergedMRDiffSHAs computes diff SHAs for a merged PR.
// Uses merge-base(merge_commit^1, refs/pull/<number>/head) which works for all
// GitHub merge strategies:
//   - Merge commit: ^1 is the pre-merge base tip
//   - Squash: ^1 is the pre-squash base tip
//   - Rebase: ^1 is the previous rebased commit
//
// In all cases, merge-base with the original PR head (from the pull ref)
// correctly identifies the fork point.
//
// When force is false, skips PRs that already have diff SHAs (periodic sync).
// When force is true, always recomputes (on-demand SyncMR).
//
// Returns a *DiffSyncError (wrapped as an error) describing the failure when
// any git or DB operation fails. A nil return covers both success and the
// no-op skip cases (empty merge SHA, existing valid diff SHAs without force).
func (s *Syncer) computeMergedMRDiffSHAs(
	ctx context.Context, repo RepoRef, repoID int64, number int, mergeCommitSHA string,
	force bool,
) error {
	if mergeCommitSHA == "" {
		return nil
	}

	if !force {
		existing, err := s.db.GetDiffSHAsByRepoID(ctx, repoID, number)
		if err != nil {
			return &DiffSyncError{
				Code: DiffSyncCodeInternal,
				Err:  fmt.Errorf("get diff SHAs for merged PR #%d: %w", number, err),
			}
		}
		if existing == nil || existing.DiffHeadSHA != "" {
			return nil // already has diff SHAs or PR not found
		}
	}

	// Resolve the PR head from the pull ref. GitHub keeps these refs
	// indefinitely, pointing to the original PR head commit regardless
	// of merge strategy.
	mergedHost := repo.PlatformHost
	if mergedHost == "" {
		mergedHost = "github.com"
	}
	pullRef := fmt.Sprintf("refs/pull/%d/head", number)
	prHead, err := s.clones.RevParse(ctx, mergedHost, repo.Owner, repo.Name, pullRef)
	if err != nil {
		return &DiffSyncError{
			Code: DiffSyncCodeCommitUnreachable,
			Err:  fmt.Errorf("rev-parse %s for merged PR #%d: %w", pullRef, number, err),
		}
	}

	// Use the merge commit's first parent as the base for merge-base.
	// This avoids the post-merge ancestor problem where prHead is reachable
	// from the current base branch tip (making merge-base return prHead).
	preMergeBase, err := s.clones.RevParse(ctx, mergedHost, repo.Owner, repo.Name, mergeCommitSHA+"^1")
	if err != nil {
		return &DiffSyncError{
			Code: DiffSyncCodeCommitUnreachable,
			Err:  fmt.Errorf("rev-parse %s^1 for merged PR #%d: %w", mergeCommitSHA, number, err),
		}
	}

	mb, err := s.clones.MergeBase(ctx, mergedHost, repo.Owner, repo.Name, preMergeBase, prHead)
	if err != nil {
		return &DiffSyncError{
			Code: DiffSyncCodeMergeBaseFailed,
			Err:  fmt.Errorf("merge-base for merged PR #%d: %w", number, err),
		}
	}

	if prHead == "" || mb == "" {
		return nil
	}

	if err := s.db.UpdateDiffSHAs(ctx, repoID, number, prHead, mb, mb); err != nil {
		return &DiffSyncError{
			Code: DiffSyncCodeInternal,
			Err:  fmt.Errorf("update diff SHAs for merged PR #%d: %w", number, err),
		}
	}
	return nil
}
