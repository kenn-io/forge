package db

import (
	"context"
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

func TestListUnpreparedProviderWorkspacesUsesOneReadConnection(t *testing.T) {
	require := require.New(t)
	database := openTestDB(t)
	workspace, spec := workspaceLaunchFixture(t, database, "ws-unprepared-one-connection")
	require.NoError(database.CreateWorkspaceWithLaunchSpec(
		t.Context(), workspace, spec,
	))

	database.ReadDB().SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	unprepared, err := database.ListUnpreparedProviderWorkspacesAt(
		ctx, spec.SourceVisibleUntil,
	)

	require.NoError(err)
	require.Len(unprepared, 1)
	require.Equal(workspace.ID, unprepared[0].Workspace.ID)
	require.Equal("sourceVisibilityExpired", unprepared[0].Reason)
}

func TestCreateWorkspaceWithLaunchSpecRejectsCatalogIdentityMismatch(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	database := openTestDB(t)
	workspace, spec := workspaceLaunchFixture(t, database, "ws-catalog-mismatch")
	spec.Repository.PlatformRepoID = "replacement-repository"

	err := database.CreateWorkspaceWithLaunchSpec(t.Context(), workspace, spec)

	require.ErrorIs(err, ErrRepositoryRouteFenceChanged)
	stored, readErr := database.GetWorkspace(t.Context(), workspace.ID)
	require.NoError(readErr)
	require.Nil(stored)
}

func TestPutWorkspaceLaunchSpecRejectsCatalogIdentityMismatch(t *testing.T) {
	require := require.New(t)
	database := openTestDB(t)
	workspace, spec := workspaceLaunchFixture(t, database, "ws-put-mismatch")
	require.NoError(database.InsertWorkspace(t.Context(), workspace))
	spec.Repository.PlatformRepoID = "replacement-repository"

	err := database.PutWorkspaceLaunchSpec(t.Context(), workspace.ID, spec)

	require.ErrorIs(err, ErrRepositoryRouteFenceChanged)
	stored, readErr := database.GetWorkspaceLaunchSpec(t.Context(), workspace.ID)
	require.NoError(readErr)
	require.Nil(stored)
}

func TestRefreshWorkspaceLaunchSpecRejectsSameRouteIdentityMismatch(t *testing.T) {
	require := require.New(t)
	database := openTestDB(t)
	workspace, spec := workspaceLaunchFixture(t, database, "ws-refresh-mismatch")
	require.NoError(database.CreateWorkspaceWithLaunchSpec(t.Context(), workspace, spec))
	refreshed := spec
	refreshed.Repository.PlatformRepoID = "replacement-repository"
	refreshed.IssuedAt = spec.IssuedAt.Add(time.Minute)
	refreshed.SourceVisibleUntil = refreshed.IssuedAt.Add(WorkspaceLaunchSpecVisibilityLease)

	_, err := database.PutRefreshedWorkspaceLaunchSpec(
		t.Context(), workspace.ID, refreshed,
	)

	require.ErrorIs(err, ErrRepositoryRouteFenceChanged)
	stored, readErr := database.GetWorkspaceLaunchSpec(t.Context(), workspace.ID)
	require.NoError(readErr)
	require.NotNil(stored)
	require.Equal(spec.Repository.PlatformRepoID, stored.Repository.PlatformRepoID)
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
