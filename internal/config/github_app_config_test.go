package config

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/tokenauth"
)

const githubAppConfigTOML = `
[[repos]]
owner = "kenn-io"
name = "kenn-forge"

[[github_apps]]
host = "github.com"
app_id = 4321
slug = "kenn-forge-abc"
owner = "mariusvniekerk"
owner_type = "User"
private_key_path = "github-app-kenn-forge-abc.pem"
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
	assert := assert.New(t)
	assert.Equal("github.com", app.Host)
	assert.Equal(int64(4321), app.AppID)
	assert.Equal("kenn-forge-abc", app.Slug)
	assert.Equal(int64(99), app.InstallationID)
	assert.Equal("kenn-io", app.InstallationAccount)
	assert.Equal(GitHubAppRoleSync, app.Role)
	// Relative key paths resolve against the config directory, like
	// token_file does, so the CLI can write portable entries.
	assert.Equal(
		filepath.Join(filepath.Dir(path), "github-app-kenn-forge-abc.pem"),
		app.PrivateKeyPath,
	)
}

func TestGitHubAppsSaveLoadRoundTrip(t *testing.T) {
	cfg, cfg2 := roundTripConfigString(t, githubAppConfigTOML)
	require.Len(t, cfg2.GitHubApps, 1)
	assert.Equal(t, cfg.GitHubApps[0], cfg2.GitHubApps[0])
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
		{
			name: "same owner may have one app per role",
			toml: `
[[github_apps]]
app_id = 1
owner = "app-owner"
role = "sync"
private_key_path = "sync.pem"

[[github_apps]]
app_id = 2
owner = "app-owner"
role = "archive"
private_key_path = "archive.pem"
`,
		},
		{
			name: "same installation cannot have both roles",
			toml: `
[[github_apps]]
app_id = 1
owner = "sync-owner"
role = "sync"
private_key_path = "sync.pem"
installation_id = 10
installation_account = "acme"
repository_selection = "all"

[[github_apps]]
app_id = 1
owner = "archive-owner"
role = "archive"
private_key_path = "archive.pem"
installation_id = 10
installation_account = "ACME"
repository_selection = "all"
`,
			wantErr: `cannot be configured for both "sync" and "archive" roles`,
		},
		{
			name: "invalid role",
			toml: `
[[github_apps]]
app_id = 1
role = "background"
private_key_path = "key.pem"
`,
			wantErr: `role must be "sync" or "archive"`,
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
			assert.ErrorContains(t, err, tt.wantErr)
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
name = "kenn-forge"

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
name = "kenn-forge"

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
name = "kenn-forge"

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
name = "kenn-forge"

[[github_apps]]
app_id = 1
private_key_path = "a.pem"
installation_id = 9
installation_account = "kenn-io"
repository_selection = "selected"
selected_repos = ["kenn-io/kenn-forge"]
`,
		},
		{
			name: "same-owner repo outside the selected set uses the PAT route",
			toml: `
[[repos]]
owner = "kenn-io"
name = "kenn-forge"

[[repos]]
owner = "kenn-io"
name = "added-later"

[[github_apps]]
app_id = 1
private_key_path = "a.pem"
installation_id = 9
installation_account = "kenn-io"
repository_selection = "selected"
selected_repos = ["kenn-io/kenn-forge"]
`,
		},
		{
			name: "glob repo with a selected install uses the PAT route",
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
			name: "whitespace and case in repository_selection still selects the PAT route",
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
selected_repos = ["kenn-io/kenn-forge"]
`,
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
			assert.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestLoadForGitHubAppRepairKeepsStructuralValidation(t *testing.T) {
	require := require.New(t)
	_, err := LoadForGitHubAppRepair(writeConfig(t, `
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

func TestGitHubAppMixedOverrideAndCoveredReposUseSeparateRoutes(t *testing.T) {
	cfg, err := Load(writeConfig(t, `
[[repos]]
owner = "kenn-io"
name = "kenn-forge"

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
	require.NoError(t, err)

	covered := cfg.ResolveGitHubRepoTokenSource(cfg.Repos[0])
	overridden := cfg.ResolveGitHubRepoTokenSource(cfg.Repos[1])
	assert := assert.New(t)
	assert.True(covered.HasActiveGitHubAppForOwner("kenn-io"))
	assert.False(overridden.HasActiveGitHubApp())
	assert.Equal("repo:other-org/tool", overridden.Key.Scope)
}

func TestSelectedGitHubAppBuildsExactCoveredRouteAndOwnerPATFallback(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	cfg, err := Load(writeConfig(t, `
github_token_env = "DEFAULT_PAT"

[[github_owner_tokens]]
owner = "acme"
token_env = "ACME_PAT"

[[github_apps]]
app_id = 42
private_key_path = "app.pem"
installation_id = 99
installation_account = "acme"
repository_selection = "selected"
selected_repos = ["acme/covered"]

[[repos]]
owner = "acme"
name = "covered"

[[repos]]
owner = "acme"
name = "uncovered"
`))
	require.NoError(err)

	covered := cfg.ResolveGitHubRepoTokenSource(cfg.Repos[0])
	uncovered := cfg.ResolveGitHubRepoTokenSource(cfg.Repos[1])
	assert.Equal("repo:acme/covered", covered.Key.Scope)
	assert.True(covered.HasActiveGitHubAppForOwner("acme"))
	assert.Equal("owner:acme", uncovered.Key.Scope)
	assert.False(uncovered.HasActiveGitHubApp())
	assert.Equal([]string{
		"env:ACME_PAT", "env:DEFAULT_PAT", "github_cli:github.com",
	}, candidateSafeStrings(uncovered))

	plans := cfg.ProviderTokenSources()
	var coveredPlan, ownerPlan *ProviderTokenSource
	for i := range plans {
		switch plans[i].Descriptor.Key.Scope {
		case "repo:acme/covered":
			coveredPlan = &plans[i]
		case "owner:acme":
			ownerPlan = &plans[i]
		}
	}
	require.NotNil(coveredPlan)
	require.NotNil(ownerPlan)
	assert.True(coveredPlan.Descriptor.HasActiveGitHubAppForOwner("acme"))
	assert.False(ownerPlan.Descriptor.HasActiveGitHubApp())
}

func TestGitHubAppHostDefaultsToPublicHost(t *testing.T) {
	cfg, err := Load(writeConfig(t, `
[[github_apps]]
app_id = 7
private_key_path = "key.pem"
`))
	require.NoError(t, err)
	require.Len(t, cfg.GitHubApps, 1)
	assert.Equal(t, "github.com", cfg.GitHubApps[0].Host)
}

func TestRepoTokenSourceChainPrefersGitHubAppOverPATs(t *testing.T) {
	cfg, err := Load(writeConfig(t, `
github_token_env = "MY_PAT"

[[repos]]
owner = "kenn-io"
name = "kenn-forge"

[[github_apps]]
host = "github.com"
app_id = 4321
private_key_path = "app.pem"
installation_id = 99
installation_account = "kenn-io"
repository_selection = "all"
`))
	require.NoError(t, err)

	desc := cfg.ResolveGitHubRepoTokenSource(cfg.Repos[0])
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

	assert := assert.New(t)
	app := desc.Candidates[0]
	assert.Equal(int64(4321), app.AppID)
	assert.Equal(int64(99), app.InstallationID)
	assert.Equal("github.com", app.Host)
	assert.Equal("kenn-io", app.InstallationAccount)
	assert.True(filepath.IsAbs(app.FilePath), "key path %q", app.FilePath)
	assert.Equal("MY_PAT", desc.Candidates[1].EnvName)
}

func TestResolveGitHubArchiveTokenSourceIsolatedFromSyncApp(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	cfg, err := Load(writeConfig(t, `
[[repos]]
owner = "kenn-io"
name = "kenn-forge"

[[github_apps]]
app_id = 1
private_key_path = "sync.pem"
installation_id = 10
installation_account = "kenn-io"
repository_selection = "all"

[[github_apps]]
app_id = 2
role = "archive"
private_key_path = "archive.pem"
installation_id = 20
installation_account = "kenn-io"
repository_selection = "all"
`))
	require.NoError(err)
	repo := cfg.Repos[0]
	syncDesc := cfg.ResolveGitHubRepoTokenSource(repo)
	archiveDesc := cfg.ResolveGitHubArchiveTokenSource(repo)
	assert.True(syncDesc.HasActiveGitHubApp())
	assert.Equal("archive:owner:kenn-io", archiveDesc.Key.Scope)
	assert.NotEqual(archiveDesc.Key, syncDesc.Key)
	require.Len(archiveDesc.Candidates, 1)
	assert.Equal(int64(2), archiveDesc.Candidates[0].AppID)
	assert.Equal(int64(20), archiveDesc.Candidates[0].InstallationID)
}

func TestProviderTokenSourcesKeepSelectedArchiveReposExact(t *testing.T) {
	cfg, err := Load(writeConfig(t, `
[[repos]]
owner = "kenn-io"
name = "one"

[[repos]]
owner = "kenn-io"
name = "two"

[[github_apps]]
app_id = 2
role = "archive"
private_key_path = "archive.pem"
installation_id = 20
installation_account = "kenn-io"
repository_selection = "selected"
selected_repos = ["kenn-io/one", "kenn-io/two"]
`))
	require.NoError(t, err)
	var keys []string
	for _, plan := range cfg.ProviderTokenSources() {
		if plan.ArchiveDescriptor.Key.Host != "" {
			keys = append(keys, plan.Descriptor.Key.Scope)
		}
	}
	assert.ElementsMatch(t, []string{"repo:kenn-io/one", "repo:kenn-io/two"}, keys)
}

func TestProviderTokenSourcesExpandSelectedArchiveReposForGlob(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	cfg, err := Load(writeConfig(t, `
[[repos]]
owner = "acme"
name = "widget-*"

[[github_apps]]
app_id = 2
role = "archive"
private_key_path = "archive.pem"
installation_id = 20
installation_account = "acme"
repository_selection = "selected"
selected_repos = ["acme/widget-a"]
`))
	require.NoError(err)

	var selected *ProviderTokenSource
	plans := cfg.ProviderTokenSources()
	for i := range plans {
		if plans[i].Descriptor.Key.Scope == "repo:acme/widget-a" {
			selected = &plans[i]
			break
		}
	}
	require.NotNil(selected)
	assert.False(selected.Descriptor.HasActiveGitHubApp())
	assert.Equal("archive:repo:acme/widget-a", selected.ArchiveDescriptor.Key.Scope)
	require.Len(selected.ArchiveDescriptor.Candidates, 1)
	assert.Equal(int64(2), selected.ArchiveDescriptor.Candidates[0].AppID)
}

func TestFallbackTokenSourceExcludesAccountScopedGitHubApps(t *testing.T) {
	cfg, err := Load(writeConfig(t, `
github_token_env = "MY_PAT"

[[repos]]
owner = "kenn-io"
name = "kenn-forge"

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
	kinds := make([]tokenauth.SourceKind, 0, len(desc.Candidates))
	for _, candidate := range desc.Candidates {
		kinds = append(kinds, candidate.Kind)
	}
	assert.Equal(t, []tokenauth.SourceKind{
		tokenauth.SourceKindEnv,
		tokenauth.SourceKindGitHubCLI,
	}, kinds)
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

	// A repo token_env/token_file override is terminal: an unset override
	// must not fall through to the installation token and silently change
	// which credential route owns the repository.
	desc := cfg.TokenSourceForPlatformHost("github", "github.com", "OTHER_ORG_PAT", "")
	for _, cand := range desc.Candidates {
		assert.NotEqual(t, tokenauth.SourceKindGitHubApp, cand.Kind,
			"repo-level override chains must not fall through to the app token")
	}
}

func TestTokenSourceChainSkipsAppForOtherHosts(t *testing.T) {
	cfg, err := Load(writeConfig(t, `
[[repos]]
owner = "kenn-io"
name = "kenn-forge"

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
			assert.NotEqual(
				t, tokenauth.SourceKindGitHubApp, cand.Kind,
				"%s/%s must not inherit the github.com app", tc.platform, tc.host,
			)
		}
	}
}
