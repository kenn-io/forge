package workspace

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/middleman/internal/db"
)

type fakeRemoteHeadReader struct {
	branchCalls      int
	upstreamCalls    int
	trackingCalls    int
	branch           string
	upstream         upstreamState
	trackingSHA      string
	trackingRef      string
	trackingOK       bool
	trackingErr      error
	setUpstreamCalls []fakeSetUpstreamCall
	setUpstreamErr   error
}

type fakeSetUpstreamCall struct {
	dir      string
	branch   string
	remote   string
	mergeRef string
}

func (r *fakeRemoteHeadReader) BranchName(context.Context, string) (string, error) {
	r.branchCalls++
	return r.branch, nil
}

func (r *fakeRemoteHeadReader) UpstreamState(context.Context, string, string) (upstreamState, error) {
	r.upstreamCalls++
	return r.upstream, nil
}

func (r *fakeRemoteHeadReader) RemoteTrackingSHA(context.Context, string, string, string) (string, string, bool, error) {
	r.trackingCalls++
	return r.trackingSHA, r.trackingRef, r.trackingOK, r.trackingErr
}

func (r *fakeRemoteHeadReader) SetBranchUpstream(_ context.Context, dir, branch, remote, mergeRef string) error {
	r.setUpstreamCalls = append(r.setUpstreamCalls, fakeSetUpstreamCall{
		dir:      dir,
		branch:   branch,
		remote:   remote,
		mergeRef: mergeRef,
	})
	return r.setUpstreamErr
}

func insertPushedHeadWorkspace(
	t *testing.T,
	d *db.DB,
	id, itemType string,
	itemNumber int,
	associatedPRNumber *int,
) {
	t.Helper()
	require.NoError(t, d.InsertWorkspace(t.Context(), &db.Workspace{
		ID:                 id,
		Platform:           "github",
		PlatformHost:       "github.com",
		RepoOwner:          "acme",
		RepoName:           "widget",
		ItemType:           itemType,
		ItemNumber:         itemNumber,
		AssociatedPRNumber: associatedPRNumber,
		GitHeadRef:         "feature/remote-head",
		WorkspaceBranch:    "feature/remote-head",
		WorktreePath:       "/tmp/worktree",
		TmuxSession:        "middleman-" + id,
		Status:             "ready",
	}))
}

func seedMRWithPlatformHead(
	t *testing.T,
	d *db.DB,
	repoID int64,
	number int,
	branch, sha, headRepoCloneURL string,
) {
	t.Helper()
	now := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	_, err := d.UpsertMergeRequest(t.Context(), &db.MergeRequest{
		RepoID:           repoID,
		PlatformID:       repoID*10000 + int64(number),
		Number:           number,
		Title:            "Refresh me",
		Author:           "author",
		State:            db.MergeRequestStateOpen,
		HeadBranch:       branch,
		PlatformHeadSHA:  sha,
		HeadRepoCloneURL: headRepoCloneURL,
		BaseBranch:       "main",
		CreatedAt:        now,
		UpdatedAt:        now,
		LastActivityAt:   now,
	})
	require.NoError(t, err)
}

func newPushedHeadObserverForTest(
	t *testing.T,
	d *db.DB,
	reader *fakeRemoteHeadReader,
) *PushedHeadObserver {
	t.Helper()
	observer := NewPushedHeadObserver(d)
	observer.SetGitReaderForTest(reader)
	observer.SetNowForTest(func() time.Time {
		return time.Date(2026, 5, 20, 14, 15, 0, 0, time.UTC)
	})
	return observer
}

func TestPushedHeadObserverFirstObservationSkipsWhenProviderHeadMatches(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	repoID := seedRepo(t, d, "github.com", "acme", "widget")
	seedMRWithPlatformHead(t, d, repoID, 42, "feature/remote-head", "2222222", "")
	insertPushedHeadWorkspace(t, d, "ws-pr", db.WorkspaceItemTypePullRequest, 42, nil)
	reader := &fakeRemoteHeadReader{
		branch: "feature/remote-head",
		upstream: upstreamState{
			hasTracking: true,
			remoteName:  "origin",
			branchName:  "feature/remote-head",
		},
		trackingSHA: "2222222",
		trackingRef: "refs/remotes/origin/feature/remote-head",
		trackingOK:  true,
	}
	observer := newPushedHeadObserverForTest(t, d, reader)

	result, err := observer.RunOnce(context.Background())
	require.NoError(err)
	assert.Empty(result.Associations)
	assert.Empty(result.HeadChanges)
	assert.Equal(1, reader.trackingCalls)
}

func TestPushedHeadObserverFirstObservationEnqueuesWhenProviderHeadDiffers(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	repoID := seedRepo(t, d, "github.com", "acme", "widget")
	seedMRWithPlatformHead(t, d, repoID, 42, "feature/remote-head", "1111111", "")
	insertPushedHeadWorkspace(t, d, "ws-pr", db.WorkspaceItemTypePullRequest, 42, nil)
	reader := &fakeRemoteHeadReader{
		branch: "feature/remote-head",
		upstream: upstreamState{
			hasTracking: true,
			remoteName:  "origin",
			branchName:  "feature/remote-head",
		},
		trackingSHA: "2222222",
		trackingRef: "refs/remotes/origin/feature/remote-head",
		trackingOK:  true,
	}
	observer := newPushedHeadObserverForTest(t, d, reader)

	result, err := observer.RunOnce(context.Background())
	require.NoError(err)
	require.Len(result.HeadChanges, 1)
	change := result.HeadChanges[0]
	assert.Equal("ws-pr", change.WorkspaceID)
	assert.Equal("github", string(change.Provider))
	assert.Equal("github.com", change.PlatformHost)
	assert.Equal("acme/widget", change.RepoPath)
	assert.Equal(42, change.Number)
	assert.Equal("1111111", change.OldSHA)
	assert.Equal("2222222", change.NewSHA)
	assert.Equal("origin", change.RemoteName)
	assert.Equal("feature/remote-head", change.BranchName)
	assert.Equal("refs/remotes/origin/feature/remote-head", change.TrackingRef)
}

func TestPushedHeadObserverRetriesObservedSHAUntilProviderHeadMatches(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	repoID := seedRepo(t, d, "github.com", "acme", "widget")
	seedMRWithPlatformHead(t, d, repoID, 42, "feature/remote-head", "1111111", "")
	insertPushedHeadWorkspace(t, d, "ws-pr", db.WorkspaceItemTypePullRequest, 42, nil)
	reader := &fakeRemoteHeadReader{
		branch:      "feature/remote-head",
		upstream:    upstreamState{hasTracking: true, remoteName: "origin", branchName: "feature/remote-head"},
		trackingSHA: "2222222",
		trackingRef: "refs/remotes/origin/feature/remote-head",
		trackingOK:  true,
	}
	now := time.Date(2026, 5, 20, 14, 15, 0, 0, time.UTC)
	observer := NewPushedHeadObserver(d)
	observer.SetGitReaderForTest(reader)
	observer.SetNowForTest(func() time.Time { return now })

	first, err := observer.RunOnce(context.Background())
	require.NoError(err)
	require.Len(first.HeadChanges, 1)
	observer.MarkRefreshEnqueued(first.HeadChanges[0], now)

	suppressed, err := observer.RunOnce(context.Background())
	require.NoError(err)
	assert.Empty(suppressed.HeadChanges)

	now = now.Add(pushedHeadRefreshRetryInterval + time.Second)
	retry, err := observer.RunOnce(context.Background())
	require.NoError(err)
	require.Len(retry.HeadChanges, 1)
	assert.Equal("1111111", retry.HeadChanges[0].OldSHA)
	assert.Equal("2222222", retry.HeadChanges[0].NewSHA)
}

func TestPushedHeadObserverDoesNotRetryAfterRefreshSucceeds(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	repoID := seedRepo(t, d, "github.com", "acme", "widget")
	seedMRWithPlatformHead(t, d, repoID, 42, "feature/remote-head", "1111111", "")
	insertPushedHeadWorkspace(t, d, "ws-pr", db.WorkspaceItemTypePullRequest, 42, nil)
	reader := &fakeRemoteHeadReader{
		branch:      "feature/remote-head",
		upstream:    upstreamState{hasTracking: true, remoteName: "origin", branchName: "feature/remote-head"},
		trackingSHA: "2222222",
		trackingRef: "refs/remotes/origin/feature/remote-head",
		trackingOK:  true,
	}
	observer := newPushedHeadObserverForTest(t, d, reader)

	first, err := observer.RunOnce(context.Background())
	require.NoError(err)
	require.Len(first.HeadChanges, 1)
	now := time.Date(2026, 5, 20, 14, 15, 0, 0, time.UTC)
	observer.MarkRefreshSucceeded(first.HeadChanges[0], now)
	seedMRWithPlatformHead(t, d, repoID, 42, "feature/remote-head", "2222222", "")

	retry, err := observer.RunOnce(context.Background())
	require.NoError(err)
	assert.Empty(retry.HeadChanges)
}

func TestPushedHeadObserverStopsRetryingAfterSuccessfulRefreshStillDiffers(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	repoID := seedRepo(t, d, "github.com", "acme", "widget")
	seedMRWithPlatformHead(t, d, repoID, 42, "feature/remote-head", "1111111", "")
	insertPushedHeadWorkspace(t, d, "ws-pr", db.WorkspaceItemTypePullRequest, 42, nil)
	reader := &fakeRemoteHeadReader{
		branch:      "feature/remote-head",
		upstream:    upstreamState{hasTracking: true, remoteName: "origin", branchName: "feature/remote-head"},
		trackingSHA: "2222222",
		trackingRef: "refs/remotes/origin/feature/remote-head",
		trackingOK:  true,
	}
	now := time.Date(2026, 5, 20, 14, 15, 0, 0, time.UTC)
	observer := NewPushedHeadObserver(d)
	observer.SetGitReaderForTest(reader)
	observer.SetNowForTest(func() time.Time { return now })

	first, err := observer.RunOnce(context.Background())
	require.NoError(err)
	require.Len(first.HeadChanges, 1)
	observer.MarkRefreshEnqueued(first.HeadChanges[0], now)
	// The enqueued provider sync completed and authoritatively reported a
	// head that still differs from the local tracking ref: the local ref
	// is stale, so another sync cannot converge.
	observer.MarkRefreshSucceeded(first.HeadChanges[0], now.Add(2*time.Second))

	now = now.Add(pushedHeadRefreshRetryInterval + time.Second)
	steady, err := observer.RunOnce(context.Background())
	require.NoError(err)
	assert.Empty(steady.HeadChanges)

	now = now.Add(pushedHeadRefreshRetryInterval + time.Second)
	later, err := observer.RunOnce(context.Background())
	require.NoError(err)
	assert.Empty(later.HeadChanges)

	// A real local push moves the tracking ref and restarts the cycle.
	reader.trackingSHA = "3333333"
	pushed, err := observer.RunOnce(context.Background())
	require.NoError(err)
	require.Len(pushed.HeadChanges, 1)
	assert.Equal("2222222", pushed.HeadChanges[0].OldSHA)
	assert.Equal("3333333", pushed.HeadChanges[0].NewSHA)
}

func TestPushedHeadObserverRetriesNewSHAWhenEnqueueWasDropped(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	repoID := seedRepo(t, d, "github.com", "acme", "widget")
	seedMRWithPlatformHead(t, d, repoID, 42, "feature/remote-head", "1111111", "")
	insertPushedHeadWorkspace(t, d, "ws-pr", db.WorkspaceItemTypePullRequest, 42, nil)
	reader := &fakeRemoteHeadReader{
		branch:      "feature/remote-head",
		upstream:    upstreamState{hasTracking: true, remoteName: "origin", branchName: "feature/remote-head"},
		trackingSHA: "2222222",
		trackingRef: "refs/remotes/origin/feature/remote-head",
		trackingOK:  true,
	}
	now := time.Date(2026, 5, 20, 14, 15, 0, 0, time.UTC)
	observer := NewPushedHeadObserver(d)
	observer.SetGitReaderForTest(reader)
	observer.SetNowForTest(func() time.Time { return now })

	// Suppressed steady state for the first SHA: refresh enqueued and
	// succeeded, provider still reports a different head.
	first, err := observer.RunOnce(context.Background())
	require.NoError(err)
	require.Len(first.HeadChanges, 1)
	observer.MarkRefreshEnqueued(first.HeadChanges[0], now)
	observer.MarkRefreshSucceeded(first.HeadChanges[0], now.Add(2*time.Second))

	// A push moves the tracking ref, but the server drops the enqueue
	// (same-key detail sync already in flight), so neither marker runs.
	reader.trackingSHA = "3333333"
	now = now.Add(time.Minute)
	moved, err := observer.RunOnce(context.Background())
	require.NoError(err)
	require.Len(moved.HeadChanges, 1)

	// The new SHA must keep retrying: the old SHA's refresh stamps do not
	// belong to it and must not satisfy the stop-retrying gate.
	now = now.Add(time.Minute)
	retry, err := observer.RunOnce(context.Background())
	require.NoError(err)
	require.Len(retry.HeadChanges, 1)
	assert.Equal("3333333", retry.HeadChanges[0].NewSHA)
}

func TestPushedHeadObserverLateSuccessForOldSHADoesNotDisturbNewCycle(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	repoID := seedRepo(t, d, "github.com", "acme", "widget")
	seedMRWithPlatformHead(t, d, repoID, 42, "feature/remote-head", "1111111", "")
	insertPushedHeadWorkspace(t, d, "ws-pr", db.WorkspaceItemTypePullRequest, 42, nil)
	reader := &fakeRemoteHeadReader{
		branch:      "feature/remote-head",
		upstream:    upstreamState{hasTracking: true, remoteName: "origin", branchName: "feature/remote-head"},
		trackingSHA: "2222222",
		trackingRef: "refs/remotes/origin/feature/remote-head",
		trackingOK:  true,
	}
	now := time.Date(2026, 5, 20, 14, 15, 0, 0, time.UTC)
	observer := NewPushedHeadObserver(d)
	observer.SetGitReaderForTest(reader)
	observer.SetNowForTest(func() time.Time { return now })

	first, err := observer.RunOnce(context.Background())
	require.NoError(err)
	require.Len(first.HeadChanges, 1)
	observer.MarkRefreshEnqueued(first.HeadChanges[0], now)

	// Push moves the ref; the new SHA's refresh is enqueued normally.
	reader.trackingSHA = "3333333"
	now = now.Add(time.Minute)
	moved, err := observer.RunOnce(context.Background())
	require.NoError(err)
	require.Len(moved.HeadChanges, 1)
	observer.MarkRefreshEnqueued(moved.HeadChanges[0], now)

	// The first SHA's refresh completes only now. It must not rebind the
	// observation to the old SHA: doing so re-routed the next pass through
	// the SHA-changed branch and re-emitted the same head change inside
	// the new refresh's retry window.
	observer.MarkRefreshSucceeded(first.HeadChanges[0], now.Add(time.Second))

	now = now.Add(5 * time.Second)
	within, err := observer.RunOnce(context.Background())
	require.NoError(err)
	assert.Empty(within.HeadChanges)
}

func TestPushedHeadObserverDetectsSubsequentTrackingRefMove(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	repoID := seedRepo(t, d, "github.com", "acme", "widget")
	seedMRWithPlatformHead(t, d, repoID, 42, "feature/remote-head", "1111111", "")
	insertPushedHeadWorkspace(t, d, "ws-pr", db.WorkspaceItemTypePullRequest, 42, nil)
	reader := &fakeRemoteHeadReader{
		branch: "feature/remote-head",
		upstream: upstreamState{
			hasTracking: true,
			remoteName:  "origin",
			branchName:  "feature/remote-head",
		},
		trackingSHA: "1111111",
		trackingRef: "refs/remotes/origin/feature/remote-head",
		trackingOK:  true,
	}
	observer := newPushedHeadObserverForTest(t, d, reader)
	first, err := observer.RunOnce(context.Background())
	require.NoError(err)
	assert.Empty(first.HeadChanges)

	reader.trackingSHA = "2222222"
	second, err := observer.RunOnce(context.Background())
	require.NoError(err)
	require.Len(second.HeadChanges, 1)
	assert.Equal("1111111", second.HeadChanges[0].OldSHA)
	assert.Equal("2222222", second.HeadChanges[0].NewSHA)
}

func TestPushedHeadObserverAssociatesIssueWorkspaceAndObservesHead(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	repoID := seedRepo(t, d, "github.com", "acme", "widget")
	seedIssue(t, d, repoID, 7, "Track pushed branch")
	seedMRWithHeadRepo(
		t, d, repoID, 42,
		"feature/remote-head", "https://github.com/acme/widget.git",
	)
	worktreePath := setupMonitorRepo(t)
	runWorkspaceTestGit(t, worktreePath, "checkout", "-b", "feature/remote-head")
	runWorkspaceTestGit(t, worktreePath, "push", "-u", "origin", "feature/remote-head")
	runWorkspaceTestGit(t, worktreePath, "remote", "set-url", "origin", "git@github.com:acme/widget.git")
	insertMonitorWorkspace(t, d, worktreePath, nil)

	observer := NewPushedHeadObserver(d)
	observer.SetNowForTest(func() time.Time {
		return time.Date(2026, 5, 20, 14, 15, 0, 0, time.UTC)
	})
	result, err := observer.RunOnce(context.Background())
	require.NoError(err)
	require.Len(result.Associations, 1)
	assert.Equal("ws-issue", result.Associations[0].WorkspaceID)
	assert.Equal(7, result.Associations[0].IssueNumber)
	assert.Equal(42, result.Associations[0].PRNumber)
	assert.Equal("acme/widget", result.Associations[0].RepoPath)
	assert.Empty(result.HeadChanges)

	ws, err := d.GetWorkspace(context.Background(), "ws-issue")
	require.NoError(err)
	require.NotNil(ws)
	require.NotNil(ws.AssociatedPRNumber)
	assert.Equal(42, *ws.AssociatedPRNumber)
}

func TestPushedHeadObserverRunOnceHealsAssociatedKataWorkspace(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)
	repoID := seedRepo(t, d, "github.com", "acme", "widget")
	worktreePath := setupMonitorRepo(t)
	runWorkspaceTestGit(t, worktreePath, "checkout", "-b", "feature/kata-task")
	runWorkspaceTestGit(t, worktreePath, "push", "origin", "feature/kata-task")
	headSHA, err := gitHeadSHA(ctx, worktreePath)
	require.NoError(err)
	seedMRWithPlatformHead(
		t, d, repoID, 42, "feature/kata-task", headSHA,
		"https://github.com/acme/widget.git",
	)
	associatedPRNumber := 42
	kataMetadata := db.WorkspaceKataMetadata{
		DaemonID:   "local",
		ProjectUID: "project-1",
		IssueUID:   "issue-1",
	}
	require.NoError(d.InsertWorkspace(ctx, &db.Workspace{
		ID:                 "ws-kata",
		Platform:           "github",
		PlatformHost:       "github.com",
		RepoOwner:          "acme",
		RepoName:           "widget",
		ItemType:           db.WorkspaceItemTypeKataTask,
		ItemKey:            db.KataWorkspaceItemKey(kataMetadata),
		AssociatedPRNumber: &associatedPRNumber,
		GitHeadRef:         "feature/kata-task",
		WorkspaceBranch:    "feature/kata-task",
		WorktreePath:       worktreePath,
		TmuxSession:        "middleman-ws-kata",
		Status:             "ready",
		KataMetadata:       &kataMetadata,
	}))

	before, err := gitUpstreamState(ctx, worktreePath, "feature/kata-task")
	require.NoError(err)
	assert.False(before.hasTracking)

	observer := NewPushedHeadObserver(d)
	result, err := observer.RunOnce(ctx)
	require.NoError(err)
	assert.Empty(result.Associations)
	assert.Empty(result.HeadChanges)

	after, err := gitUpstreamState(ctx, worktreePath, "feature/kata-task")
	require.NoError(err)
	assert.True(after.hasTracking)
	assert.Equal("origin", after.remoteName)
	assert.Equal("feature/kata-task", after.branchName)
}

func TestPushedHeadObserverMissingRefAndTransientErrorKeepObservationState(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	repoID := seedRepo(t, d, "github.com", "acme", "widget")
	seedMRWithPlatformHead(t, d, repoID, 42, "feature/remote-head", "1111111", "")
	insertPushedHeadWorkspace(t, d, "ws-pr", db.WorkspaceItemTypePullRequest, 42, nil)
	reader := &fakeRemoteHeadReader{
		branch:      "feature/remote-head",
		upstream:    upstreamState{hasTracking: true, remoteName: "origin", branchName: "feature/remote-head"},
		trackingSHA: "1111111",
		trackingRef: "refs/remotes/origin/feature/remote-head",
		trackingOK:  true,
	}
	observer := newPushedHeadObserverForTest(t, d, reader)
	_, err := observer.RunOnce(context.Background())
	require.NoError(err)

	reader.trackingSHA = ""
	reader.trackingOK = false
	missing, err := observer.RunOnce(context.Background())
	require.NoError(err)
	assert.Empty(missing.HeadChanges)

	reader.trackingOK = true
	reader.trackingErr = errors.New("transient git failure")
	failed, err := observer.RunOnce(context.Background())
	require.NoError(err)
	assert.Empty(failed.HeadChanges)

	reader.trackingErr = nil
	reader.trackingSHA = "2222222"
	recovered, err := observer.RunOnce(context.Background())
	require.NoError(err)
	require.Len(recovered.HeadChanges, 1)
	assert.Equal("1111111", recovered.HeadChanges[0].OldSHA)
	assert.Equal("2222222", recovered.HeadChanges[0].NewSHA)
}

func TestPushedHeadObserverUpstreamHeal(t *testing.T) {
	forkHeadRepo := "https://github.com/contributor/widget.git"
	sameRepoURL := "https://github.com/acme/widget.git"
	unknownHeadRepo := ""
	cases := []struct {
		name             string
		branch           string
		mrHeadRepo       *string
		headRepoCloneURL string
		trackingOK       bool
		wantHeal         bool
	}{
		{
			name:             "synthetic PR branch is rewired to the PR head",
			branch:           "middleman/pr-42",
			headRepoCloneURL: sameRepoURL,
			trackingOK:       true,
			wantHeal:         true,
		},
		{
			name:             "head-branch checkout is rewired to itself",
			branch:           "feature/remote-head",
			headRepoCloneURL: sameRepoURL,
			trackingOK:       true,
			wantHeal:         true,
		},
		{
			name:             "legacy unknown snapshot heals from current same-repo metadata",
			branch:           "feature/remote-head",
			mrHeadRepo:       &unknownHeadRepo,
			headRepoCloneURL: sameRepoURL,
			trackingOK:       true,
			wantHeal:         true,
		},
		{
			name:             "unrelated branch is left alone",
			branch:           "scratch/unrelated",
			headRepoCloneURL: sameRepoURL,
			trackingOK:       true,
		},
		{
			name:             "fork PR head cannot be tracked by origin",
			branch:           "feature/remote-head",
			mrHeadRepo:       &forkHeadRepo,
			headRepoCloneURL: forkHeadRepo,
			trackingOK:       true,
		},
		{
			name:             "MR row without head-repo metadata is not trusted",
			branch:           "feature/remote-head",
			headRepoCloneURL: "",
			trackingOK:       true,
		},
		{
			name:             "fork-backed MR row blocks heal even with nil workspace head repo",
			branch:           "feature/remote-head",
			headRepoCloneURL: forkHeadRepo,
			trackingOK:       true,
		},
		{
			name:             "missing remote-tracking ref blocks heal",
			branch:           "middleman/pr-42",
			headRepoCloneURL: sameRepoURL,
			trackingOK:       false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			d := openTestDB(t)
			repoID := seedRepo(t, d, "github.com", "acme", "widget")
			seedMRWithPlatformHead(
				t, d, repoID, 42, "feature/remote-head", "2222222",
				tc.headRepoCloneURL,
			)
			require.NoError(d.InsertWorkspace(t.Context(), &db.Workspace{
				ID:              "ws-pr",
				Platform:        "github",
				PlatformHost:    "github.com",
				RepoOwner:       "acme",
				RepoName:        "widget",
				ItemType:        db.WorkspaceItemTypePullRequest,
				ItemNumber:      42,
				GitHeadRef:      "feature/remote-head",
				MRHeadRepo:      tc.mrHeadRepo,
				WorkspaceBranch: tc.branch,
				WorktreePath:    "/tmp/worktree",
				TmuxSession:     "middleman-ws-pr",
				Status:          "ready",
			}))
			reader := &fakeRemoteHeadReader{
				branch:      tc.branch,
				upstream:    upstreamState{},
				trackingSHA: "2222222",
				trackingRef: "refs/remotes/origin/feature/remote-head",
				trackingOK:  tc.trackingOK,
			}
			if !tc.trackingOK {
				reader.trackingSHA = ""
			}
			observer := newPushedHeadObserverForTest(t, d, reader)

			result, err := observer.RunOnce(context.Background())
			require.NoError(err)
			assert.Empty(result.HeadChanges)
			if !tc.wantHeal {
				assert.Empty(reader.setUpstreamCalls,
					"branch must not be rewired without positive same-repo evidence")
				return
			}
			require.Len(reader.setUpstreamCalls, 1,
				"missing upstream must be configured")
			call := reader.setUpstreamCalls[0]
			assert.Equal("/tmp/worktree", call.dir)
			assert.Equal(tc.branch, call.branch)
			assert.Equal("origin", call.remote)
			assert.Equal("refs/heads/feature/remote-head", call.mergeRef)
			assert.Equal(1, reader.trackingCalls,
				"heal probe and observation must share one tracking lookup")
		})
	}
}

func TestConfigureMissingUpstreamForRefreshMappedWorkspaces(t *testing.T) {
	tests := []struct {
		name         string
		platform     string
		platformHost string
		owner        string
		repoName     string
		itemType     string
		cloneURL     string
	}{
		{
			name:         "provider issue associated during refresh",
			platform:     "github",
			platformHost: "github.com",
			owner:        "acme",
			repoName:     "widget",
			itemType:     db.WorkspaceItemTypeIssue,
			cloneURL:     "https://github.com/acme/widget.git",
		},
		{
			name:         "Kata task associated during refresh",
			platform:     "github",
			platformHost: "github.com",
			owner:        "acme",
			repoName:     "widget",
			itemType:     db.WorkspaceItemTypeKataTask,
			cloneURL:     "https://github.com/acme/widget.git",
		},
		{
			name:         "GitLab nested group",
			platform:     "gitlab",
			platformHost: "gitlab.com",
			owner:        "group/subgroup",
			repoName:     "project",
			itemType:     db.WorkspaceItemTypePullRequest,
			cloneURL:     "https://gitlab.com/group/subgroup/project.git",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			reader := &fakeRemoteHeadReader{
				branch:      "feature/remote-head",
				trackingSHA: "2222222",
				trackingRef: "refs/remotes/origin/feature/remote-head",
				trackingOK:  true,
			}
			observer := newPushedHeadObserverForTest(t, openTestDB(t), reader)
			ws := &Workspace{
				ID:           "ws-refresh-mapped",
				Platform:     tt.platform,
				PlatformHost: tt.platformHost,
				RepoOwner:    tt.owner,
				RepoName:     tt.repoName,
				ItemType:     tt.itemType,
				ItemNumber:   42,
				WorktreePath: "/tmp/worktree",
			}
			mr := db.MergeRequest{
				Number:           42,
				HeadBranch:       "feature/remote-head",
				HeadRepoCloneURL: tt.cloneURL,
			}

			healed, err := observer.configureMissingUpstream(
				t.Context(), ws, mr, "feature/remote-head",
				map[string]trackingLookup{},
			)

			require.NoError(err)
			assert.True(healed)
			require.Len(reader.setUpstreamCalls, 1)
			assert.Equal("origin", reader.setUpstreamCalls[0].remote)
			assert.Equal("refs/heads/feature/remote-head", reader.setUpstreamCalls[0].mergeRef)
		})
	}
}
