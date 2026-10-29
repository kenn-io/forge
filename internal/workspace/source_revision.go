package workspace

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// SourceState reads Git itself; a creation request is not proof of the checkout.
type SourceState struct {
	RequestedRevision string `json:"requested_revision"`
	Head              string `json:"head"`
	Clean             bool   `json:"clean"`
}

func (m *Manager) SourceState(ctx context.Context, id string) (SourceState, error) {
	ws, err := m.db.GetWorkspace(ctx, id)
	if err != nil {
		return SourceState{}, err
	}
	if ws == nil || ws.Status != "ready" {
		return SourceState{}, errors.New("workspace is not ready")
	}
	if err := m.verifyWorkspaceRepository(ctx, ws); err != nil {
		return SourceState{}, err
	}
	spec, err := m.RequireWorkspaceLaunchSpec(ctx, ws)
	if err != nil {
		return SourceState{}, err
	}
	var state SourceState
	if spec != nil {
		state.RequestedRevision = spec.SourceRevision
	}
	head, exists, err := gitRefSHA(ctx, ws.WorktreePath, "HEAD^{commit}")
	if err != nil {
		return state, err
	}
	if !exists {
		return state, errors.New("workspace has no commit")
	}
	state.Head = head
	cmd := workspaceGitCommand(ctx, ws.WorktreePath, "-c", "core.fsmonitor=false", "status", "--porcelain", "--untracked-files=normal", "--ignore-submodules=none")
	raw, err := cmd.Output()
	if err != nil {
		return state, fmt.Errorf("read workspace status: %w", err)
	}
	state.Clean = strings.TrimSpace(string(raw)) == ""
	return state, nil
}
