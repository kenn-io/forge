package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/kit/daemon"
	"go.kenn.io/middleman/internal/config"
	"go.kenn.io/middleman/internal/daemonruntime"
	"go.kenn.io/middleman/internal/runtimelock"
)

func TestValidateBackgroundConfigRejectsUnsafeDirectVerification(t *testing.T) {
	tests := []struct {
		name string
		cfg  config.Config
		want string
	}{
		{
			name: "non-loopback listener",
			cfg: config.Config{
				Host: "192.0.2.1", API: config.API{RequireAuth: true},
			},
			want: "loopback TCP listener",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateBackgroundConfig(&tt.cfg)
			require.ErrorContains(t, err, tt.want)
		})
	}
}

func TestValidateBackgroundConfigAllowsUnauthenticatedReverseProxy(t *testing.T) {
	cfg := config.Config{Host: "127.0.0.1", TrustReverseProxy: true}

	require.NoError(t, validateBackgroundConfig(&cfg))
}

// TestBackgroundManagerRejectsMismatchedPingIdentity protects the attachment
// boundary rather than a particular implementation of the proof check. If all
// identity validation is removed, a record for one runtime can claim another
// live endpoint and background start will report the wrong daemon as success.
func TestBackgroundManagerRejectsMismatchedPingIdentity(t *testing.T) {
	require := require.New(t)
	dataDir := t.TempDir()
	token := "daemon-secret"
	_, record := newBackgroundProofServer(t, dataDir, token, "v-real")
	require.NoError(os.WriteFile(
		runtimelock.AuthTokenPath(dataDir), []byte(token+"\n"), 0o600,
	))
	record.Version = "v-claimed"
	store := daemon.RuntimeStore{Dir: t.TempDir()}
	_, err := store.Write(record)
	require.NoError(err)
	manager := daemonruntime.NewManager(store, dataDir, "v-claimed", nil)

	_, _, found, err := manager.Find(t.Context())

	require.NoError(err)
	assert.False(t, found)
}

// TestBackgroundDiscoveryDoesNotDiscloseBearerBeforeProof protects the
// credential boundary for stale runtime records. If discovery attaches the
// bearer before the endpoint proves possession of the daemon token, a process
// that inherits the recorded loopback port can steal the persistent token.
func TestBackgroundDiscoveryDoesNotDiscloseBearerBeforeProof(t *testing.T) {
	var receivedAuthorization atomic.Value
	var requests atomic.Int32
	attacker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		receivedAuthorization.Store(r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w,
			`{"ok":true,"service":"middleman","version":"v-test","pid":%d}`,
			os.Getpid(),
		)
	}))
	t.Cleanup(attacker.Close)

	dataDir := t.TempDir()
	identity, err := daemonruntime.NewIdentity(
		attacker.Listener.Addr(), daemonruntime.IdentityOptions{
			Version: "v-test", DataDir: dataDir, RequireAuth: true,
		},
	)
	require.NoError(t, err)
	record := identity.Record
	require.NoError(t, os.WriteFile(
		runtimelock.AuthTokenPath(dataDir), []byte("daemon-secret\n"), 0o600,
	))
	store := daemon.RuntimeStore{Dir: t.TempDir()}
	_, err = store.Write(record)
	require.NoError(t, err)
	manager := daemonruntime.NewManager(store, dataDir, "v-test", nil)

	_, _, compatible, err := manager.Find(t.Context())

	require.NoError(t, err)
	assert.False(t, compatible)
	assert.NotZero(t, requests.Load())
	assert.Empty(t, receivedAuthorization.Load())
}

func newBackgroundProofServer(
	t *testing.T,
	dataDir, token, version string,
) (*httptest.Server, daemon.RuntimeRecord) {
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
	return server, record
}
