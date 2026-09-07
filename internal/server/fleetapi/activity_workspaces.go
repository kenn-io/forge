package fleetapi

import (
	"context"

	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/fleet"
)

// ActivityWorkspaces supplies the hub's Activity indicators from the same
// observer projection as the Workspaces tab. Spokes keep local indicators.
func (s *Handler) ActivityWorkspaces(ctx context.Context) ([]fleet.WorkspaceSummary, error) {
	cfg := s.configSnapshot().Fleet
	if !cfg.Enabled || cfg.RoleOrDefault() != config.FleetRoleHub {
		return nil, nil
	}
	s.noteSnapshotDemand()
	snapshot, err := s.buildFleetSnapshot(ctx, true)
	if err != nil {
		return nil, err
	}
	return snapshot.Workspaces, nil
}
