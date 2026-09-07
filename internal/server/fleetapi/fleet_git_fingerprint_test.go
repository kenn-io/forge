package fleetapi

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedFingerprintRepo creates a repository with one commit and a linked
// worktree, returning the root checkout and the linked worktree path.
func seedFingerprintRepo(t *testing.T) (string, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repoDir := t.TempDir()
	runGit(t, repoDir, "init", "-q")
	runGit(t, repoDir, "config", "user.email", "t@e.st")
	runGit(t, repoDir, "config", "user.name", "Tester")
	require.NoError(t, os.WriteFile(
		filepath.Join(repoDir, "base.txt"), []byte("base\n"), 0o644,
	))
	runGit(t, repoDir, "add", ".")
	runGit(t, repoDir, "commit", "-q", "-m", "base")
	wtDir := filepath.Join(t.TempDir(), "feature")
	runGit(t, repoDir, "worktree", "add", "-q", "-b", "feature", wtDir)
	return repoDir, wtDir
}

func TestResolveGitDirsLayouts(t *testing.T) {
	repoDir, wtDir := seedFingerprintRepo(t)
	require := require.New(t)
	assert := assert.New(t)

	root, err := resolveGitDirs(repoDir)
	require.NoError(err)
	assert.Equal(filepath.Join(repoDir, ".git"), root.gitDir)
	assert.Equal(root.gitDir, root.commonDir, "a main checkout shares one directory")

	// git records symlink-resolved absolute paths in the worktree pointer
	// files, so compare in resolved space.
	linked, err := resolveGitDirs(wtDir)
	require.NoError(err)
	assert.Equal(
		resolvedPathKey(filepath.Join(repoDir, ".git", "worktrees", "feature")),
		resolvedPathKey(linked.gitDir),
	)
	assert.Equal(
		resolvedPathKey(filepath.Join(repoDir, ".git")),
		resolvedPathKey(linked.commonDir),
		"a linked worktree's refs live in the main repository",
	)

	bareDir := t.TempDir()
	runGit(t, bareDir, "init", "-q", "--bare")
	bare, err := resolveGitDirs(bareDir)
	require.NoError(err)
	assert.Equal(bareDir, bare.gitDir)
	assert.Equal(bareDir, bare.commonDir)

	_, err = resolveGitDirs(t.TempDir())
	assert.Error(err, "a plain directory is not a checkout")
}

func TestWorktreeStatsFingerprintTracksGitDirectoryChanges(t *testing.T) {
	repoDir, wtDir := seedFingerprintRepo(t)
	require := require.New(t)
	assert := assert.New(t)

	first, err := worktreeStatsFingerprint(wtDir)
	require.NoError(err)
	again, err := worktreeStatsFingerprint(wtDir)
	require.NoError(err)
	assert.Equal(first, again, "an untouched checkout is stable")

	// Staging changes the index in the linked worktree's own git directory.
	require.NoError(os.WriteFile(
		filepath.Join(wtDir, "new.txt"), []byte("x\n"), 0o644,
	))
	afterWrite, err := worktreeStatsFingerprint(wtDir)
	require.NoError(err)
	assert.Equal(first, afterWrite, "an unstaged working-tree file is outside the fingerprint")
	runGit(t, wtDir, "add", "new.txt")
	afterAdd, err := worktreeStatsFingerprint(wtDir)
	require.NoError(err)
	assert.NotEqual(afterWrite, afterAdd, "staging moves the index")

	// Committing moves the branch ref under the shared refs tree.
	runGit(t, wtDir, "commit", "-q", "-m", "feature")
	afterCommit, err := worktreeStatsFingerprint(wtDir)
	require.NoError(err)
	assert.NotEqual(afterAdd, afterCommit, "a commit moves the branch ref")

	// A commit on the default branch in the root checkout changes the merge
	// base of the linked worktree, which shares the refs tree.
	beforeMain, err := worktreeStatsFingerprint(wtDir)
	require.NoError(err)
	runGit(t, repoDir, "commit", "-q", "--allow-empty", "-m", "main moves")
	afterMain, err := worktreeStatsFingerprint(wtDir)
	require.NoError(err)
	assert.NotEqual(beforeMain, afterMain, "the default branch ref is part of the fingerprint")

	// Packing refs rewrites packed-refs and removes loose refs.
	runGit(t, repoDir, "pack-refs", "--all")
	afterPack, err := worktreeStatsFingerprint(wtDir)
	require.NoError(err)
	assert.NotEqual(afterMain, afterPack)
}

func TestProjectDiscoveryFingerprintTracksWorktreeRegistry(t *testing.T) {
	repoDir, wtDir := seedFingerprintRepo(t)
	require := require.New(t)
	assert := assert.New(t)

	first, err := projectDiscoveryFingerprint(repoDir)
	require.NoError(err)
	again, err := projectDiscoveryFingerprint(repoDir)
	require.NoError(err)
	assert.Equal(first, again)

	runGit(t, repoDir, "worktree", "remove", wtDir)
	afterRemove, err := projectDiscoveryFingerprint(repoDir)
	require.NoError(err)
	assert.NotEqual(first, afterRemove, "removing a worktree changes worktrees/")

	other := filepath.Join(t.TempDir(), "other")
	runGit(t, repoDir, "worktree", "add", "-q", "-b", "other", other)
	afterAdd, err := projectDiscoveryFingerprint(repoDir)
	require.NoError(err)
	assert.NotEqual(afterRemove, afterAdd, "adding a worktree changes worktrees/")

	runGit(t, repoDir, "remote", "add", "origin", repoDir)
	afterRemote, err := projectDiscoveryFingerprint(repoDir)
	require.NoError(err)
	assert.NotEqual(afterAdd, afterRemote, "config is part of the discovery inputs")

	_, err = projectDiscoveryFingerprint(filepath.Join(t.TempDir(), "gone"))
	assert.Error(err)
}
