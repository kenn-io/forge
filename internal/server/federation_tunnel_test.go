package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/federationauth"
	"go.kenn.io/forge/internal/fleettunnel"
	"go.kenn.io/forge/internal/providerplane"
	"go.kenn.io/forge/internal/server/authapi"
	"go.kenn.io/forge/internal/testutil/dbtest"
	serverfake "go.kenn.io/forge/internal/testutil/serverfake"
)

const tunnelTestHubNodeID = "66666666666666666666666666666666"

// connectTestTunnel opens a spoke tunnel to hubURL whose requests reach handler.
func connectTestTunnel(
	t *testing.T, hubURL string, header http.Header, handler http.Handler,
) {
	t.Helper()
	client, err := fleettunnel.NewClient(fleettunnel.ClientOptions{
		URL:        hubURL + "/api/v1/federation/tunnel",
		Header:     func() (http.Header, error) { return header.Clone(), nil },
		Handler:    handler,
		RetryDelay: func(int) time.Duration { return 10 * time.Millisecond },
	})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		client.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
}

func spokeTunnelHeader(token string) http.Header {
	header := make(http.Header)
	header.Set("Authorization", "Bearer "+token)
	header.Set(federationauth.NodeIDHeader, serverfake.FederationEventTestNodeID)
	header.Set(providerplane.ProtocolVersionHeader, providerplane.ProtocolVersionHeaderValue())
	return header
}

func TestHubCarriesRequestsThroughSpokeTunnelUntilRevocation(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	hub, hubHTTP, spokeToken := newFederationEventServer(t)
	connectTestTunnel(t, hubHTTP.URL, spokeTunnelHeader(spokeToken),
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, "spoke answered "+r.URL.Path)
		}))
	require.Eventually(t, func() bool {
		return hub.tunnels.Connected(serverfake.FederationEventTestNodeID)
	}, 5*time.Second, 5*time.Millisecond)

	client := &http.Client{Transport: hub.tunnels.Transport(serverfake.FederationEventTestNodeID, nil)}
	response, err := client.Get("https://spoke.invalid/api/v1/snapshot/raw")
	require.NoError(t, err)
	body, err := io.ReadAll(response.Body)
	require.NoError(t, response.Body.Close())
	require.NoError(t, err)
	assert.Equal(t, "spoke answered /api/v1/snapshot/raw", string(body))

	// Revoking a spoke cancels its federation streams, which include the tunnel.
	hub.syncevents.CancelFederationEventStreams(serverfake.FederationEventTestNodeID)
	require.Eventually(t, func() bool {
		return !hub.tunnels.Connected(serverfake.FederationEventTestNodeID)
	}, 5*time.Second, 5*time.Millisecond)
}

func TestHubRejectsTunnelsWithoutSpokeCredentialOrProtocol(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	_, hubHTTP, spokeToken := newFederationEventServer(t)
	withoutProtocol := spokeTunnelHeader(spokeToken)
	withoutProtocol.Del(providerplane.ProtocolVersionHeader)

	for _, tc := range []struct {
		name   string
		header http.Header
		status int
	}{
		{
			name:   "local daemon token is not a spoke",
			header: http.Header{"Authorization": {"Bearer local-daemon-secret"}},
			status: http.StatusForbidden,
		},
		{name: "missing protocol header", header: withoutProtocol, status: http.StatusConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request, err := http.NewRequestWithContext(
				t.Context(), http.MethodGet, hubHTTP.URL+"/api/v1/federation/tunnel", nil,
			)
			require.NoError(t, err)
			request.Header = tc.header.Clone()
			request.Header.Set("Connection", "Upgrade")
			request.Header.Set("Upgrade", "websocket")
			request.Header.Set("Sec-WebSocket-Version", "13")
			request.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
			response, err := hubHTTP.Client().Do(request)
			require.NoError(t, err)
			require.NoError(t, response.Body.Close())
			assert.Equal(t, tc.status, response.StatusCode)
		})
	}
}

func TestSpokeTunnelAdmitsOnlyTheHubCredential(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	credentials, err := federationauth.Open(t.TempDir() + "/credentials.json")
	require.NoError(t, err)
	hubToken, err := credentials.MintInbound(
		tunnelTestHubNodeID, federationauth.HubToSpokeScopes(),
	)
	require.NoError(t, err)
	otherPeerToken, err := credentials.MintInbound(
		serverfake.FederationEventTestNodeID, federationauth.HubToSpokeScopes(),
	)
	require.NoError(t, err)
	spoke := New(dbtest.Open(t), nil, nil, "/", nil, ServerOptions{
		DaemonAccess: authapi.DaemonAccessOptions{
			Token: "local-daemon-secret", RequireAPIAuth: true,
		},
		FederationCredentials: credentials,
		HostCheck: authapi.HostCheckOptions{
			Bind: config.HostKey{Host: "127.0.0.1", Port: "8091"},
		},
		DisableWorkspaceBackgroundMonitors: true,
	})
	t.Cleanup(func() { serverfake.GracefulShutdown(t, spoke) })
	tunnel := spoke.hubTunnelHandler(tunnelTestHubNodeID)

	serve := func(handler http.Handler, bearer string, path string) int {
		// The hub addresses the spoke by its public origin, which this
		// listener's Host allowlist does not accept for direct requests.
		request := httptest.NewRequestWithContext(
			t.Context(), http.MethodGet, "https://spoke.example.test"+path, nil,
		)
		request.RemoteAddr = fleettunnel.Addr{}.String()
		request.Header.Set("Authorization", "Bearer "+bearer)
		request.AddCookie(&http.Cookie{Name: authapi.AuthCookieName, Value: "local-daemon-secret"})
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder.Code
	}

	assert := assert.New(t)
	const raw = "/api/v1/snapshot/raw"
	assert.Equal(http.StatusOK, serve(tunnel, hubToken, raw), "the hub's tunneled request is served")
	assert.Equal(http.StatusForbidden, serve(spoke, hubToken, raw), "a direct request still needs an allowed Host")
	assert.Equal(http.StatusForbidden, serve(tunnel, hubToken, "/api/v1/settings"),
		"a daemon cookie cannot lift the hub's request beyond its federation scopes")
	assert.Equal(http.StatusUnauthorized, serve(tunnel, otherPeerToken, raw), "another peer cannot use the hub's tunnel")
	assert.Equal(http.StatusUnauthorized, serve(tunnel, "local-daemon-secret", raw), "the daemon token cannot use the tunnel")
}
