package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gofrs/flock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadOrCreateMovesLegacyDefaultDatabase(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("KENN_FORGE_HOME", "")
	oldHome := filepath.Join(home, ".config", "middleman")
	newHome := filepath.Join(home, ".kenn", "forge")
	require.NoError(os.MkdirAll(oldHome, 0o700))
	require.NoError(os.WriteFile(filepath.Join(oldHome, legacyDatabaseFile), []byte("database"), 0o600))
	require.NoError(os.WriteFile(filepath.Join(oldHome, legacyDatabaseFile+"-wal"), []byte("wal"), 0o600))
	require.NoError(os.WriteFile(filepath.Join(oldHome, legacyDatabaseFile+"-shm"), []byte("shm"), 0o600))

	_, err := LoadOrCreate(DefaultConfigPath())
	require.NoError(err)

	assert.FileExists(filepath.Join(newHome, forgeDatabaseFile))
	assert.FileExists(filepath.Join(newHome, forgeDatabaseFile+"-wal"))
	assert.FileExists(filepath.Join(newHome, forgeDatabaseFile+"-shm"))
	assert.NoFileExists(filepath.Join(oldHome, legacyDatabaseFile))
	assert.FileExists(filepath.Join(oldHome, legacyLockFile))
}

func TestLoadOrCreateMovesLegacyDatabaseAfterCanonicalConfigExists(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("KENN_FORGE_HOME", "")
	oldHome := filepath.Join(home, ".config", "middleman")
	newHome := filepath.Join(home, ".kenn", "forge")
	require.NoError(EnsureDefault(DefaultConfigPath()))
	require.NoError(os.MkdirAll(oldHome, 0o700))
	require.NoError(os.WriteFile(filepath.Join(oldHome, legacyDatabaseFile), []byte("database"), 0o600))

	_, err := LoadOrCreate(DefaultConfigPath())
	require.NoError(err)

	assert.FileExists(filepath.Join(newHome, forgeDatabaseFile))
	assert.NoFileExists(filepath.Join(oldHome, legacyDatabaseFile))
}

func TestLoadOrCreateRenamesDatabaseInConfiguredDataDirectory(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	dataDir := filepath.Join(t.TempDir(), "state")
	configPath := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(os.MkdirAll(dataDir, 0o700))
	require.NoError(os.WriteFile(configPath, []byte("data_dir = \""+dataDir+"\"\n"), 0o600))
	require.NoError(os.WriteFile(filepath.Join(dataDir, legacyDatabaseFile), []byte("database"), 0o600))

	_, err := LoadOrCreate(configPath)
	require.NoError(err)

	assert.FileExists(filepath.Join(dataDir, forgeDatabaseFile))
	assert.NoFileExists(filepath.Join(dataDir, legacyDatabaseFile))
	assert.FileExists(filepath.Join(dataDir, legacyLockFile))
}

func TestLoadOrCreateRefusesDatabaseMoveWhileMiddlemanDaemonIsActive(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	dataDir := filepath.Join(t.TempDir(), "state")
	configPath := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(os.MkdirAll(dataDir, 0o700))
	require.NoError(os.WriteFile(configPath, []byte("data_dir = \""+dataDir+"\"\n"), 0o600))
	require.NoError(os.WriteFile(filepath.Join(dataDir, legacyDatabaseFile), []byte("database"), 0o600))
	legacyLock := flock.New(filepath.Join(dataDir, legacyLockFile))
	require.NoError(legacyLock.Lock())
	t.Cleanup(func() { require.NoError(legacyLock.Unlock()) })

	_, err := LoadOrCreate(configPath)

	require.Error(err)
	assert.Contains(err.Error(), "middleman daemon is still using")
	assert.FileExists(filepath.Join(dataDir, legacyDatabaseFile))
	assert.NoFileExists(filepath.Join(dataDir, forgeDatabaseFile))
}
