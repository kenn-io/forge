package workspace

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/testutil/gitfixture"
	"go.kenn.io/forge/internal/testutil/gitsafe"
	"go.kenn.io/kwt/worktree"
)

func TestManagerUsesInjectedRepositoryCoordinator(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	root := t.TempDir()
	fixture := gitfixture.NewRepository(t, false)
	// A lock root distinct from the manager's own makes injection observable:
	// a coordinator the manager built itself would lock elsewhere and not wait.
	coordinator, err := NewRepositoryCoordinator(t.TempDir())
	require.NoError(err)
	manager := NewManager(nil, root, coordinator)
	repo, err := coordinator.Open(t.Context(), worktree.RepositoryOptions{Path: fixture.Dir, Runner: gitsafe.Runner()})
	require.NoError(err)
	// Simulate a Projects operation holding the injected coordinator. An item
	// operation must wait for the same lock and honor cancellation.
	require.NoError(repo.WithLock(t.Context(), func(*worktree.Scope) error {
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		called := false
		err := manager.withRepoLockForGitDir(ctx, fixture.Dir, func(_ *worktree.Scope) error { called = true; return nil })
		require.ErrorIs(err, context.DeadlineExceeded)
		require.False(called)
		return nil
	}))
	called := false
	require.NoError(manager.withRepoLockForGitDir(t.Context(), fixture.Dir, func(_ *worktree.Scope) error { called = true; return nil }))
	require.True(called)
}

func TestExecutionWorkerConfigurationSharesRepositoryLock(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	path := gitfixture.DivergenceWorktree(t)
	manager := newWorkspaceTestManager(t, nil, t.TempDir())
	manager.SetExecutionWorker(config.ExecutionWorker{
		Enabled: true, CommitName: "Developer A", CommitEmail: "developer@example.com",
		BrokerSocket: "/run/example/broker.sock",
	}, "/opt/example/bin/forge")
	runWorkspaceTestGit(t, path, "config", "user.name", "Original Name")
	repo, err := manager.openRepositoryWorktrees(t.Context(), path)
	require.NoError(err)
	require.NoError(repo.WithLock(t.Context(), func(*worktree.Scope) error {
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		err := manager.configureExecutionWorktree(ctx, path)
		require.ErrorIs(err, context.DeadlineExceeded)
		require.Equal("Original Name\n", string(runWorkspaceTestGit(t, path, "config", "user.name")))
		return nil
	}))
	require.NoError(manager.configureExecutionWorktree(t.Context(), path))
	require.Equal("Developer A\n", string(runWorkspaceTestGit(t, path, "config", "user.name")))
}
