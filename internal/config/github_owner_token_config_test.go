package config

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

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

func TestProviderTokenSourcesIncludeUntrackedGitHubOwnerRoutes(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	cfg, err := Load(writeConfig(t, `
[[github_owner_tokens]]
owner = "acme"
token_env = "ACME_PAT"
`))
	require.NoError(err)

	var ownerPlan *ProviderTokenSource
	for _, plan := range cfg.ProviderTokenSources() {
		if plan.Descriptor.Key.Scope == "owner:acme" {
			planCopy := plan
			ownerPlan = &planCopy
			break
		}
	}
	require.NotNil(ownerPlan)
	assert.True(ownerPlan.Required)
	assert.Equal("acme", ownerPlan.GitHubOwner)
	assert.Equal([]string{
		"env:ACME_PAT", "env:MIDDLEMAN_GITHUB_TOKEN", "github_cli:github.com",
	}, candidateSafeStrings(ownerPlan.Descriptor))
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

// A repository PAT override makes the route exact and serves that repository's
// writes, but it must not displace a covering App installation for reads: App
// tokens carry their own rate-limit budget, so they lead every read chain.
// Mutation resolution skips App candidates, so the override is still the
// credential that signs writes.
func TestResolveGitHubRepoTokenSourceRepoOverrideIsExactAndKeepsAppForReads(t *testing.T) {
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
		"github_app:42@github.com/acme",
		"env:REPO_PAT",
		"env:ACME_PAT",
		"env:DEFAULT_PAT",
		"github_cli:github.com",
	}, candidateSafeStrings(desc))
	assert.Equal(tokenauth.SourceKindGitHubApp, desc.Candidates[0].Kind,
		"reads must prefer the installation token's own budget")
	assert.Equal(tokenauth.SourceKindEnv, desc.Candidates[1].Kind,
		"the repository override is the first PAT, so it signs writes")
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

// Fleet asks this question on a timer to report whether the platform backend
// can act. It must see owner-scoped routes: a configuration whose only
// credential is an owner PAT is authenticated, and the same configuration with
// that PAT absent is not. The gh CLI candidate is excluded by contract, so both
// directions hold whether or not the developer is signed in to gh.
func TestConfiguredCredentialAvailableSeesOwnerTokenRoute(t *testing.T) {
	assert := assert.New(t)
	cfg, err := Load(writeConfig(t, `
github_token_env = "MIDDLEMAN_OWNER_ROUTE_TEST_ABSENT_DEFAULT"

[[github_owner_tokens]]
owner = "acme"
token_env = "MIDDLEMAN_OWNER_ROUTE_TEST_ACME_PAT"

[[repos]]
owner = "acme"
name = "widgets"
`))
	require.NoError(t, err)

	assert.False(cfg.ConfiguredCredentialAvailable(),
		"an owner route whose PAT env is unset supplies no credential")

	t.Setenv("MIDDLEMAN_OWNER_ROUTE_TEST_ACME_PAT", "acme-tok")

	assert.True(cfg.ConfiguredCredentialAvailable(),
		"the owner PAT is a usable platform credential")
}

// An owner PAT or App installation that no [[repos]] entry names is still a
// registered route: it serves owner discovery and repository import. Walking
// only tracked repositories would report such a configuration as
// unauthenticated, which is the state a maintainer is in before importing
// anything.
func TestConfiguredCredentialAvailableSeesRoutesWithoutTrackedRepos(t *testing.T) {
	assert := assert.New(t)
	t.Setenv("MIDDLEMAN_STANDALONE_OWNER_PAT", "standalone-tok")
	cfg, err := Load(writeConfig(t, `
github_token_env = "MIDDLEMAN_OWNER_ROUTE_TEST_ABSENT_DEFAULT"

[[github_owner_tokens]]
owner = "acme"
token_env = "MIDDLEMAN_STANDALONE_OWNER_PAT"
`))
	require.NoError(t, err)
	require.Empty(t, cfg.Repos)

	assert.True(cfg.ConfiguredCredentialAvailable(),
		"a standalone owner route is a usable platform credential")
}

// An App installation with no [[repos]] entry is the other standalone route:
// ProviderTokenSources registers it for the installation account, and it is the
// only credential that can enumerate a selected installation. Reporting that
// configuration as unauthenticated would be wrong before any import happens.
func TestConfiguredCredentialAvailableSeesStandaloneAppRoute(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	keyPath := filepath.Join(t.TempDir(), "app.pem")
	require.NoError(os.WriteFile(keyPath, []byte("private-key\n"), 0o600))
	cfg, err := Load(writeConfig(t, `
github_token_env = "MIDDLEMAN_OWNER_ROUTE_TEST_ABSENT_DEFAULT"

[[github_apps]]
app_id = 42
private_key_path = "`+keyPath+`"
installation_id = 99
installation_account = "acme"
repository_selection = "selected"
selected_repos = ["acme/widgets"]
`))
	require.NoError(err)
	require.Empty(cfg.Repos)

	assert.True(cfg.ConfiguredCredentialAvailable(),
		"a standalone App installation is a usable platform credential")

	require.NoError(os.WriteFile(keyPath, nil, 0o600))

	assert.False(cfg.ConfiguredCredentialAvailable(),
		"an empty private key file cannot mint installation tokens")
}

// A readable App private key is a usable credential: startup mints
// installation tokens from it on demand, so a host with only an App
// installation is authenticated.
func TestConfiguredCredentialAvailableAcceptsAppPrivateKey(t *testing.T) {
	assert := assert.New(t)
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "app.pem")
	require.NoError(t, os.WriteFile(keyPath, []byte("private-key\n"), 0o600))
	cfg, err := Load(writeConfig(t, `
github_token_env = "MIDDLEMAN_OWNER_ROUTE_TEST_ABSENT_DEFAULT"

[[github_apps]]
app_id = 42
private_key_path = "`+keyPath+`"
installation_id = 99
installation_account = "acme"
repository_selection = "all"

[[repos]]
owner = "acme"
name = "widgets"
`))
	require.NoError(t, err)

	assert.True(cfg.ConfiguredCredentialAvailable())

	require.NoError(t, os.WriteFile(keyPath, nil, 0o600))

	assert.False(cfg.ConfiguredCredentialAvailable(),
		"an empty private key file cannot mint installation tokens")
}

// The read/write split is expressed entirely by candidate order plus mutation
// auth, so it is worth pinning end to end on a repository that configures its
// own PAT while an App covers it: reads must spend the installation's budget,
// writes must stay on the repository PAT so GitHub attributes them to the user.
func TestOverriddenRepoReadsUseAppInstallationAndWritesUseRepoPAT(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	t.Setenv("REPO_PAT", "repo-pat")
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "app.pem")
	require.NoError(os.WriteFile(keyPath, []byte("private-key\n"), 0o600))
	cfg, err := Load(writeConfig(t, `
github_token_env = "MIDDLEMAN_APP_READ_TEST_ABSENT"

[[github_apps]]
app_id = 42
private_key_path = "`+keyPath+`"
installation_id = 99
installation_account = "acme"
repository_selection = "all"

[[repos]]
owner = "acme"
name = "widgets"
token_env = "REPO_PAT"
`))
	require.NoError(err)

	var mints int
	source := tokenauth.NewManagedSource(
		cfg.ResolveGitHubRepoTokenSource(cfg.Repos[0]),
		tokenauth.Options{GitHubApp: func(
			context.Context, tokenauth.Candidate,
		) (string, time.Time, error) {
			mints++
			return "ghs_installation", time.Now().Add(time.Hour), nil
		}},
	)
	ctx := tokenauth.WithGitHubOwner(context.Background(), "acme")

	read, err := source.Token(ctx)
	require.NoError(err)
	assert.Equal("ghs_installation", read,
		"reads prefer the installation token's own rate-limit budget")

	write, err := source.Token(tokenauth.WithMutationAuth(ctx))
	require.NoError(err)
	assert.Equal("repo-pat", write,
		"writes stay on the repository PAT so they are attributed to the user")
	assert.Equal(1, mints, "mutation auth must not mint an installation token")
}

// A name pattern gets an owner-scoped route because a selected-repository App
// cannot cover the literal pattern. Requiring that route would fail startup for
// an App-only configuration, even though the App's exact routes serve every
// repository the pattern expands to.
func TestProviderTokenSourcesMakeAppServedGlobRouteOptional(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	keyPath := filepath.Join(t.TempDir(), "app.pem")
	require.NoError(os.WriteFile(keyPath, []byte("private-key\n"), 0o600))
	appConfig := `
[[github_apps]]
app_id = 42
private_key_path = "` + keyPath + `"
installation_id = 99
installation_account = "acme"
repository_selection = "selected"
selected_repos = ["acme/widgets"]
`
	globRepo := `
[[repos]]
owner = "acme"
name = "*"
`
	requiredByScope := func(t *testing.T, body string) map[string]bool {
		t.Helper()
		cfg, err := Load(writeConfig(t, body))
		require.NoError(err)
		out := map[string]bool{}
		for _, plan := range cfg.ProviderTokenSources() {
			out[plan.Descriptor.Key.Scope] = plan.Required
		}
		return out
	}

	withApp := requiredByScope(t, appConfig+globRepo)
	assert.False(withApp["owner:acme"],
		"the App's exact routes already serve the pattern's repositories")
	assert.True(withApp["repo:acme/widgets"],
		"the selected App route itself stays required")

	withoutApp := requiredByScope(t, globRepo)
	assert.True(withoutApp["owner:acme"],
		"without a covering App the pattern still needs its own credential")

	// A blank repository name creates no exact App route, so it must not
	// count as coverage either — otherwise the pattern route becomes
	// optional and startup proceeds with nothing able to serve it.
	malformed := requiredByScope(t, `
[[github_apps]]
app_id = 42
private_key_path = "`+keyPath+`"
installation_id = 99
installation_account = "acme"
repository_selection = "selected"
selected_repos = ["acme/"]
`+globRepo)
	assert.True(malformed["owner:acme"],
		"a selected entry with no repository name is not App coverage")
	assert.NotContains(malformed, "repo:acme/",
		"a selected entry with no repository name creates no exact route")
}
