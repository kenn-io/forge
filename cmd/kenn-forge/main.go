package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"go.kenn.io/forge/internal/archive"
	"go.kenn.io/forge/internal/cli/serve"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/daemonruntime"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/gitclone"
	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/githubapp"
	"go.kenn.io/forge/internal/platform"
	"go.kenn.io/forge/internal/profiler"
	"go.kenn.io/forge/internal/ptyowner"
	"go.kenn.io/forge/internal/runtimelock"
	"go.kenn.io/forge/internal/server"
	"go.kenn.io/forge/internal/server/fleetapi"
	"go.kenn.io/forge/internal/shutdownbudget"
	"go.kenn.io/forge/internal/stacks"
	"go.kenn.io/forge/internal/telemetry"
	"go.kenn.io/forge/internal/tokenauth"
	"go.kenn.io/forge/internal/web"
	"go.kenn.io/kit/daemon"
	oteltelemetry "go.kenn.io/kit/telemetry"
)

type splitLogHandler struct {
	handlers []slog.Handler
}

type serveReadyListener struct {
	net.Listener
	notifyReady func()
}

func (l serveReadyListener) Accept() (net.Conn, error) {
	l.notifyReady()
	return l.Listener.Accept()
}

func (h splitLogHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, handler := range h.handlers {
		if handler.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (h splitLogHandler) Handle(ctx context.Context, r slog.Record) error {
	for _, handler := range h.handlers {
		if !handler.Enabled(ctx, r.Level) {
			continue
		}
		if err := handler.Handle(ctx, r.Clone()); err != nil {
			return err
		}
	}
	return nil
}

func (h splitLogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	handlers := make([]slog.Handler, 0, len(h.handlers))
	for _, handler := range h.handlers {
		handlers = append(handlers, handler.WithAttrs(attrs))
	}
	return splitLogHandler{handlers: handlers}
}

func (h splitLogHandler) WithGroup(name string) slog.Handler {
	handlers := make([]slog.Handler, 0, len(h.handlers))
	for _, handler := range h.handlers {
		handlers = append(handlers, handler.WithGroup(name))
	}
	return splitLogHandler{handlers: handlers}
}

var (
	version                = "dev"
	commit                 = "unknown"
	buildDate              = "unknown"
	runtimePublishGatePath string
	runtimeServeGatePath   string
	runtimeShutdownDelay   string
)

var runServer = run

type versionOutput struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildDate string `json:"buildDate"`
}

func main() {
	closeLog, err := configureLogging(os.Stderr)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "configure logging: %v\n", err)
		os.Exit(1)
	}
	defer func() {
		if err := closeLog(); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "close log file: %v\n", err)
		}
	}()

	if err := runCLI(os.Args[1:], os.Stdout); err != nil {
		var apiErr *apiVerbError
		if errors.As(err, &apiErr) {
			_, _ = fmt.Fprintln(os.Stderr, err)
			os.Exit(exitCodeForAPIVerb(err))
			return
		}
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func configureLogging(stderr io.Writer) (func() error, error) {
	level, err := parseLogLevel(os.Getenv("KENN_FORGE_LOG_LEVEL"))
	if err != nil {
		return nil, err
	}

	var file *os.File
	logFile := strings.TrimSpace(os.Getenv("KENN_FORGE_LOG_FILE"))
	stderrLevel := level
	if logFile != "" {
		stderrLevel = slog.LevelInfo
	}
	if raw := os.Getenv("KENN_FORGE_LOG_STDERR_LEVEL"); strings.TrimSpace(raw) != "" {
		stderrLevel, err = parseLogLevel(raw)
		if err != nil {
			return nil, err
		}
	}

	handlers := []slog.Handler{
		tokenauth.NewRedactingHandler(slog.NewTextHandler(
			stderr,
			&slog.HandlerOptions{Level: stderrLevel},
		)),
	}
	if logFile != "" {
		if err := os.MkdirAll(filepath.Dir(logFile), 0o700); err != nil {
			return nil, fmt.Errorf("create log directory: %w", err)
		}
		file, err = os.OpenFile(
			logFile,
			os.O_CREATE|os.O_WRONLY|os.O_APPEND,
			0o600,
		)
		if err != nil {
			return nil, fmt.Errorf("open log file: %w", err)
		}
		handlers = append(
			handlers,
			tokenauth.NewRedactingHandler(slog.NewTextHandler(
				file,
				&slog.HandlerOptions{Level: level},
			)),
		)
	}

	slog.SetDefault(slog.New(splitLogHandler{handlers: handlers}))
	slog.Debug(
		"logging configured",
		"level", level.String(),
		"stderr_level", stderrLevel.String(),
		"file", logFile,
	)

	return func() error {
		if file == nil {
			return nil
		}
		return file.Close()
	}, nil
}

func parseLogLevel(raw string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "info":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return slog.LevelInfo, fmt.Errorf(
			"unsupported KENN_FORGE_LOG_LEVEL %q", raw,
		)
	}
}

func writeVersion(stdout io.Writer, asJSON bool) error {
	if !asJSON {
		_, err := fmt.Fprintf(
			stdout,
			"kenn-forge %s (%s) built %s\n",
			version, commit, buildDate,
		)
		return err
	}
	return json.NewEncoder(stdout).Encode(versionOutput{
		Name:      "kenn-forge",
		Version:   version,
		Commit:    commit,
		BuildDate: buildDate,
	})
}

func runPtyOwner(root, session, cwd, commandJSON string) error {
	if session == "" {
		return fmt.Errorf("pty-owner session is required")
	}
	if root == "" {
		return fmt.Errorf("pty-owner root is required")
	}
	if cwd == "" {
		return fmt.Errorf("pty-owner cwd is required")
	}
	var command []string
	if commandJSON != "" {
		if err := json.Unmarshal([]byte(commandJSON), &command); err != nil {
			return fmt.Errorf("parse pty-owner command-json: %w", err)
		}
	}
	return ptyowner.RunOwner(context.Background(), ptyowner.Options{
		Root:    root,
		Session: session,
		Cwd:     cwd,
		Command: command,
	})
}

func readConfigValue(configPath, key string, stdout io.Writer) error {
	if err := config.EnsureDefault(configPath); err != nil {
		return fmt.Errorf("ensure config: %w", err)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	switch key {
	case "port":
		_, err := fmt.Fprintf(stdout, "%d\n", cfg.Port)
		return err
	default:
		return fmt.Errorf("unsupported config key %q", key)
	}
}

func writeRuntimeStatus(dataDir string, asJSON bool, stdout io.Writer) error {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return fmt.Errorf(
			"create data directory %s: %w", dataDir, err,
		)
	}

	st, err := runtimelock.Read(dataDir)
	if err != nil {
		return fmt.Errorf("read runtime status: %w", err)
	}

	return runtimelock.FormatStatus(stdout, st, asJSON)
}

func run(opts serve.Options) error {
	configPath := opts.ConfigPath
	cfg, err := config.LoadOrCreate(configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if err := validateBackgroundLaunchConfig(cfg); err != nil {
		return err
	}
	slog.Debug(
		"config loaded",
		"config_path", configPath,
		"data_dir", cfg.DataDir,
		"db_path", cfg.DBPath(),
		"listen_addr", cfg.ListenAddr(),
		"repo_count", len(cfg.Repos),
	)

	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return fmt.Errorf(
			"create data directory %s: %w", cfg.DataDir, err,
		)
	}

	lockHandle, err := runtimelock.Acquire(cfg.DataDir)
	if err != nil {
		var cerr *runtimelock.CollisionError
		if errors.As(err, &cerr) {
			runtimelock.FormatCollisionBanner(
				os.Stderr, cerr, configPath, config.DefaultConfigPath(),
			)
			return fmt.Errorf(
				"another kenn-forge is already running on %s",
				cfg.DataDir,
			)
		}
		return fmt.Errorf("acquire runtime lock: %w", err)
	}
	defer func() {
		if err := lockHandle.Release(); err != nil {
			slog.Warn("release runtime lock", "err", err)
		}
	}()

	ctx, stopSignals := signal.NotifyContext(
		context.Background(),
		syscall.SIGINT,
		syscall.SIGTERM,
	)
	stopSignalsOnce := sync.OnceFunc(stopSignals)
	defer stopSignalsOnce()

	assets, err := web.Assets()
	if err != nil {
		return fmt.Errorf("load frontend assets: %w", err)
	}

	// API auth: the token is always minted (thin clients read it from
	// the well-known data_dir path), but only enforced when
	// [api].require_auth is set.
	authToken, err := runtimelock.EnsureAuthToken(cfg.DataDir)
	if err != nil {
		return fmt.Errorf("ensure auth token: %w", err)
	}
	addr := cfg.ListenAddr()
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", addr, err)
	}
	if ip := net.ParseIP(cfg.Host); ip != nil && !ip.IsLoopback() {
		slog.Warn(
			"binding a non-loopback address: the API has no"+
				" authentication, so the bound network is the trust"+
				" boundary (e.g. a tailnet with ACLs)",
			"host", cfg.Host,
		)
	}

	runtimeIdentity, err := daemonruntime.NewIdentity(ln.Addr(), daemonruntime.IdentityOptions{
		Version: version, Commit: commit, DataDir: cfg.DataDir, ConfigPath: configPath,
		BasePath: cfg.BasePath, RequireAuth: cfg.API.RequireAuth,
	})
	if err != nil {
		_ = ln.Close()
		return fmt.Errorf("build daemon runtime identity: %w", err)
	}
	if err := lockHandle.WriteMetadata(runtimeIdentity.LockMetadata); err != nil {
		slog.Warn("write runtime metadata", "err", err)
	}
	proof, err := daemon.NewProof([]byte(authToken))
	if err != nil {
		_ = ln.Close()
		return fmt.Errorf("initialize daemon proof: %w", err)
	}
	daemonProofHandler, err := proof.NewPingHandler(runtimeIdentity.Record)
	if err != nil {
		_ = ln.Close()
		return fmt.Errorf("initialize daemon ping: %w", err)
	}
	daemonAccess := server.DaemonAccessOptions{
		Token: authToken, RequireAPIAuth: cfg.API.RequireAuth,
		ProofHandler: daemonProofHandler,
	}

	startupHandler := server.NewStartupHandler(
		assets, cfg, server.ServerOptions{DaemonAccess: daemonAccess}, ln,
	)
	switcher := server.NewSwitchHandler(startupHandler)
	httpSrv := &http.Server{
		Handler:     switcher,
		ReadTimeout: 15 * time.Second,
		// WriteTimeout is 0 (disabled) because SSE and proxy
		// responses are long-lived by design.
		IdleTimeout: 60 * time.Second,
	}
	serveReady := make(chan struct{})
	readyListener := serveReadyListener{
		Listener:    ln,
		notifyReady: sync.OnceFunc(func() { close(serveReady) }),
	}
	errCh := make(chan error, 1)
	go func() {
		if serveErr := httpSrv.Serve(readyListener); !errors.Is(serveErr, http.ErrServerClosed) {
			errCh <- serveErr
		}
	}()

	var database *db.DB
	var srv *server.Server
	var syncer *ghclient.Syncer
	var telemetryReporter *telemetry.Reporter
	var profilerSrv *profiler.Server
	var notificationLoops *notificationLoopHandle
	defer func() {
		if err := waitForRuntimeShutdownDelay(); err != nil {
			slog.Warn("test shutdown delay", "err", err)
		}
		for _, shutdownErr := range runMainShutdown(
			context.Background(),
			mainShutdownCallbacks{
				StopSignals: stopSignalsOnce,
				StopNotificationLoops: func(ctx context.Context) error {
					if notificationLoops == nil {
						return nil
					}
					return notificationLoops.Stop(ctx)
				},
				ShutdownPrimaryHTTP: func(shutdownCtx context.Context) error {
					if srv != nil {
						return srv.Shutdown(shutdownCtx)
					}
					return httpSrv.Shutdown(shutdownCtx)
				},
				StopSyncer: func() {
					if syncer != nil {
						syncer.Stop()
					}
				},
				ShutdownProfiler: func(ctx context.Context) error {
					if profilerSrv != nil {
						return profilerSrv.Shutdown(ctx)
					}
					return nil
				},
				CloseTelemetry: func() error {
					if telemetryReporter == nil {
						return nil
					}
					return telemetryReporter.Close()
				},
				CloseDatabase: func() error {
					if database == nil {
						return nil
					}
					return database.Close()
				},
			},
		) {
			slog.Warn(shutdownErr.message, "err", shutdownErr.err)
		}
	}()

	select {
	case <-serveReady:
	case <-ctx.Done():
		return fmt.Errorf("wait for HTTP server readiness: %w", ctx.Err())
	case serveErr := <-errCh:
		return fmt.Errorf("start HTTP server: %w", serveErr)
	}
	if err := waitForRuntimeGate(ctx, runtimePublishGatePath, "publish"); err != nil {
		return err
	}
	runtimePath, err := daemonruntime.Publish(runtimeIdentity.Record)
	if err != nil {
		return fmt.Errorf("write daemon runtime record: %w", err)
	}
	defer func() {
		if err := os.Remove(runtimePath); err != nil && !errors.Is(err, os.ErrNotExist) {
			slog.Warn("remove daemon runtime record", "err", err)
		}
	}()
	if err := waitForRuntimeGate(ctx, runtimeServeGatePath, "serve"); err != nil {
		return err
	}

	slog.Info(fmt.Sprintf("starting server at http://%s", ln.Addr().String()))

	if ctx.Err() != nil {
		slog.Info("shutting down")
		return nil
	}

	profilerSrv, err = profiler.Start(opts.ProfilerAddr)
	if err != nil {
		return err
	}
	if profilerSrv != nil {
		profilerAddr := ""
		if addr := profilerSrv.Addr(); addr != nil {
			profilerAddr = addr.String()
		}
		slog.Info(
			"starting profiler listener",
			"addr", profilerAddr,
		)
	}

	database, err = db.Open(cfg.DBPath())
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}

	tokenSources := tokenauth.NewSourceSet(tokenauth.Options{
		GitHubCLI: config.GitHubCLITokenForHost,
		GitHubApp: func(
			ctx context.Context, candidate tokenauth.Candidate,
		) (string, time.Time, error) {
			return githubapp.MintInstallationToken(
				ctx, candidate.Host, candidate.AppID,
				candidate.FilePath, candidate.InstallationID,
			)
		},
	})
	providerSources, err := collectProviderTokenSources(ctx, cfg, tokenSources)
	if err != nil {
		return err
	}

	startup, err := buildProviderStartup(
		ctx, database, cfg, tokenSources, providerSources,
		defaultProviderFactories(), ghclient.HTTPIdentityResolver{},
	)
	if err != nil {
		if ctx.Err() != nil && errors.Is(err, context.Canceled) {
			slog.Info("shutting down")
			return nil
		}
		return err
	}

	repos := resolveStartupRepos(
		ctx, cfg, providerRegistryForSyncPolicy(startup.registry, opts.DisableSync),
		database, startup.githubRouters,
	)
	slog.Debug("startup repos resolved", "count", len(repos))

	if ctx.Err() != nil {
		slog.Info("shutting down")
		return nil
	}

	cloneMgr := gitclone.New(
		filepath.Join(cfg.DataDir, "clones"), &startup,
	)

	syncer = ghclient.NewSyncerWithRegistry(
		startup.registry, database, cloneMgr, repos,
		cfg.SyncDuration(), startup.rateTrackers, startup.budgets,
	)
	if opts.DisableSync {
		syncer.DisableSync()
	}
	syncer.SetBranchActivityLimits(
		cfg.BranchActivityRetention(),
		cfg.Activity.DefaultBranchMaxCommits,
	)
	syncer.SetWatchInterval(cfg.ActivePRRefreshDuration())
	syncer.SetActiveMRWindow(cfg.ActivePRWindowDuration())
	syncer.SetPreferGitHubNativeStacks(cfg.PullRequests.PreferGitHubNativeStacks)
	syncer.SetFetchers(startup.fetchers)
	syncer.SetGitHubRouters(startup.githubRouters)
	syncer.SetRatePrincipalLabels(startup.ratePrincipalLabels)
	syncer.SetQuotaRegistry(startup.quotaRegistry)
	syncer.SetWriteRateTrackers(startup.writeRateTrackers)
	syncer.SetWriteGQLRateTrackers(startup.writeGQLRateTrackers)
	archiveService, err := archive.NewService(
		database, syncer.DirectRegistry(), syncer, syncer, nil, nil,
	)
	if err != nil {
		return fmt.Errorf("create archive service: %w", err)
	}
	archiveService.SetMaintenanceInterval(cfg.SyncDuration())
	archiveService.SetWake(syncer.WakeArchive)
	syncer.SetArchiveService(archiveService)
	if err := syncer.SetReposWithContext(ctx, repos, false); err != nil {
		return fmt.Errorf("prepare archive repositories: %w", err)
	}

	telemetryReporter = telemetry.NewReporterOrDisabled(telemetry.Options{
		Database: database,
		Version:  version,
		Commit:   commit,
	})
	if telemetryReporter.Enabled() {
		if err := telemetryReporter.Capture("daemon_active", map[string]any{
			"repo_count": len(repos),
		}); err != nil {
			slog.Warn("capture telemetry event", "err", err)
		}
	}

	srv = server.NewWithConfig(
		database, syncer, cloneMgr, assets,
		cfg, configPath, server.ServerOptions{
			DaemonAccess:        daemonAccess,
			WorktreeDir:         filepath.Join(cfg.DataDir, "worktrees"),
			PtyOwnerManagerPath: os.Getenv("KENN_FORGE_PTY_MANAGER"),
			Telemetry:           telemetryReporter,
			TokenSources:        tokenSources,
			Archive:             archiveService,
		},
	)
	srv.AttachHTTPServer(httpSrv, ln)
	slog.Debug(
		"server initialized",
		"base_path", cfg.BasePath,
		"worktree_dir", filepath.Join(cfg.DataDir, "worktrees"),
	)

	// Wire status callback and prime the SSE event hub so clients
	// can show live sync state without polling.
	syncer.SetOnStatusChange(func(status *ghclient.SyncStatus) {
		srv.Hub().Broadcast(server.Event{
			Type: "sync_status",
			Data: status,
		})
		if !status.Running {
			srv.Hub().Broadcast(server.Event{
				Type: "data_changed",
				Data: struct{}{},
			})
		}
	})
	srv.Hub().Broadcast(server.Event{
		Type: "sync_status",
		Data: syncer.Status(),
	})

	// Notification sync runs on its own timer and can backfill rows older
	// than the activity feed's top cursor, so broadcast the same
	// data-change signal the normal sync uses to nudge a full reload.
	syncer.SetOnNotificationSyncComplete(func() {
		srv.Hub().Broadcast(server.Event{
			Type: "data_changed",
			Data: struct{}{},
		})
	})
	syncer.SetOnWatchedMRSyncCompleted(func() {
		srv.Hub().Broadcast(server.Event{
			Type: "data_changed",
			Data: struct{}{},
		})
	})

	// The branch-match recompute runs first, then chains to stack
	// detection, mirroring the embedding API wiring in forge.go. The
	// syncer is the watched-MR setter.
	syncer.SetOnSyncCompleted(
		fleetapi.WorktreeLinksSyncHook(
			ctx, database, syncer,
			srv.Fleet().NotifyWorktreeLinksChanged,
			stacks.SyncCompletedHook(ctx, database, nil),
		),
	)
	syncer.Start(ctx)
	if !opts.DisableSync && cfg.NotificationsEnabled() {
		notificationLoops = startNotificationLoops(ctx, syncer, cfg)
	}

	otelShutdown, err := oteltelemetry.Init(ctx)
	if err != nil {
		return err
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(
			context.Background(), shutdownbudget.OpenTelemetry,
		)
		defer cancel()
		if err := otelShutdown(shutdownCtx); err != nil {
			slog.Warn("telemetry shutdown failed", "err", err)
		}
	}()

	srv.SetBuildInfo(server.BuildInfo{
		Name:      "kenn-forge",
		Version:   version,
		Commit:    commit,
		BuildDate: buildDate,
	})
	switcher.Swap(srv)

	select {
	case <-ctx.Done():
		slog.Info("shutting down")
		return nil
	case err := <-profilerSrvDone(profilerSrv):
		return fmt.Errorf("profiler: %w", err)
	case err := <-errCh:
		return fmt.Errorf("server: %w", err)
	}
}

// waitForRuntimeGate is enabled only by an ldflags-injected test build.
func waitForRuntimeGate(ctx context.Context, gatePath, phase string) error {
	if gatePath == "" {
		return nil
	}
	if err := os.WriteFile(gatePath+".ready", []byte("ready\n"), 0o600); err != nil {
		return fmt.Errorf("%s runtime gate readiness: %w", phase, err)
	}
	defer func() { _ = os.Remove(gatePath + ".ready") }()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(gatePath); errors.Is(err, os.ErrNotExist) {
			return nil
		} else if err != nil {
			return fmt.Errorf("inspect runtime %s gate: %w", phase, err)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for runtime %s gate: %w", phase, ctx.Err())
		case <-ticker.C:
		}
	}
}

type mainShutdownCallbacks struct {
	StopSignals           func()
	StopNotificationLoops func(context.Context) error
	ShutdownPrimaryHTTP   func(context.Context) error
	StopSyncer            func()
	ShutdownProfiler      func(context.Context) error
	CloseTelemetry        func() error
	CloseDatabase         func() error
}

type mainShutdownError struct {
	message string
	err     error
}

func runMainShutdown(
	ctx context.Context,
	callbacks mainShutdownCallbacks,
) []mainShutdownError {
	var errs []mainShutdownError
	if callbacks.StopSignals != nil {
		callbacks.StopSignals()
	}
	if callbacks.StopNotificationLoops != nil {
		if err := runContextShutdown(
			ctx, shutdownbudget.NotificationLoop,
			callbacks.StopNotificationLoops,
		); err != nil {
			errs = append(errs, mainShutdownError{
				message: "notification loops shutdown",
				err:     err,
			})
		}
	}
	if callbacks.ShutdownPrimaryHTTP != nil {
		if err := runContextShutdown(
			ctx, shutdownbudget.PrimaryHTTP, callbacks.ShutdownPrimaryHTTP,
		); err != nil {
			errs = append(errs, mainShutdownError{
				message: "server shutdown",
				err:     err,
			})
		}
	}
	if callbacks.StopSyncer != nil {
		callbacks.StopSyncer()
	}
	if callbacks.ShutdownProfiler != nil {
		if err := runContextShutdown(
			ctx, shutdownbudget.Profiler, callbacks.ShutdownProfiler,
		); err != nil {
			errs = append(errs, mainShutdownError{
				message: "profiler shutdown",
				err:     err,
			})
		}
	}
	if callbacks.CloseTelemetry != nil {
		if err := runBoundedShutdown(
			ctx, shutdownbudget.TelemetryReporter, callbacks.CloseTelemetry,
		); err != nil {
			errs = append(errs, mainShutdownError{
				message: "close telemetry",
				err:     err,
			})
		}
	}
	if callbacks.CloseDatabase != nil {
		if err := runBoundedShutdown(
			ctx, shutdownbudget.Database, callbacks.CloseDatabase,
		); err != nil {
			errs = append(errs, mainShutdownError{
				message: "close database",
				err:     err,
			})
		}
	}
	return errs
}

func runContextShutdown(
	parent context.Context,
	timeout time.Duration,
	shutdown func(context.Context) error,
) error {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	return shutdown(ctx)
}

func runBoundedShutdown(
	parent context.Context,
	timeout time.Duration,
	shutdown func() error,
) error {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- shutdown() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func waitForRuntimeShutdownDelay() error {
	if runtimeShutdownDelay == "" {
		return nil
	}
	delay, err := time.ParseDuration(runtimeShutdownDelay)
	if err != nil {
		return fmt.Errorf("parse duration %q: %w", runtimeShutdownDelay, err)
	}
	time.Sleep(delay)
	return nil
}

func profilerSrvDone(srv *profiler.Server) <-chan error {
	if srv == nil {
		return nil
	}
	return srv.Done()
}

func providerRegistryForSyncPolicy(
	registry *platform.Registry,
	disableSync bool,
) *platform.Registry {
	if !disableSync {
		return registry
	}
	return registry.WithProviderGate(func() error { return ghclient.ErrSyncDisabled })
}

func resolveStartupRepos(
	ctx context.Context,
	cfg *config.Config,
	registry *platform.Registry,
	database *db.DB,
	githubRouters map[string]*ghclient.HostRouter,
) []ghclient.RepoRef {
	set := ghclient.NewExpandedRepoSet()
	for _, raw := range cfg.Repos {
		_, expanded, err := ghclient.ResolveConfiguredRepoWithRegistry(
			ctx, registry, raw,
		)
		if err != nil {
			slog.Warn("resolve configured repo", "err", err)
			if raw.HasNameGlob() {
				expanded = fallbackGlobFromDB(
					ctx, database, raw,
				)
			} else {
				expanded = fallbackExactFromDB(ctx, database, raw)
				if len(expanded) == 0 {
					expanded = ghclient.FallbackConfiguredRepoRefs(nil, raw)
				} else {
					// The catalog recovered a stable identity on a renamed
					// route; without the alias, repo-scoped credentials for
					// the configured route would fall through to owner or
					// host credentials.
					ghclient.RegisterConfiguredRepoCredentialAliases(
						githubRouters, raw, expanded,
					)
				}
			}
		} else {
			ghclient.RegisterConfiguredRepoCredentialAliases(
				githubRouters, raw, expanded,
			)
		}
		for _, repo := range expanded {
			set.Add(repo, err == nil)
		}
	}
	return set.Refs()
}

func providerHostKey(platformName, host string) string {
	return strings.ToLower(platformName) + "\x00" + strings.ToLower(host)
}

func splitProviderHostKey(key string) (string, string) {
	platformName, host, ok := strings.Cut(key, "\x00")
	if !ok {
		return key, ""
	}
	return platformName, host
}

// fallbackExactFromDB recovers the stored identity for an exact config
// entry whose provider resolution failed at startup. Catalog route history
// follows a provider-side rename, so the fallback keeps the stable provider
// id — deduplicating against overlapping resolved entries — instead of
// synthesizing an identity-less ref under the stale configured route.
func fallbackExactFromDB(
	ctx context.Context,
	database *db.DB,
	raw config.Repo,
) []ghclient.RepoRef {
	if database == nil {
		return nil
	}
	repoPath := strings.TrimSpace(raw.RepoPath)
	if repoPath == "" {
		repoPath = raw.Owner + "/" + raw.Name
	}
	entries, err := database.ListRepositoryCatalog(ctx, db.RepositoryCatalogFilter{
		Platform:     raw.PlatformOrDefault(),
		PlatformHost: raw.PlatformHostOrDefault(),
		RepoPath:     repoPath,
		Lifecycle:    db.RepositoryLifecycleActive,
	})
	if err != nil {
		slog.Warn("fallback exact from db", "err", err)
		return nil
	}
	entry := catalogEntryForConfiguredRoute(entries, repoPath)
	if entry == nil {
		return nil
	}
	return []ghclient.RepoRef{{
		Platform:           platform.Kind(raw.PlatformOrDefault()),
		Owner:              entry.Repository.Owner,
		Name:               entry.Repository.Name,
		PlatformHost:       entry.Repository.PlatformHost,
		RepoPath:           entry.Repository.RepoPath,
		PlatformExternalID: entry.Repository.PlatformRepoID,
		WebURL:             entry.Repository.WebURL,
		CloneURL:           entry.Repository.CloneURL,
		DefaultBranch:      entry.Repository.DefaultBranch,
		ConfiguredRepoPath: repoPath,
	}}
}

// catalogEntryForConfiguredRoute prefers the repository currently occupying
// the configured route; a reused route may also historically match the
// renamed repository that held it before. Multiple historical matches with
// no current occupant cannot be attributed safely.
func catalogEntryForConfiguredRoute(
	entries []db.RepositoryCatalogEntry, repoPath string,
) *db.RepositoryCatalogEntry {
	for i := range entries {
		for _, route := range entries[i].Routes {
			if route.Current && strings.EqualFold(route.RepoPath, repoPath) {
				return &entries[i]
			}
		}
	}
	if len(entries) == 1 {
		return &entries[0]
	}
	return nil
}

// fallbackGlobFromDB returns repos from the database that match
// the glob config entry, preserving previously tracked matches
// when GitHub is unreachable at startup.
func fallbackGlobFromDB(
	ctx context.Context,
	database *db.DB,
	raw config.Repo,
) []ghclient.RepoRef {
	if database == nil {
		return nil
	}
	dbRepos, err := database.ListRepos(ctx)
	if err != nil {
		slog.Warn("fallback glob from db", "err", err)
		return nil
	}
	rawPlatform := platform.Kind(raw.PlatformOrDefault())
	host := raw.PlatformHostOrDefault()
	var matches []ghclient.RepoRef
	for _, r := range dbRepos {
		dbPlatform := platform.Kind(r.Platform)
		if dbPlatform == "" {
			dbPlatform = platform.KindGitHub
		}
		dbHost := r.PlatformHost
		if dbHost == "" {
			dbHost = platform.DefaultGitHubHost
		}
		if dbPlatform != rawPlatform ||
			!strings.EqualFold(dbHost, host) ||
			!strings.EqualFold(r.Owner, raw.Owner) {
			continue
		}
		matched, _ := path.Match(
			strings.ToLower(raw.Name),
			strings.ToLower(r.Name),
		)
		if matched {
			repo := ghclient.RepoRef{
				Platform:     rawPlatform,
				Owner:        r.Owner,
				Name:         r.Name,
				PlatformHost: dbHost,
			}
			matches = append(matches, repo)
		}
	}
	if len(matches) > 0 {
		slog.Info(
			"using DB-persisted repos for offline glob",
			"pattern", raw.Owner+"/"+raw.Name,
			"count", len(matches),
		)
	}
	return matches
}
