package workspace

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func initFingerprintRepo(t *testing.T, dir string) {
	t.Helper()
	runWorkspaceTestGit(t, dir, "init", "--initial-branch=main", ".")
	runWorkspaceTestGit(t, dir, "config", "user.email", "test@example.com")
	runWorkspaceTestGit(t, dir, "config", "user.name", "Test")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("base\n"), 0o644))
	runWorkspaceTestGit(t, dir, "add", "tracked.txt")
	runWorkspaceTestGit(t, dir, "commit", "-m", "base")
}

// touchGitFile bumps a git metadata file's mtime far enough that
// coarse-grained filesystems still observe a change.
func touchGitFile(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	require.NoError(t, err)
	future := info.ModTime().Add(2 * time.Second)
	require.NoError(t, os.Chtimes(path, future, future))
}

func TestWorktreeGitFingerprintTracksHeadIndexAndRefs(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	dir := t.TempDir()
	initFingerprintRepo(t, dir)

	base, err := WorktreeGitFingerprint(dir)
	require.NoError(err)
	again, err := WorktreeGitFingerprint(dir)
	require.NoError(err)
	assert.Equal(base, again, "fingerprint must be stable when nothing changed")

	// A worktree-only edit leaves every git metadata file untouched.
	require.NoError(os.WriteFile(filepath.Join(dir, "scratch.txt"), []byte("scratch\n"), 0o644))
	afterEdit, err := WorktreeGitFingerprint(dir)
	require.NoError(err)
	assert.Equal(base, afterEdit)

	touchGitFile(t, filepath.Join(dir, ".git", "index"))
	afterIndex, err := WorktreeGitFingerprint(dir)
	require.NoError(err)
	assert.NotEqual(afterEdit, afterIndex, "index change must move the fingerprint")

	touchGitFile(t, filepath.Join(dir, ".git", "HEAD"))
	afterHead, err := WorktreeGitFingerprint(dir)
	require.NoError(err)
	assert.NotEqual(afterIndex, afterHead, "HEAD change must move the fingerprint")

	runWorkspaceTestGit(t, dir, "branch", "topic")
	afterRef, err := WorktreeGitFingerprint(dir)
	require.NoError(err)
	assert.NotEqual(afterHead, afterRef, "new loose ref must move the fingerprint")
}

func TestWorktreeGitFingerprintFollowsLinkedWorktreeCommonDir(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	primary := filepath.Join(t.TempDir(), "primary")
	require.NoError(os.MkdirAll(primary, 0o755))
	initFingerprintRepo(t, primary)
	linked := filepath.Join(t.TempDir(), "linked")
	runWorkspaceTestGit(t, primary, "worktree", "add", "-b", "linked", linked)

	base, err := WorktreeGitFingerprint(linked)
	require.NoError(err)

	// A ref written in the shared common directory must be visible from
	// the linked worktree, whose own git directory holds no refs.
	runWorkspaceTestGit(t, primary, "branch", "shared-topic")
	afterCommonRef, err := WorktreeGitFingerprint(linked)
	require.NoError(err)
	assert.NotEqual(base, afterCommonRef)

	// The linked worktree's private HEAD and config.worktree still count.
	gitDir, _, err := resolveWorktreeGitDirs(linked)
	require.NoError(err)
	touchGitFile(t, filepath.Join(gitDir, "HEAD"))
	afterLinkedHead, err := WorktreeGitFingerprint(linked)
	require.NoError(err)
	assert.NotEqual(afterCommonRef, afterLinkedHead)

	require.NoError(os.WriteFile(filepath.Join(gitDir, "config.worktree"), []byte("[core]\n\tbare = false\n"), 0o644))
	afterWorktreeConfig, err := WorktreeGitFingerprint(linked)
	require.NoError(err)
	assert.NotEqual(afterLinkedHead, afterWorktreeConfig, "config.worktree must move the fingerprint")
}
