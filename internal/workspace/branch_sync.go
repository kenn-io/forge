package workspace

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"go.kenn.io/middleman/internal/tokenauth"
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

// networkedBranchGit runs a branch-sync git command that contacts the remote
// (fetch or push) in the worktree dir. It returns only an error because branch
// sync never needs the command's stdout, and the managed implementation
// deliberately discards git's raw output so credential material in a remote
// error string cannot leak back through the API.
type networkedBranchGit func(ctx context.Context, dir string, args ...string) error

// branchSyncGit returns the runner used for the networked steps of branch
// push/pull. With clone management configured it routes fetch and push through
// the authenticated gitclone runner so the provider host's PAT or GitHub App
// credential is injected, expired tokens are re-resolved on auth failure, and
// raw git remote output is redacted out of returned errors. Without clone
// management (unmanaged local checkouts and unit tests) it falls back to plain
// git, which carries no injected credential.
func (m *Manager) branchSyncGit(platformHost string) networkedBranchGit {
	if m.clones == nil {
		return func(ctx context.Context, dir string, args ...string) error {
			_, err := gitCombinedOutput(ctx, dir, args...)
			return err
		}
	}
	return func(ctx context.Context, dir string, args ...string) error {
		if _, err := m.clones.RunGit(ctx, platformHost, dir, args...); err != nil {
			return err
		}
		return nil
	}
}

// PushWorktreeBranch pushes the current branch to its configured upstream.
// This is a user-triggered git operation, so it intentionally uses normal git
// hook behavior instead of the internal no-hooks mutation helper. The fetch
// and push are networked: they run through the host's authenticated git runner
// so managed HTTPS workspaces inject the provider credential, and the push is
// marked as a mutation so it stays on the user's own PAT chain rather than a
// GitHub App installation token.
func (m *Manager) PushWorktreeBranch(ctx context.Context, platformHost, dir string) error {
	return pushWorktreeBranch(ctx, m.branchSyncGit(platformHost), dir)
}

// PullWorktreeBranch fast-forwards the current branch from its configured
// upstream. It rejects dirty or diverged worktrees so the UI action cannot
// silently merge, rebase, or overwrite local work. The upstream refresh is
// networked and runs through the host's authenticated git runner; the merge
// itself is local against the already-fetched tracking ref.
func (m *Manager) PullWorktreeBranch(ctx context.Context, platformHost, dir string) error {
	return pullWorktreeBranch(ctx, m.branchSyncGit(platformHost), dir)
}

func pushWorktreeBranch(ctx context.Context, run networkedBranchGit, dir string) error {
	if err := ensureBranchSyncClean(ctx, dir); err != nil {
		return err
	}
	upstream, err := currentBranchUpstream(ctx, dir)
	if err != nil {
		return err
	}
	if err := refreshBranchUpstream(ctx, run, dir, upstream); err != nil {
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
	// Writes stay on the user's own credential chain so the pushed commits
	// are attributed to the user instead of a GitHub App bot.
	if err := run(
		tokenauth.WithMutationAuth(ctx), dir,
		"push", upstream.remote, "HEAD:"+upstream.branch,
	); err != nil {
		return fmt.Errorf("git push: %w", err)
	}
	if err := refreshBranchUpstream(ctx, run, dir, upstream); err != nil {
		return fmt.Errorf("refresh after push: %w", err)
	}
	return nil
}

func pullWorktreeBranch(ctx context.Context, run networkedBranchGit, dir string) error {
	if err := ensureBranchSyncClean(ctx, dir); err != nil {
		return err
	}
	upstream, err := currentBranchUpstream(ctx, dir)
	if err != nil {
		return err
	}
	if err := refreshBranchUpstream(ctx, run, dir, upstream); err != nil {
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

func refreshBranchUpstream(
	ctx context.Context, run networkedBranchGit, dir string, upstream branchUpstream,
) error {
	refspec := "+refs/heads/" + upstream.branch + ":refs/remotes/" + upstream.remote + "/" + upstream.branch
	if err := run(ctx, dir, "fetch", "--prune", upstream.remote, refspec); err != nil {
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
