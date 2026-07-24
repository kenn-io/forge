// Package workspaceapi owns the workspace and local-project HTTP boundary.
package workspaceapi

import (
	"context"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"go.kenn.io/middleman/internal/db"
	ghclient "go.kenn.io/middleman/internal/github"
	"go.kenn.io/middleman/internal/server/httpapi"
	"go.kenn.io/middleman/internal/workspace"
	"go.kenn.io/middleman/internal/workspace/localruntime"
)

// Event is the workspace domain's event-hub-neutral broadcast payload.
type Event struct {
	Type string
	Data any
}

// RecordedEvent is an Event with its event-hub generation.
type RecordedEvent struct {
	ID uint64
	Event
}

type eventHubAdapter struct {
	broadcast  func(Event) uint64
	subscribe  func(context.Context, bool) (<-chan RecordedEvent, <-chan struct{})
	generation func() uint64
}

func (h *eventHubAdapter) Broadcast(event Event) uint64 {
	if h == nil || h.broadcast == nil {
		return 0
	}
	return h.broadcast(event)
}

func (h *eventHubAdapter) Subscribe(
	ctx context.Context, injectCached bool,
) (<-chan RecordedEvent, <-chan struct{}) {
	if h != nil && h.subscribe != nil {
		return h.subscribe(ctx, injectCached)
	}
	events := make(chan RecordedEvent)
	done := make(chan struct{})
	close(events)
	close(done)
	return events, done
}

func (h *eventHubAdapter) Generation() uint64 {
	if h == nil || h.generation == nil {
		return 0
	}
	return h.generation()
}

type workspaceDiffEventData struct {
	WorkspaceID string `json:"workspace_id"`
	Revision    uint64 `json:"revision"`
	Version     string `json:"version"`
}

// Deps contains the state and root-owned integration hooks used by the
// workspace and project boundary. Hooks keep this package independent from
// the root server package while preserving the shared shutdown and event
// ordering owned by the composition root.
type Deps struct {
	DB          *db.DB
	Resolver    *httpapi.RepositoryResolver
	Syncer      *ghclient.Syncer
	Config      ConfigSnapshot
	Workspaces  *workspace.Manager
	Runtime     *localruntime.Manager
	TmuxCommand []string
	Now         func() time.Time
	Broadcast   func(Event) uint64
	Subscribe   func(context.Context, bool) (<-chan RecordedEvent, <-chan struct{})
	Generation  func() uint64

	RecomputeWorktreeLinks  func(context.Context)
	RefreshWorktreeStats    func(context.Context, string, string) error
	RefreshProjectInventory func(context.Context, string) error
	LookupRepo              func(context.Context, string, string, string, string) (*db.Repo, error)
	EnqueueDetailSync       func(
		string, []any, func(context.Context) error, func(context.Context),
	) bool
}

// Handler implements both the workspace and local-project services so their
// Git-heavy tests and process limits remain in one package and test binary.
type Handler struct {
	db         *db.DB
	resolver   *httpapi.RepositoryResolver
	syncer     *ghclient.Syncer
	configMu   sync.RWMutex
	config     ConfigSnapshot
	workspaces *workspace.Manager
	runtime    *localruntime.Manager
	tmuxCmd    []string
	now        func() time.Time
	broadcast  func(Event) uint64
	subscribe  func(context.Context, bool) (<-chan RecordedEvent, <-chan struct{})
	generation func() uint64

	recomputeWorktreeLinks         func(context.Context)
	refreshWorktreeStats           func(context.Context, string, string) error
	refreshProjectInventory        func(context.Context, string) error
	lookupRepo                     func(context.Context, string, string, string, string) (*db.Repo, error)
	enqueueDetailSync              func(string, []any, func(context.Context) error, func(context.Context)) bool
	hub                            *eventHubAdapter
	workspacePRMonitor             *workspace.PRMonitor
	workspacePushedHeadObserver    *workspace.PushedHeadObserver
	workspaceDiffCache             *workspaceDiffCache
	tmuxActivity                   *tmuxActivityTracker
	workspaceEnrichmentMu          sync.Mutex
	workspaceEnrichmentCache       map[string]workspaceEnrichmentCacheEntry
	workspaceEnrichmentInFlight    map[string]uint64
	workspaceEnrichmentGenerations map[string]uint64
	workspaceEnrichmentPending     map[string]workspaceEnrichmentJob
	workspaceEnrichmentWorkers     int
	workspaceEnrichmentSlots       chan struct{}
	workspaceEnrichmentDisabled    bool
	workspaceTmuxPrunedAt          time.Time
	workspaceTmuxPrunePending      bool
	workspaceTmuxPruneInFlight     bool
	lifecycleMu                    sync.Mutex
	lifecycleCtx                   context.Context
	lifecycleCancel                context.CancelFunc
	lifecycleWG                    sync.WaitGroup
	lifecycleStarted               bool
	lifecycleStopping              bool
	lifecycleDone                  chan struct{}
}

// New creates the workspace and project handler.
func New(deps Deps) *Handler {
	now := deps.Now
	if now == nil {
		now = time.Now
	}
	lifecycleCtx, lifecycleCancel := context.WithCancel(context.Background())
	h := &Handler{
		db:                             deps.DB,
		resolver:                       deps.Resolver,
		syncer:                         deps.Syncer,
		config:                         cloneConfigSnapshot(deps.Config),
		workspaces:                     deps.Workspaces,
		runtime:                        deps.Runtime,
		tmuxCmd:                        slices.Clone(deps.TmuxCommand),
		now:                            now,
		broadcast:                      deps.Broadcast,
		subscribe:                      deps.Subscribe,
		generation:                     deps.Generation,
		recomputeWorktreeLinks:         deps.RecomputeWorktreeLinks,
		refreshWorktreeStats:           deps.RefreshWorktreeStats,
		refreshProjectInventory:        deps.RefreshProjectInventory,
		lookupRepo:                     deps.LookupRepo,
		enqueueDetailSync:              deps.EnqueueDetailSync,
		hub:                            &eventHubAdapter{broadcast: deps.Broadcast, subscribe: deps.Subscribe, generation: deps.Generation},
		workspaceEnrichmentCache:       make(map[string]workspaceEnrichmentCacheEntry),
		workspaceEnrichmentInFlight:    make(map[string]uint64),
		workspaceEnrichmentGenerations: make(map[string]uint64),
		workspaceEnrichmentPending:     make(map[string]workspaceEnrichmentJob),
		workspaceEnrichmentSlots:       make(chan struct{}, tmuxProbeMaxConcurrency),
		tmuxActivity:                   newTmuxActivityTracker(nil),
		lifecycleCtx:                   lifecycleCtx,
		lifecycleCancel:                lifecycleCancel,
		lifecycleDone:                  make(chan struct{}),
	}
	if deps.DB != nil && deps.Workspaces != nil {
		h.workspacePRMonitor = workspace.NewPRMonitor(deps.DB)
		h.workspacePushedHeadObserver = workspace.NewPushedHeadObserver(deps.DB)
	}
	h.workspaceDiffCache = newWorkspaceDiffCache(lifecycleCtx, workspaceDiffCacheDeps{
		onReady: func(workspaceID string, revision uint64, version string) {
			h.hub.Broadcast(Event{Type: "workspace_diff_ready", Data: workspaceDiffEventData{WorkspaceID: workspaceID, Revision: revision, Version: version}})
		},
		onChanged: func(workspaceID string, revision uint64, version string) {
			h.hub.Broadcast(Event{Type: "workspace_diff_changed", Data: workspaceDiffEventData{WorkspaceID: workspaceID, Revision: revision, Version: version}})
		},
	})
	return h
}

// Workspaces returns the shared workspace service boundary.
func (h *Handler) Workspaces() *Handler { return h }

// Projects returns the shared project service boundary.
func (h *Handler) Projects() *Handler { return h }

// RevalidateSelectedDiffs schedules validation for active workspace diff
// leases after worktree stats change.
func (h *Handler) RevalidateSelectedDiffs() {
	if h != nil && h.workspaceDiffCache != nil {
		h.workspaceDiffCache.RevalidateSelected()
	}
}

// SetEnrichmentDisabled disables background enrichment. It is intended for
// deterministic integration fixtures that exercise unrelated workspace paths.
func (h *Handler) SetEnrichmentDisabled(disabled bool) {
	if h != nil {
		h.workspaceEnrichmentDisabled = disabled
	}
}

// SetNow overrides the handler clock. It is used by deterministic tests and
// applies to observers, caches, and response enrichment.
func (h *Handler) SetNow(now func() time.Time) {
	if h != nil && now != nil {
		h.now = now
	}
}

// SetTmuxActivityClock replaces the activity tracker with the supplied clock.
func (h *Handler) SetTmuxActivityClock(now func() time.Time) {
	if h != nil {
		h.tmuxActivity = newTmuxActivityTracker(now)
	}
}

// EnrichmentSettled reports whether a workspace has cached enrichment and no
// reconciliation currently in flight.
func (h *Handler) EnrichmentSettled(workspaceID string) bool {
	if h == nil {
		return false
	}
	h.workspaceEnrichmentMu.Lock()
	defer h.workspaceEnrichmentMu.Unlock()
	_, cached := h.workspaceEnrichmentCache[workspaceID]
	_, inFlight := h.workspaceEnrichmentInFlight[workspaceID]
	return cached && !inFlight
}

// EnrichmentIdle reports whether no reconciliation is in flight.
func (h *Handler) EnrichmentIdle() bool {
	h.workspaceEnrichmentMu.Lock()
	defer h.workspaceEnrichmentMu.Unlock()
	return len(h.workspaceEnrichmentInFlight) == 0
}

// ClearEnrichmentCache clears cached reconciliation responses.
func (h *Handler) ClearEnrichmentCache() {
	h.workspaceEnrichmentMu.Lock()
	clear(h.workspaceEnrichmentCache)
	h.workspaceEnrichmentMu.Unlock()
}

// EnrichmentCached reports whether every workspace has a cached response.
func (h *Handler) EnrichmentCached(workspaceIDs ...string) bool {
	h.workspaceEnrichmentMu.Lock()
	defer h.workspaceEnrichmentMu.Unlock()
	for _, workspaceID := range workspaceIDs {
		if _, ok := h.workspaceEnrichmentCache[workspaceID]; !ok {
			return false
		}
	}
	return true
}

// HoldEnrichmentSlots occupies every worker slot until release is called.
func (h *Handler) HoldEnrichmentSlots() func() {
	for range cap(h.workspaceEnrichmentSlots) {
		h.workspaceEnrichmentSlots <- struct{}{}
	}
	return func() {
		for range cap(h.workspaceEnrichmentSlots) {
			<-h.workspaceEnrichmentSlots
		}
	}
}

// EnrichmentPending reports queued or active reconciliation for a workspace.
func (h *Handler) EnrichmentPending(workspaceID string) bool {
	h.workspaceEnrichmentMu.Lock()
	defer h.workspaceEnrichmentMu.Unlock()
	_, pending := h.workspaceEnrichmentPending[workspaceID]
	_, inFlight := h.workspaceEnrichmentInFlight[workspaceID]
	return pending || inFlight
}

// EnrichmentQueued reports a queued job that has not started running.
func (h *Handler) EnrichmentQueued(workspaceID string) bool {
	h.workspaceEnrichmentMu.Lock()
	defer h.workspaceEnrichmentMu.Unlock()
	_, pending := h.workspaceEnrichmentPending[workspaceID]
	_, inFlight := h.workspaceEnrichmentInFlight[workspaceID]
	return pending && !inFlight
}

// EnrichmentCacheCountSettled reports cache size after all workers settle.
func (h *Handler) EnrichmentCacheCountSettled(want int) bool {
	h.workspaceEnrichmentMu.Lock()
	defer h.workspaceEnrichmentMu.Unlock()
	return len(h.workspaceEnrichmentCache) == want &&
		len(h.workspaceEnrichmentInFlight) == 0
}

// RunPushedHeadObserverPass runs one observer pass synchronously.
func (h *Handler) RunPushedHeadObserverPass(ctx context.Context) {
	if h != nil {
		h.runWorkspacePushedHeadObserverPass(ctx)
	}
}

// SetPushedHeadObserverNow overrides the observer clock for deterministic
// convergence tests.
func (h *Handler) SetPushedHeadObserverNow(now func() time.Time) {
	if h != nil && h.workspacePushedHeadObserver != nil {
		h.workspacePushedHeadObserver.SetNowForTest(now)
	}
}

// RunPRMonitorOnce runs one association reconciliation pass.
func (h *Handler) RunPRMonitorOnce(
	ctx context.Context,
) ([]workspace.PRAssociationUpdate, error) {
	if h == nil || h.workspacePRMonitor == nil {
		return nil, nil
	}
	return h.workspacePRMonitor.RunOnce(ctx)
}

// RunPRMonitorPass runs one pass and emits its invalidation events.
func (h *Handler) RunPRMonitorPass(ctx context.Context) {
	if h != nil {
		h.runWorkspacePRMonitorPass(ctx)
	}
}

func (h *Handler) enqueueDetailSyncWithCompletion(
	key string,
	attrs []any,
	run func(context.Context) error,
	onSuccess func(context.Context),
) bool {
	if h.enqueueDetailSync == nil {
		return false
	}
	return h.enqueueDetailSync(key, attrs, run, onSuccess)
}

// Register registers workspace and local-project REST operations.
func (s *Handler) Register(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID:   "create-workspace",
		Method:        http.MethodPost,
		Path:          "/workspaces",
		DefaultStatus: http.StatusAccepted,
		Summary:       "Create workspace",
		Tags:          []string{"Workspaces"},
	}, s.createWorkspace)
	huma.Get(api, "/workspaces", s.listWorkspaces,
		httpapi.DocumentOperation("list-workspaces", "List workspaces", "Workspaces"))
	huma.Get(api, "/workspaces/{id}", s.getWorkspace,
		httpapi.DocumentOperation("get-workspace", "Get workspace", "Workspaces"))
	huma.Get(api, "/workspaces/{id}/commits", s.getWorkspaceCommits,
		httpapi.DocumentOperation("get-workspace-commits", "Get workspace commits", "Workspaces"))
	huma.Get(api, "/workspaces/{id}/diff", s.getWorkspaceDiff,
		httpapi.DocumentOperation("get-workspace-diff", "Get workspace diff", "Workspaces"))
	huma.Get(api, "/workspaces/{id}/file-preview", s.getWorkspaceFilePreview,
		httpapi.DocumentOperation("get-workspace-file-preview", "Get workspace file preview", "Workspaces"))
	huma.Get(api, "/workspaces/{id}/files", s.getWorkspaceFiles,
		httpapi.DocumentOperation("get-workspace-files", "Get workspace files", "Workspaces"))
	huma.Get(api, "/workspaces/{id}/diff/watch", s.watchWorkspaceDiff,
		httpapi.DocumentOperation("watch-workspace-diff", "Watch selected workspace diff", "Workspaces"))
	huma.Register(api, huma.Operation{
		OperationID:   "retry-workspace",
		Method:        http.MethodPost,
		Path:          "/workspaces/{id}/retry",
		DefaultStatus: http.StatusAccepted,
		Summary:       "Retry workspace",
		Tags:          []string{"Workspaces"},
	}, s.retryWorkspace)
	huma.Register(api, huma.Operation{
		OperationID: "refresh-workspace",
		Method:      http.MethodPost,
		Path:        "/workspaces/{id}/refresh",
		Summary:     "Refresh workspace",
		Tags:        []string{"Workspaces"},
	}, s.refreshWorkspace)
	huma.Register(api, huma.Operation{
		OperationID: "push-workspace-branch",
		Method:      http.MethodPost,
		Path:        "/workspaces/{id}/push",
		Summary:     "Push workspace branch",
		Tags:        []string{"Workspaces"},
	}, s.pushWorkspaceBranch)
	huma.Register(api, huma.Operation{
		OperationID: "pull-workspace-branch",
		Method:      http.MethodPost,
		Path:        "/workspaces/{id}/pull",
		Summary:     "Pull workspace branch",
		Tags:        []string{"Workspaces"},
	}, s.pullWorkspaceBranch)
	huma.Register(api, huma.Operation{
		OperationID:   "reveal-workspace",
		Method:        http.MethodPost,
		Path:          "/workspaces/{id}/reveal",
		DefaultStatus: http.StatusNoContent,
		Summary:       "Reveal workspace folder",
		Tags:          []string{"Workspaces"},
	}, s.revealWorkspace)
	huma.Register(api, huma.Operation{
		OperationID: "get-workspace-runtime",
		Method:      http.MethodGet,
		Path:        "/workspaces/{id}/runtime",
		Summary:     "Get workspace runtime",
		Tags:        []string{"Workspaces"},
	}, s.getWorkspaceRuntime)
	huma.Register(api, huma.Operation{
		OperationID: "launch-workspace-runtime-session",
		Method:      http.MethodPost,
		Path:        "/workspaces/{id}/runtime/sessions",
		Summary:     "Launch workspace runtime session",
		Tags:        []string{"Workspaces"},
	}, s.launchWorkspaceRuntimeSession)
	huma.Register(api, huma.Operation{
		OperationID:   "stop-workspace-runtime-session",
		Method:        http.MethodDelete,
		Path:          "/workspaces/{id}/runtime/sessions/{session_key}",
		DefaultStatus: http.StatusNoContent,
		Summary:       "Stop workspace runtime session",
		Tags:          []string{"Workspaces"},
	}, s.stopWorkspaceRuntimeSession)
	huma.Register(api, huma.Operation{
		OperationID: "get-workspace-runtime-session-attach-spec",
		Method:      http.MethodGet,
		Path:        "/workspaces/{id}/runtime/sessions/{session_key}/attach-spec",
		Summary:     "Get workspace runtime session attach spec",
		Tags:        []string{"Workspaces"},
	}, s.getWorkspaceRuntimeSessionAttachSpec)
	huma.Register(api, huma.Operation{
		OperationID: "rename-workspace-runtime-session",
		Method:      http.MethodPatch,
		Path:        "/workspaces/{id}/runtime/sessions/{session_key}",
		Summary:     "Rename workspace runtime session",
		Tags:        []string{"Workspaces"},
	}, s.renameWorkspaceRuntimeSession)
	huma.Register(api, huma.Operation{
		OperationID:   "delete-workspace",
		Method:        http.MethodDelete,
		Path:          "/workspaces/{id}",
		DefaultStatus: http.StatusNoContent,
		Summary:       "Delete workspace",
		Tags:          []string{"Workspaces"},
	}, s.deleteWorkspace)

	huma.Register(api, huma.Operation{
		OperationID:   "register-project",
		Method:        http.MethodPost,
		Path:          "/projects",
		DefaultStatus: http.StatusCreated,
		Summary:       "Register project",
		Tags:          []string{"Projects"},
	}, s.registerProject)
	huma.Register(api, huma.Operation{
		OperationID: "list-projects",
		Method:      http.MethodGet,
		Path:        "/projects",
		Summary:     "List projects",
		Tags:        []string{"Projects"},
	}, s.listProjects)
	huma.Register(api, huma.Operation{
		OperationID: "get-project",
		Method:      http.MethodGet,
		Path:        "/projects/{project_id}",
		Summary:     "Get project",
		Tags:        []string{"Projects"},
	}, s.getProject)
	huma.Register(api, huma.Operation{
		OperationID:   "delete-project",
		Method:        http.MethodDelete,
		Path:          "/projects/{project_id}",
		DefaultStatus: http.StatusNoContent,
		Summary:       "Delete project",
		Tags:          []string{"Projects"},
	}, s.deleteProject)
	huma.Register(api, huma.Operation{
		OperationID:   "register-worktree",
		Method:        http.MethodPost,
		Path:          "/projects/{project_id}/worktrees",
		DefaultStatus: http.StatusCreated,
		Summary:       "Register worktree",
		Tags:          []string{"Projects"},
	}, s.registerWorktree)
	huma.Register(api, huma.Operation{
		OperationID:   "delete-worktree",
		Method:        http.MethodDelete,
		Path:          "/projects/{project_id}/worktrees/{worktree_id}",
		DefaultStatus: http.StatusNoContent,
		Summary:       "Delete worktree",
		Tags:          []string{"Projects"},
	}, s.deleteProjectWorktree)
	huma.Register(api, huma.Operation{
		OperationID:   "remove-worktree",
		Method:        http.MethodPost,
		Path:          "/projects/{project_id}/worktrees/{worktree_id}/delete",
		DefaultStatus: http.StatusNoContent,
		Summary:       "Remove worktree (optionally from disk)",
		Tags:          []string{"Projects"},
	}, s.removeProjectWorktree)
	huma.Register(api, huma.Operation{
		OperationID:   "create-worktree-from-merge-request",
		Method:        http.MethodPost,
		Path:          "/projects/{project_id}/worktrees/from-merge-request",
		DefaultStatus: http.StatusCreated,
		Summary:       "Create worktree from a merge request head",
		Tags:          []string{"Projects"},
	}, s.createProjectWorktreeFromMergeRequest)
	huma.Register(api, huma.Operation{
		OperationID:   "remove-stale-worktree",
		Method:        http.MethodPost,
		Path:          "/worktrees/remove-stale",
		DefaultStatus: http.StatusOK,
		Summary:       "Remove a stale worktree",
		Tags:          []string{"Projects"},
	}, s.removeStaleWorktree)
	huma.Register(api, huma.Operation{
		OperationID: "set-worktree-hidden",
		Method:      http.MethodPut,
		Path:        "/projects/{project_id}/worktrees/{worktree_id}/hidden",
		Summary:     "Set worktree hidden",
		Tags:        []string{"Projects"},
	}, s.setProjectWorktreeHidden)
	huma.Register(api, huma.Operation{
		OperationID: "set-worktree-session-backend",
		Method:      http.MethodPut,
		Path:        "/projects/{project_id}/worktrees/{worktree_id}/session-backend",
		Summary:     "Set worktree session backend",
		Tags:        []string{"Projects"},
	}, s.setProjectWorktreeSessionBackend)
	// OperationID stays generic ("links", not "linked-issues"): the project
	// registry's operation surface must not bake issue/PR-tracker terms into
	// op IDs (enforced by TestW1SliceAGate). The path and body field keep the
	// precise "linked issue numbers" naming; the op ID does not.
	huma.Register(api, huma.Operation{
		OperationID: "set-worktree-links",
		Method:      http.MethodPut,
		Path:        "/projects/{project_id}/worktrees/{worktree_id}/linked-issues",
		Summary:     "Set worktree linked issues",
		Tags:        []string{"Projects"},
	}, s.setProjectWorktreeLinkedIssues)
	huma.Register(api, huma.Operation{
		OperationID: "refresh-worktree-stats",
		Method:      http.MethodPost,
		Path:        "/projects/{project_id}/worktrees/{worktree_id}/refresh-stats",
		Summary:     "Refresh worktree git stats",
		Tags:        []string{"Projects"},
	}, s.refreshProjectWorktreeStats)
	huma.Register(api, huma.Operation{
		OperationID: "list-worktrees",
		Method:      http.MethodGet,
		Path:        "/projects/{project_id}/worktrees",
		Summary:     "List worktrees",
		Tags:        []string{"Projects"},
	}, s.listWorktrees)
	huma.Register(api, huma.Operation{
		OperationID: "list-launch-targets",
		Method:      http.MethodGet,
		Path:        "/projects/{project_id}/launch-targets",
		Summary:     "List launch targets",
		Tags:        []string{"Projects"},
	}, s.listLaunchTargets)
	huma.Register(api, huma.Operation{
		OperationID:   "clone-project",
		Method:        http.MethodPost,
		Path:          "/projects/clone",
		DefaultStatus: http.StatusCreated,
		Summary:       "Clone a repository and register it as a project",
		Tags:          []string{"Projects"},
	}, s.cloneProject)
	huma.Register(api, huma.Operation{
		OperationID: "list-project-branches",
		Method:      http.MethodGet,
		Path:        "/projects/{project_id}/branches",
		Summary:     "List project repository branches",
		Tags:        []string{"Projects"},
	}, s.listProjectBranches)
	huma.Register(api, huma.Operation{
		OperationID: "inspect-project-worktree",
		Method:      http.MethodGet,
		Path:        "/projects/{project_id}/worktrees/{worktree_id}/inspect",
		Summary:     "Inspect project worktree",
		Tags:        []string{"Projects"},
	}, s.inspectProjectWorktree)
	huma.Register(api, huma.Operation{
		OperationID: "get-project-worktree-runtime",
		Method:      http.MethodGet,
		Path:        "/projects/{project_id}/worktrees/{worktree_id}/runtime",
		Summary:     "Get project worktree runtime",
		Tags:        []string{"Projects"},
	}, s.getProjectWorktreeRuntime)
	huma.Register(api, huma.Operation{
		OperationID: "ensure-project-worktree-runtime-shell",
		Method:      http.MethodPost,
		Path:        "/projects/{project_id}/worktrees/{worktree_id}/runtime/shell",
		Summary:     "Ensure project worktree shell",
		Tags:        []string{"Projects"},
	}, s.ensureProjectWorktreeRuntimeShell)
	huma.Register(api, huma.Operation{
		OperationID: "launch-project-worktree-runtime-session",
		Method:      http.MethodPost,
		Path:        "/projects/{project_id}/worktrees/{worktree_id}/runtime/sessions",
		Summary:     "Launch project worktree runtime session",
		Tags:        []string{"Projects"},
	}, s.launchProjectWorktreeRuntimeSession)
	huma.Register(api, huma.Operation{
		OperationID:   "stop-project-worktree-runtime-session",
		Method:        http.MethodDelete,
		Path:          "/projects/{project_id}/worktrees/{worktree_id}/runtime/sessions/{session_key}",
		DefaultStatus: http.StatusNoContent,
		Summary:       "Stop project worktree runtime session",
		Tags:          []string{"Projects"},
	}, s.stopProjectWorktreeRuntimeSession)
	huma.Register(api, huma.Operation{
		OperationID: "get-project-worktree-runtime-session-attach-spec",
		Method:      http.MethodGet,
		Path:        "/projects/{project_id}/worktrees/{worktree_id}/runtime/sessions/{session_key}/attach-spec",
		Summary:     "Get project worktree runtime session attach spec",
		Tags:        []string{"Projects"},
	}, s.getProjectWorktreeRuntimeSessionAttachSpec)
}
