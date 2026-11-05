package gitclone

import (
	"context"

	gitcmd "go.kenn.io/kit/git/cmd"
	"go.kenn.io/kwt/worktree"
)

// RepositoryLockFile coordinates clone fetches with workspace creation and warming.
const RepositoryLockFile = ".kenn-forge-worktree.lock"

func withCloneWorktreeLock(ctx context.Context, dir string, run func() ([]byte, error)) ([]byte, error) {
	coordinator, err := worktree.NewCoordinator(worktree.LockPolicy{FileName: RepositoryLockFile, BareUsesSuppliedPath: true})
	if err != nil {
		return nil, err
	}
	repo, err := coordinator.Open(ctx, worktree.RepositoryOptions{Path: dir, Runner: gitcmd.New(), RunGit: func(ctx context.Context, runner gitcmd.Runner, dir string, args ...string) ([]byte, error) {
		out, stderr, err := runGitCommand(ctx, runner, dir, nil, args...)
		return append(out, stderr...), err
	}})
	if err != nil {
		return nil, err
	}
	var out []byte
	err = repo.WithLock(ctx, func(*worktree.Scope) error {
		// Git 2.43 publishes a zero HEAD while registering a linked worktree.
		// Fetch's connectivity check rejects it until creation finishes.
		out, err = run()
		return err
	})
	return out, err
}
