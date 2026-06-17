package server

import (
	"net/http"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newFleetTestAPI builds an in-process huma.API the same way the
// route-metadata walker tests in this package do, so registration can be
// introspected via api.OpenAPI() without standing up a real server.
func newFleetTestAPI() huma.API {
	mux := http.NewServeMux()
	return humago.New(mux, huma.DefaultConfig("test", "0.0.0"))
}

func TestSnapshotRoutesRegistered(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	s := &Server{} // registration does not call handlers
	api := newFleetTestAPI()
	s.registerFleetRoutes(api)

	spec := api.OpenAPI()
	for _, path := range []string{
		"/snapshot",
		"/snapshot/raw",
		"/fleet/hosts/{host_key}/workspaces",
		"/fleet/hosts/{host_key}/issues/{provider}/{owner}/{name}/{number}/workspace",
		"/fleet/hosts/{host_key}/host/{platform_host}/issues/{provider}/{owner}/{name}/{number}/workspace",
		"/fleet/hosts/{host_key}/workspaces/{id}/runtime/sessions",
		"/fleet/hosts/{host_key}/workspaces/{id}/runtime/sessions/{session_key}",
		"/fleet/hosts/{host_key}/workspaces/{id}/runtime/sessions/{session_key}/attach-spec",
		"/fleet/hosts/{host_key}/workspaces/{id}/commits",
		"/fleet/hosts/{host_key}/workspaces/{id}/diff",
		"/fleet/hosts/{host_key}/workspaces/{id}/file-preview",
		"/fleet/hosts/{host_key}/workspaces/{id}/files",
		"/fleet/hosts/{host_key}/projects/{project_id}/worktrees/{worktree_id}/runtime",
		"/fleet/hosts/{host_key}/projects/{project_id}/worktrees/{worktree_id}/runtime/shell",
		"/fleet/hosts/{host_key}/projects/{project_id}/worktrees/{worktree_id}/runtime/sessions",
		"/fleet/hosts/{host_key}/projects/{project_id}/worktrees/{worktree_id}/runtime/sessions/{session_key}",
		"/fleet/hosts/{host_key}/projects/{project_id}/worktrees/{worktree_id}/runtime/sessions/{session_key}/attach-spec",
		"/fleet/hosts/{host_key}/workspaces/{id}",
		"/fleet/hosts/{host_key}/projects",
		"/fleet/hosts/{host_key}/projects/{project_id}",
		"/fleet/hosts/{host_key}/projects/{project_id}/worktrees",
		"/fleet/hosts/{host_key}/projects/{project_id}/worktrees/from-merge-request",
		"/fleet/hosts/{host_key}/projects/{project_id}/worktrees/{worktree_id}/delete",
		"/fleet/hosts/{host_key}/projects/{project_id}/worktrees/{worktree_id}/session-backend",
		"/fleet/hosts/{host_key}/projects/{project_id}/worktrees/{worktree_id}/linked-issues",
		"/fleet/hosts/{host_key}/projects/{project_id}/worktrees/{worktree_id}/refresh-stats",
	} {
		require.NotNil(spec.Paths[path], "%s not registered", path)
	}
	require.NotNil(spec.Paths["/snapshot"].Get, "GET /snapshot not registered")
	require.NotNil(spec.Paths["/snapshot/raw"].Get, "GET /snapshot/raw not registered")
	require.NotNil(spec.Paths["/fleet/hosts/{host_key}/workspaces"].Post, "POST fleet workspaces not registered")
	require.NotNil(spec.Paths["/fleet/hosts/{host_key}/issues/{provider}/{owner}/{name}/{number}/workspace"].Post, "POST fleet issue workspace not registered")
	require.NotNil(spec.Paths["/fleet/hosts/{host_key}/host/{platform_host}/issues/{provider}/{owner}/{name}/{number}/workspace"].Post, "POST fleet host issue workspace not registered")
	require.NotNil(spec.Paths["/fleet/hosts/{host_key}/workspaces/{id}/runtime/sessions"].Post, "POST fleet session not registered")
	require.NotNil(spec.Paths["/fleet/hosts/{host_key}/workspaces/{id}/runtime/sessions/{session_key}"].Delete, "DELETE fleet session not registered")
	require.NotNil(spec.Paths["/fleet/hosts/{host_key}/workspaces/{id}/runtime/sessions/{session_key}/attach-spec"].Get, "GET fleet session attach spec not registered")
	require.NotNil(spec.Paths["/fleet/hosts/{host_key}/workspaces/{id}/commits"].Get, "GET fleet workspace commits not registered")
	require.NotNil(spec.Paths["/fleet/hosts/{host_key}/workspaces/{id}/diff"].Get, "GET fleet workspace diff not registered")
	require.NotNil(spec.Paths["/fleet/hosts/{host_key}/workspaces/{id}/file-preview"].Get, "GET fleet workspace file preview not registered")
	require.NotNil(spec.Paths["/fleet/hosts/{host_key}/workspaces/{id}/files"].Get, "GET fleet workspace files not registered")
	require.NotNil(spec.Paths["/fleet/hosts/{host_key}/projects/{project_id}/worktrees/{worktree_id}/runtime"].Get, "GET fleet project worktree runtime not registered")
	require.NotNil(spec.Paths["/fleet/hosts/{host_key}/projects/{project_id}/worktrees/{worktree_id}/runtime/shell"].Post, "POST fleet project worktree shell not registered")
	require.NotNil(spec.Paths["/fleet/hosts/{host_key}/projects/{project_id}/worktrees/{worktree_id}/runtime/sessions"].Post, "POST fleet project worktree session not registered")
	require.NotNil(spec.Paths["/fleet/hosts/{host_key}/projects/{project_id}/worktrees/{worktree_id}/runtime/sessions/{session_key}"].Delete, "DELETE fleet project worktree session not registered")
	require.NotNil(spec.Paths["/fleet/hosts/{host_key}/projects/{project_id}/worktrees/{worktree_id}/runtime/sessions/{session_key}/attach-spec"].Get, "GET fleet project worktree session attach spec not registered")
	require.NotNil(spec.Paths["/fleet/hosts/{host_key}/workspaces/{id}"].Delete, "DELETE fleet workspace not registered")
	require.NotNil(spec.Paths["/fleet/hosts/{host_key}/projects"].Post, "POST fleet project register not registered")
	require.NotNil(spec.Paths["/fleet/hosts/{host_key}/projects/{project_id}"].Delete, "DELETE fleet project not registered")

	require.NotNil(spec.Paths["/fleet/hosts/{host_key}/projects/{project_id}/worktrees"].Post, "POST fleet worktree create not registered")
	require.NotNil(spec.Paths["/fleet/hosts/{host_key}/projects/{project_id}/worktrees/from-merge-request"].Post, "POST fleet worktree from-merge-request not registered")
	require.NotNil(spec.Paths["/fleet/hosts/{host_key}/projects/{project_id}/worktrees/{worktree_id}/delete"].Post, "POST fleet worktree remove not registered")
	require.NotNil(spec.Paths["/fleet/hosts/{host_key}/projects/{project_id}/worktrees/{worktree_id}/session-backend"].Put, "PUT fleet worktree session backend not registered")
	require.NotNil(spec.Paths["/fleet/hosts/{host_key}/projects/{project_id}/worktrees/{worktree_id}/linked-issues"].Put, "PUT fleet worktree linked issues not registered")
	require.NotNil(spec.Paths["/fleet/hosts/{host_key}/projects/{project_id}/worktrees/{worktree_id}/refresh-stats"].Post, "POST fleet worktree stats refresh not registered")

	gotProjectRegister := spec.Paths["/fleet/hosts/{host_key}/projects"].Post
	assert.Equal("register-fleet-project", gotProjectRegister.OperationID)
	assert.Equal([]string{"Fleet"}, gotProjectRegister.Tags)

	// Operation IDs + Fleet tag present on the enriched snapshot route.
	got := spec.Paths["/snapshot"].Get
	assert.Equal("get-snapshot", got.OperationID)
	assert.Equal([]string{"Fleet"}, got.Tags)

	// Operation IDs + Fleet tag present on the raw snapshot route.
	gotRaw := spec.Paths["/snapshot/raw"].Get
	assert.Equal("get-snapshot-raw", gotRaw.OperationID)
	assert.Equal([]string{"Fleet"}, gotRaw.Tags)

	gotCreate := spec.Paths["/fleet/hosts/{host_key}/workspaces"].Post
	assert.Equal("create-fleet-workspace", gotCreate.OperationID)
	assert.Equal([]string{"Fleet"}, gotCreate.Tags)
}
