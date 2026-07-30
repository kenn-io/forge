package daemonruntime

import (
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/kit/daemon"
	"go.kenn.io/middleman/internal/runtimelock"
)

// TestNewIdentityKeepsDiscoverySurfacesAligned protects clients of both the
// authoritative lock metadata and generic daemon record. If either surface is
// built independently again, they can advertise different process or listener
// identities and attach clients to the wrong runtime.
func TestNewIdentityKeepsDiscoverySurfacesAligned(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(err)
	t.Cleanup(func() { require.NoError(listener.Close()) })
	dataDir := t.TempDir()

	identity, err := NewIdentity(listener.Addr(), IdentityOptions{
		Version: "v-test", Commit: "commit-test", DataDir: dataDir,
		BasePath: "/middleman/", RequireAuth: true,
	})

	require.NoError(err)
	record := identity.Record
	metadata := identity.LockMetadata
	assert.Equal(record.PID, metadata.PID)
	assert.Equal(record.Address, metadata.ListenAddr)
	assert.Equal(record.Version, metadata.Version)
	assert.Equal(record.StartedAt.Format(time.RFC3339), metadata.StartedAt)
	assert.Equal(record.Metadata[metadataHost], metadata.Host)
	assert.Equal(strconv.Itoa(listener.Addr().(*net.TCPAddr).Port), record.Metadata[metadataPort])
	assert.Equal(runtimelock.AuthTokenPath(dataDir), metadata.TokenPath)
	assert.Equal(metadata.TokenPath, record.Metadata[metadataAuthTokenPath])
	assert.Equal("/middleman", metadata.BasePath)
	assert.True(metadata.RequireAuth)
}

func TestNewIdentityRejectsNonTCPAddress(t *testing.T) {
	_, err := NewIdentity(fakeAddress("not-tcp"), IdentityOptions{})

	require.ErrorContains(t, err, "non-TCP")
}

// TestStartLockStoreScopesLocksByDataDirectory protects independent daemon
// instances that share the config-home runtime store. If the data directory is
// dropped from lock identity, a stalled start blocks unrelated instances.
func TestStartLockStoreScopesLocksByDataDirectory(t *testing.T) {
	store := daemon.RuntimeStore{Dir: t.TempDir()}

	first := StartLockStore(store, "/data/first")
	second := StartLockStore(store, "/data/second")

	assert.Equal(t, first.Dir, second.Dir)
	assert.NotEqual(t, first.Prefix, second.Prefix)
}

type fakeAddress string

func (a fakeAddress) Network() string { return "fake" }
func (a fakeAddress) String() string  { return string(a) }
