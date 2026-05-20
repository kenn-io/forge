//go:build !windows

package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/wesm/middleman/internal/config"
	"github.com/wesm/middleman/internal/procutil"
	_ "modernc.org/sqlite"
)

const defaultEphemeralWorkDir = "tmp/dev-ephemeral"

const (
	stopPollInterval   = 50 * time.Millisecond
	stopInterruptGrace = 500 * time.Millisecond
	stopTerminateGrace = 2 * time.Second
	stopWaitGrace      = 500 * time.Millisecond
)

type ephemeralOptions struct {
	sourceConfigPath string
	workDir          string
	backendPort      int
	frontendPort     int
	freshDB          bool
}

type ephemeralRun struct {
	configPath   string
	statusPath   string
	dataDir      string
	logDir       string
	backendURL   string
	frontendURL  string
	backendPort  int
	frontendPort int
}

type ephemeralStatus struct {
	PID               int    `json:"pid"`
	PIDStartedAt      string `json:"pid_started_at,omitempty"`
	BackendPID        int    `json:"backend_pid"`
	BackendStartedAt  string `json:"backend_started_at,omitempty"`
	FrontendPID       int    `json:"frontend_pid"`
	FrontendStartedAt string `json:"frontend_started_at,omitempty"`
	BackendPort       int    `json:"backend_port"`
	FrontendPort      int    `json:"frontend_port"`
	ConfigPath        string `json:"config_path"`
	DataDir           string `json:"data_dir"`
	BackendURL        string `json:"backend_url"`
	FrontendURL       string `json:"frontend_url"`
}

type processRef struct {
	pid       int
	startedAt string
}

type commandSpec struct {
	name string
	args []string
	env  []string
	dir  string
}

type commandSpecs struct {
	backend  commandSpec
	frontend commandSpec
}

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "dev-ephemeral: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("dev-ephemeral", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	sourceConfigPath := fs.String(
		"config", firstNonEmpty(os.Getenv("MIDDLEMAN_CONFIG"), config.DefaultConfigPath()),
		"source config file",
	)
	workDir := fs.String("work-dir", "", "directory for generated config, database, logs, and status JSON")
	statusPath := fs.String("status", "", "status JSON path for stopping an ephemeral dev stack")
	stop := fs.Bool("stop", false, "stop an ephemeral dev stack using its status JSON")
	backendPort := fs.Int("backend-port", 0, "backend port (0 selects a free port)")
	frontendPort := fs.Int("frontend-port", 0, "frontend port (0 selects a free port)")
	freshDB := fs.Bool("fresh-db", false, "start with an empty ephemeral database instead of copying the source database")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *stop {
		resolvedStatusPath, err := resolveStopStatusPath(*statusPath, *workDir)
		if err != nil {
			return err
		}
		releaseLock, err := lockEphemeralWorkDir(filepath.Dir(resolvedStatusPath))
		if err != nil {
			return err
		}
		defer func() { _ = releaseLock() }()
		if err := stopEphemeralStack(resolvedStatusPath); err != nil {
			return err
		}
		fmt.Printf("stopped ephemeral dev stack: %s\n", resolvedStatusPath)
		return nil
	}

	resolvedWorkDir, err := resolveRunWorkDir(*workDir)
	if err != nil {
		return err
	}

	resolvedBackendPort, err := resolvePort(*backendPort)
	if err != nil {
		return fmt.Errorf("resolve backend port: %w", err)
	}
	resolvedFrontendPort, err := resolvePort(*frontendPort)
	if err != nil {
		return fmt.Errorf("resolve frontend port: %w", err)
	}
	if resolvedBackendPort == resolvedFrontendPort {
		return fmt.Errorf("backend and frontend ports both resolved to %d", resolvedBackendPort)
	}

	releaseLock, err := lockEphemeralWorkDir(resolvedWorkDir)
	if err != nil {
		return err
	}
	locked := true
	defer func() {
		if locked {
			_ = releaseLock()
		}
	}()

	existingStatusPath := statusPathForWorkDir(resolvedWorkDir)
	existingStatus, running, err := readRunningEphemeralStatus(existingStatusPath)
	if err != nil {
		return err
	}
	if running {
		fmt.Printf("ephemeral dev stack already running\n")
		printStatus(existingStatus, existingStatusPath)
		return nil
	}

	prepared, err := prepareEphemeralConfig(ephemeralOptions{
		sourceConfigPath: *sourceConfigPath,
		workDir:          resolvedWorkDir,
		backendPort:      resolvedBackendPort,
		frontendPort:     resolvedFrontendPort,
		freshDB:          *freshDB,
	})
	if err != nil {
		return err
	}

	specs := buildCommandSpecs(prepared, fs.Args())
	backend, err := startCommand(ctx, specs.backend)
	if err != nil {
		return fmt.Errorf("start backend: %w", err)
	}
	frontend, err := startCommand(ctx, specs.frontend)
	if err != nil {
		return errors.Join(
			fmt.Errorf("start frontend: %w", err),
			errors.Join(stopStartedCommands(backend)...),
		)
	}
	launcherStartedAt, err := processStartTime(os.Getpid())
	if err != nil {
		return errors.Join(
			fmt.Errorf("read launcher process identity: %w", err),
			errors.Join(stopStartedCommands(frontend, backend)...),
		)
	}
	backendStartedAt, err := processStartTime(backend.Process.Pid)
	if err != nil {
		return errors.Join(
			fmt.Errorf("read backend process identity: %w", err),
			errors.Join(stopStartedCommands(frontend, backend)...),
		)
	}
	frontendStartedAt, err := processStartTime(frontend.Process.Pid)
	if err != nil {
		return errors.Join(
			fmt.Errorf("read frontend process identity: %w", err),
			errors.Join(stopStartedCommands(frontend, backend)...),
		)
	}

	status := ephemeralStatus{
		PID:               os.Getpid(),
		PIDStartedAt:      launcherStartedAt,
		BackendPID:        backend.Process.Pid,
		BackendStartedAt:  backendStartedAt,
		FrontendPID:       frontend.Process.Pid,
		FrontendStartedAt: frontendStartedAt,
		BackendPort:       prepared.backendPort,
		FrontendPort:      prepared.frontendPort,
		ConfigPath:        prepared.configPath,
		DataDir:           prepared.dataDir,
		BackendURL:        prepared.backendURL,
		FrontendURL:       prepared.frontendURL,
	}
	if err := writeStatusFile(prepared.statusPath, status); err != nil {
		return errors.Join(err, errors.Join(stopStartedCommands(frontend, backend)...))
	}

	printStatus(status, prepared.statusPath)

	if err := releaseLock(); err != nil {
		return err
	}
	locked = false

	return waitForCommands(ctx, backend, frontend)
}

func resolveRunWorkDir(workDir string) (string, error) {
	if strings.TrimSpace(workDir) != "" {
		return workDir, nil
	}
	return defaultEphemeralWorkDir, nil
}

func prepareEphemeralConfig(opts ephemeralOptions) (ephemeralRun, error) {
	if err := validatePort(opts.backendPort); err != nil {
		return ephemeralRun{}, fmt.Errorf("backend port: %w", err)
	}
	if err := validatePort(opts.frontendPort); err != nil {
		return ephemeralRun{}, fmt.Errorf("frontend port: %w", err)
	}
	if err := os.MkdirAll(opts.workDir, 0o700); err != nil {
		return ephemeralRun{}, fmt.Errorf("create work directory: %w", err)
	}
	if err := config.EnsureDefault(opts.sourceConfigPath); err != nil {
		return ephemeralRun{}, fmt.Errorf("ensure source config: %w", err)
	}

	cfg, err := config.Load(opts.sourceConfigPath)
	if err != nil {
		return ephemeralRun{}, fmt.Errorf("load source config: %w", err)
	}
	sourceDBPath := cfg.DBPath()

	dataDir := filepath.Join(opts.workDir, "data")
	logDir := filepath.Join(opts.workDir, "logs")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return ephemeralRun{}, fmt.Errorf("create data directory: %w", err)
	}
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		return ephemeralRun{}, fmt.Errorf("create log directory: %w", err)
	}
	if err := prepareEphemeralDatabase(sourceDBPath, filepath.Join(dataDir, "middleman.db"), !opts.freshDB); err != nil {
		return ephemeralRun{}, err
	}

	cfg.Host = "127.0.0.1"
	cfg.Port = opts.backendPort
	cfg.DataDir = dataDir

	configPath := filepath.Join(opts.workDir, "config.toml")
	if err := cfg.Save(configPath); err != nil {
		return ephemeralRun{}, fmt.Errorf("write generated config: %w", err)
	}

	backendURL, err := serverURL(cfg.Host, opts.backendPort, cfg.BasePath)
	if err != nil {
		return ephemeralRun{}, err
	}
	frontendURL, err := serverURL("127.0.0.1", opts.frontendPort, "/")
	if err != nil {
		return ephemeralRun{}, err
	}

	return ephemeralRun{
		configPath:   configPath,
		statusPath:   filepath.Join(opts.workDir, "dev-ephemeral.json"),
		dataDir:      dataDir,
		logDir:       logDir,
		backendURL:   backendURL,
		frontendURL:  frontendURL,
		backendPort:  opts.backendPort,
		frontendPort: opts.frontendPort,
	}, nil
}

func buildCommandSpecs(run ephemeralRun, frontendArgs []string) commandSpecs {
	baseEnv := os.Environ()
	backendEnv := overlayEnv(baseEnv, map[string]string{
		"MIDDLEMAN_CONFIG":           run.configPath,
		"MIDDLEMAN_LOG_LEVEL":        envDefault("MIDDLEMAN_LOG_LEVEL", "debug"),
		"MIDDLEMAN_LOG_FILE":         filepath.Join(run.logDir, "backend-dev.log"),
		"MIDDLEMAN_LOG_STDERR_LEVEL": envDefault("MIDDLEMAN_LOG_STDERR_LEVEL", "info"),
	})
	frontendEnv := overlayEnv(sanitizedFrontendEnv(baseEnv), map[string]string{
		"MIDDLEMAN_CONFIG": run.configPath,
		// frontend/vite.config.ts resolves its dev proxy from this value,
		// so random ephemeral backend ports do not fall back to defaults.
		"MIDDLEMAN_API_URL": run.backendURL,
	})
	args := append([]string{"--port", strconv.Itoa(run.frontendPort)}, frontendArgs...)
	return commandSpecs{
		backend: commandSpec{
			name: "./scripts/dev-stack-backend.sh",
			env:  backendEnv,
		},
		frontend: commandSpec{
			name: "./scripts/frontend-dev.sh",
			args: args,
			env:  frontendEnv,
		},
	}
}

func sanitizedFrontendEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, entry := range env {
		key, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if allowedFrontendEnvKey(key) {
			out = append(out, entry)
		}
	}
	return out
}

func allowedFrontendEnvKey(key string) bool {
	// Vite and Bun plugins can inspect process.env, so the frontend gets only
	// runtime basics and explicit non-secret dev-server controls.
	switch key {
	case "BUN_INSTALL", "BUN_INSTALL_CACHE_DIR",
		"CLICOLOR", "CLICOLOR_FORCE", "COLORTERM",
		"FORCE_COLOR", "HOME", "LANG", "LOGNAME",
		"MISE_CACHE_DIR", "MISE_CONFIG_DIR", "MISE_DATA_DIR",
		"MIDDLEMAN_VITE_ALLOWED_HOSTS", "MIDDLEMAN_VITE_HMR_CLIENT_PORT",
		"MIDDLEMAN_VITE_HMR_HOST", "MIDDLEMAN_VITE_HMR_PROTOCOL",
		"NO_COLOR", "PATH", "SHELL", "TEMP", "TERM", "TMP", "TMPDIR",
		"USER", "XDG_CACHE_HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME":
		return true
	}
	return strings.HasPrefix(key, "LC_")
}

func writeStatusFile(path string, status ephemeralStatus) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create status directory: %w", err)
	}
	content, err := json.MarshalIndent(status, "", "  ")
	if err != nil {
		return fmt.Errorf("encode status: %w", err)
	}
	content = append(content, '\n')
	if err := os.WriteFile(path, content, 0o644); err != nil {
		return fmt.Errorf("write status file: %w", err)
	}
	return nil
}

func printStatus(status ephemeralStatus, statusPath string) {
	fmt.Printf("backend:  %s pid=%d\n", status.BackendURL, status.BackendPID)
	fmt.Printf("frontend: %s pid=%d\n", status.FrontendURL, status.FrontendPID)
	fmt.Printf("config:   %s\n", status.ConfigPath)
	fmt.Printf("status:   %s\n", statusPath)
}

func statusPathForWorkDir(workDir string) string {
	return filepath.Join(workDir, "dev-ephemeral.json")
}

func resolveStopStatusPath(statusPath, workDir string) (string, error) {
	if strings.TrimSpace(statusPath) != "" {
		return statusPath, nil
	}
	if strings.TrimSpace(workDir) != "" {
		return statusPathForWorkDir(workDir), nil
	}
	return statusPathForWorkDir(defaultEphemeralWorkDir), nil
}

func lockEphemeralWorkDir(workDir string) (func() error, error) {
	if err := os.MkdirAll(workDir, 0o700); err != nil {
		return nil, fmt.Errorf("create work directory for lock: %w", err)
	}
	lockPath := filepath.Join(workDir, "dev-ephemeral.lock")
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open ephemeral work directory lock: %w", err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, fmt.Errorf("ephemeral work directory is locked: %s", workDir)
		}
		return nil, fmt.Errorf("lock ephemeral work directory: %w", err)
	}
	return func() error {
		unlockErr := syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		closeErr := file.Close()
		return errors.Join(unlockErr, closeErr)
	}, nil
}

func stopEphemeralStack(statusPath string) error {
	content, err := os.ReadFile(statusPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read status file: %w", err)
	}
	var status ephemeralStatus
	if err := json.Unmarshal(content, &status); err != nil {
		return fmt.Errorf("decode status file: %w", err)
	}

	var stopErrs []error
	refs, identityErrs := verifiedProcessRefs(statusProcessRefs(status))
	stopErrs = append(stopErrs, identityErrs...)
	stopErrs = append(stopErrs, stopEphemeralProcesses(refs)...)
	if len(stopErrs) == 0 {
		if err := os.Remove(statusPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			stopErrs = append(stopErrs, fmt.Errorf("remove status file: %w", err))
		}
	}
	return errors.Join(stopErrs...)
}

func readRunningEphemeralStatus(statusPath string) (ephemeralStatus, bool, error) {
	content, err := os.ReadFile(statusPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ephemeralStatus{}, false, nil
		}
		return ephemeralStatus{}, false, fmt.Errorf("read status file: %w", err)
	}
	var status ephemeralStatus
	if err := json.Unmarshal(content, &status); err != nil {
		return ephemeralStatus{}, false, fmt.Errorf("decode status file: %w", err)
	}
	running, err := allEphemeralProcessesRunning(status)
	if err != nil {
		return ephemeralStatus{}, false, err
	}
	if running {
		return status, true, nil
	}
	refs, identityErrs := verifiedProcessRefs(statusProcessRefs(status))
	stopErrs := append(identityErrs, stopEphemeralProcesses(refs)...)
	if len(stopErrs) > 0 {
		return ephemeralStatus{}, false, errors.Join(stopErrs...)
	}
	if err := os.Remove(statusPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return ephemeralStatus{}, false, fmt.Errorf("remove stale status file: %w", err)
	}
	return ephemeralStatus{}, false, nil
}

func allEphemeralProcessesRunning(status ephemeralStatus) (bool, error) {
	for _, ref := range statusProcessRefs(status) {
		if ref.pid <= 0 || ref.startedAt == "" {
			return false, nil
		}
		running, err := processMatchesStartTime(ref)
		if err != nil {
			return false, err
		}
		if !running {
			return false, nil
		}
	}
	return true, nil
}

func statusProcessRefs(status ephemeralStatus) []processRef {
	return []processRef{
		{pid: status.BackendPID, startedAt: status.BackendStartedAt},
		{pid: status.FrontendPID, startedAt: status.FrontendStartedAt},
		{pid: status.PID, startedAt: status.PIDStartedAt},
	}
}

func verifiedProcessRefs(refs []processRef) ([]processRef, []error) {
	var errs []error
	out := make([]processRef, 0, len(refs))
	seen := make(map[int]struct{}, len(refs))
	for _, ref := range refs {
		if ref.pid <= 0 || ref.startedAt == "" {
			continue
		}
		if _, ok := seen[ref.pid]; ok {
			continue
		}
		seen[ref.pid] = struct{}{}
		matches, err := processMatchesStartTime(ref)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if matches {
			out = append(out, ref)
		}
	}
	return out, errs
}

func stopEphemeralProcesses(refs []processRef) []error {
	var stopErrs []error
	for _, ref := range refs {
		if err := interruptProcessGroup(ref.pid); err != nil && !isNoSuchProcess(err) {
			stopErrs = append(stopErrs, fmt.Errorf("interrupt pid %d: %w", ref.pid, err))
		}
	}
	if len(stopErrs) > 0 {
		return stopErrs
	}
	running, waitErrs := waitForProcessesExit(refs, stopInterruptGrace)
	if len(waitErrs) > 0 {
		return waitErrs
	}
	if len(running) == 0 {
		return nil
	}
	for _, ref := range running {
		if err := terminateProcessGroup(ref.pid); err != nil && !isNoSuchProcess(err) {
			stopErrs = append(stopErrs, fmt.Errorf("terminate pid %d: %w", ref.pid, err))
		}
	}
	if len(stopErrs) > 0 {
		return stopErrs
	}
	running, waitErrs = waitForProcessesExit(running, stopTerminateGrace)
	if len(waitErrs) > 0 {
		return waitErrs
	}
	for _, ref := range running {
		stopErrs = append(stopErrs, fmt.Errorf("pid %d still running after shutdown timeout", ref.pid))
	}
	return stopErrs
}

func waitForProcessesExit(refs []processRef, timeout time.Duration) ([]processRef, []error) {
	deadline := time.Now().Add(timeout)
	for {
		running, errs := runningProcessRefs(refs)
		if len(errs) > 0 || len(running) == 0 || !time.Now().Before(deadline) {
			return running, errs
		}
		time.Sleep(stopPollInterval)
	}
}

func runningProcessRefs(refs []processRef) ([]processRef, []error) {
	var errs []error
	runningRefs := make([]processRef, 0, len(refs))
	for _, ref := range refs {
		running, err := processMatchesStartTime(ref)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if running {
			runningRefs = append(runningRefs, ref)
		}
	}
	return runningRefs, errs
}

func processRunning(pid int) (bool, error) {
	if pid <= 0 {
		return false, nil
	}
	err := syscall.Kill(pid, 0)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, syscall.ESRCH) {
		return false, nil
	}
	if errors.Is(err, syscall.EPERM) {
		return true, nil
	}
	return false, err
}

func processMatchesStartTime(ref processRef) (bool, error) {
	actual, err := processStartTime(ref.pid)
	if err != nil {
		if isNoSuchProcess(err) {
			return false, nil
		}
		return false, fmt.Errorf("check pid %d identity: %w", ref.pid, err)
	}
	return actual == ref.startedAt, nil
}

func processStartTime(pid int) (string, error) {
	if pid <= 0 {
		return "", os.ErrProcessDone
	}
	out, err := procutil.Command("ps", "-p", strconv.Itoa(pid), "-o", "lstart=").Output()
	startedAt := strings.TrimSpace(string(out))
	if startedAt == "" {
		return "", os.ErrProcessDone
	}
	if err != nil {
		return "", err
	}
	return startedAt, nil
}

func interruptPID(pid int) error {
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return process.Signal(os.Interrupt)
}

func interruptProcessGroup(pid int) error {
	if err := syscall.Kill(-pid, syscall.SIGINT); err == nil || !isNoSuchProcess(err) {
		return err
	}
	return interruptPID(pid)
}

func terminateProcessGroup(pid int) error {
	if err := syscall.Kill(-pid, syscall.SIGTERM); err == nil || !isNoSuchProcess(err) {
		return err
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return process.Signal(syscall.SIGTERM)
}

func isNoSuchProcess(err error) bool {
	return errors.Is(err, syscall.ESRCH) || errors.Is(err, os.ErrProcessDone)
}

func prepareEphemeralDatabase(sourcePath, destPath string, copyDB bool) error {
	samePath, err := sameFilesystemPath(sourcePath, destPath)
	if err != nil {
		return err
	}
	if samePath {
		return fmt.Errorf("source and destination database are the same: %s", destPath)
	}
	if err := removeSQLiteFiles(destPath); err != nil {
		return err
	}
	if !copyDB {
		return nil
	}
	if _, err := os.Stat(sourcePath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("stat source database: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(destPath), 0o700); err != nil {
		return fmt.Errorf("create database directory: %w", err)
	}
	source, err := sql.Open("sqlite", sourcePath+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return fmt.Errorf("open source database: %w", err)
	}
	defer source.Close()
	if _, err := source.Exec("VACUUM INTO ?", destPath); err != nil {
		return fmt.Errorf("copy source database snapshot: %w", err)
	}
	return nil
}

func sameFilesystemPath(left, right string) (bool, error) {
	leftAbs, err := filepath.Abs(left)
	if err != nil {
		return false, fmt.Errorf("resolve source database path: %w", err)
	}
	rightAbs, err := filepath.Abs(right)
	if err != nil {
		return false, fmt.Errorf("resolve destination database path: %w", err)
	}
	if filepath.Clean(leftAbs) == filepath.Clean(rightAbs) {
		return true, nil
	}
	leftInfo, err := os.Stat(leftAbs)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("stat source database: %w", err)
	}
	rightInfo, err := os.Stat(rightAbs)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("stat destination database: %w", err)
	}
	return os.SameFile(leftInfo, rightInfo), nil
}

func removeSQLiteFiles(path string) error {
	for _, candidate := range []string{path, path + "-wal", path + "-shm"} {
		if err := os.Remove(candidate); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove stale database file %s: %w", candidate, err)
		}
	}
	return nil
}

func startCommand(ctx context.Context, spec commandSpec) (*exec.Cmd, error) {
	cmd := procutil.CommandContext(ctx, spec.name, spec.args...)
	cmd.Env = spec.env
	cmd.Dir = spec.dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmd, nil
}

func waitForCommands(ctx context.Context, backend, frontend *exec.Cmd) error {
	ctx, stopSignals := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stopSignals()

	errCh := make(chan error, 2)
	go func() { errCh <- commandWaitError("backend", backend.Wait()) }()
	go func() { errCh <- commandWaitError("frontend", frontend.Wait()) }()

	var firstErr error
	consumed := 0
	select {
	case <-ctx.Done():
		firstErr = ctx.Err()
	case firstErr = <-errCh:
		consumed = 1
	}

	stopErr := errors.Join(stopForegroundProcesses(backend.Process, frontend.Process)...)

	for i := consumed; i < 2; i++ {
		select {
		case err := <-errCh:
			if err != nil && firstErr == nil {
				firstErr = err
			}
		case <-time.After(stopWaitGrace):
			if stopErr != nil {
				firstErr = stopErr
			} else if firstErr == nil || errors.Is(firstErr, context.Canceled) {
				firstErr = fmt.Errorf("timed out waiting for child shutdown")
			}
		}
	}

	if stopErr != nil && firstErr == nil {
		firstErr = stopErr
	}
	if errors.Is(firstErr, context.Canceled) {
		return nil
	}
	return firstErr
}

func stopStartedCommands(commands ...*exec.Cmd) []error {
	waitCh := make(chan error, len(commands))
	waiting := 0
	processes := make([]*os.Process, 0, len(commands))
	for _, cmd := range commands {
		if cmd == nil || cmd.Process == nil {
			continue
		}
		processes = append(processes, cmd.Process)
		waiting++
		go func(cmd *exec.Cmd) {
			waitCh <- commandWaitError(filepath.Base(cmd.Path), cmd.Wait())
		}(cmd)
	}

	stopErrs := stopForegroundProcesses(processes...)
	waitErrs := make([]error, 0, waiting)
	for i := 0; i < waiting; i++ {
		select {
		case err := <-waitCh:
			if err != nil {
				waitErrs = append(waitErrs, err)
			}
		case <-time.After(stopWaitGrace):
			waitErrs = append(waitErrs, fmt.Errorf("timed out waiting for child shutdown"))
		}
	}
	return append(stopErrs, waitErrs...)
}

func stopForegroundProcesses(processes ...*os.Process) []error {
	refs := make([]processRef, 0, len(processes))
	for _, process := range processes {
		if process == nil {
			continue
		}
		startedAt, err := processStartTime(process.Pid)
		if err != nil {
			continue
		}
		refs = append(refs, processRef{pid: process.Pid, startedAt: startedAt})
	}
	return stopEphemeralProcesses(refs)
}

func commandWaitError(name string, err error) error {
	if err == nil {
		return nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ProcessState != nil && exitErr.Exited() {
		return fmt.Errorf("%s exited: %w", name, err)
	}
	if errors.As(err, &exitErr) && exitErr.ProcessState != nil && processSignaledForShutdown(exitErr.ProcessState) {
		return nil
	}
	return err
}

func processSignaledForShutdown(state *os.ProcessState) bool {
	status, ok := state.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() {
		return false
	}
	return status.Signal() == syscall.SIGINT || status.Signal() == syscall.SIGTERM
}

func stopProcess(process *os.Process) {
	if process == nil {
		return
	}
	if err := interruptProcessGroup(process.Pid); err != nil && !isNoSuchProcess(err) {
		_ = process.Signal(os.Interrupt)
	}
}

func resolvePort(port int) (int, error) {
	if port != 0 {
		if err := validatePort(port); err != nil {
			return 0, err
		}
		return port, nil
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer listener.Close()
	addr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		return 0, fmt.Errorf("unexpected address type %T", listener.Addr())
	}
	return addr.Port, nil
}

func validatePort(port int) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("invalid port %d", port)
	}
	return nil
}

func serverURL(host string, port int, basePath string) (string, error) {
	if err := validatePort(port); err != nil {
		return "", err
	}
	value := strings.TrimSpace(host)
	if value == "" {
		value = "127.0.0.1"
	}
	u := url.URL{
		Scheme: "http",
		Host:   net.JoinHostPort(value, strconv.Itoa(port)),
	}
	normalizedBasePath := strings.TrimSuffix(basePath, "/")
	if normalizedBasePath != "" && normalizedBasePath != "/" {
		u.Path = normalizedBasePath
	}
	return u.String(), nil
}

func overlayEnv(env []string, values map[string]string) []string {
	out := make([]string, 0, len(env)+len(values))
	seen := make(map[string]struct{}, len(values))
	for _, entry := range env {
		key, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if _, alreadyReplaced := seen[key]; alreadyReplaced {
			continue
		}
		if value, replace := values[key]; replace {
			out = append(out, key+"="+value)
			seen[key] = struct{}{}
			continue
		}
		out = append(out, entry)
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if _, ok := seen[key]; ok {
			continue
		}
		out = append(out, key+"="+values[key])
	}
	return out
}

func envDefault(key, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
