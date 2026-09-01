package db

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func workspaceLaunchFixture(t *testing.T, database *DB, id string) (*Workspace, WorkspaceLaunchSpec) {
	t.Helper()
	repoID := insertTestRepo(t, database, "acme", "widget")
	require.NoError(t, database.UpdateRepoProviderMetadata(t.Context(), repoID, RepoProviderMetadata{
		PlatformRepoID: verifiedTestRepoIdentity("github", "github.com", "acme", "widget").PlatformRepoID,
		CloneURL:       "https://github.com/acme/widget.git", DefaultBranch: "main",
	}))
	issuedAt := time.Date(2026, 8, 22, 12, 0, 0, 123456000, time.UTC)
	workspace := &Workspace{
		ID: id, Platform: "github", PlatformHost: "github.com",
		RepoOwner: "acme", RepoName: "widget",
		ItemType: WorkspaceItemTypePullRequest, ItemNumber: 7,
		ItemKey: "7", GitHeadRef: "feature/seven", WorkspaceBranch: "feature/seven",
		WorktreePath: "/tmp/" + id, TmuxSession: id, Status: "ready",
	}
	spec := WorkspaceLaunchSpec{
		Version: WorkspaceLaunchSpecVersion,
		Repository: WorkspaceLaunchRepository{
			Provider: "github", PlatformHost: "github.com",
			PlatformRepoID: verifiedTestRepoIdentity("github", "github.com", "acme", "widget").PlatformRepoID,
			Owner:          "acme", Name: "widget",
			CloneURL: "https://github.com/acme/widget.git", DefaultBranch: "main",
		},
		ItemType: WorkspaceItemTypePullRequest, ItemNumber: 7,
		ItemKey: "7", GitHeadRef: "feature/seven",
		Pull: &WorkspaceLaunchPull{
			HeadBranch: "feature/seven", HeadRepoKind: "same_repo", SnapshotRevision: 1,
		},
		SourceVisible: true, IssuedAt: issuedAt,
		SourceVisibleUntil: issuedAt.Add(WorkspaceLaunchSpecVisibilityLease),
	}
	return workspace, spec
}

func TestWorkspaceAndLaunchSpecPersistAtomically(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := openTestDB(t)
	workspace, spec := workspaceLaunchFixture(t, database, "ws-atomic")
	spec.Repository.PlatformRepoID = ""
	err := database.CreateWorkspaceWithLaunchSpec(t.Context(), workspace, spec)
	require.Error(err)
	stored, readErr := database.GetWorkspace(t.Context(), workspace.ID)
	require.NoError(readErr)
	assert.Nil(stored)
	storedSpec, readErr := database.GetWorkspaceLaunchSpec(t.Context(), workspace.ID)
	require.NoError(readErr)
	assert.Nil(storedSpec)
}

func TestWorkspaceLaunchSpecRoundTripsCanonicalUTCTimestamps(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := openTestDB(t)
	workspace, spec := workspaceLaunchFixture(t, database, "ws-roundtrip")
	require.NoError(database.CreateWorkspaceWithLaunchSpec(t.Context(), workspace, spec))
	got, err := database.GetWorkspaceLaunchSpec(t.Context(), workspace.ID)
	require.NoError(err)
	require.NotNil(got)
	assert.Equal(spec.IssuedAt, got.IssuedAt)
	assert.Equal(spec.SourceVisibleUntil, got.SourceVisibleUntil)

	// Reading is self-contained: provider rows can disappear without becoming
	// a runtime fallback dependency.
	_, err = database.WriteDB().ExecContext(t.Context(), `DELETE FROM forge_merge_requests`)
	require.NoError(err)
	got, err = database.GetWorkspaceLaunchSpec(t.Context(), workspace.ID)
	require.NoError(err)
	assert.Equal(spec.Repository.PlatformRepoID, got.Repository.PlatformRepoID)
}

func TestHistoricalWorkspaceRepositoryIdentityRejectsReusedRoute(t *testing.T) {
	database := openTestDB(t)
	seedRepositoryCatalogCollision(t, database)
	platformRepoID, err := database.ResolveUnambiguousHistoricalWorkspaceRepoID(
		t.Context(), "github", "github.com", "org-a", "project-a",
	)
	require.NoError(t, err)
	assert.Empty(t, platformRepoID)
}
