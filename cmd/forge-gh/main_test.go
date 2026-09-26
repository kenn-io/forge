package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/ghshim"
	"go.kenn.io/forge/internal/procutil"
	"go.kenn.io/forge/internal/runtimelock"
	"go.kenn.io/forge/internal/testutil/gitsafe"
)

func TestRepositoryOverrides(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	t.Setenv("GH_REPO", "github.com/acme/env-repo")
	t.Setenv("GH_HOST", "github.com")
	for _, tc := range []struct{ input, host, repo string }{
		{"", "github.com", "env-repo"},
		{"acme/widget", "github.com", "widget"},
		{"https://github.com/acme/widget.git", "github.com", "widget"},
		{"git@code.example:acme/widget.git", "code.example", "widget"},
		{"code.example/acme/widget", "code.example", "widget"},
	} {
		var q ghshim.Query
		require.True(resolveRepo(&q, tc.input))
		assert.Equal(tc.host, q.Host)
		assert.Equal(tc.repo, q.Repo)
	}
}

func TestDaemonDiscoveryUsesRuntimeBasePathAndBearer(t *testing.T) {
	t.Run("explicit config", func(t *testing.T) {
		dir := t.TempDir()
		configPath := filepath.Join(dir, "shim-config.toml")
		t.Setenv("KENN_FORGE_HOME", t.TempDir())
		t.Setenv("FORGE_GH_CONFIG", configPath)
		assertDaemonDiscovery(t, dir, configPath)
	})
	t.Run("default config", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("KENN_FORGE_HOME", dir)
		t.Setenv("FORGE_GH_CONFIG", "")
		assertDaemonDiscovery(t, dir, filepath.Join(dir, "config.toml"))
	})
}

func assertDaemonDiscovery(t *testing.T, dir, configPath string) {
	t.Helper()
	require := require.New(t)
	assert := assert.New(t)
	require.NoError(os.WriteFile(configPath, fmt.Appendf(nil, "data_dir = %q\nbase_path = \"/changed-config\"\n", dir), 0o600))
	token, err := runtimelock.EnsureAuthToken(dir)
	require.NoError(err)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal("/running/api/v1/gh/query", r.URL.Path)
		assert.Equal("Bearer "+token, r.Header.Get("Authorization"))
		var q ghshim.Query
		if !assert.NoError(json.NewDecoder(r.Body).Decode(&q)) {
			http.Error(w, "bad query", 400)
			return
		}
		assert.Equal("widget", q.Repo)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"handled":true,"output":"[{\"number\":7}]\n","reason":"served"}`))
	}))
	defer server.Close()
	lock, err := runtimelock.Acquire(dir)
	require.NoError(err)
	defer func() { require.NoError(lock.Release()) }()
	require.NoError(lock.WriteMetadata(runtimelock.Metadata{PID: os.Getpid(), ListenAddr: strings.TrimPrefix(server.URL, "http://"), BasePath: "/running"}))
	output, handled, reason := queryDaemon(ghshim.Query{Repo: "widget"})
	assert.True(handled)
	assert.Equal("served", reason)
	assert.Equal("[{\"number\":7}]\n", output)
}

func TestDefaultHostMatchesGHSoleConfiguredHost(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	dir := t.TempDir()
	t.Setenv("GH_CONFIG_DIR", dir)
	t.Setenv("GH_HOST", "")
	require.NoError(os.WriteFile(filepath.Join(dir, "hosts.yml"), []byte("code.example:\n  user: user-a\n"), 0o600))
	assert.Equal("code.example", defaultGHHost())
	require.NoError(os.WriteFile(filepath.Join(dir, "hosts.yml"), []byte("code.example: {}\ngithub.com: {}\n"), 0o600))
	assert.Equal("github.com", defaultGHHost())
	t.Setenv("GH_HOST", "override.example")
	assert.Equal("override.example", defaultGHHost())
}

var fixtureGitExecutable string

func TestMain(m *testing.M) {
	// Preserve the immutable executable selected by git-safe before isolation
	// removes GIT_* variables and replaces its config home.
	fixtureGitExecutable = os.Getenv("GIT_SAFE_REAL_GIT")
	os.Exit(gitsafe.RunIsolatedMain(m))
}

func TestRemoteResolutionDelegatesAmbiguousSelections(t *testing.T) {
	require := require.New(t)
	if fixtureGitExecutable != "" {
		t.Setenv("GIT_SAFE_REAL_GIT", fixtureGitExecutable)
	}
	assert := assert.New(t)
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("GH_REPO", "")
	t.Setenv("GH_HOST", "")
	t.Setenv("GH_CONFIG_DIR", t.TempDir())
	for _, args := range [][]string{{"init"}, {"config", "gc.auto", "0"}, {"config", "maintenance.auto", "false"}, {"remote", "add", "origin", "https://github.com/acme/widget.git"}} {
		output, err := procutil.CommandContext(t.Context(), "git", args...).CombinedOutput()
		require.NoError(err, string(output))
	}
	var q ghshim.Query
	require.True(resolveRepo(&q, ""))
	assert.Equal("widget", q.Repo)
	t.Setenv("GH_HOST", "other.example")
	assert.False(resolveRepo(&q, ""))
	t.Setenv("GH_HOST", "")
	output, err := procutil.CommandContext(t.Context(), "git", "config", "remote.origin.gh-resolved", "acme/parent").CombinedOutput()
	require.NoError(err, string(output))
	assert.False(resolveRepo(&q, ""))
	output, err = procutil.CommandContext(t.Context(), "git", "config", "remote.origin.gh-resolved", "base").CombinedOutput()
	require.NoError(err, string(output))
	output, err = procutil.CommandContext(t.Context(), "git", "remote", "add", "upstream", "https://github.com/acme/parent.git").CombinedOutput()
	require.NoError(err, string(output))
	assert.False(resolveRepo(&q, ""))
}
