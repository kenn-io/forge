package middleman

import (
	"context"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go.kenn.io/middleman/internal/config"
	"go.kenn.io/middleman/internal/db"
	"go.kenn.io/middleman/internal/gitclone"
	ghclient "go.kenn.io/middleman/internal/github"
	"go.kenn.io/middleman/internal/server"
	"go.kenn.io/middleman/internal/stacks"
	"go.kenn.io/middleman/internal/tokenauth"
	"go.kenn.io/middleman/internal/web"
)

// Type aliases so external callers don't need to import
// internal packages.
type (
	EmbedConfig  = server.EmbedConfig
	ThemeConfig  = server.ThemeConfig
	UIConfig     = server.UIConfig
	RepoRef      = server.RepoRef
	WorktreeLink = db.WorktreeLink
	WatchedMR    = ghclient.WatchedMR

	MergeRequestState = db.MergeRequestState
)

const (
	MergeRequestStateOpen   = db.MergeRequestStateOpen
	MergeRequestStateClosed = db.MergeRequestStateClosed
	MergeRequestStateMerged = db.MergeRequestStateMerged
)

// Repo identifies a GitHub repository to monitor.
type Repo struct {
	Owner        string
	Name         string
	PlatformHost string // e.g. "github.com" or GHE hostname
}

// uniqueHosts returns the deduplicated list of platform hosts
// from the given repos. Repos without PlatformHost default to
// "github.com". If repos is empty, returns ["github.com"].
func uniqueHosts(repos []Repo) []string {
	seen := make(map[string]bool)
	var hosts []string
	for _, r := range repos {
		h := r.PlatformHost
		if h == "" {
			h = "github.com"
		}
		if !seen[h] {
			seen[h] = true
			hosts = append(hosts, h)
		}
	}
	if len(hosts) == 0 {
		return []string{"github.com"}
	}
	return hosts
}

// resolveHostTokenSources builds a map of host -> runtime token source. When
// resolveToken is set the source calls it per token request, allowing embedded
// callers to rotate credentials without rebuilding the instance.
func resolveHostTokenSources(
	ctx context.Context,
	hosts []string,
	staticToken string,
	resolveToken func(ctx context.Context, host string) (string, error),
) (map[string]tokenauth.Source, error) {
	sources := make(map[string]tokenauth.Source, len(hosts))
	for _, host := range hosts {
		source := embedTokenSource{
			host:         host,
			staticToken:  staticToken,
			resolveToken: resolveToken,
		}
		if _, err := source.Token(ctx); err != nil {
			return nil, err
		}
		sources[host] = source
	}
	return sources, nil
}

type embedTokenSource struct {
	host         string
	staticToken  string
	resolveToken func(ctx context.Context, host string) (string, error)
}

func (s embedTokenSource) Token(ctx context.Context) (string, error) {
	token := s.staticToken
	if s.resolveToken != nil {
		resolved, err := s.resolveToken(ctx, s.host)
		if err != nil {
			return "", fmt.Errorf(
				"middleman: resolve token for %s: %w",
				s.host, err,
			)
		}
		token = resolved
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return "", fmt.Errorf(
			"%w for github host %s via %s",
			tokenauth.ErrMissingToken, s.host, s.Descriptor().SafeString(),
		)
	}
	tokenauth.RegisterKnownSecret(token)
	return token, nil
}

func (s embedTokenSource) Invalidate() {}

func (s embedTokenSource) Descriptor() tokenauth.Descriptor {
	kind := tokenauth.SourceKind("embedded_static")
	if s.resolveToken != nil {
		kind = tokenauth.SourceKind("embedded_resolver")
	}
	return tokenauth.Descriptor{
		Key: tokenauth.Key{Platform: "github", Host: s.host},
		Candidates: []tokenauth.Candidate{{
			Kind: kind,
			Host: s.host,
		}},
	}
}

// EmbedHooks provides lifecycle callbacks for embedded consumers.
//
// Concurrency:
//   - OnMRSynced fires after each merge request is synced. Sync
//     processes repos in parallel, so this callback may be
//     invoked from multiple goroutines concurrently (one per
//     in-flight repo sync). Implementations must be safe for
//     concurrent use and must not block indefinitely or they
//     will stall sync progress.
//   - OnSyncCompleted fires once at the end of each sync pass on
//     the goroutine that drives the sync, so it is not invoked
//     concurrently with itself.
//
// Hooks should be set before calling StartSync. Mutating the
// hook fields while a sync is in flight is not safe.
type EmbedHooks struct {
	OnMRSynced      func(MergeRequestSummary)
	OnSyncCompleted func(results []RepoSyncResult)
}

// MergeRequestSummary is a lightweight snapshot of a synced MR,
// passed to EmbedHooks callbacks.
type MergeRequestSummary struct {
	MergeRequestID int64
	RepoOwner      string
	RepoName       string
	Number         int
	State          MergeRequestState
	Title          string
	IsDraft        bool
	CIStatus       string
	ReviewDecision string
	PlatformHost   string
	CIChecksJSON   string
	UpdatedAt      time.Time
}

// RepoSyncResult holds the outcome of syncing a single repo.
type RepoSyncResult struct {
	Owner        string
	Name         string
	PlatformHost string
	Error        string // empty on success
}

// Activity configures the activity view defaults.
type Activity struct {
	ViewMode   string
	TimeRange  string
	HideClosed bool
	HideBots   bool
}

// Options configures a middleman Instance for embedding.
type Options struct {
	// Token is a static GitHub token. Used when ResolveToken
	// is nil.
	Token string
	// ResolveToken returns a GitHub token for the given platform
	// host (e.g. "github.com"). Preferred over Token for
	// embedded use cases that need per-host auth or runtime token
	// rotation. It is called during startup validation and again
	// when provider or clone operations need a token.
	ResolveToken func(ctx context.Context, host string) (string, error)
	// DataDir is the directory for middleman state. Required if
	// DBPath is not set.
	DataDir string
	// DBPath overrides the DataDir-derived database path. When
	// set, the host owns the SQLite file and DataDir may be
	// omitted.
	DBPath        string
	BasePath      string
	SyncInterval  time.Duration
	WatchInterval time.Duration
	Repos         []Repo
	Activity      Activity
	Assets        fs.FS
	EmbedConfig   *server.EmbedConfig
	EmbedHooks    *EmbedHooks
}

// Instance holds a running middleman server and its resources.
type Instance struct {
	db         *db.DB
	server     *server.Server
	syncer     *ghclient.Syncer
	cancelSync context.CancelFunc
	// cancelHook aborts any in-flight stack-detection pass triggered by
	// SetOnSyncCompleted. Separate from cancelSync so ad-hoc syncs via
	// TriggerRun before StartSync still run detection, and so StopSync
	// can cancel hook work regardless of how callers parent their ctx.
	// Read/written only from inside cancelHookOnce.Do in StopSync.
	cancelHook context.CancelFunc
	// cancelHookOnce serializes the cancelHook check/call/reset
	// sequence in StopSync so concurrent callers cannot race into a
	// nil-dereference TOCTOU window. It intentionally does NOT cover
	// i.syncer.Stop(): Syncer.Stop is designed to be called multiple
	// times and waits up to stopGracePeriod on every call, so a
	// subsequent Close after a StopSync that hit the grace-period
	// timeout can still re-wait for lingering sync work.
	cancelHookOnce sync.Once
	stopMu         sync.Mutex
	closed         bool
}

type graphQLRateTrackerSetter interface {
	SetGraphQLRateTracker(*ghclient.RateTracker)
}

// New creates a middleman Instance from the given options. It
// wraps NewWithContext with context.Background(); callers that
// need to bound or cancel a slow ResolveToken callback should
// use NewWithContext directly.
//
// Either Token or ResolveToken must yield a non-empty token.
// Either DBPath or DataDir must be provided.
func New(opts Options) (*Instance, error) {
	return NewWithContext(context.Background(), opts)
}

// NewWithContext creates a middleman Instance from the given
// options. The provided ctx is used for ResolveToken lookups
// during initialization so hosts that depend on a slow secret
// store can bound it. A nil ctx is treated as
// context.Background() to preserve the panic-free contract of
// the older New(opts) entry point. ctx is not retained past
// New; runtime sync cancellation is controlled by StartSync /
// Close.
func NewWithContext(
	ctx context.Context, opts Options,
) (*Instance, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	// DB path: prefer explicit DBPath over DataDir-derived.
	dbPath := opts.DBPath
	if dbPath == "" {
		if opts.DataDir == "" {
			return nil, fmt.Errorf(
				"middleman: either DBPath or DataDir " +
					"is required",
			)
		}
		if err := os.MkdirAll(opts.DataDir, 0o700); err != nil {
			return nil, fmt.Errorf(
				"middleman: create data directory %s: %w",
				opts.DataDir, err,
			)
		}
		dbPath = filepath.Join(opts.DataDir, "middleman.db")
	}

	// Collect unique hosts from repos.
	hosts := uniqueHosts(opts.Repos)

	// Multi-host requires ResolveToken.
	if len(hosts) > 1 && opts.ResolveToken == nil {
		return nil, fmt.Errorf(
			"middleman: multi-host config requires " +
				"ResolveToken (repos span multiple hosts)",
		)
	}

	// Build per-host token sources.
	hostSources, err := resolveHostTokenSources(
		ctx, hosts, opts.Token, opts.ResolveToken,
	)
	if err != nil {
		return nil, err
	}

	cfg := buildConfig(opts)

	frontend, err := resolveAssets(opts)
	if err != nil {
		return nil, fmt.Errorf(
			"middleman: resolving assets: %w", err,
		)
	}

	database, err := db.Open(dbPath)
	if err != nil {
		return nil, fmt.Errorf(
			"middleman: opening database: %w", err,
		)
	}

	// Build per-host budgets, clients, and rate trackers.
	budgetPerHour := cfg.BudgetPerHour()
	budgets := make(
		map[string]*ghclient.SyncBudget, len(hosts),
	)
	clients := make(map[string]ghclient.Client, len(hosts))
	rateTrackers := make(
		map[string]*ghclient.RateTracker, len(hosts),
	)
	for _, host := range hosts {
		rateKey := ghclient.RateBucketKey("github", host)
		rt := ghclient.NewPlatformRateTracker(database, "github", host, "rest")
		rateTrackers[rateKey] = rt
		if budgetPerHour > 0 {
			budgets[rateKey] = ghclient.NewSyncBudget(
				budgetPerHour,
			)
		}
		gh, cErr := ghclient.NewClient(
			hostSources[host], host, rt, budgets[rateKey],
		)
		if cErr != nil {
			database.Close()
			return nil, fmt.Errorf(
				"middleman: creating GitHub client for %s: %w",
				host, cErr,
			)
		}
		clients[host] = gh
	}

	// Build repo refs with resolved PlatformHost.
	var refs []ghclient.RepoRef
	for _, r := range opts.Repos {
		h := r.PlatformHost
		if h == "" {
			h = "github.com"
		}
		refs = append(refs, ghclient.RepoRef{
			Owner:        r.Owner,
			Name:         r.Name,
			PlatformHost: h,
		})
	}

	// Clone manager (needs DataDir for clone storage).
	var cloneMgr *gitclone.Manager
	if opts.DataDir != "" {
		cloneDir := filepath.Join(opts.DataDir, "clones")
		cloneMgr = gitclone.New(cloneDir, hostSources)
	}

	syncer := ghclient.NewSyncer(
		clients, database, cloneMgr, refs,
		cfg.SyncDuration(), rateTrackers, budgets,
	)

	// Wire GraphQL fetchers for bulk PR sync.
	fetchers := make(
		map[string]*ghclient.GraphQLFetcher, len(hosts),
	)
	for _, host := range hosts {
		rateKey := ghclient.RateBucketKey("github", host)
		gqlRT := ghclient.NewPlatformRateTracker(
			database, "github", host, "graphql",
		)
		if setter, ok := clients[host].(graphQLRateTrackerSetter); ok {
			setter.SetGraphQLRateTracker(gqlRT)
		}
		fetchers[host] = ghclient.NewGraphQLFetcher(
			hostSources[host], host, gqlRT, budgets[rateKey],
		)
	}
	syncer.SetFetchers(fetchers)

	if opts.WatchInterval > 0 {
		syncer.SetWatchInterval(opts.WatchInterval)
	}

	if opts.EmbedHooks != nil {
		if opts.EmbedHooks.OnMRSynced != nil {
			cb := opts.EmbedHooks.OnMRSynced
			syncer.SetOnMRSynced(
				func(owner, name string, mr *db.MergeRequest) {
					host := "github.com"
					for _, r := range opts.Repos {
						if r.Owner == owner && r.Name == name && r.PlatformHost != "" {
							host = r.PlatformHost
							break
						}
					}
					cb(MergeRequestSummary{
						MergeRequestID: mr.ID,
						RepoOwner:      owner,
						RepoName:       name,
						Number:         mr.Number,
						State:          mr.State,
						Title:          mr.Title,
						IsDraft:        mr.IsDraft,
						CIStatus:       mr.CIStatus,
						ReviewDecision: mr.ReviewDecision,
						PlatformHost:   host,
						CIChecksJSON:   mr.CIChecksJSON,
						UpdatedAt:      mr.UpdatedAt,
					})
				},
			)
		}
	}

	// Adapter for embed hook if present.
	var embedNext func([]ghclient.RepoSyncResult)
	if opts.EmbedHooks != nil && opts.EmbedHooks.OnSyncCompleted != nil {
		cb := opts.EmbedHooks.OnSyncCompleted
		embedNext = func(results []ghclient.RepoSyncResult) {
			out := make([]RepoSyncResult, len(results))
			for i, r := range results {
				out[i] = RepoSyncResult{
					Owner:        r.Owner,
					Name:         r.Name,
					PlatformHost: r.PlatformHost,
					Error:        r.Error,
				}
			}
			cb(out)
		}
	}
	// Install the stacks hook eagerly so ad-hoc syncs triggered
	// before StartSync still run detection. The hook context is
	// canceled by StopSync, which is a terminal operation.
	hookCtx, cancelHook := context.WithCancel(context.Background())
	syncer.SetOnSyncCompleted(
		stacks.SyncCompletedHook(hookCtx, database, embedNext),
	)

	srv := server.New(
		database, syncer, frontend,
		cfg.BasePath, cfg,
		server.ServerOptions{
			EmbedConfig: opts.EmbedConfig,
			Clones:      cloneMgr,
		},
	)

	return &Instance{
		db:         database,
		server:     srv,
		syncer:     syncer,
		cancelHook: cancelHook,
	}, nil
}

// Handler returns the HTTP handler for this instance.
func (i *Instance) Handler() http.Handler {
	return i.server
}

// StartSync begins periodic GitHub sync in the background.
// The context is used for cancellation during Close.
//
// StartSync must be called at most once per Instance. Once
// StopSync (or Close) has stopped the syncer, the underlying
// Syncer cannot be restarted — a subsequent StartSync call is a
// silent no-op. Construct a new Instance if sync must run again.
func (i *Instance) StartSync(ctx context.Context) {
	ctx, i.cancelSync = context.WithCancel(ctx)
	i.syncer.Start(ctx)
}

// StopSync stops the periodic GitHub sync. This operation is
// terminal: the underlying Syncer permanently refuses further
// Start or TriggerRun calls after Stop, so callers that need to
// resume sync must create a new Instance.
//
// Safe to call concurrently. The cancelHook check/call/reset is
// protected by cancelHookOnce so only the first caller cancels
// the stack-detection context. i.syncer.Stop() runs on every
// call by design: Syncer.Stop waits up to stopGracePeriod on
// each call, so a Close() following a StopSync() that hit the
// grace-period timeout can still re-wait for lingering work
// rather than closing the database out from under it.
func (i *Instance) StopSync() {
	// Cancel any in-flight stack-detection pass before stopping the
	// syncer so the hook does not continue after StopSync returns.
	i.cancelHookOnce.Do(func() {
		if i.cancelHook != nil {
			i.cancelHook()
			i.cancelHook = nil
		}
	})
	i.syncer.Stop()
}

// Close stops sync and closes the database. Safe to call
// multiple times.
func (i *Instance) Close() error {
	i.stopMu.Lock()
	defer i.stopMu.Unlock()

	if i.closed {
		return nil
	}
	i.closed = true

	if i.cancelSync != nil {
		i.cancelSync()
	}
	i.StopSync()
	return i.db.Close()
}

// PurgeOtherHosts deletes all data for platform hosts other
// than keepHost. Uses context.Background(); callers that need
// to bound a long-running purge should call
// PurgeOtherHostsWithContext instead.
func (inst *Instance) PurgeOtherHosts(keepHost string) error {
	return inst.PurgeOtherHostsWithContext(
		context.Background(), keepHost,
	)
}

// PurgeOtherHostsWithContext deletes all data for platform
// hosts other than keepHost under the given ctx. A nil ctx is
// treated as context.Background() so callers that previously
// used the context-less API do not hit database/sql's nil-ctx
// panic.
func (inst *Instance) PurgeOtherHostsWithContext(
	ctx context.Context, keepHost string,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	return inst.db.PurgeOtherHosts(ctx, keepHost)
}

// SetWatchedMRs sets the list of merge requests to sync on a
// fast interval. Replaces any previous watch list.
func (inst *Instance) SetWatchedMRs(mrs []WatchedMR) {
	inst.syncer.SetWatchedMRs(mrs)
}

// SetWorktreeLinks replaces all worktree links atomically.
// Uses context.Background(); callers that need cancellation
// should call SetWorktreeLinksWithContext.
func (inst *Instance) SetWorktreeLinks(
	links []WorktreeLink,
) error {
	return inst.SetWorktreeLinksWithContext(
		context.Background(), links,
	)
}

// SetWorktreeLinksWithContext replaces all worktree links
// atomically under the given ctx. A nil ctx is treated as
// context.Background() so callers that previously used the
// context-less API do not hit database/sql's nil-ctx panic.
func (inst *Instance) SetWorktreeLinksWithContext(
	ctx context.Context, links []WorktreeLink,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	return inst.db.SetWorktreeLinks(ctx, links)
}

// SetActiveWorktree sets the key of the currently focused worktree.
// This is bootstrap-only state: it is injected into the initial HTML
// page load. An already-loaded SPA will not see the change until the
// next full page load. For live updates, mutate
// window.__middleman_config.ui.activeWorktreeKey in the browser and
// call window.__middleman_notify_config_changed().
func (inst *Instance) SetActiveWorktree(key string) {
	inst.server.SetActiveWorktreeKey(key)
}

func buildConfig(opts Options) *config.Config {
	interval := opts.SyncInterval
	if interval == 0 {
		interval = 5 * time.Minute
	}

	basePath := opts.BasePath
	if basePath == "" {
		basePath = "/"
	} else {
		basePath = "/" + strings.Trim(basePath, "/")
		if basePath != "/" {
			basePath += "/"
		}
	}

	viewMode := opts.Activity.ViewMode
	if viewMode == "" {
		viewMode = "threaded"
	}

	timeRange := opts.Activity.TimeRange
	if timeRange == "" {
		timeRange = "7d"
	}

	var repos []config.Repo
	for _, r := range opts.Repos {
		repos = append(repos, config.Repo{
			Owner: r.Owner,
			Name:  r.Name,
		})
	}
	if repos == nil {
		repos = []config.Repo{}
	}

	return &config.Config{
		SyncInterval:   interval.String(),
		GitHubTokenEnv: "UNUSED",
		Host:           "127.0.0.1",
		Port:           8091,
		BasePath:       basePath,
		DataDir:        opts.DataDir,
		Repos:          repos,
		Activity: config.Activity{
			ViewMode:   viewMode,
			TimeRange:  timeRange,
			HideClosed: opts.Activity.HideClosed,
			HideBots:   opts.Activity.HideBots,
		},
	}
}

func resolveAssets(opts Options) (fs.FS, error) {
	if opts.Assets != nil {
		return opts.Assets, nil
	}
	return web.Assets()
}
