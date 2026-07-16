package config

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/tokenauth"
)

func TestGitHubOwnerTokensValidation(t *testing.T) {
	tests := []struct {
		name    string
		config  string
		wantErr string
	}{
		{
			name: "duplicate owner is case insensitive",
			config: `
[[github_owner_tokens]]
owner = "Acme"
token_env = "ACME_ONE"

[[github_owner_tokens]]
owner = "acme"
token_env = "ACME_TWO"
`,
			wantErr: `duplicate github owner token for host "github.com" and owner "acme"`,
		},
		{
			name: "credential is required",
			config: `
[[github_owner_tokens]]
owner = "acme"
`,
			wantErr: "token_file or token_env is required",
		},
		{
			name: "owner is required",
			config: `
[[github_owner_tokens]]
token_env = "ACME_TOKEN"
`,
			wantErr: "owner is required",
		},
		{
			name: "owner is exact",
			config: `
[[github_owner_tokens]]
owner = "acme/tools"
token_env = "ACME_TOKEN"
`,
			wantErr: "owner must be one exact GitHub owner",
		},
		{
			name: "owner rejects glob",
			config: `
[[github_owner_tokens]]
owner = "acme*"
token_env = "ACME_TOKEN"
`,
			wantErr: "owner must be one exact GitHub owner",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, tc.config))
			require.Error(t, err)
			assert.ErrorContains(t, err, tc.wantErr)
		})
	}
}

func TestGitHubOwnerTokensNormalizeAndRoundTrip(t *testing.T) {
	cfg, saved := roundTripConfigString(t, `
[[github_owner_tokens]]
owner = "Acme"
token_file = "tokens/acme"
token_env = "ACME_TOKEN"
`)

	require.Len(t, cfg.GitHubOwnerTokens, 1)
	require.Len(t, saved.GitHubOwnerTokens, 1)
	assert := assert.New(t)
	assert.Equal("github.com", cfg.GitHubOwnerTokens[0].Host)
	assert.True(filepath.IsAbs(cfg.GitHubOwnerTokens[0].TokenFile))
	assert.Equal(cfg.GitHubOwnerTokens, saved.GitHubOwnerTokens)
	assert.Contains(cfg.TokenEnvNames(), "ACME_TOKEN")

	got, ok := cfg.GitHubOwnerTokenFor("github.com", "acme")
	require.True(t, ok)
	assert.Equal("Acme", got.Owner)
}

func TestResolveGitHubRepoTokenSourcePrecedence(t *testing.T) {
	cfg, err := Load(writeConfig(t, `
github_token_env = "DEFAULT_PAT"

[[platforms]]
type = "github"
host = "github.com"
token_file = "tokens/platform"
token_env = "PLATFORM_PAT"

[[github_owner_tokens]]
owner = "acme"
token_file = "tokens/acme"
token_env = "ACME_PAT"

[[github_apps]]
app_id = 42
private_key_path = "app.pem"
installation_id = 99
installation_account = "acme"
repository_selection = "all"

[[repos]]
owner = "acme"
name = "widgets"
`))
	require.NoError(t, err)

	desc := cfg.ResolveGitHubRepoTokenSource(cfg.Repos[0])
	assert.Equal(t, tokenauth.Key{
		Platform: "github",
		Host:     "github.com",
		Scope:    "owner:acme",
	}, desc.Key)
	assert.Equal(t, []string{
		"github_app:42@github.com/acme",
		"file:" + cfg.GitHubOwnerTokens[0].TokenFile,
		"env:ACME_PAT",
		"file:" + cfg.Platforms[0].TokenFile,
		"env:PLATFORM_PAT",
		"env:DEFAULT_PAT",
		"github_cli:github.com",
	}, candidateSafeStrings(desc))
}

func TestResolveGitHubRepoTokenSourceRepoOverrideIsExactAndTerminalForApp(t *testing.T) {
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
repository_selection = "all"

[[repos]]
owner = "acme"
name = "widgets"
token_env = "REPO_PAT"
`))
	require.NoError(t, err)

	desc := cfg.ResolveGitHubRepoTokenSource(cfg.Repos[0])
	assert := assert.New(t)
	assert.Equal("repo:acme/widgets", desc.Key.Scope)
	assert.Equal([]string{
		"env:REPO_PAT",
		"env:ACME_PAT",
		"env:DEFAULT_PAT",
		"github_cli:github.com",
	}, candidateSafeStrings(desc))
	for _, candidate := range desc.Candidates {
		assert.NotEqual(tokenauth.SourceKindGitHubApp, candidate.Kind)
	}
}

func TestGitHubOwnersMayUseDifferentPATsOnOneHost(t *testing.T) {
	cfg, err := Load(writeConfig(t, `
[[github_owner_tokens]]
owner = "acme"
token_env = "ACME_PAT"

[[github_owner_tokens]]
owner = "example"
token_env = "EXAMPLE_PAT"

[[repos]]
owner = "acme"
name = "widgets"

[[repos]]
owner = "example"
name = "tools"
`))
	require.NoError(t, err)

	assert.NotEqual(t,
		cfg.ResolveGitHubRepoTokenSource(cfg.Repos[0]).CanonicalSourceString(),
		cfg.ResolveGitHubRepoTokenSource(cfg.Repos[1]).CanonicalSourceString(),
	)
}

func candidateSafeStrings(desc tokenauth.Descriptor) []string {
	out := make([]string, 0, len(desc.Candidates))
	for _, candidate := range desc.Candidates {
		out = append(out, candidate.SafeString())
	}
	return out
}
