package workspace

import (
	"context"
	"errors"
	"testing"
	"time"

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/middleman/internal/db"
)

type fakeRemoteHeadReader struct {
	branchCalls   int
	upstreamCalls int
	trackingCalls int
	branch        string
	upstream      upstreamState
	trackingSHA   string
	trackingRef   string
	trackingOK    bool
	trackingErr   error
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
	branch, sha string,
) {
	t.Helper()
	now := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	_, err := d.UpsertMergeRequest(t.Context(), &db.MergeRequest{
		RepoID:          repoID,
		PlatformID:      repoID*10000 + int64(number),
		Number:          number,
		Title:           "Refresh me",
		Author:          "author",
		State:           db.MergeRequestStateOpen,
		HeadBranch:      branch,
		PlatformHeadSHA: sha,
		BaseBranch:      "main",
		CreatedAt:       now,
		UpdatedAt:       now,
		LastActivityAt:  now,
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
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	repoID := seedRepo(t, d, "github.com", "acme", "widget")
	seedMRWithPlatformHead(t, d, repoID, 42, "feature/remote-head", "2222222")
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
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	repoID := seedRepo(t, d, "github.com", "acme", "widget")
	seedMRWithPlatformHead(t, d, repoID, 42, "feature/remote-head", "1111111")
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
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	repoID := seedRepo(t, d, "github.com", "acme", "widget")
	seedMRWithPlatformHead(t, d, repoID, 42, "feature/remote-head", "1111111")
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
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	repoID := seedRepo(t, d, "github.com", "acme", "widget")
	seedMRWithPlatformHead(t, d, repoID, 42, "feature/remote-head", "1111111")
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
	seedMRWithPlatformHead(t, d, repoID, 42, "feature/remote-head", "2222222")

	retry, err := observer.RunOnce(context.Background())
	require.NoError(err)
	assert.Empty(retry.HeadChanges)
}

func TestPushedHeadObserverDetectsSubsequentTrackingRefMove(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	repoID := seedRepo(t, d, "github.com", "acme", "widget")
	seedMRWithPlatformHead(t, d, repoID, 42, "feature/remote-head", "1111111")
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
	assert := Assert.New(t)
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

func TestPushedHeadObserverMissingRefAndTransientErrorKeepObservationState(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	repoID := seedRepo(t, d, "github.com", "acme", "widget")
	seedMRWithPlatformHead(t, d, repoID, 42, "feature/remote-head", "1111111")
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
