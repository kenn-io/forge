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
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/daemonruntime"
	"go.kenn.io/kit/daemon"
)

const backgroundStartTimeout = 90 * time.Second

type backgroundRunner func(context.Context, string, io.Writer) error

func newStartCommand(run backgroundRunner, stdout io.Writer) *cobra.Command {
	var background bool
	var configPath string
	cmd := &cobra.Command{
		Use:   "start",
		Short: "Ensure the kenn-forge daemon is running",
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
	cfg, err := config.LoadOrCreate(configPath)
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
	manager := daemonruntime.NewManager(
		store, cfg.DataDir, version,
		func(ctx context.Context) error {
			return startBackgroundProcess(ctx, configPath, cfg.DataDir)
		},
	)
	record, _, err := manager.Ensure(ctx, backgroundStartTimeout)
	if err != nil {
		return err
	}
	runtimeURL, err := daemonruntime.URL(record)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(
		stdout, "kenn-forge running at %s (pid %d)\n",
		runtimeURL, record.PID,
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
	return nil
}

func startBackgroundProcess(
	ctx context.Context, configPath, dataDir string,
) error {
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve kenn-forge executable: %w", err)
	}
	logFile, err := os.OpenFile(
		filepath.Join(dataDir, "forge.background.log"),
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
