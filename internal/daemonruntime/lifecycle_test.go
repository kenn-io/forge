package daemonruntime

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/runtimelock"
	"go.kenn.io/kit/daemon"
)

func TestManagerRejectsProvenRuntimeWithDifferentVersion(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	store, dataDir := newProofRuntime(t, "daemon-secret", "v-old")
	manager, err := NewManager(store, dataDir, "v-new", nil)
	require.NoError(err)

	_, _, found, err := manager.Find(t.Context())

	require.Error(err)
	assert.False(found)
	var mismatch *VersionMismatchError
	require.ErrorAs(err, &mismatch)
	assert.Equal("v-old", mismatch.Running)
	assert.Equal("v-new", mismatch.Expected)
}

func TestFindVerifiedAcceptsProvenRuntimeWithDifferentVersion(t *testing.T) {
	assert := assert.New(t)
	store, dataDir := newProofRuntime(t, "daemon-secret", "v-old")

	record, ping, found, err := FindVerified(t.Context(), store, dataDir)

	require.NoError(t, err)
	assert.True(found)
	assert.Equal("v-old", record.Version)
	assert.Equal(record.PID, ping.PID)
}

func newProofRuntime(
	t *testing.T,
	token, runtimeVersion string,
) (daemon.RuntimeStore, string) {
	t.Helper()
	dataDir := t.TempDir()
	require.NoError(t, os.WriteFile(
		runtimelock.AuthTokenPath(dataDir), []byte(token+"\n"), 0o600,
	))
	server := httptest.NewUnstartedServer(nil)
	identity, err := NewIdentity(server.Listener.Addr(), IdentityOptions{
		Version: runtimeVersion, DataDir: dataDir,
		ConfigPath: filepath.Join(dataDir, "config.toml"), RequireAuth: true,
	})
	require.NoError(t, err)
	proof, err := daemon.NewProof([]byte(token))
	require.NoError(t, err)
	handler, err := proof.NewPingHandler(identity.Record)
	require.NoError(t, err)
	server.Config.Handler = handler
	server.Start()
	t.Cleanup(server.Close)
	store := daemon.RuntimeStore{Dir: t.TempDir()}
	_, err = store.Write(identity.Record)
	require.NoError(t, err)
	return store, dataDir
}
