package workspaceapi

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/testutil/reposeed"
)

func TestFleetSnapshotUsesWorkspaceOwnedSummaryContract(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	require := require.New(t)

	database := dbtest.Open(t)
	repoID, err := reposeed.Seed(t.Context(), database, db.RepoIdentity{
		Platform: "github", PlatformHost: "github.com",
		PlatformRepoID: 1001, Owner: "octo", Name: "repo",
	})
	require.NoError(err)
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	_, err = database.UpsertMergeRequest(t.Context(), &db.MergeRequest{
		RepoID: repoID, PlatformID: 7, Number: 7, Title: "Provider title",
		Author: "octo", State: db.MergeRequestStateOpen,
		HeadBranch: "feature", BaseBranch: "main",
		CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
	})
	require.NoError(err)
	_, err = database.WriteDB().ExecContext(t.Context(), `
		INSERT INTO forge_workspaces
		    (id, platform, platform_host, repo_owner, repo_name, repo_id,
		     item_type, item_number, item_key, git_head_ref, worktree_path,
		     tmux_session, status, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)`,
		"ws-fleet", "github", "github.com", "octo", "repo", repoID,
		db.WorkspaceItemTypePullRequest, 7, "7", "feature", t.TempDir(),
		"ws-fleet", "ready",
	)
	require.NoError(err)

	h := New(Deps{DB: database})
	snapshot, err := h.FleetSnapshot(t.Context())
	require.NoError(err)
	require.Len(snapshot.Workspaces, 1)
	workspace := snapshot.Workspaces[0]
	assert.Equal("ws-fleet", workspace.ID)
	assert.Equal(int64(1001), workspace.Repository.PlatformRepoID)
	assert.True(workspace.SourceItemVisible)
	assert.Nil(workspace.MRTitle, "spoke raw state must omit provider title")
	assert.Nil(workspace.MRState, "spoke raw state must omit provider state")
	encoded, err := json.Marshal(snapshot)
	require.NoError(err)
	assert.NotContains(string(encoded), "repoID")
	assert.NotContains(string(encoded), "Provider title")
	assert.Empty(h.RuntimeSnapshot("ws-fleet"))
}
