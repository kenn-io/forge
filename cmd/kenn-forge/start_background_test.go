package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/daemonruntime"
	"go.kenn.io/forge/internal/runtimelock"
	"go.kenn.io/kit/daemon"
)

func TestValidateBackgroundConfigRejectsUnsafeDirectVerification(t *testing.T) {
	err := validateBackgroundConfig(&config.Config{Host: "192.0.2.1"})
	require.ErrorContains(t, err, "loopback TCP listener")
}

// TestBackgroundManagerRejectsMismatchedPingIdentity protects the attachment
// boundary rather than a particular implementation of the proof check. If all
// identity validation is removed, a record for one runtime can claim another
// live endpoint and background start will report the wrong daemon as success.
func TestBackgroundManagerRejectsMismatchedPingIdentity(t *testing.T) {
	require := require.New(t)
	dataDir := t.TempDir()
	token := "daemon-secret"
	record := newBackgroundProofRecord(t, dataDir, token, "v-real")
	record.Version = "v-claimed"
	manager := newDiscoveryManager(t, dataDir, token, record, "v-claimed")

	_, _, found, err := manager.Find(t.Context())

	require.NoError(err)
	assert.False(t, found)
}

// TestBackgroundDiscoveryDoesNotDiscloseBearerBeforeProof protects the
// credential boundary for stale runtime records. If discovery attaches the
// bearer before the endpoint proves possession of the daemon token, a process
// that inherits the recorded loopback port can steal the persistent token.
func TestBackgroundDiscoveryDoesNotDiscloseBearerBeforeProof(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	var receivedAuthorization atomic.Value
	var requests atomic.Int32
	attacker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		receivedAuthorization.Store(r.Header.Get("Authorization"))
		daemon.NewPingHandler(daemon.PingHandlerOptions{
			Service: daemonruntime.Service, Version: "v-test", PID: os.Getpid(),
		}).ServeHTTP(w, r)
	}))
	t.Cleanup(attacker.Close)

	dataDir := t.TempDir()
	identity, err := daemonruntime.NewIdentity(
		attacker.Listener.Addr(), daemonruntime.IdentityOptions{
			Version: "v-test", DataDir: dataDir, RequireAuth: true,
		},
	)
	require.NoError(err)
	manager := newDiscoveryManager(
		t, dataDir, "daemon-secret", identity.Record, "v-test",
	)

	_, _, compatible, err := manager.Find(t.Context())

	require.NoError(err)
	assert.False(compatible)
	assert.NotZero(requests.Load())
	assert.Empty(receivedAuthorization.Load())
}

func newDiscoveryManager(
	t *testing.T,
	dataDir, token string,
	record daemon.RuntimeRecord,
	version string,
) daemon.Manager {
	t.Helper()
	require.NoError(t, os.WriteFile(
		runtimelock.AuthTokenPath(dataDir), []byte(token+"\n"), 0o600,
	))
	store := daemon.RuntimeStore{Dir: t.TempDir()}
	_, err := store.Write(record)
	require.NoError(t, err)
	return daemonruntime.NewManager(store, dataDir, version, nil)
}

func newBackgroundProofRecord(
	t *testing.T,
	dataDir, token, version string,
) daemon.RuntimeRecord {
	t.Helper()
	server := httptest.NewUnstartedServer(nil)
	identity, err := daemonruntime.NewIdentity(
		server.Listener.Addr(), daemonruntime.IdentityOptions{
			Version: version, DataDir: dataDir, RequireAuth: true,
		},
	)
	require.NoError(t, err)
	record := identity.Record
	proof, err := daemon.NewProof([]byte(token))
	require.NoError(t, err)
	ping, err := proof.NewPingHandler(record)
	require.NoError(t, err)
	server.Config.Handler = ping
	server.Start()
	t.Cleanup(server.Close)
	return record
}
