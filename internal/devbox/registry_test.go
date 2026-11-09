package devbox

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/BurntSushi/toml"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRegistryConnectSharedAndPersonalWorkers(t *testing.T) {
	for _, workerID := range []int64{42, 777} {
		t.Run(fmt.Sprintf("worker-%d", workerID), func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			identity := WorkerIdentity{NodeID: "0123456789abcdef0123456789abcdef", UID: 1001, GitHubUserID: workerID, Role: "execution", Protocol: Protocol}
			token := strings.Repeat("a", 64)
			var workerToken atomic.Value
			workerToken.Store(token)
			worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				bearer := r.Header.Get("Authorization")
				if bearer != "Bearer "+workerToken.Load().(string) {
					http.Error(w, "invalid worker bearer", http.StatusUnauthorized)
					return
				}
				assert.Empty(r.Header.Get("Cookie"))
				if r.URL.Path == "/api/v1/worker" {
					assert.NoError(json.MarshalWrite(w, identity))
					return
				}
				_, _ = w.Write([]byte(`{"status":"ready"}`))
			}))
			t.Cleanup(worker.Close)
			workerSetting, account := "", "developer-a"
			if workerID != 42 {
				workerSetting = fmt.Sprintf("worker_github_user_id = %d", workerID)
				account = "service"
			}
			var assignment Assignment
			_, err := toml.Decode(fmt.Sprintf(`
		node_id = "0123456789abcdef0123456789abcdef"
		uid = 1001
		github_user_id = 42
		%s
		role = "execution"
		protocol = 1
		host_id = "compute-a"
		name = "Compute A"
		url = "http://compute-a.example.ts.net:9001"
		account = %q
		enabled = true
		token_file = "/run/example/token"
		`, workerSetting, account), &assignment)
			require.NoError(err)
			cfg := RegistryConfig{Socket: "/run/example/registry.sock", RegistryID: "example-registry", Revision: "1", Developers: []Developer{{GitHubUserID: 42, TailscaleUserID: 11, Enabled: true}, {GitHubUserID: 43, TailscaleUserID: 12, Enabled: true}}, Assignments: []Assignment{assignment}}
			if workerID == 777 {
				second := assignment
				second.GitHubUserID = 43
				second.Name = "Shared compute"
				cfg.Assignments = append(cfg.Assignments, second)
			}
			require.NoError(cfg.validate())
			peerID := int64(11)
			lookup := func(context.Context, string) (tailnetPeer, error) {
				peer := tailnetPeer{}
				peer.Node.StableID = "controller-a"
				peer.UserProfile.ID = peerID
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
			require.Len(discovery.Devboxes, 1)
			assert.Equal(int64(42), discovery.Devboxes[0].GitHubUserID)
			connection, err := store.Connect(t.Context(), registry.URL, "compute-a")
			require.NoError(err)
			if workerID == 777 {
				assert.Equal(int64(777), discovery.Devboxes[0].WorkerGitHubUserID)
			}
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
			peerID = 12
			if workerID == 777 {
				secondStore, err := OpenConnections(t.TempDir())
				require.NoError(err)
				t.Cleanup(secondStore.Close)
				secondStore.client.Transport = transport.Clone()
				discovery, err = secondStore.Discover(t.Context(), registry.URL)
				require.NoError(err)
				require.Len(discovery.Devboxes, 1)
				assert.Equal(int64(43), discovery.Devboxes[0].GitHubUserID)
				assert.Equal(workerID, discovery.Devboxes[0].WorkerGitHubUserID)
				_, err = secondStore.Connect(t.Context(), registry.URL, "compute-a")
				require.NoError(err)
				cfg.Assignments[1].Enabled = false
				discovery, err = secondStore.Discover(t.Context(), registry.URL)
				require.NoError(err)
				assert.Empty(discovery.Devboxes)
				_, err = secondStore.Connect(t.Context(), registry.URL, "compute-a")
				require.ErrorContains(err, "not assigned")
				cfg.Assignments[1].Enabled = true
				newToken := strings.Repeat("b", 64)
				workerToken.Store(newToken)
				handler = registryHandler(cfg, map[string]string{identity.NodeID: newToken}, lookup)
				secondConnection, err := secondStore.Connect(t.Context(), registry.URL, "compute-a")
				require.NoError(err)
				response, err := secondStore.Do(t.Context(), secondConnection.ID, http.MethodGet, "/api/v1/workspaces", nil, nil)
				require.NoError(err)
				assert.Equal(http.StatusOK, response.StatusCode)
				require.NoError(response.Body.Close())
				peerID = 11
				connection, err = store.Connect(t.Context(), registry.URL, "compute-a")
				require.NoError(err)
			} else {
				discovery, err = store.Discover(t.Context(), registry.URL)
				require.NoError(err)
				assert.Empty(discovery.Devboxes)
				_, err = store.Connect(t.Context(), registry.URL, "compute-a")
				require.ErrorContains(err, "not assigned")
			}
			peerID = 11
			identity.GitHubUserID = 99
			_, err = store.Connect(t.Context(), registry.URL, "compute-a")
			require.ErrorContains(err, "identity mismatch")
			registry.Close()
			response, err := store.Do(t.Context(), connection.ID, "GET", "/api/v1/workspaces", nil, http.Header{"Authorization": {"browser-secret"}, "Cookie": {"forge_auth=browser-secret"}})
			require.NoError(err)
			defer response.Body.Close()
			assert.Equal(200, response.StatusCode)
		})
	}
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

func TestRegistrySharedServiceWorkerValidation(t *testing.T) {
	assignment := Assignment{NodeID: "0123456789abcdef0123456789abcdef", UID: 1001, GitHubUserID: 42, WorkerGitHubUserID: 777, Role: "execution", Protocol: Protocol, HostID: "compute-a", Name: "Compute A", URL: "http://compute-a.example.ts.net:9001", Account: "service", Enabled: true, TokenFile: "/run/example/token"}
	second := assignment
	second.GitHubUserID = 43
	second.Name = "Shared compute"
	cfg := RegistryConfig{Socket: "/run/example/registry.sock", RegistryID: "example", Revision: "1", Developers: []Developer{{GitHubUserID: 42, TailscaleUserID: 11, Enabled: true}, {GitHubUserID: 43, TailscaleUserID: 12, Enabled: true}}, Assignments: []Assignment{assignment, second}}
	require.NoError(t, cfg.validate())
	cfg.Assignments[1].Enabled = false
	require.NoError(t, cfg.validate())
	for name, change := range map[string]func(*Assignment){
		"worker identity": func(a *Assignment) { a.WorkerGitHubUserID = 778 },
		"worker details":  func(a *Assignment) { a.TokenFile = "/run/example/other" },
		"maintenance":     func(a *Assignment) { a.Maintenance = true },
	} {
		t.Run(name, func(t *testing.T) {
			other := second
			change(&other)
			cfg.Assignments[1] = other
			assert.ErrorContains(t, cfg.validate(), "same service worker")
		})
	}
	t.Run("personal workers", func(t *testing.T) {
		cfg.Assignments[1] = second
		cfg.Assignments[0].WorkerGitHubUserID = 0
		cfg.Assignments[1].WorkerGitHubUserID = 0
		require.ErrorContains(t, cfg.validate(), "same service worker")
	})
}
