package reposervertest

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/db"
	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/server"
	"go.kenn.io/forge/internal/testutil/dbtest"
)

const defaultTestConfigContent = `
sync_interval = "5m"
github_token_env = "KENN_FORGE_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "widget"
`

func setupTestServerWithConfig(
	t *testing.T,
) (*server.Server, *db.DB, string, *ghclient.Syncer) {
	return setupTestServerWithConfigContent(t, defaultTestConfigContent, &mockGH{})
}

func setupTestServerWithConfigContent(
	t *testing.T,
	cfgContent string,
	mock *mockGH,
) (*server.Server, *db.DB, string, *ghclient.Syncer) {
	return setupTestServerWithConfigContentAndOptions(
		t, cfgContent, mock, server.ServerOptions{HostCheckAllowLoopbackAnyPort: true},
	)
}

func setupTestServerWithConfigContentAndOptions(
	t *testing.T,
	cfgContent string,
	mock *mockGH,
	options server.ServerOptions,
) (*server.Server, *db.DB, string, *ghclient.Syncer) {
	t.Helper()

	dir := t.TempDir()
	database := dbtest.Open(t)
	cfgPath := filepath.Join(dir, "config.toml")
	err := os.WriteFile(cfgPath, []byte(cfgContent), 0o644)
	require.NoError(t, err)

	cfg, err := config.Load(cfgPath)
	require.NoError(t, err)

	clients := map[string]ghclient.Client{"github.com": mock}
	resolved := ghclient.ResolveConfiguredRepos(
		t.Context(), clients, cfg.Repos,
	)
	syncer := ghclient.NewSyncer(
		clients, database, nil, resolved.Expanded,
		time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	srv := server.NewWithConfig(
		database, syncer, nil, nil, cfg, cfgPath,
		options,
	)
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	return srv, database, cfgPath, syncer
}
