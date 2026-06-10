package server

import (
	"fmt"
	"log/slog"
	"testing"
	"time"

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/config"
)

func TestDocFolderDaemonBindingWarnsWhenCatalogTargetMissingOnStartup(t *testing.T) {
	assert := Assert.New(t)

	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	writeKataServerCatalog(t, home, `
[[daemon]]
name = "home"
local = true
`)

	logBuf := &lockedBuffer{}
	origLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(origLogger) })

	root := t.TempDir()
	cfg := &config.Config{
		SyncInterval: "5m",
		DocFolders: []config.DocFolder{
			{ID: "notes", Name: "Notes", Path: root, Daemon: "gone"},
		},
	}
	srv := New(openTestDB(t), nil, nil, "/", cfg, ServerOptions{})
	t.Cleanup(func() { gracefulShutdown(t, srv) })

	logs := logBuf.String()
	assert.Contains(logs, "doc folder references missing Kata daemon")
	assert.Contains(logs, "folder=notes")
	assert.Contains(logs, "daemon=gone")
}

func TestConfigReloadWarnsWhenDocFolderDaemonBindingTargetIsMissing(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	writeKataServerCatalog(t, home, `
[[daemon]]
name = "home"
local = true
`)

	initialRoot := t.TempDir()
	updatedRoot := t.TempDir()
	initialConfig := validReloadConfigWithDocFolderDaemon("notes", "Notes", initialRoot, "home")
	updatedConfig := validReloadConfigWithDocFolderDaemon("handbook", "Handbook", updatedRoot, "gone")
	srv, _, cfgPath := setupTestServerWithConfigContent(t, initialConfig, &mockGH{})
	t.Cleanup(func() { gracefulShutdown(t, srv) })

	logBuf := &lockedBuffer{}
	origLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(origLogger) })

	writeConfigToml(t, cfgPath, updatedConfig)

	ev := srv.applyConfigChange(t.Context())
	require.True(ev.Valid)
	assert.False(ev.RestartRequired)

	logs := logBuf.String()
	require.NotEmpty(logs)
	assert.Contains(logs, "doc folder references missing Kata daemon")
	assert.Contains(logs, "folder=handbook")
	assert.Contains(logs, "daemon=gone")
}

func TestConfigWatcherWarnsWhenDocFolderDaemonBindingTargetIsMissing(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	writeKataServerCatalog(t, home, `
[[daemon]]
name = "home"
local = true
`)

	initialRoot := t.TempDir()
	updatedRoot := t.TempDir()
	initialConfig := validReloadConfigWithDocFolderDaemon("notes", "Notes", initialRoot, "home")
	updatedConfig := validReloadConfigWithDocFolderDaemon("handbook", "Handbook", updatedRoot, "gone")
	srv, _, cfgPath := setupTestServerWithConfigContent(t, initialConfig, &mockGH{})
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	waitForConfigWatcher(t, srv, 2*time.Second)
	stream := streamConfigEvents(t, srv)
	defer stream.Close()

	logBuf := &lockedBuffer{}
	origLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(origLogger) })

	writeConfigToml(t, cfgPath, updatedConfig)

	ev := waitForConfigEvent(t, stream, 2*time.Second)
	require.True(ev.Valid)
	assert.False(ev.RestartRequired)

	logs := logBuf.String()
	require.NotEmpty(logs)
	assert.Contains(logs, "doc folder references missing Kata daemon")
	assert.Contains(logs, "folder=handbook")
	assert.Contains(logs, "daemon=gone")
}

func validReloadConfigWithDocFolderDaemon(id, name, root, daemon string) string {
	return validReloadConfig + fmt.Sprintf(`
[[doc_folders]]
id = %q
name = %q
path = %q
daemon = %q
`, id, name, root, daemon)
}
