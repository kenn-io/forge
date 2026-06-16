package projecttest

import (
	"context"
	"database/sql"
	"testing"
	"time"

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

// TestCreateProjectWorktreeConvergesByPath verifies registration is idempotent
// by path within a project — adopting an existing row (for example one the
// background discovery pass created) rather than conflicting — while still
// rejecting a path already owned by a different project.
func TestCreateProjectWorktreeConvergesByPath(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := context.Background()

	project, err := d.CreateProject(ctx, db.CreateProjectInput{
		DisplayName: "myrepo",
		LocalPath:   "/tmp/myrepo",
	})
	require.NoError(err)

	first, err := d.CreateProjectWorktree(ctx, db.CreateProjectWorktreeInput{
		ProjectID: project.ID,
		Branch:    "feature-x",
		Path:      "/tmp/wt",
	})
	require.NoError(err)

	adopted, err := d.CreateProjectWorktree(ctx, db.CreateProjectWorktreeInput{
		ProjectID: project.ID,
		Branch:    "feature-y",
		Path:      "/tmp/wt",
	})
	require.NoError(err)
	assert.Equal(first.ID, adopted.ID, "same path keeps the existing row id")
	assert.Equal("feature-y", adopted.Branch, "branch is refreshed on adoption")

	other, err := d.CreateProject(ctx, db.CreateProjectInput{
		DisplayName: "otherrepo",
		LocalPath:   "/tmp/otherrepo",
	})
	require.NoError(err)

	_, err = d.CreateProjectWorktree(ctx, db.CreateProjectWorktreeInput{
		ProjectID: other.ID,
		Branch:    "feature-z",
		Path:      "/tmp/wt",
	})
	assert.ErrorIs(err, db.ErrWorktreePathTaken, "cross-project path collision still conflicts")
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

func TestProjectWorktreeTmuxSessionRoundTrip(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := context.Background()

	worktree := createProjectWorktreeForTmuxTest(t, d, "/tmp/runtime-repo", "/tmp/runtime-wt")
	createdAt := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

	require.NoError(d.UpsertProjectWorktreeTmuxSession(ctx, &db.ProjectWorktreeTmuxSession{
		WorktreeID:  worktree.ID,
		SessionKey:  "wt_codex",
		SessionName: "middleman-project-worktree-codex",
		TargetKey:   "codex",
		CreatedAt:   createdAt,
	}))

	sessions, err := d.ListProjectWorktreeTmuxSessions(ctx, worktree.ID)
	require.NoError(err)
	require.Len(sessions, 1)
	assert.Equal(worktree.ID, sessions[0].WorktreeID)
	assert.Equal("wt_codex", sessions[0].SessionKey)
	assert.Equal("middleman-project-worktree-codex", sessions[0].SessionName)
	assert.Equal("codex", sessions[0].TargetKey)
	assert.Equal(createdAt, sessions[0].CreatedAt)
}

func TestProjectWorktreeTmuxSessionKeyedBySessionKey(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := context.Background()

	worktree := createProjectWorktreeForTmuxTest(t, d, "/tmp/runtime-repo-unique", "/tmp/runtime-wt-unique")
	first := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	second := first.Add(time.Minute)

	// Re-upserting the same session key refreshes the row in place.
	require.NoError(d.UpsertProjectWorktreeTmuxSession(ctx, &db.ProjectWorktreeTmuxSession{
		WorktreeID:  worktree.ID,
		SessionKey:  "wt_codex_1",
		SessionName: "middleman-project-worktree-codex-a",
		TargetKey:   "codex",
		CreatedAt:   first,
	}))
	require.NoError(d.UpsertProjectWorktreeTmuxSession(ctx, &db.ProjectWorktreeTmuxSession{
		WorktreeID:  worktree.ID,
		SessionKey:  "wt_codex_1",
		SessionName: "middleman-project-worktree-codex-b",
		TargetKey:   "codex",
		CreatedAt:   second,
	}))

	sessions, err := d.ListProjectWorktreeTmuxSessions(ctx, worktree.ID)
	require.NoError(err)
	require.Len(sessions, 1)
	assert.Equal("middleman-project-worktree-codex-b", sessions[0].SessionName)
	assert.Equal(second, sessions[0].CreatedAt)

	// A distinct session key for the same target is a separate row.
	require.NoError(d.UpsertProjectWorktreeTmuxSession(ctx, &db.ProjectWorktreeTmuxSession{
		WorktreeID:  worktree.ID,
		SessionKey:  "wt_codex_2",
		SessionName: "middleman-project-worktree-codex-c",
		TargetKey:   "codex",
		CreatedAt:   second.Add(time.Minute),
	}))
	sessions, err = d.ListProjectWorktreeTmuxSessions(ctx, worktree.ID)
	require.NoError(err)
	require.Len(sessions, 2)
}

func TestProjectWorktreeTmuxSessionForgetAndCascade(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := context.Background()

	project, err := d.CreateProject(ctx, db.CreateProjectInput{
		DisplayName: "runtime-repo-cascade",
		LocalPath:   "/tmp/runtime-repo-cascade",
	})
	require.NoError(err)
	worktree, err := d.CreateProjectWorktree(ctx, db.CreateProjectWorktreeInput{
		ProjectID: project.ID,
		Branch:    "runtime",
		Path:      "/tmp/runtime-wt-cascade",
	})
	require.NoError(err)

	require.NoError(d.UpsertProjectWorktreeTmuxSession(ctx, &db.ProjectWorktreeTmuxSession{
		WorktreeID:  worktree.ID,
		SessionKey:  "wt_cascade",
		SessionName: "middleman-project-worktree-cascade",
		TargetKey:   "codex",
		CreatedAt:   time.Now().UTC(),
	}))
	require.NoError(d.DeleteProjectWorktreeTmuxSession(ctx, worktree.ID, "wt_cascade"))

	sessions, err := d.ListProjectWorktreeTmuxSessions(ctx, worktree.ID)
	require.NoError(err)
	assert.Empty(sessions)

	require.NoError(d.UpsertProjectWorktreeTmuxSession(ctx, &db.ProjectWorktreeTmuxSession{
		WorktreeID:  worktree.ID,
		SessionKey:  "wt_cascade",
		SessionName: "middleman-project-worktree-cascade",
		TargetKey:   "codex",
		CreatedAt:   time.Now().UTC(),
	}))
	_, err = d.WriteDB().ExecContext(ctx,
		`DELETE FROM middleman_project_worktrees WHERE id = ?`, worktree.ID,
	)
	require.NoError(err)

	var count int
	err = d.ReadDB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM middleman_project_worktree_runtime_sessions WHERE worktree_id = ?`,
		worktree.ID,
	).Scan(&count)
	require.NoError(err)
	assert.Zero(count)
}

func createProjectWorktreeForTmuxTest(
	t *testing.T,
	d *db.DB,
	projectPath string,
	worktreePath string,
) *db.ProjectWorktree {
	t.Helper()
	ctx := context.Background()
	project, err := d.CreateProject(ctx, db.CreateProjectInput{
		DisplayName: "runtime-repo",
		LocalPath:   projectPath,
	})
	require.NoError(t, err)
	worktree, err := d.CreateProjectWorktree(ctx, db.CreateProjectWorktreeInput{
		ProjectID: project.ID,
		Branch:    "runtime",
		Path:      worktreePath,
	})
	require.NoError(t, err)
	return worktree
}
