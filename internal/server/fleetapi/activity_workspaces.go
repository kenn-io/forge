package fleetapi

import (
	"context"
	"sync"
	"time"

	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/fleet"
)

const (
	// activityPeerWait bounds how long an Activity read waits for member
	// results when it has none, or only results older than activityPeerMaxAge.
	activityPeerWait   = time.Second
	activityPeerMaxAge = 2 * time.Minute
)

// activityPeerCache keeps the latest member fan-out for Activity workspace
// indicators. Activity is polled and re-read on every filter change, so it
// reads the latest results and starts at most one background refresh instead
// of waiting up to the peer timeout for a slow or unreachable member.
type activityPeerCache struct {
	mu          sync.Mutex
	results     []fleet.PeerResult
	refreshedAt time.Time
	// refreshed is non-nil while a refresh runs and closes when it completes.
	refreshed chan struct{}
}

// ActivityWorkspaces supplies the hub's Activity indicators from the same
// observer projection as the Workspaces tab, using fresh local workspaces and
// the latest member results. Spokes keep local indicators.
func (s *Handler) ActivityWorkspaces(ctx context.Context) ([]fleet.WorkspaceSummary, error) {
	cfg := s.configSnapshot().Fleet
	if !cfg.Enabled || cfg.RoleOrDefault() != config.FleetRoleHub {
		return nil, nil
	}
	s.noteSnapshotDemand()
	local, err := s.buildLocalRaw(ctx)
	if err != nil {
		return nil, err
	}
	aggregate, err := fleet.EnrichProviderState(
		ctx, s.db, fleet.BuildNeutralAggregate(local, s.latestActivityPeerResults(ctx)),
	)
	if err != nil {
		return nil, err
	}
	snapshot := fleet.ProjectForObserver(aggregate, local, fleet.Observer{NodeID: local.NodeID, Role: fleet.RoleHub})
	return snapshot.Workspaces, nil
}

// latestActivityPeerResults returns the latest member results and starts a
// refresh when none is running. It waits up to activityPeerWait only when the
// cached results are missing or older than activityPeerMaxAge.
func (s *Handler) latestActivityPeerResults(ctx context.Context) []fleet.PeerResult {
	cache := &s.activityPeers
	cache.mu.Lock()
	if cache.refreshed == nil {
		refreshed := make(chan struct{})
		if s.runBackground(func(background context.Context) {
			s.refreshActivityPeerResults(background, refreshed)
		}) {
			cache.refreshed = refreshed
		}
	}
	results, refreshed := cache.results, cache.refreshed
	current := !cache.refreshedAt.IsZero() && s.now().Sub(cache.refreshedAt) <= activityPeerMaxAge
	cache.mu.Unlock()
	if current || refreshed == nil {
		return results
	}
	timer := time.NewTimer(activityPeerWait)
	defer timer.Stop()
	select {
	case <-refreshed:
	case <-timer.C:
	case <-ctx.Done():
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	return cache.results
}

func (s *Handler) refreshActivityPeerResults(ctx context.Context, refreshed chan struct{}) {
	cfg := s.configSnapshot().Fleet
	results := s.fetchPeerResults(ctx, cfg, cfg.PeerTimeoutOrDefault())
	cache := &s.activityPeers
	cache.mu.Lock()
	cache.results = results
	cache.refreshedAt = s.now()
	cache.refreshed = nil
	cache.mu.Unlock()
	close(refreshed)
}
