package workspaceapi

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/agentactivity"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/ptyowner"
	ptyownerruntime "go.kenn.io/forge/internal/ptyowner/runtime"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/workspace"
	"go.kenn.io/forge/internal/workspace/localruntime"
)

// TestTmuxEnrichmentSkipsWorkspacesCoveredByHookReports proves that a
// workspace whose live agent session has reported through the hook
// integration is not probed through tmux at all, and that the probe resumes
// once the hook coverage is gone.
func TestTmuxEnrichmentSkipsWorkspacesCoveredByHookReports(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	t.Setenv("KENN_FORGE_AGENT_SESSION_HELPER", "1")
	ctx := t.Context()
	database := dbtest.Open(t)
	worktree := t.TempDir()
	workspaceID := "ws-hook-gate"
	require.NoError(database.InsertWorkspace(ctx, &db.Workspace{
		ID:              workspaceID,
		Platform:        "github",
		PlatformHost:    "github.com",
		RepoOwner:       "acme",
		RepoName:        "widgets",
		ItemType:        db.WorkspaceItemTypePullRequest,
		ItemNumber:      7,
		GitHeadRef:      "feature/hook-gate",
		WorkspaceBranch: "feature/hook-gate",
		WorktreePath:    worktree,
		TmuxSession:     "forge-hook-gate",
		Status:          "ready",
	}))

	// Every tmux invocation the workspace manager makes lands in this log.
	logPath := filepath.Join(t.TempDir(), "tmux-calls.log")
	tmuxScript := filepath.Join(t.TempDir(), "tmux")
	require.NoError(os.WriteFile(tmuxScript, []byte(
		"#!/bin/sh\necho \"$@\" >> \""+logPath+"\"\nexit 1\n",
	), 0o755))
	manager := workspace.NewManager(database, t.TempDir())
	manager.SetTmuxCommand([]string{tmuxScript})

	helperCommand := []string{
		os.Args[0], "-test.run=^TestWorkspaceAgentSessionHelper$", "--", "sleep",
	}
	runtime := localruntime.NewManager(localruntime.Options{
		Targets: []localruntime.LaunchTarget{{
			Key: "claude", Label: "Claude", Kind: localruntime.LaunchTargetAgent,
			Source: "test", Command: helperCommand, Available: true,
		}},
		PtyOwnerRuntime: ptyownerruntime.New(&ptyowner.Client{
			Root: filepath.Join(t.TempDir(), "pty-owner"), InProcess: true,
		}, nil),
	})
	t.Cleanup(func() {
		runtime.StopWorkspace(context.Background(), workspaceID)
		runtime.Shutdown()
	})
	agentRuntime, err := runtime.Launch(ctx, workspaceID, worktree, "claude")
	require.NoError(err)

	activity := agentactivity.NewStore(t.TempDir())
	handler := New(Deps{
		DB: database, Workspaces: manager, Runtime: runtime, AgentActivity: activity,
	})
	summary, err := database.GetWorkspaceSummary(ctx, workspaceID)
	require.NoError(err)

	tmuxCalls := func() string {
		raw, _ := os.ReadFile(logPath)
		return string(raw)
	}

	// Without any hook report the classic pane probe runs.
	result := handler.workspaceResponseWithTmuxEnrichment(ctx, summary)
	assert.NotEmpty(tmuxCalls(), "an uncovered workspace is probed through tmux")
	assert.False(result.tmuxComplete, "the fake tmux fails, so the probe is incomplete")

	// A live agent session reporting through hooks makes the probe redundant.
	require.NoError(activity.HandleEvent("claude", agentactivity.HookEvent{
		SessionID: "claude-session", CWD: worktree, HookEventName: "UserPromptSubmit",
	}, agentRuntime.Key))
	require.NoError(os.Remove(logPath))
	result = handler.workspaceResponseWithTmuxEnrichment(ctx, summary)
	assert.Empty(tmuxCalls(), "a hook-covered workspace spawns no tmux probe")
	assert.True(result.tmuxComplete, "hook coverage counts as a complete sample")
	assert.False(result.response.TmuxWorking)
	assert.Nil(result.response.TmuxLastOutputAt)

	full := handler.workspaceResponseWithEnrichment(ctx, summary)
	assert.Empty(tmuxCalls(), "the full enrichment pass honours the same gate")
	assert.True(full.tmuxComplete)

	// Once the session ends its hook report is gone and tmux is probed again.
	require.NoError(activity.HandleEvent("claude", agentactivity.HookEvent{
		SessionID: "claude-session", CWD: worktree, HookEventName: "SessionEnd",
	}, agentRuntime.Key))
	_ = handler.workspaceResponseWithTmuxEnrichment(ctx, summary)
	assert.NotEmpty(tmuxCalls(), "losing hook coverage resumes the tmux probe")
}
