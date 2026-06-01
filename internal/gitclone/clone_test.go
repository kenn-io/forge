//go:build integration

package gitclone

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gitcmd "go.kenn.io/kit/git/cmd"
)

// setupTestRepo creates a bare "remote" repo with one commit and returns
// both the remote path and the working clone path (for follow-up pushes).
func setupTestRepo(t *testing.T) (remote, work string) {
	t.Helper()
	dir := t.TempDir()
	remote = filepath.Join(dir, "remote.git")
	run(t, dir, "git", "init", "--bare", "--initial-branch=main", remote)

	work = filepath.Join(dir, "work")
	run(t, dir, "git", "clone", remote, work)
	run(t, work, "git", "config", "user.email", "test@test.com")
	run(t, work, "git", "config", "user.name", "Test")
	require.NoError(t, os.WriteFile(filepath.Join(work, "hello.go"), []byte("package main\n"), 0o644))
	run(t, work, "git", "add", ".")
	run(t, work, "git", "commit", "-m", "initial")
	run(t, work, "git", "push", "origin", "main")
	return remote, work
}

// commitAndPush creates a new commit on main in the given working clone
// and pushes it to origin. Returns the new commit SHA.
func commitAndPush(t *testing.T, work, file, content, msg string) string {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(work, file), []byte(content), 0o644))
	run(t, work, "git", "add", ".")
	run(t, work, "git", "commit", "-m", msg)
	run(t, work, "git", "push", "origin", "main")
	out, err := gitcmd.New().Output(t.Context(), work, "rev-parse", "HEAD")
	require.NoError(t, err)
	return strings.TrimSpace(string(out))
}

func run(t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	require.Equal(t, "git", name)
	out, stderr, err := gitcmd.New().Run(t.Context(), dir, nil, args...)
	require.NoError(t, err, "command %s %v failed: %s%s", name, args, out, stderr)
}

func TestEnsureClone(t *testing.T) {
	remote, _ := setupTestRepo(t)
	clonesDir := t.TempDir()
	mgr := New(clonesDir, nil)

	ctx := t.Context()
	err := mgr.EnsureClone(ctx, "github.com", "testowner", "testrepo", remote)
	require.NoError(t, err)

	clonePath := filepath.Join(
		clonesDir, "github.com", "testowner", "testrepo.git")
	assert.DirExists(t, clonePath)

	// Second call should be a no-op fetch, not re-clone.
	err = mgr.EnsureClone(ctx, "github.com", "testowner", "testrepo", remote)
	require.NoError(t, err)
}

// TestEnsureCloneShortCircuitsCanceledContext verifies that a caller
// with an already-canceled context does not start any clone work. The
// pre-check exists so a canceled caller cannot trigger background
// fetches that outlive the request it abandoned.
func TestEnsureCloneShortCircuitsCanceledContext(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	remote, _ := setupTestRepo(t)
	clonesDir := t.TempDir()
	mgr := New(clonesDir, nil)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	err := mgr.EnsureClone(ctx, "github.com", "testowner", "testrepo", remote)
	require.ErrorIs(err, context.Canceled)

	clonePath := filepath.Join(
		clonesDir, "github.com", "testowner", "testrepo.git")
	_, statErr := os.Stat(clonePath)
	assert.True(os.IsNotExist(statErr),
		"no clone directory should be created when ctx is already canceled")
}

// TestEnsureCloneSweepsPartialClone verifies that a previously aborted
// clone attempt — manifesting as a non-empty directory at the clone
// path that lacks the HEAD file — is cleaned out before the retry runs
// git clone --bare. Without the sweep, git refuses to write into the
// non-empty destination and every retry would fail with "destination
// path already exists and is not an empty directory."
func TestEnsureCloneSweepsPartialClone(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	remote, _ := setupTestRepo(t)
	clonesDir := t.TempDir()
	mgr := New(clonesDir, nil)

	// Simulate a partial clone left behind by a failed attempt: the
	// target directory exists with stray files but no HEAD.
	clonePath := filepath.Join(
		clonesDir, "github.com", "testowner", "testrepo.git")
	require.NoError(os.MkdirAll(clonePath, 0o755))
	require.NoError(os.WriteFile(
		filepath.Join(clonePath, "stray"), []byte("junk"), 0o644))

	require.NoError(mgr.EnsureClone(
		t.Context(), "github.com", "testowner", "testrepo", remote))

	// Verify the partial state was cleaned out and replaced with a
	// real bare clone.
	_, err := os.Stat(filepath.Join(clonePath, "HEAD"))
	require.NoError(err, "real bare clone should exist after sweep")
	_, err = os.Stat(filepath.Join(clonePath, "stray"))
	assert.True(os.IsNotExist(err), "stray file from partial clone should be gone")
}

// TestEnsureCloneInstallsBothRefspecs verifies that a fresh clone gets both
// the remote-tracking and pull refspecs configured. Without the remote-
// tracking refspec, git fetch never updates origin/* and branch tips
// drift stale in the bare clone.
func TestEnsureCloneInstallsBothRefspecs(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	remote, _ := setupTestRepo(t)
	clonesDir := t.TempDir()
	mgr := New(clonesDir, nil)

	require.NoError(mgr.EnsureClone(
		t.Context(), "github.com", "testowner", "testrepo", remote))

	clonePath, err := mgr.ClonePath("github.com", "testowner", "testrepo")
	require.NoError(err)
	refspecs := getFetchRefspecs(t, clonePath)
	assert.Contains(refspecs, remoteTrackingRefspec)
	assert.Contains(refspecs, pullRefspec)
	assert.NotContains(refspecs, legacyBranchRefspec)
}

// TestEnsureCloneFetchesNewBranchCommits is the regression test for the bug
// where a merged PR's merge commit was never fetched into the bare clone
// because git clone --bare sets no default fetch refspec.
func TestEnsureCloneFetchesNewBranchCommits(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	remote, work := setupTestRepo(t)
	clonesDir := t.TempDir()
	mgr := New(clonesDir, nil)

	ctx := t.Context()
	require.NoError(mgr.EnsureClone(
		ctx, "github.com", "testowner", "testrepo", remote))

	// Push a new commit to the remote after the initial clone.
	newSHA := commitAndPush(t, work, "second.go", "package main\n", "second")

	// Re-run EnsureClone and verify the new commit is now reachable.
	require.NoError(mgr.EnsureClone(
		ctx, "github.com", "testowner", "testrepo", remote))

	got, err := mgr.RevParse(ctx, "github.com", "testowner", "testrepo", newSHA)
	require.NoError(err)
	assert.Equal(newSHA, got)
}

// TestEnsureCloneMigratesBrokenClone simulates a clone created by the
// previous version of cloneBare (only pull refspec, no remote-tracking
// refspec) and verifies ensureRefspecs migrates it so branch fetches
// work again.
func TestEnsureCloneMigratesBrokenClone(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	remote, work := setupTestRepo(t)
	clonesDir := t.TempDir()
	mgr := New(clonesDir, nil)

	ctx := t.Context()
	require.NoError(mgr.EnsureClone(
		ctx, "github.com", "testowner", "testrepo", remote))

	// Simulate a pre-fix clone: unset all fetch refspecs, then add only
	// the pull refspec back. This matches the state created by the old
	// cloneBare which never installed a branch refspec.
	clonePath, err := mgr.ClonePath("github.com", "testowner", "testrepo")
	require.NoError(err)
	run(t, clonePath, "git", "config", "--unset-all", "remote.origin.fetch")
	run(t, clonePath, "git", "config", "--add",
		"remote.origin.fetch", "+refs/pull/*/head:refs/pull/*/head")
	refspecs := getFetchRefspecs(t, clonePath)
	require.NotContains(refspecs, remoteTrackingRefspec)
	require.Contains(refspecs, pullRefspec)

	// Push a new commit that would be invisible without the remote-tracking
	// refspec.
	newSHA := commitAndPush(t, work, "third.go", "package main\n", "third")

	// Next EnsureClone should re-add the remote-tracking refspec and fetch
	// the commit.
	require.NoError(mgr.EnsureClone(
		ctx, "github.com", "testowner", "testrepo", remote))

	refspecs = getFetchRefspecs(t, clonePath)
	assert.Contains(refspecs, remoteTrackingRefspec)
	assert.Contains(refspecs, pullRefspec)
	assert.NotContains(refspecs, legacyBranchRefspec)

	got, err := mgr.RevParse(ctx, "github.com", "testowner", "testrepo", newSHA)
	require.NoError(err)
	assert.Equal(newSHA, got)
}

// TestEnsureCloneRemovesLegacyBranchRefspec verifies that legacy clones stop
// fetching origin branches into refs/heads/*, which would collide with a
// workspace checking out the PR branch name locally.
func TestEnsureCloneRemovesLegacyBranchRefspec(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	remote, _ := setupTestRepo(t)
	clonesDir := t.TempDir()
	mgr := New(clonesDir, nil)

	ctx := t.Context()
	require.NoError(mgr.EnsureClone(
		ctx, "github.com", "testowner", "testrepo", remote))

	clonePath, err := mgr.ClonePath("github.com", "testowner", "testrepo")
	require.NoError(err)
	run(t, clonePath, "git", "config", "--add",
		"remote.origin.fetch", legacyBranchRefspec)
	refspecs := getFetchRefspecs(t, clonePath)
	require.Contains(refspecs, legacyBranchRefspec)

	require.NoError(mgr.EnsureClone(
		ctx, "github.com", "testowner", "testrepo", remote))

	refspecs = getFetchRefspecs(t, clonePath)
	assert.Contains(refspecs, remoteTrackingRefspec)
	assert.Contains(refspecs, pullRefspec)
	assert.NotContains(refspecs, legacyBranchRefspec)
}

// TestEnsureCloneMigratesCloneWithNoRefspec covers a clone that has no
// fetch refspec at all (the state left by a vanilla `git clone --bare`
// before any middleman-specific refspec was added). In that case
// `git config --get-all remote.origin.fetch` exits 1, which must not
// short-circuit ensureRefspecs.
func TestEnsureCloneMigratesCloneWithNoRefspec(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	remote, work := setupTestRepo(t)
	clonesDir := t.TempDir()
	mgr := New(clonesDir, nil)

	ctx := t.Context()
	require.NoError(mgr.EnsureClone(
		ctx, "github.com", "testowner", "testrepo", remote))

	// Remove every fetch refspec so the key is entirely unset, matching
	// the state of a clone that was created by an older code path which
	// did not install any refspec.
	clonePath, err := mgr.ClonePath("github.com", "testowner", "testrepo")
	require.NoError(err)
	run(t, clonePath, "git", "config", "--unset-all", "remote.origin.fetch")
	refspecs := getFetchRefspecs(t, clonePath)
	require.Empty(refspecs)

	// Push a new commit that would be invisible without the remote-tracking
	// refspec.
	newSHA := commitAndPush(t, work, "fourth.go", "package main\n", "fourth")

	// Next EnsureClone should install both refspecs and fetch the commit.
	require.NoError(mgr.EnsureClone(
		ctx, "github.com", "testowner", "testrepo", remote))

	refspecs = getFetchRefspecs(t, clonePath)
	assert.Contains(refspecs, remoteTrackingRefspec)
	assert.Contains(refspecs, pullRefspec)
	assert.NotContains(refspecs, legacyBranchRefspec)

	got, err := mgr.RevParse(ctx, "github.com", "testowner", "testrepo", newSHA)
	require.NoError(err)
	assert.Equal(newSHA, got)
}

// TestEnsureCloneRestoresOriginHead verifies that EnsureClone leaves the
// remote default-branch symref available as refs/remotes/origin/HEAD.
// Issue workspaces start from origin/HEAD, so older clones that lack that
// symref would otherwise fail to create a worktree.
func TestEnsureCloneRestoresOriginHead(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	remote, _ := setupTestRepo(t)
	clonesDir := t.TempDir()
	mgr := New(clonesDir, nil)

	ctx := t.Context()
	require.NoError(mgr.EnsureClone(
		ctx, "github.com", "testowner", "testrepo", remote))

	clonePath, err := mgr.ClonePath("github.com", "testowner", "testrepo")
	require.NoError(err)
	run(t, clonePath, "git", "symbolic-ref", "--delete", "refs/remotes/origin/HEAD")
	_, err = gitcmd.New().Output(t.Context(), clonePath, "symbolic-ref", "refs/remotes/origin/HEAD")
	require.Error(err)

	require.NoError(mgr.EnsureClone(
		ctx, "github.com", "testowner", "testrepo", remote))

	headRef := gitSymbolicRef(
		t, clonePath, "refs/remotes/origin/HEAD",
	)
	assert.Equal("refs/remotes/origin/main", headRef)
}

func TestEnsureCloneRepairsStaleOriginHead(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	remote, _ := setupTestRepo(t)
	clonesDir := t.TempDir()
	mgr := New(clonesDir, nil)

	ctx := t.Context()
	require.NoError(mgr.EnsureClone(
		ctx, "github.com", "testowner", "testrepo", remote))

	clonePath, err := mgr.ClonePath("github.com", "testowner", "testrepo")
	require.NoError(err)
	run(t, clonePath, "git", "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/master")
	assert.Equal("refs/remotes/origin/master", gitSymbolicRef(
		t, clonePath, "refs/remotes/origin/HEAD",
	))

	require.NoError(mgr.EnsureClone(
		ctx, "github.com", "testowner", "testrepo", remote))

	headRef := gitSymbolicRef(
		t, clonePath, "refs/remotes/origin/HEAD",
	)
	assert.Equal("refs/remotes/origin/main", headRef)
}

func TestEnsureCloneToleratesUnresolvedRemoteHead(t *testing.T) {
	require := require.New(t)

	remote, _ := setupTestRepo(t)
	clonesDir := t.TempDir()
	mgr := New(clonesDir, nil)

	ctx := t.Context()
	require.NoError(mgr.EnsureClone(
		ctx, "github.com", "testowner", "testrepo", remote))

	run(t, remote, "git", "symbolic-ref", "HEAD", "refs/heads/missing")

	require.NoError(mgr.EnsureClone(
		ctx, "github.com", "testowner", "testrepo", remote))
}

// getFetchRefspecs returns the current fetch refspecs configured for the
// "origin" remote in a bare clone. Returns an empty slice when the key
// is unset; `git config --get-all` signals that with exit code 1.
func getFetchRefspecs(t *testing.T, clonePath string) []string {
	t.Helper()
	out, err := gitcmd.New().Output(t.Context(), clonePath,
		"config", "--get-all", "remote.origin.fetch")
	if err != nil {
		if gitcmd.IsExitCode(err, 1) {
			return nil // key unset
		}
		require.NoError(t, err)
	}
	var result []string
	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			result = append(result, line)
		}
	}
	return result
}

func gitSymbolicRef(t *testing.T, dir, ref string) string {
	t.Helper()
	out, err := gitcmd.New().Output(t.Context(), dir, "symbolic-ref", ref)
	require.NoError(t, err)
	return strings.TrimSpace(string(out))
}

func TestEnsureCloneIgnoresInheritedGitEnv(t *testing.T) {
	remote, _ := setupTestRepo(t)
	clonesDir := t.TempDir()
	mgr := New(clonesDir, nil)

	// Worktree/index vars
	t.Setenv("GIT_WORK_TREE", t.TempDir())
	t.Setenv("GIT_DIR", "")
	t.Setenv("GIT_INDEX_FILE", filepath.Join(t.TempDir(), "index"))
	t.Setenv("GIT_OBJECT_DIRECTORY", t.TempDir())
	t.Setenv("GIT_ALTERNATE_OBJECT_DIRECTORIES", t.TempDir())
	// Config injection vars
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "http.extraHeader")
	t.Setenv("GIT_CONFIG_VALUE_0", "X-Bad: injected")
	t.Setenv("GIT_CONFIG_PARAMETERS", "'http.extraHeader=X-Bad: injected'")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	// Credential/interactive helpers
	t.Setenv("GIT_ASKPASS", "/bin/false")
	t.Setenv("GIT_SSH_COMMAND", "/bin/false")
	t.Setenv("SSH_ASKPASS", "/bin/false")

	err := mgr.EnsureClone(t.Context(), "github.com", "testowner", "testrepo", remote)
	require.NoError(t, err)
}

func TestMergeBase(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	remote, _ := setupTestRepo(t)
	clonesDir := t.TempDir()
	mgr := New(clonesDir, nil)

	ctx := t.Context()
	require.NoError(mgr.EnsureClone(
		ctx, "github.com", "testowner", "testrepo", remote))

	// Get the HEAD SHA.
	clonePath, err := mgr.ClonePath("github.com", "testowner", "testrepo")
	require.NoError(err)
	out, err := gitcmd.New().Output(t.Context(), clonePath, "rev-parse", "HEAD")
	require.NoError(err)
	headSHA := strings.TrimSpace(string(out))

	// Merge base of HEAD with itself is HEAD.
	mb, err := mgr.MergeBase(
		ctx, "github.com", "testowner", "testrepo", headSHA, headSHA)
	require.NoError(err)
	assert.Equal(headSHA, mb)
}
