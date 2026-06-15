package apitest

import (
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/apiclient/generated"
	"go.kenn.io/middleman/internal/config"
	ghclient "go.kenn.io/middleman/internal/github"
	"go.kenn.io/middleman/internal/server"
	"go.kenn.io/middleman/internal/testutil/dbtest"
)

func setupFleetSettingsServer(t *testing.T) (*server.Server, string) {
	t.Helper()
	cfgPath := t.TempDir() + "/config.toml"
	require.NoError(t, os.WriteFile(cfgPath, []byte(`
sync_interval = "5m"
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091
allowed_hosts = ["middleman.test"]
`), 0o600))
	cfg, err := config.Load(cfgPath)
	require.NoError(t, err)

	database := dbtest.Open(t)
	syncer := ghclient.NewSyncer(
		nil, database, nil, defaultTestRepos, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)

	srv := server.NewWithConfig(
		database, syncer, nil, nil, cfg, cfgPath,
		server.ServerOptions{},
	)
	return srv, cfgPath
}

// TestAPIFleetSSHPeerSettings drives GET/PUT
// /settings/fleet/ssh-peers through the generated client: the typed
// round-trip, the restart_required signal (the transport is wired at
// startup), persistence to the config file, and the typed validation
// failure path with rollback.
func TestAPIFleetSSHPeerSettings(t *testing.T) {
	require := require.New(t)
	srv, cfgPath := setupFleetSettingsServer(t)
	client := setupTestClient(t, srv)

	empty, err := client.HTTP.GetFleetSshPeersWithResponse(t.Context())
	require.NoError(err)
	require.Equal(http.StatusOK, empty.StatusCode())
	require.NotNil(empty.JSON200)
	require.Empty(empty.JSON200.SshPeers)
	require.False(empty.JSON200.RestartRequired)

	name := "EPYC"
	platform := "linux"
	updated, err := client.HTTP.UpdateFleetSshPeersWithResponse(
		t.Context(),
		generated.UpdateFleetSshPeersJSONRequestBody{
			SshPeers: []generated.FleetSSHPeer{{
				Key:         "epyc",
				Name:        &name,
				Destination: "wes@epyc.local",
				Platform:    &platform,
			}},
		},
	)
	require.NoError(err)
	require.Equal(http.StatusOK, updated.StatusCode(), string(updated.Body))
	require.NotNil(updated.JSON200)
	require.Len(updated.JSON200.SshPeers, 1)
	require.Equal("epyc", updated.JSON200.SshPeers[0].Key)
	require.True(updated.JSON200.RestartRequired,
		"ssh peers are startup-bound")

	raw, err := os.ReadFile(cfgPath)
	require.NoError(err)
	require.Contains(string(raw), "wes@epyc.local")

	// Duplicate keys reject with a typed problem and roll back.
	rejected, err := client.HTTP.UpdateFleetSshPeersWithResponse(
		t.Context(),
		generated.UpdateFleetSshPeersJSONRequestBody{
			SshPeers: []generated.FleetSSHPeer{
				{Key: "dup", Destination: "a@b"},
				{Key: "dup", Destination: "c@d"},
			},
		},
	)
	require.NoError(err)
	require.Equal(http.StatusBadRequest, rejected.StatusCode())
	require.NotNil(rejected.ApplicationproblemJSONDefault)
	require.Equal(
		"badRequest",
		string(rejected.ApplicationproblemJSONDefault.Code),
	)

	after, err := client.HTTP.GetFleetSshPeersWithResponse(t.Context())
	require.NoError(err)
	require.NotNil(after.JSON200)
	require.Len(after.JSON200.SshPeers, 1,
		"failed update must not persist")
}
