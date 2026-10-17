package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/db"
	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/telemetry"
	"go.kenn.io/kit/telemetry/posthog"
)

const databaseOptimizeInterval = 24 * time.Hour

const setupFinishedKey = "telemetry.setup_finished"

func startSetupFinishedLoop(handle *backgroundLoopHandle, database *db.DB, reporter telemetry.Client) {
	handle.start(func(ctx context.Context) {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			done, err := captureSetupFinished(ctx, database, reporter)
			if err != nil && ctx.Err() == nil {
				slog.Warn("capture setup finished", "err", err)
			}
			if done {
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	})
}

func captureSetupFinished(ctx context.Context, database *db.DB, reporter telemetry.Client) (bool, error) {
	lookupCtx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	if _, found, err := database.AppMetadataValue(lookupCtx, setupFinishedKey); err != nil || found {
		return found, err
	}
	prs, err := database.ListMergeRequests(lookupCtx, db.ListMergeRequestsOpts{State: "all", Limit: 1})
	if err != nil {
		return false, err
	}
	if len(prs) == 0 {
		issues, err := database.ListIssues(lookupCtx, db.ListIssuesOpts{State: "all", Limit: 1})
		if err != nil || len(issues) == 0 {
			return false, err
		}
	}
	claimed := false
	_, err = database.GetOrCreateAppMetadataValue(lookupCtx, setupFinishedKey, func() (string, error) {
		claimed = true
		return "queued", nil
	})
	if err != nil || !claimed {
		return err == nil, err
	}
	cancel()
	reportCtx, reportCancel := context.WithTimeout(ctx, 300*time.Millisecond)
	status, err := reporter.Report(reportCtx, "setup_finished", nil)
	reportCancel()
	if err == nil && status == posthog.StatusQueued {
		return true, nil
	}
	cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), 300*time.Millisecond)
	defer cleanupCancel()
	return false, errors.Join(err, database.DeleteAppMetadataValue(cleanupCtx, setupFinishedKey))
}

func closeTelemetry(database *db.DB, reporter telemetry.Client, checkSetup bool) error {
	var err error
	if checkSetup {
		ctx, cancel := context.WithTimeout(context.Background(), 900*time.Millisecond)
		_, err = captureSetupFinished(ctx, database, reporter)
		cancel()
	}
	return errors.Join(err, reporter.Close())
}

type backgroundLoopHandle struct {
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

type notificationLoopSettings struct {
	syncInterval        time.Duration
	propagationInterval time.Duration
	batchSize           int
}

func (h *backgroundLoopHandle) Stop(ctx context.Context) error {
	if h == nil {
		return nil
	}
	h.cancel()
	done := make(chan struct{})
	go func() {
		h.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func startBackgroundLoops(ctx context.Context, database *db.DB) *backgroundLoopHandle {
	handle := newBackgroundLoopHandle(ctx)
	handle.startTickerAfterInterval("database optimize", databaseOptimizeInterval, database.Optimize)
	return handle
}

// startTelemetryHeartbeat reports daemon_active daily so long-running servers
// keep counting as active, not just at startup.
func startTelemetryHeartbeat(handle *backgroundLoopHandle, reporter telemetry.Client, repoCount func() int) {
	handle.startTickerAfterInterval("telemetry heartbeat", telemetry.HeartbeatInterval, func(context.Context) error {
		return captureDaemonActive(reporter, repoCount)
	})
}

func captureDaemonActive(reporter telemetry.Client, repoCount func() int) error {
	return reporter.Capture("daemon_active", map[string]any{"repo_count": telemetry.RepoCountBucket(repoCount())})
}

func startNotificationLoops(handle *backgroundLoopHandle, syncer *ghclient.Syncer, cfg *config.Config) {
	settings := notificationLoopSettingsFromConfig(cfg)
	handle.startTicker("notification sync", settings.syncInterval, func(runCtx context.Context) error {
		if !syncer.AutomaticSyncEnabled() {
			return nil
		}
		return syncer.RunNotificationSync(runCtx)
	})
	handle.startTicker("notification read propagation", settings.propagationInterval, func(runCtx context.Context) error {
		return syncer.ProcessQueuedNotificationReadsForAllHosts(runCtx, settings.batchSize)
	})
}

// relayHTTPClient keeps a subscription open indefinitely while still bounding
// connection setup and response headers.
func relayHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 10 * time.Second
	return &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
}

func notificationLoopSettingsFromConfig(cfg *config.Config) notificationLoopSettings {
	return notificationLoopSettings{
		syncInterval:        cfg.NotificationSyncDuration(),
		propagationInterval: cfg.NotificationPropagationDuration(),
		batchSize:           cfg.NotificationBatchSize(),
	}
}

func newBackgroundLoopHandle(parent context.Context) *backgroundLoopHandle {
	ctx, cancel := context.WithCancel(parent)
	return &backgroundLoopHandle{ctx: ctx, cancel: cancel}
}

// start runs a long-lived loop that owns its own retry schedule.
func (h *backgroundLoopHandle) start(run func(context.Context)) {
	h.wg.Go(func() {
		if h.ctx.Err() == nil {
			run(h.ctx)
		}
	})
}

func (h *backgroundLoopHandle) startTicker(name string, interval time.Duration, run func(context.Context) error) {
	h.startTickerLoop(name, interval, true, run)
}

func (h *backgroundLoopHandle) startTickerAfterInterval(
	name string,
	interval time.Duration,
	run func(context.Context) error,
) {
	h.startTickerLoop(name, interval, false, run)
}

func (h *backgroundLoopHandle) startTickerLoop(
	name string,
	interval time.Duration,
	runImmediately bool,
	run func(context.Context) error,
) {
	h.wg.Go(func() {
		ctx := h.ctx
		runOnce := func() {
			if err := run(ctx); err != nil && ctx.Err() == nil {
				slog.Warn(name+" failed", "err", err)
			}
		}
		if ctx.Err() != nil {
			return
		}
		if runImmediately {
			runOnce()
			if ctx.Err() != nil {
				return
			}
		}

		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				runOnce()
			}
		}
	})
}
