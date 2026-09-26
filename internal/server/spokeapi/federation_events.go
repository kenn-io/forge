package spokeapi

import (
	"context"
	"sync"
)

type HubEventLifecycle struct {
	mu                   sync.Mutex
	RunFunc              func(context.Context)
	RestartOnCleanReturn bool
	Enabled              bool
	Changed              chan struct{}
	activeCancel         context.CancelFunc
}

func (l *HubEventLifecycle) SetEnabled(enabled bool) {
	if l == nil {
		return
	}
	l.mu.Lock()
	if l.Enabled == enabled {
		l.mu.Unlock()
		return
	}
	l.Enabled = enabled
	close(l.Changed)
	l.Changed = make(chan struct{})
	cancel := l.activeCancel
	l.mu.Unlock()
	if !enabled && cancel != nil {
		cancel()
	}
}

func (l *HubEventLifecycle) Run(ctx context.Context) {
	for ctx.Err() == nil {
		l.mu.Lock()
		enabled := l.Enabled
		changed := l.Changed
		runCtx := ctx
		if enabled {
			var cancel context.CancelFunc
			runCtx, cancel = context.WithCancel(ctx)
			l.activeCancel = cancel
		}
		l.mu.Unlock()

		if !enabled {
			select {
			case <-ctx.Done():
				return
			case <-changed:
				continue
			}
		}
		l.RunFunc(runCtx)
		cleanReturn := runCtx.Err() == nil
		l.mu.Lock()
		l.activeCancel = nil
		l.mu.Unlock()
		if cleanReturn && !l.RestartOnCleanReturn {
			return
		}
	}
}
