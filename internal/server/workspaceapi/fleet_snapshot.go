package workspaceapi

import (
	"context"
	"slices"

	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/workspace/localruntime"
)

// FleetSnapshot is the canonical workspace-owned projection consumed by
// Fleet. Its summaries are detached from the manager's mutable state.
type FleetSnapshot struct {
	Workspaces []db.WorkspaceSummary
}

// RuntimeSnapshot is the canonical Workspace-owned runtime-session projection.
type RuntimeSnapshot []localruntime.SessionInfo

// FleetSnapshot returns the current local workspace inventory.
func (h *Handler) FleetSnapshot(ctx context.Context) (FleetSnapshot, error) {
	if h == nil || h.workspaces == nil {
		if h == nil || h.db == nil {
			return FleetSnapshot{}, nil
		}
		workspaces, err := h.db.ListWorkspaceSummaries(ctx)
		return FleetSnapshot{Workspaces: workspaces}, err
	}
	workspaces, err := h.workspaces.ListSummaries(ctx)
	return FleetSnapshot{Workspaces: workspaces}, err
}

// RuntimeSnapshot returns a detached view of runtime sessions for a workspace
// or project-worktree scope.
func (h *Handler) RuntimeSnapshot(scope string) RuntimeSnapshot {
	if h == nil || h.runtime == nil {
		return nil
	}
	return slices.Clone(h.runtime.ListSessions(scope))
}
