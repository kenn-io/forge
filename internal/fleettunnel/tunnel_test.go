package fleettunnel

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testNodeID = "20202020202020202020202020202020"

// hubFixture accepts spoke tunnels the way the hub does: one authenticated
// WebSocket per spoke, registered under the spoke's node ID.
type hubFixture struct {
	registry *Registry
	server   *httptest.Server
	served   chan error
}

func newHubFixture(t *testing.T) *hubFixture {
	t.Helper()
	hub := &hubFixture{registry: NewRegistry(), served: make(chan error, 4)}
	hub.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer spoke-secret" {
			http.Error(w, "unknown spoke", http.StatusUnauthorized)
			return
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		ctx := r.Context()
		hub.served <- hub.registry.Serve(
			ctx, testNodeID, websocket.NetConn(ctx, conn, websocket.MessageBinary),
		)
	}))
	t.Cleanup(hub.server.Close)
	return hub
}

func (h *hubFixture) connectSpoke(t *testing.T, handler http.Handler) context.CancelFunc {
	t.Helper()
	client, err := NewClient(ClientOptions{
		URL: h.server.URL,
		Header: func() (http.Header, error) {
			return http.Header{"Authorization": {"Bearer spoke-secret"}}, nil
		},
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
	require.Eventually(t, func() bool {
		return h.registry.Connected(testNodeID)
	}, 5*time.Second, 5*time.Millisecond)
	return cancel
}

func TestHubRequestsReachSpokeHandlerThroughTunnel(t *testing.T) {
	t.Parallel()
	hub := newHubFixture(t)
	hub.connectSpoke(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, r.Method+" "+r.Host+r.URL.RequestURI()+
			" from "+r.RemoteAddr+" auth="+r.Header.Get("Authorization"))
	}))

	client := &http.Client{Transport: hub.registry.Transport(testNodeID, nil, false)}
	request, err := http.NewRequestWithContext(
		t.Context(), http.MethodGet,
		"https://spoke.invalid/api/v1/snapshot/raw?x=1", nil,
	)
	require.NoError(t, err)
	request.Header.Set("Authorization", "Bearer hub-secret")
	response, err := client.Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)

	assert := assert.New(t)
	assert.Equal(http.StatusOK, response.StatusCode)
	assert.Equal(
		"GET spoke.invalid/api/v1/snapshot/raw?x=1 from fleet-tunnel auth=Bearer hub-secret",
		string(body),
	)
}

func TestHubWebSocketUpgradesThroughTunnel(t *testing.T) {
	t.Parallel()
	hub := newHubFixture(t)
	hub.connectSpoke(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		kind, message, err := conn.Read(r.Context())
		if err != nil {
			return
		}
		_ = conn.Write(r.Context(), kind, append([]byte("echo "), message...))
	}))

	conn, _, err := websocket.Dial(t.Context(), "wss://spoke.invalid/ws/v1/terminal", &websocket.DialOptions{
		HTTPClient: &http.Client{Transport: hub.registry.Transport(testNodeID, nil, false)},
	})
	require.NoError(t, err)
	defer conn.CloseNow()
	require.NoError(t, conn.Write(t.Context(), websocket.MessageText, []byte("hello")))
	_, message, err := conn.Read(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "echo hello", string(message))
}

func TestTransportFallsBackToDirectOnlyWhenAllowed(t *testing.T) {
	t.Parallel()
	direct := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "direct")
	}))
	t.Cleanup(direct.Close)
	registry := NewRegistry()

	withDirect := &http.Client{Transport: registry.Transport(testNodeID, nil, true)}
	response, err := withDirect.Get(direct.URL)
	require.NoError(t, err)
	body, err := io.ReadAll(response.Body)
	require.NoError(t, response.Body.Close())
	require.NoError(t, err)
	assert.Equal(t, "direct", string(body))

	tunnelOnly := &http.Client{Transport: registry.Transport(testNodeID, nil, false)}
	_, err = tunnelOnly.Get(direct.URL) //nolint:bodyclose // The request fails before a response exists.
	require.ErrorIs(t, err, ErrNotConnected)
}

func TestSpokeDisconnectRemovesTunnel(t *testing.T) {
	t.Parallel()
	hub := newHubFixture(t)
	disconnect := hub.connectSpoke(t, http.NotFoundHandler())

	disconnect()
	require.Eventually(t, func() bool {
		return !hub.registry.Connected(testNodeID)
	}, 5*time.Second, 5*time.Millisecond)
	_, err := hub.registry.Dial(t.Context(), testNodeID)
	require.ErrorIs(t, err, ErrNotConnected)
}

func TestNewerTunnelReplacesOlderOneForSameNode(t *testing.T) {
	t.Parallel()
	registry := NewRegistry()
	serveSpoke := func(reply string) <-chan error {
		hubEnd, spokeEnd := net.Pipe()
		go func() {
			_ = Serve(t.Context(), spokeEnd, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, reply)
			}))
		}()
		served := make(chan error, 1)
		go func() { served <- registry.Serve(t.Context(), testNodeID, hubEnd) }()
		return served
	}

	first := serveSpoke("first")
	require.Eventually(t, func() bool {
		return registry.Connected(testNodeID)
	}, 5*time.Second, 5*time.Millisecond)
	second := serveSpoke("second")

	select {
	case err := <-first:
		require.NoError(t, err, "the replaced tunnel ends without error")
	case <-time.After(5 * time.Second):
		require.FailNow(t, "the older tunnel kept running after replacement")
	}
	client := &http.Client{Transport: registry.Transport(testNodeID, nil, false)}
	response, err := client.Get("http://spoke.invalid/")
	require.NoError(t, err)
	body, err := io.ReadAll(response.Body)
	require.NoError(t, response.Body.Close())
	require.NoError(t, err)
	assert.Equal(t, "second", string(body))
	assert.Empty(t, second, "the replacing tunnel keeps running")
}
