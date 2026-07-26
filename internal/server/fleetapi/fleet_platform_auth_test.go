package fleetapi

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"go.kenn.io/middleman/internal/config"
)

func TestFleetPlatformAuthMonitorCachesResolvedSignal(t *testing.T) {
	require := require.New(t)
	calls := 0
	m := &fleetPlatformAuthMonitor{
		resolve:  func() bool { calls++; return true },
		interval: time.Hour,
	}
	require.Nil(m.authenticated(), "auth is unknown until the first resolve")

	m.runOnce()
	got := m.authenticated()
	require.NotNil(got)
	require.True(*got)
	require.Equal(1, calls)
}

func TestFleetPlatformAuthMonitorStoresUnauthenticated(t *testing.T) {
	require := require.New(t)
	m := &fleetPlatformAuthMonitor{
		resolve:  func() bool { return false },
		interval: time.Hour,
	}
	m.runOnce()
	got := m.authenticated()
	require.NotNil(got, "a resolved false is a concrete signal, not unknown")
	require.False(*got)
}

func TestNilFleetPlatformAuthMonitorAuthenticatedIsNil(t *testing.T) {
	var m *fleetPlatformAuthMonitor
	require.Nil(t, m.authenticated())
}

func TestPlatformAuthResolverNilConfigIsUnauthenticated(t *testing.T) {
	require.False(t, platformAuthResolver(func() *config.Config { return nil })())
}

func TestPlatformAuthResolverEnvTokenIsAuthenticated(t *testing.T) {
	require := require.New(t)
	t.Setenv("MIDDLEMAN_PLATFORM_AUTH_TEST_TOKEN", "tok")
	cfg := &config.Config{GitHubTokenEnv: "MIDDLEMAN_PLATFORM_AUTH_TEST_TOKEN"}
	require.True(platformAuthResolver(func() *config.Config { return cfg })(),
		"a resolvable env token means the platform backend is authenticated")
}

// An owner PAT is the only credential in configurations that route every
// repository by owner. The resolver must consult the repository-aware chain to
// see it, and the snapshot must carry it across ApplyConfig, or Fleet reports
// the platform backend as unauthenticated while sync and mutations work.
//
// The host is self-hosted so the resolution stays deterministic: the host-level
// chain has no default env var for a non-github.com GitHub host and `gh auth
// token` cannot answer for it, so a developer signed in to github.com does not
// make this pass vacuously.
func TestPlatformAuthResolverOwnerTokenOnlyIsAuthenticated(t *testing.T) {
	require := require.New(t)
	t.Setenv("MIDDLEMAN_PLATFORM_AUTH_TEST_OWNER_TOKEN", "owner-tok")
	authConfig := config.Config{
		// Names an env var that is never set, so only the owner
		// route can supply a credential.
		GitHubTokenEnv:      "MIDDLEMAN_PLATFORM_AUTH_TEST_ABSENT",
		DefaultPlatformHost: "github.example.com",
		Repos: []config.Repo{{
			Platform: "github", PlatformHost: "github.example.com",
			Owner: "acme", Name: "widget",
		}},
	}
	handler := &Handler{}
	handler.ApplyConfig(ConfigSnapshot{
		PlatformAuthEnabled: true, PlatformAuthConfig: authConfig,
	})

	require.False(platformAuthResolver(handler.snapshotPlatformAuthConfig)(),
		"no credential is configured for the self-hosted GitHub host yet")

	authConfig.GitHubOwnerTokens = []config.GitHubOwnerTokenConfig{{
		Host:     "github.example.com",
		Owner:    "acme",
		TokenEnv: "MIDDLEMAN_PLATFORM_AUTH_TEST_OWNER_TOKEN",
	}}
	handler.ApplyConfig(ConfigSnapshot{
		PlatformAuthEnabled: true, PlatformAuthConfig: authConfig,
	})

	snapshot := handler.snapshotPlatformAuthConfig()
	require.NotNil(snapshot)
	require.Len(snapshot.GitHubOwnerTokens, 1,
		"the auth snapshot must preserve owner token routes")
	require.True(platformAuthResolver(handler.snapshotPlatformAuthConfig)(),
		"an owner PAT is a usable platform credential")
}

// Before any repository is imported, an owner PAT is the whole configuration.
// Resolving only tracked repositories reports that state as unauthenticated,
// which is exactly when a maintainer is looking at Fleet to confirm the daemon
// can reach the platform.
func TestPlatformAuthResolverStandaloneOwnerRouteIsAuthenticated(t *testing.T) {
	require := require.New(t)
	t.Setenv("MIDDLEMAN_PLATFORM_AUTH_TEST_STANDALONE_PAT", "owner-tok")
	handler := &Handler{}
	handler.ApplyConfig(ConfigSnapshot{
		PlatformAuthEnabled: true,
		PlatformAuthConfig: config.Config{
			GitHubTokenEnv:      "MIDDLEMAN_PLATFORM_AUTH_TEST_ABSENT",
			DefaultPlatformHost: "github.example.com",
			GitHubOwnerTokens: []config.GitHubOwnerTokenConfig{{
				Host:     "github.example.com",
				Owner:    "acme",
				TokenEnv: "MIDDLEMAN_PLATFORM_AUTH_TEST_STANDALONE_PAT",
			}},
		},
	})

	snapshot := handler.snapshotPlatformAuthConfig()
	require.NotNil(snapshot)
	require.Empty(snapshot.Repos, "no repository is tracked yet")
	require.True(platformAuthResolver(handler.snapshotPlatformAuthConfig)(),
		"a configured owner route means the platform backend can act")
}
