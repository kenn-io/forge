package settingsservertest

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/server/configreload"
)

func writeConfigToml(t *testing.T, path string, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

const validReloadConfig = `
sync_interval = "5m"
github_token_env = "KENN_FORGE_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "widget"
`

func TestValidateReloadCloneTokenSourcesUsesRepoDescriptorForProviderHost(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	writeConfigToml(t, cfgPath, `
github_token_env = "KENN_FORGE_GITHUB_TOKEN"

[[platforms]]
type = "github"
host = "github.com"
token_env = "PLATFORM_TOKEN"

[[repos]]
owner = "acme"
name = "widget"
platform = "github"
platform_host = "github.com"
token_env = "REPO_TOKEN"
`)
	cfg, err := config.Load(cfgPath)
	require.NoError(t, err)

	require.NoError(t, configreload.ValidateReloadCloneTokenSources(cfg))
}

func TestValidateReloadCloneTokenSourcesAllowsDifferentProviderFallbacksOnSharedHost(t *testing.T) {
	// Credentials are provider-scoped, so providers sharing one hostname may
	// carry different fallback tokens; the ownerless host fallback is
	// disabled in that case rather than the reload being rejected.
	cfg := &config.Config{Platforms: []config.PlatformConfig{
		{Type: "github", Host: "code.example.com", TokenEnv: "GITHUB_PAT"},
		{Type: "forgejo", Host: "code.example.com", TokenEnv: "FORGEJO_PAT"},
	}}

	require.NoError(t, configreload.ValidateReloadCloneTokenSources(cfg))
}

func TestValidateReloadCloneTokenSourcesRejectsConflictingRepoOverrides(t *testing.T) {
	cfg := &config.Config{Repos: []config.Repo{
		{Platform: "gitlab", PlatformHost: "gitlab.com", Owner: "group", Name: "one", TokenEnv: "TOKEN_A"},
		{Platform: "gitlab", PlatformHost: "gitlab.com", Owner: "group", Name: "two", TokenEnv: "TOKEN_B"},
	}}

	err := configreload.ValidateReloadCloneTokenSources(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "conflicting token source")
}

func TestValidateReloadCloneTokenSourcesAllowsEquivalentChainsOnSameHost(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	// Two providers share a self-hosted host. The forgejo repo's token_env
	// repeats its platform fallback, producing the chain env:SHARED ->
	// env:SHARED, while gitlab resolves to a plain env:SHARED. They name the
	// same token, so the per-host clone-token check must compare canonical
	// chains and accept the reload rather than flag a conflict.
	writeConfigToml(t, cfgPath, `
[[platforms]]
type = "forgejo"
host = "code.example.com"
token_env = "SHARED"

[[platforms]]
type = "gitlab"
host = "code.example.com"
token_env = "SHARED"

[[repos]]
owner = "acme"
name = "widget"
platform = "forgejo"
platform_host = "code.example.com"
token_env = "SHARED"
`)
	cfg, err := config.Load(cfgPath)
	require.NoError(t, err)

	require.NoError(t, configreload.ValidateReloadCloneTokenSources(cfg))
}

func TestValidateReloadCloneTokenSourcesIgnoresCredentiallessPlatformHosts(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	// The forgejo entry has no token config and a non-default host, so its
	// candidate chain is empty. It imposes no clone credential and must not
	// conflict with the tokened gitlab entry on the same host.
	writeConfigToml(t, cfgPath, `
[[platforms]]
type = "forgejo"
host = "code.example.com"

[[platforms]]
type = "gitlab"
host = "code.example.com"
token_env = "SHARED"
`)
	cfg, err := config.Load(cfgPath)
	require.NoError(t, err)

	require.NoError(t, configreload.ValidateReloadCloneTokenSources(cfg))
}

func TestSanitizeConfigErrorRedactsTokenMaterial(t *testing.T) {
	assert := assert.New(t)

	got := configreload.SanitizeConfigError(
		errors.New("open /home/me/.kenn/forge/config.toml: https://x-access-token:ghp_config_secret@github.com/acme/widgets.git failed"),
		"/home/me/.kenn/forge/config.toml",
	)

	assert.Contains(got, "config.toml")
	assert.Contains(got, "[REDACTED]")
	assert.NotContains(got, "ghp_config_secret")
	assert.NotContains(got, "x-access-token")
}

// TestRestartRequiredForAuthFleetRoleAndSessions pins startup-bound settings
// while member and timeout edits remain live.
func TestRestartRequiredForAuthFleetRoleAndSessions(t *testing.T) {
	require := require.New(t)
	base := func() *config.Config {
		cfg := &config.Config{}
		cfg.API.RequireAuth = true
		cfg.Fleet.BaseURL = "https://hub.example"
		cfg.Fleet.Sessions.IncludeUnmanagedDetails = false
		cfg.Fleet.Members = []config.FleetMember{
			{NodeID: "fedcba9876543210fedcba9876543210", BaseURL: "https://spoke.example", State: "active"},
		}
		return cfg
	}
	snap := configreload.SnapshotStartupConfig(base())

	require.False(snap.RestartRequiredFor(base()),
		"identical config must not demand a restart")

	enabledFlipped := base()
	enabledFlipped.Fleet.Enabled = true
	require.False(snap.RestartRequiredFor(enabledFlipped),
		"fleet.enabled changes apply without restart")

	timeoutChanged := base()
	timeoutChanged.Fleet.PeerTimeout = "4s"
	require.False(snap.RestartRequiredFor(timeoutChanged),
		"fleet.peer_timeout changes apply without restart")

	memberAdded := base()
	memberAdded.Fleet.Members = append(memberAdded.Fleet.Members, config.FleetMember{
		NodeID: "0123456789abcdef0123456789abcdef", BaseURL: "https://mini.example", State: "active",
	})
	require.False(snap.RestartRequiredFor(memberAdded),
		"federation member changes apply without restart")

	authFlipped := base()
	authFlipped.API.RequireAuth = false
	require.True(snap.RestartRequiredFor(authFlipped))

	fleetSessionsFlipped := base()
	fleetSessionsFlipped.Fleet.Sessions.IncludeUnmanagedDetails = true
	require.True(snap.RestartRequiredFor(fleetSessionsFlipped))

	originChanged := base()
	originChanged.Fleet.BaseURL = "https://new-hub.example"
	require.True(snap.RestartRequiredFor(originChanged))

	tailscaleServeChanged := base()
	tailscaleServeChanged.API.TailscaleServe = config.TailscaleServeAPI{
		Enabled: true, AllowedUsers: []string{"user@example.com"},
	}
	require.True(snap.RestartRequiredFor(tailscaleServeChanged))
}

func TestRestartRequiredForFleetRoleAndHubBinding(t *testing.T) {
	require := require.New(t)
	base := &config.Config{
		Fleet: config.Fleet{
			Role: config.FleetRoleHub,
			Members: []config.FleetMember{{
				NodeID: "fedcba9876543210fedcba9876543210", Name: "Spoke A", BaseURL: "https://spoke.test", State: "active",
			}},
		},
	}
	snap := configreload.SnapshotStartupConfig(base)

	roleChanged := *base
	roleChanged.Fleet = base.Fleet
	roleChanged.Fleet.Role = config.FleetRoleSpoke
	roleChanged.Fleet.Hub = &config.FleetHub{
		NodeID:  "0123456789abcdef0123456789abcdef",
		BaseURL: "https://hub.test",
	}
	require.True(snap.RestartRequiredFor(&roleChanged))

	bound := roleChanged
	boundSnap := configreload.SnapshotStartupConfig(&bound)
	bindingChanged := bound
	bindingChanged.Fleet = bound.Fleet
	bindingChanged.Fleet.Hub = &config.FleetHub{
		NodeID:  bound.Fleet.Hub.NodeID,
		BaseURL: "https://new-hub.test",
	}
	require.True(boundSnap.RestartRequiredFor(&bindingChanged))

	hubNameChanged := bound
	hubNameChanged.Fleet = bound.Fleet
	hubNameChanged.Fleet.Hub = &config.FleetHub{
		NodeID:  bound.Fleet.Hub.NodeID,
		Name:    "Renamed hub",
		BaseURL: bound.Fleet.Hub.BaseURL,
	}
	require.False(boundSnap.RestartRequiredFor(&hubNameChanged))

	displayChanged := *base
	displayChanged.Fleet = base.Fleet
	displayChanged.Fleet.Members = slices.Clone(base.Fleet.Members)
	displayChanged.Fleet.Members[0].Name = "Renamed spoke"
	require.False(snap.RestartRequiredFor(&displayChanged))
}

func TestRestartRequiredForMCPConfig(t *testing.T) {
	assert := assert.New(t)
	base := &config.Config{MCP: config.MCP{Enabled: true, Port: 8092, DiffCacheMB: 128}}
	snap := configreload.SnapshotStartupConfig(base)

	assert.False(snap.RestartRequiredFor(&config.Config{
		MCP: config.MCP{Enabled: true, Port: 8092, DiffCacheMB: 128},
	}))
	assert.True(snap.RestartRequiredFor(&config.Config{
		MCP: config.MCP{Enabled: false, Port: 8092, DiffCacheMB: 128},
	}))
	assert.True(snap.RestartRequiredFor(&config.Config{
		MCP: config.MCP{Enabled: true, Port: 9192, DiffCacheMB: 128},
	}))
	assert.True(snap.RestartRequiredFor(&config.Config{
		MCP: config.MCP{Enabled: true, Port: 8092, DiffCacheMB: 256},
	}))
}

func TestRestartRequiredForGitHubArchiveRoutes(t *testing.T) {
	assert := assert.New(t)
	base := &config.Config{
		Repos: []config.Repo{{Owner: "acme", Name: "widget"}},
		GitHubApps: []config.GitHubAppConfig{{
			Host: "github.com", Role: "archive", AppID: 1,
			PrivateKeyPath: "archive.pem", InstallationID: 2,
			InstallationAccount: "acme", RepositorySelection: "all",
		}},
	}
	snap := configreload.SnapshotStartupConfig(base)
	assert.False(snap.RestartRequiredFor(base))

	changed := *base
	changed.GitHubApps = slices.Clone(base.GitHubApps)
	changed.GitHubApps[0].InstallationID = 3
	assert.True(snap.RestartRequiredFor(&changed))
}

func TestRestartRequiredForPlatformTransportChange(t *testing.T) {
	require := require.New(t)
	base := func() *config.Config {
		return &config.Config{Platforms: []config.PlatformConfig{
			{
				Type:          "gitea",
				Host:          "gitea.example.test:3000",
				BaseURL:       "http://gitea.example.test:3000",
				AllowInsecure: true,
			},
		}}
	}
	snapshot := configreload.SnapshotStartupConfig(base())

	require.False(snapshot.RestartRequiredFor(base()))

	baseURLChanged := base()
	baseURLChanged.Platforms[0].BaseURL = "https://gitea.example.test:3000"
	require.True(snapshot.RestartRequiredFor(baseURLChanged))

	allowInsecureChanged := base()
	allowInsecureChanged.Platforms[0].AllowInsecure = false
	require.True(snapshot.RestartRequiredFor(allowInsecureChanged))
}

func TestRestartRequiredForRoborevEndpointButNotManagedCloneInit(t *testing.T) {
	assert := assert.New(t)
	base := &config.Config{Roborev: config.Roborev{
		Endpoint: "http://127.0.0.1:7373",
	}}
	snap := configreload.SnapshotStartupConfig(base)

	toggleChanged := *base
	toggleChanged.Roborev.InitManagedClones = true
	assert.False(snap.RestartRequiredFor(&toggleChanged))

	endpointChanged := *base
	endpointChanged.Roborev.Endpoint = "http://localhost:7474"
	assert.True(snap.RestartRequiredFor(&endpointChanged))
}

func TestRestartRequiredForNotificationIntervals(t *testing.T) {
	require := require.New(t)
	base := func() *config.Config {
		cfg := &config.Config{}
		cfg.SyncInterval = "5m"
		cfg.ActivePRRefreshInterval = "2m"
		cfg.ActivePRWindow = "4h"
		cfg.Notifications.SyncInterval = "30s"
		cfg.Notifications.PropagationInterval = "1m"
		cfg.Notifications.BatchSize = 25
		return cfg
	}
	snap := configreload.SnapshotStartupConfig(base())

	require.False(snap.RestartRequiredFor(base()),
		"identical notification loop config must not demand a restart")

	syncIntervalChanged := base()
	syncIntervalChanged.Notifications.SyncInterval = "2m"
	require.True(snap.RestartRequiredFor(syncIntervalChanged),
		"notification sync_interval is bound to the startup ticker")

	propagationIntervalChanged := base()
	propagationIntervalChanged.Notifications.PropagationInterval = "5m"
	require.True(snap.RestartRequiredFor(propagationIntervalChanged),
		"notification propagation_interval is bound to the startup ticker")

	batchSizeChanged := base()
	batchSizeChanged.Notifications.BatchSize = 50
	require.True(snap.RestartRequiredFor(batchSizeChanged),
		"notification batch_size is snapped by the loop")

	activeRefreshChanged := base()
	activeRefreshChanged.ActivePRRefreshInterval = "30s"
	require.False(snap.RestartRequiredFor(activeRefreshChanged),
		"active PR refresh interval is hot-reloadable by the syncer")

	activeHotWindowChanged := base()
	activeHotWindowChanged.ActivePRHotWindow = "30m"
	require.False(snap.RestartRequiredFor(activeHotWindowChanged))

	activeWarmRefreshChanged := base()
	activeWarmRefreshChanged.ActivePRWarmRefreshInterval = "5m"
	require.False(snap.RestartRequiredFor(activeWarmRefreshChanged))

	activeWindowChanged := base()
	activeWindowChanged.ActivePRWindow = "8h"
	require.False(snap.RestartRequiredFor(activeWindowChanged),
		"active PR window is hot-reloadable by the syncer")
}
