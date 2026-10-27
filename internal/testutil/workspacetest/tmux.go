package workspacetest

import (
	"testing"

	"go.kenn.io/forge/internal/testutil/testtmux"
	"go.kenn.io/forge/internal/workspace/localruntime"
)

// NewTmuxRuntimeFixture registers the private server before runtime cleanup, so
// the runtime stops its agents before the server owner reaps the server.
func NewTmuxRuntimeFixture(t *testing.T, owner *testtmux.Owner, tmuxPath string) *localruntime.TmuxRuntimeFixture {
	t.Helper()
	command := owner.Command(t, tmuxPath)
	return localruntime.NewTmuxRuntimeFixture(t, command)
}
