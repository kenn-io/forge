package server

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/gitclone"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/testutil/dbtest"
	serverfake "go.kenn.io/forge/internal/testutil/serverfake"
	"go.kenn.io/forge/platform"
)

type countingNodeRoleTransport struct {
	requests atomic.Int32
}

func (t *countingNodeRoleTransport) RoundTrip(*http.Request) (*http.Response, error) {
	t.requests.Add(1)
	return nil, assert.AnError
}

func TestInactiveFleetNodeKeepsLocalServicesWithoutProviderPlane(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	transport := &countingNodeRoleTransport{}
	clones := gitclone.New(t.TempDir(), nil)
	cfg := &config.Config{Fleet: config.Fleet{
		Enabled: true, Role: config.FleetRoleSpoke,
		Hub: &config.FleetHub{
			NodeID: proxyTestHubID, BaseURL: "https://hub.example",
		},
	}}
	srv := New(dbtest.Open(t), nil, nil, "/", cfg, ServerOptions{
		FederationSpokeID:                  proxyTestNodeID,
		FederationSpokeUnavailableReason:   "activation required",
		FederationHTTPClient:               &http.Client{Transport: transport},
		Clones:                             clones,
		WorktreeDir:                        t.TempDir(),
		HostCheckAllowLoopbackAnyPort:      true,
		DisableWorkspaceBackgroundMonitors: true,
	})
	t.Cleanup(func() { serverfake.GracefulShutdown(t, srv) })

	assert.Nil(srv.archive)
	assert.Same(clones, srv.clones)
	require.NotNil(srv.providerSource)
	assert.Nil(srv.providerSource.Client)
	assert.Nil(srv.providerProxy)
	assert.Nil(srv.hubEvents)
	assert.Equal(httpapi.ProviderCapabilitiesResponse{}, srv.repoResolver.Capabilities(
		platform.KindGitHub, platform.DefaultGitHubHost,
	))
	assert.NotNil(srv.MCPBackend())

	httpServer := httptest.NewServer(srv)
	t.Cleanup(httpServer.Close)
	for _, path := range []string{
		"/api/v1/workspaces",
		"/api/v1/snapshot?include_peers=true",
	} {
		responseReq, err := http.NewRequestWithContext(t.Context(), http.MethodGet, httpServer.URL+path, nil)
		require.NoError(err)
		httpClient := httpServer.Client()
		httpClient.Timeout = 5 * time.Second
		response, err := httpClient.Do(responseReq)
		require.NoError(err)
		response.Body.Close()
		assert.Equal(http.StatusOK, response.StatusCode, path)
	}
	providerResponseReq, err := http.NewRequestWithContext(t.Context(), http.MethodGet, httpServer.URL+"/api/v1/pulls", nil)
	require.NoError(err)
	httpClient := httpServer.Client()
	httpClient.Timeout = 5 * time.Second
	providerResponse, err := httpClient.Do(providerResponseReq)
	require.NoError(err)
	providerResponse.Body.Close()
	assert.Equal(http.StatusServiceUnavailable, providerResponse.StatusCode)
	assert.Zero(transport.requests.Load(),
		"an unvalidated spoke must not contact its configured hub")
}
