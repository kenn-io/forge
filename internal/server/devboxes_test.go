package server

import (
	"encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/devbox"
	"go.kenn.io/forge/internal/fleet"
	"go.kenn.io/forge/internal/terminalwebsocket"
)

func TestDevboxSnapshotMaintenanceBlocksCreation(t *testing.T) {
	for _, maintenance := range []bool{false, true} {
		t.Run(fmt.Sprint(maintenance), func(t *testing.T) {
			assert, require := assert.New(t), require.New(t)
			worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal("Bearer worker-test-token", r.Header.Get("Authorization"))
				assert.NoError(json.MarshalWrite(w, fleet.RawSnapshot{NodeID: "worker-a"}))
			}))
			t.Cleanup(worker.Close)
			directory := t.TempDir()
			raw, err := json.Marshal([]any{map[string]any{
				"id": "compute-a", "profile": devbox.Profile{
					Assignment: devbox.Assignment{URL: worker.URL, Maintenance: maintenance, WorkerIdentity: devbox.WorkerIdentity{NodeID: "worker-a"}},
					Token:      "worker-test-token",
				},
			}})
			require.NoError(err)
			require.NoError(os.WriteFile(filepath.Join(directory, "devbox-connections.json"), raw, 0o600))
			connections, err := devbox.OpenConnections(directory)
			require.NoError(err)
			t.Cleanup(connections.Close)
			controller := &Server{options: ServerOptions{Devboxes: connections}}
			local := fleet.RawSnapshot{NodeID: "controller"}
			aggregate := fleet.BuildNeutralAggregate(local, controller.devboxSnapshots(t.Context(), time.Second))
			snapshot := fleet.ProjectForObserver(aggregate, local, fleet.Observer{NodeID: local.NodeID, Role: fleet.RoleHub})
			require.Len(snapshot.Hosts, 2)
			host := snapshot.Hosts[1]
			assert.True(host.Reachable)
			assert.Equal(!maintenance, host.OperationAvailability[fleet.OpWorkspaceWrite].Available)
			assert.True(host.OperationAvailability[fleet.OpWorkspaceRead].Available)
			if maintenance {
				require.NotNil(host.OperationAvailability[fleet.OpWorkspaceWrite].UnavailableReason)
				assert.Contains(*host.OperationAvailability[fleet.OpWorkspaceWrite].UnavailableReason, "maintenance")
			}
		})
	}
}

func TestDevboxTerminalsBypassDefaultHTTPProxy(t *testing.T) {
	assert, require := assert.New(t), require.New(t)
	var proxyRequests atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		proxyRequests.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(proxy.Close)
	proxyURL, err := url.Parse(proxy.URL)
	require.NoError(err)
	// Exercise real proxy routing without Go's cached environment lookup or its
	// loopback exception; all fixture listeners remain on loopback.
	defaultTransport := http.DefaultTransport
	proxied := &http.Transport{Proxy: http.ProxyURL(proxyURL)}
	http.DefaultTransport = proxied
	t.Cleanup(func() { http.DefaultTransport = defaultTransport; proxied.CloseIdleConnections() })
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal("Bearer worker-test-token", r.Header.Get("Authorization"))
		conn, err := terminalwebsocket.Accept(w, r)
		if !assert.NoError(err) {
			return
		}
		defer conn.CloseNow()
		assert.NoError(conn.Write(r.Context(), websocket.MessageText, []byte(r.URL.RequestURI())))
		_, _, _ = conn.Read(r.Context())
	}))
	t.Cleanup(worker.Close)
	directory := t.TempDir()
	raw, err := json.Marshal([]any{map[string]any{
		"id": "compute-a", "profile": devbox.Profile{
			Assignment: devbox.Assignment{URL: worker.URL}, Token: "worker-test-token",
		},
	}})
	require.NoError(err)
	require.NoError(os.WriteFile(filepath.Join(directory, "devbox-connections.json"), raw, 0o600))
	connections, err := devbox.OpenConnections(directory)
	require.NoError(err)
	t.Cleanup(connections.Close)
	controller := &Server{options: ServerOptions{Devboxes: connections}}
	mux := http.NewServeMux()
	controller.registerDevboxTerminalAPI(humago.NewWithPrefix(mux, "/ws/v1", huma.DefaultConfig("test", "1")))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	direct := &http.Transport{}
	t.Cleanup(direct.CloseIdleConnections)
	for _, path := range []string{"/workspaces/work-a/terminal", "/workspaces/work-a/runtime/sessions/session-a/terminal"} {
		conn, _, err := terminalwebsocket.Dial(t.Context(), "ws"+strings.TrimPrefix(server.URL, "http")+"/ws/v1/devboxes/compute-a"+path+"?cols=100", nil, &http.Client{Transport: direct})
		require.NoError(err)
		_, message, err := conn.Read(t.Context())
		require.NoError(err)
		assert.Equal("/ws/v1"+path+"?cols=100", string(message))
		require.NoError(conn.Close(websocket.StatusNormalClosure, "done"))
	}
	assert.Zero(proxyRequests.Load(), "worker credentials and terminal traffic must bypass the default proxy")
}
