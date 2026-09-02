package db

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListWorkspaceSubjectMetadataReturnsPullRequestAndIssue(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	d := openTestDB(t)
	repoID := insertTestRepo(t, d, "acme", "widget")
	base := baseTime()
	insertTestMR(t, d, repoID, 4, "live pull request", base)
	insertTestIssue(t, d, repoID, 5, "live issue", base)

	got, err := d.ListWorkspaceSubjectMetadata(t.Context(), []WorkspaceSubjectKey{
		{RepoID: repoID, ItemType: WorkspaceItemTypePullRequest, ItemNumber: 4},
		{RepoID: repoID, ItemType: WorkspaceItemTypeIssue, ItemNumber: 5},
	})
	require.NoError(err)
	require.Len(got, 2)
	assert.Equal("live pull request", got[WorkspaceSubjectKey{
		RepoID: repoID, ItemType: WorkspaceItemTypePullRequest, ItemNumber: 4,
	}].Title)
	assert.Equal("live issue", got[WorkspaceSubjectKey{
		RepoID: repoID, ItemType: WorkspaceItemTypeIssue, ItemNumber: 5,
	}].Title)
}

func TestListWorkspaceSubjectMetadataUsesLaunchSpecWithoutProviderReplica(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	database := openTestDB(t)
	workspace, spec := workspaceLaunchFixture(t, database, "ws-node-overlay")
	require.NoError(database.CreateWorkspaceWithLaunchSpec(
		t.Context(), workspace, spec,
	))
	repo, err := database.GetRepoByIdentity(
		t.Context(), GitHubRepoIdentity("github.com", "acme", "widget"),
	)
	require.NoError(err)
	require.NotNil(repo)

	key := WorkspaceSubjectKey{
		RepoID: repo.ID, ItemType: WorkspaceItemTypePullRequest, ItemNumber: 7,
	}
	got, err := database.ListWorkspaceSubjectMetadata(
		t.Context(), []WorkspaceSubjectKey{key},
	)
	require.NoError(err)
	require.Contains(got, key)
	assert.Equal("github", got[key].Platform)
	assert.Equal("github.com", got[key].PlatformHost)
	assert.Equal(spec.Repository.PlatformRepoID, got[key].PlatformRepoID)
	assert.Empty(got[key].Title, "provider details stay hub-owned")
}

func TestListWorkspaceSubjectMetadataUsesStableRepositoryAfterRename(t *testing.T) {
	require := require.New(t)
	database := openTestDB(t)
	workspace, spec := workspaceLaunchFixture(t, database, "ws-renamed-overlay")
	require.NoError(database.CreateWorkspaceWithLaunchSpec(
		t.Context(), workspace, spec,
	))
	repo, err := database.GetRepoByIdentity(
		t.Context(), GitHubRepoIdentity("github.com", "acme", "widget"),
	)
	require.NoError(err)
	require.NotNil(repo)
	_, accepted, err := database.ReconcileRepositoryObservation(
		t.Context(), RepoIdentity{
			Platform: "github", PlatformHost: "github.com",
			PlatformRepoID: spec.Repository.PlatformRepoID,
			Owner:          "acme-renamed", Name: "widget-renamed",
		}, time.Now().UTC().Add(time.Hour),
	)
	require.NoError(err)
	require.True(accepted)
	key := WorkspaceSubjectKey{
		RepoID: repo.ID, ItemType: WorkspaceItemTypePullRequest, ItemNumber: 7,
	}

	got, err := database.ListWorkspaceSubjectMetadata(
		t.Context(), []WorkspaceSubjectKey{key},
	)

	require.NoError(err)
	require.Contains(got, key)
	require.Equal("acme-renamed", got[key].RepoOwner)
	require.Equal("widget-renamed", got[key].RepoName)
}

func TestListWorkspaceSubjectMetadataHidesOnlyRemovedUpstreamItems(t *testing.T) {
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	repoID := insertTestRepo(t, d, "acme", "widget")
	now := time.Now().UTC().Truncate(time.Second)
	insertTestMR(t, d, repoID, 1, "inaccessible pull", now)
	insertTestMR(t, d, repoID, 2, "removed pull", now)
	insertTestIssue(t, d, repoID, 3, "inaccessible issue", now)
	insertTestIssue(t, d, repoID, 4, "removed issue", now)
	_, err := d.WriteDB().ExecContext(ctx, `
		INSERT INTO forge_archive_items (
			repo_id, item_type, item_number, provider_item_id,
			provider_created_at, provider_updated_at, lifecycle_state
		) VALUES
			(?, 'merge_request', 1, 'pr-1', ?, ?, 'inaccessible'),
			(?, 'merge_request', 2, 'pr-2', ?, ?, 'removed_upstream'),
			(?, 'issue', 3, 'issue-3', ?, ?, 'inaccessible'),
			(?, 'issue', 4, 'issue-4', ?, ?, 'removed_upstream')`,
		repoID, now, now, repoID, now, now,
		repoID, now, now, repoID, now, now,
	)
	require.NoError(err)

	got, err := d.ListWorkspaceSubjectMetadata(ctx, []WorkspaceSubjectKey{
		{RepoID: repoID, ItemType: WorkspaceItemTypePullRequest, ItemNumber: 1},
		{RepoID: repoID, ItemType: WorkspaceItemTypePullRequest, ItemNumber: 2},
		{RepoID: repoID, ItemType: WorkspaceItemTypeIssue, ItemNumber: 3},
		{RepoID: repoID, ItemType: WorkspaceItemTypeIssue, ItemNumber: 4},
	})
	require.NoError(err)
	require.Len(got, 2)
	require.Contains(got, WorkspaceSubjectKey{
		RepoID: repoID, ItemType: WorkspaceItemTypePullRequest, ItemNumber: 1,
	})
	require.Contains(got, WorkspaceSubjectKey{
		RepoID: repoID, ItemType: WorkspaceItemTypeIssue, ItemNumber: 3,
	})
}

func TestListWorkspaceSubjectMetadataSupportsLargeSubjectSets(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	d := openTestDB(t)
	repoID := insertTestRepo(t, d, "acme", "widget")
	base := baseTime()
	insertTestMR(t, d, repoID, 1, "live pull request", base)

	// Three bind parameters per subject would exceed SQLite's default variable
	// limit here. The request set must remain bounded independently of the
	// number of retained workspaces.
	keys := make([]WorkspaceSubjectKey, 11_000)
	for i := range keys {
		keys[i] = WorkspaceSubjectKey{
			RepoID: repoID, ItemType: WorkspaceItemTypePullRequest, ItemNumber: i + 1,
		}
	}

	got, err := d.ListWorkspaceSubjectMetadata(t.Context(), keys)
	require.NoError(err, "metadata lookup failed for %d subjects", len(keys))
	require.Len(got, 1)
	assert.Equal("live pull request", got[WorkspaceSubjectKey{
		RepoID: repoID, ItemType: WorkspaceItemTypePullRequest, ItemNumber: 1,
	}].Title)
}
