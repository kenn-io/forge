package config

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/tokenauth"
)

const serviceConfig = `
[service]
enabled = true
github_user_id = 123
github_client_id = "pilot-client"
github_client_secret_file = "pilot-secret"
base_url = "https://forge.example.com"
`

func TestServiceCredentialsAreExclusive(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	cfg, err := Load(writeConfig(t, serviceConfig))
	require.NoError(err)
	assert.True(cfg.API.RequireAuth)
	assert.Contains(cfg.AllowedHosts, "forge.example.com")

	t.Setenv("KENN_FORGE_GITHUB_TOKEN", "personal-token")
	assert.Empty(cfg.ResolveRepoToken(Repo{Owner: "acme", Name: "widgets"}))
	for _, desc := range []tokenauth.Descriptor{
		cfg.ResolveRepoTokenSource(Repo{Owner: "acme", Name: "widgets"}),
		cfg.TokenSourceForPlatformHost("github", "github.com", "KENN_FORGE_GITHUB_TOKEN", ""),
		cfg.CloneTokenDescriptors()[0],
	} {
		source := tokenauth.NewManagedSource(desc, tokenauth.Options{
			GitHubCLI: func(context.Context, string) (string, error) {
				return "personal-cli-token", nil
			},
		})
		_, err := source.Token(t.Context())
		require.ErrorIs(err, tokenauth.ErrMissingToken)
	}
}

func TestServicePolicySurvivesSettingsSave(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	cfg, err := Load(writeConfig(t, serviceConfig))
	require.NoError(err)
	path := filepath.Join(t.TempDir(), "saved.toml")
	require.NoError(cfg.Save(path))
	saved, err := Load(path)
	require.NoError(err)
	assert.True(saved.API.RequireAuth)
	desc := saved.TokenSourceForPlatformHost("github", "github.com", "", "")
	require.Len(desc.Candidates, 1)
	assert.Equal(tokenauth.SourceKind("github_app_user"), desc.Candidates[0].Kind)
}

func TestServiceRejectsConflictingConfiguration(t *testing.T) {
	for _, input := range []string{
		strings.Replace(serviceConfig, "github_user_id = 123", "github_user_id = 0", 1),
		strings.Replace(serviceConfig, "https://forge.example.com", "http://forge.example.com", 1),
		strings.Replace(serviceConfig, "https://forge.example.com", "https://forge.example.com/other", 1),
		serviceConfig + "\n[api.tailscale_serve]\nenabled = true\nallowed_users = [\"user@example.com\"]\n",
		serviceConfig + "\n[fleet]\nenabled = true\n",
		serviceConfig + "\n[[platforms]]\ntype = \"gitlab\"\nhost = \"gitlab.com\"\n",
		serviceConfig + "\n[[platforms]]\ntype = \"github\"\nhost = \"github.com\"\nbase_url = \"https://other.example.com\"\n",
		serviceConfig + "\n[[repos]]\nowner = \"acme\"\nname = \"widgets\"\ntoken_env = \"PERSONAL_TOKEN\"\n",
	} {
		_, err := Load(writeConfig(t, input))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "service")
	}
}

func TestDefaultModeKeepsPersonalCredentials(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	cfg, err := Load(writeConfig(t, ""))
	require.NoError(err)
	assert.False(cfg.API.RequireAuth)
	t.Setenv("KENN_FORGE_GITHUB_TOKEN", "personal-token")
	source := tokenauth.NewManagedSource(
		cfg.TokenSourceForPlatformHost("github", "github.com", "", ""), tokenauth.Options{},
	)
	token, err := source.Token(t.Context())
	require.NoError(err)
	assert.Equal("personal-token", token)
}
