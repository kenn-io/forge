package workspace

import (
	"testing"

	"go.kenn.io/kwt/worktree"

	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
)

func newWorkspaceTestManager(tb testing.TB, database *db.DB, root string) *Manager {
	tb.Helper()
	coordinator, err := NewRepositoryCoordinator(root)
	require.NoError(tb, err)
	return NewManager(database, root, coordinator)
}

func newTestRepositoryCoordinator(tb testing.TB) *worktree.Coordinator {
	tb.Helper()
	coordinator, err := NewRepositoryCoordinator(tb.TempDir())
	require.NoError(tb, err)
	return coordinator
}
