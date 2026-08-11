package workspaceapi

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
)

func TestWorkspaceSubjectSnapshotFencesRepositoryReconciliationAcrossReads(t *testing.T) {
	require := require.New(t)
	h := newEnrichmentTestHandler(t, "")
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	identity := db.GitHubRepoIdentity("github.com", "acme", "widget")
	identity.PlatformRepoID = "repo-acme-widget"
	repository, accepted, err := h.db.ReconcileRepositoryObservation(t.Context(), identity, now)
	require.NoError(err)
	require.True(accepted)
	require.NoError(h.db.InsertWorkspace(t.Context(), &db.Workspace{
		ID: "ws-fenced", Platform: "github", PlatformHost: "github.com",
		RepoOwner: "acme", RepoName: "widget", ItemType: db.WorkspaceItemTypePullRequest,
		ItemNumber: 51, WorktreePath: t.TempDir(), Status: "ready",
	}))
	_, err = h.db.UpsertMergeRequest(t.Context(), &db.MergeRequest{
		RepoID: repository.Repository.ID, PlatformID: 51, Number: 51,
		URL: "https://github.com/acme/widget/pull/51", Title: "Fenced work",
		Author: "alice", State: "open", CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
	})
	require.NoError(err)

	afterSummaries := make(chan struct{})
	continueSnapshot := make(chan struct{})
	h.workspaceSubjectAfterSummariesForTest = func() {
		close(afterSummaries)
		<-continueSnapshot
	}
	snapshotDone := make(chan error, 1)
	go func() {
		_, snapshotErr := h.WorkspaceSubjectSnapshot(context.Background())
		snapshotDone <- snapshotErr
	}()
	<-afterSummaries

	writeAttempted := make(chan struct{})
	restoreHook := h.db.SetBeforeRepositoryReconciliationWriteLockForTest(func() {
		close(writeAttempted)
	})
	t.Cleanup(restoreHook)
	renameDone := make(chan error, 1)
	go func() {
		renamed := db.GitHubRepoIdentity("github.com", "acme", "gadget")
		renamed.PlatformRepoID = identity.PlatformRepoID
		_, _, renameErr := h.db.ReconcileRepositoryObservation(context.Background(), renamed, now.Add(time.Minute))
		renameDone <- renameErr
	}()
	<-writeAttempted

	select {
	case err := <-renameDone:
		require.Failf("repository reconciliation completed during snapshot", "error: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(continueSnapshot)
	require.NoError(<-snapshotDone)
	require.NoError(<-renameDone)
}

func TestWorkspaceSubjectSnapshotKeepsReferenceAndCachedActivity(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	h := newEnrichmentTestHandler(t, "")
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	h.now = func() time.Time { return now }
	identity := db.GitHubRepoIdentity("github.com", "acme", "widget")
	identity.PlatformRepoID = "repo-acme-widget"
	repoID, err := h.db.UpsertRepo(t.Context(), identity)
	require.NoError(err)
	_, err = h.db.UpsertMergeRequest(t.Context(), &db.MergeRequest{
		RepoID: repoID, PlatformID: 41, Number: 41,
		URL: "https://github.com/acme/widget/pull/41", Title: "Live work",
		Author: "alice", State: "open", CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
	})
	require.NoError(err)
	require.NoError(h.db.InsertWorkspace(t.Context(), &db.Workspace{
		ID: "ws-41", Platform: "github", PlatformHost: "github.com",
		RepoOwner: "acme", RepoName: "widget", ItemType: db.WorkspaceItemTypePullRequest,
		ItemNumber: 41, GitHeadRef: "feature", WorkspaceBranch: "feature",
		WorktreePath: t.TempDir(), TmuxSession: "ws-41", Status: "ready",
	}))
	activityAt := now.Add(time.Minute).Format(time.RFC3339)
	h.workspaceEnrichmentCache["ws-41"] = workspaceEnrichmentCacheEntry{
		response: workspaceResponse{TmuxLastOutputAt: &activityAt},
		hasTmux:  true, tmuxRefreshedAt: now, tmuxAttemptAt: now,
	}

	snapshot, err := h.WorkspaceSubjectSnapshot(t.Context())
	require.NoError(err)
	key := db.WorkspaceSubjectKey{RepoID: repoID, ItemType: db.WorkspaceItemTypePullRequest, ItemNumber: 41}
	assert.Equal(WorkspaceRef{ID: "ws-41", Status: "ready"}, snapshot.OwnReferences[key])
	require.Contains(snapshot.Subjects, key)
	require.NotNil(snapshot.Subjects[key].ActivityAt)
	assert.Equal(now.Add(time.Minute), *snapshot.Subjects[key].ActivityAt)
	assert.Equal("Live work", snapshot.Subjects[key].Subject.Title)
}

func TestWorkspaceSubjectSnapshotResolvesAdHocAssociationAsPullReference(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	h := newEnrichmentTestHandler(t, "")
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	h.now = func() time.Time { return now }
	identity := db.GitHubRepoIdentity("github.com", "acme", "widget")
	identity.PlatformRepoID = "repo-acme-widget"
	repoID, err := h.db.UpsertRepo(t.Context(), identity)
	require.NoError(err)
	_, err = h.db.UpsertMergeRequest(t.Context(), &db.MergeRequest{
		RepoID: repoID, PlatformID: 42, Number: 42,
		URL: "https://github.com/acme/widget/pull/42", Title: "Associated work",
		Author: "alice", State: "open", CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
	})
	require.NoError(err)
	associatedPR := 42
	require.NoError(h.db.InsertWorkspace(t.Context(), &db.Workspace{
		ID: "ws-adhoc", Platform: "github", PlatformHost: "github.com",
		RepoOwner: "acme", RepoName: "widget", ItemType: db.WorkspaceItemTypeAdHoc,
		ItemKey: "adhoc:associated-work", AssociatedPRNumber: &associatedPR,
		GitHeadRef: "feature", WorkspaceBranch: "feature",
		WorktreePath: t.TempDir(), TmuxSession: "ws-adhoc", Status: "ready",
	}))

	snapshot, err := h.WorkspaceSubjectSnapshot(t.Context())
	require.NoError(err)
	key := db.WorkspaceSubjectKey{RepoID: repoID, ItemType: db.WorkspaceItemTypePullRequest, ItemNumber: 42}
	assert.Empty(snapshot.OwnReferences, "ad-hoc identity must not masquerade as a direct PR workspace")
	require.Contains(snapshot.Subjects, key)
	assert.Equal(WorkspaceRef{ID: "ws-adhoc", Status: "ready"}, snapshot.Subjects[key].Workspace)
}

func TestWorkspaceSubjectSnapshotUsesStableRepositoryIdentityAfterRename(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	h := newEnrichmentTestHandler(t, "")
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	h.now = func() time.Time { return now }
	identity := db.GitHubRepoIdentity("github.com", "acme", "widget")
	identity.PlatformRepoID = "repo-acme-widget"
	repository, accepted, err := h.db.ReconcileRepositoryObservation(
		t.Context(), identity, now,
	)
	require.NoError(err)
	require.True(accepted)
	repoID := repository.Repository.ID
	_, err = h.db.UpsertMergeRequest(t.Context(), &db.MergeRequest{
		RepoID: repoID, PlatformID: 43, Number: 43,
		URL: "https://github.com/acme/gadget/pull/43", Title: "Renamed work",
		Author: "alice", State: "open", CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
	})
	require.NoError(err)
	require.NoError(h.db.InsertWorkspace(t.Context(), &db.Workspace{
		ID: "ws-renamed", Platform: "github", PlatformHost: "github.com",
		RepoOwner: "acme", RepoName: "widget", ItemType: db.WorkspaceItemTypePullRequest,
		ItemNumber: 43, GitHeadRef: "feature", WorkspaceBranch: "feature",
		WorktreePath: t.TempDir(), TmuxSession: "ws-renamed", Status: "ready",
	}))
	renamedIdentity := db.GitHubRepoIdentity("github.com", "acme", "gadget")
	renamedIdentity.PlatformRepoID = identity.PlatformRepoID
	_, accepted, err = h.db.ReconcileRepositoryObservation(
		t.Context(), renamedIdentity, now.Add(time.Minute),
	)
	require.NoError(err)
	require.True(accepted)

	snapshot, err := h.WorkspaceSubjectSnapshot(t.Context())
	require.NoError(err)
	key := db.WorkspaceSubjectKey{RepoID: repoID, ItemType: db.WorkspaceItemTypePullRequest, ItemNumber: 43}
	require.Contains(snapshot.Subjects, key)
	assert.Equal("gadget", snapshot.Subjects[key].Subject.RepoName)
	assert.Equal(WorkspaceRef{ID: "ws-renamed", Status: "ready"}, snapshot.Subjects[key].Workspace)
}

func TestWorkspaceSubjectSnapshotRejectsAmbiguousReusedRoute(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	h := newEnrichmentTestHandler(t, "")
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	h.now = func() time.Time { return now }
	oldIdentity := db.GitHubRepoIdentity("github.com", "acme", "widget")
	oldIdentity.PlatformRepoID = "repo-old-widget"
	oldRepo, accepted, err := h.db.ReconcileRepositoryObservation(t.Context(), oldIdentity, now)
	require.NoError(err)
	require.True(accepted)
	_, err = h.db.UpsertMergeRequest(t.Context(), &db.MergeRequest{
		RepoID: oldRepo.Repository.ID, PlatformID: 45, Number: 45,
		URL: "https://github.com/acme/widget/pull/45", Title: "Old routed work",
		Author: "alice", State: "open", CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
	})
	require.NoError(err)
	require.NoError(h.db.InsertWorkspace(t.Context(), &db.Workspace{
		ID: "ws-old-route", Platform: "github", PlatformHost: "github.com",
		RepoOwner: "acme", RepoName: "widget", ItemType: db.WorkspaceItemTypePullRequest,
		ItemNumber: 45, GitHeadRef: "feature", WorkspaceBranch: "feature",
		WorktreePath: t.TempDir(), TmuxSession: "ws-old-route", Status: "ready",
	}))

	renamedIdentity := db.GitHubRepoIdentity("github.com", "acme", "gadget")
	renamedIdentity.PlatformRepoID = oldIdentity.PlatformRepoID
	_, accepted, err = h.db.ReconcileRepositoryObservation(t.Context(), renamedIdentity, now.Add(time.Minute))
	require.NoError(err)
	require.True(accepted)
	replacementIdentity := db.GitHubRepoIdentity("github.com", "acme", "widget")
	replacementIdentity.PlatformRepoID = "repo-replacement-widget"
	replacement, accepted, err := h.db.ReconcileRepositoryObservation(
		t.Context(), replacementIdentity, now.Add(2*time.Minute),
	)
	require.NoError(err)
	require.True(accepted)
	_, err = h.db.UpsertMergeRequest(t.Context(), &db.MergeRequest{
		RepoID: replacement.Repository.ID, PlatformID: 145, Number: 45,
		URL: "https://github.com/acme/widget/pull/45", Title: "Replacement work",
		Author: "bob", State: "open", CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
	})
	require.NoError(err)

	snapshot, err := h.WorkspaceSubjectSnapshot(t.Context())
	require.NoError(err)
	assert.Empty(snapshot.OwnReferences,
		"ambiguous historical routes must not bind the workspace to the replacement repository")
	assert.Empty(snapshot.Subjects)
}

func TestWorkspaceSubjectSnapshotKeepsNonReadyReferenceWithoutCachedActivity(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	h := newEnrichmentTestHandler(t, "")
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	h.now = func() time.Time { return now }
	identity := db.GitHubRepoIdentity("github.com", "acme", "widget")
	identity.PlatformRepoID = "repo-acme-widget"
	repoID, err := h.db.UpsertRepo(t.Context(), identity)
	require.NoError(err)
	_, err = h.db.UpsertMergeRequest(t.Context(), &db.MergeRequest{
		RepoID: repoID, PlatformID: 44, Number: 44,
		URL: "https://github.com/acme/widget/pull/44", Title: "Stopped work",
		Author: "alice", State: "open", CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
	})
	require.NoError(err)
	require.NoError(h.db.InsertWorkspace(t.Context(), &db.Workspace{
		ID: "ws-stopped", Platform: "github", PlatformHost: "github.com",
		RepoOwner: "acme", RepoName: "widget", ItemType: db.WorkspaceItemTypePullRequest,
		ItemNumber: 44, GitHeadRef: "feature", WorkspaceBranch: "feature",
		WorktreePath: t.TempDir(), TmuxSession: "ws-stopped", Status: "ready",
	}))
	activityAt := now.Add(time.Minute).Format(time.RFC3339)
	h.workspaceEnrichmentCache["ws-stopped"] = workspaceEnrichmentCacheEntry{
		response: workspaceResponse{TmuxLastOutputAt: &activityAt},
		hasTmux:  true, tmuxRefreshedAt: now, tmuxAttemptAt: now,
	}
	errorMessage := "tmux session no longer exists"
	require.NoError(h.db.UpdateWorkspaceStatus(
		t.Context(), "ws-stopped", "error", &errorMessage,
	))

	snapshot, err := h.WorkspaceSubjectSnapshot(t.Context())
	require.NoError(err)
	key := db.WorkspaceSubjectKey{RepoID: repoID, ItemType: db.WorkspaceItemTypePullRequest, ItemNumber: 44}
	assert.Equal(WorkspaceRef{ID: "ws-stopped", Status: "error"}, snapshot.OwnReferences[key])
	require.Contains(snapshot.Subjects, key)
	assert.Equal(WorkspaceRef{ID: "ws-stopped", Status: "error"}, snapshot.Subjects[key].Workspace)
	assert.Nil(snapshot.Subjects[key].ActivityAt)
}
