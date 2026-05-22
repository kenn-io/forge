package projecttest

import (
	"context"
	"database/sql"
	"testing"

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/middleman/internal/db"
	"go.kenn.io/middleman/internal/testutil/dbtest"
)

func openTestDB(t *testing.T) *db.DB {
	t.Helper()
	return dbtest.Open(t)
}

func TestCreateProjectWithoutPlatformIdentity(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := context.Background()

	project, err := d.CreateProject(ctx, db.CreateProjectInput{
		DisplayName: "myrepo",
		LocalPath:   "/tmp/myrepo",
	})
	require.NoError(err)
	require.NotNil(project)

	assert.NotEmpty(project.ID)
	assert.Greater(len(project.ID), len("prj_"))
	assert.Equal("myrepo", project.DisplayName)
	assert.Equal("/tmp/myrepo", project.LocalPath)
	assert.Nil(project.PlatformIdentity)
	assert.False(project.CreatedAt.IsZero())

	roundTrip, err := d.GetProjectByID(ctx, project.ID)
	require.NoError(err)
	assert.Equal(project.ID, roundTrip.ID)
	assert.Nil(roundTrip.PlatformIdentity)
}

func TestCreateProjectLinkedToRepo(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := context.Background()

	repoID, err := d.UpsertRepo(ctx, db.GitHubRepoIdentity("github.com", "wesm", "examplerepo"))
	require.NoError(err)

	project, err := d.CreateProject(ctx, db.CreateProjectInput{
		DisplayName:   "examplerepo",
		LocalPath:     "/Users/example/code/examplerepo",
		RepoID:        sql.NullInt64{Int64: repoID, Valid: true},
		DefaultBranch: "main",
	})
	require.NoError(err)
	require.NotNil(project.PlatformIdentity)
	assert.Equal("github.com", project.PlatformIdentity.Host)
	assert.Equal("wesm", project.PlatformIdentity.Owner)
	assert.Equal("examplerepo", project.PlatformIdentity.Name)
	assert.Equal("main", project.DefaultBranch)

	// Re-fetching reads the joined identity off middleman_repos -
	// pin that the JOIN is the source of truth.
	roundTrip, err := d.GetProjectByID(ctx, project.ID)
	require.NoError(err)
	require.NotNil(roundTrip.PlatformIdentity)
	assert.Equal("github.com", roundTrip.PlatformIdentity.Host)
}

func TestCreateProjectFKSetNullOnRepoDelete(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := context.Background()

	repoID, err := d.UpsertRepo(ctx, db.GitHubRepoIdentity("github.com", "wesm", "examplerepo"))
	require.NoError(err)

	project, err := d.CreateProject(ctx, db.CreateProjectInput{
		DisplayName: "examplerepo",
		LocalPath:   "/tmp/examplerepo",
		RepoID:      sql.NullInt64{Int64: repoID, Valid: true},
	})
	require.NoError(err)
	require.NotNil(project.PlatformIdentity)

	// Deleting the repo row must null the project's FK rather than
	// cascade-delete the project. The on-disk checkout is the source
	// of truth for the project record.
	_, err = d.WriteDB().ExecContext(ctx,
		`DELETE FROM middleman_repos WHERE id = ?`, repoID,
	)
	require.NoError(err)

	after, err := d.GetProjectByID(ctx, project.ID)
	require.NoError(err)
	assert.Nil(after.PlatformIdentity,
		"identity must clear when the linked repo row is removed")
	assert.Equal(project.LocalPath, after.LocalPath,
		"project record itself must survive repo deletion")
}

func TestCreateProjectRejectsBlankRequiredFields(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := context.Background()

	_, err := d.CreateProject(ctx, db.CreateProjectInput{
		DisplayName: "",
		LocalPath:   "/tmp/x",
	})
	require.Error(err)
	assert.Contains(err.Error(), "display_name")

	_, err = d.CreateProject(ctx, db.CreateProjectInput{
		DisplayName: "ok",
		LocalPath:   "",
	})
	require.Error(err)
	assert.Contains(err.Error(), "local_path")
}

func TestCreateProjectDuplicateLocalPath(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := context.Background()

	_, err := d.CreateProject(ctx, db.CreateProjectInput{
		DisplayName: "first",
		LocalPath:   "/tmp/repo",
	})
	require.NoError(err)

	_, err = d.CreateProject(ctx, db.CreateProjectInput{
		DisplayName: "second",
		LocalPath:   "/tmp/repo",
	})
	require.Error(err)
	assert.ErrorIs(err, db.ErrProjectPathTaken)
}

func TestGetProjectByIDNotFound(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := context.Background()

	_, err := d.GetProjectByID(ctx, "prj_doesnotexist")
	require.Error(err)
	assert.ErrorIs(err, db.ErrProjectNotFound)
}

func TestGetProjectByLocalPath(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := context.Background()

	created, err := d.CreateProject(ctx, db.CreateProjectInput{
		DisplayName: "myrepo",
		LocalPath:   "/tmp/myrepo",
	})
	require.NoError(err)

	found, err := d.GetProjectByLocalPath(ctx, "/tmp/myrepo")
	require.NoError(err)
	assert.Equal(created.ID, found.ID)

	_, err = d.GetProjectByLocalPath(ctx, "/tmp/nope")
	assert.ErrorIs(err, db.ErrProjectNotFound)
}

func TestListProjectsOrdersByDisplayName(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := context.Background()

	for _, p := range []db.CreateProjectInput{
		{DisplayName: "Zeta", LocalPath: "/tmp/zeta"},
		{DisplayName: "alpha", LocalPath: "/tmp/alpha"},
		{DisplayName: "Mu", LocalPath: "/tmp/mu"},
	} {
		_, err := d.CreateProject(ctx, p)
		require.NoError(err)
	}

	listed, err := d.ListProjects(ctx)
	require.NoError(err)
	require.Len(listed, 3)
	assert.Equal("alpha", listed[0].DisplayName)
	assert.Equal("Mu", listed[1].DisplayName)
	assert.Equal("Zeta", listed[2].DisplayName)
}

func TestCreateProjectWorktreeRoundTrip(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := context.Background()

	project, err := d.CreateProject(ctx, db.CreateProjectInput{
		DisplayName: "myrepo",
		LocalPath:   "/tmp/myrepo",
	})
	require.NoError(err)

	worktree, err := d.CreateProjectWorktree(ctx, db.CreateProjectWorktreeInput{
		ProjectID: project.ID,
		Branch:    "feature-x",
		Path:      "/tmp/myrepo-worktrees/feature-x",
	})
	require.NoError(err)
	assert.Greater(len(worktree.ID), len("wtr_"))
	assert.Equal(project.ID, worktree.ProjectID)
	assert.Equal("feature-x", worktree.Branch)

	roundTrip, err := d.GetProjectWorktreeByID(ctx, worktree.ID)
	require.NoError(err)
	assert.Equal(worktree.ID, roundTrip.ID)
}

func TestCreateProjectWorktreeRejectsUnknownProject(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := context.Background()

	_, err := d.CreateProjectWorktree(ctx, db.CreateProjectWorktreeInput{
		ProjectID: "prj_doesnotexist",
		Branch:    "feature-x",
		Path:      "/tmp/x",
	})
	require.Error(err)
	assert.ErrorIs(err, db.ErrProjectNotFound)
}

func TestCreateProjectWorktreeRejectsDuplicatePath(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := context.Background()

	project, err := d.CreateProject(ctx, db.CreateProjectInput{
		DisplayName: "myrepo",
		LocalPath:   "/tmp/myrepo",
	})
	require.NoError(err)

	_, err = d.CreateProjectWorktree(ctx, db.CreateProjectWorktreeInput{
		ProjectID: project.ID,
		Branch:    "feature-x",
		Path:      "/tmp/wt",
	})
	require.NoError(err)

	_, err = d.CreateProjectWorktree(ctx, db.CreateProjectWorktreeInput{
		ProjectID: project.ID,
		Branch:    "feature-y",
		Path:      "/tmp/wt",
	})
	require.Error(err)
	assert.ErrorIs(err, db.ErrWorktreePathTaken)
}

func TestListProjectWorktreesScopedToProject(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := context.Background()

	a, err := d.CreateProject(ctx, db.CreateProjectInput{
		DisplayName: "a", LocalPath: "/tmp/a",
	})
	require.NoError(err)
	b, err := d.CreateProject(ctx, db.CreateProjectInput{
		DisplayName: "b", LocalPath: "/tmp/b",
	})
	require.NoError(err)

	for _, in := range []db.CreateProjectWorktreeInput{
		{ProjectID: a.ID, Branch: "wip", Path: "/tmp/a-wt-1"},
		{ProjectID: a.ID, Branch: "wip2", Path: "/tmp/a-wt-2"},
		{ProjectID: b.ID, Branch: "wip", Path: "/tmp/b-wt-1"},
	} {
		_, err := d.CreateProjectWorktree(ctx, in)
		require.NoError(err)
	}

	aList, err := d.ListProjectWorktrees(ctx, a.ID)
	require.NoError(err)
	assert.Len(aList, 2)

	bList, err := d.ListProjectWorktrees(ctx, b.ID)
	require.NoError(err)
	assert.Len(bList, 1)

	_, err = d.ListProjectWorktrees(ctx, "prj_doesnotexist")
	assert.ErrorIs(err, db.ErrProjectNotFound)
}

func TestProjectWorktreeCascadesOnProjectDelete(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := context.Background()

	project, err := d.CreateProject(ctx, db.CreateProjectInput{
		DisplayName: "myrepo", LocalPath: "/tmp/myrepo",
	})
	require.NoError(err)
	_, err = d.CreateProjectWorktree(ctx, db.CreateProjectWorktreeInput{
		ProjectID: project.ID, Branch: "wip", Path: "/tmp/wt",
	})
	require.NoError(err)

	_, err = d.WriteDB().ExecContext(ctx,
		`DELETE FROM middleman_projects WHERE id = ?`, project.ID,
	)
	require.NoError(err)

	var count int
	err = d.ReadDB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM middleman_project_worktrees WHERE project_id = ?`,
		project.ID,
	).Scan(&count)
	require.NoError(err)
	assert.Zero(count)
}
