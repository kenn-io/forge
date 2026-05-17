package main

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/middleman/internal/config"
	_ "modernc.org/sqlite"
)

func TestPrepareEphemeralConfigOverridesPortAndDataDir(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.toml")
	sourceDataDir := filepath.Join(dir, "source-data")
	workDir := filepath.Join(dir, "run")
	require.NoError(os.MkdirAll(workDir, 0o700))

	source := config.Config{
		SyncInterval:        "5m",
		GitHubTokenEnv:      "MIDDLEMAN_GITHUB_TOKEN",
		DefaultPlatformHost: "github.com",
		Host:                "127.0.0.1",
		Port:                8091,
		DataDir:             sourceDataDir,
		Activity:            config.Activity{ViewMode: "threaded", TimeRange: "7d"},
	}
	require.NoError(source.Save(sourcePath))

	prepared, err := prepareEphemeralConfig(ephemeralOptions{
		sourceConfigPath: sourcePath,
		workDir:          workDir,
		backendPort:      39101,
		frontendPort:     39102,
	})
	require.NoError(err)

	reloaded, err := config.Load(prepared.configPath)
	require.NoError(err)
	assert.Equal(39101, reloaded.Port)
	assert.Equal(filepath.Join(workDir, "data"), reloaded.DataDir)
	assert.Equal(filepath.Join(workDir, "dev-ephemeral.json"), prepared.statusPath)
	assert.Equal("http://127.0.0.1:39101", prepared.backendURL)
	assert.Equal("http://127.0.0.1:39102", prepared.frontendURL)
	assert.Equal(sourceDataDir, source.DataDir)
}

func TestPrepareEphemeralConfigCopiesSourceDatabaseByDefault(t *testing.T) {
	require := require.New(t)
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.toml")
	sourceDataDir := filepath.Join(dir, "source-data")
	workDir := filepath.Join(dir, "run")

	source := config.Config{
		SyncInterval:        "5m",
		GitHubTokenEnv:      "MIDDLEMAN_GITHUB_TOKEN",
		DefaultPlatformHost: "github.com",
		Host:                "127.0.0.1",
		Port:                8091,
		DataDir:             sourceDataDir,
		Activity:            config.Activity{ViewMode: "threaded", TimeRange: "7d"},
	}
	require.NoError(os.MkdirAll(sourceDataDir, 0o700))
	require.NoError(source.Save(sourcePath))
	writeSQLiteMarker(t, source.DBPath(), "copied state")

	prepared, err := prepareEphemeralConfig(ephemeralOptions{
		sourceConfigPath: sourcePath,
		workDir:          workDir,
		backendPort:      39111,
		frontendPort:     39112,
	})
	require.NoError(err)

	Assert.Equal(t, "copied state", readSQLiteMarker(t, filepath.Join(prepared.dataDir, "middleman.db")))
}

func TestPrepareEphemeralConfigCanStartWithFreshDatabase(t *testing.T) {
	require := require.New(t)
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.toml")
	sourceDataDir := filepath.Join(dir, "source-data")
	workDir := filepath.Join(dir, "run")

	source := config.Config{
		SyncInterval:        "5m",
		GitHubTokenEnv:      "MIDDLEMAN_GITHUB_TOKEN",
		DefaultPlatformHost: "github.com",
		Host:                "127.0.0.1",
		Port:                8091,
		DataDir:             sourceDataDir,
		Activity:            config.Activity{ViewMode: "threaded", TimeRange: "7d"},
	}
	require.NoError(os.MkdirAll(sourceDataDir, 0o700))
	require.NoError(source.Save(sourcePath))
	writeSQLiteMarker(t, source.DBPath(), "do not copy")

	prepared, err := prepareEphemeralConfig(ephemeralOptions{
		sourceConfigPath: sourcePath,
		workDir:          workDir,
		backendPort:      39121,
		frontendPort:     39122,
		freshDB:          true,
	})
	require.NoError(err)

	_, err = os.Stat(filepath.Join(prepared.dataDir, "middleman.db"))
	require.ErrorIs(err, os.ErrNotExist)
}

func TestPrepareEphemeralConfigKeepsBasePathInBackendURL(t *testing.T) {
	require := require.New(t)
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.toml")

	source := config.Config{
		SyncInterval:        "5m",
		GitHubTokenEnv:      "MIDDLEMAN_GITHUB_TOKEN",
		DefaultPlatformHost: "github.com",
		Host:                "127.0.0.1",
		Port:                8091,
		BasePath:            "/middleman/",
		DataDir:             filepath.Join(dir, "source-data"),
		Activity:            config.Activity{ViewMode: "threaded", TimeRange: "7d"},
	}
	require.NoError(source.Save(sourcePath))

	prepared, err := prepareEphemeralConfig(ephemeralOptions{
		sourceConfigPath: sourcePath,
		workDir:          filepath.Join(dir, "run"),
		backendPort:      39201,
		frontendPort:     39202,
	})
	require.NoError(err)

	Assert.Equal(t, "http://127.0.0.1:39201/middleman", prepared.backendURL)
}

func TestBuildCommandSpecsWiresEphemeralEnvironment(t *testing.T) {
	assert := Assert.New(t)

	specs := buildCommandSpecs(ephemeralRun{
		configPath:   "/tmp/middleman-dev/config.toml",
		backendURL:   "http://127.0.0.1:39301",
		frontendPort: 39302,
		logDir:       "/tmp/middleman-dev/logs",
	}, []string{"--host", "127.0.0.1"})

	assert.Equal("./scripts/dev-stack-backend.sh", specs.backend.name)
	assert.Contains(specs.backend.env, "MIDDLEMAN_CONFIG=/tmp/middleman-dev/config.toml")
	assert.Contains(specs.backend.env, "MIDDLEMAN_LOG_FILE=/tmp/middleman-dev/logs/backend-dev.log")
	assert.Equal("./scripts/frontend-dev.sh", specs.frontend.name)
	assert.Equal([]string{"--port", "39302", "--host", "127.0.0.1"}, specs.frontend.args)
	assert.Contains(specs.frontend.env, "MIDDLEMAN_CONFIG=/tmp/middleman-dev/config.toml")
	assert.Contains(specs.frontend.env, "MIDDLEMAN_API_URL=http://127.0.0.1:39301")
}

func TestWriteStatusFileRecordsPIDsAndPortsNextToConfig(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	dir := t.TempDir()
	statusPath := filepath.Join(dir, "dev-ephemeral.json")

	err := writeStatusFile(statusPath, ephemeralStatus{
		PID:          1001,
		BackendPID:   1002,
		FrontendPID:  1003,
		BackendPort:  39401,
		FrontendPort: 39402,
		ConfigPath:   filepath.Join(dir, "config.toml"),
		DataDir:      filepath.Join(dir, "data"),
		BackendURL:   "http://127.0.0.1:39401",
		FrontendURL:  "http://127.0.0.1:39402",
	})
	require.NoError(err)

	content, err := os.ReadFile(statusPath)
	require.NoError(err)

	var got ephemeralStatus
	require.NoError(json.Unmarshal(content, &got))
	assert.Equal(1001, got.PID)
	assert.Equal(1002, got.BackendPID)
	assert.Equal(1003, got.FrontendPID)
	assert.Equal(39401, got.BackendPort)
	assert.Equal(39402, got.FrontendPort)
	assert.Equal(filepath.Join(dir, "config.toml"), got.ConfigPath)
}

func writeSQLiteMarker(t *testing.T, path, value string) {
	t.Helper()
	require := require.New(t)
	db, err := sql.Open("sqlite", path)
	require.NoError(err)
	defer db.Close()
	_, err = db.Exec("CREATE TABLE marker (value TEXT NOT NULL)")
	require.NoError(err)
	_, err = db.Exec("INSERT INTO marker (value) VALUES (?)", value)
	require.NoError(err)
}

func readSQLiteMarker(t *testing.T, path string) string {
	t.Helper()
	require := require.New(t)
	db, err := sql.Open("sqlite", path)
	require.NoError(err)
	defer db.Close()
	var value string
	require.NoError(db.QueryRow("SELECT value FROM marker").Scan(&value))
	return value
}
