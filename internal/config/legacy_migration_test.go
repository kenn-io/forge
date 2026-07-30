package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadOrCreateMigratesLegacyDefaultState(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("KENN_FORGE_HOME", "")
	oldHome := filepath.Join(home, ".config", "middleman")
	newHome := filepath.Join(home, ".kenn", "forge")
	require.NoError(os.MkdirAll(oldHome, 0o700))
	require.NoError(os.WriteFile(filepath.Join(oldHome, "config.toml"), []byte(
		"github_token_env = \"MIDDLEMAN_GITHUB_TOKEN\"\n"+
			"token_file = \""+filepath.Join(oldHome, "token")+"\"\n",
	), 0o600))
	require.NoError(os.WriteFile(filepath.Join(oldHome, "middleman.db"), []byte("database"), 0o600))
	require.NoError(os.WriteFile(filepath.Join(oldHome, "middleman.db-wal"), []byte("wal"), 0o600))
	require.NoError(os.WriteFile(filepath.Join(oldHome, "middleman.db-shm"), []byte("shm"), 0o600))
	require.NoError(os.WriteFile(filepath.Join(oldHome, "auth_token"), []byte("secret"), 0o600))

	cfg, err := LoadOrCreate(DefaultConfigPath())
	require.NoError(err)
	canonicalNewHome, err := filepath.EvalSymlinks(newHome)
	require.NoError(err)

	assert.Equal(canonicalNewHome, cfg.DataDir)
	assert.FileExists(filepath.Join(newHome, "config.toml"))
	assert.FileExists(filepath.Join(newHome, "forge.db"))
	assert.FileExists(filepath.Join(newHome, "forge.db-wal"))
	assert.FileExists(filepath.Join(newHome, "forge.db-shm"))
	assert.FileExists(filepath.Join(newHome, "auth_token"))
	assert.FileExists(filepath.Join(oldHome, "middleman.lock"))
	assert.NoFileExists(filepath.Join(oldHome, "config.toml"))
	configBody, err := os.ReadFile(filepath.Join(newHome, "config.toml"))
	require.NoError(err)
	assert.Contains(string(configBody), "KENN_FORGE_GITHUB_TOKEN")
	assert.Contains(string(configBody), filepath.Join(newHome, "token"))
}

func TestLoadOrCreateKeepsCustomDataDirectoryAndRenamesDatabase(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	home := t.TempDir()
	customData := filepath.Join(t.TempDir(), "state")
	t.Setenv("HOME", home)
	t.Setenv("KENN_FORGE_HOME", "")
	oldHome := filepath.Join(home, ".config", "middleman")
	require.NoError(os.MkdirAll(oldHome, 0o700))
	require.NoError(os.MkdirAll(customData, 0o700))
	require.NoError(os.WriteFile(filepath.Join(oldHome, "config.toml"), []byte(
		"data_dir = \""+customData+"\"\n",
	), 0o600))
	require.NoError(os.WriteFile(filepath.Join(customData, "middleman.db"), []byte("database"), 0o600))

	cfg, err := LoadOrCreate(DefaultConfigPath())
	require.NoError(err)
	canonicalCustomData, err := filepath.EvalSymlinks(customData)
	require.NoError(err)

	assert.Equal(canonicalCustomData, cfg.DataDir)
	assert.FileExists(filepath.Join(customData, "forge.db"))
	assert.NoFileExists(filepath.Join(customData, "middleman.db"))
	assert.FileExists(filepath.Join(customData, "middleman.lock"))
}

func TestLoadOrCreateRejectsNonemptyLegacyAndCanonicalHomes(t *testing.T) {
	require := require.New(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("KENN_FORGE_HOME", "")
	oldHome := filepath.Join(home, ".config", "middleman")
	newHome := filepath.Join(home, ".kenn", "forge")
	require.NoError(os.MkdirAll(oldHome, 0o700))
	require.NoError(os.MkdirAll(newHome, 0o700))
	require.NoError(os.WriteFile(filepath.Join(oldHome, "config.toml"), []byte("host = \"127.0.0.1\"\n"), 0o600))
	require.NoError(os.WriteFile(filepath.Join(newHome, "config.toml"), []byte("host = \"127.0.0.1\"\n"), 0o600))

	_, err := LoadOrCreate(DefaultConfigPath())

	require.Error(err)
	assert.Contains(t, err.Error(), oldHome)
	assert.Contains(t, err.Error(), newHome)
}
