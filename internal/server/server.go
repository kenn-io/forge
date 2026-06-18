package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"go.kenn.io/middleman/internal/config"
	"go.kenn.io/middleman/internal/configwatch"
	"go.kenn.io/middleman/internal/db"
	"go.kenn.io/middleman/internal/docs"
	"go.kenn.io/middleman/internal/gitclone"
	ghclient "go.kenn.io/middleman/internal/github"
	"go.kenn.io/middleman/internal/ptyowner"
	ptyownerruntime "go.kenn.io/middleman/internal/ptyowner/runtime"
	"go.kenn.io/middleman/internal/telemetry"
	"go.kenn.io/middleman/internal/tokenauth"
	"go.kenn.io/middleman/internal/workspace"
	"go.kenn.io/middleman/internal/workspace/localruntime"
)

const middlemanCSRFHeaderName = "X-Middleman-Csrf"

type versionOutputBody struct {
	Version string `json:"version"`
}

type versionOutput = bodyOutput[versionOutputBody]

type ServerOptions struct {
	// APIAuthToken, when non-empty, gates /api routes behind bearer
	// or session-cookie auth (see api_auth.go). Health probes stay
	// open. Minted under data_dir by the serve entrypoint when
	// [api].require_auth is set.
	APIAuthToken                       string
	Clones                             *gitclone.Manager // optional clone manager for diff view
	WorktreeDir                        string            // base dir for workspace worktrees
	DisableWorkspaceBackgroundMonitors bool
	PtyOwnerDir                        string
	PtyOwnerExePath                    string
	PtyOwnerExeArgs                    []string
	PtyOwnerManagerPath                string
	PtyOwnerCommand                    []string
	PtyOwnerInProcess                  bool
	Telemetry                          telemetry.Client
	TokenSources                       *tokenauth.SourceSet
	// HostCheck overrides the Host validation middleware options.
	// When Valid(), the override wins over any cfg-derived options.
	// Used by wire-level tests that want to control the bind /
	// allowed_hosts / trust_reverse_proxy independently of a full
	// config.Config.
	HostCheck HostCheckOptions
	// HostCheckAllowLoopbackAnyPort relaxes literal loopback Host
	// port matching after HostCheck/cfg options have been selected.
	// Use this for httptest-style listeners on ephemeral ports.
	HostCheckAllowLoopbackAnyPort bool
	msgvaultRemoteImageDeps       *msgvaultRemoteImageDeps
}

type shutdownDeadline struct {
	mu       sync.RWMutex
	deadline time.Time
	set      bool
}

var (
	startupTmuxCleanupTimeout    = 2 * time.Second
	runtimeSessionCleanupTimeout = 2 * time.Second
)

func (d *shutdownDeadline) tighten(deadline time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.set || deadline.Before(d.deadline) {
		d.deadline = deadline
		d.set = true
	}
}

func (d *shutdownDeadline) get() (time.Time, bool) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.deadline, d.set
}

type shutdownAwareContext struct {
	parent   context.Context
	deadline *shutdownDeadline
}

func (c shutdownAwareContext) Deadline() (time.Time, bool) {
	deadline, ok := c.deadline.get()
	if !ok {
		return c.parent.Deadline()
	}
	if parentDeadline, parentOK := c.parent.Deadline(); parentOK &&
		parentDeadline.Before(deadline) {
		return parentDeadline, true
	}
	return deadline, true
}

func (c shutdownAwareContext) Done() <-chan struct{} {
	return c.parent.Done()
}

func (c shutdownAwareContext) Err() error {
	return c.parent.Err()
}

func (c shutdownAwareContext) Value(key any) any {
	return c.parent.Value(key)
}

// Server holds the HTTP mux and its dependencies.
type Server struct {
	db                          *db.DB
	syncer                      *ghclient.Syncer
	clones                      *gitclone.Manager
	workspaces                  *workspace.Manager
	workspacePRMonitor          *workspace.PRMonitor
	workspacePushedHeadObserver *workspace.PushedHeadObserver
	tmuxActivity                *tmuxActivityTracker
	fleetTmuxMonitor            *fleetTmuxMonitor
	fleetWorktreeDiscoverer     *fleetWorktreeDiscoverer
	fleetWorktreeStatsSampler   *fleetWorktreeStatsSampler
	fleetPlatformAuthMonitor    *fleetPlatformAuthMonitor
	runtime                     *localruntime.Manager
	tmuxCmd                     []string
	telemetry                   telemetry.Client
	cfg                         *config.Config
	cfgPath                     string
	tokenSources                *tokenauth.SourceSet
	cfgMu                       sync.Mutex
	configReloadMu              sync.Mutex
	// bootCfgSnapshot freezes the subset of config fields that are
	// bound at startup (registry, listeners, clone manager, etc.) so a
	// config-file watcher reload can detect when those changed and
	// surface restart_required to the UI without ever mutating them.
	bootCfgSnapshot     startupConfigSnapshot
	runtimeStripEnvVars []string
	configWatcher       *configwatch.Watcher
	basePath            string
	options             ServerOptions
	allowedHostMu       sync.RWMutex
	allowedHosts        map[string]struct{}
	// hostOpts is atomic: Serve repoints an ephemeral (port-0) bind
	// at the kernel-assigned port while requests may already be
	// reading the options.
	hostOpts               atomic.Pointer[HostCheckOptions]
	version                string
	now                    func() time.Time
	handler                http.Handler
	hub                    *EventHub
	activeWorktreeMu       sync.Mutex
	activeWorktreeKey      string
	activeWorktreeSet      bool
	labelCatalogRefreshMu  sync.Mutex
	labelCatalogRefreshIDs map[int64]struct{}
	detailSyncMu           sync.Mutex
	detailSyncInFlight     map[string]struct{}
	writeCredProbeMu       sync.Mutex
	writeCredProbes        map[string]writeCredentialProbe
	writeCredProbeInFlight map[string]chan struct{}
	kataHealthMu           sync.Mutex
	kataHealthCache        map[string]kataDaemonHealthCacheEntry
	kataHealthInFlight     map[string]*kataDaemonInflightProbe
	kataProxyMu            sync.Mutex
	kataProxyCache         map[kataProxyCacheKey]kataProxyCacheEntry
	kataProxyIdleCloseOnce sync.Once
	docsRegistry           *docs.Registry
	docsPublishLocks       *docsPublishLockSet
	msgvault               *msgvaultHandler

	// toolingStatus caches the assembled CLI tooling probe;
	// toolingRun overrides the probe subprocess runner in tests.
	toolingStatus toolingStatusCache
	toolingRun    toolingRunner

	// apiAuthToken gates /api routes when non-empty (api_auth.go).
	apiAuthToken string

	// sshFleet relays API exchanges to fleet peers reached over
	// ssh(1); nil when no ssh peers are configured (fleet_ssh.go).
	sshFleet *sshFleetTransport

	// bg tracks short-lived goroutines that HTTP handlers spawn
	// outside of the Syncer's own wait group (e.g. mergePR's
	// post-failure refresh). Shutdown waits on bg before the
	// caller tears down the DB.
	//
	// bgMu guards shuttingDown, drainDone, and httpSrv, and
	// serializes bg.Add against Shutdown's bg.Wait so the
	// WaitGroup cannot observe Add racing with Wait when the
	// counter transiently hits zero.
	bgMu         sync.Mutex
	bg           sync.WaitGroup
	bgCtx        context.Context
	bgCancel     context.CancelFunc
	bgDeadline   *shutdownDeadline
	shuttingDown bool
	// drainDone is created the first time Shutdown is called and
	// closed when bg.Wait returns. Every caller waits on it
	// subject to its own ctx, so a retry with a longer deadline
	// observes true drain after an earlier caller's ctx expired.
	drainDone chan struct{}
	httpSrv   *http.Server
	// connWG tracks per-connection goroutines spawned by Serve.
	// Incremented from ConnState(StateNew), decremented from
	// ConnState(StateClosed|StateHijacked). Shutdown waits on it
	// after http.Server.Shutdown so that the deferred setState in
	// (*conn).serve finishes before tests tear down dependencies.
	connWG sync.WaitGroup
}

// trackHTTPConn is installed as http.Server.ConnState by Serve so
// Shutdown can wait for per-connection goroutines to fully unwind.
func (s *Server) trackHTTPConn(_ net.Conn, state http.ConnState) {
	switch state {
	case http.StateNew:
		s.connWG.Add(1)
	case http.StateHijacked, http.StateClosed:
		s.connWG.Done()
	}
}

// Hub returns the server's SSE event hub. Callers should never
// retain the returned pointer beyond the server's lifetime.
func (s *Server) Hub() *EventHub { return s.hub }

// SubscriberCount returns the number of live SSE subscribers. Intended
// for tests that need to wait for a connection to register before
// broadcasting (broadcasts issued before subscription would otherwise
// race against the handler's Subscribe call).
func (s *Server) SubscriberCount() int { return s.hub.SubscriberCount() }

// SetVersion sets the version string returned by GET /api/v1/version.
func (s *Server) SetVersion(v string) { s.version = v }

// runBackground launches fn as a tracked goroutine. fn receives a
// context cancelled by Shutdown. If Shutdown has already started,
// runBackground drops the task: these goroutines are best-effort
// refreshes and starting one during drain would race with bg.Wait.
func (s *Server) runBackground(fn func(ctx context.Context)) bool {
	s.bgMu.Lock()
	if s.shuttingDown {
		s.bgMu.Unlock()
		return false
	}
	s.bg.Add(1)
	s.bgMu.Unlock()
	go func() {
		defer s.bg.Done()
		fn(s.bgCtx)
	}()
	return true
}

func (s *Server) runWorkspacePRMonitorLoop(ctx context.Context) {
	if s.workspacePRMonitor == nil {
		return
	}

	s.runWorkspacePRMonitorPass(ctx)

	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runWorkspacePRMonitorPass(ctx)
		}
	}
}

func (s *Server) runWorkspacePRMonitorPass(ctx context.Context) {
	if s.workspacePRMonitor == nil {
		return
	}

	updates, err := s.workspacePRMonitor.RunOnce(ctx)
	if err != nil {
		slog.Warn("workspace PR monitor pass failed", "err", err)
		return
	}
	for i := range updates {
		update := updates[i]
		s.broadcastWorkspaceStatus(update.WorkspaceID)
		s.hub.Broadcast(Event{Type: "data_changed", Data: struct{}{}})
	}
}

func (s *Server) runWorkspacePushedHeadObserverLoop(ctx context.Context) {
	if s.workspacePushedHeadObserver == nil {
		return
	}

	s.runWorkspacePushedHeadObserverPass(ctx)

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runWorkspacePushedHeadObserverPass(ctx)
		}
	}
}

func (s *Server) runWorkspacePushedHeadObserverPass(ctx context.Context) {
	if s.workspacePushedHeadObserver == nil {
		return
	}

	result, err := s.workspacePushedHeadObserver.RunOnce(ctx)
	if err != nil {
		slog.Warn("workspace pushed-head observer pass failed", "err", err)
		return
	}
	for i := range result.Associations {
		association := result.Associations[i]
		s.hub.Broadcast(Event{
			Type: "workspace_pr_associated",
			Data: workspacePRAssociatedPayload{
				WorkspaceID:  association.WorkspaceID,
				Provider:     string(association.Provider),
				PlatformHost: association.PlatformHost,
				RepoPath:     association.RepoPath,
				Owner:        association.Owner,
				Name:         association.Name,
				IssueNumber:  association.IssueNumber,
				PRNumber:     association.PRNumber,
				AssociatedAt: formatUTCRFC3339(association.AssociatedAt),
			},
		})
		s.broadcastWorkspaceStatus(association.WorkspaceID)
		s.hub.Broadcast(Event{Type: "data_changed", Data: struct{}{}})
	}
	for i := range result.HeadChanges {
		change := result.HeadChanges[i]
		s.hub.Broadcast(Event{
			Type: "workspace_pushed_head_changed",
			Data: workspacePushedHeadChangedPayload{
				WorkspaceID:  change.WorkspaceID,
				Provider:     string(change.Provider),
				PlatformHost: change.PlatformHost,
				RepoPath:     change.RepoPath,
				Owner:        change.Owner,
				Name:         change.Name,
				Number:       change.Number,
				OldSHA:       change.OldSHA,
				NewSHA:       change.NewSHA,
				Remote:       change.RemoteName,
				Branch:       change.BranchName,
				TrackingRef:  change.TrackingRef,
				ObservedAt:   formatUTCRFC3339(change.ObservedAt),
			},
		})
		s.enqueueWorkspacePushedHeadRefresh(change)
	}
}

func (s *Server) broadcastWorkspaceStatus(workspaceID string) {
	s.hub.Broadcast(Event{
		Type: "workspace_status",
		Data: map[string]string{"id": workspaceID},
	})
}

// Shutdown stops the HTTP listener (if started via ListenAndServe
// or Serve), closes the SSE event hub so streaming handlers exit,
// cancels background goroutines' context, and blocks until they
// finish or ctx expires. Safe to call concurrently and repeatedly.
// Every caller drives http.Server.Shutdown with its own ctx
// (stdlib polls idle-conn closure per call) and waits on a shared
// drain channel, so a retry with a longer deadline observes true
// drain for both HTTP handlers and the bg group. Only the first
// caller closes the hub and cancels bgCtx.
func (s *Server) Shutdown(ctx context.Context) error {
	s.bgMu.Lock()
	first := !s.shuttingDown
	if first {
		s.shuttingDown = true
		s.drainDone = make(chan struct{})
		if deadline, ok := ctx.Deadline(); ok {
			s.bgDeadline.tighten(deadline)
		}
	}
	drainDone := s.drainDone
	httpSrv := s.httpSrv
	s.bgMu.Unlock()

	// Close the hub first so handleSSE subscribers can exit on
	// their <-done select arm. Otherwise http.Server.Shutdown
	// below would wait on SSE handlers that never return until
	// client disconnect, hanging the shutdown until ctx expires.
	if first && s.hub != nil {
		s.hub.Close()
	}
	if first && s.runtime != nil {
		s.runtime.Shutdown()
	}
	if first {
		s.sshFleet.shutdown()
	}

	var httpErr error
	httpDrained := httpSrv == nil
	if httpSrv != nil {
		httpErr = httpSrv.Shutdown(ctx)
		// http.Server.Shutdown returns when active connections
		// become idle and are removed from its tracking map, but
		// the per-connection goroutine's deferred setState(Closed)
		// chain is still running on its way out. Wait for our
		// ConnState hook to observe the final state transition so
		// callers can safely tear down dependencies.
		connDone := make(chan struct{})
		go func() {
			s.connWG.Wait()
			close(connDone)
		}()
		select {
		case <-connDone:
		case <-ctx.Done():
			if httpErr == nil {
				httpErr = ctx.Err()
			}
		}
		httpDrained = httpErr == nil
	}

	if httpDrained {
		s.kataProxyIdleCloseOnce.Do(s.closeKataProxyIdleConnections)
	}

	if first {
		s.bgCancel()
		go func() {
			s.bg.Wait()
			close(drainDone)
		}()
	}

	select {
	case <-drainDone:
		return httpErr
	case <-ctx.Done():
		if httpErr != nil {
			return errors.Join(httpErr, ctx.Err())
		}
		return ctx.Err()
	}
}

// SetActiveWorktreeKey sets the key of the currently
// focused worktree. Thread-safe.
func (s *Server) SetActiveWorktreeKey(key string) {
	s.activeWorktreeMu.Lock()
	s.activeWorktreeKey = key
	s.activeWorktreeSet = true
	s.activeWorktreeMu.Unlock()
}

// ActiveWorktreeKey returns the key of the currently
// focused worktree and whether it was explicitly set.
// Thread-safe.
func (s *Server) ActiveWorktreeKey() (string, bool) {
	s.activeWorktreeMu.Lock()
	defer s.activeWorktreeMu.Unlock()
	return s.activeWorktreeKey, s.activeWorktreeSet
}

// New creates a Server without config persistence.
// Pass cfg for repo filtering (can be nil for tests that
// don't need filtering).
func New(
	database *db.DB,
	syncer *ghclient.Syncer,
	frontend fs.FS,
	basePath string,
	cfg *config.Config,
	opts ServerOptions,
) *Server {
	return newServer(
		database, syncer, opts.Clones, frontend,
		basePath, cfg, "", opts,
	)
}

// NewWithConfig creates a Server with config persistence for
// settings/repo endpoints.
func NewWithConfig(
	database *db.DB,
	syncer *ghclient.Syncer,
	clones *gitclone.Manager,
	frontend fs.FS,
	cfg *config.Config,
	cfgPath string,
	opts ServerOptions,
) *Server {
	return newServer(
		database, syncer, clones, frontend,
		cfg.BasePath, cfg, cfgPath, opts,
	)
}

// hostCheckTestFallbackBindHost / Port define the bind used when
// server.New is called with cfg=nil AND no explicit
// ServerOptions.HostCheck. These match the defaults that come out
// of config.Load, so existing same-package tests work without
// per-test churn.
const (
	hostCheckTestFallbackBindHost = "127.0.0.1"
	hostCheckTestFallbackBindPort = "8091"
)

// testFallbackAllowedHosts is the allowlist applied alongside the
// fallback bind. httptest.NewRequest defaults the Host to
// "example.com" and the apitest helpers use "middleman.test"; both
// must be accepted so the dozens of test helpers that pass
// cfg=nil work unchanged.
func testFallbackAllowedHosts() []config.HostKey {
	return []config.HostKey{
		{Host: "example.com", Port: ""},
		{Host: "middleman.test", Port: ""},
	}
}

// allowUnvalidatedConfigHostCheckFallbackForTests is false in
// production. Same-package tests set it from _test.go so legacy
// partial config literals can exercise unrelated server behavior
// without manufacturing a full validated config.
var allowUnvalidatedConfigHostCheckFallbackForTests bool

// resolveHostCheckOptions applies the precedence rule:
// caller override > cfg-derived options > cfg=nil test-friendly
// fallback. For non-nil configs that bypassed config.Load, derive
// the bind and allowlist from the provided config fields so
// production callers do not silently inherit hard-coded host
// defaults.
func resolveHostCheckOptions(
	cfg *config.Config,
	override HostCheckOptions,
	allowLoopbackAnyPort bool,
) HostCheckOptions {
	opts, err := pickHostCheckOptions(cfg, override)
	if err != nil {
		panic(err)
	}
	if allowLoopbackAnyPort {
		opts.AllowLoopbackAnyPort = true
	}
	return opts
}

func pickHostCheckOptions(cfg *config.Config, override HostCheckOptions) (HostCheckOptions, error) {
	if override.Valid() {
		return override, nil
	}
	if cfg != nil {
		if k := cfg.BindHostKey(); k.Valid() {
			return HostCheckOptions{
				Bind:              k,
				Allowed:           cfg.ParsedAllowedHosts(),
				TrustReverseProxy: cfg.TrustReverseProxy,
			}, nil
		}
		opts, err := deriveHostCheckOptionsFromConfig(cfg)
		if err == nil {
			return opts, nil
		}
		if !allowUnvalidatedConfigHostCheckFallbackForTests {
			return HostCheckOptions{}, fmt.Errorf("server: config did not provide valid Host check options: %w", err)
		}
		return fallbackHostCheckOptions(), nil
	}
	slog.Warn(
		"server.New used without a cfg or explicit ServerOptions.HostCheck; using httptest-compatible Host defaults. Production callers must pass a validated config or explicit HostCheck options.",
	)
	return fallbackHostCheckOptions(), nil
}

func deriveHostCheckOptionsFromConfig(cfg *config.Config) (HostCheckOptions, error) {
	if strings.TrimSpace(cfg.Host) == "" {
		return HostCheckOptions{}, errors.New("host is empty")
	}
	if ip := net.ParseIP(cfg.Host); ip == nil {
		return HostCheckOptions{}, fmt.Errorf("config: invalid host %q", cfg.Host)
	} else if !ip.IsLoopback() {
		return HostCheckOptions{}, fmt.Errorf(
			"config: host %q is not loopback; only loopback addresses are supported",
			cfg.Host,
		)
	}
	if cfg.Port < 1 || cfg.Port > 65535 {
		return HostCheckOptions{}, fmt.Errorf("port %d is outside 1-65535", cfg.Port)
	}
	bind, err := config.ParseHostKey(net.JoinHostPort(cfg.Host, fmt.Sprintf("%d", cfg.Port)))
	if err != nil {
		return HostCheckOptions{}, fmt.Errorf("bind host %q: %w", cfg.ListenAddr(), err)
	}
	allowed := make([]config.HostKey, 0, len(cfg.AllowedHosts))
	for _, entry := range cfg.AllowedHosts {
		key, err := config.ParseHostKey(entry)
		if err != nil {
			return HostCheckOptions{}, fmt.Errorf("allowed_hosts entry %q: %w", entry, err)
		}
		allowed = append(allowed, key)
	}
	return HostCheckOptions{
		Bind:              bind,
		Allowed:           allowed,
		TrustReverseProxy: cfg.TrustReverseProxy,
	}, nil
}

func fallbackHostCheckOptions() HostCheckOptions {
	return HostCheckOptions{
		Bind: config.HostKey{
			Host: hostCheckTestFallbackBindHost,
			Port: hostCheckTestFallbackBindPort,
		},
		Allowed:              testFallbackAllowedHosts(),
		TrustReverseProxy:    false,
		AllowLoopbackAnyPort: true,
	}
}

func newServer(
	database *db.DB,
	syncer *ghclient.Syncer,
	clones *gitclone.Manager,
	frontend fs.FS,
	basePath string,
	cfg *config.Config,
	cfgPath string,
	options ServerOptions,
) *Server {
	mux := http.NewServeMux()

	bgBaseCtx, bgCancel := context.WithCancel(context.Background())
	bgDeadline := &shutdownDeadline{}
	hostOpts := resolveHostCheckOptions(
		cfg,
		options.HostCheck,
		options.HostCheckAllowLoopbackAnyPort,
	)

	s := &Server{
		db:                     database,
		basePath:               basePath,
		syncer:                 syncer,
		clones:                 clones,
		telemetry:              options.Telemetry,
		cfg:                    cfg,
		cfgPath:                cfgPath,
		tokenSources:           options.TokenSources,
		bootCfgSnapshot:        snapshotStartupConfig(cfg),
		runtimeStripEnvVars:    initialRuntimeStripEnvNames(cfg),
		options:                options,
		apiAuthToken:           options.APIAuthToken,
		now:                    time.Now,
		hub:                    NewEventHubWithCapacity(cfg.SSEBufferSizeOrDefault()),
		tmuxActivity:           newTmuxActivityTracker(nil),
		labelCatalogRefreshIDs: make(map[int64]struct{}),
		docsPublishLocks:       newDocsPublishLockSet(),
		msgvault:               newMsgvaultHandler(cfg, basePath, options.msgvaultRemoteImageDeps),
		bgCtx: shutdownAwareContext{
			parent:   bgBaseCtx,
			deadline: bgDeadline,
		},
		bgCancel:   bgCancel,
		bgDeadline: bgDeadline,
	}
	var docFolders []config.DocFolder
	if cfg != nil {
		docFolders = cfg.DocFolders
	}
	s.docsRegistry = docs.NewRegistry(docFolders)
	warnDocFolderDaemonBindings(docFolders)

	s.hostOpts.Store(&hostOpts)
	if hostOpts.TrustReverseProxy && len(hostOpts.Allowed) == 0 {
		slog.Warn(
			"trust_reverse_proxy is enabled but allowed_hosts is empty; only loopback Hosts will be accepted",
		)
	}

	// (*Config).TmuxCommand handles a nil receiver and returns the
	// default ["tmux"]. Compute once so the workspace, runtime, and
	// terminal handler all share the same value and the nil-safety
	// of the call is explicit at this level.
	tmuxCmd := cfg.TmuxCommand()
	s.tmuxCmd = tmuxCmd
	hideTmuxStatus := false
	if cfg != nil {
		hideTmuxStatus = cfg.Terminal.HideTmuxStatus
	}
	tmuxAvailable := tmuxCommandAvailable(tmuxCmd)
	includeUnmanagedTmuxDetails := false
	if cfg != nil {
		includeUnmanagedTmuxDetails = cfg.Fleet.Sessions.IncludeUnmanagedDetails
	}
	s.fleetTmuxMonitor = newFleetTmuxMonitor(
		tmuxCmd, includeUnmanagedTmuxDetails, nil,
	)
	s.fleetWorktreeDiscoverer = newFleetWorktreeDiscoverer(database)
	s.fleetWorktreeStatsSampler = newFleetWorktreeStatsSampler(
		database, s.notifyWorktreeStatsChanged,
	)
	s.fleetPlatformAuthMonitor = newFleetPlatformAuthMonitor(
		s.snapshotPlatformAuthConfig,
	)
	if cfg != nil && len(cfg.Fleet.SSHPeers) > 0 && cfg.DataDir != "" {
		s.sshFleet = newSSHFleetTransport(
			filepath.Join(cfg.DataDir, "ssh-sockets"),
			cfg.Fleet.SSHPeers,
			s.hub,
		)
	}
	if options.WorktreeDir != "" {
		s.workspaces = workspace.NewManager(database, options.WorktreeDir)
		s.workspacePRMonitor = workspace.NewPRMonitor(database)
		s.workspacePushedHeadObserver = workspace.NewPushedHeadObserver(database)
		s.workspaces.SetTmuxCommand(tmuxCmd)
		s.workspaces.SetHideTmuxStatus(hideTmuxStatus)
		s.workspaces.SetIssueBranchSlugEnabled(
			cfg.IssueWorkspaceBranchSlugEnabled(),
		)
		s.workspaces.SetWorktreeBasePathResolver(s.worktreeBasePathForRepo)
		ptyOwnerDir := options.PtyOwnerDir
		if ptyOwnerDir == "" {
			ptyOwnerDir = filepath.Join(
				filepath.Dir(options.WorktreeDir), "pty-owner",
			)
		}
		ptyOwnerClient := &ptyowner.Client{
			Root:        ptyOwnerDir,
			ExePath:     options.PtyOwnerExePath,
			ExeArgs:     append([]string(nil), options.PtyOwnerExeArgs...),
			ManagerPath: options.PtyOwnerManagerPath,
			Command:     append([]string(nil), options.PtyOwnerCommand...),
			InProcess:   options.PtyOwnerInProcess,
		}
		if preferPtyOwnerForWorkspaces(runtime.GOOS, tmuxAvailable, options) {
			s.workspaces.SetPtyOwnerClient(ptyOwnerClient)
		} else {
			s.workspaces.SetPtyOwnerFallbackClient(ptyOwnerClient)
		}
		if clones != nil {
			s.workspaces.SetClones(clones)
		}
		if tmuxAvailable {
			cleanupCtx, cleanupCancel := context.WithTimeout(
				context.Background(), startupTmuxCleanupTimeout,
			)
			if err := s.workspaces.ReapOrphanTmuxSessions(cleanupCtx); err != nil {
				slog.Warn("reap orphan tmux sessions", "err", err)
			}
			if err := s.workspaces.PruneMissingTmuxSessions(cleanupCtx); err != nil {
				slog.Warn("prune missing tmux sessions", "err", err)
			}
			cleanupCancel()
		}
		var agents []config.Agent
		if cfg != nil {
			agents = cfg.Agents
		}
		// Runtime sessions that are not tmux-backed must still be owned
		// outside the middleman server process so restarts do not tear down
		// workspace terminal state. Tmux-backed sessions still attach via
		// tmux; the runtime manager only uses this owner for non-tmux starts.
		runtimePtyOwner := ptyownerruntime.New(ptyOwnerClient, nil)
		s.runtime = localruntime.NewManager(localruntime.Options{
			Targets: localruntime.ResolveLaunchTargets(
				agents, tmuxCmd, nil,
			),
			TmuxCommand:              tmuxCmd,
			TmuxOwnerMarker:          s.workspaces.TmuxOwnerMarker(),
			WrapAgentSessionsInTmux:  cfg.TmuxAgentSessionsEnabled(),
			HideTmuxStatus:           hideTmuxStatus,
			StripEnvVars:             s.runtimeStripEnvVars,
			ShellCommand:             cfg.ShellCommand(),
			OnSessionExit:            s.handleRuntimeSessionExit,
			PtyOwnerRuntime:          runtimePtyOwner,
			KnownPtyOwnerSessionKeys: s.workspaces.RuntimeSessionKeysForWorkspace,
		})
		if err := s.restoreRuntimeSessions(context.Background()); err != nil {
			slog.Warn("restore runtime tmux sessions", "err", err)
		}
	}

	if s.workspaces != nil && !options.DisableWorkspaceBackgroundMonitors {
		s.runBackground(s.runWorkspacePRMonitorLoop)
		s.runBackground(s.runWorkspacePushedHeadObserverLoop)
	}
	if s.workspaces != nil && tmuxAvailable && s.fleetTmuxMonitor != nil {
		s.runBackground(s.fleetTmuxMonitor.run)
	}
	if s.fleetWorktreeDiscoverer != nil && !options.DisableWorkspaceBackgroundMonitors {
		s.runBackground(s.fleetWorktreeDiscoverer.run)
	}
	if s.fleetWorktreeStatsSampler != nil && !options.DisableWorkspaceBackgroundMonitors {
		s.runBackground(s.fleetWorktreeStatsSampler.run)
	}
	if s.fleetPlatformAuthMonitor != nil && !options.DisableWorkspaceBackgroundMonitors {
		s.runBackground(s.fleetPlatformAuthMonitor.run)
	}

	// Watch the config file so an external edit (vim, dotfiles deploy,
	// sd -i, etc.) is picked up without a restart. Watcher init failures
	// are logged inside startConfigWatcher; the server still serves.
	s.startConfigWatcher()

	healthAPI := humago.New(mux, healthAPIConfig())
	s.registerHealthAPI(healthAPI)

	api := humago.NewWithPrefix(mux, "/api/v1", apiConfig(basePath))
	api.UseMiddleware(newResponseCompressionMiddleware(responseCompressionMinSize))
	s.registerAPI(api)
	if s.workspaces != nil {
		s.registerTerminalAPI(api, tmuxCmd)
		wsAPI := humago.NewWithPrefix(mux, "/ws/v1", terminalAPIConfig())
		s.registerTerminalAPI(wsAPI, tmuxCmd)
	}

	// Roborev proxy
	if cfg != nil {
		roborevAPI := humago.NewWithPrefix(
			mux, "/api", roborevProxyAPIConfig(),
		)
		s.registerRoborevProxyAPI(roborevAPI)
	}

	if frontend != nil {
		mux.Handle("/", newSPAAssetHandler(frontend, basePath, s.bootstrapScript))
	}

	// When serving under a base path, use an outer mux with
	// StripPrefix so the inner mux sees clean paths like /api/v1/...
	// Health endpoints stay at the root so external probes do not need
	// to know about the UI base path.
	if basePath != "/" {
		outer := http.NewServeMux()
		prefix := strings.TrimSuffix(basePath, "/")
		outer.Handle("/healthz", mux)
		outer.Handle("/livez", mux)
		outer.Handle(basePath, http.StripPrefix(prefix, mux))
		s.handler = outer
	} else {
		s.handler = mux
	}

	return s
}

func (s *Server) restoreRuntimeSessions(ctx context.Context) error {
	if s.db == nil || s.runtime == nil || s.workspaces == nil {
		return nil
	}
	stored, err := s.db.ListAllWorkspaceRuntimeSessions(ctx)
	if err != nil {
		return err
	}
	for _, session := range stored {
		summary, err := s.workspaces.GetSummary(ctx, session.WorkspaceID)
		if err != nil {
			return err
		}
		if summary == nil {
			continue
		}
		restored := localruntime.RestoredRuntimeSession{
			WorkspaceID: session.WorkspaceID,
			SessionKey:  session.SessionKey,
			TargetKey:   session.TargetKey,
			Label:       session.Label,
			Kind:        localruntime.LaunchTargetKind(session.Kind),
			TmuxSession: session.TmuxSession,
			CWD:         summary.WorktreePath,
			CreatedAt:   session.CreatedAt,
		}
		err = s.runtime.RestoreRuntimeSessions(
			ctx, []localruntime.RestoredRuntimeSession{restored},
		)
		if err == nil {
			continue
		}
		if errors.Is(err, localruntime.ErrSessionNotFound) {
			if _, forgetErr := s.workspaces.ForgetRuntimeSessionCreatedAt(
				ctx, session.WorkspaceID, session.SessionKey, session.CreatedAt,
			); forgetErr != nil {
				return forgetErr
			}
			continue
		}
		if errors.Is(err, localruntime.ErrSessionUnavailable) {
			slog.Warn(
				"runtime session unavailable after restore",
				"workspace_id", session.WorkspaceID,
				"session_key", session.SessionKey,
				"target_key", session.TargetKey,
				"tmux_session", session.TmuxSession,
				"err", err,
			)
			continue
		}
		return err
	}

	slog.Debug("restored runtime sessions", "count", len(stored))
	return nil
}

func (s *Server) handleRuntimeSessionExit(info localruntime.SessionInfo) {
	if info.WorkspaceID == hostRuntimeScope {
		if s.db == nil || info.TmuxSession == "" {
			return
		}
		s.runBackground(func(ctx context.Context) {
			cleanupCtx, cancel := context.WithTimeout(
				ctx, runtimeSessionCleanupTimeout,
			)
			defer cancel()
			// Generation-qualified: command session keys are reusable, so
			// this exit's cleanup must not delete the row of a newer live
			// session relaunched under the same key.
			if _, err := s.db.DeleteHostRuntimeTmuxSessionCreatedAt(
				cleanupCtx, info.Key, info.CreatedAt,
			); err != nil {
				slog.Warn(
					"forget host runtime tmux session",
					"session_key", info.Key,
					"tmux_session", info.TmuxSession,
					"err", err,
				)
			}
		})
		return
	}
	if worktreeID, ok := strings.CutPrefix(info.WorkspaceID, "project-worktree:"); ok {
		if worktreeID == "" || s.db == nil || info.TmuxSession == "" {
			return
		}
		s.runBackground(func(ctx context.Context) {
			cleanupCtx, cancel := context.WithTimeout(
				ctx, runtimeSessionCleanupTimeout,
			)
			defer cancel()
			// Generation-qualified: command session keys are reusable, so
			// this exit's cleanup must not delete the row of a newer live
			// session relaunched under the same key.
			if _, err := s.db.DeleteProjectWorktreeTmuxSessionCreatedAt(
				cleanupCtx, worktreeID, info.Key, info.CreatedAt,
			); err != nil {
				slog.Warn(
					"forget project worktree runtime tmux session",
					"worktree_id", worktreeID,
					"session_key", info.Key,
					"tmux_session", info.TmuxSession,
					"err", err,
				)
			}
		})
		return
	}
	if s.workspaces == nil {
		return
	}
	s.runBackground(func(ctx context.Context) {
		cleanupCtx, cancel := context.WithTimeout(
			ctx, runtimeSessionCleanupTimeout,
		)
		defer cancel()
		if _, err := s.workspaces.ForgetRuntimeSessionAfterExit(
			cleanupCtx, info.WorkspaceID, info.Key, info.CreatedAt,
			info.TmuxSession,
		); err != nil {
			slog.Warn(
				"forget exited runtime session",
				"workspace_id", info.WorkspaceID,
				"session_key", info.Key,
				"err", err,
			)
		}
	})
}

func preferPtyOwnerForWorkspaces(
	runtimeGOOS string,
	tmuxAvailable bool,
	options ServerOptions,
) bool {
	if !tmuxAvailable {
		return true
	}
	return runtimeGOOS == "windows" &&
		(options.PtyOwnerManagerPath != "" || options.PtyOwnerExePath != "" ||
			options.PtyOwnerInProcess)
}

func tmuxCommandAvailable(command []string) bool {
	if len(command) == 0 || command[0] == "" {
		return false
	}
	_, err := exec.LookPath(command[0])
	return err == nil
}

func (s *Server) bootstrapScript() string {
	safeBase, _ := json.Marshal(s.basePath)
	var builder strings.Builder
	builder.WriteString(`window.__BASE_PATH__=`)
	builder.WriteString(scriptSafe(string(safeBase)))
	builder.WriteString(`;`)
	// The served config carries the daemon-side UI state thin
	// clients set over the API (PUT /api/v1/ui/active-worktree);
	// presentation preferences (embed mode, theming) are injected
	// client-side by whoever hosts the webview.
	if awKey, set := s.ActiveWorktreeKey(); set {
		configJSON, _ := json.Marshal(map[string]any{
			"ui": map[string]any{"activeWorktreeKey": awKey},
		})
		builder.WriteString(`window.__middleman_config=`)
		builder.WriteString(scriptSafe(string(configJSON)))
		builder.WriteString(`;`)
	}
	return builder.String()
}

// scriptSafe escapes sequences that could break out of an inline
// <script> block. Replaces "</" with "<\/" so that payloads
// containing "</script>" cannot close the tag early.
func scriptSafe(s string) string {
	return strings.ReplaceAll(s, "</", `<\/`)
}

// ServeHTTP implements http.Handler so Server can be used directly.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	logged := &statusLoggingResponseWriter{ResponseWriter: w}
	w = logged
	start := time.Now()
	slog.Debug(
		"http request started",
		"method", r.Method,
		"path", r.URL.Path,
		"query", redactedQuery(r.URL),
		"remote_addr", r.RemoteAddr,
		"user_agent", r.UserAgent(),
	)
	defer func() {
		status := logged.status
		if status == 0 {
			status = http.StatusOK
		}
		args := []any{
			"method", r.Method,
			"path", r.URL.Path,
			"query", redactedQuery(r.URL),
			"status", status,
			"duration", time.Since(start).String(),
			"bytes", logged.bytes,
			"remote_addr", r.RemoteAddr,
			"user_agent", r.UserAgent(),
		}
		if status >= http.StatusBadRequest {
			slog.Warn("http request failed", args...)
		} else {
			slog.Debug("http request completed", args...)
		}
	}()
	if !checkHost(w, r, *s.hostOpts.Load()) {
		return
	}
	if !s.checkHost(w, r) {
		return
	}
	if s.apiAuthToken != "" {
		if s.handleAuthBootstrap(w, r) {
			return
		}
		if s.isGatedAPIRequest(r) && !s.authorizeAPIRequest(w, r) {
			return
		}
	}
	if r.Method != http.MethodGet && s.isMutatingAPIRequest(r) {
		if !checkCSRF(w, r, s.isKataProxyAPIRequest(r)) {
			return
		}
		if s.isMutatingDocsAPIRequest(r) && !isLoopbackRemoteAddr(r.RemoteAddr) {
			writeProblemResponse(w, newProblem(
				http.StatusForbidden,
				CodeForbidden,
				"docs mutations require a loopback client",
				map[string]any{"reason": "loopbackOnly"},
			))
			return
		}
		if s.isMutatingMessagesAPIRequest(r) {
			if !isLoopbackRemoteAddr(r.RemoteAddr) {
				writeProblemResponse(w, newProblem(
					http.StatusForbidden,
					CodeForbidden,
					"message configuration changes require a loopback client",
					map[string]any{"reason": "loopbackOnly"},
				))
				return
			}
			if r.Header.Get(middlemanCSRFHeaderName) == "" {
				writeProblemResponse(w, newProblem(
					http.StatusForbidden,
					CodeForbidden,
					"message mutations require the "+middlemanCSRFHeaderName+" header",
					map[string]any{"reason": "missingCsrfHeader"},
				))
				return
			}
		}
	}
	if r.Method == http.MethodGet && s.isDocsBrowseAPIRequest(r) && !isLoopbackRemoteAddr(r.RemoteAddr) {
		writeProblemResponse(w, newProblem(
			http.StatusForbidden,
			CodeForbidden,
			"docs browse requires a loopback client",
			map[string]any{"reason": "loopbackOnly"},
		))
		return
	}
	if r.Method == http.MethodGet && s.isDocsReadAPIRequest(r) && !isLoopbackRemoteAddr(r.RemoteAddr) {
		writeProblemResponse(w, newProblem(
			http.StatusForbidden,
			CodeForbidden,
			"docs reads require a loopback client",
			map[string]any{"reason": "loopbackOnly"},
		))
		return
	}
	if r.Method == http.MethodGet && s.isMessagesSavedSearchesAPIRequest(r) && !isLoopbackRemoteAddr(r.RemoteAddr) {
		writeProblemResponse(w, newProblem(
			http.StatusForbidden,
			CodeForbidden,
			"message saved searches require a loopback client",
			map[string]any{"reason": "loopbackOnly"},
		))
		return
	}
	s.handler.ServeHTTP(w, r)
}

func (s *Server) checkHost(w http.ResponseWriter, r *http.Request) bool {
	s.allowedHostMu.RLock()
	allowedHosts := s.allowedHosts
	s.allowedHostMu.RUnlock()
	return checkListenerHost(w, r, allowedHosts)
}

func checkListenerHost(
	w http.ResponseWriter,
	r *http.Request,
	allowedHosts map[string]struct{},
) bool {
	if len(allowedHosts) == 0 {
		return true
	}
	if !authorityIsLoopbackHost(r.Host) || isLoopbackRemoteAddr(r.RemoteAddr) {
		return true
	}
	writeProblemResponse(w, newProblem(
		http.StatusForbidden,
		CodeForbidden,
		"host is not allowed",
		map[string]any{"reason": "hostNotAllowed"},
	))
	return false
}

// isMutatingAPIRequest checks whether the request targets an API route,
// accounting for the configured basePath prefix.
func (s *Server) isMutatingAPIRequest(r *http.Request) bool {
	path := r.URL.Path
	if s.basePath != "/" {
		prefix := strings.TrimSuffix(s.basePath, "/")
		path = strings.TrimPrefix(path, prefix)
	}
	return strings.HasPrefix(path, "/api/")
}

func (s *Server) isKataProxyAPIRequest(r *http.Request) bool {
	path := r.URL.Path
	if s.basePath != "/" {
		prefix := strings.TrimSuffix(s.basePath, "/")
		path = strings.TrimPrefix(path, prefix)
	}
	return path == kataProxyPrefix || strings.HasPrefix(path, kataProxyPrefix+"/")
}

func (s *Server) isMutatingDocsAPIRequest(r *http.Request) bool {
	path := r.URL.Path
	if s.basePath != "/" {
		prefix := strings.TrimSuffix(s.basePath, "/")
		path = strings.TrimPrefix(path, prefix)
	}
	return strings.HasPrefix(path, "/api/v1/docs/")
}

func (s *Server) isMutatingMessagesAPIRequest(r *http.Request) bool {
	path := r.URL.Path
	if s.basePath != "/" {
		prefix := strings.TrimSuffix(s.basePath, "/")
		path = strings.TrimPrefix(path, prefix)
	}
	return path == "/api/v1/msgvault/configure" ||
		path == "/api/v1/messages/saved-searches"
}

func (s *Server) isMessagesSavedSearchesAPIRequest(r *http.Request) bool {
	path := r.URL.Path
	if s.basePath != "/" {
		prefix := strings.TrimSuffix(s.basePath, "/")
		path = strings.TrimPrefix(path, prefix)
	}
	return path == "/api/v1/messages/saved-searches"
}

func (s *Server) isDocsBrowseAPIRequest(r *http.Request) bool {
	path := r.URL.Path
	if s.basePath != "/" {
		prefix := strings.TrimSuffix(s.basePath, "/")
		path = strings.TrimPrefix(path, prefix)
	}
	return path == "/api/v1/docs/browse"
}

func (s *Server) isDocsReadAPIRequest(r *http.Request) bool {
	path := r.URL.Path
	if s.basePath != "/" {
		prefix := strings.TrimSuffix(s.basePath, "/")
		path = strings.TrimPrefix(path, prefix)
	}
	if path == "/api/v1/docs/folders" || path == "/api/v1/docs/search" {
		return true
	}
	if !strings.HasPrefix(path, "/api/v1/docs/folders/") {
		return false
	}
	return strings.HasSuffix(path, "/tree") ||
		strings.HasSuffix(path, "/git") ||
		strings.HasSuffix(path, "/git/changes") ||
		strings.HasSuffix(path, "/file") ||
		strings.HasSuffix(path, "/blob") ||
		strings.HasSuffix(path, "/search")
}

func isLoopbackRemoteAddr(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func authorityIsLoopbackHost(hostHeader string) bool {
	host := hostHeader
	if h, _, err := net.SplitHostPort(hostHeader); err == nil {
		host = h
	}
	host = strings.ToLower(host)
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// checkCSRF rejects cross-site mutation requests. Returns true if
// the request is allowed, false if it was rejected (response written).
func checkCSRF(w http.ResponseWriter, r *http.Request, allowProxyContentType bool) bool {
	if sfs := r.Header.Get("Sec-Fetch-Site"); sfs != "" {
		if sfs != "same-origin" && sfs != "none" {
			writeError(w, http.StatusForbidden,
				"cross-origin requests are not allowed")
			return false
		}
	}

	// Require Content-Type: application/json on all mutation requests,
	// including zero-body endpoints like POST /sync. This prevents
	// cross-origin form submissions and simple fetches from forging
	// requests even without Sec-Fetch-Site.
	ct := r.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		if allowProxyContentType && r.Header.Get("Sec-Fetch-Site") != "" {
			return true
		}
		writeError(w, http.StatusUnsupportedMediaType,
			"Content-Type must be application/json")
		return false
	}

	return true
}

// ListenAndServe starts the HTTP server on addr. Returns
// http.ErrServerClosed when stopped by Shutdown (matches net/http).
func (s *Server) ListenAndServe(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	return s.Serve(ln)
}

// Serve accepts HTTP connections on the provided listener. Useful
// for tests and any caller that wants to own the listener lifetime.
// Returns http.ErrServerClosed when stopped by Shutdown.
func (s *Server) Serve(ln net.Listener) error {
	s.setAllowedHostsForListener(ln)
	s.adoptListenerHostPort(ln)
	srv := &http.Server{
		Handler:     s,
		ReadTimeout: 15 * time.Second,
		// WriteTimeout is 0 (disabled) because the roborev
		// proxy streams SSE/NDJSON responses that are
		// long-lived by design. A non-zero value would kill
		// /api/roborev/api/stream/events and /api/job/log
		// after the deadline.
		IdleTimeout: 60 * time.Second,
		ConnState:   s.trackHTTPConn,
	}

	s.bgMu.Lock()
	if s.shuttingDown {
		s.bgMu.Unlock()
		_ = ln.Close()
		return http.ErrServerClosed
	}
	s.httpSrv = srv
	s.bgMu.Unlock()

	return srv.Serve(ln)
}

// AttachHTTPServer records an externally-started HTTP server so Shutdown can
// close the listener after a startup handler has been swapped to this Server.
func (s *Server) AttachHTTPServer(srv *http.Server, ln net.Listener) {
	s.setAllowedHostsForListener(ln)
	s.adoptListenerHostPort(ln)
	s.bgMu.Lock()
	s.httpSrv = srv
	s.bgMu.Unlock()
}

// adoptListenerHostPort repoints the Host-check bind at the actual
// bound port when the configured bind asked for a kernel-assigned
// one (port 0) - otherwise every request to an ephemeral-port daemon
// would be rejected, since no Host header can match port "0".
func (s *Server) adoptListenerHostPort(ln net.Listener) {
	opts := *s.hostOpts.Load()
	if opts.Bind.Port != "0" {
		return
	}
	_, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		return
	}
	opts.Bind.Port = port
	s.hostOpts.Store(&opts)
}

func (s *Server) setAllowedHostsForListener(ln net.Listener) {
	allowed := allowedHostsForListener(ln)
	s.allowedHostMu.Lock()
	s.allowedHosts = allowed
	s.allowedHostMu.Unlock()
}

func allowedHostsForListener(ln net.Listener) map[string]struct{} {
	host, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return nil
	}
	out := map[string]struct{}{}
	for _, h := range []string{host, "127.0.0.1", "localhost", "::1"} {
		out[strings.ToLower(net.JoinHostPort(h, port))] = struct{}{}
	}
	return out
}

// handleSSE streams server events to a client. The handler subscribes
// to the EventHub and forwards each broadcast as an SSE frame. It exits
// when the client disconnects, when the hub closes, when the subscriber
// is evicted (slow consumer), or when context is canceled.
func (s *Server) handleSSE(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	rc := http.NewResponseController(w)
	// Clear server-wide WriteTimeout for this SSE response
	if err := rc.SetWriteDeadline(time.Time{}); err != nil {
		return
	}
	cursor, hasCursor := parseLastEventID(r)
	s.serveSSE(r.Context(), w, rc, cursor, hasCursor)
}

func (s *Server) streamEvents(
	_ context.Context, _ *struct{},
) (*huma.StreamResponse, error) {
	return &huma.StreamResponse{
		Body: func(ctx huma.Context) {
			ctx.SetHeader("Content-Type", "text/event-stream")
			ctx.SetHeader("Cache-Control", "no-cache")
			ctx.SetHeader("Connection", "keep-alive")

			r, w := humago.Unwrap(ctx)
			rc := http.NewResponseController(w)
			_ = rc.SetWriteDeadline(time.Time{})
			cursor, hasCursor := parseLastEventID(r)
			s.serveSSE(ctx.Context(), w, rc, cursor, hasCursor)
		},
	}, nil
}

type sseController interface {
	SetWriteDeadline(time.Time) error
	Flush() error
}

// parseLastEventID inspects an incoming SSE request for a reconnect
// cursor. The Last-Event-ID header takes priority (HTML5 EventSource
// emits it automatically on reconnect); the since= query parameter is
// the fallback for non-browser callers and explicit first-connect
// resumption. Returns (0, false) when no usable cursor is present, so
// the handler can fall back to the no-cursor path (live + cached
// sync_status) without further branching.
func parseLastEventID(r *http.Request) (uint64, bool) {
	candidates := []string{r.Header.Get("Last-Event-ID")}
	if q := r.URL.Query().Get("since"); q != "" {
		candidates = append(candidates, q)
	}
	for _, raw := range candidates {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		n, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			slog.Debug("sse: ignoring unparseable cursor", "value", raw, "err", err)
			continue
		}
		return n, true
	}
	return 0, false
}

func (s *Server) serveSSE(
	ctx context.Context,
	w io.Writer,
	rc sseController,
	cursor uint64,
	hasCursor bool,
) {
	// Subscribe BEFORE the first flush so any broadcast issued between
	// the headers landing on the wire and the subscriber being registered
	// is delivered to this client instead of dropped. When a cursor is
	// supplied the handler replays the ring directly, so cached
	// sync_status injection by Subscribe would duplicate; pass false.
	ch, done := s.hub.Subscribe(ctx, !hasCursor)

	if err := rc.Flush(); err != nil {
		return
	}

	// Resolve the replay path before entering the live loop so the
	// client sees missed events (or a stale signal) before any new
	// live broadcasts and never out of order with them.
	deliveredThrough := cursor
	if hasCursor {
		replay, synID, stale := s.hub.ReplaySnapshotSince(cursor)
		if stale {
			if !writeSSEFrame(w, rc, synID, "reconnect.stale", []byte("{}")) {
				return
			}
			deliveredThrough = synID
		} else {
			for _, rec := range replay {
				if !writeSSERecorded(w, rc, rec) {
					return
				}
				deliveredThrough = rec.ID
			}
		}
	}

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		// Non-blocking done check
		select {
		case <-done:
			return
		default:
		}

		select {
		case <-done:
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			if hasCursor && ev.ID <= deliveredThrough {
				// Already replayed; skip the duplicate that arrived
				// via the cached-status pre-load or a race between
				// the snapshot read and a fresh broadcast.
				continue
			}
			if !writeSSERecorded(w, rc, ev) {
				return
			}
		case <-ticker.C:
			if err := rc.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
				return
			}
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			if err := rc.Flush(); err != nil {
				return
			}
			if err := rc.SetWriteDeadline(time.Time{}); err != nil {
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

// writeSSERecorded serializes a recorded event and writes it as a
// framed SSE frame. Returns true on success, false if any write or
// flush failed and the handler should exit.
func writeSSERecorded(w io.Writer, rc sseController, rec RecordedEvent) bool {
	data, err := json.Marshal(rec.Event.Data)
	if err != nil {
		slog.Error("sse: marshal event", "type", rec.Event.Type, "err", err)
		// Skip the unmarshalable event but keep streaming.
		return true
	}
	return writeSSEFrame(w, rc, rec.ID, rec.Event.Type, data)
}

func writeSSEFrame(
	w io.Writer, rc sseController, id uint64, eventType string, data []byte,
) bool {
	if err := rc.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return false
	}
	if _, err := fmt.Fprintf(
		w, "id: %d\nevent: %s\ndata: %s\n\n", id, eventType, data,
	); err != nil {
		return false
	}
	if err := rc.Flush(); err != nil {
		return false
	}
	if err := rc.SetWriteDeadline(time.Time{}); err != nil {
		return false
	}
	return true
}

func (s *Server) getVersion(
	_ context.Context, _ *struct{},
) (*versionOutput, error) {
	resp := &versionOutput{}
	resp.Body.Version = s.version
	return resp, nil
}

// writeJSON encodes v as JSON and writes it with the given HTTP status code.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError writes a JSON error response.
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
