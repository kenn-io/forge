package server

import (
	"encoding/json"
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
	for _, tc := range []struct {
		name    string
		pending string
	}{
		{name: "tooling provenance", pending: "pending-token"},
		{name: "discovery with unset pending token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			assert := assert.New(t)
			t.Setenv("KENN_FORGE_RELOAD_OLD_TOKEN", "active-token")
			t.Setenv("KENN_FORGE_RELOAD_NEW_TOKEN", tc.pending)
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
			sources, source := reloadTestTokenSources(t, cfgPath, tokenauth.Key{Platform: "github", Host: "github.com"})
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

			if tc.pending != "" {
				tooling := decodeToolingStatus(t, ts)
				require.NotNil(tooling.GitHubToken)
				require.True(tooling.GitHubToken.Ready)
				require.Equal("KENN_FORGE_RELOAD_OLD_TOKEN", tooling.GitHubToken.Source)
			} else {
				status, _ := decodeUserRepositories(t, ts, "/api/v1/platform/user-repositories?provider=github&platform_host=github.com")
				require.Equal(http.StatusOK, status)
			}

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
			token, err := source.Token(t.Context())
			require.NoError(err)
			require.Equal("active-token", token, "the live credential remains usable until restart")
			requests.Store(0)
			f.ghCalls.Store(0)
			f.now.Add(31) // Expire the tooling-status cache before observing the reload.

			if tc.pending != "" {
				tooling := decodeToolingStatus(t, ts)
				require.NotNil(tooling.GitHubToken)
				assert.True(tooling.GitHubToken.Ready)
				assert.Positive(requests.Load(), "readiness must probe the active token")
				assert.Equal("KENN_FORGE_RELOAD_OLD_TOKEN", tooling.GitHubToken.Source)
				return
			}
			status, body := decodeUserRepositories(t, ts, "/api/v1/platform/user-repositories?provider=github&platform_host=github.com")
			assert.Equal(http.StatusOK, status, "discovery must keep using the live token: %s", body)
			assert.Zero(f.ghCalls.Load(), "a usable live token must avoid the unavailable CLI")
			assert.Positive(requests.Load(), "discovery must reach the provider with the active token")
			var listing struct {
				Repositories []struct {
					NameWithOwner string `json:"name_with_owner"`
				} `json:"repositories"`
			}
			require.NoError(json.Unmarshal(body, &listing))
			require.Len(listing.Repositories, 1)
			assert.Equal("acme/widget", listing.Repositories[0].NameWithOwner)
		})
	}
}
