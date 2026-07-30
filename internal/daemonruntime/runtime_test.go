package daemonruntime

import (
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

type fakeAddress string

func (a fakeAddress) Network() string { return "fake" }
func (a fakeAddress) String() string  { return string(a) }
