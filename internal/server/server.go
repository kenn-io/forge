package server

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"go.kenn.io/forge/internal/agentactivity"
	"go.kenn.io/forge/internal/archive"
	"go.kenn.io/forge/internal/browserlogin"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/configwatch"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/devbox"
	"go.kenn.io/forge/internal/docs"
	"go.kenn.io/forge/internal/federation"
	"go.kenn.io/forge/internal/federationauth"
	"go.kenn.io/forge/internal/fleet"
	"go.kenn.io/forge/internal/gitclone"
	ghclient "go.kenn.io/forge/internal/github"
	katacatalog "go.kenn.io/forge/internal/kata"
	"go.kenn.io/forge/internal/providerplane"
	"go.kenn.io/forge/internal/ptyowner"
	ptyownerruntime "go.kenn.io/forge/internal/ptyowner/runtime"
	"go.kenn.io/forge/internal/server/activityapi"
	"go.kenn.io/forge/internal/server/archiveapi"
	"go.kenn.io/forge/internal/server/authapi"
	"go.kenn.io/forge/internal/server/browserloginapi"
	"go.kenn.io/forge/internal/server/compression"
	"go.kenn.io/forge/internal/server/configreload"
	"go.kenn.io/forge/internal/server/devboxapi"
	"go.kenn.io/forge/internal/server/docsapi"
	"go.kenn.io/forge/internal/server/fleetapi"
	"go.kenn.io/forge/internal/server/hostapi"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/server/issueapi"
	"go.kenn.io/forge/internal/server/itemapi"
	"go.kenn.io/forge/internal/server/kata"
	"go.kenn.io/forge/internal/server/notificationapi"
	"go.kenn.io/forge/internal/server/operationapi"
	"go.kenn.io/forge/internal/server/otelmiddleware"
	"go.kenn.io/forge/internal/server/providerapi"
	"go.kenn.io/forge/internal/server/pullapi"
	"go.kenn.io/forge/internal/server/repoapi"
	"go.kenn.io/forge/internal/server/repobrowserapi"
	"go.kenn.io/forge/internal/server/roborevapi"
	"go.kenn.io/forge/internal/server/routepolicy"
	"go.kenn.io/forge/internal/server/settingsapi"
	"go.kenn.io/forge/internal/server/spokeapi"
	"go.kenn.io/forge/internal/server/statuslog"
	"go.kenn.io/forge/internal/server/streamapi"
	"go.kenn.io/forge/internal/server/syncevents"
	"go.kenn.io/forge/internal/server/telemetryapi"
	"go.kenn.io/forge/internal/server/workflowapi"
	"go.kenn.io/forge/internal/server/workspaceapi"
	"go.kenn.io/forge/internal/systemclipboard"
	"go.kenn.io/forge/internal/telemetry"
	"go.kenn.io/forge/internal/terminalpaste"
	"go.kenn.io/forge/internal/tokenauth"
	"go.kenn.io/forge/internal/workspace"
	"go.kenn.io/forge/internal/workspace/localruntime"
	"go.kenn.io/forge/platform"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

type BuildInfo struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildDate string `json:"buildDate"`
}

type (
	versionOutputBody BuildInfo
	versionOutput     = httpapi.BodyOutput[versionOutputBody]
)

type ServerOptions struct {
	Devboxes                           *devbox.Connections
	ExecutionWorker                    bool
	DaemonAccess                       authapi.DaemonAccessOptions
	FederationCredentials              *federationauth.Store
	FederationEnrollments              *federation.Store
	FederationSpokeID                  string
	FederationSpokeActive              bool
	FederationSpokeUnavailableReason   string
	MaintainFederationSpokeActivation  func(context.Context)
	FederationHTTPClient               *http.Client
	ProviderWriteGate                  *providerplane.ProviderWriteGate
	MCPURL                             string
	Clones                             *gitclone.Manager // optional clone manager for diff view
	WorktreeDir                        string            // base dir for workspace worktrees
	DisableWorkspaceBackgroundMonitors bool
	DisableWorkspaceEnrichment         bool
	WorkspaceNow                       func() time.Time
	PtyOwnerDir                        string
	PtyOwnerExePath                    string
	PtyOwnerExeArgs                    []string
	PtyOwnerManagerPath                string
	PtyOwnerCommand                    []string
	PtyOwnerInProcess                  bool
	Telemetry                          telemetry.Client
	TokenSources                       *tokenauth.SourceSet
	Archive                            archive.Controller
	DocsRegistry                       *docs.Registry
	// TerminalClipboard overrides native clipboard integration in tests.
	TerminalClipboard systemclipboard.Writer
	// HostCheck overrides the Host validation middleware options.
	// When Valid(), the override wins over any cfg-derived options.
	// Used by wire-level tests that want to control the bind /
	// allowed_hosts / trust_reverse_proxy independently of a full
	// config.Config.
	HostCheck authapi.HostCheckOptions
	// HostCheckAllowLoopbackAnyPort relaxes literal loopback Host
	// port matching after HostCheck/cfg options have been selected.
	// Use this for httptest-style listeners on ephemeral ports.
	HostCheckAllowLoopbackAnyPort bool
	// DetachRuntimeSessionsForRestart makes shutdown-driven terminal
	// attachment loss reconnectable. Development reloaders use this because
	// the durable tmux/ptyowner process outlives the server process.
	DetachRuntimeSessionsForRestart bool
	deferredMergeMaxWait            time.Duration
}

// Server holds the HTTP mux and its dependencies.
type Server struct {
	db             *db.DB
	repoResolver   *httpapi.RepositoryResolver
	syncer         *ghclient.Syncer
	archive        archive.Controller
	clones         *gitclone.Manager
	workspaces     *workspace.Manager
	fleetAPI       *fleetapi.Handler
	runtime        *localruntime.Manager
	tmuxCmd        []string
	telemetry      telemetry.Client
	cfg            *config.Config
	cfgPath        string
	tokenSources   *tokenauth.SourceSet
	cfgMu          sync.Mutex
	configReloadMu sync.Mutex
	// repoVisibilityMu serializes hidden-from-UI mutations with the orphan
	// sweep so a visibility write cannot interleave with a concurrent
	// exact-entry removal and recreate an orphaned preference.
	repoVisibilityMu sync.Mutex
	// bootCfgSnapshot freezes the subset of config fields that are
	// bound at startup (registry, listeners, clone manager, etc.) so a
	// config-file watcher reload can detect when those changed and
	// surface restart_required to the UI without ever mutating them.
	bootCfgSnapshot     configreload.StartupConfigSnapshot
	fleetEnabledAtBoot  bool
	runtimeStripEnvVars []string
	ptyOwnerClient      *ptyowner.Client
	configWatcher       *configwatch.Watcher
	basePath            string
	options             ServerOptions
	allowedHostMu       sync.RWMutex
	allowedHosts        map[string]struct{}
	// hostOpts is atomic: Serve repoints an ephemeral (port-0) bind
	// at the kernel-assigned port while requests may already be
	// reading the options.
	hostOpts atomic.Pointer[authapi.HostCheckOptions]
	// tailnetMCP serves /mcp on this listener for allowlisted Tailscale
	// Serve users; nil until the MCP companion is initialized.
	tailnetMCP             atomic.Pointer[http.Handler]
	buildInfo              BuildInfo
	now                    func() time.Time
	handler                http.Handler
	hub                    *syncevents.EventHub
	federationStreamsMu    sync.Mutex
	federationStreamsNext  uint64
	federationStreams      map[string]map[uint64]context.CancelFunc
	activeWorktreeMu       sync.Mutex
	activeWorktreeKey      string
	activeWorktreeSet      bool
	labelCatalogRefreshMu  sync.Mutex
	labelCatalogRefreshIDs map[int64]struct{}
	detailSyncMu           sync.Mutex
	detailSyncInFlight     map[string]struct{}
	detailSyncPending      map[string]syncevents.DetailSyncJob
	writeCredProbeMu       sync.Mutex
	writeCredProbes        map[string]operationapi.WriteCredentialProbe
	writeCredProbeInFlight map[string]chan struct{}
	viewerLoginMu          sync.Mutex
	viewerLoginCache       map[string]authapi.ViewerLoginCacheEntry
	viewerLoginInFlight    map[string]*authapi.ViewerLoginCall
	docsAPI                *docsapi.Handler
	kataAPI                *kata.Handler
	repoBrowserAPI         *repobrowserapi.Handler
	pullAPI                *pullapi.Handler
	issueAPI               *issueapi.Handler
	workflowAPI            *workflowapi.Handler
	pullLifecycle          streamapi.PullLifecycle
	workspaceAPI           *workspaceapi.Handler
	providerSource         *spokeapi.HubProviderSource
	providerProxy          *routepolicy.ProviderProxy
	hubEvents              *spokeapi.HubEventLifecycle
	spokeActivationLease   *spokeapi.HubEventLifecycle
	providerRouteSpoke     bool
	providerWriteGate      *providerplane.ProviderWriteGate
	// activityAfterItemsForTest pauses Activity between its two identity reads
	// so tests can prove the request-wide repository reconciliation fence.
	activityAfterItemsForTest func()
	// providerDescriptorBeforeSnapshotForTest marks descriptor admission before
	// the reconciliation lease so tests can queue an identity writer first.
	providerDescriptorBeforeSnapshotForTest func()
	markdownImages                          *providerapi.MarkdownImageCache
	roborevRepositories                     *roborevapi.RoborevRepositoryProbe

	// toolingStatus caches the assembled CLI tooling probe;
	// toolingRun overrides the probe subprocess runner in tests.
	toolingStatus repoapi.ToolingStatusCache
	toolingRun    repoapi.ToolingRunner

	daemonRequests authapi.DaemonRequestPolicy
	federationAuth *federationauth.Authenticator
	// browserLoginTickets and browserSessions hold digest-only secrets for
	// peer-issued browser logins; both are in memory only.
	browserLoginTickets *browserlogin.Store
	browserSessions     *browserlogin.Store

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
	bgDeadline   *streamapi.ShutdownDeadline
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

	// workspaceDependents tracks Fleet and repository-browser loops started
	// after Workspace. Root shutdown drains this group before stopping the
	// Workspace domain they consume.
	workspaceDependentsCtx    context.Context
	workspaceDependentsCancel context.CancelFunc
	workspaceDependentsWG     sync.WaitGroup
	workspaceDependentsDone   chan struct{}
	workspaceDependentsOnce   sync.Once
	workspaceLifecycleCtx     context.Context
	workspaceLifecycleCancel  context.CancelFunc
	workspaceDependencyStop   *streamapi.WorkspaceDependencyShutdown

	// Handlers for the packages split out of this one; see wireHandlers.
	activityapi     *activityapi.Handlers
	archiveapi      *archiveapi.Handlers
	authapi         *authapi.Handlers
	browserloginapi *browserloginapi.Handlers
	configreload    *configreload.Handlers
	devboxapi       *devboxapi.Handlers
	hostapi         *hostapi.Handlers
	itemapi         *itemapi.Handlers
	notificationapi *notificationapi.Handlers
	operationapi    *operationapi.Handlers
	providerapi     *providerapi.Handlers
	repoapi         *repoapi.Handlers
	roborevapi      *roborevapi.Handlers
	routepolicy     *routepolicy.Handlers
	settingsapi     *settingsapi.Handlers
	spokeapi        *spokeapi.Handlers
	streamapi       *streamapi.Handlers
	syncevents      *syncevents.Handlers
	telemetryapi    *telemetryapi.Handlers
}

// trackHTTPConn is installed as http.Server.ConnState by Serve so
// Shutdown can wait for per-connection goroutines to fully unwind.
func (s *Server) trackHTTPConn(_ net.Conn, state http.ConnState) {
	switch state {
	case http.StateNew:
		s.connWG.Add(1)
	case http.StateHijacked, http.StateClosed:
		s.connWG.Done()
	case http.StateActive, http.StateIdle:
	}
}

// Hub returns the server's SSE event hub. Callers should never
// retain the returned pointer beyond the server's lifetime.
func (s *Server) Hub() *syncevents.EventHub { return s.hub }

// Fleet returns the composed Fleet service boundary.
func (s *Server) Fleet() *fleetapi.Handler { return s.fleetAPI }

// SubscriberCount returns the number of live SSE subscribers. Intended
// for tests that need to wait for a connection to register before
// broadcasting (broadcasts issued before subscription would otherwise
// race against the handler's Subscribe call).
func (s *Server) SubscriberCount() int { return s.hub.SubscriberCount() }

// SetBuildInfo sets the metadata returned by GET /api/v1/version.
func (s *Server) SetBuildInfo(info BuildInfo) { s.buildInfo = info }

// workflowRuntime exposes event publication and tracked background work to
// the workflow API without leaking the rest of the server.
type workflowRuntime struct{ server *Server }

func (r workflowRuntime) Publish(eventType string, data any) {
	r.server.hub.Broadcast(syncevents.Event{Type: eventType, Data: data})
}

func (r workflowRuntime) Go(fn func(context.Context)) bool {
	return r.server.streamapi.RunBackground(fn)
}

// Shutdown stops the HTTP listener (if started via ListenAndServe
// or Serve), closes the SSE event hub so streaming handlers exit,
// drains later-started Workspace consumers, shuts Workspace down before its
// runtime dependency, cancels remaining background goroutines, and blocks
// until they finish or ctx expires. Safe to call concurrently and repeatedly.
// Every caller drives http.Server.Shutdown with its own ctx
// (stdlib polls idle-conn closure per call) and waits on a shared
// drain channel, so a retry with a longer deadline observes true
// drain for both HTTP handlers and the bg group. Only the first
// caller closes the hub and cancels bgCtx.
func (s *Server) Shutdown(ctx context.Context) error {
	if s.pullLifecycle != nil {
		s.pullLifecycle.Stop()
	}
	s.bgMu.Lock()
	first := !s.shuttingDown
	if first {
		s.shuttingDown = true
		s.drainDone = make(chan struct{})
		if deadline, ok := ctx.Deadline(); ok {
			s.bgDeadline.Tighten(deadline)
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
	// Agent handoffs outlive their HTTP requests on purpose; cancel them
	// here so the HTTP drain below does not wait on a handoff that only the
	// later workspace shutdown would end.
	if first && s.workspaceAPI != nil {
		s.workspaceAPI.CancelAgentHandoffs()
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

	if first {
		s.streamapi.StopWorkspaceDependents()
		s.bgCancel()
		go func() {
			s.bg.Wait()
			close(drainDone)
		}()
	}
	if !httpDrained {
		return httpErr
	}
	return s.workspaceDependencyStop.Shutdown(ctx)
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
	bgDeadline := &streamapi.ShutdownDeadline{}
	hostOpts := streamapi.ResolveHostCheckOptions(
		cfg,
		options.HostCheck,
		options.HostCheckAllowLoopbackAnyPort,
	)
	deferredMergeMaxWait := options.deferredMergeMaxWait
	terminalClipboard := options.TerminalClipboard
	if terminalClipboard == nil {
		terminalClipboard = systemclipboard.NewWriter()
	}
	markdownImageDataDir := ""
	if cfg != nil {
		markdownImageDataDir = cfg.DataDir
	}
	var terminalPasteImages *terminalpaste.Store
	if markdownImageDataDir != "" {
		var err error
		terminalPasteImages, err = terminalpaste.NewStore(filepath.Join(
			markdownImageDataDir,
			"cache",
			"terminal-paste-images",
		))
		if err != nil {
			slog.Warn("initialize terminal paste image cache", "err", err)
		}
	}
	repoResolver := httpapi.NewRepositoryResolver(httpapi.RepositoryResolverDeps{
		DB: database,
		ProviderCapabilities: func(kind platform.Kind, host string) (platform.Capabilities, error) {
			if syncer == nil {
				return platform.Capabilities{}, errors.New("provider registry unavailable")
			}
			return syncer.ProviderCapabilities(kind, host)
		},
	})

	s := &Server{
		db:                     database,
		repoResolver:           repoResolver,
		basePath:               basePath,
		syncer:                 syncer,
		archive:                options.Archive,
		clones:                 clones,
		telemetry:              options.Telemetry,
		cfg:                    cfg,
		cfgPath:                cfgPath,
		tokenSources:           options.TokenSources,
		bootCfgSnapshot:        configreload.SnapshotStartupConfig(cfg),
		fleetEnabledAtBoot:     cfg != nil && cfg.Fleet.Enabled,
		runtimeStripEnvVars:    configreload.InitialRuntimeStripEnvNames(cfg),
		options:                options,
		daemonRequests:         authapi.NewDaemonRequestPolicy(options.DaemonAccess),
		federationAuth:         federationauth.NewAuthenticator(options.FederationCredentials),
		now:                    time.Now,
		hub:                    syncevents.NewEventHubWithCapacity(cfg.SSEBufferSizeOrDefault()),
		labelCatalogRefreshIDs: make(map[int64]struct{}),
		markdownImages:         providerapi.NewMarkdownImageCache(providerapi.MarkdownImageCacheRoot(markdownImageDataDir)),
		bgCtx: streamapi.ShutdownAwareContext{
			Parent:        bgBaseCtx,
			DeadlineValue: bgDeadline,
		},
		bgCancel:                bgCancel,
		bgDeadline:              bgDeadline,
		workspaceDependentsDone: make(chan struct{}),
	}
	s.wireHandlers()
	s.browserLoginTickets = browserlogin.NewTicketStore(func() time.Time { return s.now() })
	s.browserSessions = browserlogin.NewSessionStore(func() time.Time { return s.now() })
	s.providerWriteGate = options.ProviderWriteGate
	if s.providerWriteGate == nil {
		restoreDurableState := false
		if options.FederationEnrollments != nil {
			_, restoreDurableState = options.FederationEnrollments.Local()
		}
		s.providerWriteGate = providerplane.NewProviderWriteGate(database, restoreDurableState)
	}
	if cfg != nil && cfg.Fleet.RoleOrDefault() == config.FleetRoleSpoke {
		s.providerRouteSpoke = true
		s.providerSource = &spokeapi.HubProviderSource{
			Db: database, Clones: clones, Enabled: s.streamapi.FederationEnabled,
		}
		if options.FederationSpokeActive &&
			options.MaintainFederationSpokeActivation != nil {
			s.spokeActivationLease = syncevents.NewHubEventLifecycleStoppingOnCleanReturn(
				cfg.Fleet.Enabled, options.MaintainFederationSpokeActivation,
			)
		}
		if options.FederationSpokeActive && cfg.Fleet.Hub != nil {
			client, err := providerplane.NewClient(providerplane.Options{
				LocalNodeID: options.FederationSpokeID,
				Hub: providerplane.Hub{
					NodeID:  cfg.Fleet.Hub.NodeID,
					BaseURL: cfg.Fleet.Hub.BaseURL,
				},
				Credentials: options.FederationCredentials,
				HTTPClient:  options.FederationHTTPClient,
			})
			if err != nil {
				slog.Error("configure hub provider client", "err", err)
			} else {
				s.providerSource.Client = client
				s.providerProxy = routepolicy.NewProviderProxy(client)
				events, eventsErr := providerplane.NewEventClient(providerplane.EventClientOptions{
					Client:              client,
					OnEvent:             s.syncevents.ReceiveHubEvent,
					OnResync:            s.syncevents.ResynchronizeHubProviderState,
					OnConnectionChanged: s.syncevents.BroadcastHubConnection,
				})
				if eventsErr != nil {
					slog.Error("configure hub event client", "err", eventsErr)
				} else {
					s.hubEvents = syncevents.NewHubEventLifecycle(
						cfg.Fleet.Enabled, events.Run,
					)
				}
			}
		}
		if s.hubEvents == nil || !cfg.Fleet.Enabled {
			s.syncevents.BroadcastHubConnection(false)
		}
	}
	roborevConfig := cfg
	if roborevConfig == nil {
		roborevConfig = &config.Config{}
	}
	s.roborevRepositories = roborevapi.NewRoborevRepositoryProbe(
		s.bgCtx,
		roborevConfig.RoborevEndpoint(),
		streamapi.WorkspaceConfigSnapshot(cfg, nil).KnownPlatformHosts,
	)
	if syncer != nil {
		syncer.SetOnMergedActorRepaired(s.syncevents.BroadcastMergedActorDetailRefresh)
		syncer.SetOnRelayRefresh(s.syncevents.BroadcastRelayRefresh)
	}
	s.workspaceDependentsCtx, s.workspaceDependentsCancel = context.WithCancel(s.bgCtx)
	s.workspaceLifecycleCtx, s.workspaceLifecycleCancel = context.WithCancel(context.Background())
	workspaceNow := s.now
	var executionWorker config.ExecutionWorker
	if options.ExecutionWorker && cfg != nil {
		executionWorker = cfg.ExecutionWorker
	}
	if options.WorkspaceNow != nil {
		workspaceNow = options.WorkspaceNow
	}
	if !options.ExecutionWorker {
		s.docsAPI = docsapi.New(docsapi.Deps{
			Config:   cfg,
			Registry: options.DocsRegistry,
			BeginConfigMutation: func() func() {
				s.configReloadMu.Lock()
				return s.configReloadMu.Unlock
			},
			SaveFolders: func(folders []config.DocFolder) error {
				if s.cfgPath == "" || s.cfg == nil {
					return docsapi.ErrSettingsUnavailable
				}
				s.cfgMu.Lock()
				defer s.cfgMu.Unlock()
				previous := slices.Clone(s.cfg.DocFolders)
				s.cfg.DocFolders = slices.Clone(folders)
				if err := s.cfg.Save(s.cfgPath); err != nil {
					s.cfg.DocFolders = previous
					return err
				}
				return nil
			},
		})
		if cfg != nil {
			docsapi.WarnDaemonBindings(cfg.DocFolders)
		}
		var repositoryDescriptorSource repobrowserapi.RepositoryDescriptorSource
		if s.providerSource != nil {
			repositoryDescriptorSource = s.providerSource
		}
		s.repoBrowserAPI = repobrowserapi.New(repobrowserapi.Deps{
			Resolver:         repoResolver,
			Clones:           clones,
			Config:           cfg,
			DescriptorSource: repositoryDescriptorSource,
			AutomaticRefreshEnabled: func() bool {
				s.cfgMu.Lock()
				defer s.cfgMu.Unlock()
				return s.cfg == nil || !s.cfg.AirplaneMode
			},
		})
	}
	s.hostOpts.Store(&hostOpts)
	if hostOpts.TrustReverseProxy && len(hostOpts.Allowed) == 0 {
		slog.Warn(
			"trust_reverse_proxy is enabled but allowed_hosts is empty; only loopback Hosts will be accepted",
		)
	}

	// (*Config).TmuxCommand handles a nil receiver and returns
	// config.DefaultTmuxCommand. Compute once so the workspace, runtime, and
	// terminal handler all share the same value and the nil-safety
	// of the call is explicit at this level.
	tmuxCmd := cfg.TmuxCommand()
	s.tmuxCmd = tmuxCmd
	hideTmuxStatus := false
	terminalGraphics := cfg.TerminalGraphicsEnabled()
	tmuxMouse := cfg.TerminalTmuxMouseEnabled()
	if cfg != nil {
		hideTmuxStatus = cfg.Terminal.HideTmuxStatus
	}
	tmuxAvailable := streamapi.TmuxCommandAvailable(tmuxCmd)
	var workspaceProviderState func(context.Context, []fleet.RawWorkspace) ([]fleet.RawWorkspace, error)
	if s.providerSource != nil {
		workspaceProviderState = s.providerSource.WorkspaceProviderState
	}
	s.fleetAPI = fleetapi.New(fleetapi.Deps{
		WorkspaceProviderState: workspaceProviderState,
		DB:                     database,
		Syncer:                 syncer,
		Config:                 streamapi.FleetConfigSnapshot(cfg, tmuxCmd),
		BasePath:               basePath,
		BuildVersion: func() string {
			return s.buildInfo.Version
		},
		Now: workspaceNow,
		LocalHandler: func() http.Handler {
			return s.handler
		},
		Broadcast: func(event fleetapi.Event) uint64 {
			return s.hub.Broadcast(syncevents.Event{Type: event.Type, Data: event.Data})
		},
		Generation:      s.hub.Generation,
		SubscriberCount: s.hub.SubscriberCount,
		WorkspaceSnapshot: func(ctx context.Context) (workspaceapi.FleetSnapshot, error) {
			if s.workspaceAPI == nil {
				return workspaceapi.FleetSnapshot{}, nil
			}
			return s.workspaceAPI.FleetSnapshot(ctx)
		},
		WorkspaceStatsSnapshot: func(ctx context.Context) (workspaceapi.FleetSnapshot, error) {
			if s.workspaceAPI == nil {
				return workspaceapi.FleetSnapshot{}, nil
			}
			return s.workspaceAPI.FleetStatsSnapshot(ctx)
		},
		QueueWorkspaceDeletion: func(id string) error {
			if s.workspaceAPI == nil {
				return errors.New("workspace cleanup is unavailable")
			}
			return s.workspaceAPI.QueueWorkspaceDeletion(id)
		},
		RuntimeSnapshot: func(scope string) workspaceapi.RuntimeSnapshot {
			if s.workspaceAPI == nil {
				return nil
			}
			return s.workspaceAPI.RuntimeSnapshot(scope)
		},
		RevalidateDiffs: func() {
			if s.workspaceAPI != nil {
				s.workspaceAPI.RevalidateSelectedDiffs()
			}
		},
		ExecutionTargets:            s.devboxSnapshots,
		NodeID:                      options.FederationSpokeID,
		FederationActive:            options.FederationSpokeActive,
		FederationUnavailableReason: options.FederationSpokeUnavailableReason,
		Credentials:                 options.FederationCredentials,
		Enrollments:                 options.FederationEnrollments,
		FederationHTTPClient:        options.FederationHTTPClient,
		PersistMember:               s.settingsapi.PersistFleetMember,
		PersistHubBinding:           s.settingsapi.PersistHubBinding,
		RemoveMember:                s.settingsapi.RemoveFleetMember,
		CancelEventStreams:          s.syncevents.CancelFederationEventStreams,
	})
	var launchSpecResolver providerplane.WorkspaceLaunchSpecResolver
	if !options.ExecutionWorker {
		launchSpecResolver = s
	}
	var workspacePullCandidates workspace.PullCandidateSource
	if s.providerSource != nil {
		launchSpecResolver = s.providerSource
		workspacePullCandidates = s.providerSource
	}
	if options.WorktreeDir != "" {
		s.workspaces = workspace.NewManager(database, options.WorktreeDir)
		if options.ExecutionWorker {
			executable, err := os.Executable()
			if err != nil {
				panic(fmt.Errorf("resolve worker executable: %w", err))
			}
			s.workspaces.SetExecutionWorker(cfg.ExecutionWorker, executable)
		}
		s.workspaces.SetNow(workspaceNow)
		s.workspaces.SetLaunchSpecResolver(launchSpecResolver)
		s.workspaces.SetRequireProviderCredential(s.providerRouteSpoke || options.ExecutionWorker)
		s.workspaces.SetTmuxCommand(tmuxCmd)
		s.workspaces.UpdateTmuxStripEnvVars(s.runtimeStripEnvVars)
		s.workspaces.SetHideTmuxStatus(hideTmuxStatus)
		s.workspaces.SetTmuxGraphics(terminalGraphics)
		s.workspaces.SetTmuxMouse(tmuxMouse)
		s.workspaces.SetIssueBranchSlugEnabled(
			cfg.IssueWorkspaceBranchSlugEnabled(),
		)
		s.workspaces.SetRoborevEndpoint(roborevConfig.RoborevEndpoint())
		s.workspaces.SetRoborevRepositoryInvalidator(s.roborevRepositories.Invalidate)
		s.workspaces.SetWorktreeBasePathResolver(s.settingsapi.WorktreeBasePathForRepo)
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
			// Configured token names must vanish from tmux-less base
			// terminals just like tmux-backed ones.
			StripEnvVars: slices.Clone(s.runtimeStripEnvVars),
			InProcess:    options.PtyOwnerInProcess,
		}
		s.ptyOwnerClient = ptyOwnerClient
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
				context.Background(), streamapi.StartupTmuxCleanupTimeout,
			)
			if err := s.workspaces.ReapOrphanTmuxSessions(cleanupCtx); err != nil {
				slog.Warn("reap orphan tmux sessions", "err", err)
			}
			cleanupCancel()
		}
		var agents []config.Agent
		if cfg != nil {
			agents = cfg.Agents
		}
		// Runtime sessions that are not tmux-backed must still be owned
		// outside the kenn-forge server process so restarts do not tear down
		// workspace terminal state. Tmux-backed sessions still attach via
		// tmux; the runtime manager only uses this owner for non-tmux starts.
		runtimePtyOwner := ptyownerruntime.New(ptyOwnerClient, nil)
		s.runtime = localruntime.NewManager(localruntime.Options{
			Targets: localruntime.ResolveLaunchTargets(
				agents, tmuxCmd, nil,
			),
			TmuxCommand:                    tmuxCmd,
			TmuxOwnerMarker:                s.workspaces.TmuxOwnerMarker(),
			WrapAgentSessionsInTmux:        cfg.TmuxAgentSessionsEnabled(),
			HideTmuxStatus:                 hideTmuxStatus,
			TmuxGraphics:                   terminalGraphics,
			TmuxMouse:                      tmuxMouse,
			StripEnvVars:                   s.runtimeStripEnvVars,
			ShellCommand:                   cfg.ShellCommand(),
			OnSessionExit:                  s.streamapi.HandleRuntimeSessionExit,
			PtyOwnerRuntime:                runtimePtyOwner,
			KnownPtyOwnerSessionKeys:       s.workspaces.RuntimeSessionKeysForWorkspace,
			DetachSessionsForServerRestart: options.DetachRuntimeSessionsForRestart,
		})
	}
	var providerWorkspaceAutomation workspaceapi.ProviderWorkspaceAutomation
	var mergeRequestWorktreeSource workspaceapi.MergeRequestWorktreeSource
	var resolveRepository func(
		context.Context, providerplane.RepositoryRoute,
	) (*db.Repo, error)
	if s.providerSource != nil {
		providerWorkspaceAutomation = s.providerSource
		mergeRequestWorktreeSource = s.providerSource
		if s.providerSource.Client != nil {
			resolveRepository = s.providerSource.ResolveRepositoryRoute
		}
	}
	s.workspaceAPI = workspaceapi.New(workspaceapi.Deps{
		ExecutionWorker:     executionWorker,
		DB:                  database,
		Resolver:            repoResolver,
		Syncer:              syncer,
		Config:              streamapi.WorkspaceConfigSnapshot(cfg, tmuxCmd),
		Workspaces:          s.workspaces,
		Runtime:             s.runtime,
		TerminalClipboard:   terminalClipboard,
		TerminalPasteImages: terminalPasteImages,
		AgentActivity: agentactivity.NewStore(filepath.Join(
			filepath.Dir(options.WorktreeDir), "agent-activity",
		)),
		TmuxCommand:        tmuxCmd,
		Now:                workspaceNow,
		EnrichmentDisabled: options.DisableWorkspaceEnrichment,
		Broadcast: func(event workspaceapi.Event) uint64 {
			return s.hub.Broadcast(syncevents.Event{Type: event.Type, Data: event.Data})
		},
		Subscribe:                   s.streamapi.SubscribeWorkspaceEvents,
		Generation:                  s.hub.Generation,
		RecomputeWorktreeLinks:      s.fleetAPI.RecomputeWorktreeLinks,
		RefreshWorktreeStats:        s.fleetAPI.RefreshWorktreeStats,
		RefreshProjectInventory:     s.fleetAPI.RefreshProjectInventory,
		LookupRepo:                  repoResolver.LookupRoute,
		ResolveRepository:           resolveRepository,
		EnqueueDetailSync:           s.syncevents.EnqueueDetailSyncWithCompletion,
		ProviderWriteGate:           s.providerWriteGate,
		LaunchSpecResolver:          launchSpecResolver,
		PullCandidates:              workspacePullCandidates,
		ProviderWorkspaceAutomation: providerWorkspaceAutomation,
		MergeRequestWorktreeSource:  mergeRequestWorktreeSource,
	})
	if !options.ExecutionWorker {
		s.kataAPI = kata.New(kata.Deps{
			DB:                     database,
			Resolver:               repoResolver,
			Config:                 streamapi.KataConfigSnapshot(cfg),
			Workspaces:             s.workspaces,
			WorkspaceAPI:           s.workspaceAPI.Workspaces(),
			SamePlatformHost:       spokeapi.SamePlatformHost,
			ConfigRepoPath:         settingsapi.ConfigRepoPath,
			OnCatalogTokenEnvNames: s.streamapi.UpdateCatalogStripEnvVars,
		})
		// Kata catalogs load lazily per request; feed their token env names
		// into stripping at boot too so terminals created before the first
		// Kata route never see cataloged credentials. Decoded-but-invalid
		// catalogs still carry their declared names, so apply them
		// regardless of the load error.
		bootCatalog, err := katacatalog.LoadCatalog()
		if err != nil {
			slog.Debug(
				"kata catalog boot load for credential stripping", "err", err,
			)
		}
		s.streamapi.UpdateCatalogStripEnvVars(bootCatalog.TokenEnvNames())
		s.workflowAPI = workflowapi.New(workflowapi.Deps{
			Resolver:       repoResolver,
			Syncer:         syncer,
			RepoOperations: s.operationapi.RepoOperations,
			Runtime:        workflowRuntime{server: s},
		})
		var pullProviderSource pullapi.ProviderSource
		var issueProviderSource issueapi.ProviderSource
		if s.providerSource != nil {
			pullProviderSource = s.providerSource
			issueProviderSource = s.providerSource
		}
		s.pullAPI = pullapi.New(pullapi.Deps{
			DB:                   database,
			Resolver:             repoResolver,
			Syncer:               syncer,
			Clones:               clones,
			Config:               streamapi.PullConfigSnapshot(cfg),
			Now:                  func() time.Time { return s.now() },
			DeferredMergeMaxWait: deferredMergeMaxWait,
			QueueWorkspaceDeletion: func(
				ctx context.Context, hostKey, workspaceID string,
			) error {
				if hostKey == "" || hostKey == s.fleetAPI.SelfKey("") {
					return s.workspaceAPI.QueueWorkspaceDeletion(workspaceID)
				}
				return s.fleetAPI.RequestWorkspaceCleanup(ctx, hostKey, workspaceID)
			},
			WorkspaceSubjects: s.workspaceAPI.WorkspaceSubjectSnapshot,
			ViewerLogins:      s.authapi.ResolveAuthenticatedViewerLogins,
			ProviderSource:    pullProviderSource,
			ProviderWriteGate: s.providerWriteGate,
			FleetSelfKey:      s.fleetAPI.SelfKey,
			FilterRepos: func(repos []db.Repo) []db.Repo {
				if s.cfg == nil {
					return repos
				}
				return s.repoapi.FilterConfiguredRepos(repos)
			},
			RepoOperations:                s.operationapi.RepoOperations,
			RepoOperationsForMergeRequest: s.operationapi.RepoOperationsForMergeRequest,
			EnqueueDetailSyncOrRerun:      s.syncevents.EnqueueDetailSyncOrRerun,
			Broadcast: func(event pullapi.Event) uint64 {
				return s.hub.Broadcast(syncevents.Event{Type: event.Type, Data: event.Data})
			},
			MarkClosedLinkedNotificationsDone: s.notificationapi.MarkClosedLinkedNotificationsDone,
		})
		s.issueAPI = issueapi.New(issueapi.Deps{
			DB:                database,
			Resolver:          repoResolver,
			Syncer:            syncer,
			Now:               func() time.Time { return s.now() },
			Config:            streamapi.IssueConfigSnapshot(cfg),
			WorkspaceSubjects: s.workspaceAPI.WorkspaceSubjectSnapshot,
			ViewerLogins:      s.authapi.ResolveAuthenticatedViewerLogins,
			ProviderSource:    issueProviderSource,
			FilterRepos: func(repos []db.Repo) []db.Repo {
				if s.cfg == nil {
					return repos
				}
				return s.repoapi.FilterConfiguredRepos(repos)
			},
			RepoOperations:                    s.operationapi.RepoOperations,
			MarkClosedLinkedNotificationsDone: s.notificationapi.MarkClosedLinkedNotificationsDone,
		})
		s.pullLifecycle = s.pullAPI
	}
	s.workspaceDependencyStop = streamapi.NewWorkspaceDependencyShutdown(
		func(ctx context.Context) error {
			for _, done := range []<-chan struct{}{
				s.workspaceDependentsDone,
				s.drainDone,
			} {
				select {
				case <-done:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			return nil
		},
		func(ctx context.Context) error {
			if s.pullLifecycle != nil {
				if err := s.pullLifecycle.Shutdown(ctx); err != nil {
					return err
				}
			}
			if err := s.fleetAPI.Shutdown(ctx); err != nil {
				return err
			}
			s.workspaceLifecycleCancel()
			return s.workspaceAPI.Shutdown(ctx)
		},
		func() {
			if s.runtime != nil {
				s.runtime.Shutdown()
			}
		},
	)
	if s.workspaces != nil {
		if err := s.workspaces.ApplyTmuxGraphics(context.Background()); err != nil {
			slog.Warn("apply startup tmux graphics setting", "err", err)
		}
		if err := s.workspaces.ApplyTmuxClipboard(context.Background()); err != nil {
			slog.Warn("apply startup tmux clipboard setting", "err", err)
		}
		if err := s.workspaces.ApplyTmuxMouse(context.Background()); err != nil {
			slog.Warn("apply startup tmux mouse setting", "err", err)
		}
	}
	recoveryCtx, cancelRecovery := context.WithTimeout(s.workspaceLifecycleCtx, 30*time.Second)
	if err := s.workspaceAPI.RestoreRuntimeSessions(recoveryCtx); err != nil {
		slog.Warn("restore runtime tmux sessions", "err", err)
	}
	cancelRecovery()
	s.workspaceAPI.Start(s.workspaceLifecycleCtx, options.DisableWorkspaceBackgroundMonitors)
	s.fleetAPI.Start(
		s.workspaceLifecycleCtx,
		tmuxAvailable && s.workspaces != nil,
		options.DisableWorkspaceBackgroundMonitors,
	)
	if s.hubEvents != nil {
		s.streamapi.RunWorkspaceDependent(s.hubEvents.Run)
	}
	if s.spokeActivationLease != nil {
		s.streamapi.RunWorkspaceDependent(s.spokeActivationLease.Run)
	}
	if clones != nil && !options.ExecutionWorker {
		// Seed even when background refresh is disabled: startup also adopts
		// safe pre-stable-ID clone paths so cached reads survive an upgrade.
		s.repoBrowserAPI.SeedRefreshRepos(context.Background())
		if !options.DisableWorkspaceBackgroundMonitors {
			s.streamapi.RunWorkspaceDependent(s.repoBrowserAPI.RunRefreshLoop)
		}
	}

	// The syncer's native-stack preference is the transition authority for
	// later settings changes, so every server binds it to the boot config
	// rather than relying on the caller that assembled the syncer. This runs
	// before the config watcher so the boot value cannot race a reload that
	// swaps the preference and reconciles from its own snapshot.
	if syncer != nil && cfg != nil {
		syncer.SetAirplaneMode(cfg.AirplaneMode)
		syncer.SetPreferGitHubNativeStacks(cfg.PullRequests.PreferGitHubNativeStacks)
		if !cfg.PullRequests.PreferGitHubNativeStacks {
			// Boot is a transition point too: the setting may have been edited
			// while the daemon was stopped, or a previous run may have saved it and
			// exited before reconciling. Stored native ordering would otherwise
			// keep driving the merge safeguard until each repository next synced,
			// and forever for repositories no longer tracked.
			s.syncevents.RestoreBranchDerivedStackProjections()
		}
	}

	// Watch the config file so an external edit (vim, dotfiles deploy,
	// sd -i, etc.) is picked up without a restart. Watcher init failures
	// are logged inside startConfigWatcher; the server still serves.
	if !options.ExecutionWorker {
		s.configreload.StartConfigWatcher()
	}

	healthAPI := humago.New(mux, routepolicy.HealthAPIConfig())
	healthAPI.UseMiddleware(otelmiddleware.OtelSpanMiddleware)
	s.routepolicy.RegisterHealthAPI(healthAPI)

	api := humago.NewWithPrefix(mux, "/api/v1", activityapi.ApiConfig(basePath))
	api.UseMiddleware(compression.NewResponseCompressionMiddleware(compression.ResponseCompressionMinSize))
	api.UseMiddleware(otelmiddleware.OtelSpanMiddleware)
	s.registerAPI(api)
	if s.workspaces != nil || options.Devboxes != nil {
		s.registerTerminalAPI(api, tmuxCmd)
		wsAPI := humago.NewWithPrefix(mux, "/ws/v1", authapi.TerminalAPIConfig())
		wsAPI.UseMiddleware(otelmiddleware.OtelSpanMiddleware)
		s.registerTerminalAPI(wsAPI, tmuxCmd)
	}

	// Roborev proxy
	if cfg != nil && !options.ExecutionWorker {
		roborevAPI := humago.NewWithPrefix(
			mux, "/api", roborevapi.RoborevProxyAPIConfig(),
		)
		s.roborevapi.RegisterRoborevProxyAPI(roborevAPI)
	}

	if frontend != nil && !options.ExecutionWorker {
		mux.Handle("/", compression.NewSPAAssetHandler(frontend, basePath, s.bootstrapScript))
	}

	// When serving under a base path, use an outer mux with
	// StripPrefix so the inner mux sees clean paths like /api/v1/...
	// Health endpoints stay at the root so external probes do not need
	// to know about the UI base path.
	var assembled http.Handler
	if basePath != "/" {
		outer := http.NewServeMux()
		prefix := strings.TrimSuffix(basePath, "/")
		outer.Handle("/healthz", mux)
		outer.Handle("/livez", mux)
		s.registerDaemonPing(outer)
		outer.Handle(basePath, otelmiddleware.StripPrefixPreservingPattern(prefix, mux))
		assembled = outer
	} else {
		s.registerDaemonPing(mux)
		assembled = mux
	}
	s.handler = otelhttp.NewHandler(assembled, "forge.http",
		otelhttp.WithFilter(otelTraceable(basePath)),
		otelhttp.WithSpanNameFormatter(otelmiddleware.OtelSpanName))

	// Exact entries removed from the TOML file while the daemon was stopped
	// must release their hidden-from-UI preferences; boot restores tracked
	// refs from provider snapshots before the server is constructed, so
	// exact-owned preferences resolve and survive the sweep.
	if err := s.settingsapi.ReconcileOrphanedRepoVisibility(s.bgCtx); err != nil {
		slog.Warn(
			"release orphaned hidden-from-UI preferences at startup",
			"err", err,
		)
	}

	return s
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

func (s *Server) bootstrapScript() string {
	safeBase, _ := json.Marshal(s.basePath)
	var builder strings.Builder
	builder.WriteString(`window.__BASE_PATH__=`)
	builder.WriteString(streamapi.ScriptSafe(string(safeBase)))
	builder.WriteString(`;`)
	// Preserve daemon-side worktree focus set by thin clients through the API.
	if awKey, set := s.ActiveWorktreeKey(); set {
		keyJSON, _ := json.Marshal(awKey)
		builder.WriteString(`window.__kenn_forge_active_worktree_key=`)
		builder.WriteString(streamapi.ScriptSafe(string(keyJSON)))
		builder.WriteString(`;`)
	}
	return builder.String()
}

// ServeHTTP implements http.Handler so Server can be used directly.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	logged := &statuslog.StatusLoggingResponseWriter{ResponseWriter: w}
	w = logged
	start := time.Now()
	slog.Debug(
		"http request started",
		"method", r.Method,
		"path", r.URL.Path,
		"query", authapi.RedactedQuery(r.URL),
		"remote_addr", r.RemoteAddr,
		"user_agent", r.UserAgent(),
	)
	defer func() {
		status := logged.Status
		if status == 0 {
			status = http.StatusOK
		}
		args := []any{
			"method", r.Method,
			"path", r.URL.Path,
			"query", authapi.RedactedQuery(r.URL),
			"status", status,
			"duration", time.Since(start).String(),
			"bytes", logged.Bytes,
			"remote_addr", r.RemoteAddr,
			"user_agent", r.UserAgent(),
		}
		if status >= http.StatusBadRequest {
			slog.Warn("http request failed", args...)
		} else {
			slog.Debug("http request completed", args...)
		}
	}()
	hostOpts := *s.hostOpts.Load()
	admission := s.daemonRequests.Admit(
		w, r, hostOpts, s.authapi.IsGatedAPIRequest(r),
	)
	if admission.Handled {
		return
	}
	if !admission.BypassProxyHostCheck && !hostapi.CheckHost(w, r, hostOpts) {
		return
	}
	if !s.streamapi.CheckHost(w, r) {
		return
	}
	if s.serveTailnetMCP(w, r) {
		return
	}
	if s.daemonRequests.RequireAPIAuth {
		if !s.options.ExecutionWorker &&
			(s.authapi.HandleAuthBootstrap(w, r) || s.handleLoginTicketBootstrap(w, r)) {
			return
		}
		if s.authapi.IsGatedAPIRequest(r) && !s.authorizeAPIRequest(w, r) {
			return
		}
	}
	if r.Method != http.MethodGet && s.streamapi.IsMutatingAPIRequest(r) {
		if !streamapi.CheckCrossOrigin(w, r, hostOpts.TrustReverseProxy) {
			return
		}
		if s.streamapi.IsMutatingDocsAPIRequest(r) && !authapi.IsLoopbackRemoteAddr(r.RemoteAddr) {
			routepolicy.WriteProblemResponse(w, httpapi.NewProblem(
				http.StatusForbidden,
				httpapi.CodeForbidden,
				"docs mutations require a loopback client",
				map[string]any{"reason": "loopbackOnly"},
			))
			return
		}
		if s.streamapi.IsTerminalClipboardAPIRequest(r) &&
			!authapi.IsLocalTerminalClipboardRequest(r, hostOpts.TrustReverseProxy) {
			routepolicy.WriteProblemResponse(w, httpapi.NewProblem(
				http.StatusForbidden,
				httpapi.CodeForbidden,
				"terminal clipboard writes require a local client",
				map[string]any{"reason": "loopbackOnly"},
			))
			return
		}
	}
	if r.Method == http.MethodGet && s.streamapi.IsDocsBrowseAPIRequest(r) && !authapi.IsLoopbackRemoteAddr(r.RemoteAddr) {
		routepolicy.WriteProblemResponse(w, httpapi.NewProblem(
			http.StatusForbidden,
			httpapi.CodeForbidden,
			"docs browse requires a loopback client",
			map[string]any{"reason": "loopbackOnly"},
		))
		return
	}
	if r.Method == http.MethodGet && s.streamapi.IsDocsReadAPIRequest(r) && !authapi.IsLoopbackRemoteAddr(r.RemoteAddr) {
		routepolicy.WriteProblemResponse(w, httpapi.NewProblem(
			http.StatusForbidden,
			httpapi.CodeForbidden,
			"docs reads require a loopback client",
			map[string]any{"reason": "loopbackOnly"},
		))
		return
	}
	if release, handled := s.admitProviderWrite(w, r); handled {
		return
	} else if release != nil {
		defer release()
	}
	if s.serveProviderRoute(w, r) {
		return
	}
	s.handler.ServeHTTP(w, r)
}

func (s *Server) serveProviderRoute(w http.ResponseWriter, r *http.Request) bool {
	if !s.providerRouteSpoke {
		return false
	}
	canonicalPath := s.authapi.CanonicalAPIPath(r)
	rule, ok := providerRouteRuleForRequest(r.Method, canonicalPath)
	if !ok || rule.Owner != routepolicy.ProviderHubOnly {
		return false
	}
	if !s.streamapi.FederationEnabled() || s.providerProxy == nil {
		routepolicy.WriteProblemResponse(w, httpapi.HubUnavailable(
			"provider data is unavailable because the federation hub cannot be reached",
		))
		return true
	}
	request := r.Clone(r.Context())
	requestURL := *r.URL
	requestURL.Path = r.URL.Path
	if s.basePath != "/" {
		prefix := strings.TrimSuffix(s.basePath, "/")
		requestURL.Path = strings.TrimPrefix(requestURL.Path, prefix)
	}
	requestURL.RawPath = canonicalPath
	request.URL = &requestURL
	s.providerProxy.ServeHTTP(w, request, rule)
	return true
}

// ListenAndServe starts the HTTP server on addr. Returns
// http.ErrServerClosed when stopped by Shutdown (matches net/http).
func (s *Server) ListenAndServe(addr string) error {
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", addr)
	if err != nil {
		return err
	}
	return s.Serve(ln)
}

// Serve accepts HTTP connections on the provided listener. Useful
// for tests and any caller that wants to own the listener lifetime.
// Returns http.ErrServerClosed when stopped by Shutdown.
func (s *Server) Serve(ln net.Listener) error {
	s.streamapi.SetAllowedHostsForListener(ln)
	s.streamapi.AdoptListenerHostPort(ln)
	srv := &http.Server{
		Handler:     s,
		ReadTimeout: 15 * time.Second,
		// WriteTimeout is 0 (disabled) because the roborev
		// proxy streams SSE/NDJSON responses that are
		// long-lived by design. A non-zero value would kill
		// /api/roborev/api/stream/events and /api/job/log
		// after the deadline.
		IdleTimeout: 60 * time.Second,
		ConnState:   s.streamapi.TrackHTTPConn,
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
	s.streamapi.SetAllowedHostsForListener(ln)
	s.streamapi.AdoptListenerHostPort(ln)
	s.bgMu.Lock()
	s.httpSrv = srv
	s.bgMu.Unlock()
}

func (s *Server) getVersion(
	_ context.Context, _ *struct{},
) (*versionOutput, error) {
	resp := &versionOutput{}
	resp.Body = versionOutputBody(s.buildInfo)
	return resp, nil
}
