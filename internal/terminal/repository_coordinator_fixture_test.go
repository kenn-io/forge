package terminal

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/workspace"
)

func newWorkspaceTestManager(t testing.TB, database *db.DB, root string) *workspace.Manager {
	t.Helper()
	coordinator, err := workspace.NewRepositoryCoordinator(root)
	require.NoError(t, err)
	return workspace.NewManager(database, root, coordinator)
}
