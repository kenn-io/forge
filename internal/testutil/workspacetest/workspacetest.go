// Package workspacetest builds workspace managers for tests outside the
// workspace package.
package workspacetest

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/workspace"
)

// NewManager returns a manager rooted at root with its own repository
// coordinator, wired as the daemon wires one.
func NewManager(tb testing.TB, database *db.DB, root string) *workspace.Manager {
	tb.Helper()
	coordinator, err := workspace.NewRepositoryCoordinator(root)
	require.NoError(tb, err)
	return workspace.NewManager(database, root, coordinator)
}
