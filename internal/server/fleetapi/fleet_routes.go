package fleetapi

import (
	"context"

	"github.com/danielgtaylor/huma/v2"

	"go.kenn.io/forge/internal/fleet"
	"go.kenn.io/forge/internal/server/httpapi"
)

type snapshotInput struct {
	IncludePeers bool `query:"include_peers" doc:"Fan out to configured fleet peers and include their hosts/worktrees."`
}

type snapshotOutput struct {
	Body fleet.Snapshot
}

type rawSnapshotOutput struct {
	Body fleet.RawSnapshot
}

// Register registers Fleet snapshot, proxy, project, and terminal operations.
func (s *Handler) Register(api huma.API) {
	huma.Get(api, "/snapshot", s.getSnapshot,
		httpapi.DocumentOperation("get-snapshot", "Read the workspace snapshot", "Fleet"))
	huma.Get(api, "/snapshot/raw", s.getSnapshotRaw,
		httpapi.DocumentOperation("get-snapshot-raw", "Read the local raw inventory", "Fleet"))
	huma.Post(api, "/snapshot/refresh-stats", s.refreshFleetStats,
		httpapi.DocumentOperation("refresh-fleet-stats",
			"Refresh all worktree git stats", "Fleet"))
	s.registerFleetOperationRoutes(api)
	s.registerFleetProjectRoutes(api)
}

type refreshFleetStatsOutput struct {
	Body struct {
		Refreshed bool `json:"refreshed" doc:"True once the synchronous stats pass has completed."`
	}
}

// refreshFleetStats samples every worktree's git stats now, bypassing the 30s
// background interval, so a caller that just mutated the fleet (or an explicit
// refresh action) sees fresh diff/sync fields in the next snapshot read. Unlike
// the per-worktree refresh route it covers synthesized primary worktrees, which
// have no registry row. It runs synchronously: when it returns, the stats store
// reflects the current worktree set.
func (s *Handler) refreshFleetStats(
	ctx context.Context, _ *struct{},
) (*refreshFleetStatsOutput, error) {
	s.fleetWorktreeStatsSampler.runOnce(ctx)
	out := &refreshFleetStatsOutput{}
	out.Body.Refreshed = true
	return out, nil
}

// getSnapshot returns the enriched snapshot. With include_peers=true the hub
// fans out to configured peers, merges, and enriches once.
func (s *Handler) getSnapshot(ctx context.Context, in *snapshotInput) (*snapshotOutput, error) {
	snap, err := s.buildFleetSnapshot(ctx, in.IncludePeers)
	if err != nil {
		return nil, httpapi.Internal("build snapshot: " + err.Error())
	}
	return &snapshotOutput{Body: snap}, nil
}

// getSnapshotRaw returns this daemon's local raw inventory. It never fans
// out — the hub only ever calls peers' /raw — so federation cannot loop.
func (s *Handler) getSnapshotRaw(ctx context.Context, _ *struct{}) (*rawSnapshotOutput, error) {
	raw, err := s.buildLocalRaw(ctx)
	if err != nil {
		return nil, httpapi.Internal("build raw snapshot: " + err.Error())
	}
	return &rawSnapshotOutput{Body: raw}, nil
}
