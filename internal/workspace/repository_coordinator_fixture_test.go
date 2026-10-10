package workspace

import (
	"testing"

	"go.kenn.io/kwt/worktree"

	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
)

func newWorkspaceTestManager(t testing.TB, database *db.DB, root string) *Manager {
	t.Helper()
	coordinator, err := NewRepositoryCoordinator(root)
	require.NoError(t, err)
	return NewManager(database, root, coordinator)
}

func newTestRepositoryCoordinator(t testing.TB) *worktree.Coordinator {
	t.Helper()
	coordinator, err := NewRepositoryCoordinator(t.TempDir())
	require.NoError(t, err)
	return coordinator
}
