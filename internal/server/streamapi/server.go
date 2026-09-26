package streamapi

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/projects"
	"go.kenn.io/forge/internal/server/authapi"
	"go.kenn.io/forge/internal/server/fleetapi"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/server/issueapi"
	"go.kenn.io/forge/internal/server/itemapi"
	"go.kenn.io/forge/internal/server/kata"
	"go.kenn.io/forge/internal/server/pullapi"
	"go.kenn.io/forge/internal/server/routepolicy"
	"go.kenn.io/forge/internal/server/spokeapi"
	"go.kenn.io/forge/internal/server/syncevents"
	"go.kenn.io/forge/internal/server/workspaceapi"
	"go.kenn.io/forge/internal/workspace/localruntime"
	"go.kenn.io/forge/platform"
)

var crossOriginProtection http.CrossOriginProtection

type ShutdownDeadline struct {
	mu       sync.RWMutex
	deadline time.Time
	set      bool
}

var (
	StartupTmuxCleanupTimeout    = 2 * time.Second
	RuntimeSessionCleanupTimeout = 2 * time.Second
)

func (d *ShutdownDeadline) Tighten(deadline time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.set || deadline.Before(d.deadline) {
		d.deadline = deadline
		d.set = true
	}
}

func (d *ShutdownDeadline) get() (time.Time, bool) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.deadline, d.set
}

type ShutdownAwareContext struct {
	Parent        context.Context
	DeadlineValue *ShutdownDeadline
}

func (c ShutdownAwareContext) Deadline() (time.Time, bool) {
	deadline, ok := c.DeadlineValue.get()
	if !ok {
		return c.Parent.Deadline()
	}
	if parentDeadline, parentOK := c.Parent.Deadline(); parentOK &&
		parentDeadline.Before(deadline) {
		return parentDeadline, true
	}
	return deadline, true
}

func (c ShutdownAwareContext) Done() <-chan struct{} {
	return c.Parent.Done()
}

func (c ShutdownAwareContext) Err() error {
	return c.Parent.Err()
}

func (c ShutdownAwareContext) Value(key any) any {
	return c.Parent.Value(key)
}

type PullLifecycle interface {
	Stop()
	Shutdown(context.Context) error
}

// trackHTTPConn is installed as http.Server.ConnState by Serve so
// Shutdown can wait for per-connection goroutines to fully unwind.
func (s *Handlers) TrackHTTPConn(_ net.Conn, state http.ConnState) {
	switch state {
	case http.StateNew:
		s.ConnWG.Add(1)
	case http.StateHijacked, http.StateClosed:
		s.ConnWG.Done()
	case http.StateActive, http.StateIdle:
	}
}

func (s *Handlers) SubscribeWorkspaceEvents(
	ctx context.Context, injectCached bool,
) (<-chan workspaceapi.RecordedEvent, <-chan struct{}) {
	source, done := (*s.Hub).Subscribe(ctx, injectCached)
	events := make(chan workspaceapi.RecordedEvent, cap(source))
	go func() {
		defer close(events)
		for event := range source {
			select {
			case events <- workspaceapi.RecordedEvent{
				ID:   event.ID,
				Type: event.Event.Type,
				Data: event.Event.Data,
			}:
			case <-ctx.Done():
				return
			}
		}
	}()
	return events, done
}

// runBackground launches fn as a tracked goroutine. fn receives a
// context cancelled by Shutdown. If Shutdown has already started,
// runBackground drops the task: these goroutines are best-effort
// refreshes and starting one during drain would race with bg.Wait.
func (s *Handlers) RunBackground(fn func(ctx context.Context)) bool {
	s.BgMu.Lock()
	if *s.ShuttingDown {
		s.BgMu.Unlock()
		return false
	}
	s.Bg.Add(1)
	s.BgMu.Unlock()
	go func() {
		defer s.Bg.Done()
		fn(s.BgCtx)
	}()
	return true
}

func (s *Handlers) RunWorkspaceDependent(fn func(context.Context)) {
	if fn == nil {
		return
	}
	s.WorkspaceDependentsWG.Go(func() {
		fn((*s.WorkspaceDependentsCtx))
	})
}

func (s *Handlers) StopWorkspaceDependents() <-chan struct{} {
	s.WorkspaceDependentsOnce.Do(func() {
		(*s.WorkspaceDependentsCancel)()
		go func() {
			s.WorkspaceDependentsWG.Wait()
			close(s.WorkspaceDependentsDone)
		}()
	})
	return s.WorkspaceDependentsDone
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
// "example.com" and the apitest helpers use "forge.test"; both
// must be accepted so the dozens of test helpers that pass
// cfg=nil work unchanged.
func testFallbackAllowedHosts() []config.HostKey {
	return []config.HostKey{
		{Host: "example.com", Port: ""},
		{Host: "forge.test", Port: ""},
	}
}

// allowUnvalidatedConfigHostCheckFallbackForTests is false in
// production. Same-package tests set it from _test.go so legacy
// partial config literals can exercise unrelated server behavior
// without manufacturing a full validated config.
var AllowUnvalidatedConfigHostCheckFallbackForTests bool

// resolveHostCheckOptions applies the precedence rule:
// caller override > cfg-derived options > cfg=nil test-friendly
// fallback. For non-nil configs that bypassed config.Load, derive
// the bind and allowlist from the provided config fields so
// production callers do not silently inherit hard-coded host
// defaults.
func ResolveHostCheckOptions(
	cfg *config.Config,
	override authapi.HostCheckOptions,
	allowLoopbackAnyPort bool,
) authapi.HostCheckOptions {
	opts, err := pickHostCheckOptions(cfg, override)
	if err != nil {
		panic(err)
	}
	if allowLoopbackAnyPort {
		opts.AllowLoopbackAnyPort = true
	}
	return opts
}

func pickHostCheckOptions(cfg *config.Config, override authapi.HostCheckOptions) (authapi.HostCheckOptions, error) {
	if override.Valid() {
		return override, nil
	}
	if cfg != nil {
		if k := cfg.BindHostKey(); k.Valid() {
			return authapi.HostCheckOptions{
				Bind:              k,
				Allowed:           cfg.ParsedAllowedHosts(),
				TrustReverseProxy: cfg.TrustReverseProxy,
			}, nil
		}
		opts, err := authapi.DeriveHostCheckOptionsFromConfig(cfg)
		if err == nil {
			return opts, nil
		}
		if !AllowUnvalidatedConfigHostCheckFallbackForTests {
			return authapi.HostCheckOptions{}, fmt.Errorf("server: config did not provide valid Host check options: %w", err)
		}
		return fallbackHostCheckOptions(), nil
	}
	slog.Warn(
		"server.New used without a cfg or explicit ServerOptions.HostCheck; using httptest-compatible Host defaults. Production callers must pass a validated config or explicit HostCheck options.",
	)
	return fallbackHostCheckOptions(), nil
}

func fallbackHostCheckOptions() authapi.HostCheckOptions {
	return authapi.HostCheckOptions{
		Bind: config.HostKey{
			Host: hostCheckTestFallbackBindHost,
			Port: hostCheckTestFallbackBindPort,
		},
		Allowed:              testFallbackAllowedHosts(),
		TrustReverseProxy:    false,
		AllowLoopbackAnyPort: true,
	}
}

func WorkspaceConfigSnapshot(
	cfg *config.Config, tmuxCommand []string,
) workspaceapi.ConfigSnapshot {
	snapshot := workspaceapi.ConfigSnapshot{
		TmuxCommand: slices.Clone(tmuxCommand), IssueBranchSlug: true,
	}
	if cfg == nil {
		return snapshot
	}
	snapshot.Agents = spokeapi.CloneConfigAgents(cfg.Agents)
	snapshot.AutoAssignOnCreate = cfg.Workspaces.AutoAssignOnCreate
	snapshot.RoborevInitManagedClones = cfg.Roborev.InitManagedClones
	snapshot.IssueBranchSlug = cfg.IssueWorkspaceBranchSlugEnabled()
	snapshot.KnownPlatformHosts = make(
		[]projects.KnownPlatformHost, 0, len(cfg.Platforms)+len(cfg.Repos)+1,
	)
	snapshot.KnownPlatformHosts = append(snapshot.KnownPlatformHosts, projects.KnownPlatformHost{
		Platform: string(platform.KindGitHub),
		Host:     cfg.DefaultPlatformHost,
	})
	for _, configured := range cfg.Platforms {
		snapshot.KnownPlatformHosts = append(snapshot.KnownPlatformHosts, projects.KnownPlatformHost{
			Platform: configured.Type,
			Host:     configured.Host,
		})
	}
	for _, repo := range cfg.Repos {
		snapshot.KnownPlatformHosts = append(snapshot.KnownPlatformHosts, projects.KnownPlatformHost{
			Platform: repo.PlatformOrDefault(),
			Host:     repo.PlatformHostOrDefault(),
		})
	}
	return snapshot
}

func KataConfigSnapshot(cfg *config.Config) kata.ConfigSnapshot {
	if cfg == nil {
		return kata.ConfigSnapshot{}
	}
	return kata.ConfigSnapshot{
		Repos:        slices.Clone(cfg.Repos),
		KataProjects: slices.Clone(cfg.KataProjects),
	}
}

func PullConfigSnapshot(cfg *config.Config) pullapi.ConfigSnapshot {
	if cfg == nil {
		return pullapi.ConfigSnapshot{}
	}
	return pullapi.ConfigSnapshot{
		AllowMidStackMerges:            cfg.PullRequests.AllowMidStackMerges,
		UseWorkspaceActivityForRecency: cfg.Activity.UseWorkspaceActivityForRecency,
	}
}

func IssueConfigSnapshot(cfg *config.Config) issueapi.ConfigSnapshot {
	if cfg == nil {
		return issueapi.ConfigSnapshot{}
	}
	return issueapi.ConfigSnapshot{
		UseWorkspaceActivityForRecency: cfg.Activity.UseWorkspaceActivityForRecency,
	}
}

func FleetConfigSnapshot(cfg *config.Config, tmuxCommand []string) fleetapi.ConfigSnapshot {
	if cfg == nil {
		return fleetapi.ConfigSnapshot{TmuxCommand: slices.Clone(tmuxCommand)}
	}
	platformAuth := config.Config{
		GitHubTokenEnv:      cfg.GitHubTokenEnv,
		DefaultPlatformHost: cfg.DefaultPlatformHost,
		Repos:               slices.Clone(cfg.Repos),
		Platforms:           slices.Clone(cfg.Platforms),
		// Owner PATs and App installations are credential routes in their
		// own right: without them a repository served only by an owner
		// token resolves to no credential and Fleet reports the platform
		// backend as unauthenticated while sync and mutations work.
		GitHubOwnerTokens: slices.Clone(cfg.GitHubOwnerTokens),
		GitHubApps:        slices.Clone(cfg.GitHubApps),
	}
	return fleetapi.ConfigSnapshot{
		Fleet:               cfg.Fleet,
		PlatformAuthConfig:  platformAuth,
		PlatformAuthEnabled: !cfg.ExecutionWorker.Enabled,
		TmuxCommand:         slices.Clone(tmuxCommand),
	}
}

// updateCatalogStripEnvVars widens every credential strip set with
// externally cataloged token env names (Kata daemon catalogs). All
// consumers accumulate monotonically, so stale catalog names only
// over-strip.
func (s *Handlers) UpdateCatalogStripEnvVars(names []string) {
	if len(names) == 0 {
		return
	}
	if (*s.Workspaces) != nil {
		(*s.Workspaces).UpdateTmuxStripEnvVars(names)
	}
	if (*s.Runtime) != nil {
		(*s.Runtime).UpdateStripEnvVars(names)
	}
	if (*s.PtyOwnerClient) != nil {
		(*s.PtyOwnerClient).UpdateStripEnvVars(names)
	}
}

func (s *Handlers) ApplyWorkspaceConfigLocked() {
	if (*s.WorkspaceAPI) != nil {
		(*s.WorkspaceAPI).ApplyConfig(WorkspaceConfigSnapshot((*s.Cfg), (*s.TmuxCmd)))
	}
}

func (s *Handlers) ApplyFleetConfigLocked() {
	active := s.ActiveFleetConfigSnapshotLocked()
	if (*s.FleetAPI) != nil {
		(*s.FleetAPI).ApplyConfig(active)
	}
	if (*s.HubEvents) != nil {
		(*s.HubEvents).SetEnabled(active.Fleet.Enabled)
	}
	if (*s.SpokeActivationLease) != nil {
		(*s.SpokeActivationLease).SetEnabled(active.Fleet.Enabled)
	}
}

func (s *Handlers) ActiveFleetConfigSnapshotLocked() fleetapi.ConfigSnapshot {
	snapshot := FleetConfigSnapshot((*s.Cfg), (*s.TmuxCmd))
	// A daemon that booted outside a fleet may activate federation only when
	// its startup request policy already required API authentication.
	snapshot.Fleet.Enabled = snapshot.Fleet.Enabled &&
		((*s.FleetEnabledAtBoot) || s.DaemonRequests.RequireAPIAuth)
	snapshot.Fleet.Role = s.BootCfgSnapshot.FleetRole
	snapshot.Fleet.BaseURL = s.BootCfgSnapshot.FleetBaseURL
	if s.BootCfgSnapshot.Hub == nil {
		snapshot.Fleet.Hub = nil
	} else {
		name := ""
		if snapshot.Fleet.Hub != nil {
			name = snapshot.Fleet.Hub.Name
		}
		snapshot.Fleet.Hub = &config.FleetHub{
			NodeID: s.BootCfgSnapshot.Hub.NodeID,
			Name:   name, BaseURL: s.BootCfgSnapshot.Hub.BaseURL,
		}
	}
	return snapshot
}

func (s *Handlers) ApplyKataConfigLocked() {
	if (*s.KataAPI) != nil {
		(*s.KataAPI).ApplyConfig(KataConfigSnapshot((*s.Cfg)))
	}
}

func (s *Handlers) ApplyPullConfigLocked() {
	if (*s.PullAPI) != nil {
		(*s.PullAPI).ApplyConfig(PullConfigSnapshot((*s.Cfg)))
	}
}

func (s *Handlers) ApplyIssueConfigLocked() {
	if (*s.IssueAPI) != nil {
		(*s.IssueAPI).ApplyConfig(IssueConfigSnapshot((*s.Cfg)))
	}
}

func (s *Handlers) HandleRuntimeSessionExit(info localruntime.SessionInfo) {
	if info.WorkspaceID == authapi.HostRuntimeScope {
		if s.Db == nil || info.TmuxSession == "" {
			return
		}
		s.RunBackground(func(ctx context.Context) {
			cleanupCtx, cancel := context.WithTimeout(
				ctx, RuntimeSessionCleanupTimeout,
			)
			defer cancel()
			// Generation-qualified: command session keys are reusable, so
			// this exit's cleanup must not delete the row of a newer live
			// session relaunched under the same key.
			if _, err := s.Db.DeleteHostRuntimeTmuxSessionCreatedAt(
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
		if worktreeID == "" || s.Db == nil || info.TmuxSession == "" {
			return
		}
		s.RunBackground(func(ctx context.Context) {
			cleanupCtx, cancel := context.WithTimeout(
				ctx, RuntimeSessionCleanupTimeout,
			)
			defer cancel()
			// Generation-qualified: command session keys are reusable, so
			// this exit's cleanup must not delete the row of a newer live
			// session relaunched under the same key.
			if _, err := s.Db.DeleteProjectWorktreeTmuxSessionCreatedAt(
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
	if (*s.WorkspaceAPI) != nil {
		(*s.WorkspaceAPI).HandleRuntimeSessionExit(info)
	}
}

func TmuxCommandAvailable(command []string) bool {
	if len(command) == 0 || command[0] == "" {
		return false
	}
	_, err := exec.LookPath(command[0])
	return err == nil
}

// scriptSafe escapes sequences that could break out of an inline
// <script> block. Replaces "</" with "<\/" so that payloads
// containing "</script>" cannot close the tag early.
func ScriptSafe(s string) string {
	return strings.ReplaceAll(s, "</", `<\/`)
}

func (s *Handlers) FederationEnabled() bool {
	s.CfgMu.Lock()
	defer s.CfgMu.Unlock()
	return (*s.Cfg) == nil ||
		(*s.Cfg).Fleet.Enabled &&
			((*s.FleetEnabledAtBoot) || s.DaemonRequests.RequireAPIAuth)
}

func (s *Handlers) CheckHost(w http.ResponseWriter, r *http.Request) bool {
	s.AllowedHostMu.RLock()
	allowedHosts := (*s.AllowedHosts)
	s.AllowedHostMu.RUnlock()
	return CheckListenerHost(w, r, allowedHosts)
}

func CheckListenerHost(
	w http.ResponseWriter,
	r *http.Request,
	allowedHosts map[string]struct{},
) bool {
	if len(allowedHosts) == 0 {
		return true
	}
	if !authorityIsLoopbackHost(r.Host) || authapi.IsLoopbackRemoteAddr(r.RemoteAddr) {
		return true
	}
	routepolicy.WriteProblemResponse(w, httpapi.NewProblem(
		http.StatusForbidden,
		httpapi.CodeForbidden,
		"host is not allowed",
		map[string]any{"reason": "hostNotAllowed"},
	))
	return false
}

// isMutatingAPIRequest checks whether the request targets an API route,
// accounting for the configured basePath prefix.
func (s *Handlers) IsMutatingAPIRequest(r *http.Request) bool {
	path := r.URL.Path
	if (*s.BasePath) != "/" {
		prefix := strings.TrimSuffix((*s.BasePath), "/")
		path = strings.TrimPrefix(path, prefix)
	}
	return strings.HasPrefix(path, "/api/")
}

func (s *Handlers) IsMutatingDocsAPIRequest(r *http.Request) bool {
	path := r.URL.Path
	if (*s.BasePath) != "/" {
		prefix := strings.TrimSuffix((*s.BasePath), "/")
		path = strings.TrimPrefix(path, prefix)
	}
	return strings.HasPrefix(path, "/api/v1/docs/")
}

func (s *Handlers) IsTerminalClipboardAPIRequest(r *http.Request) bool {
	path := r.URL.Path
	if (*s.BasePath) != "/" {
		prefix := strings.TrimSuffix((*s.BasePath), "/")
		path = strings.TrimPrefix(path, prefix)
	}
	return path == "/api/v1/terminal/clipboard"
}

func (s *Handlers) IsDocsBrowseAPIRequest(r *http.Request) bool {
	path := r.URL.Path
	if (*s.BasePath) != "/" {
		prefix := strings.TrimSuffix((*s.BasePath), "/")
		path = strings.TrimPrefix(path, prefix)
	}
	return path == "/api/v1/docs/browse"
}

func (s *Handlers) IsDocsReadAPIRequest(r *http.Request) bool {
	path := r.URL.Path
	if (*s.BasePath) != "/" {
		prefix := strings.TrimSuffix((*s.BasePath), "/")
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

// checkCrossOrigin rejects cross-origin browser requests. Returns true if
// the request is allowed, false if it was rejected (response written).
func CheckCrossOrigin(w http.ResponseWriter, r *http.Request, trustReverseProxy bool) bool {
	request := r
	if trustReverseProxy {
		// Host validation has already accepted the forwarded public authority.
		// Use it for the Origin comparison instead of the proxy's backend Host.
		publicHost := ""
		if values := r.Header.Values("X-Forwarded-Host"); len(values) > 0 {
			if key, err := authapi.ParseXForwardedHost(strings.Join(values, ",")); err == nil {
				publicHost = key.String()
			}
		} else if values := r.Header.Values("Forwarded"); len(values) > 0 {
			if key, err := authapi.ParseForwardedHost(strings.Join(values, ",")); err == nil {
				publicHost = key.String()
			}
		}
		if publicHost != "" {
			request = r.Clone(r.Context())
			request.Host = publicHost
		}
	}
	if err := crossOriginProtection.Check(request); err != nil {
		authapi.WriteError(w, http.StatusForbidden, "cross-origin requests are not allowed")
		return false
	}
	return true
}

// adoptListenerHostPort repoints the Host-check bind at the listener's actual
// authority. Besides kernel-assigned ports, this normalizes IP literals to the
// form net/http places in direct request Host headers.
func (s *Handlers) AdoptListenerHostPort(ln net.Listener) {
	opts := *s.HostOpts.Load()
	bind, ok := authapi.ListenerHostKey(ln)
	if !ok {
		return
	}
	opts.Bind = bind
	s.HostOpts.Store(&opts)
}

func (s *Handlers) SetAllowedHostsForListener(ln net.Listener) {
	allowed := AllowedHostsForListener(ln)
	s.AllowedHostMu.Lock()
	(*s.AllowedHosts) = allowed
	s.AllowedHostMu.Unlock()
}

func AllowedHostsForListener(ln net.Listener) map[string]struct{} {
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
func (s *Handlers) HandleSSE(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	rc := http.NewResponseController(w)
	// Clear server-wide WriteTimeout for this SSE response
	if err := rc.SetWriteDeadline(time.Time{}); err != nil {
		return
	}
	cursor, hasCursor := ParseLastEventID(r)
	s.ServeSSE(r.Context(), w, rc, cursor, hasCursor)
}

func (s *Handlers) StreamEvents(
	_ context.Context, input *itemapi.StreamEventsInput,
) (*huma.StreamResponse, error) {
	return &huma.StreamResponse{
		Body: func(ctx huma.Context) {
			ctx.SetHeader("Content-Type", "text/event-stream")
			ctx.SetHeader("Cache-Control", "no-cache")
			ctx.SetHeader("Connection", "keep-alive")

			r, w := humago.Unwrap(ctx)
			rc := http.NewResponseController(w)
			_ = rc.SetWriteDeadline(time.Time{})
			cursor, hasCursor := ParseLastEventID(r)
			ch, done := (*s.Hub).Subscribe(ctx.Context(), !hasCursor)
			releaseSelection := func() {}
			if input.WorkspaceID != "" && (*s.WorkspaceAPI) != nil {
				releaseSelection = (*s.WorkspaceAPI).SelectWorkspaceDiff(input.WorkspaceID)
			}
			defer releaseSelection()
			s.serveSSESubscribed(ctx.Context(), w, rc, cursor, hasCursor, ch, done)
		},
	}, nil
}

// parseLastEventID inspects an incoming SSE request for a reconnect
// cursor. The Last-Event-ID header takes priority (HTML5 EventSource
// emits it automatically on reconnect); the since= query parameter is
// the fallback for non-browser callers and explicit first-connect
// resumption. Returns (0, false) when no usable cursor is present, so
// the handler can fall back to the no-cursor path (live + cached
// sync_status) without further branching.
func ParseLastEventID(r *http.Request) (uint64, bool) {
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

func (s *Handlers) ServeSSE(
	ctx context.Context,
	w io.Writer,
	rc authapi.SseController,
	cursor uint64,
	hasCursor bool,
) {
	// Subscribe BEFORE the first flush so any broadcast issued between
	// the headers landing on the wire and the subscriber being registered
	// is delivered to this client instead of dropped. When a cursor is
	// supplied the handler replays the ring directly, so cached
	// sync_status injection by Subscribe would duplicate; pass false.
	ch, done := (*s.Hub).Subscribe(ctx, !hasCursor)
	s.serveSSESubscribed(ctx, w, rc, cursor, hasCursor, ch, done)
}

func (s *Handlers) serveSSESubscribed(
	ctx context.Context,
	w io.Writer,
	rc authapi.SseController,
	cursor uint64,
	hasCursor bool,
	ch <-chan syncevents.RecordedEvent,
	done <-chan struct{},
) {
	serveSSESubscribedFromHub(
		ctx,
		w,
		rc,
		(*s.Hub),
		cursor,
		hasCursor,
		ch,
		done,
		func(uint64) syncevents.Event {
			return s.ReconnectStaleEvent()
		},
	)
}

func serveSSESubscribedFromHub(
	ctx context.Context,
	w io.Writer,
	rc authapi.SseController,
	hub *syncevents.EventHub,
	cursor uint64,
	hasCursor bool,
	ch <-chan syncevents.RecordedEvent,
	done <-chan struct{},
	staleEvent func(uint64) syncevents.Event,
) {
	ServeSSESubscribedFromHubTransformed(
		ctx, w, rc, hub, cursor, hasCursor, ch, done, staleEvent,
		func(rec syncevents.RecordedEvent) (syncevents.RecordedEvent, bool) { return rec, true },
		nil,
		nil,
	)
}

type sseReplaySnapshot struct {
	records []syncevents.RecordedEvent
	staleID uint64
	stale   bool
}

func ServeSSESubscribedFromHubTransformed(
	ctx context.Context,
	w io.Writer,
	rc authapi.SseController,
	hub *syncevents.EventHub,
	cursor uint64,
	hasCursor bool,
	ch <-chan syncevents.RecordedEvent,
	done <-chan struct{},
	staleEvent func(uint64) syncevents.Event,
	transform func(syncevents.RecordedEvent) (syncevents.RecordedEvent, bool),
	afterReplay func(io.Writer, authapi.SseController) bool,
	preparedReplay *sseReplaySnapshot,
) {
	if err := rc.Flush(); err != nil {
		return
	}

	// Resolve the replay path before entering the live loop so the
	// client sees missed events (or a stale signal) before any new
	// live broadcasts and never out of order with them.
	deliveredThrough := cursor
	if hasCursor {
		var replay []syncevents.RecordedEvent
		var synID uint64
		var stale bool
		if preparedReplay == nil {
			replay, synID, stale = hub.ReplaySnapshotSince(cursor)
		} else {
			replay = preparedReplay.records
			synID = preparedReplay.staleID
			stale = preparedReplay.stale
		}
		if stale {
			if !writeSSERecorded(w, rc, syncevents.RecordedEvent{ID: synID, Event: staleEvent(synID)}) {
				return
			}
			deliveredThrough = synID
		} else {
			for _, rec := range replay {
				deliveredThrough = rec.ID
				transformed, ok := transform(rec)
				if !ok {
					continue
				}
				if !writeSSERecorded(w, rc, transformed) {
					return
				}
			}
		}
	}
	if afterReplay != nil && !afterReplay(w, rc) {
		return
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
			deliveredThrough = ev.ID
			transformed, include := transform(ev)
			if !include {
				continue
			}
			if !writeSSERecorded(w, rc, transformed) {
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
func writeSSERecorded(w io.Writer, rc authapi.SseController, rec syncevents.RecordedEvent) bool {
	data, err := json.Marshal(rec.Event.Data)
	if err != nil {
		slog.Error("sse: marshal event", "type", rec.Event.Type, "err", err)
		// Skip the unmarshalable event but keep streaming.
		return true
	}
	return writeSSEFrame(w, rc, rec.ID, rec.Event.Type, data)
}

func writeSSEFrame(
	w io.Writer, rc authapi.SseController, id uint64, eventType string, data []byte,
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
