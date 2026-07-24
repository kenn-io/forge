// Package fleetapi owns the Fleet HTTP boundary and its background workers.
package fleetapi

import (
	"context"
	"net/http"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"go.kenn.io/middleman/internal/config"
	"go.kenn.io/middleman/internal/db"
	ghclient "go.kenn.io/middleman/internal/github"
	"go.kenn.io/middleman/internal/server/workspaceapi"
)

// ConfigSnapshot is the committed configuration state consumed by Fleet.
// It owns all nested slices so callers may safely reuse or mutate their input.
type ConfigSnapshot struct {
	Fleet               config.Fleet
	PlatformAuthConfig  config.Config
	PlatformAuthEnabled bool
	TmuxCommand         []string
	SSHSocketDir        string
}

// Event is Fleet's event-hub-neutral broadcast payload.
type Event struct {
	Type string
	Data any
}

// Deps contains Fleet's durable services and root-owned integration hooks.
type Deps struct {
	DB                *db.DB
	Syncer            *ghclient.Syncer
	Config            ConfigSnapshot
	BasePath          string
	BuildVersion      func() string
	Now               func() time.Time
	LocalHandler      func() http.Handler
	Broadcast         func(Event) uint64
	Generation        func() uint64
	WorkspaceSnapshot func(context.Context) (workspaceapi.FleetSnapshot, error)
	RuntimeSnapshot   func(string) workspaceapi.RuntimeSnapshot
	RevalidateDiffs   func()
}

// Handler implements Fleet routes, caches, transports, and workers.
type Handler struct {
	db                *db.DB
	syncer            *ghclient.Syncer
	basePath          string
	buildVersion      func() string
	now               func() time.Time
	localHandler      func() http.Handler
	broadcast         func(Event) uint64
	generation        func() uint64
	workspaceSnapshot func(context.Context) (workspaceapi.FleetSnapshot, error)
	runtimeSnapshot   func(string) workspaceapi.RuntimeSnapshot
	revalidateDiffs   func()

	configMu sync.RWMutex
	config   ConfigSnapshot

	fleetTmuxMonitor          *fleetTmuxMonitor
	fleetWorktreeDiscoverer   *fleetWorktreeDiscoverer
	fleetWorktreeStatsSampler *fleetWorktreeStatsSampler
	fleetPlatformAuthMonitor  *fleetPlatformAuthMonitor
	sshFleet                  *sshFleetTransport

	lifecycleMu       sync.Mutex
	lifecycleCtx      context.Context
	lifecycleCancel   context.CancelFunc
	lifecycleWG       sync.WaitGroup
	lifecycleStopping bool
	lifecycleDone     chan struct{}
	lifecycleStarted  bool
	shutdownOnce      sync.Once
}

// New constructs a Fleet handler without starting its workers.
func New(deps Deps) *Handler {
	ctx, cancel := context.WithCancel(context.Background())
	now := deps.Now
	if now == nil {
		now = time.Now
	}
	h := &Handler{
		db:                deps.DB,
		syncer:            deps.Syncer,
		basePath:          deps.BasePath,
		buildVersion:      deps.BuildVersion,
		now:               now,
		localHandler:      deps.LocalHandler,
		broadcast:         deps.Broadcast,
		generation:        deps.Generation,
		workspaceSnapshot: deps.WorkspaceSnapshot,
		runtimeSnapshot:   deps.RuntimeSnapshot,
		revalidateDiffs:   deps.RevalidateDiffs,
		config:            cloneConfigSnapshot(deps.Config),
		lifecycleCtx:      ctx,
		lifecycleCancel:   cancel,
		lifecycleDone:     make(chan struct{}),
	}
	h.fleetTmuxMonitor = newFleetTmuxMonitor(
		deps.Config.TmuxCommand,
		deps.Config.Fleet.Sessions.IncludeUnmanagedDetails,
		nil,
	)
	h.fleetWorktreeDiscoverer = newFleetWorktreeDiscoverer(deps.DB)
	h.fleetWorktreeStatsSampler = newFleetWorktreeStatsSampler(
		deps.DB, h.notifyWorktreeStatsChanged,
	)
	h.fleetPlatformAuthMonitor = newFleetPlatformAuthMonitor(
		h.snapshotPlatformAuthConfig,
	)
	if len(deps.Config.Fleet.SSHPeers) > 0 && deps.Config.SSHSocketDir != "" {
		h.sshFleet = newSSHFleetTransport(
			filepath.Clean(deps.Config.SSHSocketDir),
			deps.Config.Fleet.SSHPeers,
			h.broadcastEvent,
		)
	}
	return h
}

func cloneConfigSnapshot(in ConfigSnapshot) ConfigSnapshot {
	out := in
	out.Fleet.Peers = slices.Clone(in.Fleet.Peers)
	out.Fleet.SSHPeers = slices.Clone(in.Fleet.SSHPeers)
	out.TmuxCommand = slices.Clone(in.TmuxCommand)
	out.PlatformAuthConfig.Repos = slices.Clone(in.PlatformAuthConfig.Repos)
	out.PlatformAuthConfig.Platforms = slices.Clone(in.PlatformAuthConfig.Platforms)
	return out
}

func (h *Handler) broadcastEvent(event Event) uint64 {
	if h == nil || h.broadcast == nil {
		return 0
	}
	return h.broadcast(event)
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
		h.shutdownOnce.Do(func() { h.sshFleet.shutdown() })
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

// SelfKey returns Fleet's configured or hostname-derived local host key.
func (h *Handler) SelfKey(localHostname string) string {
	return h.fleetSelfKey(localHostname)
}

// SSHPeers returns the peer set owned by the running SSH transport.
func (h *Handler) SSHPeers() []config.FleetSSHPeer {
	if h == nil || h.sshFleet == nil {
		return nil
	}
	return h.sshFleet.snapshotPeers()
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
