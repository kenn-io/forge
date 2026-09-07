package fleetapi

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/fleet"
	"go.kenn.io/forge/internal/server/httpapi"
)

type snapshotInput struct {
	IncludePeers bool `query:"include_peers" doc:"Include the federation aggregate projected for this spoke."`
}

type snapshotOutput struct {
	Body fleet.Snapshot
}

type rawSnapshotOutput struct {
	Body fleet.RawSnapshot
}

type aggregateSnapshotOutput struct {
	Body fleet.NeutralSnapshot
}

type aggregateSnapshotInput struct {
	MemberTimeout string `query:"member_timeout" doc:"Maximum member fan-out time requested by a spoke."`
}

// Register registers Fleet snapshot, proxy, project, and terminal operations.
func (s *Handler) Register(api huma.API) {
	s.registerEnrollmentRoutes(api)
	huma.Register(api, huma.Operation{
		OperationID:   "queue-federation-workspace-cleanup",
		Method:        http.MethodPost,
		Path:          "/federation/workspaces/{id}/cleanup",
		DefaultStatus: http.StatusAccepted,
		Hidden:        true,
	}, s.queueFederationWorkspaceCleanup)
	huma.Get(api, "/snapshot", s.getSnapshot,
		httpapi.DocumentOperation("get-snapshot", "Read the workspace snapshot", "Fleet"))
	huma.Get(api, "/snapshot/raw", s.getSnapshotRaw,
		httpapi.DocumentOperation("get-snapshot-raw", "Read the local raw inventory", "Fleet"))
	huma.Get(api, "/snapshot/aggregate", s.getSnapshotAggregate,
		httpapi.DocumentOperation("get-snapshot-aggregate",
			"Read the hub's neutral fleet aggregate", "Fleet"))
	huma.Post(api, "/snapshot/refresh-stats", s.refreshFleetStats,
		httpapi.DocumentOperation("refresh-fleet-stats",
			"Refresh all worktree git stats", "Fleet"))
	s.registerFleetOperationRoutes(api)
	s.registerFleetProjectRoutes(api)
}

func (s *Handler) getSnapshotAggregate(
	ctx context.Context,
	input *aggregateSnapshotInput,
) (*aggregateSnapshotOutput, error) {
	s.noteSnapshotDemand()
	fleetConfig := s.configSnapshot().Fleet
	if !fleetConfig.Enabled ||
		fleetConfig.RoleOrDefault() != config.FleetRoleHub {
		return nil, httpapi.Conflict(
			httpapi.CodeConflict,
			"this daemon is not an enabled federation hub",
			nil,
		)
	}
	local, err := s.buildLocalRaw(ctx)
	if err != nil {
		return nil, httpapi.Internal("build raw snapshot: " + err.Error())
	}
	memberTimeout := fleetConfig.PeerTimeoutOrDefault()
	if input.MemberTimeout != "" {
		requestedTimeout, parseErr := time.ParseDuration(input.MemberTimeout)
		if parseErr != nil || requestedTimeout <= 0 {
			return nil, httpapi.BadRequest(
				httpapi.CodeBadRequest,
				"member_timeout must be a positive duration",
				nil,
			)
		}
		memberTimeout = min(memberTimeout, requestedTimeout)
	}
	aggregate, err := s.buildHubAggregate(ctx, local, true, memberTimeout)
	if err != nil {
		return nil, httpapi.Internal("build aggregate snapshot: " + err.Error())
	}
	return &aggregateSnapshotOutput{Body: aggregate}, nil
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
	s.fleetWorktreeStatsSampler.runOnceForced(ctx)
	out := &refreshFleetStatsOutput{}
	out.Body.Refreshed = true
	return out, nil
}

// getSnapshot returns the observer-relative snapshot. With include_peers=true,
// a hub aggregates member raw state and a spoke consumes that aggregate.
func (s *Handler) getSnapshot(ctx context.Context, in *snapshotInput) (*snapshotOutput, error) {
	s.noteSnapshotDemand()
	snap, err := s.buildFleetSnapshot(ctx, in.IncludePeers)
	if err != nil {
		return nil, httpapi.Internal("build snapshot: " + err.Error())
	}
	return &snapshotOutput{Body: snap}, nil
}

// getSnapshotRaw returns this daemon's local raw inventory. It never fans out
// or re-exports a fetched aggregate, so federation cannot loop.
func (s *Handler) getSnapshotRaw(ctx context.Context, _ *struct{}) (*rawSnapshotOutput, error) {
	s.noteSnapshotDemand()
	raw, err := s.buildLocalRaw(ctx)
	if err != nil {
		return nil, httpapi.Internal("build raw snapshot: " + err.Error())
	}
	return &rawSnapshotOutput{Body: raw}, nil
}
