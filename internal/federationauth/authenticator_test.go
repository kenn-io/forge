package federationauth

import (
	"net/http"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFederationCredentialAuthorizationUsesExactRouteInventory(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	store, err := Open(filepath.Join(t.TempDir(), "credentials.json"))
	require.NoError(err)
	token, err := store.MintInbound(testMemberNodeID, []Scope{ScopeSnapshotRead})
	require.NoError(err)
	authenticator := NewAuthenticator(store)

	principal, ok := authenticator.Authenticate(token)
	require.True(ok)
	required, listed := authenticator.RequiredScope(http.MethodGet, "/api/v1/snapshot/raw")
	require.True(listed)
	assert.Equal(ScopeSnapshotRead, required)
	assert.True(principal.Has(required))
	required, listed = authenticator.RequiredScope(http.MethodHead, "/api/v1/snapshot/raw")
	require.True(listed)
	assert.Equal(ScopeSnapshotRead, required)

	_, listed = authenticator.RequiredScope(http.MethodGet, "/api/v1/settings")
	assert.False(listed)
	_, listed = authenticator.RequiredScope(http.MethodPost, "/api/v1/snapshot/raw")
	assert.False(listed)
	required, listed = authenticator.RequiredScope(
		http.MethodPost, "/api/v1/workspaces/ws-1/runtime/sessions",
	)
	require.True(listed)
	assert.Equal(ScopeWorkspaceWrite, required)
	required, listed = authenticator.RequiredScope(
		http.MethodPost, "/api/v1/host/git.example.test/repo/gitlab/acme/widgets/workspaces",
	)
	require.True(listed)
	assert.Equal(ScopeWorkspaceWrite, required)
	required, listed = authenticator.RequiredScope(
		http.MethodPost, "/api/v1/federation/workspaces/ws-1/cleanup",
	)
	require.True(listed)
	assert.Equal(ScopeWorkspaceWrite, required)
	required, listed = authenticator.RequiredScope(
		http.MethodGet, "/ws/v1/workspaces/ws-1/terminal",
	)
	require.True(listed)
	assert.Equal(ScopeTerminalAttach, required)
	required, listed = authenticator.RequiredScope(
		http.MethodPost,
		"/api/v1/federation/enrollments/11111111111111111111111111111111/activate",
	)
	require.True(listed)
	assert.Equal(ScopeEnrollmentActivate, required)
	for _, suffix := range []string{"preparation/begin", "preparation/seal"} {
		required, listed = authenticator.RequiredScope(
			http.MethodPost,
			"/api/v1/federation/enrollments/11111111111111111111111111111111/"+suffix,
		)
		require.True(listed)
		assert.Equal(ScopeEnrollmentActivate, required)
	}
	_, listed = authenticator.RequiredScope(
		http.MethodPost,
		"/api/v1/federation/enrollments/11111111111111111111111111111111/activate/extra",
	)
	assert.False(listed)
}

func TestFleetHostRoutesRequireTheirTargetRouteScope(t *testing.T) {
	const host = "/fleet/hosts/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	for _, tc := range []struct {
		method, path string
		want         Scope
	}{
		{http.MethodPost, "/api/v1" + host + "/workspaces", ScopeWorkspaceWrite},
		{http.MethodGet, "/api/v1" + host + "/workspaces/ws-1", ScopeWorkspaceRead},
		{http.MethodDelete, "/api/v1" + host + "/workspaces/ws-1", ScopeWorkspaceWrite},
		{http.MethodPost, "/api/v1" + host + "/issues/github/acme/widget/7/workspace", ScopeWorkspaceWrite},
		{http.MethodGet, "/ws/v1" + host + "/workspaces/ws-1/terminal", ScopeTerminalAttach},
	} {
		required, listed := RouteScope(tc.method, tc.path)
		assert.True(t, listed, tc.path)
		assert.Equal(t, tc.want, required, tc.path)
	}
	for _, path := range []string{
		"/api/v1" + host + "/browser-login",
		"/api/v1" + host + "/settings",
		"/api/v1/fleet/hosts/",
	} {
		_, listed := RouteScope(http.MethodPost, path)
		assert.False(t, listed, "a fleet route whose target is not peer-callable stays forbidden: "+path)
	}
}
