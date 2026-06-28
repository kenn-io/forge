package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupDivergenceWorktree creates a small repo with a remote +
// local clone whose `feature` branch tracks `origin/feature`. The
// returned path points at the working tree where ahead/behind
// commits can be staged.
func setupDivergenceWorktree(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	work := filepath.Join(root, "work")

	runWorkspaceTestGit(
		t, root, "init", "--bare", "--initial-branch=main", remote,
	)
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
		filepath.Join(work, "f.txt"), []byte("f1\n"), 0o644,
	))
	runWorkspaceTestGit(t, work, "add", ".")
	runWorkspaceTestGit(t, work, "commit", "-m", "feature 1")
	runWorkspaceTestGit(t, work, "push", "-u", "origin", "feature")
	return work
}

func TestWorktreeDivergenceCleanInSync(t *testing.T) {
	work := setupDivergenceWorktree(t)

	div, ok, err := WorktreeDivergence(t.Context(), work)
	require := require.New(t)
	require.NoError(err)
	require.True(ok)
	assert := assert.New(t)
	assert.Equal(0, div.Ahead)
	assert.Equal(0, div.Behind)
}

func TestWorktreeDivergenceAheadOfRemote(t *testing.T) {
	work := setupDivergenceWorktree(t)
	require.NoError(t, os.WriteFile(
		filepath.Join(work, "f.txt"), []byte("ahead-1\n"), 0o644,
	))
	runWorkspaceTestGit(t, work, "add", ".")
	runWorkspaceTestGit(t, work, "commit", "-m", "ahead 1")
	require.NoError(t, os.WriteFile(
		filepath.Join(work, "f.txt"), []byte("ahead-2\n"), 0o644,
	))
	runWorkspaceTestGit(t, work, "add", ".")
	runWorkspaceTestGit(t, work, "commit", "-m", "ahead 2")

	div, ok, err := WorktreeDivergence(t.Context(), work)
	require := require.New(t)
	require.NoError(err)
	require.True(ok)
	assert := assert.New(t)
	assert.Equal(2, div.Ahead)
	assert.Equal(0, div.Behind)
}

func TestWorktreeDivergenceBehindRemote(t *testing.T) {
	require := require.New(t)
	work := setupDivergenceWorktree(t)

	// Push an extra commit from a parallel clone so `origin/feature`
	// moves forward without the local `work` clone advancing.
	other := filepath.Join(filepath.Dir(work), "other")
	remote := filepath.Join(filepath.Dir(work), "remote.git")
	runWorkspaceTestGit(t, filepath.Dir(work), "clone", remote, other)
	runWorkspaceTestGit(t, other, "config", "user.email", "o@test.com")
	runWorkspaceTestGit(t, other, "config", "user.name", "Other")
	runWorkspaceTestGit(t, other, "checkout", "-b", "feature", "origin/feature")
	require.NoError(os.WriteFile(
		filepath.Join(other, "f.txt"), []byte("remote-extra\n"), 0o644,
	))
	runWorkspaceTestGit(t, other, "add", ".")
	runWorkspaceTestGit(t, other, "commit", "-m", "remote extra")
	runWorkspaceTestGit(t, other, "push", "origin", "feature")

	runWorkspaceTestGit(t, work, "fetch", "origin")

	div, ok, err := WorktreeDivergence(t.Context(), work)
	require.NoError(err)
	require.True(ok)
	assert := assert.New(t)
	assert.Equal(0, div.Ahead)
	assert.Equal(1, div.Behind)
}

func TestWorktreeDivergenceWithoutUpstream(t *testing.T) {
	require := require.New(t)
	root := t.TempDir()
	work := filepath.Join(root, "work")
	runWorkspaceTestGit(t, root, "init", "--initial-branch=main", work)
	runWorkspaceTestGit(t, work, "config", "user.email", "t@test.com")
	runWorkspaceTestGit(t, work, "config", "user.name", "Test")
	require.NoError(os.WriteFile(
		filepath.Join(work, "x.txt"), []byte("x\n"), 0o644,
	))
	runWorkspaceTestGit(t, work, "add", ".")
	runWorkspaceTestGit(t, work, "commit", "-m", "init")

	div, ok, err := WorktreeDivergence(t.Context(), work)
	require.NoError(err)
	assert.New(t).False(ok, "expected ok=false for branch without upstream")
	assert.New(t).Equal(Divergence{}, div)
}

func TestWorktreeUnpushedSHAsInSync(t *testing.T) {
	work := setupDivergenceWorktree(t)

	unpushed, ok, err := WorktreeUnpushedSHAs(t.Context(), work)
	require := require.New(t)
	require.NoError(err)
	require.True(ok)
	assert.New(t).Empty(unpushed)
}

func TestWorktreeUnpushedSHAsAheadOfRemote(t *testing.T) {
	require := require.New(t)
	work := setupDivergenceWorktree(t)
	pushedHead := revParseTestSHA(t, work, "HEAD")

	require.NoError(os.WriteFile(
		filepath.Join(work, "f.txt"), []byte("ahead-1\n"), 0o644,
	))
	runWorkspaceTestGit(t, work, "add", ".")
	runWorkspaceTestGit(t, work, "commit", "-m", "ahead 1")
	first := revParseTestSHA(t, work, "HEAD")

	require.NoError(os.WriteFile(
		filepath.Join(work, "f.txt"), []byte("ahead-2\n"), 0o644,
	))
	runWorkspaceTestGit(t, work, "add", ".")
	runWorkspaceTestGit(t, work, "commit", "-m", "ahead 2")
	second := revParseTestSHA(t, work, "HEAD")

	unpushed, ok, err := WorktreeUnpushedSHAs(t.Context(), work)
	require.NoError(err)
	require.True(ok)
	assert := assert.New(t)
	assert.Len(unpushed, 2)
	assert.Contains(unpushed, first)
	assert.Contains(unpushed, second)
	assert.NotContains(unpushed, pushedHead,
		"commit already on the upstream must not be reported as unpushed")
}

func TestWorktreeUnpushedSHAsWithoutUpstream(t *testing.T) {
	require := require.New(t)
	root := t.TempDir()
	work := filepath.Join(root, "work")
	runWorkspaceTestGit(t, root, "init", "--initial-branch=main", work)
	runWorkspaceTestGit(t, work, "config", "user.email", "t@test.com")
	runWorkspaceTestGit(t, work, "config", "user.name", "Test")
	require.NoError(os.WriteFile(
		filepath.Join(work, "x.txt"), []byte("x\n"), 0o644,
	))
	runWorkspaceTestGit(t, work, "add", ".")
	runWorkspaceTestGit(t, work, "commit", "-m", "init")

	unpushed, ok, err := WorktreeUnpushedSHAs(t.Context(), work)
	require.NoError(err)
	assert.New(t).False(ok, "expected ok=false for branch without upstream")
	assert.New(t).Nil(unpushed)
}

func revParseTestSHA(t *testing.T, dir, ref string) string {
	t.Helper()
	return strings.TrimSpace(string(
		runWorkspaceTestGit(t, dir, "rev-parse", ref),
	))
}
