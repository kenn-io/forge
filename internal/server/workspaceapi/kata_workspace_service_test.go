package workspaceapi

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stoppedKataTargets struct{ WorkspaceKataTargets }

func (stoppedKataTargets) WorkspaceKataTargetsAvailable() bool { return false }

func (stoppedKataTargets) CreateKataWorkspace(context.Context, WorkspaceKataTarget) (WorkspaceResult, error) {
	return WorkspaceResult{Workspace: WorkspaceResponse{ID: "existing"}}, nil
}

// An existing Kata workspace must stay reusable when the daemon is stopped.
func TestCreateKataWorkspaceServiceDelegatesWhenDaemonUnavailable(t *testing.T) {
	h := &Handler{}
	_, err := h.CreateKataWorkspaceService(t.Context(), WorkspaceKataTarget{})
	require.Error(t, err)

	h.SetKataTargets(stoppedKataTargets{})
	got, err := h.CreateKataWorkspaceService(t.Context(), WorkspaceKataTarget{})
	require.NoError(t, err)
	assert.Equal(t, "existing", got.Workspace.ID)
}
