package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/gofrs/flock"
	"github.com/spf13/cobra"
	"go.kenn.io/kit/daemon"
	"go.kenn.io/middleman/internal/config"
	"go.kenn.io/middleman/internal/runtimelock"
)

const (
	daemonServiceName      = "middleman"
	backgroundStartTimeout = 90 * time.Second
	backgroundProbeTimeout = 750 * time.Millisecond
	backgroundProbeTick    = 50 * time.Millisecond
)

type backgroundRunner func(context.Context, string, io.Writer) error

type backgroundLifecycle struct {
	store         daemon.RuntimeStore
	dataDir       string
	token         string
	authTokenPath string
	version       string
	prepare       func(*backgroundLifecycle) error
	start         daemon.StartFunc
}

func newStartCommand(run backgroundRunner, stdout io.Writer) *cobra.Command {
	var background bool
	var configPath string
	cmd := &cobra.Command{
		Use:   "start",
		Short: "Ensure the middleman daemon is running",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !background {
				return errors.New("start requires --background")
			}
			return run(cmd.Context(), configPath, stdout)
		},
	}
	cmd.Flags().BoolVar(&background, "background", false, "start in the background")
	cmd.Flags().StringVar(&configPath, "config", config.DefaultConfigPath(), "path to config file")
	return cmd
}

func startBackground(
	ctx context.Context, configPath string, stdout io.Writer,
) error {
	if err := config.EnsureDefault(configPath); err != nil {
		return fmt.Errorf("ensure config: %w", err)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if err := validateBackgroundConfig(cfg); err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return fmt.Errorf("create data directory %s: %w", cfg.DataDir, err)
	}
	dataDir, err := config.CanonicalDataDir(cfg.DataDir)
	if err != nil {
		return err
	}
	cfg.DataDir = dataDir
	token, err := runtimelock.ReadAuthToken(cfg.DataDir)
	if err != nil {
		return fmt.Errorf("read auth token: %w", err)
	}
	runtimeDir, err := filepath.Abs(config.DefaultDataDir())
	if err != nil {
		return fmt.Errorf("resolve runtime directory: %w", err)
	}
	lifecycle := backgroundLifecycle{
		store:         daemon.RuntimeStore{Dir: runtimeDir},
		dataDir:       cfg.DataDir,
		token:         token,
		authTokenPath: runtimelock.AuthTokenPath(cfg.DataDir),
		version:       version,
		prepare: func(l *backgroundLifecycle) error {
			token, err := runtimelock.EnsureAuthToken(cfg.DataDir)
			if err != nil {
				return fmt.Errorf("ensure auth token: %w", err)
			}
			l.token = token
			return nil
		},
		start: func(ctx context.Context) error {
			return startBackgroundProcess(ctx, configPath, cfg.DataDir)
		},
	}
	record, _, err := lifecycle.Ensure(ctx, backgroundStartTimeout)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(
		stdout, "middleman running at %s (pid %d)\n",
		record.Endpoint().BaseURL(), record.PID,
	)
	return err
}

func validateBackgroundConfig(cfg *config.Config) error {
	if !config.IsLoopbackHostname(cfg.Host) {
		return fmt.Errorf(
			"background start requires a loopback TCP listener, got %q",
			cfg.Host,
		)
	}
	if cfg.TrustReverseProxy && !cfg.API.RequireAuth {
		return errors.New(
			"background start with trust_reverse_proxy=true requires api.require_auth=true",
		)
	}
	return nil
}

func startBackgroundProcess(
	ctx context.Context, configPath, dataDir string,
) error {
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve middleman executable: %w", err)
	}
	logFile, err := os.OpenFile(
		filepath.Join(dataDir, "middleman.background.log"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600,
	)
	if err != nil {
		return fmt.Errorf("open background log: %w", err)
	}
	defer func() { _ = logFile.Close() }()
	return daemon.StartDetached(ctx, daemon.StartDetachedOptions{
		Executable:      executable,
		Args:            []string{"serve", "--config", configPath},
		Env:             os.Environ(),
		Stdout:          logFile,
		Stderr:          logFile,
		RefuseEphemeral: true,
	})
}

func (l backgroundLifecycle) Ensure(
	ctx context.Context, timeout time.Duration,
) (daemon.RuntimeRecord, daemon.PingInfo, error) {
	if timeout <= 0 {
		timeout = backgroundStartTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if record, ping, ok, err := l.find(ctx); err != nil || ok {
		return record, ping, err
	}
	lockPath, err := l.store.LockPath()
	if err != nil {
		return daemon.RuntimeRecord{}, daemon.PingInfo{}, err
	}
	lock := flock.New(lockPath)
	locked, err := lock.TryLockContext(ctx, backgroundProbeTick)
	if err != nil {
		return daemon.RuntimeRecord{}, daemon.PingInfo{}, fmt.Errorf(
			"acquire daemon start lock: %w", err,
		)
	}
	if !locked {
		return daemon.RuntimeRecord{}, daemon.PingInfo{}, ctx.Err()
	}
	defer func() { _ = lock.Unlock() }()

	if l.prepare != nil {
		if err := l.prepare(&l); err != nil {
			return daemon.RuntimeRecord{}, daemon.PingInfo{}, err
		}
	}
	if record, ping, ok, err := l.find(ctx); err != nil || ok {
		return record, ping, err
	}
	if l.start == nil {
		return daemon.RuntimeRecord{}, daemon.PingInfo{}, errors.New("middleman daemon is not running")
	}
	if err := l.start(ctx); err != nil {
		return daemon.RuntimeRecord{}, daemon.PingInfo{}, fmt.Errorf("start middleman daemon: %w", err)
	}

	ticker := time.NewTicker(backgroundProbeTick)
	defer ticker.Stop()
	for {
		record, ping, ok, err := l.find(ctx)
		if err != nil {
			return daemon.RuntimeRecord{}, daemon.PingInfo{}, err
		}
		if ok {
			return record, ping, nil
		}
		select {
		case <-ctx.Done():
			return daemon.RuntimeRecord{}, daemon.PingInfo{}, fmt.Errorf(
				"middleman failed to start within %s: %w", timeout, ctx.Err(),
			)
		case <-ticker.C:
		}
	}
}

func (l backgroundLifecycle) find(
	ctx context.Context,
) (daemon.RuntimeRecord, daemon.PingInfo, bool, error) {
	records, err := l.store.List()
	if err != nil {
		return daemon.RuntimeRecord{}, daemon.PingInfo{}, false, err
	}
	for _, record := range records {
		ping, compatible, err := l.probe(ctx, record)
		if err != nil {
			return daemon.RuntimeRecord{}, daemon.PingInfo{}, false, err
		}
		if compatible {
			return record, ping, true, nil
		}
	}
	return daemon.RuntimeRecord{}, daemon.PingInfo{}, false, nil
}

func (l backgroundLifecycle) probe(
	ctx context.Context, record daemon.RuntimeRecord,
) (daemon.PingInfo, bool, error) {
	if record.Service != daemonServiceName || record.Network != daemon.NetworkTCP ||
		record.Metadata["data_dir"] != l.dataDir || !daemon.ProcessAlive(record.PID) {
		return daemon.PingInfo{}, false, nil
	}
	if err := daemon.RequireLoopback(record.Address); err != nil {
		return daemon.PingInfo{}, false, nil
	}
	host, port, err := net.SplitHostPort(record.Address)
	if err != nil || record.Metadata["host"] != host || record.Metadata["port"] != port {
		return daemon.PingInfo{}, false, nil
	}
	readOnly, err := strconv.ParseBool(record.Metadata["read_only"])
	if err != nil || readOnly {
		return daemon.PingInfo{}, false, nil
	}
	requireAuth, err := strconv.ParseBool(record.Metadata["require_auth"])
	if err != nil {
		return daemon.PingInfo{}, false, nil
	}
	if requireAuth && (l.token == "" || record.Metadata["auth_token_path"] != l.authTokenPath) {
		return daemon.PingInfo{}, false, nil
	}
	if !requireAuth && record.Metadata["auth_token_path"] != "" {
		return daemon.PingInfo{}, false, nil
	}

	endpoint := record.Endpoint()
	client := endpoint.HTTPClient(daemon.HTTPClientOptions{
		Timeout:           backgroundProbeTimeout,
		DisableKeepAlives: true,
	})
	client.Transport = daemonOriginTransport{
		token: l.token, origin: endpoint.BaseURL(), base: client.Transport,
	}
	ping, err := daemon.ProbeHTTP(
		ctx, client, endpoint.BaseURL(), daemon.ProbeOptions{
			Path:            "/api/ping",
			ExpectedService: daemonServiceName,
			Timeout:         backgroundProbeTimeout,
		},
	)
	if err != nil {
		if ctx.Err() != nil {
			return daemon.PingInfo{}, false, ctx.Err()
		}
		return daemon.PingInfo{}, false, nil
	}
	if ping.PID != record.PID {
		return daemon.PingInfo{}, false, nil
	}
	if record.Version != l.version || ping.Version != l.version {
		return daemon.PingInfo{}, false, fmt.Errorf(
			"running middleman version %q is incompatible with %q",
			ping.Version, l.version,
		)
	}
	return ping, true, nil
}
