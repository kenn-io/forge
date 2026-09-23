package settingsservertest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/config"
	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/server"
	"go.kenn.io/forge/internal/server/roborevapi"
	"go.kenn.io/forge/internal/testutil"
	"go.kenn.io/forge/internal/testutil/dbtest"
)

// setupTestServerWithRoborev creates a server with the roborev
// proxy configured to point at the given endpoint URL.
func setupTestServerWithRoborev(
	t *testing.T, roborevEndpoint string,
) *server.Server {
	t.Helper()

	dir := t.TempDir()
	database := dbtest.Open(t)

	cfgContent := fmt.Sprintf(`
sync_interval = "5m"
github_token_env = "KENN_FORGE_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "widget"

[roborev]
endpoint = %q
`, roborevEndpoint)

	cfgPath := filepath.Join(dir, "config.toml")
	err := os.WriteFile(cfgPath, []byte(cfgContent), 0o644)
	require.NoError(t, err)

	cfg, err := config.Load(cfgPath)
	require.NoError(t, err)

	mock := &mockGH{}
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": mock},
		database, nil, nil, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	return server.NewWithConfig(
		database, syncer, nil, nil, cfg, cfgPath,
		server.ServerOptions{HostCheckAllowLoopbackAnyPort: true},
	)
}

func TestRoborevHealthProbeAvailable(t *testing.T) {
	assert := assert.New(t)

	daemon := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/status" {
				w.Header().Set(
					"Content-Type", "application/json",
				)
				_, _ = w.Write(
					[]byte(`{"version":"1.2.3"}`),
				)
				return
			}
			http.NotFound(w, r)
		},
	))
	defer daemon.Close()

	srv := setupTestServerWithRoborev(t, daemon.URL)

	rr := testutil.DoJSON(
		t, srv, http.MethodGet,
		"/api/v1/roborev/status", nil)

	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	var resp roborevapi.RoborevStatusResponse
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	assert.True(resp.Available)
	assert.Equal("1.2.3", resp.Version)
	assert.Equal(daemon.URL, resp.Endpoint)
}

func TestRoborevHealthProbeUnavailable(t *testing.T) {
	assert := assert.New(t)

	srv := setupTestServerWithRoborev(t, "http://127.0.0.1:1")

	rr := testutil.DoJSON(
		t, srv, http.MethodGet,
		"/api/v1/roborev/status", nil)

	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	var resp roborevapi.RoborevStatusResponse
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	assert.False(resp.Available)
	assert.Empty(resp.Version)
}
