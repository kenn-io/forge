package fleetapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/server/workspaceapi"
)

func TestHTTPFleetProxyRelaysPastedImageJSON(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	require := require.New(t)
	png := []byte("\x89PNG\r\n\x1a\nfleet http fixture")
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal("/api/v1/workspaces/ws_1/pasted-images", r.URL.Path)
		assert.Equal("application/json", r.Header.Get("Content-Type"))
		var request struct {
			Data string `json:"data"`
		}
		if !assert.NoError(json.NewDecoder(r.Body).Decode(&request)) {
			return
		}
		decoded, err := base64.StdEncoding.DecodeString(request.Data)
		if !assert.NoError(err) {
			return
		}
		assert.Equal(png, decoded)
		w.Header().Set("Content-Type", "application/json")
		_, err = io.WriteString(w, `{"path":".kenn-forge/pasted-images/paste-http.png"}`)
		assert.NoError(err)
	}))
	t.Cleanup(peer.Close)

	srv, _ := setupTestServer(t)
	setTestFleetConfig(srv, func(cfg *config.Config) {
		cfg.Fleet.Enabled = true
		cfg.Fleet.Peers = []config.FleetPeer{{Key: "member", BaseURL: peer.URL}}
	})
	hub := httptest.NewServer(srv.localHandler())
	t.Cleanup(hub.Close)
	body, err := json.Marshal(map[string]string{"data": base64.StdEncoding.EncodeToString(png)})
	require.NoError(err)

	response := httpDo(t, hub, http.MethodPost,
		"/api/v1/fleet/hosts/member/workspaces/ws_1/pasted-images", body)
	defer response.Body.Close()
	require.Equal(http.StatusOK, response.StatusCode)
	var output struct {
		Path string `json:"path"`
	}
	require.NoError(json.NewDecoder(response.Body).Decode(&output))
	assert.Equal(".kenn-forge/pasted-images/paste-http.png", output.Path)
}

func TestFleetPastedImageRejectsOversizedBodyBeforeRelay(t *testing.T) {
	t.Parallel()
	var relayed atomic.Bool
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		relayed.Store(true)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(peer.Close)

	srv, _ := setupTestServer(t)
	setTestFleetConfig(srv, func(cfg *config.Config) {
		cfg.Fleet.Enabled = true
		cfg.Fleet.Peers = []config.FleetPeer{{Key: "member", BaseURL: peer.URL}}
	})
	hub := httptest.NewServer(srv.localHandler())
	t.Cleanup(hub.Close)
	body := `{"data":"` + strings.Repeat("A", int(workspaceapi.MaxPastedImageRequestBytes)) + `"}`

	response, err := http.Post(
		hub.URL+"/api/v1/fleet/hosts/member/workspaces/ws_1/pasted-images",
		"application/json",
		strings.NewReader(body),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = response.Body.Close() })

	assert.Equal(t, http.StatusRequestEntityTooLarge, response.StatusCode)
	assert.False(t, relayed.Load())
}

func TestFleetWebSocketProxyNegotiatesContextTakeoverOnBothLegs(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	peerExtensions := make(chan string, 1)
	peerErrors := make(chan error, 1)
	peer := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
				InsecureSkipVerify: true,
				CompressionMode:    websocket.CompressionContextTakeover,
			})
			if err != nil {
				peerErrors <- err
				return
			}
			defer conn.Close(websocket.StatusNormalClosure, "done")
			peerExtensions <- w.Header().Get("Sec-WebSocket-Extensions")

			typ, payload, err := conn.Read(r.Context())
			if err != nil {
				peerErrors <- err
				return
			}
			if err := conn.Write(r.Context(), typ, payload); err != nil {
				peerErrors <- err
			}
		},
	))
	t.Cleanup(peer.Close)

	srv, _ := setupTestServer(t)
	setTestFleetConfig(srv, func(cfg *config.Config) {
		cfg.Fleet.Enabled = true
		cfg.Fleet.Peers = []config.FleetPeer{
			{Key: "member", BaseURL: peer.URL},
		}
	})
	hub := httptest.NewServer(srv.localHandler())
	t.Cleanup(hub.Close)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	wsURL := "ws" + strings.TrimPrefix(hub.URL, "http") +
		"/ws/v1/fleet/hosts/member/workspaces/ws_1/runtime/sessions/sess-1/terminal"
	conn, resp, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		CompressionMode: websocket.CompressionContextTakeover,
	})
	require.NoError(err)
	defer conn.Close(websocket.StatusNormalClosure, "done")
	require.NotNil(resp)

	clientExtensions := resp.Header.Get("Sec-WebSocket-Extensions")
	assert.Contains(clientExtensions, "permessage-deflate")
	assert.NotContains(clientExtensions, "client_no_context_takeover")
	assert.NotContains(clientExtensions, "server_no_context_takeover")

	select {
	case extensions := <-peerExtensions:
		assert.Contains(extensions, "permessage-deflate")
		assert.NotContains(extensions, "client_no_context_takeover")
		assert.NotContains(extensions, "server_no_context_takeover")
	case err := <-peerErrors:
		require.NoError(err)
	case <-ctx.Done():
		require.Fail("peer websocket handshake did not complete")
	}

	want := []byte("fleet-compression-round-trip")
	require.NoError(conn.Write(ctx, websocket.MessageBinary, want))
	typ, got, err := conn.Read(ctx)
	require.NoError(err)
	assert.Equal(websocket.MessageBinary, typ)
	assert.Equal(want, got)
}

// TestCopyProxyRequestHeadersStripsBrowserHeaders verifies the hub does not
// forward a browser's Origin or Sec-Fetch-* metadata onto a server-to-server
// fleet proxy request. Forwarding them trips the peer's host-authority guard,
// which validates Origin against its own allowed hosts and rejects the
// fan-out because the origin is the hub, not the peer. It also verifies the
// caller's Authorization and Cookie are stripped: they authenticate the hub,
// not the peer, so forwarding them only leaks the hub credential.
func TestCopyProxyRequestHeadersStripsBrowserHeaders(t *testing.T) {
	assert := assert.New(t)
	src := http.Header{}
	src.Set("Origin", "http://hub.local:8091")
	src.Set("Sec-Fetch-Site", "same-origin")
	src.Set("Sec-Fetch-Mode", "cors")
	src.Set("Authorization", "Bearer token")
	src.Set("Content-Type", "application/json")
	src.Set("Cookie", "session=abc")
	src.Set("Connection", "keep-alive") // hop-by-hop
	src.Set("Forwarded", "host=hub.local:8091;proto=https")
	src.Set("X-Forwarded-Host", "hub.local:8091")
	src.Set("X-Forwarded-Proto", "https")

	dst := http.Header{}
	copyProxyRequestHeaders(dst, src)

	assert.Empty(dst.Get("Origin"), "browser Origin must not reach the peer")
	assert.Empty(dst.Get("Sec-Fetch-Site"), "Sec-Fetch-* must not reach the peer")
	assert.Empty(dst.Get("Sec-Fetch-Mode"), "Sec-Fetch-* must not reach the peer")
	assert.Empty(dst.Get("Connection"), "hop-by-hop headers are still dropped")
	assert.Empty(dst.Get("Forwarded"), "forwarded host metadata must not reach the peer")
	assert.Empty(dst.Get("X-Forwarded-Host"), "forwarded host metadata must not reach the peer")
	assert.Empty(dst.Get("X-Forwarded-Proto"), "forwarded proxy metadata must not reach the peer")
	assert.Empty(dst.Get("Authorization"), "the hub credential must not leak to the peer")
	assert.Empty(dst.Get("Cookie"), "the hub session cookie must not leak to the peer")
	assert.Equal("application/json", dst.Get("Content-Type"), "content type must pass through")
}

// TestCopyProxyWebSocketRequestHeadersStripsBrowserHeaders verifies the same
// browser-header stripping applies to fleet websocket dials, on top of the
// existing Sec-WebSocket-* exclusion the dialer sets itself.
func TestCopyProxyWebSocketRequestHeadersStripsBrowserHeaders(t *testing.T) {
	assert := assert.New(t)
	src := http.Header{}
	src.Set("Origin", "http://hub.local:8091")
	src.Set("Sec-Fetch-Dest", "websocket")
	src.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	src.Set("Authorization", "Bearer token")
	src.Set("Cookie", "forge_auth=abc")
	src.Set("Forwarded", "host=hub.local:8091")
	src.Set("X-Forwarded-Host", "hub.local:8091")

	dst := http.Header{}
	copyProxyWebSocketRequestHeaders(dst, src)

	assert.Empty(dst.Get("Origin"), "browser Origin must not reach the peer")
	assert.Empty(dst.Get("Sec-Fetch-Dest"), "Sec-Fetch-* must not reach the peer")
	assert.Empty(dst.Get("Sec-WebSocket-Key"), "Sec-WebSocket-* stays dialer-owned")
	assert.Empty(dst.Get("Forwarded"), "forwarded host metadata must not reach the peer")
	assert.Empty(dst.Get("X-Forwarded-Host"), "forwarded host metadata must not reach the peer")
	assert.Empty(dst.Get("Authorization"), "the hub credential must not leak to the peer")
	assert.Empty(dst.Get("Cookie"), "the hub session cookie must not leak to the peer")
}

func TestIsPeerProxyClientHeader(t *testing.T) {
	for _, tc := range []struct {
		key  string
		want bool
	}{
		{"Origin", true},
		{"origin", true},
		{"Sec-Fetch-Site", true},
		{"sec-fetch-mode", true},
		{"Sec-Fetch-Dest", true},
		{"Forwarded", true},
		{"X-Forwarded-Host", true},
		{"x-forwarded-proto", true},
		{"X-Forwarded-For", true},
		{"Authorization", false},
		{"Content-Type", false},
		{"Sec-WebSocket-Key", false},
		{"X-Kenn-Forge-Fleet-Host", false},
	} {
		assert.Equal(t, tc.want, isPeerProxyClientHeader(tc.key), "header %q", tc.key)
	}
}

func TestIsPeerProxyCredentialHeader(t *testing.T) {
	for _, tc := range []struct {
		key  string
		want bool
	}{
		{"Authorization", true},
		{"authorization", true},
		{"Cookie", true},
		{"cookie", true},
		{"Content-Type", false},
		{"Origin", false},
		{"X-Kenn-Forge-Fleet-Host", false},
	} {
		assert.Equal(t, tc.want, isPeerProxyCredentialHeader(tc.key), "header %q", tc.key)
	}
}

func TestResolveFleetHostTargetSkipsRemotePeersWhenFederationDisabled(t *testing.T) {
	assert := assert.New(t)
	srv := &Handler{
		config: ConfigSnapshot{
			Fleet: config.Fleet{
				Key: "hub",
				Peers: []config.FleetPeer{
					{Key: "member", BaseURL: "http://member.test"},
				},
			},
		},
	}

	_, ok := srv.resolveFleetHostTarget("member")
	assert.False(ok, "disabled federation must not resolve remote HTTP peers")

	self, ok := srv.resolveFleetHostTarget(fleetSelfHostAlias)
	require.True(t, ok, "disabled federation must preserve self routing")
	assert.True(self.self)
}

func TestResolveFleetHostTargetUsesRemotePeersWhenFederationEnabled(t *testing.T) {
	assert := assert.New(t)
	srv := &Handler{
		config: ConfigSnapshot{
			Fleet: config.Fleet{
				Enabled: true,
				Key:     "hub",
				Peers: []config.FleetPeer{
					{Key: "member", BaseURL: "http://member.test"},
				},
			},
		},
	}

	target, ok := srv.resolveFleetHostTarget("member")
	require.True(t, ok)
	assert.Equal("member", target.peer.Key)
}
