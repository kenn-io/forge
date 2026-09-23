package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/federationauth"
	"go.kenn.io/forge/internal/fleet"
	"go.kenn.io/forge/internal/providerplane"
	"go.kenn.io/forge/internal/testutil/dbtest"
)

func TestSpokePullsWorkspaceProviderStateFromHub(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := dbtest.Open(t)
	credentials, err := federationauth.Open(filepath.Join(t.TempDir(), "credentials.json"))
	require.NoError(err)
	token, err := credentials.MintInbound(
		"fedcba9876543210fedcba9876543210", []federationauth.Scope{federationauth.ScopeProviderRead},
	)
	require.NoError(err)
	hub := New(database, nil, nil, "/", nil, ServerOptions{
		DaemonAccess:          DaemonAccessOptions{Token: "local-secret", RequireAPIAuth: true},
		FederationCredentials: credentials,
	})
	t.Cleanup(func() { gracefulShutdown(t, hub) })
	hubServer := httptest.NewServer(hub)
	t.Cleanup(hubServer.Close)
	seedPR(t, database, "acme", "widget", 1, func(pull *db.MergeRequest) {
		pull.Title = "Merged change"
		pull.State = db.MergeRequestStateMerged
	})
	seedPR(t, database, "acme", "widget", 2, func(pull *db.MergeRequest) {
		pull.Title = "Linked change"
	})
	source := &hubProviderSource{client: providerPlaneClientFunc(func(
		_ context.Context, scope federationauth.Scope, request *http.Request,
	) (*http.Response, error) {
		assert.Equal(federationauth.ScopeProviderRead, scope)
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set(providerplane.ProtocolVersionHeader, providerplane.ProtocolVersionHeaderValue())
		target, err := url.Parse(hubServer.URL)
		require.NoError(err)
		request.URL.Scheme, request.URL.Host, request.Host = target.Scheme, target.Host, ""
		return hubServer.Client().Do(request)
	})}
	repository := fleet.RepositoryIdentity{
		Provider: "github", PlatformHost: "github.com", PlatformRepoID: "repo-acme-widget",
		Owner: "acme", Name: "widget",
	}

	got, err := source.WorkspaceProviderState(t.Context(), []fleet.RawWorkspace{
		{ID: "ws-pull", Repository: repository, ItemType: db.WorkspaceItemTypePullRequest, ItemNumber: 1},
		{ID: "ws-adhoc", Repository: repository, ItemType: db.WorkspaceItemTypeAdHoc, AssociatedPRNumber: new(2)},
		{ID: "ws-unsynced", ItemType: db.WorkspaceItemTypePullRequest, ItemNumber: 1},
	})
	require.NoError(err)
	require.Len(got, 3)
	require.NotNil(got[0].MRState)
	assert.Equal("merged", *got[0].MRState)
	require.NotNil(got[0].MRTitle)
	assert.Equal("Merged change", *got[0].MRTitle)
	require.NotNil(got[1].MRState)
	assert.Equal("open", *got[1].MRState)
	require.NotNil(got[1].MRTitle)
	assert.Equal("Linked change", *got[1].MRTitle)
	assert.Nil(got[2].MRState, "a workspace without a stable repository identity is not resolved")
}
