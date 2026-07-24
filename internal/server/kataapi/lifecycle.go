package kataapi

import "context"

// Start attaches Kata's cache lifecycle to parent once.
func (h *Handler) Start(parent context.Context) {
	if h == nil {
		return
	}
	if parent == nil {
		parent = context.Background()
	}
	h.lifecycleMu.Lock()
	if h.started || h.stopping {
		h.lifecycleMu.Unlock()
		return
	}
	h.started = true
	lifecycleCtx, cancel := context.WithCancel(parent)
	h.lifecycleCancel = cancel
	h.lifecycleWG.Add(1)
	h.lifecycleMu.Unlock()

	go func() {
		defer h.lifecycleWG.Done()
		<-lifecycleCtx.Done()
		h.stop()
	}()
}

func (h *Handler) stop() <-chan struct{} {
	h.lifecycleMu.Lock()
	if h.stopping {
		done := h.lifecycleDone
		h.lifecycleMu.Unlock()
		return done
	}
	h.stopping = true
	if h.lifecycleCancel != nil {
		h.lifecycleCancel()
	}
	done := h.lifecycleDone
	h.lifecycleMu.Unlock()

	go func() {
		h.lifecycleWG.Wait()
		h.closeKataProxyIdleConnections()
		close(done)
	}()
	return done
}

// Shutdown stops Kata-owned work and closes cached transports. Repeated and
// concurrent calls wait on the same completion signal within the caller's
// context.
func (h *Handler) Shutdown(ctx context.Context) error {
	if h == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-h.stop():
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
