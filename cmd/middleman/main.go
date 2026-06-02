package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
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
	"syscall"
	"time"

	"go.kenn.io/middleman/internal/cli/ctl"
	"go.kenn.io/middleman/internal/cli/serve"
	"go.kenn.io/middleman/internal/config"
	"go.kenn.io/middleman/internal/db"
	"go.kenn.io/middleman/internal/gitclone"
	ghclient "go.kenn.io/middleman/internal/github"
	"go.kenn.io/middleman/internal/platform"
	"go.kenn.io/middleman/internal/profiler"
	"go.kenn.io/middleman/internal/ptyowner"
	"go.kenn.io/middleman/internal/runtimelock"
	"go.kenn.io/middleman/internal/server"
	"go.kenn.io/middleman/internal/stacks"
	"go.kenn.io/middleman/internal/telemetry"
	"go.kenn.io/middleman/internal/web"
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
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func configureLogging(stderr io.Writer) (func() error, error) {
	level, err := parseLogLevel(os.Getenv("MIDDLEMAN_LOG_LEVEL"))
	if err != nil {
		return nil, err
	}

	var file *os.File
	logFile := strings.TrimSpace(os.Getenv("MIDDLEMAN_LOG_FILE"))
	stderrLevel := level
	if logFile != "" {
		stderrLevel = slog.LevelInfo
	}
	if raw := os.Getenv("MIDDLEMAN_LOG_STDERR_LEVEL"); strings.TrimSpace(raw) != "" {
		stderrLevel, err = parseLogLevel(raw)
		if err != nil {
			return nil, err
		}
	}

	handlers := []slog.Handler{
		slog.NewTextHandler(
			stderr,
			&slog.HandlerOptions{Level: stderrLevel},
		),
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
			slog.NewTextHandler(
				file,
				&slog.HandlerOptions{Level: level},
			),
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
			"unsupported MIDDLEMAN_LOG_LEVEL %q", raw,
		)
	}
}

func runCLI(args []string, stdout io.Writer) error {
	if len(args) > 0 {
		switch args[0] {
		case "version":
			_, err := fmt.Fprintf(
				stdout,
				"middleman %s (%s) built %s\n",
				version, commit, buildDate,
			)
			return err
		case "config":
			return runConfigCLI(args[1:], stdout)
		case "pty-owner":
			return runPtyOwnerCLI(args[1:])
		case "status":
			return runStatusCLI(args[1:], stdout)
		case "serve":
			return serve.Run(args[1:], runServer)
		}
	}

	if ctl.IsInvocation(args) {
		return ctl.Execute(args, ctl.Options{
			Stdout: stdout,
			Stderr: os.Stderr,
		})
	}

	return serve.Run(args, runServer)
}

func runPtyOwnerCLI(args []string) error {
	fs := flag.NewFlagSet("middleman pty-owner", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	root := fs.String("root", "", "pty owner state root")
	session := fs.String("session", "", "session name")
	cwd := fs.String("cwd", "", "working directory")
	commandJSON := fs.String("command-json", "", "JSON command argv")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *session == "" {
		return fmt.Errorf("pty-owner session is required")
	}
	if *root == "" {
		return fmt.Errorf("pty-owner root is required")
	}
	if *cwd == "" {
		return fmt.Errorf("pty-owner cwd is required")
	}
	var command []string
	if *commandJSON != "" {
		if err := json.Unmarshal([]byte(*commandJSON), &command); err != nil {
			return fmt.Errorf("parse pty-owner command-json: %w", err)
		}
	}
	return ptyowner.RunOwner(context.Background(), ptyowner.Options{
		Root:    *root,
		Session: *session,
		Cwd:     *cwd,
		Command: command,
	})
}

func runConfigCLI(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("config command requires subcommand")
	}

	switch args[0] {
	case "read":
		return runConfigRead(args[1:], stdout)
	default:
		return fmt.Errorf("unknown config subcommand %q", args[0])
	}
}

func runConfigRead(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("middleman config read", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	configPath := fs.String(
		"config", config.DefaultConfigPath(),
		"path to config file",
	)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("config read requires exactly one key")
	}

	if err := config.EnsureDefault(*configPath); err != nil {
		return fmt.Errorf("ensure config: %w", err)
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	switch fs.Arg(0) {
	case "port":
		_, err := fmt.Fprintf(stdout, "%d\n", cfg.Port)
		return err
	default:
		return fmt.Errorf("unsupported config key %q", fs.Arg(0))
	}
}

func runStatusCLI(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("middleman status", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	configPath := fs.String(
		"config", config.DefaultConfigPath(),
		"path to config file",
	)
	asJSON := fs.Bool("json", false, "render output as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if err := config.EnsureDefault(*configPath); err != nil {
		return fmt.Errorf("ensure config: %w", err)
	}
	cfg, err := config.Load(*configPath)
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

	return runtimelock.FormatStatus(stdout, st, *asJSON)
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
				"another middleman is already running on %s",
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

	database, err := db.Open(cfg.DBPath())
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer database.Close()

	providerTokens, err := collectProviderTokens(cfg)
	if err != nil {
		return err
	}

	startup, err := buildProviderStartup(
		database, cfg, providerTokens, defaultProviderFactories(),
	)
	if err != nil {
		return err
	}

	repos := resolveStartupRepos(
		context.Background(), cfg, startup.registry, database,
	)
	slog.Debug("startup repos resolved", "count", len(repos))

	cloneMgr := gitclone.New(
		filepath.Join(cfg.DataDir, "clones"), startup.cloneTokens,
	)

	syncer := ghclient.NewSyncerWithRegistry(
		startup.registry, database, cloneMgr, repos,
		cfg.SyncDuration(), startup.rateTrackers, startup.budgets,
	)
	syncer.SetBranchActivityLimits(
		cfg.BranchActivityRetention(),
		cfg.Activity.DefaultBranchMaxCommits,
	)
	syncer.SetFetchers(startup.fetchers)

	telemetryReporter := telemetry.NewReporterOrDisabled(telemetry.Options{
		Database: database,
		Version:  version,
		Commit:   commit,
	})
	defer func() {
		if err := telemetryReporter.Close(); err != nil {
			slog.Warn("close telemetry", "err", err)
		}
	}()
	if telemetryReporter.Enabled() {
		if err := telemetryReporter.Capture("daemon_active", map[string]any{
			"repo_count": len(repos),
		}); err != nil {
			slog.Warn("capture telemetry event", "err", err)
		}
	}

	assets, err := web.Assets()
	if err != nil {
		return fmt.Errorf("load frontend assets: %w", err)
	}

	srv := server.NewWithConfig(
		database, syncer, cloneMgr, assets,
		cfg, configPath, server.ServerOptions{
			WorktreeDir:         filepath.Join(cfg.DataDir, "worktrees"),
			PtyOwnerManagerPath: os.Getenv("MIDDLEMAN_PTY_MANAGER"),
			Telemetry:           telemetryReporter,
		},
	)
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

	ctx, stop := signal.NotifyContext(
		context.Background(),
		syscall.SIGINT,
		syscall.SIGTERM,
	)
	syncer.SetOnSyncCompleted(stacks.SyncCompletedHook(ctx, database, nil))
	syncer.Start(ctx)
	defer syncer.Stop()
	defer stop()

	profilerSrv, err := profiler.Start(opts.ProfilerAddr)
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
		defer func() {
			shutdownCtx, cancel := context.WithTimeout(
				context.Background(), 5*time.Second,
			)
			defer cancel()
			if err := profilerSrv.Shutdown(shutdownCtx); err != nil {
				slog.Warn("profiler shutdown", "err", err)
			}
		}()
	}

	// srv.Shutdown MUST be the last-registered defer so LIFO runs
	// it FIRST on return: close the HTTP listener (and SSE hub)
	// before syncer.Stop blocks for up to 30 s, otherwise the
	// process keeps serving requests against a syncer that is
	// already winding down.
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(
			context.Background(), 10*time.Second,
		)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			slog.Warn("server shutdown", "err", err)
		}
	}()

	displayVersion := version
	if version == "dev" && commit != "unknown" {
		displayVersion = "dev-" + commit
	}
	srv.SetVersion(displayVersion)

	addr := cfg.ListenAddr()
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", addr, err)
	}

	if err := writeRuntimeMetadata(lockHandle, ln); err != nil {
		slog.Warn("write runtime metadata", "err", err)
	}

	slog.Info(fmt.Sprintf("starting server at http://%s", ln.Addr().String()))

	errCh := make(chan error, 1)
	go func() {
		if serveErr := srv.Serve(ln); !errors.Is(serveErr, http.ErrServerClosed) {
			errCh <- serveErr
		}
	}()

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

func profilerSrvDone(srv *profiler.Server) <-chan error {
	if srv == nil {
		return nil
	}
	return srv.Done()
}

// writeRuntimeMetadata snapshots the bound listener and process state
// into the runtime metadata file. The recorded port comes from
// ln.Addr() (not cfg.Port) so it matches the kernel-assigned value if
// they ever diverge.
func writeRuntimeMetadata(h *runtimelock.Handle, ln net.Listener) error {
	tcpAddr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		return fmt.Errorf("listener returned non-TCP address %T", ln.Addr())
	}
	return h.WriteMetadata(runtimelock.Metadata{
		PID:        os.Getpid(),
		Host:       tcpAddr.IP.String(),
		Port:       tcpAddr.Port,
		ListenAddr: ln.Addr().String(),
		StartedAt:  time.Now().UTC().Format(time.RFC3339),
		Version:    version,
		Commit:     commit,
	})
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

func validateProviderHostKeys(providerTokens map[string]string) error {
	type hostToken struct {
		platform string
		token    string
	}
	byHost := make(map[string]hostToken, len(providerTokens))
	for key, token := range providerTokens {
		platformName, host := splitProviderHostKey(key)
		if existing, ok := byHost[host]; ok {
			if existing.token != token {
				return fmt.Errorf(
					"host %s is configured for both %s and %s with different clone tokens; use identical tokens or separate hosts",
					host, existing.platform, platformName,
				)
			}
			continue
		}
		byHost[host] = hostToken{platform: platformName, token: token}
	}
	return nil
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
				Owner:        r.Owner,
				Name:         r.Name,
				PlatformHost: dbHost,
			}
			if rawPlatform != platform.KindGitHub {
				repo.Platform = rawPlatform
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
