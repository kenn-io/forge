package config

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/internal/tokenauth"
)

// setFakeGlabCLIScript installs a fake `glab` on PATH that records argv
// and emits opts, mirroring setFakeGHCLIScript.
func setFakeGlabCLIScript(t *testing.T, opts fakeGHCLIOptions) string {
	t.Helper()
	dir := t.TempDir()
	argvPath := filepath.Join(dir, "argv")
	writeFakeCLI(t, dir, "glab", fakeGHCLIScript(t, opts))
	t.Setenv("FAKE_GH_ARGV", argvPath)
	return argvPath
}

// isolateForgejoCLIKeys points every platform's fj data directory at a
// fresh temp dir and returns the keys file path fj would use there.
func isolateForgejoCLIKeys(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "xdg"))
	t.Setenv("APPDATA", filepath.Join(dir, "appdata"))
	path, err := forgejoCLIKeysPath()
	require.NoError(t, err)
	return path
}

func writeForgejoCLIKeys(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
}

func TestGitLabCLITokenForHostReadsGlabConfig(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	argvPath := setFakeGlabCLIScript(t, fakeGHCLIOptions{Stdout: "glpat-secret"})

	got, err := GitLabCLITokenForHost(context.Background(), "gitlab.example.test")
	require.NoError(err)
	assert.Equal("glpat-secret", got)

	argv := readFakeGHArgv(t, argvPath)
	require.Len(argv, 1)
	assert.Equal("config get token --host gitlab.example.test", argv[0])
}

func TestGitLabCLITokenForHostIsEmptyWhenHostUnset(t *testing.T) {
	// glab prints nothing and exits zero for an unset key.
	setFakeGlabCLIScript(t, fakeGHCLIOptions{})

	got, err := GitLabCLITokenForHost(context.Background(), "gitlab.example.test")
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestGitLabCLITokenForHostIgnoresFailuresAndProse(t *testing.T) {
	t.Run("nonzero exit", func(t *testing.T) {
		setFakeGlabCLIScript(t, fakeGHCLIOptions{
			Stdout: "glpat-secret", Stderr: "boom", ExitCode: 1,
		})
		got, err := GitLabCLITokenForHost(context.Background(), "gitlab.example.test")
		require.NoError(t, err)
		assert.Empty(t, got)
	})
	t.Run("prose on stdout", func(t *testing.T) {
		setFakeGlabCLIScript(t, fakeGHCLIOptions{Stdout: "Update available: v2"})
		got, err := GitLabCLITokenForHost(context.Background(), "gitlab.example.test")
		require.NoError(t, err)
		assert.Empty(t, got)
	})
	t.Run("binary missing", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		got, err := GitLabCLITokenForHost(context.Background(), "gitlab.example.test")
		require.NoError(t, err)
		assert.Empty(t, got)
	})
}

func TestTokenForPlatformHostFallsBackToGlab(t *testing.T) {
	path := writeConfig(t, `
[[platforms]]
type = "gitlab"
host = "gitlab.example.test"
token_env = "GITLAB_EXAMPLE_TOKEN"
`)
	t.Setenv("GITLAB_EXAMPLE_TOKEN", "")
	setFakeGlabCLIScript(t, fakeGHCLIOptions{Stdout: "glpat-from-cli"})

	cfg, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, "glpat-from-cli",
		cfg.TokenForPlatformHost("gitlab", "gitlab.example.test", ""))

	t.Setenv("GITLAB_EXAMPLE_TOKEN", "glpat-from-env")
	assert.Equal(t, "glpat-from-env",
		cfg.TokenForPlatformHost("gitlab", "gitlab.example.test", ""),
		"declared token_env must win over the CLI")
}

func TestTokenSourceForPlatformHostEndsOnProviderCLI(t *testing.T) {
	cfg, err := Load(writeConfig(t, `
[[platforms]]
type = "gitlab"
host = "gitlab.example.test"
token_env = "GITLAB_EXAMPLE_TOKEN"
`))
	require.NoError(t, err)

	for _, tc := range []struct {
		platform, host, want string
	}{
		{"gitlab", "gitlab.example.test", "env:GITLAB_EXAMPLE_TOKEN -> gitlab_cli:gitlab.example.test"},
		{"gitlab", "gitlab.com", "gitlab_cli:gitlab.com"},
		{"forgejo", "codeberg.org", "env:KENN_FORGE_FORGEJO_TOKEN -> forgejo_cli:codeberg.org"},
		{"forgejo", "forge.example.test", "forgejo_cli:forge.example.test"},
		{"gitea", "gitea.example.test:3000", "forgejo_cli:gitea.example.test:3000"},
		{"github", "ghe.example.test", "github_cli:ghe.example.test"},
	} {
		t.Run(tc.platform+"/"+tc.host, func(t *testing.T) {
			desc := cfg.TokenSourceForPlatformHost(tc.platform, tc.host, "", "")
			assert.Equal(t, tc.want, desc.SafeString())
		})
	}
}

func TestConfiguredCredentialAvailableIgnoresProviderCLIs(t *testing.T) {
	cfg, err := Load(writeConfig(t, `
[[repos]]
platform = "gitlab"
platform_host = "gitlab.example.test"
owner = "team"
name = "service"
`))
	require.NoError(t, err)
	setFakeGlabCLIScript(t, fakeGHCLIOptions{Stdout: "glpat-from-cli"})

	assert.False(t, cfg.ConfiguredCredentialAvailable())
}

func TestForgejoCLITokenForHostReadsKeysFile(t *testing.T) {
	path := isolateForgejoCLIKeys(t)
	writeForgejoCLIKeys(t, path, `{
  "hosts": {
    "codeberg.org": {"type": "Application", "token": "fj-app-token"},
    "forge.example.test:3000": {"type": "Application", "token": "fj-port-token"},
    "gitea.example.test/gitea": {"type": "Application", "token": "fj-subpath-token"}
  },
  "aliases": {"cb": "codeberg.org"},
  "default_ssh": []
}`)

	for _, tc := range []struct{ host, want string }{
		{"codeberg.org", "fj-app-token"},
		{"Codeberg.org", "fj-app-token"},
		{"forge.example.test:3000", "fj-port-token"},
		{"gitea.example.test", "fj-subpath-token"},
		{"cb", "fj-app-token"},
		{"unknown.example.test", ""},
	} {
		t.Run(tc.host, func(t *testing.T) {
			got, err := ForgejoCLITokenForHost(context.Background(), tc.host)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestForgejoCLITokenForHostSkipsExpiredOAuth(t *testing.T) {
	path := isolateForgejoCLIKeys(t)
	future := time.Now().Add(time.Hour).UTC()
	past := time.Now().Add(-time.Hour).UTC()
	writeForgejoCLIKeys(t, path, fmt.Sprintf(`{
  "hosts": {
    "live.example.test": {"type": "OAuth", "token": "fj-live", "refresh_token": "r", "expires_at": %q},
    "stale.example.test": {"type": "OAuth", "token": "fj-stale", "refresh_token": "r", "expires_at": %q},
    "live-array.example.test": {"type": "OAuth", "token": "fj-live-array", "refresh_token": "r", "expires_at": [%d, %d, %d, %d, %d, 0, 0, 0, 0]},
    "stale-array.example.test": {"type": "OAuth", "token": "fj-stale-array", "refresh_token": "r", "expires_at": [%d, %d, %d, %d, %d, 0, 0, 0, 0]},
    "opaque.example.test": {"type": "OAuth", "token": "fj-opaque", "refresh_token": "r", "expires_at": {"unknown": true}}
  }
}`,
		future.Format(time.RFC3339Nano), past.Format(time.RFC3339Nano),
		future.Year(), future.YearDay(), future.Hour(), future.Minute(), future.Second(),
		past.Year(), past.YearDay(), past.Hour(), past.Minute(), past.Second(),
	))

	for _, tc := range []struct{ host, want string }{
		{"live.example.test", "fj-live"},
		{"stale.example.test", ""},
		{"live-array.example.test", "fj-live-array"},
		{"stale-array.example.test", ""},
		{"opaque.example.test", "fj-opaque"},
	} {
		t.Run(tc.host, func(t *testing.T) {
			got, err := ForgejoCLITokenForHost(context.Background(), tc.host)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestForgejoCLITokenForHostMissingOrMalformedFile(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	path := isolateForgejoCLIKeys(t)

	got, err := ForgejoCLITokenForHost(context.Background(), "codeberg.org")
	require.NoError(err)
	assert.Empty(got, "missing keys file is a missing credential")

	writeForgejoCLIKeys(t, path, `{"hosts": [`)
	_, err = ForgejoCLITokenForHost(context.Background(), "codeberg.org")
	require.Error(err)
	assert.Contains(err.Error(), "fj keys file")
}

func TestTokenForPlatformHostFallsBackToForgejoCLI(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	path := isolateForgejoCLIKeys(t)
	writeForgejoCLIKeys(t, path, `{"hosts": {
  "forge.example.test": {"type": "Application", "token": "fj-forgejo"},
  "gitea.example.test": {"type": "Application", "token": "fj-gitea"}
}}`)
	cfg, err := Load(writeConfig(t, `
[[platforms]]
type = "forgejo"
host = "forge.example.test"
token_env = "FORGE_EXAMPLE_TOKEN"

[[platforms]]
type = "gitea"
host = "gitea.example.test"
token_env = "GITEA_EXAMPLE_TOKEN"
`))
	require.NoError(err)
	t.Setenv("FORGE_EXAMPLE_TOKEN", "")
	t.Setenv("GITEA_EXAMPLE_TOKEN", "")

	assert.Equal("fj-forgejo", cfg.TokenForPlatformHost("forgejo", "forge.example.test", ""))
	assert.Equal("fj-gitea", cfg.TokenForPlatformHost("gitea", "gitea.example.test", ""))
	assert.Empty(cfg.TokenForPlatformHost("forgejo", "other.example.test", ""))
}

func TestProviderCLITokenForHostSkipsGitHub(t *testing.T) {
	assert.Equal(t, tokenauth.SourceKind(""), cliSourceKindForPlatform("github"))
	assert.Empty(t, providerCLITokenForHost("github", "github.com"))
}
