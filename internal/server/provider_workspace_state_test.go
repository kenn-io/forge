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
	"go.kenn.io/forge/internal/server/authapi"
	"go.kenn.io/forge/internal/server/spokeapi"
	"go.kenn.io/forge/internal/testutil"
	"go.kenn.io/forge/internal/testutil/dbtest"
	serverfake "go.kenn.io/forge/internal/testutil/serverfake"
	"go.kenn.io/forge/platform"
)

func TestSpokePullsWorkspaceProviderStateFromHub(t *testing.T) {
	serverfake.RunParallelServerTest(t)
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
		DaemonAccess:          authapi.DaemonAccessOptions{Token: "local-secret", RequireAPIAuth: true},
		FederationCredentials: credentials,
	})
	t.Cleanup(func() { serverfake.GracefulShutdown(t, hub) })
	hubServer := httptest.NewServer(hub)
	t.Cleanup(hubServer.Close)
	serverfake.SeedPR(t, database, "acme", "widget", 1, func(pull *db.MergeRequest) {
		pull.Title = "Merged change"
		pull.Author = "alice"
		pull.State = db.MergeRequestStateMerged
		pull.AutoMergeEnabled = true
	})
	serverfake.SeedPR(t, database, "acme", "widget", 2, func(pull *db.MergeRequest) {
		pull.Title = "Linked change"
		pull.Author = "bob"
		pull.AutoMergeEnabled = true
		pull.MergeableState = "dirty"
		pull.CommentCount = 3
		pull.CIChecksJSON = `[{"name":"lint","status":"completed","conclusion":"FAILURE"},{"name":"build","status":"IN_PROGRESS"}]`
	})
	source := &spokeapi.HubProviderSource{Client: providerPlaneClientFunc(func(
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
		Provider: "github", PlatformHost: "github.com", Key: platform.RepositoryIDKey(testutil.FixtureRepoID("acme", "widget")),
		Owner: "acme", Name: "widget",
	}
	entry, err := database.GetRepositoryByProviderID(t.Context(), repository.ProviderIdentity())
	require.NoError(err)
	require.NotNil(entry)
	require.NoError(database.UpdateRepoProviderObservation(
		t.Context(), entry.Repository.ID, db.RepoProviderMetadata{Private: new(true)}, nil, nil,
	))

	got, err := source.WorkspaceProviderState(t.Context(), []fleet.RawWorkspace{
		{ID: "ws-pull", Repository: repository, ItemType: db.WorkspaceItemTypePullRequest, ItemNumber: 1, RepoPrivate: new(false)},
		{ID: "ws-adhoc", Repository: repository, ItemType: db.WorkspaceItemTypeAdHoc, AssociatedPRNumber: new(2)},
		{ID: "ws-unsynced", ItemType: db.WorkspaceItemTypePullRequest, ItemNumber: 1, RepoPrivate: new(false)},
	})
	require.NoError(err)
	require.Len(got, 3)
	require.NotNil(got[0].MRState)
	assert.Equal("merged", *got[0].MRState)
	require.NotNil(got[0].MRTitle)
	assert.Equal("Merged change", *got[0].MRTitle)
	assert.Equal(new("alice"), got[0].ItemAuthor)
	require.NotNil(got[1].MRState)
	assert.Equal("open", *got[1].MRState)
	require.NotNil(got[1].MRTitle)
	assert.Equal("Linked change", *got[1].MRTitle)
	assert.Equal(new("bob"), got[1].ItemAuthor)
	assert.Equal(new(true), got[1].MRDeferredMergePending, "provider auto-merge on an open pull request waits for CI")
	assert.Equal(new(false), got[0].MRDeferredMergePending, "a merged pull request has nothing left to merge")
	assert.Equal(new("dirty"), got[1].MRMergeableState)
	assert.Equal(new(3), got[1].MRCommentCount)
	assert.Equal([]fleet.CheckDetail{
		{Name: "lint", Status: "completed", Conclusion: "failure"},
		{Name: "build", Status: "in_progress"},
	}, got[1].MRChecks)
	assert.Nil(got[2].MRState, "a workspace without a stable repository identity is not resolved")
	assert.Equal(new(true), got[0].RepoPrivate, "the hub's privacy replaces the spoke's stale value")
	assert.Equal(new(true), got[1].RepoPrivate, "the hub's privacy fills the spoke's unknown value")
	assert.Equal(new(false), got[2].RepoPrivate, "an unresolved workspace keeps its own privacy")
}
