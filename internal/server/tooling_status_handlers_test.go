package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/server/repoapi"
	"go.kenn.io/forge/internal/testutil/dbtest"
	serverfake "go.kenn.io/forge/internal/testutil/serverfake"
	"go.kenn.io/forge/internal/tokenauth"
)

// recordingToolingRunner simulates probe subprocesses. Each call is
// recorded as "name arg1 arg2 ..."; outputs/errors key on the same
// joined string, and unmatched commands succeed with empty output.
type recordingToolingRunner struct {
	mu      sync.Mutex
	calls   []string
	outputs map[string]string
	errs    map[string]error
}

func (r *recordingToolingRunner) run(
	_ context.Context, name string, args ...string,
) ([]byte, error) {
	key := strings.Join(append([]string{name}, args...), " ")
	r.mu.Lock()
	r.calls = append(r.calls, key)
	r.mu.Unlock()
	if err, ok := r.errs[key]; ok {
		return nil, err
	}
	return []byte(r.outputs[key]), nil
}

func decodeToolingStatus(
	t *testing.T, ts *httptest.Server,
) repoapi.ToolingStatusBody {
	t.Helper()
	resp := serverfake.HttpDo(t, ts, http.MethodGet, "/api/v1/tooling-status", nil)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var body repoapi.ToolingStatusBody
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	return body
}

// TestToolingStatusHappyPath covers GET /api/v1/tooling-status with
// every CLI present and authenticated: versions, hosts, and users are
// reported in the shape the UI's ToolingStatus contract expects.
func TestToolingStatusHappyPath(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)

	srv, _, _ := setupTestServer(t)
	runner := &recordingToolingRunner{
		outputs: map[string]string{
			"git --version": "git version 2.44.0 (Apple Git-170)",
			"gh api user --hostname github.com --jq .login": "octocat\n",
			"glab api user --hostname gitlab.com":           `{"username":"tanuki"}`,
		},
	}
	srv.toolingRun = runner.run
	ts := httptest.NewServer(srv)
	defer ts.Close()

	body := decodeToolingStatus(t, ts)

	assert.True(body.Git.Available)
	assert.Equal("2.44.0", body.Git.Version)
	assert.True(body.Gh.Available)
	assert.True(body.Gh.Authenticated)
	assert.Equal("octocat", body.Gh.User)
	assert.Equal("github.com", body.Gh.Host)
	assert.True(body.Glab.Available)
	assert.True(body.Glab.Authenticated)
	assert.Equal("tanuki", body.Glab.User)
	assert.Equal("gitlab.com", body.Glab.Host)
}

// TestToolingStatusMissingGh covers the recovery-copy path: gh is not
// installed, so gh reports unavailable without auth probes, while git
// and glab report independently.
func TestToolingStatusMissingGh(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)

	srv, _, _ := setupTestServer(t)
	runner := &recordingToolingRunner{
		outputs: map[string]string{
			"git --version": "git version 2.44.0",
		},
		errs: map[string]error{
			"gh --version":                           errors.New("executable file not found in $PATH"),
			"glab auth status --hostname gitlab.com": errors.New("not logged in"),
		},
	}
	srv.toolingRun = runner.run
	ts := httptest.NewServer(srv)
	defer ts.Close()

	body := decodeToolingStatus(t, ts)

	assert.True(body.Git.Available)
	assert.False(body.Gh.Available)
	assert.Equal("github.com", body.Gh.Host)
	assert.False(body.Gh.Authenticated)
	assert.Empty(body.Gh.User)
	assert.True(body.Glab.Available)
	assert.False(body.Glab.Authenticated)
	for _, call := range runner.calls {
		assert.False(strings.HasPrefix(call, "gh auth"),
			"no auth probe for an unavailable CLI: %s", call)
		assert.False(strings.HasPrefix(call, "gh api"),
			"no user probe for an unavailable CLI: %s", call)
	}
}

// TestToolingStatusCachesProbes verifies rapid repeat requests serve
// the cached status instead of re-spawning probe subprocesses.
func TestToolingStatusCachesProbes(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)

	srv, _, _ := setupTestServer(t)
	runner := &recordingToolingRunner{}
	srv.toolingRun = runner.run
	ts := httptest.NewServer(srv)
	defer ts.Close()

	decodeToolingStatus(t, ts)
	probesAfterFirst := len(runner.calls)
	decodeToolingStatus(t, ts)

	assert.Positive(probesAfterFirst)
	assert.Len(runner.calls, probesAfterFirst,
		"second request within the TTL must not re-probe")
}

// TestToolingStatusProbesConfiguredHosts verifies provider-awareness:
// configured self-hosted platform hosts are probed instead of the
// public defaults, and the response reports the probed host.
func TestToolingStatusProbesConfiguredHosts(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)

	srv, _, _ := setupTestServer(t)
	srv.cfgMu.Lock()
	srv.cfg = &config.Config{
		Platforms: []config.PlatformConfig{
			{Type: "github", Host: "ghe.example.com"},
			{Type: "gitlab", Host: "gitlab.example.com"},
		},
	}
	srv.cfgMu.Unlock()
	runner := &recordingToolingRunner{
		outputs: map[string]string{
			"gh api user --hostname ghe.example.com --jq .login": "hubber",
			"glab api user --hostname gitlab.example.com":        `{"username":"laber"}`,
		},
	}
	srv.toolingRun = runner.run
	ts := httptest.NewServer(srv)
	defer ts.Close()

	body := decodeToolingStatus(t, ts)

	assert.Equal("ghe.example.com", body.Gh.Host)
	assert.Equal("hubber", body.Gh.User)
	assert.Equal("gitlab.example.com", body.Glab.Host)
	assert.Equal("laber", body.Glab.User)
	require.Contains(runner.calls,
		"gh auth token --hostname ghe.example.com")
	require.Contains(runner.calls,
		"glab auth status --hostname gitlab.example.com")
}

func TestToolingStatusGitHubToken(t *testing.T) {
	for _, tt := range []struct {
		name        string
		host        string
		globalToken string
		platformEnv string
		status      int
		wantEnv     string
		wantSource  string
		wantToken   string
	}{
		{name: "platform token wins", host: "github.com", globalToken: "global-token", platformEnv: "TEST_PLATFORM_TOKEN", status: http.StatusOK, wantEnv: "TEST_PLATFORM_TOKEN", wantSource: "TEST_PLATFORM_TOKEN", wantToken: "platform-token"},
		{name: "Enterprise platform token", host: "ghe.example.com", globalToken: "global-token", platformEnv: "TEST_PLATFORM_TOKEN", status: http.StatusOK, wantEnv: "TEST_PLATFORM_TOKEN", wantSource: "TEST_PLATFORM_TOKEN", wantToken: "platform-token"},
		{name: "Enterprise excludes global token", host: "ghe.example.com", globalToken: "global-token"},
		{name: "rejected token", host: "github.com", globalToken: "global-token", status: http.StatusUnauthorized, wantEnv: "KENN_FORGE_GITHUB_TOKEN", wantSource: "KENN_FORGE_GITHUB_TOKEN", wantToken: "global-token"},
		{name: "no credential", host: "github.com", wantEnv: "KENN_FORGE_GITHUB_TOKEN"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			t.Setenv("KENN_FORGE_GITHUB_TOKEN", tt.globalToken)
			t.Setenv("TEST_PLATFORM_TOKEN", "platform-token")
			cfg := &config.Config{
				GitHubTokenEnv: "KENN_FORGE_GITHUB_TOKEN",
				Platforms:      []config.PlatformConfig{{Type: "github", Host: tt.host, TokenEnv: tt.platformEnv}},
			}
			var requests atomic.Int32
			f := newGitHubTokenFixture(t, cfg, func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				assert.Equal("/api/v3/user/repos", r.URL.Path)
				assert.Equal("1", r.URL.Query().Get("page"))
				assert.Equal("1", r.URL.Query().Get("per_page"))
				assert.Equal("Bearer "+tt.wantToken, r.Header.Get("Authorization"))
				w.WriteHeader(tt.status)
				if tt.status == http.StatusOK {
					_, _ = w.Write([]byte(`[]`))
				} else {
					_, _ = w.Write([]byte(`{"message":"Repository listing unavailable"}`))
				}
			}, ServerOptions{})
			body := decodeToolingStatus(t, f.ts)
			if tt.wantEnv == "" && tt.wantSource == "" {
				assert.Nil(body.GitHubToken)
				assert.Zero(requests.Load())
				return
			}
			require.NotNil(body.GitHubToken)
			assert.Equal(tt.wantEnv, body.GitHubToken.Env)
			assert.Equal(tt.wantSource, body.GitHubToken.Source)
			assert.Equal(tt.wantSource != "" && tt.status == http.StatusOK, body.GitHubToken.Ready)
			if tt.wantSource == "" {
				assert.Zero(requests.Load())
			} else {
				assert.Positive(requests.Load())
			}
		})
	}

	t.Run("rotated file", func(t *testing.T) {
		assert := assert.New(t)
		require := require.New(t)
		t.Setenv("KENN_FORGE_GITHUB_TOKEN", "")
		tokenFile := filepath.Join(t.TempDir(), "token")
		require.NoError(os.WriteFile(tokenFile, []byte("file-token"), 0o600))
		cfg := &config.Config{
			GitHubTokenEnv: "KENN_FORGE_GITHUB_TOKEN",
			Platforms:      []config.PlatformConfig{{Type: "github", Host: "github.com", TokenFile: tokenFile}},
		}
		f := newGitHubTokenFixture(t, cfg, func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") == "Bearer file-token" {
				_, _ = w.Write([]byte(`[]`))
				return
			}
			assert.Equal("Bearer rejected-token", r.Header.Get("Authorization"))
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"message":"Repository listing unavailable"}`))
		}, ServerOptions{})
		body := decodeToolingStatus(t, f.ts)
		require.NotNil(body.GitHubToken)
		assert.Equal("KENN_FORGE_GITHUB_TOKEN", body.GitHubToken.Env)
		assert.Equal(tokenFile, body.GitHubToken.Source)
		require.True(body.GitHubToken.Ready)

		require.NoError(os.WriteFile(tokenFile, []byte("rejected-token"), 0o600))
		f.now.Add(31)
		body = decodeToolingStatus(t, f.ts)
		require.NotNil(body.GitHubToken)
		assert.Equal(tokenFile, body.GitHubToken.Source)
		assert.False(body.GitHubToken.Ready)
	})

	t.Run("pending app reload", func(t *testing.T) {
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
		for _, host := range []string{"github.com", "GITHUB.COM", "github.com:443"} {
			status, body := decodeUserRepositories(t, ts, "/api/v1/platform/user-repositories?platform_host="+host)
			require.Equal(http.StatusOK, status)
			var listing struct {
				Repositories json.RawMessage `json:"repositories"`
			}
			require.NoError(json.Unmarshal(body, &listing))
			assert.JSONEq(`[{"name_with_owner":"acme/widget","default_branch":"main"}]`, string(listing.Repositories))
		}
	})
}

func TestToolingStatusWithoutSyncerIgnoresGitHubToken(t *testing.T) {
	t.Setenv("KENN_FORGE_GITHUB_TOKEN", "test-token")
	database := dbtest.Open(t)
	cfg := &config.Config{GitHubTokenEnv: "KENN_FORGE_GITHUB_TOKEN"}
	srv := New(database, nil, nil, "/", cfg, ServerOptions{})
	t.Cleanup(func() { serverfake.GracefulShutdown(t, srv) })
	runner := &recordingToolingRunner{}
	srv.toolingRun = runner.run
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)

	tooling := decodeToolingStatus(t, ts)
	assert.Nil(t, tooling.GitHubToken)
}
