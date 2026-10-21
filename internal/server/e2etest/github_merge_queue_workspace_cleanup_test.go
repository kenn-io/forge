package e2etest

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/server"
	"go.kenn.io/forge/internal/testutil/gitfixture"
	serverfake "go.kenn.io/forge/internal/testutil/serverfake"
)

const githubMergeQueueEnqueued = `{"status":"enqueued","details":{"message":"Pull request was added to the merge queue."}}`

// mergeQueueWorkspaceE2E is a pull request on a merge-queue branch with a
// workspace the merge asks to delete.
type mergeQueueWorkspaceE2E struct {
	githubAsyncMergeE2E
	options     server.ServerOptions
	workspaceID string
}

func setupMergeQueueWorkspaceE2E(t *testing.T) mergeQueueWorkspaceE2E {
	t.Helper()
	fixture := seedGitHubAsyncMergeE2E(t, githubMergeQueueEnqueued)
	seedMergeQueue(t, fixture.database, fixture.repoID, true)
	// A clean checkout, so deletion's uncommitted-changes check passes.
	worktree := gitfixture.ScratchDir(t)
	gitfixture.Run(t, worktree, "init", "--initial-branch=feature")
	gitfixture.Run(t, worktree, "-c", "user.name=Test", "-c", "user.email=test@test.com",
		"commit", "--allow-empty", "-m", "feature")
	require.NoError(t, fixture.database.InsertWorkspace(t.Context(), &db.Workspace{
		ID: "ws-queued", Platform: "github", PlatformHost: "github.com",
		RepoOwner: "acme", RepoName: "widget",
		ItemType: db.WorkspaceItemTypePullRequest, ItemNumber: 7,
		GitHeadRef: "feature", WorktreePath: worktree, Status: "ready",
	}))
	return mergeQueueWorkspaceE2E{
		githubAsyncMergeE2E: fixture,
		options: server.ServerOptions{
			WorktreeDir: t.TempDir(), DisableWorkspaceBackgroundMonitors: true,
		},
		workspaceID: "ws-queued",
	}
}

// enqueue merges through the API, which adds the pull request to the queue
// and asks for its workspace to be deleted.
func (f mergeQueueWorkspaceE2E) enqueue(t *testing.T, srv http.Handler, deferred bool) {
	t.Helper()
	path := "/api/v1/pulls/github/acme/widget/7/merge"
	status := http.StatusOK
	if deferred {
		path += "/deferred"
		status = http.StatusAccepted
	}
	rr := doJSONRequest(t, srv, http.MethodPost, path, json.RawMessage(
		`{"method":"squash","commit_title":"Merge title","commit_message":"Merge body","expected_head_sha":"reviewed-sha","delete_workspace_id":"`+f.workspaceID+`"}`,
	))
	require.Equal(t, status, rr.Code, rr.Body.String())
	if deferred {
		return
	}
	var result struct {
		Merged                  bool   `json:"merged"`
		Queued                  bool   `json:"queued"`
		WorkspaceCleanupPending bool   `json:"workspace_cleanup_pending"`
		WorkspaceCleanupWarning string `json:"workspace_cleanup_warning"`
	}
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&result))
	assert.False(t, result.Merged)
	assert.True(t, result.Queued)
	assert.False(t, result.WorkspaceCleanupPending, "nothing is deleted at enqueue time")
	assert.Empty(t, result.WorkspaceCleanupWarning)
}

// queueMerges makes GitHub report the pull request merged, as it does once
// the merge queue lands it.
func (f mergeQueueWorkspaceE2E) queueMerges() {
	f.upstream.mu.Lock()
	f.upstream.merged = true
	f.upstream.mu.Unlock()
}

func (f mergeQueueWorkspaceE2E) requireWorkspaceKept(t *testing.T) {
	t.Helper()
	ws, err := f.database.GetWorkspace(t.Context(), f.workspaceID)
	require.NoError(t, err)
	require.NotNil(t, ws)
	require.Equal(t, "ready", ws.Status)
}

func (f mergeQueueWorkspaceE2E) requireWorkspaceDeleted(t *testing.T) {
	t.Helper()
	require.Eventually(t, func() bool {
		ws, err := f.database.GetWorkspace(t.Context(), f.workspaceID)
		return err == nil && ws == nil
	}, 5*time.Second, 10*time.Millisecond)
	kept, err := f.database.ListMergeWorkspaceCleanups(t.Context())
	require.NoError(t, err)
	assert.Empty(t, kept, "a resolved request is forgotten")
}

// syncPullRequest re-reads the pull request through sync's close-detection
// flow, which records the merge once GitHub reports it.
func (f mergeQueueWorkspaceE2E) syncPullRequest(t *testing.T, syncer *ghclient.Syncer) {
	t.Helper()
	require.NoError(t, syncer.SyncClosedMROnProvider(t.Context(), f.repoID, 7))
}

func TestGitHubMergeQueueDeletesWorkspaceWhenSyncSeesQueueMerge(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	fixture := setupMergeQueueWorkspaceE2E(t)
	srv, syncer := fixture.startServer(t, fixture.options)

	fixture.enqueue(t, srv, false)
	fixture.syncPullRequest(t, syncer)
	fixture.requireWorkspaceKept(t)

	fixture.queueMerges()
	fixture.syncPullRequest(t, syncer)
	fixture.requireWorkspaceDeleted(t)
}

func TestGitHubDeferredMergeIntoQueueDeletesWorkspaceWhenSyncSeesQueueMerge(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	fixture := setupMergeQueueWorkspaceE2E(t)
	srv, syncer := fixture.startServer(t, fixture.options)

	fixture.enqueue(t, srv, true)
	require.Eventually(t, func() bool {
		kept, err := fixture.database.ListMergeWorkspaceCleanups(t.Context())
		return err == nil && len(kept) == 1
	}, 5*time.Second, 10*time.Millisecond, "the deferred worker's enqueue keeps the deletion")
	fixture.requireWorkspaceKept(t)

	fixture.queueMerges()
	fixture.syncPullRequest(t, syncer)
	fixture.requireWorkspaceDeleted(t)
}

func TestGitHubMergeQueueWorkspaceCleanupSurvivesRestart(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	fixture := setupMergeQueueWorkspaceE2E(t)
	first, _ := fixture.startServer(t, fixture.options)
	fixture.enqueue(t, first, false)
	require.NoError(t, first.Shutdown(t.Context()))

	_, syncer := fixture.startServer(t, fixture.options)
	fixture.queueMerges()
	fixture.syncPullRequest(t, syncer)
	fixture.requireWorkspaceDeleted(t)
}

func TestGitHubMergeQueueMergeRecordedWhileStoppedDeletesWorkspaceAtStartup(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	fixture := setupMergeQueueWorkspaceE2E(t)
	first, syncer := fixture.startServer(t, fixture.options)
	fixture.enqueue(t, first, false)
	require.NoError(t, first.Shutdown(t.Context()))

	// The merge is recorded with no server attached to act on it, as when
	// the daemon stops between recording the merge and deleting.
	fixture.queueMerges()
	fixture.syncPullRequest(t, syncer)
	mr, err := fixture.database.GetMergeRequestByRepoIDAndNumber(t.Context(), fixture.repoID, 7)
	require.NoError(t, err)
	require.Equal(t, db.MergeRequestStateMerged, mr.State)
	fixture.requireWorkspaceKept(t)

	fixture.startServer(t, fixture.options)
	fixture.requireWorkspaceDeleted(t)
}
