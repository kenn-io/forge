package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"time"

	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/daemonruntime"
	"go.kenn.io/forge/internal/runtimelock"
	"go.kenn.io/forge/internal/shutdownbudget"
	"go.kenn.io/kit/daemon"
)

const daemonStopTimeout = shutdownbudget.Total

var errNoAuthoritativeRuntime = errors.New("no authoritative match")

type daemonLifecycleLock interface {
	Release() error
}

type daemonLifecycleLocks []daemonLifecycleLock

func (locks daemonLifecycleLocks) Release() error {
	errs := make([]error, 0, len(locks))
	for _, lock := range slices.Backward(locks) {
		if err := lock.Release(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

type daemonCommandRunner interface {
	Start(context.Context, string, io.Writer) error
	Status(context.Context, string, bool, io.Writer) error
	Stop(context.Context, string, io.Writer) error
	Restart(context.Context, string, io.Writer) error
}

type daemonLifecycleDeps struct {
	loadConfig         func(string) (*config.Config, error)
	store              func() (daemon.RuntimeStore, error)
	acquireConfigLock  func(context.Context, daemon.RuntimeStore, string) (daemonLifecycleLock, error)
	acquireLock        func(context.Context, daemon.RuntimeStore, string) (daemonLifecycleLock, error)
	findVerified       func(context.Context, daemon.RuntimeStore, string) (daemon.RuntimeRecord, daemon.PingInfo, bool, error)
	findVerifiedRecord func(context.Context, daemon.RuntimeRecord, string) (daemon.PingInfo, bool, error)
	readStatus         func(string) (runtimelock.Status, error)
	ensureNodeID       func(string) (string, error)
	ensureBackground   func(context.Context, string, *config.Config) (daemon.RuntimeRecord, error)
	signal             func(int) error
	waitForExit        func(context.Context, int, time.Duration) bool
	remove             func(string) error
	writeRuntimeStatus func(string, bool, io.Writer) error
}

type daemonLifecycle struct {
	deps daemonLifecycleDeps
}

type preparedDaemonMutation struct {
	config     *config.Config
	configPath string
	store      daemon.RuntimeStore
	locks      daemonLifecycleLocks
	target     *daemonruntime.ConfigRuntime
}

type daemonMutationOptions struct {
	requireLoopback         bool
	preferIdentifiedRuntime bool
}

func defaultDaemonLifecycleDeps() daemonLifecycleDeps {
	return daemonLifecycleDeps{
		loadConfig: config.LoadOrCreate,
		store:      daemonruntime.Store,
		acquireConfigLock: func(
			ctx context.Context,
			store daemon.RuntimeStore,
			configPath string,
		) (daemonLifecycleLock, error) {
			return daemonruntime.AcquireConfigLifecycleLock(
				ctx, store, configPath,
			)
		},
		acquireLock: func(
			ctx context.Context,
			store daemon.RuntimeStore,
			dataDir string,
		) (daemonLifecycleLock, error) {
			return daemonruntime.AcquireLifecycleLock(ctx, store, dataDir)
		},
		findVerified:       daemonruntime.FindVerified,
		findVerifiedRecord: daemonruntime.FindVerifiedRecord,
		readStatus:         runtimelock.Read,
		ensureNodeID:       runtimelock.EnsureNodeID,
		ensureBackground:   ensureBackground,
		signal:             signalDaemonProcess,
		waitForExit:        waitForDaemonExit,
		remove:             os.Remove,
		writeRuntimeStatus: writeRuntimeStatus,
	}
}

func newDaemonLifecycle(deps daemonLifecycleDeps) daemonCommandRunner {
	return &daemonLifecycle{deps: deps}
}

func (l *daemonLifecycle) Start(
	ctx context.Context,
	configPath string,
	stdout io.Writer,
) (resultErr error) {
	mutation, err := l.prepareConfigMutation(
		ctx, "daemon start", configPath,
		daemonMutationOptions{requireLoopback: true},
	)
	if err != nil {
		return err
	}
	defer joinLifecycleLockRelease("daemon start", mutation.locks, &resultErr)
	if err := l.reconcileStartTarget(ctx, mutation); err != nil {
		return err
	}

	record, err := l.deps.ensureBackground(ctx, mutation.configPath, mutation.config)
	if err != nil {
		if _, ok := errors.AsType[*daemonruntime.VersionMismatchError](err); ok {
			return fmt.Errorf(
				"daemon start: %w; run `kenn-forge daemon restart` to replace it",
				err,
			)
		}
		return fmt.Errorf("daemon start: %w", err)
	}
	if err := writeDaemonRunning(stdout, record); err != nil {
		return fmt.Errorf("daemon start: report running daemon: %w", err)
	}
	return nil
}

func (l *daemonLifecycle) Status(
	ctx context.Context,
	configPath string,
	asJSON bool,
	stdout io.Writer,
) error {
	canonicalConfigPath, err := daemonruntime.CanonicalConfigPath(configPath)
	if err != nil {
		return fmt.Errorf("daemon status: resolve config identity: %w", err)
	}
	store, err := l.deps.store()
	if err != nil {
		return fmt.Errorf("daemon status: resolve runtime store: %w", err)
	}
	target, err := l.identifiedRuntimeForConfig(
		ctx, "daemon status", store, canonicalConfigPath,
	)
	if err != nil && !errors.Is(err, errNoAuthoritativeRuntime) {
		return fmt.Errorf("daemon status: inspect config runtime identity: %w", err)
	}
	if target != nil {
		return l.deps.writeRuntimeStatus(target.DataDir, asJSON, stdout)
	}

	cfg, err := l.loadMutationConfig("daemon status", configPath, false)
	if err != nil {
		return err
	}
	target, err = l.runtimeForConfig(
		ctx, "daemon status", store, canonicalConfigPath, cfg.DataDir,
	)
	if err != nil {
		if errors.Is(err, errNoAuthoritativeRuntime) {
			status, statusErr := l.deps.readStatus(cfg.DataDir)
			if statusErr != nil {
				return fmt.Errorf(
					"daemon status: inspect configured runtime status: %w",
					statusErr,
				)
			}
			if status.Running {
				return l.deps.writeRuntimeStatus(cfg.DataDir, asJSON, stdout)
			}
		}
		return fmt.Errorf("daemon status: inspect config runtime identity: %w", err)
	}
	dataDir := cfg.DataDir
	if target != nil {
		dataDir = target.DataDir
	}
	return l.deps.writeRuntimeStatus(dataDir, asJSON, stdout)
}

func (l *daemonLifecycle) reconcileStartTarget(
	ctx context.Context,
	mutation *preparedDaemonMutation,
) error {
	target := mutation.target
	if target == nil {
		return nil
	}
	_, found, err := l.deps.findVerifiedRecord(ctx, target.Record, target.DataDir)
	if err != nil {
		return fmt.Errorf("daemon start: authenticate config runtime: %w", err)
	}
	if found {
		if target.DataDir != mutation.config.DataDir {
			return fmt.Errorf(
				"daemon start: config already has a daemon running from data_dir %q; run `kenn-forge daemon restart` to replace it",
				target.DataDir,
			)
		}
		return nil
	}
	status, err := l.deps.readStatus(target.DataDir)
	if err != nil {
		return fmt.Errorf("daemon start: read runtime status: %w", err)
	}
	if status.Running {
		return fmt.Errorf(
			"daemon start: authoritative runtime lock is held, but daemon identity could not be authenticated; stale runtime record was preserved",
		)
	}
	return l.removeRuntimeRecord(
		"daemon start", "unauthenticated", target.Record.SourcePath,
	)
}

func (l *daemonLifecycle) Stop(
	ctx context.Context,
	configPath string,
	stdout io.Writer,
) (resultErr error) {
	mutation, err := l.prepareConfigMutation(
		ctx, "daemon stop", configPath,
		daemonMutationOptions{preferIdentifiedRuntime: true},
	)
	if err != nil {
		return err
	}
	defer joinLifecycleLockRelease("daemon stop", mutation.locks, &resultErr)

	stopped, err := l.stopLocked(
		ctx, "daemon stop", configPath, mutation.config.DataDir,
		mutation.store, mutation.target,
	)
	if err != nil {
		return err
	}
	if stopped == nil {
		_, err = fmt.Fprintln(stdout, "kenn-forge daemon is not running")
		if err != nil {
			return fmt.Errorf("daemon stop: report status: %w", err)
		}
		return nil
	}
	_, err = fmt.Fprintf(stdout, "kenn-forge daemon stopped (pid %d)\n", stopped.PID)
	if err != nil {
		return fmt.Errorf("daemon stop: report status: %w", err)
	}
	return nil
}

func (l *daemonLifecycle) Restart(
	ctx context.Context,
	configPath string,
	stdout io.Writer,
) (resultErr error) {
	mutation, err := l.prepareConfigMutation(
		ctx, "daemon restart", configPath,
		daemonMutationOptions{requireLoopback: true},
	)
	if err != nil {
		return err
	}
	defer joinLifecycleLockRelease("daemon restart", mutation.locks, &resultErr)

	stopped, err := l.stopLocked(
		ctx, "daemon restart", configPath, mutation.config.DataDir,
		mutation.store, mutation.target,
	)
	if err != nil {
		return err
	}
	record, err := l.deps.ensureBackground(ctx, mutation.configPath, mutation.config)
	if err != nil {
		return fmt.Errorf("daemon restart: start replacement: %w", err)
	}
	if stopped == nil {
		if _, err := fmt.Fprintln(stdout, "kenn-forge daemon was not running before start"); err != nil {
			return fmt.Errorf("daemon restart: report prior status: %w", err)
		}
	}
	if err := writeDaemonRunning(stdout, record); err != nil {
		return fmt.Errorf("daemon restart: report running daemon: %w", err)
	}
	return nil
}

func (l *daemonLifecycle) prepareConfigMutation(
	ctx context.Context,
	operation, configPath string,
	opts daemonMutationOptions,
) (*preparedDaemonMutation, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", operation, err)
	}
	store, err := l.deps.store()
	if err != nil {
		return nil, fmt.Errorf("%s: resolve runtime store: %w", operation, err)
	}
	canonicalConfigPath, cfg, configLock, err := l.lockMutationConfig(
		ctx, operation, store, configPath, opts,
	)
	if err != nil {
		return nil, err
	}
	releaseConfigWith := func(cause error) error {
		return errors.Join(cause, releaseLifecycleLock(operation, configLock))
	}
	for {
		if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
			return nil, releaseConfigWith(fmt.Errorf(
				"%s: create data directory %s: %w", operation, cfg.DataDir, err,
			))
		}
		target, err := l.runtimeForConfig(
			ctx, operation, store, canonicalConfigPath, cfg.DataDir,
		)
		if err != nil {
			identityErr := fmt.Errorf(
				"%s: inspect config runtime identity: %w", operation, err,
			)
			return nil, releaseConfigWith(identityErr)
		}
		lockDataDirs := []string{cfg.DataDir}
		if target != nil {
			lockDataDirs = append(lockDataDirs, target.DataDir)
		}
		dataLocks, err := l.acquireMutationLocks(ctx, operation, store, lockDataDirs)
		if err != nil {
			return nil, releaseConfigWith(err)
		}
		locks := append(daemonLifecycleLocks{configLock}, dataLocks...)

		lockedDataDir := cfg.DataDir
		cfg, err = l.loadMutationConfigOrRuntime(
			ctx, operation, store, canonicalConfigPath, canonicalConfigPath,
			opts,
		)
		if err != nil {
			return nil, errors.Join(err, releaseLifecycleLock(operation, locks))
		}
		lockedTarget, err := l.runtimeForConfig(
			ctx, operation, store, canonicalConfigPath, cfg.DataDir,
		)
		if err != nil {
			identityErr := fmt.Errorf(
				"%s: recheck config runtime identity: %w", operation, err,
			)
			return nil, errors.Join(
				identityErr, releaseLifecycleLock(operation, locks),
			)
		}
		if cfg.DataDir != lockedDataDir || !sameConfigRuntime(target, lockedTarget) {
			if err := releaseLifecycleLock(operation, dataLocks); err != nil {
				return nil, releaseConfigWith(err)
			}
			continue
		}
		if _, err := l.deps.ensureNodeID(cfg.DataDir); err != nil {
			identityErr := fmt.Errorf("%s: ensure node ID: %w", operation, err)
			return nil, errors.Join(
				identityErr, releaseLifecycleLock(operation, locks),
			)
		}
		return &preparedDaemonMutation{
			config:     cfg,
			configPath: canonicalConfigPath,
			store:      store,
			locks:      locks,
			target:     lockedTarget,
		}, nil
	}
}

func (l *daemonLifecycle) lockMutationConfig(
	ctx context.Context,
	operation string,
	store daemon.RuntimeStore,
	configPath string,
	opts daemonMutationOptions,
) (string, *config.Config, daemonLifecycleLock, error) {
	for {
		canonicalPath, err := daemonruntime.CanonicalConfigPath(configPath)
		if err != nil {
			return "", nil, nil, fmt.Errorf(
				"%s: resolve config identity: %w", operation, err,
			)
		}
		lock, err := l.deps.acquireConfigLock(ctx, store, canonicalPath)
		if err != nil {
			return "", nil, nil, fmt.Errorf("%s: %w", operation, err)
		}
		cfg, err := l.loadMutationConfigOrRuntime(
			ctx, operation, store, canonicalPath, configPath,
			opts,
		)
		if err != nil {
			return "", nil, nil, errors.Join(
				err, releaseLifecycleLock(operation, lock),
			)
		}
		loadedPath, err := daemonruntime.CanonicalConfigPath(configPath)
		if err != nil {
			identityErr := fmt.Errorf(
				"%s: recheck config identity: %w", operation, err,
			)
			return "", nil, nil, errors.Join(
				identityErr, releaseLifecycleLock(operation, lock),
			)
		}
		if loadedPath == canonicalPath {
			return canonicalPath, cfg, lock, nil
		}
		if err := releaseLifecycleLock(operation, lock); err != nil {
			return "", nil, nil, err
		}
	}
}

func (l *daemonLifecycle) loadMutationConfigOrRuntime(
	ctx context.Context,
	operation string,
	store daemon.RuntimeStore,
	canonicalConfigPath, loadConfigPath string,
	opts daemonMutationOptions,
) (*config.Config, error) {
	if opts.preferIdentifiedRuntime {
		target, err := l.identifiedRuntimeForConfig(
			ctx, operation, store, canonicalConfigPath,
		)
		if err != nil && !errors.Is(err, errNoAuthoritativeRuntime) {
			return nil, fmt.Errorf(
				"%s: inspect config runtime identity: %w", operation, err,
			)
		}
		if target != nil {
			return &config.Config{DataDir: target.DataDir}, nil
		}
	}
	return l.loadMutationConfig(operation, loadConfigPath, opts.requireLoopback)
}

func (l *daemonLifecycle) runtimeForConfig(
	ctx context.Context,
	operation string,
	store daemon.RuntimeStore,
	configPath, currentDataDir string,
) (*daemonruntime.ConfigRuntime, error) {
	runtimes, err := daemonruntime.ConfigRuntimes(
		store, configPath, currentDataDir,
	)
	if err != nil {
		return nil, err
	}
	target, err := l.runtimeFromCandidates(
		ctx, operation, configPath, runtimes,
	)
	if err != nil || target == nil {
		return target, err
	}
	if target.ConfigPath == "" && target.DataDir != currentDataDir {
		return nil, fmt.Errorf(
			"live daemon pid %d uses a legacy runtime identity for data_dir %q and does not identify its config; stop it before changing data_dir",
			target.Record.PID, target.DataDir,
		)
	}
	return target, nil
}

func (l *daemonLifecycle) identifiedRuntimeForConfig(
	ctx context.Context,
	operation string,
	store daemon.RuntimeStore,
	configPath string,
) (*daemonruntime.ConfigRuntime, error) {
	runtimes, err := daemonruntime.IdentifiedConfigRuntimes(store, configPath)
	if err != nil {
		return nil, err
	}
	return l.runtimeFromCandidates(ctx, operation, configPath, runtimes)
}

func (l *daemonLifecycle) runtimeFromCandidates(
	ctx context.Context,
	operation, configPath string,
	runtimes []daemonruntime.ConfigRuntime,
) (*daemonruntime.ConfigRuntime, error) {
	if len(runtimes) == 0 {
		return nil, nil
	}

	authenticated := make([]*daemonruntime.ConfigRuntime, 0, len(runtimes))
	for index := range runtimes {
		candidate := &runtimes[index]
		matches, err := l.authenticatedRuntimeMatchesStatus(
			ctx, candidate,
		)
		if err != nil {
			return nil, err
		}
		if matches {
			authenticated = append(authenticated, candidate)
		}
	}
	if len(authenticated) > 1 {
		return nil, fmt.Errorf(
			"config %q has multiple live daemon runtime records and %d authoritative matches",
			configPath, len(authenticated),
		)
	}
	if len(authenticated) == 0 {
		stalePaths := make([]string, 0, len(runtimes))
		for index := range runtimes {
			candidate := &runtimes[index]
			status, err := l.readCleanupRuntimeStatus(candidate.DataDir)
			if err != nil {
				return nil, err
			}
			if status.Running {
				return nil, fmt.Errorf(
					"config %q has %d live daemon runtime records: %w",
					configPath, len(runtimes), errNoAuthoritativeRuntime,
				)
			}
			stalePaths = append(stalePaths, candidate.Record.SourcePath)
		}
		if err := l.removeStaleRuntimeRecords(operation, stalePaths); err != nil {
			return nil, err
		}
		return nil, nil
	}
	target := authenticated[0]
	var stalePaths []string
	for index := range runtimes {
		candidate := &runtimes[index]
		if sameConfigRuntime(target, candidate) {
			continue
		}
		removable, err := l.shadowedRuntimeCanBeRemoved(
			configPath, target, candidate,
		)
		if err != nil {
			return nil, err
		}
		if !removable {
			return nil, fmt.Errorf(
				"config %q has multiple authoritative daemon runtime records",
				configPath,
			)
		}
		stalePaths = append(stalePaths, candidate.Record.SourcePath)
	}
	if err := l.removeStaleRuntimeRecords(operation, stalePaths); err != nil {
		return nil, err
	}
	return target, nil
}

func (l *daemonLifecycle) removeStaleRuntimeRecords(
	operation string,
	paths []string,
) error {
	for _, path := range paths {
		if err := l.removeRuntimeRecord(
			operation, "stale unauthenticated", path,
		); err != nil {
			return err
		}
	}
	return nil
}

func (l *daemonLifecycle) authenticatedRuntimeMatchesStatus(
	ctx context.Context,
	candidate *daemonruntime.ConfigRuntime,
) (bool, error) {
	_, found, err := l.deps.findVerifiedRecord(
		ctx, candidate.Record, candidate.DataDir,
	)
	if err != nil {
		return false, fmt.Errorf("authenticate config runtime: %w", err)
	}
	if !found {
		return false, nil
	}
	status, err := l.deps.readStatus(candidate.DataDir)
	if err != nil {
		return false, fmt.Errorf("read authenticated runtime status: %w", err)
	}
	return runtimeStatusMatches(
		status, candidate.ConfigPath, candidate.Record.PID,
	)
}

func (l *daemonLifecycle) shadowedRuntimeCanBeRemoved(
	configPath string,
	target, candidate *daemonruntime.ConfigRuntime,
) (bool, error) {
	status, err := l.readCleanupRuntimeStatus(candidate.DataDir)
	if err != nil {
		return false, err
	}
	if !status.Running {
		return true, nil
	}
	return runtimeStatusMatches(status, configPath, target.Record.PID)
}

func (l *daemonLifecycle) readCleanupRuntimeStatus(
	dataDir string,
) (runtimelock.Status, error) {
	status, err := l.deps.readStatus(dataDir)
	if errors.Is(err, os.ErrNotExist) {
		return runtimelock.Status{DataDir: dataDir}, nil
	}
	if err != nil {
		return runtimelock.Status{}, fmt.Errorf(
			"read stale runtime status: %w", err,
		)
	}
	return status, nil
}

func sameConfigRuntime(
	first, second *daemonruntime.ConfigRuntime,
) bool {
	if first == nil || second == nil {
		return first == nil && second == nil
	}
	return first.DataDir == second.DataDir &&
		first.ConfigPath == second.ConfigPath &&
		first.Record.PID == second.Record.PID &&
		first.Record.SourcePath == second.Record.SourcePath &&
		first.Record.Network == second.Record.Network &&
		first.Record.Address == second.Record.Address &&
		first.Record.Service == second.Record.Service &&
		first.Record.Version == second.Record.Version
}

func (l *daemonLifecycle) loadMutationConfig(
	operation, configPath string,
	requireLoopback bool,
) (*config.Config, error) {
	cfg, err := l.deps.loadConfig(configPath)
	if err != nil {
		return nil, fmt.Errorf("%s: load config: %w", operation, err)
	}
	if requireLoopback {
		if err := validateBackgroundConfig(cfg); err != nil {
			return nil, fmt.Errorf("%s: %w", operation, err)
		}
	}
	return cfg, nil
}

func (l *daemonLifecycle) acquireMutationLocks(
	ctx context.Context,
	operation string,
	store daemon.RuntimeStore,
	dataDirs []string,
) (daemonLifecycleLocks, error) {
	uniqueDataDirs := append([]string(nil), dataDirs...)
	slices.Sort(uniqueDataDirs)
	uniqueDataDirs = slices.Compact(uniqueDataDirs)
	locks := make(daemonLifecycleLocks, 0, len(uniqueDataDirs))
	for _, dataDir := range uniqueDataDirs {
		lock, err := l.deps.acquireLock(ctx, store, dataDir)
		if err != nil {
			acquireErr := fmt.Errorf("%s: %w", operation, err)
			return nil, errors.Join(acquireErr, locks.Release())
		}
		locks = append(locks, lock)
	}
	return locks, nil
}

func (l *daemonLifecycle) stopLocked(
	ctx context.Context,
	operation, configPath, currentDataDir string,
	store daemon.RuntimeStore,
	target *daemonruntime.ConfigRuntime,
) (*daemon.RuntimeRecord, error) {
	dataDir := currentDataDir
	var record daemon.RuntimeRecord
	var found bool
	var err error
	if target == nil {
		record, _, found, err = l.deps.findVerified(ctx, store, dataDir)
	} else {
		dataDir = target.DataDir
		_, found, err = l.deps.findVerifiedRecord(ctx, target.Record, dataDir)
		record = target.Record
	}
	if err != nil {
		return nil, fmt.Errorf("%s: discover daemon: %w", operation, err)
	}
	if !found {
		status, err := l.deps.readStatus(dataDir)
		if err != nil {
			return nil, fmt.Errorf("%s: read runtime status: %w", operation, err)
		}
		if status.Running {
			return nil, fmt.Errorf(
				"%s: authoritative runtime lock is held, but daemon identity could not be authenticated; no process was signaled",
				operation,
			)
		}
		if target != nil {
			if err := l.removeRuntimeRecord(
				operation, "unauthenticated", target.Record.SourcePath,
			); err != nil {
				return nil, err
			}
		}
		return nil, nil
	}
	if target != nil && target.DataDir != currentDataDir {
		status, err := l.deps.readStatus(target.DataDir)
		if err != nil {
			return nil, fmt.Errorf("%s: read runtime status: %w", operation, err)
		}
		matches, err := runtimeStatusMatches(
			status, target.ConfigPath, record.PID,
		)
		if err != nil {
			return nil, fmt.Errorf("%s: validate runtime status identity: %w", operation, err)
		}
		if !matches {
			return nil, fmt.Errorf(
				"%s: authenticated daemon does not match config identity %q; no process was signaled",
				operation, configPath,
			)
		}
	}
	if err := l.deps.signal(record.PID); err != nil {
		return nil, fmt.Errorf("%s: signal process %d: %w", operation, record.PID, err)
	}
	if !l.deps.waitForExit(ctx, record.PID, daemonStopTimeout) {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("%s: wait for process %d: %w", operation, record.PID, err)
		}
		return nil, fmt.Errorf(
			"%s: process %d did not exit after %s; the CLI will not force-kill an unconfirmed PID, inspect the process and terminate it manually",
			operation, record.PID, daemonStopTimeout,
		)
	}
	if err := l.deps.remove(record.SourcePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%s: remove runtime record: %w", operation, err)
	}
	return &record, nil
}

func runtimeStatusMatches(
	status runtimelock.Status,
	configPath string,
	pid int,
) (bool, error) {
	if !status.Running || status.Metadata == nil || status.Metadata.PID != pid {
		return false, nil
	}
	if configPath == "" || status.Metadata.ConfigPath == "" {
		return configPath == "" && status.Metadata.ConfigPath == "", nil
	}
	statusConfigPath, err := daemonruntime.CanonicalConfigPath(
		status.Metadata.ConfigPath,
	)
	if err != nil {
		return false, fmt.Errorf("canonicalize runtime status config identity: %w", err)
	}
	return statusConfigPath == configPath, nil
}

func (l *daemonLifecycle) removeRuntimeRecord(
	operation, description, path string,
) error {
	if err := l.deps.remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%s: remove %s runtime record: %w", operation, description, err)
	}
	return nil
}

func writeDaemonRunning(stdout io.Writer, record daemon.RuntimeRecord) error {
	runtimeURL, err := daemonruntime.URL(record)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(
		stdout, "kenn-forge running at %s (pid %d)\n", runtimeURL, record.PID,
	)
	return err
}

func joinLifecycleLockRelease(
	operation string,
	lock daemonLifecycleLock,
	resultErr *error,
) {
	*resultErr = errors.Join(*resultErr, releaseLifecycleLock(operation, lock))
}

func releaseLifecycleLock(
	operation string,
	lock daemonLifecycleLock,
) error {
	if err := lock.Release(); err != nil {
		return fmt.Errorf("%s: release lifecycle lock: %w", operation, err)
	}
	return nil
}

func waitForDaemonExit(ctx context.Context, pid int, timeout time.Duration) bool {
	if !daemon.ProcessAlive(pid) {
		return true
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-timer.C:
			return !daemon.ProcessAlive(pid)
		case <-ticker.C:
			if !daemon.ProcessAlive(pid) {
				return true
			}
		}
	}
}
