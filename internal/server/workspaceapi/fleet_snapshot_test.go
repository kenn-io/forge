package workspaceapi

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/testutil/dbtest"
)

func TestFleetSnapshotUsesWorkspaceOwnedSummaryContract(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	require := require.New(t)

	database := dbtest.Open(t)
	_, err := database.WriteDB().ExecContext(context.Background(), `
		INSERT INTO forge_workspaces
		    (id, platform, platform_host, repo_owner, repo_name,
		     item_type, item_number, item_key, git_head_ref, worktree_path,
		     tmux_session, status, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)`,
		"ws-fleet", "github", "github.com", "octo", "repo",
		db.WorkspaceItemTypePullRequest, 7, "7", "feature", t.TempDir(),
		"ws-fleet", "ready",
	)
	require.NoError(err)

	h := New(Deps{DB: database})
	snapshot, err := h.FleetSnapshot(context.Background())
	require.NoError(err)
	require.Len(snapshot.Workspaces, 1)
	assert.Equal("ws-fleet", snapshot.Workspaces[0].ID)
	assert.Empty(h.RuntimeSnapshot("ws-fleet"))
}
