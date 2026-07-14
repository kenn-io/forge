package docs

import (
	"context"
	"errors"
	"fmt"
	"strings"

	gitcmd "go.kenn.io/kit/git/cmd"
)

// ErrDiverged is returned when the local branch and its upstream have both
// moved: completing the pull would need a merge or rebase, which the docs
// UI does not do. The user resolves divergence in a real git client.
var ErrDiverged = errors.New("local branch and upstream have diverged; resolve with a git client")

// PullFailedError reports a failed fetch or a refused fast-forward (for
// example dirty tracked files the update would overwrite), carrying git's
// stderr for the UI.
type PullFailedError struct {
	Stderr string
}

func (e *PullFailedError) Error() string {
	return fmt.Sprintf("git pull failed: %s", e.Stderr)
}

type PullResponse struct {
	Branch      string `json:"branch"`
	Upstream    string `json:"upstream"`
	UpToDate    bool   `json:"up_to_date"`
	Commit      string `json:"commit"`
	ShortCommit string `json:"short_commit"`
}

// GitPull fast-forwards the docs folder's branch to its upstream: the same
// fetch and ff-only merge the user's own `git pull --ff-only` would run,
// through the standard docs runner. Divergence is detected with
// merge-base ancestry checks (no stderr parsing) and reported as a typed
// error instead of a conflict state on disk.
//
// Pull deliberately skips publish's command-bearing-config and attribute
// gates (assertSafeToPublish, assertWorktreeAttributesSafe). Those gates
// exist because publish-side commands rehash worktree content implicitly —
// the status/changes previews run whenever the UI shows a folder, so a
// repo-local filter must be rejected before middleman triggers it in the
// background. Pull is the opposite shape: an explicit user action against
// a folder and upstream the user registered by hand, carrying exactly the
// trust of typing `git pull` in that directory. The docs runner still
// neutralizes the surfaces that need no trust decision at all (hooks,
// fsmonitor, transport allowlist, stripped env).
//
// Overwrite behavior is exactly a terminal pull's, no more and no less:
// the fast-forward checkout is git's own, so it refuses to touch tracked
// files whose worktree content differs from HEAD (the same protection a
// terminal pull gives against a concurrent editor save), and it applies
// git's standard rule that a gitignored untracked file may be replaced
// when an incoming commit starts tracking the same path. Guarding beyond
// git's own semantics is deliberately out of scope.
func (r *Registry) GitPull(ctx context.Context, folderID string) (PullResponse, error) {
	v, err := r.Lookup(folderID)
	if err != nil {
		return PullResponse{}, err
	}
	if !isGitRepo(v.Path) {
		return PullResponse{}, ErrNotAGitRepo
	}
	branch, err := currentBranch(ctx, v.Path)
	if err != nil {
		return PullResponse{}, err
	}
	noUpstream := &NoUpstreamError{
		Branch:           branch,
		SuggestedCommand: fmt.Sprintf("git branch --set-upstream-to=origin/%s %s", branch, branch),
	}
	upstream, err := currentUpstream(ctx, v.Path, branch)
	if err != nil || upstream == "" {
		return PullResponse{}, noUpstream
	}
	remote, mergeRef, err := currentUpstreamPushTarget(ctx, v.Path, branch)
	if err != nil || remote == "" || mergeRef == "" {
		return PullResponse{}, noUpstream
	}
	// A source-only refspec still updates the remote-tracking ref: git
	// opportunistically writes refs/remotes/<remote>/<branch> whenever the
	// command-line ref matches the configured fetch refspec, so origin/main
	// does not go stale after this fetch (asserted in the integration test).
	if _, err := runDocsGit(ctx, v.Path, nil, "fetch", remote, mergeRef); err != nil {
		return PullResponse{}, &PullFailedError{Stderr: gitStderr(err)}
	}
	head, err := revParse(ctx, v.Path, "HEAD")
	if err != nil {
		return PullResponse{}, err
	}
	fetchHead, err := revParse(ctx, v.Path, "FETCH_HEAD")
	if err != nil {
		return PullResponse{}, err
	}
	res := PullResponse{Branch: branch, Upstream: upstream}
	upToDate, err := isAncestor(ctx, v.Path, fetchHead, head)
	if err != nil {
		return PullResponse{}, err
	}
	if upToDate {
		res.UpToDate = true
		res.Commit = head
		res.ShortCommit = head[:7]
		return res, nil
	}
	canFastForward, err := isAncestor(ctx, v.Path, head, fetchHead)
	if err != nil {
		return PullResponse{}, err
	}
	if !canFastForward {
		return PullResponse{}, ErrDiverged
	}
	if _, err := runDocsGit(ctx, v.Path, nil, "merge", "--ff-only", "FETCH_HEAD"); err != nil {
		return PullResponse{}, &PullFailedError{Stderr: gitStderr(err)}
	}
	res.Commit = fetchHead
	res.ShortCommit = fetchHead[:7]
	return res, nil
}

func revParse(ctx context.Context, root, rev string) (string, error) {
	out, err := runDocsGit(ctx, root, nil, "rev-parse", rev)
	if err != nil {
		return "", fmt.Errorf("git rev-parse %s: %w", rev, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// isAncestor reports whether ancestor is reachable from descendant. git
// merge-base --is-ancestor signals its answer through the exit code: 0 for
// yes, 1 for no, anything else is a real failure.
func isAncestor(ctx context.Context, root, ancestor, descendant string) (bool, error) {
	_, err := runDocsGit(ctx, root, nil, "merge-base", "--is-ancestor", ancestor, descendant)
	if err == nil {
		return true, nil
	}
	if ge, ok := errors.AsType[*gitcmd.GitError](err); ok {
		if code, ok := ge.ExitCode(); ok && code == 1 {
			return false, nil
		}
	}
	return false, fmt.Errorf("git merge-base: %w", err)
}
