package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
	"go.kenn.io/kit/daemon"
	"go.kenn.io/middleman/internal/config"
	"go.kenn.io/middleman/internal/daemonruntime"
	"go.kenn.io/middleman/internal/runtimelock"
)

const (
	backgroundStartTimeout = 90 * time.Second
	backgroundProbeTimeout = 750 * time.Millisecond
)

type backgroundRunner func(context.Context, string, io.Writer) error

type backgroundDiscovery struct {
	store   daemon.RuntimeStore
	dataDir string
	version string
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
	store, err := daemonruntime.Store()
	if err != nil {
		return err
	}
	manager := newBackgroundManager(
		store, cfg.DataDir, version,
		func(ctx context.Context) error {
			return startBackgroundProcess(ctx, configPath, cfg.DataDir)
		},
	)
	record, _, err := manager.Ensure(ctx, backgroundStartTimeout)
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

func newBackgroundManager(
	discoveryStore daemon.RuntimeStore,
	dataDir, expectedVersion string,
	start daemon.StartFunc,
) daemon.Manager {
	discovery := backgroundDiscovery{
		store: discoveryStore, dataDir: dataDir, version: expectedVersion,
	}
	return daemon.Manager{
		Store:    daemonruntime.StartLockStore(discoveryStore, dataDir),
		FindFunc: discovery.find,
		Start: func(ctx context.Context) error {
			if _, err := runtimelock.EnsureAuthToken(dataDir); err != nil {
				return fmt.Errorf("ensure auth token: %w", err)
			}
			if start == nil {
				return errors.New("middleman daemon is not running")
			}
			if err := start(ctx); err != nil {
				return fmt.Errorf("start middleman daemon: %w", err)
			}
			return nil
		},
	}
}

func (d backgroundDiscovery) find(
	ctx context.Context,
) (daemon.RuntimeRecord, daemon.PingInfo, bool, error) {
	token, err := runtimelock.ReadAuthToken(d.dataDir)
	if err != nil {
		return daemon.RuntimeRecord{}, daemon.PingInfo{}, false, err
	}
	records, err := d.store.List()
	if err != nil {
		return daemon.RuntimeRecord{}, daemon.PingInfo{}, false, err
	}
	for _, record := range records {
		ping, compatible, err := d.probe(ctx, record, token)
		if err != nil {
			return daemon.RuntimeRecord{}, daemon.PingInfo{}, false, err
		}
		if compatible {
			return record, ping, true, nil
		}
	}
	return daemon.RuntimeRecord{}, daemon.PingInfo{}, false, nil
}

func (d backgroundDiscovery) probe(
	ctx context.Context, record daemon.RuntimeRecord, token string,
) (daemon.PingInfo, bool, error) {
	if !daemonruntime.Compatible(record, daemonruntime.MatchOptions{
		DataDir:        d.dataDir,
		TokenAvailable: token != "",
	}) {
		return daemon.PingInfo{}, false, nil
	}

	endpoint := record.Endpoint()
	client := endpoint.HTTPClient(daemon.HTTPClientOptions{
		Timeout:           backgroundProbeTimeout,
		DisableKeepAlives: true,
	})
	client.Transport = daemonOriginTransport{
		token: token, origin: endpoint.BaseURL(), base: client.Transport,
	}
	ping, err := daemon.ProbeHTTP(
		ctx, client, endpoint.BaseURL(), daemon.ProbeOptions{
			Path:            "/api/ping",
			ExpectedService: daemonruntime.Service,
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
	if record.Version != d.version || ping.Version != d.version {
		return daemon.PingInfo{}, false, fmt.Errorf(
			"running middleman version %q is incompatible with %q",
			ping.Version, d.version,
		)
	}
	return ping, true, nil
}
