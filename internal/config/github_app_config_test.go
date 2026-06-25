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
repository_selection = "all"
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
			name: "installation id without account",
			toml: `
[[github_apps]]
host = "github.com"
app_id = 1
private_key_path = "key.pem"
installation_id = 5
`,
			wantErr: "installation_account is required when installation_id is set",
		},
		{
			name: "duplicate installation account",
			toml: `
[[github_apps]]
app_id = 1
owner = "app-owner-a"
private_key_path = "a.pem"
installation_id = 10
installation_account = "kenn-io"
repository_selection = "all"

[[github_apps]]
host = "github.com"
app_id = 2
owner = "app-owner-b"
private_key_path = "b.pem"
installation_id = 11
installation_account = "KENN-IO"
repository_selection = "all"
`,
			wantErr: `duplicate github app installation for host "github.com" and account "KENN-IO"`,
		},
		{
			name: "duplicate app owner",
			toml: `
[[github_apps]]
app_id = 1
owner = "kenn-io"
private_key_path = "a.pem"

[[github_apps]]
host = "github.com"
app_id = 2
owner = "KENN-IO"
private_key_path = "b.pem"
`,
			wantErr: `duplicate github app for host "github.com" and owner "KENN-IO"`,
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

func TestGitHubAppInstallationCoverage(t *testing.T) {
	tests := []struct {
		name    string
		toml    string
		wantErr string
	}{
		{
			name: "repo owned by installation account passes",
			toml: `
[[repos]]
owner = "kenn-io"
name = "middleman"

[[github_apps]]
app_id = 1
private_key_path = "a.pem"
installation_id = 9
installation_account = "kenn-io"
repository_selection = "all"
`,
		},
		{
			name: "case-insensitive owner match passes",
			toml: `
[[repos]]
owner = "kenn-io"
name = "middleman"

[[github_apps]]
app_id = 1
private_key_path = "a.pem"
installation_id = 9
installation_account = "Kenn-IO"
repository_selection = "all"
`,
		},
		{
			name: "repo owned by another account does not use the app",
			toml: `
[[repos]]
owner = "kenn-io"
name = "middleman"

[[repos]]
owner = "otherorg"
name = "thing"

[[github_apps]]
app_id = 1
private_key_path = "a.pem"
installation_id = 9
installation_account = "kenn-io"
repository_selection = "all"
`,
		},
		{
			name: "uncovered repo with its own token override passes",
			toml: `
[[repos]]
owner = "otherorg"
name = "thing"
token_env = "OTHERORG_TOKEN"

[[github_apps]]
app_id = 1
private_key_path = "a.pem"
installation_id = 9
installation_account = "kenn-io"
repository_selection = "all"
`,
		},
		{
			name: "dormant app without installation ignores coverage",
			toml: `
[[repos]]
owner = "otherorg"
name = "thing"

[[github_apps]]
app_id = 1
private_key_path = "a.pem"
`,
		},
		{
			name: "other-host repos are unaffected",
			toml: `
[[repos]]
platform = "gitlab"
platform_host = "gitlab.com"
owner = "otherorg"
name = "thing"

[[github_apps]]
app_id = 1
private_key_path = "a.pem"
installation_id = 9
installation_account = "kenn-io"
repository_selection = "all"
`,
		},
		{
			name: "selected install covering the repo passes",
			toml: `
[[repos]]
owner = "kenn-io"
name = "middleman"

[[github_apps]]
app_id = 1
private_key_path = "a.pem"
installation_id = 9
installation_account = "kenn-io"
repository_selection = "selected"
selected_repos = ["kenn-io/middleman"]
`,
		},
		{
			name: "same-owner repo outside the selected set fails",
			toml: `
[[repos]]
owner = "kenn-io"
name = "middleman"

[[repos]]
owner = "kenn-io"
name = "added-later"

[[github_apps]]
app_id = 1
private_key_path = "a.pem"
installation_id = 9
installation_account = "kenn-io"
repository_selection = "selected"
selected_repos = ["kenn-io/middleman"]
`,
			wantErr: "kenn-io/added-later is not in the \"Only select repositories\" installation",
		},
		{
			name: "glob repo with a selected install fails",
			toml: `
[[repos]]
owner = "kenn-io"
name = "widget-*"

[[github_apps]]
app_id = 1
private_key_path = "a.pem"
installation_id = 9
installation_account = "kenn-io"
repository_selection = "selected"
selected_repos = ["kenn-io/widget-a"]
`,
			wantErr: "glob pattern",
		},
		{
			name: "all-repositories install skips the selected check",
			toml: `
[[repos]]
owner = "kenn-io"
name = "anything"

[[github_apps]]
app_id = 1
private_key_path = "a.pem"
installation_id = 9
installation_account = "kenn-io"
repository_selection = "all"
`,
		},
		{
			name: "installed app without recorded selection fails",
			toml: `
[[repos]]
owner = "kenn-io"
name = "anything"

[[github_apps]]
app_id = 1
private_key_path = "a.pem"
installation_id = 9
installation_account = "kenn-io"
`,
			wantErr: "repository_selection is required",
		},
		{
			name: "invalid repository_selection value fails",
			toml: `
[[github_apps]]
app_id = 1
private_key_path = "a.pem"
repository_selection = "some"
`,
			wantErr: "repository_selection must be",
		},
		{
			name: "whitespace and case in repository_selection cannot bypass the selected check",
			toml: `
[[repos]]
owner = "kenn-io"
name = "added-later"

[[github_apps]]
app_id = 1
private_key_path = "a.pem"
installation_id = 9
installation_account = "kenn-io"
repository_selection = " Selected "
selected_repos = ["kenn-io/middleman"]
`,
			wantErr: "kenn-io/added-later is not in the \"Only select repositories\" installation",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, tt.toml))
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			Assert.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestLoadForGitHubAppRepairRelaxesOnlyCoverage(t *testing.T) {
	staleSnapshot := `
[[repos]]
owner = "kenn-io"
name = "added-later"

[[github_apps]]
app_id = 1
private_key_path = "a.pem"
installation_id = 9
installation_account = "kenn-io"
repository_selection = "selected"
selected_repos = ["kenn-io/middleman"]
`
	require := require.New(t)
	_, err := Load(writeConfig(t, staleSnapshot))
	require.Error(err, "the strict loader must keep rejecting stale snapshots")
	cfg, err := LoadForGitHubAppRepair(writeConfig(t, staleSnapshot))
	require.NoError(err, "the repair loader exists exactly for this failure")
	require.Len(cfg.GitHubApps, 1)

	// Every other validation rule still applies.
	_, err = LoadForGitHubAppRepair(writeConfig(t, `
[[github_apps]]
app_id = 0
private_key_path = "a.pem"
`))
	require.ErrorContains(err, "app_id must be a positive integer")

	_, err = LoadForGitHubAppRepair(writeConfig(t, `
[[github_apps]]
app_id = 1
private_key_path = "a.pem"
installation_id = 9
`))
	require.ErrorContains(err, "installation_account is required")

	_, err = LoadForGitHubAppRepair(writeConfig(t, `
[[github_apps]]
app_id = 1
private_key_path = "a.pem"
installation_id = 9
installation_account = "kenn-io"
`))
	require.ErrorContains(err, "repository_selection is required")
}

func TestGitHubAppMixedOverrideAndCoveredReposRejected(t *testing.T) {
	// middleman resolves one credential chain per (platform, host), so
	// a repo-level override on one repo cannot coexist with an app
	// chain on another repo of the same host. The coverage error must
	// not suggest that configuration; this pins that it is rejected.
	_, err := Load(writeConfig(t, `
[[repos]]
owner = "kenn-io"
name = "middleman"

[[repos]]
owner = "other-org"
name = "tool"
token_env = "OTHER_ORG_PAT"

[[github_apps]]
host = "github.com"
app_id = 4321
owner = "kenn-io"
private_key_path = "app.pem"
installation_id = 99
installation_account = "kenn-io"
repository_selection = "all"
`))
	require.Error(t, err)
	Assert.ErrorContains(t, err, "conflicting token source")
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

[[github_apps]]
host = "github.com"
app_id = 4321
private_key_path = "app.pem"
installation_id = 99
installation_account = "kenn-io"
repository_selection = "all"
`))
	require.NoError(t, err)

	desc := cfg.TokenSourceForPlatformHost("github", "github.com", "", "")
	kinds := make([]tokenauth.SourceKind, 0, len(desc.Candidates))
	for _, cand := range desc.Candidates {
		kinds = append(kinds, cand.Kind)
	}
	// The app outranks the global PAT env and gh CLI fallbacks.
	require.Equal(t, []tokenauth.SourceKind{
		tokenauth.SourceKindGitHubApp,
		tokenauth.SourceKindEnv,
		tokenauth.SourceKindGitHubCLI,
	}, kinds)

	assert := Assert.New(t)
	app := desc.Candidates[0]
	assert.Equal(int64(4321), app.AppID)
	assert.Equal(int64(99), app.InstallationID)
	assert.Equal("github.com", app.Host)
	assert.Equal("kenn-io", app.InstallationAccount)
	assert.True(filepath.IsAbs(app.FilePath), "key path %q", app.FilePath)
	assert.Equal("MY_PAT", desc.Candidates[1].EnvName)
}

func TestTokenSourceChainIncludesGitHubAppsForEachInstalledAccount(t *testing.T) {
	cfg, err := Load(writeConfig(t, `
github_token_env = "MY_PAT"

[[repos]]
owner = "kenn-io"
name = "middleman"

[[repos]]
owner = "other-org"
name = "tool"

[[github_apps]]
host = "github.com"
app_id = 4321
private_key_path = "app.pem"
installation_id = 99
installation_account = "kenn-io"
repository_selection = "all"

[[github_apps]]
host = "github.com"
app_id = 4322
owner = "other-org"
private_key_path = "other-app.pem"
installation_id = 100
installation_account = "other-org"
repository_selection = "all"
`))
	require.NoError(t, err)

	desc := cfg.TokenSourceForPlatformHost("github", "github.com", "", "")
	var appAccounts []string
	for _, cand := range desc.Candidates {
		if cand.Kind == tokenauth.SourceKindGitHubApp {
			appAccounts = append(appAccounts, cand.InstallationAccount)
		}
	}
	Assert.Equal(t, []string{"kenn-io", "other-org"}, appAccounts)
}

func TestTokenSourceChainRepoOverrideExcludesGitHubApp(t *testing.T) {
	cfg, err := Load(writeConfig(t, `
github_token_env = "MY_PAT"

[[repos]]
owner = "other-org"
name = "tool"
token_env = "OTHER_ORG_PAT"

[[github_apps]]
host = "github.com"
app_id = 4321
private_key_path = "app.pem"
installation_id = 99
installation_account = "other-org"
repository_selection = "all"
`))
	require.NoError(t, err)

	// Installation coverage validation exempts repos with their own
	// token_env/token_file. That exemption is only sound if the
	// override is terminal: were the app candidate still appended, an
	// unset override env var would fall through to the installation
	// token and reopen the cross-account 404 the validation prevents.
	desc := cfg.TokenSourceForPlatformHost("github", "github.com", "OTHER_ORG_PAT", "")
	for _, cand := range desc.Candidates {
		Assert.NotEqual(t, tokenauth.SourceKindGitHubApp, cand.Kind,
			"repo-level override chains must not fall through to the app token")
	}
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
installation_account = "kenn-io"
repository_selection = "all"
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
