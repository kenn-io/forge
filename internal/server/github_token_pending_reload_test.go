package server

import (
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/tokenauth"
)

func TestGitHubTokenAfterPendingAppReload(t *testing.T) { //nolint:paralleltest // t.Setenv isolates the boot and pending token variables.
	require := require.New(t)
	assert := assert.New(t)
	t.Setenv("KENN_FORGE_RELOAD_OLD_TOKEN", "active-token")
	t.Setenv("KENN_FORGE_RELOAD_NEW_TOKEN", "pending-token")
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	const boot = `sync_interval = "5m"
github_token_env = "KENN_FORGE_RELOAD_OLD_TOKEN"
host = "127.0.0.1"
port = 8091
`
	require.NoError(os.WriteFile(cfgPath, []byte(boot), 0o600))
	cfg, err := config.Load(cfgPath)
	require.NoError(err)
	sources, _ := reloadTestTokenSources(t, cfgPath, tokenauth.Key{Platform: "github", Host: "github.com"})
	var requests atomic.Int64
	f := newGitHubTokenFixture(t, cfg, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal("/api/v3/user/repos", r.URL.Path)
		assert.Equal("Bearer active-token", r.Header.Get("Authorization"))
		requests.Add(1)
		_, _ = w.Write([]byte(`[{"full_name":"acme/widget","default_branch":"main"}]`))
	}, ServerOptions{TokenSources: sources, HostCheckAllowLoopbackAnyPort: true})
	srv, ts := f.srv, f.ts
	// Supply the path after construction so only our explicit reload runs.
	srv.configReloadMu.Lock()
	srv.cfgPath = cfgPath
	srv.configReloadMu.Unlock()

	require.NoError(os.WriteFile(filepath.Join(dir, "app.pem"), []byte("pem"), 0o600))
	require.NoError(os.WriteFile(cfgPath, []byte(`sync_interval = "5m"
github_token_env = "KENN_FORGE_RELOAD_NEW_TOKEN"
host = "127.0.0.1"
port = 8091
[[github_apps]]
host = "github.com"
app_id = 4242
private_key_path = "app.pem"
installation_id = 7
installation_account = "acme"
repository_selection = "all"
`), 0o600))
	event := srv.configreload.ApplyConfigChange(t.Context())
	require.True(event.Valid, "reload error: %s", event.Error)
	require.True(event.RestartRequired)
	tooling := decodeToolingStatus(t, ts)
	require.NotNil(tooling.GitHubToken)
	assert.True(tooling.GitHubToken.Ready)
	assert.Positive(requests.Load(), "readiness must probe the active token")
	assert.Equal("KENN_FORGE_RELOAD_OLD_TOKEN", tooling.GitHubToken.Source)
}
