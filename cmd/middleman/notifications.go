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

func (h *notificationLoopHandle) Stop() {
	if h == nil {
		return
	}
	h.cancel()
	h.wg.Wait()
}

func startNotificationLoops(ctx context.Context, syncer *ghclient.Syncer, cfg *config.Config) *notificationLoopHandle {
	handle := newNotificationLoopHandle(ctx)
	handle.startTicker("notification sync", cfg.NotificationSyncDuration(), func(runCtx context.Context) error {
		return syncer.RunNotificationSync(runCtx)
	})
	handle.startTicker("notification read propagation", cfg.NotificationPropagationDuration(), func(runCtx context.Context) error {
		return syncer.ProcessQueuedNotificationReadsForAllHosts(runCtx, cfg.NotificationBatchSize())
	})
	return handle
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
