package server

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"go.kenn.io/forge/internal/config"
)

// The Fleet auth snapshot copies only the fields the resolver reads. Owner PATs
// and App installations are credential routes in their own right, so omitting
// them makes an owner-token-only configuration look unauthenticated to Fleet
// even though sync and mutations are working.
func TestFleetConfigSnapshotCarriesGitHubCredentialRoutes(t *testing.T) {
	assert := assert.New(t)
	cfg := &config.Config{
		GitHubTokenEnv:      "KENN_FORGE_GITHUB_TOKEN",
		DefaultPlatformHost: "github.com",
		Repos: []config.Repo{{
			Platform: "github", PlatformHost: "github.com",
			Owner: "acme", Name: "widget",
		}},
		GitHubOwnerTokens: []config.GitHubOwnerTokenConfig{{
			Host: "github.com", Owner: "acme", TokenEnv: "ACME_PAT",
		}},
		GitHubApps: []config.GitHubAppConfig{{
			Host: "github.com", AppID: 1, InstallationID: 2,
			InstallationAccount: "acme", PrivateKeyPath: "app.pem",
			RepositorySelection: "all",
		}},
	}

	snapshot := fleetConfigSnapshot(cfg, nil)

	assert.Equal(cfg.GitHubOwnerTokens, snapshot.PlatformAuthConfig.GitHubOwnerTokens)
	assert.Equal(cfg.GitHubApps, snapshot.PlatformAuthConfig.GitHubApps)
}
