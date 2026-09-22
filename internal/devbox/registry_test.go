package devbox

import (
	"context"
	"encoding/json/v2"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRegistryConnectCachesAccountAndRejectsChangedWorker(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	identity := WorkerIdentity{NodeID: "0123456789abcdef0123456789abcdef", UID: 1001, GitHubUserID: 42, Role: "execution", Protocol: Protocol}
	token := strings.Repeat("a", 64)
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal("Bearer "+token, r.Header.Get("Authorization"))
		assert.Empty(r.Header.Get("Cookie"))
		if r.URL.Path == "/api/v1/worker" {
			assert.NoError(json.MarshalWrite(w, identity))
			return
		}
		_, _ = w.Write([]byte(`{"status":"ready"}`))
	}))
	t.Cleanup(worker.Close)
	assignment := Assignment{WorkerIdentity: identity, HostID: "compute-a", Name: "Compute A", URL: "http://compute-a.example.ts.net:9001", Account: "developer-a", Enabled: true, TokenFile: "/run/example/token"}
	cfg := RegistryConfig{Socket: "/run/example/registry.sock", RegistryID: "example-registry", Revision: "1", Developers: []Developer{{GitHubUserID: 42, TailscaleUserID: 11, Enabled: true}}, Assignments: []Assignment{assignment}}
	require.NoError(cfg.validate())
	lookup := func(context.Context, string) (tailnetPeer, error) {
		peer := tailnetPeer{}
		peer.Node.StableID = "controller-a"
		peer.UserProfile.ID = 11
		return peer, nil
	}
	handler := registryHandler(cfg, map[string]string{identity.NodeID: token}, lookup)
	registry := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Header.Set("X-Devbox-Peer-IP", "100.64.0.2")
		r.Header.Set("X-Devbox-Peer-Port", "12345")
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(registry.Close)
	store, err := OpenConnections(t.TempDir())
	require.NoError(err)
	t.Cleanup(store.Close)
	transport := registry.Client().Transport.(*http.Transport).Clone()
	transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if strings.HasPrefix(addr, "compute-a.example.ts.net:") {
			addr = worker.Listener.Addr().String()
		}
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}
	store.client.Transport = transport
	discovery, err := store.Discover(t.Context(), registry.URL)
	require.NoError(err)
	assert.Equal(int64(42), discovery.GitHubUserID)
	connection, err := store.Connect(t.Context(), registry.URL, "compute-a")
	require.NoError(err)
	_, err = store.Connect(t.Context(), registry.URL, "compute-a")
	require.NoError(err)
	assert.Len(store.List(), 1)
	public, err := json.Marshal(store.List())
	require.NoError(err)
	assert.NotContains(string(public), token)
	assert.NotContains(string(public), "token_file")
	info, err := os.Stat(store.path)
	require.NoError(err)
	assert.Equal(os.FileMode(0o600), info.Mode().Perm())
	identity.GitHubUserID = 99
	_, err = store.Connect(t.Context(), registry.URL, "compute-a")
	require.ErrorContains(err, "identity mismatch")
	registry.Close()
	response, err := store.Do(t.Context(), connection.ID, "GET", "/api/v1/workspaces", nil, http.Header{"Authorization": {"browser-secret"}, "Cookie": {"forge_auth=browser-secret"}})
	require.NoError(err)
	defer response.Body.Close()
	assert.Equal(200, response.StatusCode)
}

func TestRegistryIdentityUsesUserOrExplicitUnambiguousPin(t *testing.T) {
	require := require.New(t)
	cfg := RegistryConfig{Developers: []Developer{
		{GitHubUserID: 42, TailscaleUserID: 11, ControllerNodeIDs: []string{"pinned-controller"}, Enabled: true},
		{GitHubUserID: 43, TailscaleUserID: 12, Enabled: true},
	}}
	peer := tailnetPeer{}
	peer.UserProfile.ID = 11
	id, err := cfg.developer(peer)
	require.NoError(err)
	require.Equal(int64(42), id)
	peer.Node.Tags = []string{"tag:shared"}
	_, err = cfg.developer(peer)
	require.ErrorContains(err, "not enrolled")
	peer.Node.StableID = "pinned-controller"
	id, err = cfg.developer(peer)
	require.NoError(err)
	require.Equal(int64(42), id)
	peer.Node.Tags, peer.UserProfile.ID = nil, 12
	_, err = cfg.developer(peer)
	require.ErrorContains(err, "conflicts")
}
