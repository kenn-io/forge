package workspace

import (
	"context"
	"fmt"
	"path/filepath"

	"go.kenn.io/forge/internal/procutil"
	gitcmd "go.kenn.io/kit/git/cmd"
	"go.kenn.io/kwt/worktree"
)

// NewRepositoryCoordinator selects Forge's lock layout independently of the
// optional item-workspace manager. Projects are available without that manager.
func NewRepositoryCoordinator(worktreeDir string) (*worktree.Coordinator, error) {
	base := ""
	if worktreeDir != "" {
		root, err := filepath.Abs(worktreeDir)
		if err != nil {
			return nil, fmt.Errorf("resolve worktree root: %w", err)
		}
		base = filepath.Join(root, ".kenn-forge-worktree-base-locks")
	}
	return worktree.NewCoordinator(worktree.LockPolicy{
		FileName: ".kenn-forge-worktree.lock", NonBareRoot: base,
		BareUsesSuppliedPath: true,
	})
}

func (m *Manager) openRepositoryWorktrees(ctx context.Context, path string) (*worktree.Repository, error) {
	return openItemWorktreeRepository(ctx, m.repositoryWorktrees, path)
}

func openItemWorktreeRepository(ctx context.Context, coordinator *worktree.Coordinator, path string) (*worktree.Repository, error) {
	return coordinator.Open(ctx, worktree.RepositoryOptions{
		Path: path, Runner: gitcmd.New().WithConfig("core.hooksPath", "/dev/null"), RunGit: runRepositoryWorktreeGit,
	})
}

func runRepositoryWorktreeGit(ctx context.Context, runner gitcmd.Runner, dir string, args ...string) ([]byte, error) {
	release, err := procutil.TryAcquire(ctx, "worktree lifecycle git")
	if err != nil {
		return nil, err
	}
	defer release()
	stdout, stderr, err := runner.Run(ctx, dir, nil, args...)
	return append(stdout, stderr...), procutil.WrapResourceExhaustion(err, "worktree lifecycle git")
}
