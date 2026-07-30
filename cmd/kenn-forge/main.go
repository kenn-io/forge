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
	version   = "dev"
	commit    = "unknown"
	buildDate = "unknown"
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

func writeStatus(configPath string, asJSON bool, stdout io.Writer) error {
	if err := config.EnsureDefault(configPath); err != nil {
		return fmt.Errorf("ensure config: %w", err)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return fmt.Errorf(
			"create data directory %s: %w", cfg.DataDir, err,
		)
	}

	st, err := runtimelock.Read(cfg.DataDir)
	if err != nil {
		return fmt.Errorf("read runtime status: %w", err)
	}

	return runtimelock.FormatStatus(stdout, st, asJSON)
}

func run(opts serve.Options) error {
	configPath := opts.ConfigPath
	if err := config.EnsureDefault(configPath); err != nil {
		return fmt.Errorf("ensure config: %w", err)
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
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
		Version: version, Commit: commit, DataDir: cfg.DataDir,
		BasePath: cfg.BasePath, RequireAuth: cfg.API.RequireAuth,
	})
	if err != nil {
		_ = ln.Close()
		return fmt.Errorf("build daemon runtime identity: %w", err)
	}
	if err := lockHandle.WriteMetadata(runtimeIdentity.LockMetadata); err != nil {
		slog.Warn("write runtime metadata", "err", err)
	}
	runtimePath, err := daemonruntime.Publish(runtimeIdentity.Record)
	if err != nil {
		_ = ln.Close()
		return fmt.Errorf("write daemon runtime record: %w", err)
	}
	defer func() {
		if err := os.Remove(runtimePath); err != nil && !errors.Is(err, os.ErrNotExist) {
			slog.Warn("remove daemon runtime record", "err", err)
		}
	}()
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

	startupHandler := server.NewStartupHandler(
		assets, cfg, server.ServerOptions{}, ln,
	)
	switcher := server.NewSwitchHandler(startupHandler)
	httpSrv := &http.Server{
		Handler:     switcher,
		ReadTimeout: 15 * time.Second,
		// WriteTimeout is 0 (disabled) because SSE and proxy
		// responses are long-lived by design.
		IdleTimeout: 60 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() {
		if serveErr := httpSrv.Serve(ln); !errors.Is(serveErr, http.ErrServerClosed) {
			errCh <- serveErr
		}
	}()

	slog.Info(fmt.Sprintf("starting server at http://%s", ln.Addr().String()))

	var database *db.DB
	var srv *server.Server
	var syncer *ghclient.Syncer
	var telemetryReporter *telemetry.Reporter
	var profilerSrv *profiler.Server
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(
			context.Background(), 10*time.Second,
		)
		defer cancel()
		for _, shutdownErr := range runMainShutdown(
			shutdownCtx,
			mainShutdownCallbacks{
				StopSignals: stopSignalsOnce,
				ShutdownPrimaryHTTP: func(ctx context.Context) error {
					if srv != nil {
						return srv.Shutdown(ctx)
					}
					return httpSrv.Shutdown(ctx)
				},
				StopSyncer: func() {
					if syncer != nil {
						syncer.Stop()
					}
				},
				ShutdownProfiler: func(context.Context) error {
					if profilerSrv != nil {
						profilerCtx, profilerCancel := context.WithTimeout(
							context.Background(), 5*time.Second,
						)
						defer profilerCancel()
						return profilerSrv.Shutdown(profilerCtx)
					}
					return nil
				},
				CloseTelemetry: func() error {
					if telemetryReporter != nil {
						return telemetryReporter.Close()
					}
					return nil
				},
				CloseDatabase: func() error {
					if database != nil {
						return database.Close()
					}
					return nil
				},
			},
		) {
			slog.Warn(shutdownErr.message, "err", shutdownErr.err)
		}
	}()

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
		ctx, cfg, startup.registry, database,
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
		database, startup.registry, syncer, syncer, nil, nil,
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
			DaemonAccess: server.DaemonAccessOptions{
				Token: authToken, RequireAPIAuth: cfg.API.RequireAuth,
				ProofHandler: daemonProofHandler,
			},
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
	if cfg.NotificationsEnabled() {
		notificationLoops := startNotificationLoops(ctx, syncer, cfg)
		defer notificationLoops.Stop()
	}

	otelShutdown, err := oteltelemetry.Init(ctx)
	if err != nil {
		return err
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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

type mainShutdownCallbacks struct {
	StopSignals         func()
	ShutdownPrimaryHTTP func(context.Context) error
	StopSyncer          func()
	ShutdownProfiler    func(context.Context) error
	CloseTelemetry      func() error
	CloseDatabase       func() error
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
	if callbacks.ShutdownPrimaryHTTP != nil {
		if err := callbacks.ShutdownPrimaryHTTP(ctx); err != nil {
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
		if err := callbacks.ShutdownProfiler(ctx); err != nil {
			errs = append(errs, mainShutdownError{
				message: "profiler shutdown",
				err:     err,
			})
		}
	}
	if callbacks.CloseTelemetry != nil {
		if err := callbacks.CloseTelemetry(); err != nil {
			errs = append(errs, mainShutdownError{
				message: "close telemetry",
				err:     err,
			})
		}
	}
	if callbacks.CloseDatabase != nil {
		if err := callbacks.CloseDatabase(); err != nil {
			errs = append(errs, mainShutdownError{
				message: "close database",
				err:     err,
			})
		}
	}
	return errs
}

func profilerSrvDone(srv *profiler.Server) <-chan error {
	if srv == nil {
		return nil
	}
	return srv.Done()
}

func resolveStartupRepos(
	ctx context.Context,
	cfg *config.Config,
	registry *platform.Registry,
	database *db.DB,
) []ghclient.RepoRef {
	seen := make(map[string]struct{})
	repos := make([]ghclient.RepoRef, 0, len(cfg.Repos))
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
				expanded = ghclient.FallbackConfiguredRepoRefs(nil, raw)
			}
		}
		for _, repo := range expanded {
			key := string(repoPlatform(repo)) + "\x00" +
				strings.ToLower(repoHost(repo)) + "\x00" +
				strings.ToLower(repo.Owner) + "\x00" +
				strings.ToLower(repo.Name)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			repos = append(repos, repo)
		}
	}
	return repos
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

func repoPlatform(repo ghclient.RepoRef) platform.Kind {
	if repo.Platform != "" {
		return repo.Platform
	}
	return platform.KindGitHub
}

func repoHost(repo ghclient.RepoRef) string {
	if repo.PlatformHost != "" {
		return strings.ToLower(repo.PlatformHost)
	}
	if host, ok := platform.DefaultHost(repoPlatform(repo)); ok {
		return host
	}
	return platform.DefaultGitHubHost
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
