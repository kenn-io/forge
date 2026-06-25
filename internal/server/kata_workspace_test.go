package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/db"
	"go.kenn.io/middleman/internal/workspace"
)

func TestKataWorkspaceTargetAutomaticMapping(t *testing.T) {
	assert := Assert.New(t)
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

	rr := doJSON(t, srv, http.MethodPost, "/api/v1/kata/workspace-target", map[string]any{
		"daemon_id":    "desktop",
		"project_uid":  "project-kata",
		"project_name": "Kata",
		"issue_uid":    "issue-kata-1",
		"short_id":     "task-123",
		"qualified_id": "Kata#task-123",
		"title":        "Fix widget",
	})
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())

	var resp kataWorkspaceTargetResponse
	require.NoError(json.NewDecoder(rr.Body).Decode(&resp))
	assert.True(resp.Available)
	require.NotNil(resp.Repo)
	assert.Equal("github", resp.Repo.Provider)
	assert.Equal("github.com", resp.Repo.PlatformHost)
	assert.Equal("acme", resp.Repo.Owner)
	assert.Equal("widget", resp.Repo.Name)
	assert.Equal(db.WorkspaceItemTypeKataTask, resp.ItemType)
	assert.Equal("issue-kata-1", resp.ItemKey)
	assert.Nil(resp.ExistingWorkspace)
}

func TestKataWorkspaceTargetUnavailableWhenMappingMissing(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	srv, _, _ := setupTestServerWithConfig(t)
	rr := doJSON(t, srv, http.MethodPost, "/api/v1/kata/workspace-target", map[string]any{
		"daemon_id":    "desktop",
		"project_uid":  "project-kata",
		"project_name": "Kata",
		"issue_uid":    "issue-kata-1",
		"short_id":     "task-123",
		"title":        "Fix widget",
	})
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())

	var resp kataWorkspaceTargetResponse
	require.NoError(json.NewDecoder(rr.Body).Decode(&resp))
	assert.False(resp.Available)
	assert.Nil(resp.Repo)
	assert.Nil(resp.ExistingWorkspace)
}

func TestKataWorkspaceTargetManualMappingReturnsExistingWorkspace(t *testing.T) {
	assert := Assert.New(t)
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
	require.NoError(database.InsertWorkspace(t.Context(), &db.Workspace{
		ID:              "ws-kata-existing",
		Platform:        "github",
		PlatformHost:    "github.com",
		RepoOwner:       "acme",
		RepoName:        "widget",
		ItemType:        db.WorkspaceItemTypeKataTask,
		ItemKey:         "issue-kata-1",
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

	rr := doJSON(t, srv, http.MethodPost, "/api/v1/kata/workspace-target", map[string]any{
		"daemon_id":    "desktop",
		"project_uid":  "project-kata",
		"project_name": "Kata",
		"issue_uid":    "issue-kata-1",
		"short_id":     "task-123",
		"title":        "Fix widget",
	})
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())

	var resp kataWorkspaceTargetResponse
	require.NoError(json.NewDecoder(rr.Body).Decode(&resp))
	assert.True(resp.Available)
	require.NotNil(resp.ExistingWorkspace)
	assert.Equal("ws-kata-existing", resp.ExistingWorkspace.ID)
	assert.Equal("ready", resp.ExistingWorkspace.Status)
}

func TestCreateKataWorkspaceDoesNotRequireProviderIssue(t *testing.T) {
	assert := Assert.New(t)
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
	assert.Equal("issue-kata-1", created.ItemKey)
	assert.Equal("middleman/kata/task-123-fix-widget", created.GitHeadRef)
	require.NotNil(created.Kata)
	assert.Equal("desktop", created.Kata.DaemonID)
	assert.Equal("issue-kata-1", created.Kata.IssueUID)
}
