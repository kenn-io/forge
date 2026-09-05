package workspace

import (
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
	"go.kenn.io/forge/internal/procutil"
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

func TestGitdirReaderSymbolicHeadWithUpstream(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	fixture := newPushedHeadFixture(t)
	reader := newGitdirRemoteHeadReader()
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
}

func TestGitdirReaderDetachedHead(t *testing.T) {
	fixture := newPushedHeadFixture(t)
	runWorkspaceTestGit(t, fixture.clone, "checkout", "--detach")
	reader := newGitdirRemoteHeadReader()

	branch, err := reader.BranchName(t.Context(), fixture.clone)
	require.NoError(t, err)
	assert.Empty(t, branch, "detached HEAD has no current branch")
}

func TestGitdirReaderBranchWithoutUpstream(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	fixture := newPushedHeadFixture(t)
	runWorkspaceTestGit(t, fixture.clone, "checkout", "-b", "scratch")
	reader := newGitdirRemoteHeadReader()
	ctx := t.Context()

	upstream, err := reader.UpstreamState(ctx, fixture.clone, "scratch")
	require.NoError(err)
	assert.Equal(upstreamState{}, upstream)

	sha, ref, ok, err := reader.RemoteTrackingSHA(ctx, fixture.clone, "origin", "scratch")
	require.NoError(err)
	assert.False(ok)
	assert.Empty(sha)
	assert.Equal("refs/remotes/origin/scratch", ref)
}

func TestGitdirReaderPackedAndLooseRefs(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	fixture := newPushedHeadFixture(t)
	reader := newGitdirRemoteHeadReader()
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
}

func TestGitdirReaderLinkedWorktree(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	fixture := newPushedHeadFixture(t)
	worktree := filepath.Join(filepath.Dir(fixture.clone), "linked")
	runWorkspaceTestGit(t, fixture.clone, "worktree", "add", "-b", "topic", worktree, "main")
	runWorkspaceTestGit(t, worktree, "push", "-u", "origin", "topic")
	reader := newGitdirRemoteHeadReader()
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
}

func TestGitdirReaderSkipsConfigIncludes(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	fixture := newPushedHeadFixture(t)
	included := filepath.Join(filepath.Dir(fixture.clone), "included.gitconfig")
	require.NoError(os.WriteFile(included, []byte("[branch \"feature\"]\n\tremote = origin\n\tmerge = refs/heads/feature\n"), 0o600))
	runWorkspaceTestGit(t, fixture.clone, "config", "include.path", included)
	reader := newGitdirRemoteHeadReader()
	ctx := t.Context()

	// go-git does not expand includes, so the effective config is unknowable
	// in process; the observer treats the workspace as unobservable rather
	// than guessing from the partial file or spawning git.
	branch, err := reader.BranchName(ctx, fixture.clone)
	require.NoError(err)
	assert.Empty(branch)
	upstream, err := reader.UpstreamState(ctx, fixture.clone, "feature")
	require.NoError(err)
	assert.Equal(upstreamState{}, upstream)
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

// TestGitdirReaderDoesNotLeakDescriptorsOnLinkedWorktree guards the storage
// construction in openWorktreeRepository: go-git's EnableDotGitCommonDir
// option opens the linked worktree's commondir file without closing it, and
// the observer opens every workspace several times a minute, so a leak there
// exhausts the daemon's descriptors and keeps worktrees undeletable on
// Windows.
func TestGitdirReaderDoesNotLeakDescriptorsOnLinkedWorktree(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("descriptor table is read through /dev/fd")
	}
	assert := assert.New(t)
	require := require.New(t)
	fixture := newPushedHeadFixture(t)
	worktree := filepath.Join(filepath.Dir(fixture.clone), "linked")
	runWorkspaceTestGit(t, fixture.clone, "worktree", "add", "-b", "topic", worktree, "main")
	runWorkspaceTestGit(t, worktree, "push", "-u", "origin", "topic")
	reader := newGitdirRemoteHeadReader()
	ctx := t.Context()

	openDescriptors := func() int {
		entries, err := os.ReadDir("/dev/fd")
		require.NoError(err)
		return len(entries)
	}
	read := func() {
		_, err := reader.BranchName(ctx, worktree)
		require.NoError(err)
		_, err = reader.UpstreamState(ctx, worktree, "topic")
		require.NoError(err)
		_, _, _, err = reader.RemoteTrackingSHA(ctx, worktree, "origin", "topic")
		require.NoError(err)
	}
	read()
	before := openDescriptors()
	for range 20 {
		read()
	}
	assert.Equal(before, openDescriptors(), "reading a linked worktree must not leave descriptors open")
}

func TestGitdirReaderOverlaysWorktreeConfig(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	fixture := newPushedHeadFixture(t)
	worktree := filepath.Join(filepath.Dir(fixture.clone), "linked")
	runWorkspaceTestGit(t, fixture.clone, "worktree", "add", "-b", "topic", worktree, "main")
	// Shared config says topic tracks origin/topic; the worktree's own
	// config.worktree overrides that to origin/feature, which is what git
	// reports once extensions.worktreeConfig is on. Git accepts any nonzero
	// integer as true for that extension.
	runWorkspaceTestGit(t, fixture.clone, "config", "branch.topic.remote", "origin")
	runWorkspaceTestGit(t, fixture.clone, "config", "branch.topic.merge", "refs/heads/topic")
	runWorkspaceTestGit(t, fixture.clone, "config", "extensions.worktreeConfig", "2")
	runWorkspaceTestGit(t, worktree, "config", "--worktree", "branch.topic.merge", "refs/heads/feature")
	reader := newGitdirRemoteHeadReader()
	ctx := t.Context()

	upstream, err := reader.UpstreamState(ctx, worktree, "topic")
	require.NoError(err)
	assert.Equal(upstreamState{
		branchName:  "feature",
		remoteName:  "origin",
		remoteURL:   fixture.remote,
		hasTracking: true,
	}, upstream, "config.worktree must override the shared config")

	shared, err := reader.UpstreamState(ctx, fixture.clone, "topic")
	require.NoError(err)
	assert.Equal("topic", shared.branchName, "the main worktree keeps the shared value")
}

// TestGitdirReaderRepairsUpstreamWithOneLimiterSlot proves upstream
// repair holds one subprocess slot per git command rather than nesting an
// outer acquisition around guarded commands, which stalls until the git
// timeout whenever the limiter is at capacity.
func TestGitdirReaderRepairsUpstreamWithOneLimiterSlot(t *testing.T) {
	require := require.New(t)
	fixture := newPushedHeadFixture(t)
	runWorkspaceTestGit(t, fixture.clone, "checkout", "-b", "scratch")
	restore := procutil.SetDefaultLimiterForTest(
		procutil.NewLimiterWithAcquireTimeout(1, 200*time.Millisecond),
	)
	defer restore()

	err := newGitdirRemoteHeadReader().SetBranchUpstream(
		t.Context(), fixture.clone, "scratch", "origin", "refs/heads/feature",
	)
	require.NoError(err)
	restore()
	merge := strings.TrimSpace(string(runWorkspaceTestGit(t, fixture.clone, "config", "--get", "branch.scratch.merge")))
	assert.Equal(t, "refs/heads/feature", merge)
}

func TestGitdirReaderSeesConfigChangesAfterCaching(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	fixture := newPushedHeadFixture(t)
	reader := newGitdirRemoteHeadReader()
	ctx := t.Context()

	first, err := reader.UpstreamState(ctx, fixture.clone, "feature")
	require.NoError(err)
	assert.Equal("feature", first.branchName)

	// Rewrite the upstream through git; the cached parse must be replaced
	// because the config file's stamp changed.
	runWorkspaceTestGit(t, fixture.clone, "config", "branch.feature.merge", "refs/heads/main")
	second, err := reader.UpstreamState(ctx, fixture.clone, "feature")
	require.NoError(err)
	assert.Equal("main", second.branchName)

	// A deleted worktree must not be answered from the cached handle.
	require.NoError(os.RemoveAll(fixture.clone))
	_, err = reader.BranchName(ctx, fixture.clone)
	assert.Error(err)
}
