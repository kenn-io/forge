package main

import (
	"context"
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

	"github.com/wesm/middleman/internal/config"
)

type ephemeralOptions struct {
	sourceConfigPath string
	workDir          string
	backendPort      int
	frontendPort     int
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
	PID          int    `json:"pid"`
	BackendPID   int    `json:"backend_pid"`
	FrontendPID  int    `json:"frontend_pid"`
	BackendPort  int    `json:"backend_port"`
	FrontendPort int    `json:"frontend_port"`
	ConfigPath   string `json:"config_path"`
	DataDir      string `json:"data_dir"`
	BackendURL   string `json:"backend_url"`
	FrontendURL  string `json:"frontend_url"`
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
	backendPort := fs.Int("backend-port", 0, "backend port (0 selects a free port)")
	frontendPort := fs.Int("frontend-port", 0, "frontend port (0 selects a free port)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	resolvedWorkDir := *workDir
	if resolvedWorkDir == "" {
		dir, err := os.MkdirTemp(filepath.Join("tmp"), "dev-ephemeral-")
		if err != nil {
			return fmt.Errorf("create work directory: %w", err)
		}
		resolvedWorkDir = dir
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

	prepared, err := prepareEphemeralConfig(ephemeralOptions{
		sourceConfigPath: *sourceConfigPath,
		workDir:          resolvedWorkDir,
		backendPort:      resolvedBackendPort,
		frontendPort:     resolvedFrontendPort,
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
		stopProcess(backend.Process)
		return fmt.Errorf("start frontend: %w", err)
	}

	status := ephemeralStatus{
		PID:          os.Getpid(),
		BackendPID:   backend.Process.Pid,
		FrontendPID:  frontend.Process.Pid,
		BackendPort:  prepared.backendPort,
		FrontendPort: prepared.frontendPort,
		ConfigPath:   prepared.configPath,
		DataDir:      prepared.dataDir,
		BackendURL:   prepared.backendURL,
		FrontendURL:  prepared.frontendURL,
	}
	if err := writeStatusFile(prepared.statusPath, status); err != nil {
		stopProcess(frontend.Process)
		stopProcess(backend.Process)
		return err
	}

	fmt.Printf("backend:  %s pid=%d\n", status.BackendURL, status.BackendPID)
	fmt.Printf("frontend: %s pid=%d\n", status.FrontendURL, status.FrontendPID)
	fmt.Printf("config:   %s\n", status.ConfigPath)
	fmt.Printf("status:   %s\n", prepared.statusPath)

	return waitForCommands(ctx, backend, frontend)
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

	dataDir := filepath.Join(opts.workDir, "data")
	logDir := filepath.Join(opts.workDir, "logs")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return ephemeralRun{}, fmt.Errorf("create data directory: %w", err)
	}
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		return ephemeralRun{}, fmt.Errorf("create log directory: %w", err)
	}

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
	frontendEnv := overlayEnv(baseEnv, map[string]string{
		"MIDDLEMAN_CONFIG":  run.configPath,
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

func startCommand(ctx context.Context, spec commandSpec) (*exec.Cmd, error) {
	cmd := exec.CommandContext(ctx, spec.name, spec.args...)
	cmd.Env = spec.env
	cmd.Dir = spec.dir
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

	stopProcess(backend.Process)
	stopProcess(frontend.Process)

	for i := consumed; i < 2; i++ {
		if err := <-errCh; err != nil && firstErr == nil {
			firstErr = err
		}
	}

	if errors.Is(firstErr, context.Canceled) {
		return nil
	}
	return firstErr
}

func commandWaitError(name string, err error) error {
	if err == nil {
		return nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ProcessState != nil && exitErr.Exited() {
		return fmt.Errorf("%s exited: %w", name, err)
	}
	return err
}

func stopProcess(process *os.Process) {
	if process == nil {
		return
	}
	_ = process.Signal(os.Interrupt)
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
