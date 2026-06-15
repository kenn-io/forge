package workspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	gitcmd "go.kenn.io/kit/git/cmd"
	"go.kenn.io/middleman/internal/procutil"
)

// WorktreeDiffTotals returns a worktree's total added/removed line counts for
// the whole-branch diff the sidebar shows — the change relative to where the
// branch forked from its default branch, including uncommitted edits and
// untracked files — not just the working-tree delta against HEAD.
//
// It mirrors the sidebar by cascading base references: the merge base with
// origin/<defaultBranch>, then with the local <defaultBranch>, then (for an
// unrelated-history branch that shares no merge base) the empty tree, and
// finally a plain HEAD diff. Like the sidebar it detects renames and copies so
// a moved file counts its content delta, not full delete/add churn, and it adds
// untracked text files as pure additions. Binary files contribute zero. The
// boolean is false when no base resolves at all (for example an empty
// repository with no commit); the error is reserved for an unexpected git
// failure, not a missing ref.
func WorktreeDiffTotals(
	ctx context.Context, dir, defaultBranch string,
) (added, removed int, ok bool, err error) {
	added, removed, ok, err = worktreeTrackedDiffTotals(ctx, dir, defaultBranch)
	if err != nil || !ok {
		return 0, 0, ok, err
	}
	return added + worktreeUntrackedAdditions(ctx, dir), removed, true, nil
}

// worktreeTrackedDiffTotals sums the tracked-file diff against the first base
// reference that resolves.
func worktreeTrackedDiffTotals(
	ctx context.Context, dir, defaultBranch string,
) (added, removed int, ok bool, err error) {
	if dir == "" {
		return 0, 0, false, errors.New("empty worktree dir")
	}
	if _, statErr := os.Stat(dir); statErr != nil {
		return 0, 0, false, nil
	}

	sawNoMergeBase := false
	for _, ref := range mergeBaseRefs(defaultBranch) {
		a, r, hit, noMergeBase, runErr := worktreeNumstatAgainst(
			ctx, dir, "--merge-base", ref,
		)
		if runErr != nil {
			return 0, 0, false, runErr
		}
		if hit {
			return a, r, true, nil
		}
		if noMergeBase {
			sawNoMergeBase = true
		}
	}

	if sawNoMergeBase {
		if hash, hashOK := worktreeEmptyTreeHash(ctx, dir); hashOK {
			a, r, hit, _, runErr := worktreeNumstatAgainst(ctx, dir, hash)
			if runErr != nil {
				return 0, 0, false, runErr
			}
			if hit {
				return a, r, true, nil
			}
		}
	}

	a, r, hit, _, runErr := worktreeNumstatAgainst(ctx, dir, "HEAD")
	if runErr != nil {
		return 0, 0, false, runErr
	}
	if hit {
		return a, r, true, nil
	}
	return 0, 0, false, nil
}

// mergeBaseRefs is the ordered set of default-branch references the diff is
// measured against, remote first. An empty default branch yields no refs, so
// the cascade falls straight through to the HEAD diff.
func mergeBaseRefs(defaultBranch string) []string {
	defaultBranch = strings.TrimSpace(defaultBranch)
	if defaultBranch == "" {
		return nil
	}
	return []string{"origin/" + defaultBranch, defaultBranch}
}

// worktreeNumstatAgainst runs `git diff --numstat -z <baseArgs> -- .` and sums
// the per-file counts. hit is true when git exited cleanly; noMergeBase reports
// the "no merge base" failure so the caller can fall back to the empty tree.
// Only the expected cascade misses — a missing/unknown ref or no merge base —
// are swallowed; any other non-zero git exit (corrupt repository, dubious
// ownership, permission failure) is returned as an error so a failed probe is
// never mistaken for a clean zero diff.
func worktreeNumstatAgainst(
	ctx context.Context, dir string, baseArgs ...string,
) (added, removed int, hit, noMergeBase bool, err error) {
	// The same rename/copy detection flags as the sidebar diff path, so a
	// renamed or copied file counts its content delta instead of full
	// delete/add churn.
	args := append(
		[]string{"diff", "--numstat", "-z", "-M", "-C", "--find-copies-harder"},
		baseArgs...,
	)
	args = append(args, "--", ".")
	cmd := gitcmd.New().Command(ctx, dir, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// The sampler probes every worktree on each cycle, so the diff must respect
	// the global git subprocess limiter. cmd.Run() is gated manually (instead of
	// procutil.Output) because the cascade needs stderr captured separately to
	// classify a missing ref vs an unrelated-history branch.
	release, acquireErr := procutil.TryAcquire(
		ctx, "git diff numstat subprocess capacity",
	)
	if acquireErr != nil {
		return 0, 0, false, false, acquireErr
	}
	defer release()

	if runErr := cmd.Run(); runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			msg := stderr.String()
			if isNoMergeBaseStderr(msg) {
				return 0, 0, false, true, nil
			}
			if isMissingRefStderr(msg) {
				return 0, 0, false, false, nil
			}
		}
		return 0, 0, false, false, fmt.Errorf(
			"git diff --numstat: %w: %s", runErr, strings.TrimSpace(stderr.String()),
		)
	}

	added, removed = sumWorktreeNumstatZ(stdout.Bytes())
	return added, removed, true, false, nil
}

// worktreeUntrackedAdditions sums the line counts of untracked text files,
// reusing the sidebar's untracked-file path so binary files contribute zero.
func worktreeUntrackedAdditions(ctx context.Context, dir string) int {
	total := 0
	for _, file := range worktreeUntrackedFiles(ctx, dir, false, false) {
		total += file.Additions
	}
	return total
}

// sumWorktreeNumstatZ totals the added/removed counts across all files in a
// NUL-delimited numstat payload, reusing the shared per-file parser.
func sumWorktreeNumstatZ(data []byte) (added, removed int) {
	for _, count := range parseWorktreeNumstatZ(data) {
		added += count.additions
		removed += count.deletions
	}
	return added, removed
}

// worktreeEmptyTreeHash returns the repository's empty-tree object id, used as a
// diff base for an unrelated-history branch. Empty stdin makes git compute the
// hash for the repo's object format without relying on /dev/null.
func worktreeEmptyTreeHash(ctx context.Context, dir string) (string, bool) {
	out, err := worktreeGitOutputWithInput(
		ctx, dir, []byte{}, "hash-object", "-t", "tree", "--stdin",
	)
	if err != nil {
		return "", false
	}
	hash := strings.TrimSpace(string(out))
	return hash, hash != ""
}

// isNoMergeBaseStderr reports git's "no merge base" failure, which means the
// branch and the base ref share no common history.
func isNoMergeBaseStderr(stderr string) bool {
	return strings.Contains(strings.ToLower(stderr), "no merge base")
}

// isMissingRefStderr reports the git failures that mean the base ref simply
// does not exist in this repository — the expected cascade miss when a
// default branch has no origin/<branch> remote ref, or HEAD is unborn.
func isMissingRefStderr(stderr string) bool {
	msg := strings.ToLower(stderr)
	return strings.Contains(msg, "unknown revision") ||
		strings.Contains(msg, "bad revision") ||
		strings.Contains(msg, "ambiguous argument")
}
