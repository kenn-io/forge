package workspace

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

var (
	ErrWorktreeDirty      = errors.New("worktree has uncommitted changes")
	ErrWorktreeDiverged   = errors.New("worktree branch diverged from upstream")
	ErrWorktreeNoUpstream = errors.New("worktree branch has no upstream")
	ErrWorktreeInSync     = errors.New("worktree branch is already in sync")
)

type branchUpstream struct {
	remote string
	branch string
}

// PushWorktreeBranch pushes the current branch to its configured upstream.
// This is a user-triggered git operation, so it intentionally uses normal git
// hook behavior instead of the internal no-hooks mutation helper.
func PushWorktreeBranch(ctx context.Context, dir string) error {
	if err := ensureBranchSyncClean(ctx, dir); err != nil {
		return err
	}
	upstream, err := currentBranchUpstream(ctx, dir)
	if err != nil {
		return err
	}
	if err := refreshBranchUpstream(ctx, dir, upstream); err != nil {
		return err
	}
	div, err := branchSyncDivergence(ctx, dir)
	if err != nil {
		return err
	}
	if div.Behind > 0 {
		return fmt.Errorf("%w: %d ahead, %d behind", ErrWorktreeDiverged, div.Ahead, div.Behind)
	}
	if div.Ahead == 0 {
		return ErrWorktreeInSync
	}
	if _, err := gitCombinedOutput(ctx, dir, "push", upstream.remote, "HEAD:"+upstream.branch); err != nil {
		return fmt.Errorf("git push: %w", err)
	}
	if err := refreshBranchUpstream(ctx, dir, upstream); err != nil {
		return fmt.Errorf("refresh after push: %w", err)
	}
	return nil
}

// PullWorktreeBranch fast-forwards the current branch from its configured
// upstream. It rejects dirty or diverged worktrees so the UI action cannot
// silently merge, rebase, or overwrite local work.
func PullWorktreeBranch(ctx context.Context, dir string) error {
	if err := ensureBranchSyncClean(ctx, dir); err != nil {
		return err
	}
	upstream, err := currentBranchUpstream(ctx, dir)
	if err != nil {
		return err
	}
	if err := refreshBranchUpstream(ctx, dir, upstream); err != nil {
		return err
	}
	div, err := branchSyncDivergence(ctx, dir)
	if err != nil {
		return err
	}
	if div.Ahead > 0 {
		return fmt.Errorf("%w: %d ahead, %d behind", ErrWorktreeDiverged, div.Ahead, div.Behind)
	}
	if div.Behind == 0 {
		return ErrWorktreeInSync
	}
	if _, err := gitCombinedOutput(ctx, dir, "merge", "--ff-only", "@{upstream}"); err != nil {
		return fmt.Errorf("git merge --ff-only upstream: %w", err)
	}
	return nil
}

func ensureBranchSyncClean(ctx context.Context, dir string) error {
	dirty, err := dirtyFiles(ctx, dir)
	if err != nil {
		return fmt.Errorf("check worktree dirty state: %w", err)
	}
	if len(dirty) > 0 {
		return fmt.Errorf("%w: %s", ErrWorktreeDirty, strings.Join(dirty, ", "))
	}
	return nil
}

func currentBranchUpstream(ctx context.Context, dir string) (branchUpstream, error) {
	out, err := gitCombinedOutput(ctx, dir, "branch", "--show-current")
	if err != nil {
		return branchUpstream{}, fmt.Errorf("git branch --show-current: %w", err)
	}
	branch := strings.TrimSpace(out)
	if branch == "" {
		return branchUpstream{}, ErrWorktreeNoUpstream
	}

	remote, err := gitCombinedOutput(ctx, dir, "config", "--get", "branch."+branch+".remote")
	if err != nil {
		return branchUpstream{}, fmt.Errorf("%w: branch %s", ErrWorktreeNoUpstream, branch)
	}
	mergeRef, err := gitCombinedOutput(ctx, dir, "config", "--get", "branch."+branch+".merge")
	if err != nil {
		return branchUpstream{}, fmt.Errorf("%w: branch %s", ErrWorktreeNoUpstream, branch)
	}
	upstream := branchUpstream{
		remote: strings.TrimSpace(remote),
		branch: strings.TrimPrefix(strings.TrimSpace(mergeRef), "refs/heads/"),
	}
	if upstream.remote == "" || upstream.branch == "" {
		return branchUpstream{}, fmt.Errorf("%w: branch %s", ErrWorktreeNoUpstream, branch)
	}
	return upstream, nil
}

func refreshBranchUpstream(ctx context.Context, dir string, upstream branchUpstream) error {
	refspec := "+refs/heads/" + upstream.branch + ":refs/remotes/" + upstream.remote + "/" + upstream.branch
	if _, err := gitCombinedOutput(ctx, dir, "fetch", "--prune", upstream.remote, refspec); err != nil {
		return fmt.Errorf("git fetch %s %s: %w", upstream.remote, upstream.branch, err)
	}
	return nil
}

func branchSyncDivergence(ctx context.Context, dir string) (Divergence, error) {
	div, ok, err := WorktreeDivergence(ctx, dir)
	if err != nil {
		return Divergence{}, err
	}
	if !ok {
		return Divergence{}, ErrWorktreeNoUpstream
	}
	return div, nil
}
