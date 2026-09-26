package workspaceapi

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/testutil/reposeed"
)

func TestWorkspaceSubjectSnapshotKeepsReferenceAndCachedActivity(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	h := newEnrichmentTestHandler(t, "")
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	h.now = func() time.Time { return now }
	identity := db.GitHubRepoIdentity("github.com", "acme", "widget")
	repoID, err := reposeed.Seed(t.Context(), h.db, identity)
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
	repoID, err := reposeed.Seed(t.Context(), h.db, identity)
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

func TestWorkspaceSubjectSnapshotFallsBackFromRemovedAssociatedPullRequest(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	h := newEnrichmentTestHandler(t, "")
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	identity := db.GitHubRepoIdentity("github.com", "acme", "widget")
	repoID, err := reposeed.Seed(t.Context(), h.db, identity)
	require.NoError(err)
	_, err = h.db.UpsertIssue(t.Context(), &db.Issue{
		RepoID: repoID, PlatformID: 7, Number: 7,
		URL: "https://github.com/acme/widget/issues/7", Title: "Visible issue",
		Author: "alice", State: "open", CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
	})
	require.NoError(err)
	_, err = h.db.UpsertMergeRequest(t.Context(), &db.MergeRequest{
		RepoID: repoID, PlatformID: 42, Number: 42,
		URL: "https://github.com/acme/widget/pull/42", Title: "Removed pull",
		Author: "alice", State: "open", CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
	})
	require.NoError(err)
	associatedPR := 42
	require.NoError(h.db.InsertWorkspace(t.Context(), &db.Workspace{
		ID: "ws-issue", Platform: "github", PlatformHost: "github.com",
		RepoOwner: "acme", RepoName: "widget", ItemType: db.WorkspaceItemTypeIssue,
		ItemNumber: 7, AssociatedPRNumber: &associatedPR,
		GitHeadRef: "issue-7", WorktreePath: t.TempDir(), Status: "ready",
	}))
	_, err = h.db.WriteDB().ExecContext(t.Context(), `
		INSERT INTO forge_archive_items (
			repo_id, item_type, item_number, provider_item_id,
			provider_created_at, provider_updated_at, lifecycle_state
		) VALUES (?, 'merge_request', 42, 'pull-42', ?, ?, 'removed_upstream')`,
		repoID, now, now,
	)
	require.NoError(err)

	snapshot, err := h.WorkspaceSubjectSnapshot(t.Context())
	require.NoError(err)
	issueKey := db.WorkspaceSubjectKey{
		RepoID: repoID, ItemType: db.WorkspaceItemTypeIssue, ItemNumber: 7,
	}
	removedPRKey := db.WorkspaceSubjectKey{
		RepoID: repoID, ItemType: db.WorkspaceItemTypePullRequest, ItemNumber: 42,
	}
	require.Contains(snapshot.Subjects, issueKey)
	assert.Equal("Visible issue", snapshot.Subjects[issueKey].Subject.Title)
	assert.Equal(WorkspaceRef{ID: "ws-issue", Status: "ready"}, snapshot.Subjects[issueKey].Workspace)
	assert.NotContains(snapshot.Subjects, removedPRKey)
}

func TestWorkspaceSubjectSnapshotUsesStableRepositoryIdentityAfterRename(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	h := newEnrichmentTestHandler(t, "")
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	h.now = func() time.Time { return now }
	identity := db.GitHubRepoIdentity("github.com", "acme", "widget")
	identity.PlatformRepoID = 1001
	repository, err := h.db.ObserveRepository(t.Context(), identity)
	require.NoError(err)
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
	_, err = h.db.ObserveRepository(
		t.Context(), renamedIdentity,
	)
	require.NoError(err)

	snapshot, err := h.WorkspaceSubjectSnapshot(t.Context())
	require.NoError(err)
	key := db.WorkspaceSubjectKey{RepoID: repoID, ItemType: db.WorkspaceItemTypePullRequest, ItemNumber: 43}
	require.Contains(snapshot.Subjects, key)
	assert.Equal("gadget", snapshot.Subjects[key].Subject.RepoName)
	assert.Equal(WorkspaceRef{ID: "ws-renamed", Status: "ready"}, snapshot.Subjects[key].Workspace)
}

func TestWorkspaceSubjectSnapshotKeepsStableIdentityAcrossReusedRoute(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	h := newEnrichmentTestHandler(t, "")
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	h.now = func() time.Time { return now }
	oldIdentity := db.GitHubRepoIdentity("github.com", "acme", "widget")
	oldIdentity.PlatformRepoID = 1002
	oldRepo, err := h.db.ObserveRepository(t.Context(), oldIdentity)
	require.NoError(err)
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
	_, err = h.db.ObserveRepository(t.Context(), renamedIdentity)
	require.NoError(err)
	replacementIdentity := db.GitHubRepoIdentity("github.com", "acme", "widget")
	replacementIdentity.PlatformRepoID = 1003
	replacement, err := h.db.ObserveRepository(
		t.Context(), replacementIdentity,
	)
	require.NoError(err)
	_, err = h.db.UpsertMergeRequest(t.Context(), &db.MergeRequest{
		RepoID: replacement.Repository.ID, PlatformID: 145, Number: 45,
		URL: "https://github.com/acme/widget/pull/45", Title: "Replacement work",
		Author: "bob", State: "open", CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
	})
	require.NoError(err)

	snapshot, err := h.WorkspaceSubjectSnapshot(t.Context())
	require.NoError(err)
	key := db.WorkspaceSubjectKey{
		RepoID: oldRepo.Repository.ID, ItemType: db.WorkspaceItemTypePullRequest, ItemNumber: 45,
	}
	assert.Equal(WorkspaceRef{ID: "ws-old-route", Status: "ready"}, snapshot.OwnReferences[key])
	require.Contains(snapshot.Subjects, key)
	assert.Equal("gadget", snapshot.Subjects[key].Subject.RepoName)
}

func TestWorkspaceSubjectSnapshotKeepsNonReadyReferenceWithoutCachedActivity(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	h := newEnrichmentTestHandler(t, "")
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	h.now = func() time.Time { return now }
	identity := db.GitHubRepoIdentity("github.com", "acme", "widget")
	repoID, err := reposeed.Seed(t.Context(), h.db, identity)
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
