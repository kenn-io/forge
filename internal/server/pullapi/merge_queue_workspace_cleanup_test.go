package pullapi

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/federationauth"
	"go.kenn.io/forge/platform"
)

type requestedWorkspaceDeletion struct {
	hostKey     string
	workspaceID string
}

// mergeQueueCleanupFixture is a pull request on a merge-queue branch, with
// sync hooked to the handler the way the server wires it.
type mergeQueueCleanupFixture struct {
	t        *testing.T
	provider *deferredMergeTestProvider
	srv      *deferredMergeRouteServer
	database *db.DB
	repoID   int64

	mu        sync.Mutex
	deletions []requestedWorkspaceDeletion
	refuse    int
}

func newMergeQueueCleanupFixture(t *testing.T) *mergeQueueCleanupFixture {
	t.Helper()
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	ref := platform.RepoRef{
		Platform:      platform.KindGitLab,
		Host:          "gitlab.example.com",
		Owner:         "group",
		Name:          "project",
		RepoPath:      "group/project",
		Key:           platform.RepositoryIDKey(4242),
		DefaultBranch: "main",
	}
	f := &mergeQueueCleanupFixture{t: t}
	f.provider = &deferredMergeTestProvider{
		ref: ref,
		mergeRequests: []platform.MergeRequest{{
			Repo: ref, PlatformID: 7001, Number: 7,
			URL:   "https://gitlab.example.com/group/project/-/merge_requests/7",
			Title: "Queue merge", Author: "ada", State: "open",
			HeadBranch: "feature", BaseBranch: "main",
			HeadSHA: "head-sha", BaseSHA: "base-sha", CIStatus: "success",
			CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
		}},
		mergeQueue: true,
	}
	f.srv, f.database, f.repoID, _ = newDeferredMergeRouteServer(
		t, f.provider, ref, now, nil,
		deferredMergeTestOptions{queueWorkspaceDeletion: f.deleteWorkspace},
	)
	_, err := f.database.UpsertMergeQueueState(t.Context(), f.repoID, db.MergeQueueState{
		Number: 7, BaseBranch: "main", Enabled: true, MergeMethod: "squash",
		ObservedAt: now.Add(-time.Hour),
	})
	require.NoError(t, err)
	f.srv.handler.syncer.SetOnMergeRequestTerminal(func(context.Context, int64, int) {
		f.srv.handler.WakeMergeWorkspaceCleanup()
	})
	f.srv.handler.StartMergeWorkspaceCleanup()
	return f
}

func (f *mergeQueueCleanupFixture) deleteWorkspace(_ context.Context, hostKey, workspaceID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletions = append(f.deletions, requestedWorkspaceDeletion{hostKey: hostKey, workspaceID: workspaceID})
	if f.refuse > 0 {
		f.refuse--
		return errors.New("fleet host is unavailable")
	}
	return nil
}

func (f *mergeQueueCleanupFixture) requestedDeletions() []requestedWorkspaceDeletion {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]requestedWorkspaceDeletion(nil), f.deletions...)
}

// enqueue merges as ctx's caller, asking for workspace ws-1 to be deleted.
func (f *mergeQueueCleanupFixture) enqueue(ctx context.Context) {
	f.t.Helper()
	result, err := f.srv.handler.mergePRWithBody(
		ctx, "gitlab", "gitlab.example.com", "group", "project", 7,
		mergePRInputBody{Method: "squash", ExpectedHeadSHA: "head-sha", DeleteWorkspaceID: "ws-1"},
	)
	require.NoError(f.t, err)
	require.True(f.t, result.Queued)
	assert.False(f.t, result.WorkspaceCleanupPending)
	require.True(f.t, f.kept())
}

// providerReports changes the pull request's provider state, as the merge
// queue landing it or a user closing it does.
func (f *mergeQueueCleanupFixture) providerReports(state string) {
	f.provider.mu.Lock()
	defer f.provider.mu.Unlock()
	mr := &f.provider.mergeRequests[0]
	at := mr.UpdatedAt.Add(time.Hour)
	mr.State = state
	mr.UpdatedAt, mr.LastActivityAt = at, at
	if state == "merged" {
		mr.MergedAt = &at
	} else {
		mr.ClosedAt = &at
	}
}

// sync records the provider's state through the close-detection flow.
func (f *mergeQueueCleanupFixture) sync() {
	f.t.Helper()
	require.NoError(f.t, f.srv.handler.syncer.SyncClosedMROnProvider(f.t.Context(), f.repoID, 7))
}

func (f *mergeQueueCleanupFixture) kept() bool {
	f.t.Helper()
	cleanups, err := f.database.ListMergeWorkspaceCleanups(f.t.Context())
	require.NoError(f.t, err)
	return len(cleanups) > 0
}

// drain stops the handler's cleanup loop.
func (f *mergeQueueCleanupFixture) drain() {
	f.t.Helper()
	require.NoError(f.t, f.srv.handler.Shutdown(f.t.Context()))
}

func TestMergeQueueWorkspaceCleanupDeletesOnOwningFleetHostAndRetriesRefusal(t *testing.T) {
	f := newMergeQueueCleanupFixture(t)
	f.refuse = 1
	spoke := federationauth.WithPrincipal(t.Context(), federationauth.Principal{
		NodeID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	})
	f.enqueue(spoke)

	f.providerReports("merged")
	f.sync()
	require.Eventually(t, func() bool { return len(f.requestedDeletions()) > 0 },
		5*time.Second, 10*time.Millisecond)

	f.srv.handler.WakeMergeWorkspaceCleanup()
	require.Eventually(t, func() bool { return !f.kept() }, 5*time.Second, 10*time.Millisecond)
	f.drain()
	spokeDeletion := requestedWorkspaceDeletion{hostKey: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", workspaceID: "ws-1"}
	assert.Equal(t, []requestedWorkspaceDeletion{spokeDeletion, spokeDeletion}, f.requestedDeletions())
}

func TestMergeQueueWorkspaceCleanupDroppedWhenPullRequestCloses(t *testing.T) {
	f := newMergeQueueCleanupFixture(t)
	f.enqueue(t.Context())

	f.providerReports("closed")
	f.sync()
	require.Eventually(t, func() bool { return !f.kept() }, 5*time.Second, 10*time.Millisecond)
	f.drain()
	assert.Empty(t, f.requestedDeletions())
}
