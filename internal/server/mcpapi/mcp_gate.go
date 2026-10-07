package mcpapi

import (
	"context"
	"net/http"
	"sync"
)

// RequestGate gives MCP handlers served by a shared listener the same
// shutdown contract as the dedicated MCP listener: stop admission, drain for
// a bounded grace period, then cancel in-flight handler contexts through the
// gate's base context.
type RequestGate struct {
	base     context.Context
	mu       sync.Mutex
	stopped  bool
	inflight sync.WaitGroup
}

// NewRequestGate returns a gate whose admitted requests are canceled when
// base is canceled.
func NewRequestGate(base context.Context) *RequestGate {
	return &RequestGate{base: base}
}

// Wrap admits requests to next until Stop is called.
func (g *RequestGate) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		if g.stopped {
			g.mu.Unlock()
			http.Error(w, "MCP is shutting down", http.StatusServiceUnavailable)
			return
		}
		g.inflight.Add(1)
		g.mu.Unlock()
		defer g.inflight.Done()

		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		stop := context.AfterFunc(g.base, cancel)
		defer stop()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// Stop rejects new requests; admitted requests keep running.
func (g *RequestGate) Stop() {
	g.mu.Lock()
	g.stopped = true
	g.mu.Unlock()
}

// Wait blocks until admitted requests finish or ctx ends.
func (g *RequestGate) Wait(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		g.inflight.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
