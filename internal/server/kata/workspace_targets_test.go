package kata

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
	katacatalog "go.kenn.io/forge/internal/kata"
	"go.kenn.io/forge/internal/server/workspaceapi"
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
