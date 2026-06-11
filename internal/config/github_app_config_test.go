package config

import (
	"path/filepath"
	"testing"

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/tokenauth"
)

const githubAppConfigTOML = `
[[repos]]
owner = "kenn-io"
name = "middleman"

[[github_apps]]
host = "github.com"
app_id = 4321
slug = "middleman-abc"
owner = "mariusvniekerk"
owner_type = "User"
private_key_path = "github-app-middleman-abc.pem"
installation_id = 99
installation_account = "kenn-io"
`

func TestLoadGitHubApps(t *testing.T) {
	path := writeConfig(t, githubAppConfigTOML)
	cfg, err := Load(path)
	require.NoError(t, err)
	require.Len(t, cfg.GitHubApps, 1)

	app := cfg.GitHubApps[0]
	assert := Assert.New(t)
	assert.Equal("github.com", app.Host)
	assert.Equal(int64(4321), app.AppID)
	assert.Equal("middleman-abc", app.Slug)
	assert.Equal(int64(99), app.InstallationID)
	assert.Equal("kenn-io", app.InstallationAccount)
	// Relative key paths resolve against the config directory, like
	// token_file does, so the CLI can write portable entries.
	assert.Equal(
		filepath.Join(filepath.Dir(path), "github-app-middleman-abc.pem"),
		app.PrivateKeyPath,
	)
}

func TestGitHubAppsSaveLoadRoundTrip(t *testing.T) {
	cfg, cfg2 := roundTripConfigString(t, githubAppConfigTOML)
	require.Len(t, cfg2.GitHubApps, 1)
	Assert.Equal(t, cfg.GitHubApps[0], cfg2.GitHubApps[0])
}

func TestGitHubAppsValidation(t *testing.T) {
	tests := []struct {
		name    string
		toml    string
		wantErr string
	}{
		{
			name: "missing app id",
			toml: `
[[github_apps]]
host = "github.com"
private_key_path = "key.pem"
`,
			wantErr: "app_id must be a positive integer",
		},
		{
			name: "missing private key path",
			toml: `
[[github_apps]]
host = "github.com"
app_id = 1
`,
			wantErr: "private_key_path is required",
		},
		{
			name: "duplicate host",
			toml: `
[[github_apps]]
app_id = 1
private_key_path = "a.pem"

[[github_apps]]
host = "github.com"
app_id = 2
private_key_path = "b.pem"
`,
			wantErr: `duplicate github app for host "github.com"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, tt.toml))
			require.Error(t, err)
			Assert.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestGitHubAppHostDefaultsToPublicHost(t *testing.T) {
	cfg, err := Load(writeConfig(t, `
[[github_apps]]
app_id = 7
private_key_path = "key.pem"
`))
	require.NoError(t, err)
	require.Len(t, cfg.GitHubApps, 1)
	Assert.Equal(t, "github.com", cfg.GitHubApps[0].Host)
}

func TestTokenSourceChainPrefersGitHubAppOverPATs(t *testing.T) {
	cfg, err := Load(writeConfig(t, `
github_token_env = "MY_PAT"

[[repos]]
owner = "kenn-io"
name = "middleman"
token_env = "REPO_PAT"

[[github_apps]]
host = "github.com"
app_id = 4321
private_key_path = "app.pem"
installation_id = 99
`))
	require.NoError(t, err)

	desc := cfg.TokenSourceForPlatformHost("github", "github.com", "REPO_PAT", "")
	kinds := make([]tokenauth.SourceKind, 0, len(desc.Candidates))
	for _, cand := range desc.Candidates {
		kinds = append(kinds, cand.Kind)
	}
	// Repo-level explicit override stays first; the app outranks the
	// global PAT env and gh CLI fallbacks.
	require.Equal(t, []tokenauth.SourceKind{
		tokenauth.SourceKindEnv,
		tokenauth.SourceKindGitHubApp,
		tokenauth.SourceKindEnv,
		tokenauth.SourceKindGitHubCLI,
	}, kinds)

	assert := Assert.New(t)
	assert.Equal("REPO_PAT", desc.Candidates[0].EnvName)
	app := desc.Candidates[1]
	assert.Equal(int64(4321), app.AppID)
	assert.Equal(int64(99), app.InstallationID)
	assert.Equal("github.com", app.Host)
	assert.True(filepath.IsAbs(app.FilePath), "key path %q", app.FilePath)
	assert.Equal("MY_PAT", desc.Candidates[2].EnvName)
}

func TestTokenSourceChainSkipsAppForOtherHosts(t *testing.T) {
	cfg, err := Load(writeConfig(t, `
[[repos]]
owner = "kenn-io"
name = "middleman"

[[github_apps]]
host = "github.com"
app_id = 4321
private_key_path = "app.pem"
installation_id = 99
`))
	require.NoError(t, err)

	for _, tc := range []struct{ platform, host string }{
		{platform: "github", host: "github.example.com"},
		{platform: "gitlab", host: "gitlab.com"},
	} {
		desc := cfg.TokenSourceForPlatformHost(tc.platform, tc.host, "", "")
		for _, cand := range desc.Candidates {
			Assert.NotEqual(
				t, tokenauth.SourceKindGitHubApp, cand.Kind,
				"%s/%s must not inherit the github.com app", tc.platform, tc.host,
			)
		}
	}
}
