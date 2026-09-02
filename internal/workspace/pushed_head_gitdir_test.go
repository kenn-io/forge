package workspace

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/internal/db"
)

// pushedHeadFixture is a bare remote with one clone whose "feature" branch
// tracks origin/feature. Fixture commands run through the kit git runner,
// which strips inherited GIT_* variables and nulls global/system config, and
// every path lives under t.TempDir outside any real repository.
type pushedHeadFixture struct {
	remote string
	clone  string
}

func newPushedHeadFixture(t *testing.T) pushedHeadFixture {
	t.Helper()
	dir := t.TempDir()
	remote := filepath.Join(dir, "remote.git")
	clone := filepath.Join(dir, "clone")
	runWorkspaceTestGit(t, dir, "init", "--bare", "--initial-branch=main", remote)
	runWorkspaceTestGit(t, dir, "clone", remote, clone)
	runWorkspaceTestGit(t, clone, "config", "user.email", "test@example.com")
	runWorkspaceTestGit(t, clone, "config", "user.name", "Test")
	require.NoError(t, os.WriteFile(filepath.Join(clone, "base.txt"), []byte("base\n"), 0o644))
	runWorkspaceTestGit(t, clone, "add", ".")
	runWorkspaceTestGit(t, clone, "commit", "-m", "base commit")
	runWorkspaceTestGit(t, clone, "push", "origin", "main")
	runWorkspaceTestGit(t, clone, "checkout", "-b", "feature")
	require.NoError(t, os.WriteFile(filepath.Join(clone, "feature.txt"), []byte("feature\n"), 0o644))
	runWorkspaceTestGit(t, clone, "add", ".")
	runWorkspaceTestGit(t, clone, "commit", "-m", "feature commit")
	runWorkspaceTestGit(t, clone, "push", "-u", "origin", "feature")
	return pushedHeadFixture{remote: remote, clone: clone}
}

// pushFeatureCommit adds a commit to the feature branch and pushes it so
// origin/feature moves; it returns the new remote-tracking SHA.
func (f pushedHeadFixture) pushFeatureCommit(t *testing.T, name string) string {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(f.clone, name), []byte(name+"\n"), 0o644))
	runWorkspaceTestGit(t, f.clone, "add", ".")
	runWorkspaceTestGit(t, f.clone, "commit", "-m", name)
	runWorkspaceTestGit(t, f.clone, "push", "origin", "feature")
	return f.revParse(t, "refs/remotes/origin/feature")
}

func (f pushedHeadFixture) revParse(t *testing.T, ref string) string {
	t.Helper()
	return strings.TrimSpace(string(runWorkspaceTestGit(t, f.clone, "rev-parse", "--verify", ref)))
}

// countingRemoteHeadReader records fallback use so tests can prove the direct
// reader answered from the git directory alone.
type countingRemoteHeadReader struct {
	inner remoteHeadGitReader
	calls int
}

func (c *countingRemoteHeadReader) BranchName(ctx context.Context, dir string) (string, error) {
	c.calls++
	return c.inner.BranchName(ctx, dir)
}

func (c *countingRemoteHeadReader) UpstreamState(ctx context.Context, dir, branch string) (upstreamState, error) {
	c.calls++
	return c.inner.UpstreamState(ctx, dir, branch)
}

func (c *countingRemoteHeadReader) RemoteTrackingSHA(ctx context.Context, dir, remote, branch string) (string, string, bool, error) {
	c.calls++
	return c.inner.RemoteTrackingSHA(ctx, dir, remote, branch)
}

func (c *countingRemoteHeadReader) SetBranchUpstream(ctx context.Context, dir, branch, remote, mergeRef string) error {
	c.calls++
	return c.inner.SetBranchUpstream(ctx, dir, branch, remote, mergeRef)
}

func newGitdirReaderForTest() (*gitdirRemoteHeadReader, *countingRemoteHeadReader) {
	fallback := &countingRemoteHeadReader{inner: gitRemoteHeadReader{}}
	return &gitdirRemoteHeadReader{fallback: fallback}, fallback
}

func TestGitdirReaderSymbolicHeadWithUpstream(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	fixture := newPushedHeadFixture(t)
	reader, fallback := newGitdirReaderForTest()
	ctx := t.Context()

	branch, err := reader.BranchName(ctx, fixture.clone)
	require.NoError(err)
	assert.Equal("feature", branch)

	upstream, err := reader.UpstreamState(ctx, fixture.clone, "feature")
	require.NoError(err)
	assert.Equal(upstreamState{
		branchName:  "feature",
		remoteName:  "origin",
		remoteURL:   fixture.remote,
		hasTracking: true,
	}, upstream)

	sha, ref, ok, err := reader.RemoteTrackingSHA(ctx, fixture.clone, "origin", "feature")
	require.NoError(err)
	assert.True(ok)
	assert.Equal("refs/remotes/origin/feature", ref)
	assert.Equal(fixture.revParse(t, "refs/remotes/origin/feature"), sha)
	assert.Equal(0, fallback.calls, "a plain clone must be answered without git")
}

func TestGitdirReaderDetachedHead(t *testing.T) {
	fixture := newPushedHeadFixture(t)
	runWorkspaceTestGit(t, fixture.clone, "checkout", "--detach")
	reader, fallback := newGitdirReaderForTest()

	branch, err := reader.BranchName(t.Context(), fixture.clone)
	require.NoError(t, err)
	assert.Empty(t, branch, "detached HEAD has no current branch")
	assert.Equal(t, 0, fallback.calls)
}

func TestGitdirReaderBranchWithoutUpstream(t *testing.T) {
	fixture := newPushedHeadFixture(t)
	runWorkspaceTestGit(t, fixture.clone, "checkout", "-b", "scratch")
	reader, fallback := newGitdirReaderForTest()
	ctx := t.Context()

	upstream, err := reader.UpstreamState(ctx, fixture.clone, "scratch")
	require.NoError(t, err)
	assert.Equal(t, upstreamState{}, upstream)

	sha, ref, ok, err := reader.RemoteTrackingSHA(ctx, fixture.clone, "origin", "scratch")
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Empty(t, sha)
	assert.Equal(t, "refs/remotes/origin/scratch", ref)
	assert.Equal(t, 0, fallback.calls)
}

func TestGitdirReaderPackedAndLooseRefs(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	fixture := newPushedHeadFixture(t)
	reader, fallback := newGitdirReaderForTest()
	ctx := t.Context()
	gitDir := filepath.Join(fixture.clone, ".git")
	looseRef := filepath.Join(gitDir, "refs", "remotes", "origin", "feature")

	// Force the tracking ref into packed-refs only.
	runWorkspaceTestGit(t, fixture.clone, "pack-refs", "--all")
	_, statErr := os.Stat(looseRef)
	require.ErrorIs(statErr, os.ErrNotExist, "pack-refs must leave no loose ref")
	packed := fixture.revParse(t, "refs/remotes/origin/feature")
	sha, _, ok, err := reader.RemoteTrackingSHA(ctx, fixture.clone, "origin", "feature")
	require.NoError(err)
	assert.True(ok)
	assert.Equal(packed, sha)

	// A push updates the loose ref, which must win over the stale packed entry.
	pushed := fixture.pushFeatureCommit(t, "second.txt")
	_, statErr = os.Stat(looseRef)
	require.NoError(statErr, "push must write a loose tracking ref")
	assert.NotEqual(packed, pushed)
	sha, _, ok, err = reader.RemoteTrackingSHA(ctx, fixture.clone, "origin", "feature")
	require.NoError(err)
	assert.True(ok)
	assert.Equal(pushed, sha)
	assert.Equal(0, fallback.calls)
}

func TestGitdirReaderLinkedWorktree(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	fixture := newPushedHeadFixture(t)
	worktree := filepath.Join(filepath.Dir(fixture.clone), "linked")
	runWorkspaceTestGit(t, fixture.clone, "worktree", "add", "-b", "topic", worktree, "main")
	runWorkspaceTestGit(t, worktree, "push", "-u", "origin", "topic")
	reader, fallback := newGitdirReaderForTest()
	ctx := t.Context()

	branch, err := reader.BranchName(ctx, worktree)
	require.NoError(err)
	assert.Equal("topic", branch)

	upstream, err := reader.UpstreamState(ctx, worktree, "topic")
	require.NoError(err)
	assert.Equal(upstreamState{
		branchName:  "topic",
		remoteName:  "origin",
		remoteURL:   fixture.remote,
		hasTracking: true,
	}, upstream)

	sha, ref, ok, err := reader.RemoteTrackingSHA(ctx, worktree, "origin", "topic")
	require.NoError(err)
	assert.True(ok)
	assert.Equal("refs/remotes/origin/topic", ref)
	assert.Equal(fixture.revParse(t, "refs/remotes/origin/topic"), sha)
	assert.Equal(0, fallback.calls)
}

func TestGitdirReaderFallsBackForConfigIncludes(t *testing.T) {
	fixture := newPushedHeadFixture(t)
	included := filepath.Join(filepath.Dir(fixture.clone), "included.gitconfig")
	require.NoError(t, os.WriteFile(included, []byte("[branch \"feature\"]\n\tremote = origin\n\tmerge = refs/heads/feature\n"), 0o600))
	runWorkspaceTestGit(t, fixture.clone, "config", "--unset", "branch.feature.remote")
	runWorkspaceTestGit(t, fixture.clone, "config", "--unset", "branch.feature.merge")
	runWorkspaceTestGit(t, fixture.clone, "config", "include.path", included)
	reader, fallback := newGitdirReaderForTest()

	upstream, err := reader.UpstreamState(t.Context(), fixture.clone, "feature")
	require.NoError(t, err)
	assert.Equal(t, upstreamState{
		branchName:  "feature",
		remoteName:  "origin",
		remoteURL:   fixture.remote,
		hasTracking: true,
	}, upstream, "included config is only visible through git")
	assert.Equal(t, 1, fallback.calls)
}

// TestPushedHeadObserverPassSpawnsNoGitWhenUnchanged runs the real observer
// against a fixture worktree with a git shim first on PATH that logs every
// invocation, proving idle passes and pushed-head detection need no
// subprocess at all.
func TestPushedHeadObserverPassSpawnsNoGitWhenUnchanged(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PATH shim requires a POSIX shell")
	}
	assert := assert.New(t)
	require := require.New(t)
	fixture := newPushedHeadFixture(t)
	oldHead := fixture.revParse(t, "refs/remotes/origin/feature")

	d := openTestDB(t)
	repoID := seedRepo(t, d, "github.com", "acme", "widget")
	seedMRWithPlatformHead(t, d, repoID, 42, "feature", oldHead, "https://github.com/acme/widget.git")
	require.NoError(d.InsertWorkspace(t.Context(), &db.Workspace{
		ID:              "ws-pr",
		Platform:        "github",
		PlatformHost:    "github.com",
		RepoOwner:       "acme",
		RepoName:        "widget",
		ItemType:        db.WorkspaceItemTypePullRequest,
		ItemNumber:      42,
		GitHeadRef:      "feature",
		WorkspaceBranch: "feature",
		WorktreePath:    fixture.clone,
		TmuxSession:     "kenn-forge-ws-pr",
		Status:          "ready",
	}))
	observer := NewPushedHeadObserver(d)
	observer.setNowForTest(func() time.Time {
		return time.Date(2026, 5, 20, 14, 15, 0, 0, time.UTC)
	})

	realGit, err := exec.LookPath("git")
	require.NoError(err)
	shimDir := t.TempDir()
	logPath := filepath.Join(shimDir, "invocations.log")
	shim := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '" + logPath + "'\nexec '" + realGit + "' \"$@\"\n"
	require.NoError(os.WriteFile(filepath.Join(shimDir, "git"), []byte(shim), 0o700))
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	gitInvocations := func() string {
		data, readErr := os.ReadFile(logPath)
		if os.IsNotExist(readErr) {
			return ""
		}
		require.NoError(readErr)
		return string(data)
	}

	result, err := observer.RunOnce(t.Context())
	require.NoError(err)
	assert.Empty(result.HeadChanges, "first observation matches the provider head")
	assert.Empty(gitInvocations(), "first pass must not spawn git")

	result, err = observer.RunOnce(t.Context())
	require.NoError(err)
	assert.Empty(result.HeadChanges)
	assert.Empty(gitInvocations(), "unchanged pass must not spawn git")

	newHead := fixture.pushFeatureCommit(t, "second.txt")
	require.NoError(os.Remove(logPath))

	result, err = observer.RunOnce(t.Context())
	require.NoError(err)
	require.Len(result.HeadChanges, 1)
	assert.Equal(oldHead, result.HeadChanges[0].OldSHA)
	assert.Equal(newHead, result.HeadChanges[0].NewSHA)
	assert.Equal("refs/remotes/origin/feature", result.HeadChanges[0].TrackingRef)
	assert.Empty(gitInvocations(), "detecting a pushed head must not spawn git")
}
