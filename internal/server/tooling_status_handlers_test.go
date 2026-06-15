package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/middleman/internal/config"
)

// recordingToolingRunner simulates probe subprocesses. Each call is
// recorded as "name arg1 arg2 ..."; outputs/errors key on the same
// joined string, and unmatched commands succeed with empty output.
type recordingToolingRunner struct {
	calls   []string
	outputs map[string]string
	errs    map[string]error
}

func (r *recordingToolingRunner) run(
	_ context.Context, name string, args ...string,
) ([]byte, error) {
	key := strings.Join(append([]string{name}, args...), " ")
	r.calls = append(r.calls, key)
	if err, ok := r.errs[key]; ok {
		return nil, err
	}
	return []byte(r.outputs[key]), nil
}

func decodeToolingStatus(
	t *testing.T, ts *httptest.Server,
) toolingStatusBody {
	t.Helper()
	resp := httpDo(t, ts, http.MethodGet, "/api/v1/tooling-status", nil)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var body toolingStatusBody
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	return body
}

// TestToolingStatusHappyPath covers GET /api/v1/tooling-status with
// every CLI present and authenticated: versions, hosts, and users are
// reported in the shape the UI's ToolingStatus contract expects.
func TestToolingStatusHappyPath(t *testing.T) {
	assert := assert.New(t)

	srv, _ := setupTestServer(t)
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
	assert := assert.New(t)

	srv, _ := setupTestServer(t)
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
	assert := assert.New(t)

	srv, _ := setupTestServer(t)
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
	assert := assert.New(t)
	require := require.New(t)

	srv, _ := setupTestServer(t)
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
