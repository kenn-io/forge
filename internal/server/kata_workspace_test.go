package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

	rr := doJSON(t, srv, http.MethodPost, "/api/v1/kata/workspace-target", map[string]any{
		"daemon_id":    "desktop",
		"project_uid":  "github.com/acme/widget",
		"project_name": "widget",
		"issue_uid":    "issue-kata-1",
		"short_id":     "task-123",
		"title":        "Fix widget",
	})
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())

	var resp kataWorkspaceTargetResponse
	require.NoError(json.NewDecoder(rr.Body).Decode(&resp))
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

	rr := doJSON(t, srv, http.MethodPost, "/api/v1/kata/workspace-target", map[string]any{
		"daemon_id":    "desktop",
		"project_uid":  "project-kata",
		"project_name": "widget",
		"issue_uid":    "issue-kata-1",
	})
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())

	var resp kataWorkspaceTargetResponse
	require.NoError(json.NewDecoder(rr.Body).Decode(&resp))
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

	rr := doJSON(t, srv, http.MethodPost, "/api/v1/kata/workspace-target", map[string]any{
		"daemon_id":    "desktop",
		"project_uid":  "project-kata-opaque",
		"project_name": "widget",
		"issue_uid":    "issue-kata-1",
	})
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())

	var resp kataWorkspaceTargetResponse
	require.NoError(json.NewDecoder(rr.Body).Decode(&resp))
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

	rr := doJSON(t, srv, http.MethodPost, "/api/v1/kata/workspace-target", map[string]any{
		"daemon_id":    "desktop",
		"project_uid":  "project-kata",
		"project_name": "Widget",
		"issue_uid":    "issue-kata-1",
	})
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())

	var resp kataWorkspaceTargetResponse
	require.NoError(json.NewDecoder(rr.Body).Decode(&resp))
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

	rr := doJSON(t, srv, http.MethodPost, "/api/v1/kata/workspace-target", map[string]any{
		"daemon_id":    "desktop",
		"project_uid":  "project-kata-opaque",
		"project_name": "Widget",
		"issue_uid":    "issue-kata-1",
	})
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())

	var resp kataWorkspaceTargetResponse
	require.NoError(json.NewDecoder(rr.Body).Decode(&resp))
	assert.True(resp.Available)
	require.NotNil(resp.Repo)
	assert.Equal("acme", resp.Repo.Owner)
	assert.Equal("widget", resp.Repo.Name)
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

	rr := doJSON(t, srv, http.MethodPost, "/api/v1/kata/workspace-target", map[string]any{
		"daemon_id":    "desktop",
		"project_uid":  "project-kata",
		"project_name": "widget",
		"issue_uid":    "issue-kata-1",
	})
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())

	var resp kataWorkspaceTargetResponse
	require.NoError(json.NewDecoder(rr.Body).Decode(&resp))
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

func TestKataWorkspaceTargetRequiresDaemonID(t *testing.T) {
	require := require.New(t)

	srv, _, _ := setupTestServerWithConfig(t)
	rr := doJSON(t, srv, http.MethodPost, "/api/v1/kata/workspace-target", map[string]any{
		"project_uid": "project-kata",
		"issue_uid":   "issue-kata-1",
	})
	require.Equal(http.StatusUnprocessableEntity, rr.Code, rr.Body.String())
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

	rr := doJSON(t, srv, http.MethodPost, "/api/v1/kata/workspace-target", map[string]any{
		"daemon_id":    "desktop",
		"project_uid":  "project-kata",
		"project_name": "Widget",
		"issue_uid":    "issue-kata-1",
	})
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())

	var resp kataWorkspaceTargetResponse
	require.NoError(json.NewDecoder(rr.Body).Decode(&resp))
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

	rr := doJSON(t, srv, http.MethodPost, "/api/v1/kata/workspace-target", map[string]any{
		"daemon_id":    "desktop",
		"project_uid":  "project-kata-opaque",
		"project_name": "Widget",
		"issue_uid":    "issue-kata-1",
	})
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())

	var resp kataWorkspaceTargetResponse
	require.NoError(json.NewDecoder(rr.Body).Decode(&resp))
	assert.False(resp.Available)
	assert.Nil(resp.Repo)
}

func TestKataWorkspaceTargetUnavailableWhenMappingMissing(t *testing.T) {
	assert := assert.New(t)
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
