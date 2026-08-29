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
