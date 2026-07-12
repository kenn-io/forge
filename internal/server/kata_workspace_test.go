package server

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/db"
	"go.kenn.io/middleman/internal/workspace"
)

func TestKataWorkspaceTargetAutomaticMapping(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	cloneDir := t.TempDir()
	require.NoError(os.WriteFile(
		filepath.Join(cloneDir, ".kata.toml"),
		[]byte("[project]\nuid = \"project-kata\"\n"),
		0o644,
	))
	cfg := fmt.Sprintf(`
sync_interval = "5m"
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "widget"
worktree_base_path = %q
`, cloneDir)
	srv, database, _ := setupTestServerWithConfigContent(t, cfg, &mockGH{})
	_, err := database.UpsertRepo(t.Context(), db.GitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)

	resp := kataWorkspaceTargetViaTaskDetail(t, srv, map[string]string{
		"daemon_id":    "desktop",
		"project_uid":  "project-kata",
		"project_name": "Kata",
		"issue_uid":    "issue-kata-1",
		"short_id":     "task-123",
		"qualified_id": "Kata#task-123",
		"title":        "Fix widget",
	})
	assert.True(resp.Available)
	require.NotNil(resp.Repo)
	assert.Equal("github", resp.Repo.Provider)
	assert.Equal("github.com", resp.Repo.PlatformHost)
	assert.Equal("acme", resp.Repo.Owner)
	assert.Equal("widget", resp.Repo.Name)
	assert.Equal(db.WorkspaceItemTypeKataTask, resp.ItemType)
	assert.Equal(db.KataWorkspaceItemKey(db.WorkspaceKataMetadata{
		DaemonID:   "desktop",
		ProjectUID: "project-kata",
		IssueUID:   "issue-kata-1",
	}), resp.ItemKey)
	assert.Nil(resp.ExistingWorkspace)
}

func TestKataWorkspaceTargetAutomaticMappingFromProjectIdentity(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	cloneDir := t.TempDir()
	require.NoError(os.WriteFile(
		filepath.Join(cloneDir, ".kata.toml"),
		[]byte("[project]\nidentity = \"github.com/acme/widget\"\n"),
		0o644,
	))
	cfg := fmt.Sprintf(`
sync_interval = "5m"
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "widget"
worktree_base_path = %q
`, cloneDir)
	srv, database, _ := setupTestServerWithConfigContent(t, cfg, &mockGH{})
	_, err := database.UpsertRepo(t.Context(), db.GitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)

	resp := kataWorkspaceTargetViaTaskDetail(t, srv, map[string]string{
		"daemon_id":    "desktop",
		"project_uid":  "github.com/acme/widget",
		"project_name": "widget",
		"issue_uid":    "issue-kata-1",
		"short_id":     "task-123",
		"title":        "Fix widget",
	})
	assert.True(resp.Available)
	require.NotNil(resp.Repo)
	assert.Equal("acme", resp.Repo.Owner)
	assert.Equal("widget", resp.Repo.Name)
}

func TestKataWorkspaceTargetAutomaticMappingFromProjectIdentityWhenUIDPresent(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	cloneDir := t.TempDir()
	require.NoError(os.WriteFile(
		filepath.Join(cloneDir, ".kata.toml"),
		[]byte("[project]\nuid = \"project-local\"\nidentity = \"project-kata\"\nname = \"Widget\"\n"),
		0o644,
	))
	cfg := fmt.Sprintf(`
sync_interval = "5m"
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "widget"
worktree_base_path = %q
`, cloneDir)
	srv, database, _ := setupTestServerWithConfigContent(t, cfg, &mockGH{})
	_, err := database.UpsertRepo(t.Context(), db.GitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)

	resp := kataWorkspaceTargetViaTaskDetail(t, srv, map[string]string{
		"daemon_id":    "desktop",
		"project_uid":  "project-kata",
		"project_name": "widget",
		"issue_uid":    "issue-kata-1",
	})
	assert.True(resp.Available)
	require.NotNil(resp.Repo)
	assert.Equal("acme", resp.Repo.Owner)
	assert.Equal("widget", resp.Repo.Name)
}

func TestKataWorkspaceTargetAutomaticMappingFromProjectName(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	cloneDir := t.TempDir()
	require.NoError(os.WriteFile(
		filepath.Join(cloneDir, ".kata.toml"),
		[]byte("[project]\nname = \"Widget\"\n"),
		0o644,
	))
	cfg := fmt.Sprintf(`
sync_interval = "5m"
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "widget"
worktree_base_path = %q
`, cloneDir)
	srv, database, _ := setupTestServerWithConfigContent(t, cfg, &mockGH{})
	_, err := database.UpsertRepo(t.Context(), db.GitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)

	resp := kataWorkspaceTargetViaTaskDetail(t, srv, map[string]string{
		"daemon_id":    "desktop",
		"project_uid":  "project-kata-opaque",
		"project_name": "widget",
		"issue_uid":    "issue-kata-1",
	})
	assert.True(resp.Available)
	require.NotNil(resp.Repo)
	assert.Equal("acme", resp.Repo.Owner)
	assert.Equal("widget", resp.Repo.Name)
	assert.Equal(db.KataWorkspaceItemKey(db.WorkspaceKataMetadata{
		DaemonID:   "desktop",
		ProjectUID: "project-kata-opaque",
		IssueUID:   "issue-kata-1",
	}), resp.ItemKey)
}

func TestKataWorkspaceTargetUnavailableWhenProjectNameMatchesButIdentifierDiffers(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	cloneDir := t.TempDir()
	require.NoError(os.WriteFile(
		filepath.Join(cloneDir, ".kata.toml"),
		[]byte("[project]\nuid = \"other-project\"\nname = \"Widget\"\n"),
		0o644,
	))
	cfg := fmt.Sprintf(`
sync_interval = "5m"
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "widget"
worktree_base_path = %q
`, cloneDir)
	srv, database, _ := setupTestServerWithConfigContent(t, cfg, &mockGH{})
	_, err := database.UpsertRepo(t.Context(), db.GitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)

	resp := kataWorkspaceTargetViaTaskDetail(t, srv, map[string]string{
		"daemon_id":    "desktop",
		"project_uid":  "project-kata",
		"project_name": "Widget",
		"issue_uid":    "issue-kata-1",
	})
	assert.False(resp.Available)
	assert.Nil(resp.Repo)
}

func TestKataWorkspaceTargetNameFallbackResolvesWithUnrelatedIdentityClone(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	nameOnlyClone := t.TempDir()
	identityClone := t.TempDir()
	require.NoError(os.WriteFile(
		filepath.Join(nameOnlyClone, ".kata.toml"),
		[]byte("[project]\nname = \"Widget\"\n"),
		0o644,
	))
	require.NoError(os.WriteFile(
		filepath.Join(identityClone, ".kata.toml"),
		[]byte("[project]\nuid = \"other-project\"\nname = \"Other\"\n"),
		0o644,
	))
	cfg := fmt.Sprintf(`
sync_interval = "5m"
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "widget"
worktree_base_path = %q

[[repos]]
owner = "acme"
name = "other"
worktree_base_path = %q
`, nameOnlyClone, identityClone)
	srv, database, _ := setupTestServerWithConfigContent(t, cfg, &mockGH{})
	_, err := database.UpsertRepo(t.Context(), db.GitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	_, err = database.UpsertRepo(t.Context(), db.GitHubRepoIdentity("github.com", "acme", "other"))
	require.NoError(err)

	resp := kataWorkspaceTargetViaTaskDetail(t, srv, map[string]string{
		"daemon_id":    "desktop",
		"project_uid":  "project-kata-opaque",
		"project_name": "Widget",
		"issue_uid":    "issue-kata-1",
	})
	assert.True(resp.Available)
	require.NotNil(resp.Repo)
	assert.Equal("acme", resp.Repo.Owner)
	assert.Equal("widget", resp.Repo.Name)
}

func TestKataWorkspaceTargetAutomaticMappingFromTrackedRepoName(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	srv, database, _ := setupTestServerWithConfigContent(t, `
sync_interval = "5m"
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "middleman"
`, &mockGH{})
	_, err := database.UpsertRepo(t.Context(), db.GitHubRepoIdentity("github.com", "acme", "middleman"))
	require.NoError(err)

	resp := kataWorkspaceTargetViaTaskDetail(t, srv, map[string]string{
		"daemon_id":    "desktop",
		"project_uid":  "project-middleman",
		"project_name": "middleman",
		"issue_uid":    "issue-kata-1",
		"short_id":     "task-123",
		"title":        "Fix workspace affordance",
	})
	assert.True(resp.Available)
	require.NotNil(resp.Repo)
	assert.Equal("github", resp.Repo.Provider)
	assert.Equal("github.com", resp.Repo.PlatformHost)
	assert.Equal("acme", resp.Repo.Owner)
	assert.Equal("middleman", resp.Repo.Name)
	assert.Equal(db.WorkspaceItemTypeKataTask, resp.ItemType)
	assert.Equal(db.KataWorkspaceItemKey(db.WorkspaceKataMetadata{
		DaemonID:   "desktop",
		ProjectUID: "project-middleman",
		IssueUID:   "issue-kata-1",
	}), resp.ItemKey)
}

func TestKataWorkspaceTargetAutomaticMappingFromRegisteredProject(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	srv, database, _ := setupTestServerWithConfigContent(t, `
sync_interval = "5m"
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091
`, &mockGH{})
	repoID, err := database.UpsertRepo(t.Context(), db.GitHubRepoIdentity("github.com", "acme", "kata"))
	require.NoError(err)
	projectPath := t.TempDir()
	_, err = database.CreateProject(t.Context(), db.CreateProjectInput{
		DisplayName: "Kata",
		LocalPath:   projectPath,
		RepoID:      sql.NullInt64{Int64: repoID, Valid: true},
	})
	require.NoError(err)

	resp := kataWorkspaceTargetViaTaskDetail(t, srv, map[string]string{
		"daemon_id":    "desktop",
		"project_uid":  "project-kata",
		"project_name": "Kata",
		"issue_uid":    "issue-kata-1",
	})

	assert.True(resp.Available)
	require.NotNil(resp.Repo)
	assert.Equal("acme", resp.Repo.Owner)
	assert.Equal("kata", resp.Repo.Name)
	basePath, ok, err := srv.worktreeBasePathForRepo(
		t.Context(), "github", "github.com", "acme", "kata",
	)
	require.NoError(err)
	assert.True(ok)
	assert.Equal(projectPath, basePath)
}

func TestKataWorkspaceTargetRegisteredProjectsWithDifferentCheckoutsAreAmbiguous(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	srv, database, _ := setupTestServerWithConfigContent(t, `
sync_interval = "5m"
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091
`, &mockGH{})
	repoID, err := database.UpsertRepo(t.Context(), db.GitHubRepoIdentity("github.com", "acme", "kata"))
	require.NoError(err)
	for _, localPath := range []string{t.TempDir(), t.TempDir()} {
		_, err = database.CreateProject(t.Context(), db.CreateProjectInput{
			DisplayName: "Kata",
			LocalPath:   localPath,
			RepoID:      sql.NullInt64{Int64: repoID, Valid: true},
		})
		require.NoError(err)
	}

	resp := kataWorkspaceTargetViaTaskDetail(t, srv, map[string]string{
		"daemon_id":    "desktop",
		"project_uid":  "project-kata",
		"project_name": "Kata",
		"issue_uid":    "issue-kata-1",
	})

	assert.False(resp.Available)
	assert.Nil(resp.Repo)
}

func TestCreateKataWorkspaceUsesRegisteredProjectCheckout(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	projectPath, _, platformHost := setupHTTPWorktreeBaseForServerTest(t, "feature")

	srv, database, _ := setupTestServerWithConfigContent(t, `
sync_interval = "5m"
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091
`, &mockGH{})
	repoID, err := database.UpsertRepo(t.Context(), db.GitHubRepoIdentity(platformHost, "acme", "widget"))
	require.NoError(err)
	_, err = database.CreateProject(t.Context(), db.CreateProjectInput{
		DisplayName: "Widget",
		LocalPath:   projectPath,
		RepoID:      sql.NullInt64{Int64: repoID, Valid: true},
	})
	require.NoError(err)
	srv.workspaces = workspace.NewManager(database, t.TempDir())
	srv.workspaces.SetTmuxCommand([]string{"sh", "-c", "exit 0"})

	rr := doJSON(t, srv, http.MethodPost, "/api/v1/kata/workspaces", map[string]any{
		"daemon_id":    "desktop",
		"project_uid":  "project-kata",
		"project_name": "Widget",
		"issue_uid":    "issue-kata-1",
		"short_id":     "task-123",
		"title":        "Use registered checkout",
	})
	require.Equal(http.StatusAccepted, rr.Code, rr.Body.String())
	var created workspaceResponse
	require.NoError(json.NewDecoder(rr.Body).Decode(&created))

	var stored *db.Workspace
	require.Eventually(func() bool {
		stored, err = database.GetWorkspace(t.Context(), created.ID)
		return err == nil && stored != nil && stored.Status == "ready"
	}, 10*time.Second, 25*time.Millisecond)
	commonDir := strings.TrimSpace(gitOutput(t, stored.WorktreePath, "rev-parse", "--git-common-dir"))
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(stored.WorktreePath, commonDir)
	}
	commonDir, err = filepath.EvalSymlinks(commonDir)
	require.NoError(err)
	wantCommonDir, err := filepath.EvalSymlinks(filepath.Join(projectPath, ".git"))
	require.NoError(err)
	assert.Equal(wantCommonDir, commonDir)
}

func TestKataManualMappingRejectsHistoricalUnconfiguredRepo(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	srv, database, _ := setupTestServerWithConfigContent(t, `
sync_interval = "5m"
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[kata_projects]]
project_uid = "project-kata"
provider = "github"
platform_host = "github.com"
repo_path = "acme/old"
`, &mockGH{})
	_, err := database.UpsertRepo(t.Context(), db.GitHubRepoIdentity("github.com", "acme", "old"))
	require.NoError(err)

	resolution, err := srv.resolveKataWorkspaceRepoResolution(t.Context(), db.WorkspaceKataMetadata{
		DaemonID: "desktop", ProjectUID: "project-kata", ProjectName: "Kata",
	})
	require.NoError(err)
	assert.Equal("invalid", resolution.Status)
	assert.Equal("manual_global", resolution.Source)
}

func TestKataMappingTargetsIncludeConfiguredRepositories(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	srv, database, _ := setupTestServerWithConfigContent(t, `
sync_interval = "5m"
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "exact"

[[repos]]
owner = "acme"
name = "glob-*"
`, &mockGH{})
	_, err := database.UpsertRepo(t.Context(), db.GitHubRepoIdentity("github.com", "acme", "glob-match"))
	require.NoError(err)

	targets, err := srv.kataMappingTargets(t.Context())
	require.NoError(err)
	require.Len(targets, 2)
	assert.Equal("acme/exact", targets[0].Repo.RepoPath)
	assert.Equal("acme/glob-match", targets[1].Repo.RepoPath)
}

func TestKataProjectMappingsReportsManualRegisteredProjectTarget(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	daemon := startKataTaskDetailDaemon(t, `{}`, `{"projects":[
		{"id":7,"uid":"project-kata","name":"Kata"},
		{"id":8,"uid":"project-unmapped","name":"Unmapped"}
	]}`)
	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	writeKataProxyCatalog(t, home, `
[[daemon]]
name = "desktop"
url = "`+daemon.URL+`"
`)
	srv, database, _ := setupTestServerWithConfigContent(t, `
sync_interval = "5m"
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[kata_projects]]
daemon_id = "desktop"
project_uid = "project-kata"
provider = "github"
platform_host = "github.com"
repo_path = "acme/middleman"
`, &mockGH{})
	repoID, err := database.UpsertRepo(t.Context(), db.GitHubRepoIdentity("github.com", "acme", "middleman"))
	require.NoError(err)
	_, err = database.CreateProject(t.Context(), db.CreateProjectInput{
		DisplayName: "Middleman",
		LocalPath:   t.TempDir(),
		RepoID:      sql.NullInt64{Int64: repoID, Valid: true},
	})
	require.NoError(err)

	rr := doJSON(t, srv, http.MethodGet, "/api/v1/kata/project-mappings", nil)
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	assert.Contains(rr.Header().Values("Vary"), kataDaemonHeaderName)
	var resp kataProjectMappingsResponse
	require.NoError(json.NewDecoder(rr.Body).Decode(&resp))
	require.Len(resp.Projects, 2)
	assert.Equal("mapped", resp.Projects[0].Status)
	assert.Equal("manual_daemon", resp.Projects[0].Source)
	require.NotNil(resp.Projects[0].Repo)
	assert.Equal("acme/middleman", resp.Projects[0].Repo.RepoPath)
	assert.Equal("unmapped", resp.Projects[1].Status)
	require.Len(resp.Targets, 1)
	assert.Equal("Middleman", resp.Targets[0].DisplayName)
	assert.Equal("acme/middleman", resp.Targets[0].Repo.RepoPath)
}

func TestKataWorkspaceTargetTrackedRepoNameFallbackRequiresOneMatch(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	srv, database, _ := setupTestServerWithConfigContent(t, `
sync_interval = "5m"
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "middleman"

[[repos]]
owner = "forks"
name = "middleman"
`, &mockGH{})
	_, err := database.UpsertRepo(t.Context(), db.GitHubRepoIdentity("github.com", "acme", "middleman"))
	require.NoError(err)
	_, err = database.UpsertRepo(t.Context(), db.GitHubRepoIdentity("github.com", "forks", "middleman"))
	require.NoError(err)

	resp := kataWorkspaceTargetViaTaskDetail(t, srv, map[string]string{
		"daemon_id":    "desktop",
		"project_uid":  "project-middleman",
		"project_name": "middleman",
		"issue_uid":    "issue-kata-1",
	})
	assert.False(resp.Available)
	assert.Nil(resp.Repo)
}

func TestKataWorkspaceTargetTrackedRepoNameFallbackRequiresTrackedRepo(t *testing.T) {
	assert := assert.New(t)

	srv, _, _ := setupTestServerWithConfigContent(t, `
sync_interval = "5m"
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "middleman"
`, &mockGH{})

	resp := kataWorkspaceTargetViaTaskDetail(t, srv, map[string]string{
		"daemon_id":    "desktop",
		"project_uid":  "project-middleman",
		"project_name": "middleman",
		"issue_uid":    "issue-kata-1",
	})
	assert.False(resp.Available)
	assert.Nil(resp.Repo)
}

func TestKataWorkspaceTargetAutomaticMappingFromGlobTrackedRepoName(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	srv, database, _ := setupTestServerWithConfigContent(t, `
sync_interval = "5m"
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "middle*"
`, &mockGH{})
	_, err := database.UpsertRepo(t.Context(), db.GitHubRepoIdentity("github.com", "acme", "middleman"))
	require.NoError(err)
	_, err = database.UpsertRepo(t.Context(), db.GitHubRepoIdentity("github.com", "acme", "middle-earth"))
	require.NoError(err)

	resp := kataWorkspaceTargetViaTaskDetail(t, srv, map[string]string{
		"daemon_id":    "desktop",
		"project_uid":  "project-middleman",
		"project_name": "middleman",
		"issue_uid":    "issue-kata-1",
	})
	assert.True(resp.Available)
	require.NotNil(resp.Repo)
	assert.Equal("github", resp.Repo.Provider)
	assert.Equal("github.com", resp.Repo.PlatformHost)
	assert.Equal("acme", resp.Repo.Owner)
	assert.Equal("middleman", resp.Repo.Name)
	assert.Equal(db.WorkspaceItemTypeKataTask, resp.ItemType)
}

func TestKataWorkspaceTargetGlobTrackedRepoNameFallbackRequiresOneMatch(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	srv, database, _ := setupTestServerWithConfigContent(t, `
sync_interval = "5m"
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "middle*"

[[repos]]
owner = "forks"
name = "middle*"
`, &mockGH{})
	_, err := database.UpsertRepo(t.Context(), db.GitHubRepoIdentity("github.com", "acme", "middleman"))
	require.NoError(err)
	_, err = database.UpsertRepo(t.Context(), db.GitHubRepoIdentity("github.com", "forks", "middleman"))
	require.NoError(err)

	resp := kataWorkspaceTargetViaTaskDetail(t, srv, map[string]string{
		"daemon_id":    "desktop",
		"project_uid":  "project-middleman",
		"project_name": "middleman",
		"issue_uid":    "issue-kata-1",
	})
	assert.False(resp.Available)
	assert.Nil(resp.Repo)
}

func TestKataWorkspaceTargetTrackedRepoNameFallbackCombinesExactAndGlobMatches(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	srv, database, _ := setupTestServerWithConfigContent(t, `
sync_interval = "5m"
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "middleman"

[[repos]]
owner = "forks"
name = "middle*"
`, &mockGH{})
	_, err := database.UpsertRepo(t.Context(), db.GitHubRepoIdentity("github.com", "acme", "middleman"))
	require.NoError(err)
	_, err = database.UpsertRepo(t.Context(), db.GitHubRepoIdentity("github.com", "forks", "middleman"))
	require.NoError(err)

	resp := kataWorkspaceTargetViaTaskDetail(t, srv, map[string]string{
		"daemon_id":    "desktop",
		"project_uid":  "project-middleman",
		"project_name": "middleman",
		"issue_uid":    "issue-kata-1",
	})
	assert.False(resp.Available)
	assert.Nil(resp.Repo)
}

func TestKataWorkspaceTargetAutomaticMappingFromProjectUIDWhenIdentityPresent(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	cloneDir := t.TempDir()
	require.NoError(os.WriteFile(
		filepath.Join(cloneDir, ".kata.toml"),
		[]byte("[project]\nuid = \"project-kata\"\nidentity = \"github.com/acme/widget\"\nname = \"Widget\"\n"),
		0o644,
	))
	cfg := fmt.Sprintf(`
sync_interval = "5m"
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "widget"
worktree_base_path = %q
`, cloneDir)
	srv, database, _ := setupTestServerWithConfigContent(t, cfg, &mockGH{})
	_, err := database.UpsertRepo(t.Context(), db.GitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)

	resp := kataWorkspaceTargetViaTaskDetail(t, srv, map[string]string{
		"daemon_id":    "desktop",
		"project_uid":  "project-kata",
		"project_name": "widget",
		"issue_uid":    "issue-kata-1",
	})
	assert.True(resp.Available)
	require.NotNil(resp.Repo)
	assert.Equal("acme", resp.Repo.Owner)
	assert.Equal("widget", resp.Repo.Name)
}

func TestReadKataProjectTOMLRejectsSymlink(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	dir := t.TempDir()
	// The symlink points at a perfectly valid TOML file: without the
	// regular-file guard os.ReadFile would happily follow it, which is the
	// vector for pointing .kata.toml at /dev/zero or another huge file.
	target := filepath.Join(dir, "payload.toml")
	require.NoError(os.WriteFile(target, []byte("[project]\nuid = \"project-kata\"\n"), 0o644))
	require.NoError(os.Symlink(target, filepath.Join(dir, ".kata.toml")))

	_, ok := readKataProjectTOML(dir)
	assert.False(ok, "symlinked .kata.toml must not be read")
}

func TestReadKataProjectTOMLRejectsOversizedFile(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	dir := t.TempDir()
	// Valid TOML that simply exceeds the cap, so rejection can only come from
	// the size guard and not from a decode failure.
	oversized := "[project]\nuid = \"" + strings.Repeat("a", maxKataProjectTOMLBytes) + "\"\n"
	require.NoError(os.WriteFile(filepath.Join(dir, ".kata.toml"), []byte(oversized), 0o644))

	_, ok := readKataProjectTOML(dir)
	assert.False(ok, "oversized .kata.toml must be rejected")
}

func TestReadKataProjectTOMLReadsRegularFile(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	dir := t.TempDir()
	require.NoError(os.WriteFile(
		filepath.Join(dir, ".kata.toml"),
		[]byte("[project]\nuid = \"project-kata\"\nidentity = \"github.com/acme/widget\"\nname = \"Widget\"\n"),
		0o644,
	))

	project, ok := readKataProjectTOML(dir)
	require.True(ok)
	assert.Equal("project-kata", project.UID)
	assert.Equal("github.com/acme/widget", project.Identity)
	assert.Equal("Widget", project.Name)
}

func TestKataWorkspaceTargetUnavailableWhenAutomaticMappingAmbiguous(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	firstClone := t.TempDir()
	secondClone := t.TempDir()
	require.NoError(os.WriteFile(
		filepath.Join(firstClone, ".kata.toml"),
		[]byte("[project]\nuid = \"project-kata\"\nname = \"Widget\"\n"),
		0o644,
	))
	require.NoError(os.WriteFile(
		filepath.Join(secondClone, ".kata.toml"),
		[]byte("[project]\nuid = \"project-kata\"\nname = \"Other\"\n"),
		0o644,
	))
	cfg := fmt.Sprintf(`
sync_interval = "5m"
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "widget"
worktree_base_path = %q

[[repos]]
owner = "acme"
name = "other"
worktree_base_path = %q
`, firstClone, secondClone)
	srv, database, _ := setupTestServerWithConfigContent(t, cfg, &mockGH{})
	_, err := database.UpsertRepo(t.Context(), db.GitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	_, err = database.UpsertRepo(t.Context(), db.GitHubRepoIdentity("github.com", "acme", "other"))
	require.NoError(err)

	resp := kataWorkspaceTargetViaTaskDetail(t, srv, map[string]string{
		"daemon_id":    "desktop",
		"project_uid":  "project-kata",
		"project_name": "Widget",
		"issue_uid":    "issue-kata-1",
	})
	assert.False(resp.Available)
	assert.Nil(resp.Repo)
}

func TestKataWorkspaceTargetTOMLAmbiguityBlocksTrackedRepoNameFallback(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	firstClone := t.TempDir()
	secondClone := t.TempDir()
	require.NoError(os.WriteFile(
		filepath.Join(firstClone, ".kata.toml"),
		[]byte("[project]\nuid = \"project-kata\"\nname = \"Widget\"\n"),
		0o644,
	))
	require.NoError(os.WriteFile(
		filepath.Join(secondClone, ".kata.toml"),
		[]byte("[project]\nuid = \"project-kata\"\nname = \"Other\"\n"),
		0o644,
	))
	cfg := fmt.Sprintf(`
sync_interval = "5m"
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "widget"
worktree_base_path = %q

[[repos]]
owner = "acme"
name = "other"
worktree_base_path = %q

[[repos]]
owner = "acme"
name = "middleman"
`, firstClone, secondClone)
	srv, database, _ := setupTestServerWithConfigContent(t, cfg, &mockGH{})
	_, err := database.UpsertRepo(t.Context(), db.GitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	_, err = database.UpsertRepo(t.Context(), db.GitHubRepoIdentity("github.com", "acme", "other"))
	require.NoError(err)
	_, err = database.UpsertRepo(t.Context(), db.GitHubRepoIdentity("github.com", "acme", "middleman"))
	require.NoError(err)

	resp := kataWorkspaceTargetViaTaskDetail(t, srv, map[string]string{
		"daemon_id":    "desktop",
		"project_uid":  "project-kata",
		"project_name": "middleman",
		"issue_uid":    "issue-kata-1",
	})
	assert.False(resp.Available)
	assert.Nil(resp.Repo)
}

func TestKataWorkspaceTargetUnavailableWhenProjectNameMappingAmbiguous(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	firstClone := t.TempDir()
	secondClone := t.TempDir()
	for _, cloneDir := range []string{firstClone, secondClone} {
		require.NoError(os.WriteFile(
			filepath.Join(cloneDir, ".kata.toml"),
			[]byte("[project]\nname = \"Widget\"\n"),
			0o644,
		))
	}
	cfg := fmt.Sprintf(`
sync_interval = "5m"
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "widget"
worktree_base_path = %q

[[repos]]
owner = "acme"
name = "other"
worktree_base_path = %q
`, firstClone, secondClone)
	srv, database, _ := setupTestServerWithConfigContent(t, cfg, &mockGH{})
	_, err := database.UpsertRepo(t.Context(), db.GitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	_, err = database.UpsertRepo(t.Context(), db.GitHubRepoIdentity("github.com", "acme", "other"))
	require.NoError(err)

	resp := kataWorkspaceTargetViaTaskDetail(t, srv, map[string]string{
		"daemon_id":    "desktop",
		"project_uid":  "project-kata-opaque",
		"project_name": "Widget",
		"issue_uid":    "issue-kata-1",
	})
	assert.False(resp.Available)
	assert.Nil(resp.Repo)
}

func TestKataWorkspaceTargetUnavailableWhenMappingMissing(t *testing.T) {
	assert := assert.New(t)

	srv, _, _ := setupTestServerWithConfig(t)
	resp := kataWorkspaceTargetViaTaskDetail(t, srv, map[string]string{
		"daemon_id":    "desktop",
		"project_uid":  "project-kata",
		"project_name": "Kata",
		"issue_uid":    "issue-kata-1",
		"short_id":     "task-123",
		"title":        "Fix widget",
	})
	assert.False(resp.Available)
	assert.Nil(resp.Repo)
	assert.Nil(resp.ExistingWorkspace)
}

func TestKataWorkspaceTargetManualMappingReturnsExistingWorkspace(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	srv, database, _ := setupTestServerWithConfigContent(t, `
sync_interval = "5m"
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "widget"

[[kata_projects]]
daemon_id = "desktop"
project_uid = "project-kata"
provider = "github"
platform_host = "github.com"
repo_path = "acme/widget"
`, &mockGH{})
	_, err := database.UpsertRepo(t.Context(), db.GitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	itemKey := db.KataWorkspaceItemKey(db.WorkspaceKataMetadata{
		DaemonID:   "desktop",
		ProjectUID: "project-kata",
		IssueUID:   "issue-kata-1",
	})
	require.NoError(database.InsertWorkspace(t.Context(), &db.Workspace{
		ID:              "ws-kata-existing",
		Platform:        "github",
		PlatformHost:    "github.com",
		RepoOwner:       "acme",
		RepoName:        "widget",
		ItemType:        db.WorkspaceItemTypeKataTask,
		ItemKey:         itemKey,
		GitHeadRef:      "middleman/kata/task-123-fix-widget",
		WorkspaceBranch: "middleman/kata/task-123-fix-widget",
		WorktreePath:    "/tmp/ws-kata-existing",
		TmuxSession:     "middleman-ws-kata-existing",
		Status:          "ready",
		KataMetadata: &db.WorkspaceKataMetadata{
			DaemonID:    "desktop",
			ProjectUID:  "project-kata",
			ProjectName: "Kata",
			IssueUID:    "issue-kata-1",
			ShortID:     "task-123",
			Title:       "Fix widget",
		},
	}))

	resp := kataWorkspaceTargetViaTaskDetail(t, srv, map[string]string{
		"daemon_id":    "desktop",
		"project_uid":  "project-kata",
		"project_name": "Kata",
		"issue_uid":    "issue-kata-1",
		"short_id":     "task-123",
		"title":        "Fix widget",
	})
	assert.True(resp.Available)
	require.NotNil(resp.ExistingWorkspace)
	assert.Equal("ws-kata-existing", resp.ExistingWorkspace.ID)
	assert.Equal("ready", resp.ExistingWorkspace.Status)
}

func TestCreateKataWorkspaceDoesNotRequireProviderIssue(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	srv, database, _ := setupTestServerWithConfigContent(t, `
sync_interval = "5m"
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "widget"

[[kata_projects]]
project_uid = "project-kata"
provider = "github"
platform_host = "github.com"
repo_path = "acme/widget"
`, &mockGH{})
	_, err := database.UpsertRepo(t.Context(), db.GitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	srv.workspaces = workspace.NewManager(database, t.TempDir())

	rr := doJSON(t, srv, http.MethodPost, "/api/v1/kata/workspaces", map[string]any{
		"daemon_id":    "desktop",
		"project_uid":  "project-kata",
		"project_name": "Kata",
		"issue_uid":    "issue-kata-1",
		"short_id":     "task-123",
		"qualified_id": "Kata#task-123",
		"title":        "Fix widget",
	})
	require.Equal(http.StatusAccepted, rr.Code, rr.Body.String())

	var created workspaceResponse
	require.NoError(json.NewDecoder(rr.Body).Decode(&created))
	assert.Equal(db.WorkspaceItemTypeKataTask, created.ItemType)
	assert.Equal(db.KataWorkspaceItemKey(db.WorkspaceKataMetadata{
		DaemonID:   "desktop",
		ProjectUID: "project-kata",
		IssueUID:   "issue-kata-1",
	}), created.ItemKey)
	assert.Contains(created.GitHeadRef, "middleman/kata/task-123-")
	assert.Contains(created.GitHeadRef, "-fix-widget")
	require.NotNil(created.Kata)
	assert.Equal("desktop", created.Kata.DaemonID)
	assert.Equal("issue-kata-1", created.Kata.IssueUID)
	// The workspace list renders the bubble label, display name, and search
	// haystack from these fields, so assert the full identity round-trips on
	// the wire rather than trusting a hand-written frontend fixture.
	assert.Equal("project-kata", created.Kata.ProjectUID)
	assert.Equal("Kata", created.Kata.ProjectName)
	assert.Equal("task-123", created.Kata.ShortID)
	assert.Equal("Kata#task-123", created.Kata.QualifiedID)
	assert.Equal("Fix widget", created.Kata.Title)
}

func TestCreateKataWorkspaceFromGlobTrackedRepoName(t *testing.T) {
	testCreateKataWorkspaceFromTrackedRepoName(t, "middle*")
}

func TestCreateKataWorkspaceFromExactTrackedRepoName(t *testing.T) {
	testCreateKataWorkspaceFromTrackedRepoName(t, "middleman")
}

func testCreateKataWorkspaceFromTrackedRepoName(t *testing.T, configuredRepoName string) {
	t.Helper()

	assert := assert.New(t)
	require := require.New(t)

	cfg := fmt.Sprintf(`
sync_interval = "5m"
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = %q
`, configuredRepoName)
	srv, database, _ := setupTestServerWithConfigContent(t, cfg, &mockGH{})
	_, err := database.UpsertRepo(t.Context(), db.GitHubRepoIdentity("github.com", "acme", "middleman"))
	require.NoError(err)
	srv.workspaces = workspace.NewManager(database, t.TempDir())

	metadata := db.WorkspaceKataMetadata{
		DaemonID:    "desktop",
		ProjectUID:  "project-middleman",
		ProjectName: "middleman",
		IssueUID:    "issue-kata-1",
		ShortID:     "task-123",
		Title:       "Fix workspace affordance",
	}
	rr := doJSON(t, srv, http.MethodPost, "/api/v1/kata/workspaces", map[string]any{
		"daemon_id":    metadata.DaemonID,
		"project_uid":  metadata.ProjectUID,
		"project_name": metadata.ProjectName,
		"issue_uid":    metadata.IssueUID,
		"short_id":     metadata.ShortID,
		"title":        metadata.Title,
	})
	require.Equal(http.StatusAccepted, rr.Code, rr.Body.String())

	var created workspaceResponse
	require.NoError(json.NewDecoder(rr.Body).Decode(&created))
	assert.Equal("github", created.Repo.Provider)
	assert.Equal("github.com", created.Repo.PlatformHost)
	assert.Equal("acme", created.Repo.Owner)
	assert.Equal("middleman", created.Repo.Name)
	assert.Equal("acme/middleman", created.Repo.RepoPath)
	assert.Equal(db.WorkspaceItemTypeKataTask, created.ItemType)
	assert.Equal(db.KataWorkspaceItemKey(metadata), created.ItemKey)
	require.NotNil(created.Kata)
	assert.Equal(metadata.ProjectUID, created.Kata.ProjectUID)
	assert.Equal(metadata.IssueUID, created.Kata.IssueUID)

	stored, err := database.GetWorkspaceByItemKeyForProvider(
		t.Context(),
		"github",
		"github.com",
		"acme",
		"middleman",
		db.WorkspaceItemTypeKataTask,
		db.KataWorkspaceItemKey(metadata),
	)
	require.NoError(err)
	require.NotNil(stored)
	assert.Equal("acme", stored.RepoOwner)
	assert.Equal("middleman", stored.RepoName)
}

func TestCreateKataWorkspaceReusesExistingScopedTaskWorkspace(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	srv, database, _ := setupTestServerWithConfigContent(t, `
sync_interval = "5m"
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "widget"

[[kata_projects]]
project_uid = "project-kata"
provider = "github"
platform_host = "github.com"
repo_path = "acme/widget"
`, &mockGH{})
	_, err := database.UpsertRepo(t.Context(), db.GitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	srv.workspaces = workspace.NewManager(database, t.TempDir())

	body := map[string]any{
		"daemon_id":    "desktop",
		"project_uid":  "project-kata",
		"project_name": "Kata",
		"issue_uid":    "issue-kata-1",
		"short_id":     "task-123",
		"title":        "Fix widget",
	}
	first := doJSON(t, srv, http.MethodPost, "/api/v1/kata/workspaces", body)
	require.Equal(http.StatusAccepted, first.Code, first.Body.String())
	var created workspaceResponse
	require.NoError(json.NewDecoder(first.Body).Decode(&created))

	second := doJSON(t, srv, http.MethodPost, "/api/v1/kata/workspaces", body)
	require.Equal(http.StatusAccepted, second.Code, second.Body.String())
	var reused workspaceResponse
	require.NoError(json.NewDecoder(second.Body).Decode(&reused))

	assert.Equal(created.ID, reused.ID)
	assert.Equal(created.ItemKey, reused.ItemKey)
	assert.Equal(db.KataWorkspaceItemKey(db.WorkspaceKataMetadata{
		DaemonID:   "desktop",
		ProjectUID: "project-kata",
		IssueUID:   "issue-kata-1",
	}), reused.ItemKey)
}
