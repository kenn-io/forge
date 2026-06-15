package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"go.kenn.io/middleman/internal/db"
)

// seedRepoWithWorktree creates a git repo with one empty commit and a single
// linked worktree on branch, returning the repo root and the worktree path.
func seedRepoWithWorktree(t *testing.T, branch, worktreeName string) (string, string) {
	t.Helper()
	repoDir := t.TempDir()
	runGit(t, repoDir, "init", "-q")
	runGit(t, repoDir, "-c", "user.email=t@e.st", "-c", "user.name=Tester",
		"commit", "--allow-empty", "-m", "init")
	wtDir := filepath.Join(t.TempDir(), worktreeName)
	runGit(t, repoDir, "worktree", "add", "-b", branch, wtDir)
	return repoDir, wtDir
}

func TestParseGitWorktreeList(t *testing.T) {
	require := require.New(t)
	out := "" +
		"worktree /repo\nHEAD abc123\nbranch refs/heads/main\n\n" +
		"worktree /repo/.wt/feature\nHEAD def456\nbranch refs/heads/feature/x\n\n" +
		"worktree /bare\nbare\n\n" +
		"worktree /repo/.wt/detached\nHEAD 9f9f9f9f9f9f9f\ndetached\n"

	entries := parseGitWorktreeList(out)
	require.Len(entries, 4)

	require.Equal("/repo", entries[0].path)
	require.Equal("main", entries[0].branch)
	require.False(entries[0].bare)

	require.Equal("/repo/.wt/feature", entries[1].path)
	require.Equal("feature/x", entries[1].branch, "refs/heads/ prefix is stripped")

	require.True(entries[2].bare)

	require.True(entries[3].detached)
	require.Empty(entries[3].branch)
}

func TestDiscoverProjectInventory_StandardRepoSurfacesLinkedWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	require := require.New(t)
	repoDir, wtDir := seedRepoWithWorktree(t, "feature/live", "wtA")

	inv, err := discoverProjectInventory(context.Background(), repoDir)
	require.NoError(err)
	require.Equal("standard", inv.RepositoryKind)
	require.Equal("main", inv.DefaultBranch)

	// The root checkout is a first-class worktree row (first, as git
	// reports it), followed by the linked worktree.
	require.Len(inv.Worktrees, 2)
	require.Equal("main", inv.Worktrees[0].Branch)
	require.Equal(filepath.Base(repoDir), filepath.Base(inv.Worktrees[0].Path))
	require.Equal("feature/live", inv.Worktrees[1].Branch)
	require.Equal(filepath.Base(wtDir), filepath.Base(inv.Worktrees[1].Path))
}

func TestDiscoverProjectInventory_BareRepoKind(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	require := require.New(t)
	bareDir := filepath.Join(t.TempDir(), "repo.git")
	require.NoError(os.MkdirAll(bareDir, 0o755))
	runGit(t, bareDir, "init", "--bare", "-q")

	inv, err := discoverProjectInventory(context.Background(), bareDir)
	require.NoError(err)
	require.Equal("bare", inv.RepositoryKind)
	require.Empty(inv.Worktrees)
}

func TestDiscoverProjectInventory_MissingRepoErrors(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	_, err := discoverProjectInventory(context.Background(), filepath.Join(t.TempDir(), "not-a-repo"))
	require.Error(t, err)
}

// TestRegisterProject_ImmediatelyDiscoversWorktrees verifies the register
// handler runs a discovery pass synchronously, so a project's linked worktrees
// and repository facts are present the moment registration returns.
func TestRegisterProject_ImmediatelyDiscoversWorktrees(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	require := require.New(t)
	ctx := context.Background()

	srv, database := setupTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	repoDir, _ := seedRepoWithWorktree(t, "feature/live", "wtB")

	body := mustMarshal(t, map[string]any{"local_path": repoDir})
	resp := httpDo(t, ts, http.MethodPost, "/api/v1/projects", body)
	require.Equal(http.StatusCreated, resp.StatusCode)
	var registered map[string]any
	require.NoError(json.NewDecoder(resp.Body).Decode(&registered))
	resp.Body.Close()
	projectID, _ := registered["id"].(string)
	require.NotEmpty(projectID)

	project, err := database.GetProjectByID(ctx, projectID)
	require.NoError(err)
	require.Equal("standard", project.RepositoryKind)
	require.Equal("main", project.DefaultBranch)
	require.False(project.IsStale)

	wts, err := database.ListProjectWorktrees(ctx, projectID)
	require.NoError(err)
	require.Len(wts, 2, "root checkout and linked worktree both get rows")
	branches := []string{wts[0].Branch, wts[1].Branch}
	require.ElementsMatch([]string{"main", "feature/live"}, branches)
	require.False(wts[0].IsStale)
	require.False(wts[1].IsStale)
}

// TestFleetWorktreeDiscoverer_StaleThenReappear verifies a removed worktree is
// marked stale on the next pass and clears when the path returns.
func TestFleetWorktreeDiscoverer_StaleThenReappear(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	require := require.New(t)
	ctx := context.Background()

	srv, database := setupTestServer(t)
	repoDir, wtDir := seedRepoWithWorktree(t, "feature/live", "wtC")

	project, err := database.CreateProject(ctx, db.CreateProjectInput{
		DisplayName: "stale-repo",
		LocalPath:   repoDir,
	})
	require.NoError(err)

	findByPath := func(rows []db.ProjectWorktree, path string) *db.ProjectWorktree {
		for i := range rows {
			if filepath.Base(rows[i].Path) == filepath.Base(path) {
				return &rows[i]
			}
		}
		return nil
	}

	srv.fleetWorktreeDiscoverer.refreshProject(ctx, project.ID, repoDir)
	live, err := database.ListProjectWorktrees(ctx, project.ID)
	require.NoError(err)
	require.Len(live, 2, "root row plus the linked worktree")
	linked := findByPath(live, wtDir)
	require.NotNil(linked)
	require.False(linked.IsStale)
	worktreeID := linked.ID

	runGit(t, repoDir, "worktree", "remove", wtDir)
	srv.fleetWorktreeDiscoverer.refreshProject(ctx, project.ID, repoDir)
	gone, err := database.ListProjectWorktrees(ctx, project.ID)
	require.NoError(err)
	require.Len(gone, 2, "removed worktree row is kept, not deleted")
	linked = findByPath(gone, wtDir)
	require.NotNil(linked)
	require.True(linked.IsStale)
	require.Equal(worktreeID, linked.ID)
	root := findByPath(gone, repoDir)
	require.NotNil(root)
	require.False(root.IsStale, "the root row never goes stale while the project exists")

	runGit(t, repoDir, "worktree", "add", wtDir, "feature/live")
	srv.fleetWorktreeDiscoverer.refreshProject(ctx, project.ID, repoDir)
	back, err := database.ListProjectWorktrees(ctx, project.ID)
	require.NoError(err)
	require.Len(back, 2)
	linked = findByPath(back, wtDir)
	require.NotNil(linked)
	require.False(linked.IsStale, "reappeared worktree clears its stale flag")
	require.Equal(worktreeID, linked.ID)
}
