package kata

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
	katacatalog "go.kenn.io/forge/internal/kata"
	"go.kenn.io/forge/internal/server/workspaceapi"
	"go.kenn.io/forge/internal/testutil/reposeed"
)

func TestWorkspaceKataTargetAvailability(t *testing.T) {
	for _, tt := range []struct {
		name       string
		daemons    []katacatalog.Daemon
		discovered string
		want       bool
	}{
		{name: "absent"},
		{name: "remote", daemons: []katacatalog.Daemon{{ID: "primary", URL: "https://kata.example.test"}}, want: true},
		{name: "local stopped", daemons: []katacatalog.Daemon{{ID: "local", Local: true}}},
		{name: "local discovered", daemons: []katacatalog.Daemon{{ID: "local", Local: true}}, discovered: "http://127.0.0.1:9876", want: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h := &Handler{loadCatalog: func() (katacatalog.Catalog, error) { return katacatalog.Catalog{Daemons: tt.daemons}, nil }, resolveDaemon: func(d katacatalog.Daemon) (katacatalog.Daemon, error) { return d, nil }, discoverLocalDaemonURL: func() string { return tt.discovered }}
			assert.Equal(t, tt.want, h.WorkspaceKataTargetsAvailable())
			if !tt.want {
				_, err := h.AddWorkspaceKataTarget(t.Context(), "ws-targets", workspaceapi.WorkspaceKataTarget{DaemonID: "local", ProjectUID: "project-a", IssueUID: "issue-a"})
				require.ErrorContains(t, err, "Kata target linking is unavailable")
				require.ErrorContains(t, h.RemoveWorkspaceKataTarget(t.Context(), "ws-targets", 1), "Kata target linking is unavailable")
			}
		})
	}
}

func TestWorkspaceKataTargetsReuseTaskLinks(t *testing.T) {
	daemon := newKataLinkTestDaemon(t)
	configureKataLinkTestDaemon(t, daemon.URL)
	srv, database := setupTestServer(t)
	ctx := t.Context()
	repoID := insertKataProviderSubject(t, database, "github", "github.com", "widget", 42, db.KataLinkSubjectIssue, "item-target")
	require.NoError(t, database.InsertWorkspace(ctx, &db.Workspace{ID: "ws-targets", RepoID: repoID, Platform: "github", PlatformHost: "github.com", RepoOwner: "acme", RepoName: "widget", ItemType: db.WorkspaceItemTypeAdHoc, ItemKey: "targets", Status: "ready", WorktreePath: t.TempDir()}))
	target := workspaceapi.WorkspaceKataTarget{DaemonID: "primary", ProjectUID: "project-a", IssueUID: "issue-a"}
	linked, err := srv.AddWorkspaceKataTarget(ctx, "ws-targets", target)
	require.NoError(t, err)
	assert.Positive(t, linked.ID)
	again, err := srv.AddWorkspaceKataTarget(ctx, "ws-targets", target)
	require.NoError(t, err)
	assert.Equal(t, linked.ID, again.ID)
	links, err := srv.listWorkspaceKataLinks(ctx, &kataWorkspaceLinkInput{WorkspaceID: "ws-targets"})
	require.NoError(t, err)
	require.Len(t, links.Body.Links, 1)
	assert.Equal(t, linked.ID, *links.Body.Links[0].DirectLinkID)
	require.NoError(t, srv.RemoveWorkspaceKataTarget(ctx, "ws-targets", linked.ID))
	listed, err := srv.ListWorkspaceKataTargets(ctx, "ws-targets")
	require.NoError(t, err)
	assert.Empty(t, listed)
}

// MCP creates Kata workspaces through the workspace service; an existing task
// workspace must be returned while the task's local daemon is stopped.
func TestCreateKataWorkspaceServiceReusesWorkspaceWhileDaemonStopped(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	srv, database, _ := setupWorkspaceTestServerWithConfigContent(t, `
sync_interval = "5m"
github_token_env = "KENN_FORGE_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "widget"
`, &mockGH{})
	srv.loadCatalog = func() (katacatalog.Catalog, error) {
		return katacatalog.Catalog{Daemons: []katacatalog.Daemon{{ID: "desktop", Local: true}}}, nil
	}
	srv.resolveDaemon = func(d katacatalog.Daemon) (katacatalog.Daemon, error) { return d, nil }
	srv.discoverLocalDaemonURL = func() string { return "" }
	require.False(srv.WorkspaceKataTargetsAvailable())
	srv.workspaceAPI.SetKataTargets(srv.Handler)

	_, err := reposeed.Seed(t.Context(), database, verifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	metadata := db.WorkspaceKataMetadata{DaemonID: "desktop", ProjectUID: "project-kata", IssueUID: "issue-kata-1"}
	require.NoError(database.InsertWorkspace(t.Context(), &db.Workspace{
		ID: "ws-kata-existing", Platform: "github", PlatformHost: "github.com",
		RepoOwner: "acme", RepoName: "widget",
		ItemType: db.WorkspaceItemTypeKataTask, ItemKey: db.KataWorkspaceItemKey(metadata),
		GitHeadRef: "kenn-forge/kata/task-1", WorkspaceBranch: "kenn-forge/kata/task-1",
		WorktreePath: "/tmp/ws-kata-existing", Status: "ready", KataMetadata: &metadata,
	}))

	result, err := srv.workspaceAPI.CreateKataWorkspaceService(t.Context(), workspaceapi.WorkspaceKataTarget{
		DaemonID: "desktop", ProjectUID: "project-kata", IssueUID: "issue-kata-1",
	})
	require.NoError(err)
	assert.Equal("ws-kata-existing", result.Workspace.ID)
	assert.False(result.Workspace.Created)
}
