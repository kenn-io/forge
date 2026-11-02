package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/daemonruntime"
	"go.kenn.io/forge/internal/runtimelock"
	"go.kenn.io/kit/daemon"
)

const (
	backgroundStartTimeout = 90 * time.Second
	expectedDataDirEnv     = "KENN_FORGE_EXPECTED_DATA_DIR"
	backgroundLogName      = "forge.background.log"
	backgroundLogTailBytes = 4096
)

var backgroundReadinessGatePath string

func ensureBackground(
	ctx context.Context,
	configPath string,
	cfg *config.Config,
) (daemon.RuntimeRecord, error) {
	ctx, cancel := context.WithTimeout(ctx, backgroundStartTimeout)
	defer cancel()
	// A child that dies before it publishes a runtime record would otherwise
	// leave Ensure polling until the start deadline.
	ensureCtx, cancelEnsure := context.WithCancelCause(ctx)
	defer cancelEnsure(nil)
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
			return startBackgroundProcess(
				ctx, configPath, cfg.DataDir,
				func(err error) {
					// A concurrent daemon that holds the data-directory lock
					// makes the child exit by design; Ensure attaches to it.
					if status, statusErr := runtimelock.Read(cfg.DataDir); statusErr == nil && status.Running {
						return
					}
					cancelEnsure(backgroundExitError(cfg.DataDir, err))
				},
			)
		},
	)
	if err != nil {
		return daemon.RuntimeRecord{}, err
	}
	record, _, err := manager.Ensure(ensureCtx, backgroundStartTimeout)
	if err != nil {
		if exitErr, ok := errors.AsType[*backgroundExitedError](context.Cause(ensureCtx)); ok {
			return daemon.RuntimeRecord{}, exitErr
		}
		return daemon.RuntimeRecord{}, err
	}
	if err := waitForBackgroundReadiness(ctx, record, cfg.DataDir); err != nil {
		return daemon.RuntimeRecord{}, err
	}
	return record, nil
}

type backgroundExitedError struct {
	err     error
	logPath string
	lastLog string
}

func (e *backgroundExitedError) Error() string {
	exit := "exited"
	if e.err != nil {
		exit = e.err.Error()
	}
	msg := fmt.Sprintf("background daemon exited before it was ready (%s); see %s", exit, e.logPath)
	if e.lastLog != "" {
		msg += ": " + e.lastLog
	}
	return msg
}

func (e *backgroundExitedError) Unwrap() error { return e.err }

func backgroundExitError(dataDir string, exitErr error) error {
	logPath := filepath.Join(dataDir, backgroundLogName)
	return &backgroundExitedError{
		err:     exitErr,
		logPath: logPath,
		lastLog: lastBackgroundLogLine(logPath),
	}
}

// lastBackgroundLogLine returns the final line the exited child wrote, which
// holds its fatal error. The log is appended across launches, so only its tail
// is read. Read failures leave the line empty; the error still names the log.
func lastBackgroundLogLine(logPath string) string {
	file, err := os.Open(logPath)
	if err != nil {
		return ""
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return ""
	}
	offset := max(info.Size()-backgroundLogTailBytes, 0)
	tail, err := io.ReadAll(io.NewSectionReader(file, offset, info.Size()-offset))
	if err != nil {
		return ""
	}
	tail = bytes.TrimSpace(tail)
	if i := bytes.LastIndexByte(tail, '\n'); i >= 0 {
		tail = tail[i+1:]
	}
	return string(bytes.TrimSpace(tail))
}

func waitForBackgroundReadiness(
	ctx context.Context,
	record daemon.RuntimeRecord,
	dataDir string,
) error {
	readinessGatePath := backgroundReadinessGatePath
	for {
		ready, err := daemonruntime.IsVerifiedReady(ctx, record, dataDir)
		if err != nil {
			return fmt.Errorf("verify daemon readiness: %w", err)
		}
		if ready {
			return nil
		}
		status, err := runtimelock.Read(dataDir)
		if err != nil {
			return fmt.Errorf("inspect daemon readiness: %w", err)
		}
		if !status.Running {
			return errors.New("daemon exited before becoming ready")
		}
		if readinessGatePath != "" {
			if err := waitForRuntimeGate(
				ctx, readinessGatePath, "background readiness",
			); err != nil {
				return err
			}
			readinessGatePath = ""
		}
		timer := time.NewTimer(50 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
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
	ctx context.Context, configPath, dataDir string, exited func(error),
) error {
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve kenn-forge executable: %w", err)
	}
	logFile, err := os.OpenFile(
		filepath.Join(dataDir, backgroundLogName),
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
		Exited:          exited,
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
