package main

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"go.kenn.io/middleman/internal/config"
	ghclient "go.kenn.io/middleman/internal/github"
)

type notificationLoopHandle struct {
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

type notificationLoopSettings struct {
	syncInterval        time.Duration
	propagationInterval time.Duration
	batchSize           int
}

func (h *notificationLoopHandle) Stop() {
	if h == nil {
		return
	}
	h.cancel()
	h.wg.Wait()
}

func startNotificationLoops(ctx context.Context, syncer *ghclient.Syncer, cfg *config.Config) *notificationLoopHandle {
	handle := newNotificationLoopHandle(ctx)
	settings := notificationLoopSettingsFromConfig(cfg)
	handle.startTicker("notification sync", settings.syncInterval, func(runCtx context.Context) error {
		return syncer.RunNotificationSync(runCtx)
	})
	handle.startTicker("notification read propagation", settings.propagationInterval, func(runCtx context.Context) error {
		return syncer.ProcessQueuedNotificationReadsForAllHosts(runCtx, settings.batchSize)
	})
	return handle
}

func notificationLoopSettingsFromConfig(cfg *config.Config) notificationLoopSettings {
	return notificationLoopSettings{
		syncInterval:        cfg.NotificationSyncDuration(),
		propagationInterval: cfg.NotificationPropagationDuration(),
		batchSize:           cfg.NotificationBatchSize(),
	}
}

func newNotificationLoopHandle(parent context.Context) *notificationLoopHandle {
	ctx, cancel := context.WithCancel(parent)
	return &notificationLoopHandle{ctx: ctx, cancel: cancel}
}

func (h *notificationLoopHandle) startTicker(name string, interval time.Duration, run func(context.Context) error) {
	h.wg.Go(func() {
		ctx := h.ctx
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := run(ctx); err != nil && ctx.Err() == nil {
					slog.Warn(name+" failed", "err", err)
				}
			}
		}
	})
}
