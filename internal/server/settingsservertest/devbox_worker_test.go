package settingsservertest

import (
	"context"
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/config"
	forgeserver "go.kenn.io/forge/internal/server"
	"go.kenn.io/forge/internal/server/authapi"
	"go.kenn.io/forge/internal/server/devboxapi"
	"go.kenn.io/forge/internal/testutil/dbtest"
)

func TestExecutionWorkerRoutesAndBearerBoundary(t *testing.T) {
	assert := assert.New(t)
	cfg := &config.Config{}
	cfg.DataDir = t.TempDir()
	cfg.ExecutionWorker = config.ExecutionWorker{Enabled: true, UID: 1001, GitHubUserID: 1234, BrokerSocket: "/run/example/broker.sock"}
	srv := forgeserver.New(dbtest.Open(t), nil, nil, "/", cfg, forgeserver.ServerOptions{
		ExecutionWorker:               true,
		DaemonAccess:                  authapi.DaemonAccessOptions{Token: "worker-test-secret", RequireAPIAuth: true},
		FederationSpokeID:             "0123456789abcdef0123456789abcdef",
		HostCheckAllowLoopbackAnyPort: true,
	})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		assert.NoError(srv.Shutdown(ctx))
	})
	server := httptest.NewServer(srv)
	t.Cleanup(server.Close)
	for _, test := range []struct {
		name, method, path, bearer, cookie, userHeader string
		status                                         int
	}{
		{name: "identity", method: "GET", path: "/api/v1/worker", bearer: "worker-test-secret", status: 200},
		{name: "workspace inventory", method: "GET", path: "/api/v1/workspaces", bearer: "worker-test-secret", status: 200},
		{name: "missing bearer", method: "GET", path: "/api/v1/worker", status: 401},
		{name: "wrong account", method: "GET", path: "/api/v1/worker", bearer: "other-account", status: 401},
		{name: "forged Serve user", method: "GET", path: "/api/v1/worker", userHeader: "developer@example.org", status: 401},
		{name: "browser cookie", method: "GET", path: "/api/v1/worker", cookie: "worker-test-secret", status: 401},
		{name: "settings", method: "PATCH", path: "/api/v1/settings", bearer: "worker-test-secret", status: 404},
		{name: "provider sync", method: "POST", path: "/api/v1/sync", bearer: "worker-test-secret", status: 404},
		{name: "enrollment", method: "POST", path: "/api/v1/fleet/enrollments", bearer: "worker-test-secret", status: 404},
	} {
		t.Run(test.name, func(t *testing.T) {
			request, err := http.NewRequestWithContext(t.Context(), test.method, server.URL+test.path, strings.NewReader(`{}`))
			require.NoError(t, err)
			request.Header.Set("Content-Type", "application/json")
			if test.bearer != "" {
				request.Header.Set("Authorization", "Bearer "+test.bearer)
			}
			request.Header.Set("Tailscale-User-Login", test.userHeader)
			if test.cookie != "" {
				request.AddCookie(&http.Cookie{Name: "forge_auth", Value: test.cookie})
			}
			response, err := server.Client().Do(request)
			require.NoError(t, err)
			defer response.Body.Close()
			assert.Equal(test.status, response.StatusCode)
			if test.name == "identity" {
				var identity devboxapi.WorkerIdentity
				require.NoError(t, json.UnmarshalRead(response.Body, &identity))
				assert.Equal(int64(1234), identity.GitHubUserID)
				assert.Equal(uint32(1001), identity.UID)
				assert.Equal("execution", identity.Role)
			}
		})
	}
}
