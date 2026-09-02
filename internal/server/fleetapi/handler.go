// Package fleetapi owns the Fleet HTTP boundary and its background workers.
package fleetapi

import (
	"context"
	"net/http"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/federation"
	"go.kenn.io/forge/internal/federationauth"
	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/server/workspaceapi"
)

// ConfigSnapshot is the committed configuration state consumed by Fleet.
// It owns all nested slices so callers may safely reuse or mutate their input.
type ConfigSnapshot struct {
	Fleet               config.Fleet
	PlatformAuthConfig  config.Config
	PlatformAuthEnabled bool
	TmuxCommand         []string
}

// Event is Fleet's event-hub-neutral broadcast payload.
type Event struct {
	Type string
	Data any
}

// Deps contains Fleet's durable services and root-owned integration hooks.
type Deps struct {
	DB                          *db.DB
	Syncer                      *ghclient.Syncer
	Config                      ConfigSnapshot
	BasePath                    string
	BuildVersion                func() string
	Now                         func() time.Time
	LocalHandler                func() http.Handler
	Broadcast                   func(Event) uint64
	Generation                  func() uint64
	WorkspaceSnapshot           func(context.Context) (workspaceapi.FleetSnapshot, error)
	WorkspaceStatsSnapshot      func(context.Context) (workspaceapi.FleetSnapshot, error)
	QueueWorkspaceDeletion      func(string) error
	RuntimeSnapshot             func(string) workspaceapi.RuntimeSnapshot
	RevalidateDiffs             func()
	NodeID                      string
	FederationActive            bool
	FederationUnavailableReason string
	Credentials                 *federationauth.Store
	Enrollments                 *federation.Store
	FederationHTTPClient        *http.Client
	PersistMember               func(context.Context, config.FleetMember) error
	PersistHubBinding           func(context.Context, config.FleetHub) error
	RemoveMember                func(context.Context, string) error
	CancelEventStreams          func(string)
	// SubscriberCount reports how many event-stream clients are connected.
	// The fleet background monitors skip their expensive passes while it is
	// zero and no snapshot was read recently; nil means always run.
	SubscriberCount func() int
}

// Handler implements Fleet routes, caches, transports, and workers.
type Handler struct {
	db                          *db.DB
	syncer                      *ghclient.Syncer
	basePath                    string
	buildVersion                func() string
	now                         func() time.Time
	localHandler                func() http.Handler
	broadcast                   func(Event) uint64
	generation                  func() uint64
	workspaceSnapshot           func(context.Context) (workspaceapi.FleetSnapshot, error)
	queueWorkspaceDeletion      func(string) error
	runtimeSnapshot             func(string) workspaceapi.RuntimeSnapshot
	revalidateDiffs             func()
	nodeID                      string
	federationActive            bool
	federationUnavailableReason string
	credentials                 *federationauth.Store
	enrollments                 *federation.Store
	federationHTTPClient        *http.Client
	persistMember               func(context.Context, config.FleetMember) error
	persistHubBinding           func(context.Context, config.FleetHub) error
	removeMember                func(context.Context, string) error
	cancelEventStreams          func(string)
	enrollmentMu                sync.Mutex
	memberClientsMu             sync.Mutex
	memberClients               map[string]federationMemberClients

	configMu sync.RWMutex
	config   ConfigSnapshot

	fleetTmuxMonitor          *fleetTmuxMonitor
	fleetWorktreeDiscoverer   *fleetWorktreeDiscoverer
	fleetWorktreeStatsSampler *fleetWorktreeStatsSampler
	fleetPlatformAuthMonitor  *fleetPlatformAuthMonitor
	lifecycleMu               sync.Mutex
	lifecycleCtx              context.Context
	lifecycleCancel           context.CancelFunc
	lifecycleWG               sync.WaitGroup
	lifecycleStopping         bool
	lifecycleDone             chan struct{}
	lifecycleStarted          bool
	// snapshotDemandAt is the UnixNano time of the last snapshot read served
	// by this handler; it keeps the monitors active for hub-driven spokes
	// that never open a local event stream.
	snapshotDemandAt atomic.Int64
}

// New constructs a Fleet handler without starting its workers.
func New(deps Deps) *Handler {
	ctx, cancel := context.WithCancel(context.Background())
	now := deps.Now
	if now == nil {
		now = time.Now
	}
	h := &Handler{
		db:                          deps.DB,
		syncer:                      deps.Syncer,
		basePath:                    deps.BasePath,
		buildVersion:                deps.BuildVersion,
		now:                         now,
		localHandler:                deps.LocalHandler,
		broadcast:                   deps.Broadcast,
		generation:                  deps.Generation,
		workspaceSnapshot:           deps.WorkspaceSnapshot,
		queueWorkspaceDeletion:      deps.QueueWorkspaceDeletion,
		runtimeSnapshot:             deps.RuntimeSnapshot,
		revalidateDiffs:             deps.RevalidateDiffs,
		nodeID:                      deps.NodeID,
		federationActive:            deps.FederationActive,
		federationUnavailableReason: deps.FederationUnavailableReason,
		credentials:                 deps.Credentials,
		enrollments:                 deps.Enrollments,
		federationHTTPClient:        deps.FederationHTTPClient,
		persistMember:               deps.PersistMember,
		persistHubBinding:           deps.PersistHubBinding,
		removeMember:                deps.RemoveMember,
		cancelEventStreams:          deps.CancelEventStreams,
		config:                      cloneConfigSnapshot(deps.Config),
		lifecycleCtx:                ctx,
		lifecycleCancel:             cancel,
		lifecycleDone:               make(chan struct{}),
	}
	if h.federationHTTPClient == nil {
		h.federationHTTPClient = newFederationHTTPClient()
	}
	h.memberClients = make(map[string]federationMemberClients)
	var hasSubscribers func() bool
	if deps.SubscriberCount != nil {
		hasSubscribers = func() bool {
			return deps.SubscriberCount() > 0 || h.recentSnapshotDemand()
		}
	}
	h.fleetTmuxMonitor = newFleetTmuxMonitor(
		deps.Config.TmuxCommand,
		deps.Config.Fleet.Sessions.IncludeUnmanagedDetails,
		nil,
		hasSubscribers,
	)
	h.fleetWorktreeDiscoverer = newFleetWorktreeDiscoverer(deps.DB, hasSubscribers)
	h.fleetWorktreeStatsSampler = newFleetWorktreeStatsSampler(
		deps.DB, deps.WorkspaceStatsSnapshot, h.notifyWorktreeStatsChanged,
		hasSubscribers,
	)
	h.fleetPlatformAuthMonitor = newFleetPlatformAuthMonitor(
		h.snapshotPlatformAuthConfig,
	)
	return h
}

func cloneConfigSnapshot(in ConfigSnapshot) ConfigSnapshot {
	out := in
	out.Fleet.Members = slices.Clone(in.Fleet.Members)
	out.TmuxCommand = slices.Clone(in.TmuxCommand)
	out.PlatformAuthConfig.Repos = slices.Clone(in.PlatformAuthConfig.Repos)
	out.PlatformAuthConfig.Platforms = slices.Clone(in.PlatformAuthConfig.Platforms)
	out.PlatformAuthConfig.GitHubOwnerTokens = slices.Clone(
		in.PlatformAuthConfig.GitHubOwnerTokens,
	)
	out.PlatformAuthConfig.GitHubApps = slices.Clone(
		in.PlatformAuthConfig.GitHubApps,
	)
	return out
}

func (h *Handler) currentBuildVersion() string {
	if h == nil || h.buildVersion == nil {
		return ""
	}
	return h.buildVersion()
}

// ApplyConfig atomically publishes committed Fleet configuration.
func (h *Handler) ApplyConfig(snapshot ConfigSnapshot) {
	if h == nil {
		return
	}
	h.configMu.Lock()
	h.config = cloneConfigSnapshot(snapshot)
	h.configMu.Unlock()
}

func (h *Handler) configSnapshot() ConfigSnapshot {
	if h == nil {
		return ConfigSnapshot{}
	}
	h.configMu.RLock()
	defer h.configMu.RUnlock()
	return cloneConfigSnapshot(h.config)
}

// Enrollments returns a detached hub membership snapshot without any
// credential material.
func (h *Handler) Enrollments() []federation.Enrollment {
	if h == nil || h.enrollments == nil {
		return nil
	}
	return h.enrollments.List()
}

func (h *Handler) runBackground(run func(context.Context)) bool {
	if h == nil || run == nil {
		return false
	}
	h.lifecycleMu.Lock()
	if h.lifecycleStopping {
		h.lifecycleMu.Unlock()
		return false
	}
	h.lifecycleWG.Add(1)
	ctx := h.lifecycleCtx
	h.lifecycleMu.Unlock()
	go func() {
		defer h.lifecycleWG.Done()
		run(ctx)
	}()
	return true
}

func (h *Handler) stopBackground() {
	h.lifecycleMu.Lock()
	if h.lifecycleStopping {
		h.lifecycleMu.Unlock()
		return
	}
	h.lifecycleStopping = true
	h.lifecycleCancel()
	done := h.lifecycleDone
	h.lifecycleMu.Unlock()
	go func() {
		h.lifecycleWG.Wait()
		close(done)
	}()
}

// Start launches Fleet-owned workers after Workspace is available.
func (h *Handler) Start(parent context.Context, tmuxAvailable, disableMonitors bool) {
	if h == nil {
		return
	}
	if parent == nil {
		parent = context.Background()
	}
	h.lifecycleMu.Lock()
	if h.lifecycleStarted || h.lifecycleStopping {
		h.lifecycleMu.Unlock()
		return
	}
	h.lifecycleStarted = true
	h.lifecycleMu.Unlock()
	h.runBackground(func(ctx context.Context) {
		select {
		case <-parent.Done():
			h.stopBackground()
		case <-ctx.Done():
		}
	})
	if tmuxAvailable && h.workspaceSnapshot != nil {
		h.runBackground(h.fleetTmuxMonitor.run)
	}
	if disableMonitors {
		return
	}
	h.runBackground(h.fleetWorktreeDiscoverer.run)
	h.runBackground(h.fleetWorktreeStatsSampler.run)
	h.runBackground(h.fleetPlatformAuthMonitor.run)
}

// noteSnapshotDemand records that a client read a snapshot, which counts as
// demand for fresh monitor data even without an event-stream subscription.
func (h *Handler) noteSnapshotDemand() {
	if h == nil {
		return
	}
	h.snapshotDemandAt.Store(h.now().UnixNano())
}

// recentSnapshotDemand reports whether a snapshot was read within the demand
// window.
func (h *Handler) recentSnapshotDemand() bool {
	if h == nil {
		return false
	}
	at := h.snapshotDemandAt.Load()
	if at == 0 {
		return false
	}
	return h.now().Sub(time.Unix(0, at)) < fleetMonitorDemandWindow
}

// RefreshWorktreeStats refreshes the cached git statistics for one worktree.
func (h *Handler) RefreshWorktreeStats(
	ctx context.Context, path, defaultBranch string,
) error {
	return h.fleetWorktreeStatsSampler.refreshWorktreeStats(ctx, path, defaultBranch)
}

// RefreshProjectInventory refreshes one registered project's worktree rows.
func (h *Handler) RefreshProjectInventory(ctx context.Context, projectID string) error {
	project, err := h.db.GetProjectByID(ctx, projectID)
	if err != nil {
		return err
	}
	h.fleetWorktreeDiscoverer.refreshProject(ctx, project.ID, project.LocalPath)
	return nil
}

// RecomputeWorktreeLinks recomputes provider-aware branch links immediately.
func (h *Handler) RecomputeWorktreeLinks(ctx context.Context) {
	h.recomputeWorktreeLinksNow(ctx)
}

// SelfKey returns Fleet's stable local daemon identity.
func (h *Handler) SelfKey(localHostname string) string {
	return h.fleetSelfKey(localHostname)
}

// Shutdown stops Fleet workers and waits within ctx. Calls are idempotent and
// a later caller may continue waiting after an earlier deadline expires.
func (h *Handler) Shutdown(ctx context.Context) error {
	if h == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	h.stopBackground()
	select {
	case <-h.lifecycleDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
