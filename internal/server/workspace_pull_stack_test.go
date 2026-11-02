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
	"go.kenn.io/forge/internal/providerplane"
	"go.kenn.io/forge/internal/server/authapi"
	"go.kenn.io/forge/internal/server/spokeapi"
	"go.kenn.io/forge/internal/testutil/dbtest"
	serverfake "go.kenn.io/forge/internal/testutil/serverfake"
	"go.kenn.io/forge/internal/workspace"
)

func TestSpokeReadsWorkspacePullStackFromHub(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	hubDB := dbtest.Open(t)
	credentials, err := federationauth.Open(filepath.Join(t.TempDir(), "credentials.json"))
	require.NoError(err)
	token, err := credentials.MintInbound(
		"fedcba9876543210fedcba9876543210", []federationauth.Scope{federationauth.ScopeProviderRead},
	)
	require.NoError(err)
	hub := New(hubDB, nil, nil, "/", nil, ServerOptions{
		DaemonAccess:          authapi.DaemonAccessOptions{Token: "local-secret", RequireAPIAuth: true},
		FederationCredentials: credentials,
	})
	t.Cleanup(func() { serverfake.GracefulShutdown(t, hub) })
	hubServer := httptest.NewServer(hub)
	t.Cleanup(hubServer.Close)

	// Only the hub database holds PR and stack rows, as on a fleet spoke.
	var repoID int64
	bottom := serverfake.SeedPR(t, hubDB, "acme", "widget", 41, func(pull *db.MergeRequest) {
		repoID, pull.HeadBranch = pull.RepoID, "feature/base"
	})
	top := serverfake.SeedPR(t, hubDB, "acme", "widget", 42, func(pull *db.MergeRequest) {
		pull.HeadBranch, pull.BaseBranch, pull.IsDraft = "feature/top", "feature/base", true
	})
	serverfake.SeedPR(t, hubDB, "acme", "widget", 50)
	stackID, err := hubDB.UpsertStack(t.Context(), repoID, 41, "feature", true)
	require.NoError(err)
	require.NoError(hubDB.ReplaceStackMembers(t.Context(), stackID, []db.StackMember{
		{StackID: stackID, MergeRequestID: bottom, Position: 1},
		{StackID: stackID, MergeRequestID: top, Position: 2},
	}))

	readStack := hubWorkspacePullStack(&spokeapi.HubProviderSource{Client: providerPlaneClientFunc(func(
		_ context.Context, _ federationauth.Scope, request *http.Request,
	) (*http.Response, error) {
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set(providerplane.ProtocolVersionHeader, providerplane.ProtocolVersionHeaderValue())
		target, err := url.Parse(hubServer.URL)
		require.NoError(err)
		request.URL.Scheme, request.URL.Host, request.Host = target.Scheme, target.Host, ""
		return hubServer.Client().Do(request)
	})})
	ws := workspace.WorkspaceSummary{
		Platform: "github", PlatformHost: "github.com", RepoOwner: "acme", RepoName: "widget",
	}

	stack, err := readStack(t.Context(), ws, 42)
	require.NoError(err)
	require.NotNil(stack)
	assert.True(stack.Native)
	assert.Equal([]workspace.AgentStackMember{
		{Number: 41, State: "open", BaseBranch: "main"},
		{Number: 42, State: "open", Draft: true, BaseBranch: "feature/base"},
	}, stack.Members)

	unstacked, err := readStack(t.Context(), ws, 50)
	require.NoError(err, "a PR outside any stack is not a read failure")
	assert.Nil(unstacked)
}
