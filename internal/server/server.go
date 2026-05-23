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
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"go.kenn.io/middleman/internal/config"
	"go.kenn.io/middleman/internal/configwatch"
	"go.kenn.io/middleman/internal/db"
	"go.kenn.io/middleman/internal/gitclone"
	ghclient "go.kenn.io/middleman/internal/github"
	"go.kenn.io/middleman/internal/ptyowner"
	ptyownerruntime "go.kenn.io/middleman/internal/ptyowner/runtime"
	"go.kenn.io/middleman/internal/workspace"
	"go.kenn.io/middleman/internal/workspace/localruntime"
)

type EmbedConfig struct {
	Theme *ThemeConfig `json:"theme,omitempty"`
	UI    *UIConfig    `json:"ui,omitempty"`
}

type ThemeConfig struct {
	Mode   string            `json:"mode,omitempty"`
	Colors map[string]string `json:"colors,omitempty"`
	Fonts  map[string]string `json:"fonts,omitempty"`
	Radii  map[string]string `json:"radii,omitempty"`
}

type UIConfig struct {
	HideSync          *bool    `json:"hideSync,omitempty"`
	HideRepoSelector  *bool    `json:"hideRepoSelector,omitempty"`
	HideStar          *bool    `json:"hideStar,omitempty"`
	SidebarCollapsed  *bool    `json:"sidebarCollapsed,omitempty"`
	Repo              *RepoRef `json:"repo,omitempty"`
	ActiveWorktreeKey string   `json:"activeWorktreeKey,omitempty"`
}

type RepoRef struct {
	Owner string `json:"owner"`
	Name  string `json:"name"`
}

type versionOutputBody struct {
	Version string `json:"version"`
}

type versionOutput = bodyOutput[versionOutputBody]

type ServerOptions struct {
	EmbedConfig         *EmbedConfig
	Clones              *gitclone.Manager // optional clone manager for diff view
	WorktreeDir         string            // base dir for workspace worktrees
	PtyOwnerDir         string
	PtyOwnerExePath     string
	PtyOwnerExeArgs     []string
	PtyOwnerManagerPath string
	PtyOwnerCommand     []string
	PtyOwnerInProcess   bool
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
	runtime                     *localruntime.Manager
	cfg                         *config.Config
	cfgPath                     string
	cfgMu                       sync.Mutex
	configReloadMu              sync.Mutex
	// bootCfgSnapshot freezes the subset of config fields that are
	// bound at startup (registry, listeners, clone manager, etc.) so a
	// config-file watcher reload can detect when those changed and
	// surface restart_required to the UI without ever mutating them.
	bootCfgSnapshot        startupConfigSnapshot
	configWatcher          *configwatch.Watcher
	basePath               string
	options                ServerOptions
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

	var httpErr error
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
	s := &Server{
		db:                     database,
		basePath:               basePath,
		syncer:                 syncer,
		clones:                 clones,
		cfg:                    cfg,
		cfgPath:                cfgPath,
		bootCfgSnapshot:        snapshotStartupConfig(cfg),
		options:                options,
		now:                    time.Now,
		hub:                    NewEventHubWithCapacity(cfg.SSEBufferSizeOrDefault()),
		tmuxActivity:           newTmuxActivityTracker(nil),
		labelCatalogRefreshIDs: make(map[int64]struct{}),
		bgCtx: shutdownAwareContext{
			parent:   bgBaseCtx,
			deadline: bgDeadline,
		},
		bgCancel:   bgCancel,
		bgDeadline: bgDeadline,
	}

	// (*Config).TmuxCommand handles a nil receiver and returns the
	// default ["tmux"]. Compute once so the workspace, runtime, and
	// terminal handler all share the same value and the nil-safety
	// of the call is explicit at this level.
	tmuxCmd := cfg.TmuxCommand()
	tmuxAvailable := tmuxCommandAvailable(tmuxCmd)
	if options.WorktreeDir != "" {
		s.workspaces = workspace.NewManager(database, options.WorktreeDir)
		s.workspacePRMonitor = workspace.NewPRMonitor(database)
		s.workspacePushedHeadObserver = workspace.NewPushedHeadObserver(database)
		s.workspaces.SetTmuxCommand(tmuxCmd)
		s.workspaces.SetIssueBranchSlugEnabled(
			cfg.IssueWorkspaceBranchSlugEnabled(),
		)
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
		var runtimePtyOwner ptyownerruntime.Owner
		if !tmuxAvailable {
			runtimePtyOwner = ptyownerruntime.New(ptyOwnerClient, nil)
		}
		s.runtime = localruntime.NewManager(localruntime.Options{
			Targets: localruntime.ResolveLaunchTargets(
				agents, tmuxCmd, nil,
			),
			TmuxCommand:             tmuxCmd,
			TmuxOwnerMarker:         s.workspaces.TmuxOwnerMarker(),
			WrapAgentSessionsInTmux: cfg.TmuxAgentSessionsEnabled(),
			StripEnvVars:            cfg.TokenEnvNames(),
			ShellCommand:            cfg.ShellCommand(),
			OnSessionExit:           s.handleRuntimeSessionExit,
			PtyOwnerRuntime:         runtimePtyOwner,
		})
		if err := s.restoreRuntimeTmuxSessions(context.Background()); err != nil {
			slog.Warn("restore runtime tmux sessions", "err", err)
		}
	}

	if s.workspaces != nil {
		s.runBackground(s.runWorkspacePRMonitorLoop)
		s.runBackground(s.runWorkspacePushedHeadObserverLoop)
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
		indexBytes, err := fs.ReadFile(frontend, "index.html")
		if err != nil {
			indexBytes = []byte("<!DOCTYPE html><html><body>frontend not found</body></html>")
		}
		indexTemplate := string(indexBytes)
		if basePath != "/" {
			prefix := strings.TrimSuffix(basePath, "/")
			indexTemplate = strings.ReplaceAll(indexTemplate, `src="/assets/`, `src="`+prefix+`/assets/`)
			indexTemplate = strings.ReplaceAll(indexTemplate, `href="/assets/`, `href="`+prefix+`/assets/`)
		}

		serveIndex := func(w http.ResponseWriter) {
			idx := strings.Replace(indexTemplate, "<head>",
				`<head><script>`+s.bootstrapScript()+`</script>`, 1)
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			// index.html references content-hashed bundles. Browsers
			// must always re-fetch it so a rebuild is picked up; the
			// hashed assets it references can still be cached forever.
			w.Header().Set("Cache-Control",
				"no-store, no-cache, must-revalidate, max-age=0")
			w.Header().Set("Pragma", "no-cache")
			w.Header().Set("Expires", "0")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(idx))
		}

		fileServer := http.FileServerFS(frontend)
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/api/") {
				http.NotFound(w, r)
				return
			}
			name := strings.TrimPrefix(r.URL.Path, "/")
			if name == "" || name == "index.html" {
				serveIndex(w)
				return
			}
			f, err := frontend.Open(name)
			if err == nil {
				f.Close()
				if strings.HasPrefix(r.URL.Path, "/assets/") {
					w.Header().Set("Cache-Control",
						"public, max-age=31536000, immutable")
				}
				fileServer.ServeHTTP(w, r)
				return
			}
			// A missing /assets/* request is a stale-bundle fetch from
			// an old cached index.html. Returning the SPA HTML here
			// would 200 with the wrong Content-Type and leave the page
			// stuck on a failed module import.
			if strings.HasPrefix(r.URL.Path, "/assets/") {
				http.NotFound(w, r)
				return
			}
			serveIndex(w)
		})
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

func (s *Server) restoreRuntimeTmuxSessions(ctx context.Context) error {
	if s.db == nil || s.runtime == nil {
		return nil
	}
	stored, err := s.db.ListAllWorkspaceTmuxSessions(ctx)
	if err != nil {
		return err
	}
	if len(stored) == 0 {
		return nil
	}

	sessions := make([]localruntime.RestoredTmuxSession, 0, len(stored))
	for _, session := range stored {
		sessions = append(sessions, localruntime.RestoredTmuxSession{
			WorkspaceID: session.WorkspaceID,
			SessionName: session.SessionName,
			TargetKey:   session.TargetKey,
			CreatedAt:   session.CreatedAt,
		})
	}
	slog.Debug("restoring runtime tmux sessions", "count", len(sessions))
	return s.runtime.RestoreTmuxSessions(ctx, sessions)
}

func (s *Server) handleRuntimeSessionExit(info localruntime.SessionInfo) {
	if info.TmuxSession == "" || s.workspaces == nil {
		return
	}
	s.runBackground(func(ctx context.Context) {
		cleanupCtx, cancel := context.WithTimeout(
			ctx, runtimeSessionCleanupTimeout,
		)
		defer cancel()
		if _, err := s.workspaces.ForgetMissingRuntimeTmuxSession(
			cleanupCtx, info.WorkspaceID, info.TmuxSession,
			info.CreatedAt,
		); err != nil {
			slog.Warn(
				"forget missing runtime tmux session",
				"workspace_id", info.WorkspaceID,
				"session_key", info.Key,
				"tmux_session", info.TmuxSession,
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
	cfg := s.options.EmbedConfig
	if awKey, set := s.ActiveWorktreeKey(); set {
		if cfg == nil {
			cfg = &EmbedConfig{}
		} else {
			cfgCopy := *cfg
			cfg = &cfgCopy
		}
		if cfg.UI == nil {
			cfg.UI = &UIConfig{}
		} else {
			uiCopy := *cfg.UI
			cfg.UI = &uiCopy
		}
		cfg.UI.ActiveWorktreeKey = awKey
	}
	if cfg != nil {
		configJSON, _ := json.Marshal(cfg)
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
	start := time.Now()
	slog.Debug(
		"http request started",
		"method", r.Method,
		"path", r.URL.Path,
		"query", r.URL.RawQuery,
		"remote_addr", r.RemoteAddr,
		"user_agent", r.UserAgent(),
	)
	defer func() {
		slog.Debug(
			"http request completed",
			"method", r.Method,
			"path", r.URL.Path,
			"duration", time.Since(start).String(),
		)
	}()
	if r.Method != http.MethodGet && s.isMutatingAPIRequest(r) {
		if !checkCSRF(w, r) {
			return
		}
	}
	s.handler.ServeHTTP(w, r)
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

// checkCSRF rejects cross-site mutation requests. Returns true if
// the request is allowed, false if it was rejected (response written).
func checkCSRF(w http.ResponseWriter, r *http.Request) bool {
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
