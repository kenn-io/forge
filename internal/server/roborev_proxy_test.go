package server

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/config"
	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/testutil/dbtest"
)

// setupTestServerWithRoborev creates a server with the roborev
// proxy configured to point at the given endpoint URL.
func setupTestServerWithRoborev(
	t *testing.T, roborevEndpoint string,
) *Server {
	t.Helper()

	dir := t.TempDir()
	database := dbtest.Open(t)

	cfgContent := fmt.Sprintf(`
sync_interval = "5m"
github_token_env = "KENN_FORGE_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "widget"

[roborev]
endpoint = %q
`, roborevEndpoint)

	cfgPath := filepath.Join(dir, "config.toml")
	err := os.WriteFile(cfgPath, []byte(cfgContent), 0o644)
	require.NoError(t, err)

	cfg, err := config.Load(cfgPath)
	require.NoError(t, err)

	mock := &mockGH{}
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": mock},
		database, nil, nil, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	return NewWithConfig(
		database, syncer, nil, nil, cfg, cfgPath,
		ServerOptions{HostCheckAllowLoopbackAnyPort: true},
	)
}
