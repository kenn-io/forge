package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/federationauth"
	"go.kenn.io/forge/internal/testutil/dbtest"
)

func TestHasReadableProviderWorkFromExecutionWorker(t *testing.T) {
	database := dbtest.Open(t)
	srv := New(database, nil, nil, "/", nil, ServerOptions{
		ExecutionWorker: true, DisableWorkspaceBackgroundMonitors: true,
	})
	t.Cleanup(func() { require.NoError(t, srv.Shutdown(context.Background())) })

	readable, err := srv.HasReadableProviderWork(t.Context())
	require.NoError(t, err)
	require.False(t, readable)
}

func TestHasReadableProviderWorkFromSpokeProviderSource(t *testing.T) {
	for _, test := range []struct {
		kind   string
		active bool
	}{
		{"pr", true},
		{"issue", true},
		{"inactive", false},
	} {
		t.Run(test.kind, func(t *testing.T) {
			require := require.New(t)
			database := dbtest.Open(t)
			var requests atomic.Int64
			hub := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.Header().Set("Content-Type", "application/json")
				if test.kind == "pr" && r.URL.Path == "/api/v1/pulls" || test.kind == "issue" && r.URL.Path == "/api/v1/issues" {
					_, _ = io.WriteString(w, `[{"number":42,"title":"Example","state":"open","repo":{"provider":"github","platform_host":"github.com","owner":"user-a","name":"project-a"}}]`)
					return
				}
				_, _ = io.WriteString(w, `[]`)
			}))
			t.Cleanup(hub.Close)
			const hubID = "44444444444444444444444444444444"
			credentials, err := federationauth.Open(filepath.Join(t.TempDir(), "credentials.json"))
			require.NoError(err)
			require.NoError(credentials.StoreOutbound(hubID, "example-secret", federationauth.SpokeToHubScopes()))
			cfg := &config.Config{Host: "127.0.0.1", Port: 8080, Fleet: config.Fleet{Enabled: true, Role: config.FleetRoleSpoke, Hub: &config.FleetHub{NodeID: hubID, BaseURL: hub.URL}}}
			srv := New(database, nil, nil, "/", cfg, ServerOptions{
				FederationSpokeID: "33333333333333333333333333333333", FederationSpokeActive: test.active,
				FederationCredentials: credentials, FederationHTTPClient: hub.Client(), DisableWorkspaceBackgroundMonitors: true,
			})
			t.Cleanup(func() { require.NoError(srv.Shutdown(context.Background())) })
			readable, err := srv.HasReadableProviderWork(t.Context())
			require.NoError(err)
			require.Equal(test.active, readable)
			if test.active {
				require.Positive(requests.Load())
			} else {
				require.Zero(requests.Load())
			}
		})
	}
}
