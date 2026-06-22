package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/config"
	"go.kenn.io/middleman/internal/tokenauth"
)

// TestCollectProviderTokensInvokesGHWithHostnameForEnterprise wires the
// host-scoped fallback together with the production startup token
// collector. A fake `gh` placed on PATH records its argv; loading a TOML
// config that points at a GitHub Enterprise host and calling the same
// collector main() uses verifies (a) the resolved token map carries the
// host-scoped value, (b) `gh auth token --hostname <host>` was actually
// invoked. Unit tests pin the helper in isolation; this test pins the
// wiring from TOML parsing through subprocess invocation as the daemon
// would run it on startup.
func TestCollectProviderTokensInvokesGHWithHostnameForEnterprise(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	// Fake gh on PATH. Records argv (one invocation per line) and
	// emits a host-scoped token on stdout.
	dir := t.TempDir()
	argvPath := filepath.Join(dir, "argv")
	ghPath := filepath.Join(dir, "gh")
	if runtime.GOOS == "windows" {
		ghPath += ".cmd"
	}
	const fakeToken = "ghe-token-from-gh"
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> \"$FAKE_GH_ARGV\"\n" +
		"printf '%s\\n' '" + fakeToken + "'\n"
	if runtime.GOOS == "windows" {
		script = "@echo off\r\n" +
			"echo %*>>\"%FAKE_GH_ARGV%\"\r\n" +
			"echo " + fakeToken + "\r\n"
	}
	require.NoError(os.WriteFile(ghPath, []byte(script), 0o755))
	t.Setenv("PATH", dir)
	t.Setenv("FAKE_GH_ARGV", argvPath)

	// Clear the env var that would short-circuit the gh fallback.
	t.Setenv("MIDDLEMAN_GITHUB_TOKEN", "")

	// Config with a GHE repo. Loading via the public API exercises
	// the same parsing path the daemon takes.
	configPath := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(os.WriteFile(configPath, []byte(`
host = "127.0.0.1"
port = 8091

[[repos]]
platform = "github"
platform_host = "ghe.example.com"
owner = "acme"
name = "widget"
`), 0o644))
	cfg, err := config.Load(configPath)
	require.NoError(err)

	set := tokenauth.NewSourceSet(tokenauth.Options{
		GitHubCLI: config.GitHubCLITokenForHost,
	})
	sources, err := collectProviderTokenSources(t.Context(), cfg, set)
	require.NoError(err)

	key := providerHostKey("github", "ghe.example.com")
	got, err := sources[key].Token(t.Context())
	require.NoError(err)
	assert.Equal(fakeToken, got,
		"GHE provider key should resolve to the gh-supplied host token")

	data, err := os.ReadFile(argvPath)
	require.NoError(err)
	invocations := strings.Split(
		strings.TrimRight(string(data), "\r\n"), "\n",
	)
	for i := range invocations {
		invocations[i] = strings.TrimRight(invocations[i], "\r")
	}
	assert.Contains(invocations, "auth token --hostname ghe.example.com",
		"expected gh auth token --hostname ghe.example.com invocation; got: %v",
		invocations,
	)
}

func TestCollectProviderTokenSourcesReadsRotatedTokenFile(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	tokenPath := filepath.Join(t.TempDir(), "token")
	require.NoError(os.WriteFile(tokenPath, []byte("first\n"), 0o600))
	cfg := &config.Config{
		GitHubTokenEnv: "MIDDLEMAN_GITHUB_TOKEN",
		SyncInterval:   "5m",
		Host:           "127.0.0.1",
		Port:           8091,
		BasePath:       "/",
		Activity: config.Activity{
			ViewMode:  "flat",
			TimeRange: "7d",
		},
		Repos: []config.Repo{{
			Owner:        "acme",
			Name:         "widget",
			Platform:     "github",
			PlatformHost: "github.com",
			TokenFile:    tokenPath,
		}},
	}
	require.NoError(cfg.Validate())

	set := tokenauth.NewSourceSet(tokenauth.Options{})
	sources, err := collectProviderTokenSources(t.Context(), cfg, set)
	require.NoError(err)
	src := sources[providerHostKey("github", "github.com")]

	first, err := src.Token(t.Context())
	require.NoError(err)
	require.NoError(os.WriteFile(tokenPath, []byte("second\n"), 0o600))
	second, err := src.Token(t.Context())
	require.NoError(err)

	assert.Equal("first", first)
	assert.Equal("second", second)
}
