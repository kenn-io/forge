package workspaceapi

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/agentactivity"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/workspace"
	"go.kenn.io/forge/internal/workspace/localruntime"
)

// TestRestoreRuntimeSessionsDropsReportsOfPrunedRuntimes proves that a report
// whose runtime row is gone, as happens when the startup prune removes a
// missing tmux session before restoration runs, is removed by restoration,
// while a report backed by a persisted row survives.
func TestRestoreRuntimeSessionsDropsReportsOfPrunedRuntimes(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	database := dbtest.Open(t)
	worktree := t.TempDir()
	workspaceID := "ws-reconcile"
	require.NoError(database.InsertWorkspace(ctx, &db.Workspace{
		ID: workspaceID, Platform: "github", PlatformHost: "github.com",
		RepoOwner: "acme", RepoName: "widgets",
		ItemType: db.WorkspaceItemTypePullRequest, ItemNumber: 9,
		GitHeadRef: "feature/reconcile", WorkspaceBranch: "feature/reconcile",
		WorktreePath: worktree, Status: "ready",
	}))
	// A persisted runtime whose pty-owner state is gone restores as
	// unavailable, which keeps its row; its report must survive.
	require.NoError(database.UpsertWorkspaceRuntimeSession(ctx, &db.WorkspaceRuntimeSession{
		WorkspaceID: workspaceID, SessionKey: "runtime-kept",
		TargetKey: "claude", Label: "Claude", Kind: "agent", Scope: "session",
	}))

	activityRoot := t.TempDir()
	activity := agentactivity.NewStore(activityRoot)
	require.NoError(activity.HandleEvent("claude", agentactivity.HookEvent{
		SessionID: "kept", CWD: worktree, HookEventName: "UserPromptSubmit",
	}, "runtime-kept"))
	require.NoError(activity.HandleEvent("claude", agentactivity.HookEvent{
		SessionID: "pruned", CWD: worktree, HookEventName: "UserPromptSubmit",
	}, "runtime-pruned"))
	entries, err := os.ReadDir(activityRoot)
	require.NoError(err)
	require.Len(entries, 2)

	runtime := localruntime.NewManager(localruntime.Options{})
	t.Cleanup(runtime.Shutdown)
	handler := New(Deps{
		DB: database, Workspaces: workspace.NewManager(database, t.TempDir()),
		Runtime: runtime, AgentActivity: activity,
	})
	require.NoError(handler.RestoreRuntimeSessions(ctx))

	entries, err = os.ReadDir(activityRoot)
	require.NoError(err)
	require.Len(entries, 1, "the report of the pruned runtime is gone")
	reports := activity.LiveReportsForWorkspace(worktree, []string{"runtime-kept", "runtime-pruned"})
	require.Len(reports, 1)
	assert.Equal("runtime-kept", reports[0].RuntimeSessionKey)
}
