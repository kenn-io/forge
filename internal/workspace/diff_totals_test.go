package workspace

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupDiffTotalsRepo builds a remote + clone whose `feature` branch is one
// commit ahead of main, mirroring setupDivergenceWorktree but returning a
// worktree on which whole-branch diff totals can be measured against main.
func setupDiffTotalsRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	work := filepath.Join(root, "work")

	runWorkspaceTestGit(t, root, "init", "--bare", "--initial-branch=main", remote)
	runWorkspaceTestGit(t, root, "clone", remote, work)
	runWorkspaceTestGit(t, work, "config", "user.email", "t@test.com")
	runWorkspaceTestGit(t, work, "config", "user.name", "Test")
	require.NoError(t, os.WriteFile(
		filepath.Join(work, "base.txt"), []byte("base\n"), 0o644,
	))
	runWorkspaceTestGit(t, work, "add", ".")
	runWorkspaceTestGit(t, work, "commit", "-m", "base")
	runWorkspaceTestGit(t, work, "push", "origin", "main")

	runWorkspaceTestGit(t, work, "checkout", "-b", "feature")
	require.NoError(t, os.WriteFile(
		filepath.Join(work, "feature.txt"), []byte("a\nb\n"), 0o644,
	))
	runWorkspaceTestGit(t, work, "add", ".")
	runWorkspaceTestGit(t, work, "commit", "-m", "feature commit")
	runWorkspaceTestGit(t, work, "push", "-u", "origin", "feature")
	return work
}

func TestWorktreeDiffTotalsWholeBranchAgainstDefault(t *testing.T) {
	require := require.New(t)
	work := setupDiffTotalsRepo(t)
	// Uncommitted edit on top of the committed feature work: the total must
	// fold both the committed file and the working-tree change, measured from
	// the merge base with origin/main.
	require.NoError(os.WriteFile(
		filepath.Join(work, "base.txt"), []byte("base\nextra\n"), 0o644,
	))

	added, removed, ok, err := WorktreeDiffTotals(t.Context(), work, "main")
	require.NoError(err)
	require.True(ok)
	require.Equal(3, added, "feature.txt (2) + base.txt extra line (1)")
	require.Equal(0, removed)
}

func TestWorktreeDiffTotalsCleanDefaultBranchIsZero(t *testing.T) {
	require := require.New(t)
	work := setupDiffTotalsRepo(t)
	runWorkspaceTestGit(t, work, "checkout", "main")

	added, removed, ok, err := WorktreeDiffTotals(t.Context(), work, "main")
	require.NoError(err)
	require.True(ok, "a clean default-branch worktree still resolves a base")
	require.Equal(0, added)
	require.Equal(0, removed)
}

func TestWorktreeDiffTotalsNoDefaultBranchCountsWorkingTreeOnly(t *testing.T) {
	require := require.New(t)
	work := setupDiffTotalsRepo(t)
	// With no default branch the cascade falls straight to the HEAD diff, which
	// sees only the uncommitted change — not the committed feature file.
	require.NoError(os.WriteFile(
		filepath.Join(work, "base.txt"), []byte("base\nworking\n"), 0o644,
	))

	added, removed, ok, err := WorktreeDiffTotals(t.Context(), work, "")
	require.NoError(err)
	require.True(ok)
	require.Equal(1, added, "only the uncommitted base.txt line")
	require.Equal(0, removed)
}

func TestWorktreeDiffTotalsUnrelatedHistoryUsesEmptyTree(t *testing.T) {
	require := require.New(t)
	root := t.TempDir()
	work := filepath.Join(root, "work")
	runWorkspaceTestGit(t, root, "init", "--initial-branch=main", work)
	runWorkspaceTestGit(t, work, "config", "user.email", "t@test.com")
	runWorkspaceTestGit(t, work, "config", "user.name", "Test")
	require.NoError(os.WriteFile(
		filepath.Join(work, "base.txt"), []byte("base\n"), 0o644,
	))
	runWorkspaceTestGit(t, work, "add", ".")
	runWorkspaceTestGit(t, work, "commit", "-m", "base")

	// An orphan branch shares no merge base with main, so the totals fall back
	// to the empty tree and count the whole orphan checkout.
	runWorkspaceTestGit(t, work, "checkout", "--orphan", "fresh")
	runWorkspaceTestGit(t, work, "rm", "-rf", ".")
	require.NoError(os.WriteFile(
		filepath.Join(work, "only.txt"), []byte("x\ny\nz\n"), 0o644,
	))
	runWorkspaceTestGit(t, work, "add", ".")
	runWorkspaceTestGit(t, work, "commit", "-m", "orphan")

	added, removed, ok, err := WorktreeDiffTotals(t.Context(), work, "main")
	require.NoError(err)
	require.True(ok)
	require.Equal(3, added, "the whole orphan checkout counts against the empty tree")
	require.Equal(0, removed)
}

func TestWorktreeDiffTotalsMissingDirIsNotAnError(t *testing.T) {
	require := require.New(t)
	added, removed, ok, err := WorktreeDiffTotals(
		t.Context(), filepath.Join(t.TempDir(), "gone"), "main",
	)
	require.NoError(err, "a missing worktree is a normal not-sampled outcome")
	require.False(ok)
	require.Equal(0, added)
	require.Equal(0, removed)
}

func TestWorktreeDiffTotalsEmptyDirErrors(t *testing.T) {
	_, _, ok, err := WorktreeDiffTotals(t.Context(), "", "main")
	require.Error(t, err)
	require.False(t, ok)
}

// TestWorktreeNumstatArgsCarriesDiffHardening pins the totals sampler to
// gitclone.DiffArgs so it keeps the --no-ext-diff/--no-textconv flags every
// other worktree diff in this package carries. A behavioral marker test (assert
// a repo-local diff.external/textconv script never runs) cannot distinguish the
// fix here: a plain `git diff --numstat` never invokes those drivers regardless
// of the flags, so such a test would pass with or without the hardening and be a
// tautology. This pins the argument construction instead — it fails if the path
// reverts to hand-built args that drop the hardening.
func TestWorktreeNumstatArgsCarriesDiffHardening(t *testing.T) {
	assert := assert.New(t)
	args := worktreeNumstatArgs("--merge-base", "origin/main")

	require.NotEmpty(t, args)
	assert.Equal("diff", args[0], "first token must be the git subcommand")
	assert.Contains(args, "--no-ext-diff", "external diff drivers must stay disabled")
	assert.Contains(args, "--no-textconv", "textconv filters must stay disabled")
	assert.Contains(args, "--numstat")
	// Base refs and the directory pathspec still thread through unchanged.
	assert.Contains(args, "--merge-base")
	assert.Contains(args, "origin/main")
	assert.Equal([]string{"--", "."}, args[len(args)-2:], "pathspec must terminate the args")
}
