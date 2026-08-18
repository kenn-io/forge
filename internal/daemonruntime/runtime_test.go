package daemonruntime

import (
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/runtimelock"
	"go.kenn.io/kit/daemon"
)

// TestNewIdentityKeepsDiscoverySurfacesAligned protects clients of both the
// authoritative lock metadata and generic daemon record. If either surface is
// built independently again, they can advertise different process or listener
// identities and attach clients to the wrong runtime.
func TestNewIdentityKeepsMCPListenAddrDiscoverySurfacesAligned(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(err)
	t.Cleanup(func() { require.NoError(listener.Close()) })
	dataDir := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.toml")
	canonicalConfigDir, err := filepath.EvalSymlinks(filepath.Dir(configPath))
	require.NoError(err)
	canonicalConfigPath := filepath.Join(
		canonicalConfigDir, filepath.Base(configPath),
	)
	canonicalDataDir, err := config.CanonicalDataDir(dataDir)
	require.NoError(err)

	identity, err := NewIdentity(listener.Addr(), IdentityOptions{
		Version: "v-test", Commit: "commit-test", DataDir: dataDir,
		ConfigPath:    configPath,
		BasePath:      "/kenn-forge/",
		RequireAuth:   true,
		MCPListenAddr: "127.0.0.1:8092",
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
	assert.Equal(runtimelock.AuthTokenPath(canonicalDataDir), metadata.TokenPath)
	assert.Equal(metadata.TokenPath, record.Metadata[metadataAuthTokenPath])
	assert.Equal(canonicalConfigPath, record.Metadata[metadataConfigPath])
	assert.Equal(metadata.ConfigPath, record.Metadata[metadataConfigPath])
	assert.Equal("/kenn-forge", metadata.BasePath)
	assert.Equal(metadata.BasePath, record.Metadata[metadataBasePath])
	assert.Equal("127.0.0.1:8092", identity.Record.Metadata["mcp_listen_addr"])
	assert.Equal("127.0.0.1:8092", identity.LockMetadata.MCPListenAddr)
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
	require := require.New(t)
	store := daemon.RuntimeStore{Dir: t.TempDir()}

	first, err := StartLockStore(store, "/data/first")
	require.NoError(err)
	second, err := StartLockStore(store, "/data/second")
	require.NoError(err)

	assert.Equal(t, first.Dir, second.Dir)
	assert.NotEqual(t, first.Prefix, second.Prefix)
}

func TestLifecycleLockStoreScopesTransitionsByDataDirectory(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	store := daemon.RuntimeStore{Dir: t.TempDir()}
	first, err := LifecycleLockStore(store, "/data/first")
	require.NoError(err)
	second, err := LifecycleLockStore(store, "/data/second")
	require.NoError(err)

	firstPath, err := first.LockPath()
	require.NoError(err)
	secondPath, err := second.LockPath()
	require.NoError(err)
	assert.Equal(first.Dir, second.Dir)
	assert.NotEqual(firstPath, secondPath)
}

func TestLifecycleLockStoreUsesFilesystemDataDirectoryIdentity(t *testing.T) {
	require := require.New(t)
	root := t.TempDir()
	dataDir := filepath.Join(root, "DataDir")
	require.NoError(os.Mkdir(dataDir, 0o700))
	caseAlias := filepath.Join(root, "datadir")
	if _, err := os.Stat(caseAlias); os.IsNotExist(err) {
		t.Skip("filesystem is case-sensitive")
	} else {
		require.NoError(err)
	}
	store := daemon.RuntimeStore{Dir: t.TempDir()}

	canonicalStore, err := LifecycleLockStore(store, dataDir)
	require.NoError(err)
	aliasStore, err := LifecycleLockStore(store, caseAlias)
	require.NoError(err)

	assert.Equal(t, canonicalStore.Prefix, aliasStore.Prefix)
}

func TestLifecycleLockReleaseLeavesStableLockFile(t *testing.T) {
	require := require.New(t)
	store := daemon.RuntimeStore{Dir: t.TempDir()}
	lock, err := AcquireLifecycleLock(t.Context(), store, "/data/instance")
	require.NoError(err)
	lockStore, err := LifecycleLockStore(store, "/data/instance")
	require.NoError(err)
	lockPath, err := lockStore.LockPath()
	require.NoError(err)

	require.NoError(lock.Release())
	_, err = os.Stat(lockPath)
	require.NoError(err)
}

func TestCanonicalConfigPathUsesStoredCaseOnCaseInsensitiveFilesystem(t *testing.T) {
	require := require.New(t)
	root := t.TempDir()
	configDir := filepath.Join(root, "ConfigHome")
	require.NoError(os.Mkdir(configDir, 0o700))
	configPath := filepath.Join(configDir, "ForgeConfig.toml")
	require.NoError(os.WriteFile(configPath, []byte("test\n"), 0o600))
	caseAlias := filepath.Join(root, "confighome", "forgeconfig.toml")
	if _, err := os.Stat(caseAlias); os.IsNotExist(err) {
		t.Skip("filesystem is case-sensitive")
	} else {
		require.NoError(err)
	}

	canonicalPath, err := CanonicalConfigPath(configPath)
	require.NoError(err)
	canonicalAlias, err := CanonicalConfigPath(caseAlias)
	require.NoError(err)

	assert.Equal(t, canonicalPath, canonicalAlias)
}

func TestCanonicalConfigPathUsesStoredUnicodeNormalization(t *testing.T) {
	require := require.New(t)
	root := t.TempDir()
	configPath := filepath.Join(root, "Cafe\u0301.toml")
	require.NoError(os.WriteFile(configPath, []byte("test\n"), 0o600))
	normalizedAlias := filepath.Join(root, "Caf\u00e9.toml")
	if _, err := os.Stat(normalizedAlias); os.IsNotExist(err) {
		t.Skip("filesystem distinguishes Unicode normalization forms")
	} else {
		require.NoError(err)
	}

	canonicalPath, err := CanonicalConfigPath(configPath)
	require.NoError(err)
	canonicalAlias, err := CanonicalConfigPath(normalizedAlias)
	require.NoError(err)

	assert.Equal(t, canonicalPath, canonicalAlias)
}

func TestCanonicalConfigPathDoesNotListAncestorDirectories(t *testing.T) {
	if filepath.Separator == '\\' {
		t.Skip("POSIX directory permissions required")
	}
	require := require.New(t)
	configDir := filepath.Join(t.TempDir(), "execute-only")
	require.NoError(os.Mkdir(configDir, 0o700))
	configPath := filepath.Join(configDir, "config.toml")
	require.NoError(os.WriteFile(configPath, []byte("test\n"), 0o600))
	require.NoError(os.Chmod(configDir, 0o111))
	t.Cleanup(func() { require.NoError(os.Chmod(configDir, 0o700)) })

	_, err := CanonicalConfigPath(configPath)
	require.NoError(err)
}

func TestConfigRuntimesIgnoresUnresolvableUnrelatedConfig(t *testing.T) {
	require := require.New(t)
	root := t.TempDir()
	store := daemon.RuntimeStore{Dir: filepath.Join(root, "runtime")}
	dataDir := filepath.Join(root, "data")
	require.NoError(os.MkdirAll(dataDir, 0o700))
	targetConfigPath := filepath.Join(root, "target", "config.toml")
	require.NoError(os.MkdirAll(filepath.Dir(targetConfigPath), 0o700))

	loopA := filepath.Join(root, "loop-a")
	loopB := filepath.Join(root, "loop-b")
	if err := os.Symlink(loopB, loopA); err != nil {
		t.Skipf("config symlink unavailable: %v", err)
	}
	require.NoError(os.Symlink(loopA, loopB))
	endpoint := daemon.Endpoint{Network: daemon.NetworkTCP, Address: "127.0.0.1:1"}
	unrelated := daemon.NewRuntimeRecord(Service, "dev", endpoint)
	unrelated.PID = os.Getppid()
	unrelated.Metadata = map[string]string{
		metadataConfigPath: filepath.Join(loopA, "config.toml"),
		metadataDataDir:    dataDir,
	}
	_, err := store.Write(unrelated)
	require.NoError(err)

	target := daemon.NewRuntimeRecord(Service, "dev", endpoint)
	target.Metadata = map[string]string{
		metadataConfigPath: targetConfigPath,
		metadataDataDir:    dataDir,
	}
	_, err = store.Write(target)
	require.NoError(err)

	runtimes, err := ConfigRuntimes(store, targetConfigPath, dataDir)

	require.NoError(err)
	require.Len(runtimes, 1)
	require.Equal(target.PID, runtimes[0].Record.PID)
}

type fakeAddress string

func (a fakeAddress) Network() string { return "fake" }
func (a fakeAddress) String() string  { return string(a) }
