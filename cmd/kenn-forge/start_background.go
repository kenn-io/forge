package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/daemonruntime"
	"go.kenn.io/kit/daemon"
)

const (
	backgroundStartTimeout = 90 * time.Second
	expectedDataDirEnv     = "KENN_FORGE_EXPECTED_DATA_DIR"
)

func ensureBackground(
	ctx context.Context,
	configPath string,
	cfg *config.Config,
) (daemon.RuntimeRecord, error) {
	if err := validateBackgroundConfig(cfg); err != nil {
		return daemon.RuntimeRecord{}, err
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return daemon.RuntimeRecord{}, fmt.Errorf(
			"create data directory %s: %w", cfg.DataDir, err,
		)
	}
	store, err := daemonruntime.Store()
	if err != nil {
		return daemon.RuntimeRecord{}, err
	}
	manager, err := daemonruntime.NewManager(
		store, cfg.DataDir, version,
		func(ctx context.Context) error {
			return startBackgroundProcess(ctx, configPath, cfg.DataDir)
		},
	)
	if err != nil {
		return daemon.RuntimeRecord{}, err
	}
	record, _, err := manager.Ensure(ctx, backgroundStartTimeout)
	return record, err
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

func validateBackgroundLaunchConfig(cfg *config.Config) error {
	expectedDataDir := os.Getenv(expectedDataDirEnv)
	if expectedDataDir == "" {
		return nil
	}
	if err := os.Unsetenv(expectedDataDirEnv); err != nil {
		return fmt.Errorf("clear background launch identity: %w", err)
	}
	if cfg.DataDir != expectedDataDir {
		return fmt.Errorf(
			"background launch expected data_dir %q, but config resolved %q",
			expectedDataDir, cfg.DataDir,
		)
	}
	return validateBackgroundConfig(cfg)
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
		Env:             backgroundProcessEnvironment(dataDir),
		Stdout:          logFile,
		Stderr:          logFile,
		RefuseEphemeral: true,
	})
}

func backgroundProcessEnvironment(dataDir string) []string {
	prefix := expectedDataDirEnv + "="
	inherited := os.Environ()
	env := make([]string, 0, len(inherited)+1)
	for _, entry := range inherited {
		if !strings.HasPrefix(entry, prefix) {
			env = append(env, entry)
		}
	}
	return append(env, prefix+dataDir)
}
