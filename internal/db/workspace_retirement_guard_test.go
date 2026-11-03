package db

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func insertGuardTestWorkspace(t *testing.T, d *DB, id, owner string) {
	t.Helper()
	require.NoError(t, d.InsertWorkspace(t.Context(), &Workspace{
		ID: id, Platform: "github", PlatformHost: "github.com",
		RepoOwner: owner, RepoName: "widget",
		ItemType: WorkspaceItemTypePullRequest, ItemNumber: 1,
		GitHeadRef: "feature", WorkspaceBranch: "feature",
		WorktreePath: filepath.Join(t.TempDir(), id), TmuxSession: "tmux-" + id,
		Status: "ready",
	}))
}

func TestRetirementGuardReportsUnresolvedWorkspacesOnlyUnderAMonitor(t *testing.T) {
	t.Parallel()

	t.Run("insert before monitor", func(t *testing.T) {
		t.Parallel()
		d := openTemplateTestDB(t)
		insertGuardTestWorkspace(t, d, "ws-unresolved", "acme")
		assert.Empty(t, d.UnresolvedWorkspacesUnderRetirementMonitor())
		d.NoteWorkspaceRetirementMonitorStarted()
		assert.Equal(t, []string{"ws-unresolved"}, d.UnresolvedWorkspacesUnderRetirementMonitor())
	})

	t.Run("insert after monitor", func(t *testing.T) {
		t.Parallel()
		d := openTemplateTestDB(t)
		d.NoteWorkspaceRetirementMonitorStarted()
		insertGuardTestWorkspace(t, d, "ws-unresolved", "acme")
		assert.Equal(t, []string{"ws-unresolved"}, d.UnresolvedWorkspacesUnderRetirementMonitor())
	})

	t.Run("seeded repository", func(t *testing.T) {
		t.Parallel()
		d := openTemplateTestDB(t)
		_, err := seedTestRepo(t.Context(), d, GitHubRepoIdentity("github.com", "acme", "widget"))
		require.NoError(t, err)
		d.NoteWorkspaceRetirementMonitorStarted()
		insertGuardTestWorkspace(t, d, "ws-resolved", "acme")
		assert.Empty(t, d.UnresolvedWorkspacesUnderRetirementMonitor())
	})
}

func TestRetirementGuardIsOffForProductionDatabases(t *testing.T) {
	t.Parallel()
	d, err := Open(filepath.Join(t.TempDir(), "prod.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, d.Close()) })

	d.NoteWorkspaceRetirementMonitorStarted()
	insertGuardTestWorkspace(t, d, "ws-unresolved", "acme")
	assert.Nil(t, d.UnresolvedWorkspacesUnderRetirementMonitor())
}
