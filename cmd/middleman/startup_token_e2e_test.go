package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/middleman/internal/config"
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

	// Fake gh on PATH. Records argv (one invocation per line) and
	// emits a host-scoped token on stdout.
	dir := t.TempDir()
	argvPath := filepath.Join(dir, "argv")
	ghPath := filepath.Join(dir, "gh")
	const fakeToken = "ghe-token-from-gh"
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> \"$FAKE_GH_ARGV\"\n" +
		"printf '%s\\n' '" + fakeToken + "'\n"
	require.NoError(t, os.WriteFile(ghPath, []byte(script), 0o755))
	t.Setenv("PATH", dir)
	t.Setenv("FAKE_GH_ARGV", argvPath)

	// Clear the env var that would short-circuit the gh fallback.
	t.Setenv("MIDDLEMAN_GITHUB_TOKEN", "")

	// Config with a GHE repo. Loading via the public API exercises
	// the same parsing path the daemon takes.
	configPath := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, os.WriteFile(configPath, []byte(`
host = "127.0.0.1"
port = 0

[[repos]]
platform = "github"
platform_host = "ghe.example.com"
owner = "acme"
name = "widget"
`), 0o644))
	cfg, err := config.Load(configPath)
	require.NoError(t, err)

	tokens, err := collectProviderTokens(cfg)
	require.NoError(t, err)

	key := providerHostKey("github", "ghe.example.com")
	assert.Equal(fakeToken, tokens[key],
		"GHE provider key should resolve to the gh-supplied host token")

	data, err := os.ReadFile(argvPath)
	require.NoError(t, err)
	invocations := strings.Split(
		strings.TrimRight(string(data), "\n"), "\n",
	)
	assert.Contains(invocations, "auth token --hostname ghe.example.com",
		"expected gh auth token --hostname ghe.example.com invocation; got: %v",
		invocations,
	)
}
