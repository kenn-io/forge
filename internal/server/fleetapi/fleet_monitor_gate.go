package fleetapi

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"
)

// fleetMonitorIdleProbeInterval is how often an idle monitor re-checks for
// subscribers. It is short so a client that connects between two regular ticks
// gets a fresh pass promptly instead of waiting out a full interval; the check
// itself is a mutex-guarded map length, not a subprocess.
const fleetMonitorIdleProbeInterval = 2 * time.Second

// fleetMonitorGate decides whether a background monitor may run an expensive
// pass. The fleet monitors exist to keep the live snapshot fresh for connected
// clients, so while nobody is subscribed to the event stream they only record
// that they skipped. A nil hasSubscribers means the monitor is always active,
// which is what unit tests and callers without an event hub want.
type fleetMonitorGate struct {
	name           string
	hasSubscribers func() bool
	idleSkips      atomic.Int64
}

func newFleetMonitorGate(name string, hasSubscribers func() bool) *fleetMonitorGate {
	return &fleetMonitorGate{name: name, hasSubscribers: hasSubscribers}
}

// active reports whether a pass should run now. When it returns false the
// skip has already been recorded.
func (g *fleetMonitorGate) active() bool {
	if g == nil || g.hasSubscribers == nil || g.hasSubscribers() {
		return true
	}
	g.idleSkips.Add(1)
	slog.Debug("fleet monitor: no subscribers, skipping pass", "monitor", g.name)
	return false
}

// skipped returns how many passes the gate has suppressed so far.
func (g *fleetMonitorGate) skipped() int64 {
	if g == nil {
		return 0
	}
	return g.idleSkips.Load()
}

// runFleetMonitorLoop drives pass on a fixed interval until ctx ends. Every
// wake-up, including the initial one, consults the gate: a suppressed pass is
// followed by a short idle probe rather than a full interval, so the first
// subscriber after an idle stretch is served within the probe interval.
func runFleetMonitorLoop(
	ctx context.Context,
	interval time.Duration,
	gate *fleetMonitorGate,
	pass func(context.Context),
) {
	wait := time.Duration(0)
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		if gate.active() {
			pass(ctx)
			wait = interval
		} else {
			wait = min(fleetMonitorIdleProbeInterval, interval)
		}
		timer.Reset(wait)
	}
}
