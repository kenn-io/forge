package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.kenn.io/forge/internal/apiclient/generated"

	"github.com/danielgtaylor/huma/v2"
	shellquote "github.com/kballard/go-shellquote"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/federation"
	"go.kenn.io/forge/internal/federationauth"
	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/providerplane"
	"go.kenn.io/forge/internal/server/configreload"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/server/settingsapi"
	"go.kenn.io/forge/internal/server/spokeapi"
	"go.kenn.io/forge/internal/stacks"
	"go.kenn.io/forge/internal/testutil"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/testutil/gitfixture"
	"go.kenn.io/forge/internal/workspace"
	"go.kenn.io/forge/internal/workspace/localruntime"
	"go.kenn.io/forge/platform"
)

const defaultTestConfigContent = `
sync_interval = "5m"
github_token_env = "KENN_FORGE_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "widget"
`

func setupTestServerWithConfig(
	t *testing.T,
) (*Server, *db.DB, string, *ghclient.Syncer) {
	return setupTestServerWithConfigContent(t, defaultTestConfigContent, &mockGH{})
}

// setupTestServerWithConfigContentNoSyncer builds a config-backed server that
// was never given a syncer, for settings paths that must work without one.
func setupTestServerWithConfigContentNoSyncer(
	t *testing.T,
	cfgContent string,
) (*Server, *db.DB, string) {
	t.Helper()

	dir := t.TempDir()
	database := dbtest.Open(t)
	cfgPath := filepath.Join(dir, "config.toml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(cfgContent), 0o644))
	cfg, err := config.Load(cfgPath)
	require.NoError(t, err)
	srv := NewWithConfig(database, nil, nil, nil, cfg, cfgPath, ServerOptions{})
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	return srv, database, cfgPath
}

func setupTestServerWithConfigContent(
	t *testing.T,
	cfgContent string,
	mock *mockGH,
) (*Server, *db.DB, string, *ghclient.Syncer) {
	t.Helper()
	return setupTestServerWithConfigContentAndOptions(
		t, cfgContent, mock, ServerOptions{HostCheckAllowLoopbackAnyPort: true},
	)
}

func setupTestServerWithConfigContentAndOptions(
	t *testing.T,
	cfgContent string,
	mock *mockGH,
	options ServerOptions,
) (*Server, *db.DB, string, *ghclient.Syncer) {
	t.Helper()

	dir := t.TempDir()
	database := dbtest.Open(t)
	cfgPath := filepath.Join(dir, "config.toml")
	err := os.WriteFile(cfgPath, []byte(cfgContent), 0o644)
	require.NoError(t, err)

	cfg, err := config.Load(cfgPath)
	require.NoError(t, err)

	clients := map[string]ghclient.Client{"github.com": mock}
	resolved := ghclient.ResolveConfiguredRepos(
		t.Context(), clients, cfg.Repos,
	)
	syncer := ghclient.NewSyncer(
		clients, database, nil, resolved.Expanded,
		time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	srv := NewWithConfig(
		database, syncer, nil, nil, cfg, cfgPath,
		options,
	)
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	return srv, database, cfgPath, syncer
}

func installSettingsTmuxRecorder(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	record := filepath.Join(dir, "commands")
	tmuxPath := filepath.Join(dir, "tmux")
	body := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> " + shellquote.Join(record) + "\n" +
		`case " $* " in *" list-sessions "*) printf 'sess-A:\n';; *" list-panes "*) printf 'pane-A\npane-B\n';; esac` + "\n"
	require.NoError(t, os.WriteFile(tmuxPath, []byte(body), 0o755))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return record
}

func readSettingsTmuxMouseCommands(t *testing.T, record string) []string {
	t.Helper()
	content, err := os.ReadFile(record)
	require.NoError(t, err)
	commands := make([]string, 0, 2)
	for command := range strings.SplitSeq(strings.TrimSpace(string(content)), "\n") {
		if strings.Contains(command, " list-sessions -F #{session_name}:#{@forge_owner}") ||
			strings.Contains(command, " set-option -q -g mouse ") {
			commands = append(commands, command)
		}
	}
	return commands
}

func readSettingsTmuxGraphicsCommands(t *testing.T, record string) []string {
	t.Helper()
	content, err := os.ReadFile(record)
	require.NoError(t, err)
	commands := make([]string, 0, 3)
	for command := range strings.SplitSeq(strings.TrimSpace(string(content)), "\n") {
		if strings.Contains(command, "allow-passthrough") ||
			strings.Contains(command, " terminal-features[100]") {
			commands = append(commands, command)
		}
	}
	return commands
}

func TestHandleGetSettingsReportsMCPDesiredAndActiveState(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	srv, _, _, _ := setupTestServerWithConfigContentAndOptions(t, `
sync_interval = "5m"
github_token_env = "KENN_FORGE_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[mcp]
enabled = true
port = 9092
diff_cache_mb = 256
`, &mockGH{}, ServerOptions{
		HostCheckAllowLoopbackAnyPort: true,
		MCPURL:                        "http://127.0.0.1:9092/mcp",
	})
	srv.bootCfgSnapshot.RequireAuth = true

	rr := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/settings", nil)
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())

	var resp spokeapi.SettingsResponse
	require.NoError(json.NewDecoder(rr.Body).Decode(&resp))
	assert.True(resp.MCP.Enabled)
	assert.Equal(9092, resp.MCP.Port)
	assert.Equal(256, resp.MCP.DiffCacheMB)
	assert.False(resp.MCP.RestartRequired)
	assert.Equal("http://127.0.0.1:9092/mcp", resp.MCP.ActiveURL)
	assert.True(resp.MCP.ActiveRequiresAuth)
}

func TestAirplaneModeSettingsPersistAndReload(t *testing.T) {
	require := require.New(t)
	srv, _, cfgPath, syncer := setupTestServerWithConfig(t)
	rr := testutil.DoJSON(t, srv, http.MethodPut, "/api/v1/settings", map[string]bool{"airplane_mode": true})
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	var response map[string]any
	require.NoError(json.NewDecoder(rr.Body).Decode(&response))
	require.Equal(true, response["airplane_mode"])
	require.False(syncer.AutomaticSyncEnabled())
	reloaded, err := config.Load(cfgPath)
	require.NoError(err)
	require.True(reloaded.AirplaneMode)
	reloaded.AirplaneMode = false
	require.NoError(reloaded.Save(cfgPath))
	srv.configreload.HandleConfigFileChanged()
	require.True(syncer.AutomaticSyncEnabled())
	rr = testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/settings", nil)
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	require.NoError(json.NewDecoder(rr.Body).Decode(&response))
	require.Equal(false, response["airplane_mode"])
}

func TestHandleUpdateSettingsRejectsInvalidMCPWithoutPublishing(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	srv, _, cfgPath, _ := setupTestServerWithConfig(t)

	rr := testutil.DoJSON(t, srv, http.MethodPut, "/api/v1/settings", spokeapi.UpdateSettingsRequest{MCP: &spokeapi.McpSettingsUpdate{
		Enabled: new(true), Port: new(8091),
	}})

	require.Equal(http.StatusBadRequest, rr.Code, rr.Body.String())
	assert.Equal(config.MCP{}, srv.cfg.MCP)

	reloaded, err := config.Load(cfgPath)
	require.NoError(err)
	assert.Equal(config.MCP{}, reloaded.MCP)
}

func TestCreateRepoPresetRejectsSaveFailureWithoutPublishing(t *testing.T) {
	require := require.New(t)
	srv, _, _, _ := setupTestServerWithConfigContent(t, `
sync_interval = "5m"
github_token_env = "KENN_FORGE_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "widget"

[[repo_presets]]
name = "Existing"
repos = [{ provider = "github", platform_host = "github.com", platform_repo_id = "R_widget", repo_path = "acme/widget" }]
`, &mockGH{})

	srv.configReloadMu.Lock()
	srv.cfgPath = t.TempDir()
	srv.configReloadMu.Unlock()
	replacement := config.RepoPreset{Name: "Replacement", Repos: []config.RepoPresetRepository{{
		Provider: "github", PlatformHost: "github.com", PlatformRepoID: "R_other", RepoPath: "acme/other",
	}}}
	rr := testutil.DoJSON(t, srv, http.MethodPost, "/api/v1/settings/repo-presets", replacement)
	require.Equal(http.StatusInternalServerError, rr.Code, rr.Body.String())
	require.Equal([]config.RepoPreset{{Name: "Existing", Repos: []config.RepoPresetRepository{{
		Provider: "github", PlatformHost: "github.com", PlatformRepoID: "R_widget", RepoPath: "acme/widget",
	}}}}, srv.cfg.RepoPresets)
}

func TestHandleUpdateSettingsPersistsModes(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	srv, _, cfgPath, _ := setupTestServerWithConfig(t)

	modes := config.DefaultModeVisibility()
	*modes.Docs = true
	*modes.Workspaces = false
	*modes.Actions = true

	rr := testutil.DoJSON(
		t, srv, http.MethodPut, "/api/v1/settings",
		spokeapi.UpdateSettingsRequest{Modes: &modes})

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())

	var resp spokeapi.SettingsResponse
	require.NoError(json.NewDecoder(rr.Body).Decode(&resp))
	assert.True(*resp.Modes.Actions)
	assert.True(*resp.Modes.Docs)
	assert.False(*resp.Modes.Workspaces)
	assert.True(*resp.Modes.Activity)
	assert.True(*resp.Modes.Repos)
	assert.True(*resp.Modes.Pulls)
	assert.True(*resp.Modes.Issues)

	cfg2, err := config.Load(cfgPath)
	require.NoError(err)
	assert.True(*cfg2.Modes.Actions)
	assert.True(*cfg2.Modes.Docs)
	assert.False(*cfg2.Modes.Workspaces)
	assert.True(*cfg2.Modes.Activity)
	assert.True(*cfg2.Modes.Repos)
	assert.True(*cfg2.Modes.Pulls)
	assert.True(*cfg2.Modes.Issues)

	activity := srv.cfg.Activity
	activity.TimeRange = "30d"
	rr = testutil.DoJSON(t, srv, http.MethodPut, "/api/v1/settings", spokeapi.UpdateSettingsRequest{
		Activity: &activity,
	})
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	cfg3, err := config.Load(cfgPath)
	require.NoError(err)
	assert.True(*cfg3.Modes.Actions)
}

func TestHandleUpdateSettingsPublishesPullConfigOnlyAfterPersistence(t *testing.T) {
	require := require.New(t)
	srv, _, _, _ := setupTestServerWithConfig(t)
	require.False(srv.pullAPI.ConfigSnapshot().AllowMidStackMerges)
	require.False(srv.pullAPI.ConfigSnapshot().UseWorkspaceActivityForRecency)
	require.False(srv.issueAPI.ConfigSnapshot().UseWorkspaceActivityForRecency)

	enabled := config.PullRequests{AllowMidStackMerges: true}
	activityEnabled := srv.cfg.Activity
	activityEnabled.UseWorkspaceActivityForRecency = true
	rr := testutil.DoJSON(t, srv, http.MethodPut, "/api/v1/settings", spokeapi.UpdateSettingsRequest{
		PullRequests: &enabled,
		Activity:     &activityEnabled,
	})

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	require.True(srv.pullAPI.ConfigSnapshot().AllowMidStackMerges)
	require.True(srv.pullAPI.ConfigSnapshot().UseWorkspaceActivityForRecency)
	require.True(srv.issueAPI.ConfigSnapshot().UseWorkspaceActivityForRecency)

	// Swapped under the reload lock: the config watcher goroutine reads
	// cfgPath under configReloadMu.
	srv.configReloadMu.Lock()
	srv.cfgPath = t.TempDir()
	srv.configReloadMu.Unlock()
	disabled := config.PullRequests{AllowMidStackMerges: false}
	activityDisabled := activityEnabled
	activityDisabled.UseWorkspaceActivityForRecency = false
	rr = testutil.DoJSON(t, srv, http.MethodPut, "/api/v1/settings", spokeapi.UpdateSettingsRequest{
		PullRequests: &disabled,
		Activity:     &activityDisabled,
	})

	require.Equal(http.StatusInternalServerError, rr.Code, rr.Body.String())
	require.True(
		srv.pullAPI.ConfigSnapshot().AllowMidStackMerges,
		"failed persistence published an uncommitted Pull config",
	)
	require.True(srv.pullAPI.ConfigSnapshot().UseWorkspaceActivityForRecency)
	require.True(srv.issueAPI.ConfigSnapshot().UseWorkspaceActivityForRecency)
}

func TestHandleUpdateSettingsSerializesWithConfigReload(t *testing.T) {
	require := require.New(t)
	srv, _, _, _ := setupTestServerWithConfig(t)

	srv.configReloadMu.Lock()
	done := make(chan error, 1)
	started := make(chan struct{})
	go func() {
		close(started)
		_, err := srv.updateSettings(t.Context(), &settingsapi.UpdateSettingsInput{
			Body: spokeapi.UpdateSettingsRequest{
				Activity: &config.Activity{TimeRange: "30d", ViewMode: "threaded"},
			},
		})
		done <- err
	}()
	<-started

	select {
	case err := <-done:
		srv.configReloadMu.Unlock()
		require.NoError(err)
		require.Fail("settings update completed while config reload lock was held")
	case <-time.After(100 * time.Millisecond):
	}

	srv.configReloadMu.Unlock()
	select {
	case err := <-done:
		require.NoError(err)
	case <-time.After(5 * time.Second):
		require.Fail("settings update did not complete after config reload lock was released")
	}
}

func TestHandleUpdateSettingsMergesWorkspaceFields(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	srv, _, cfgPath, _ := setupTestServerWithConfig(t)
	srv.cfg.Workspaces.AutoAssignOnCreate = true
	srv.cfg.Workspaces.ShowAgentStatusInLists = true
	require.NoError(srv.cfg.Save(cfgPath))

	defaultSidebarView := "item"
	rr := testutil.DoJSON(t, srv, http.MethodPut, "/api/v1/settings", spokeapi.UpdateSettingsRequest{
		Workspaces: &spokeapi.WorkspaceSettingsUpdate{DefaultSidebarView: &defaultSidebarView},
	})

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())

	cfg2, err := config.Load(cfgPath)
	require.NoError(err)
	assert.True(cfg2.Workspaces.AutoAssignOnCreate)
	assert.True(cfg2.Workspaces.ShowAgentStatusInLists)
	assert.Equal("item", cfg2.Workspaces.DefaultSidebarView)
	rr = testutil.DoJSON(t, srv, http.MethodPut, "/api/v1/settings", spokeapi.UpdateSettingsRequest{
		Workspaces: &spokeapi.WorkspaceSettingsUpdate{ShowAgentStatusInLists: new(false)},
	})
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	cfg2, err = config.Load(cfgPath)
	require.NoError(err)
	assert.False(cfg2.Workspaces.ShowAgentStatusInLists)
	assert.True(cfg2.Workspaces.AutoAssignOnCreate)
	assert.Equal("item", cfg2.Workspaces.DefaultSidebarView)
}

func TestHandleUpdateTerminalSettingsAppliesMouseToDedicatedTmuxServer(t *testing.T) {
	require := require.New(t)
	record := installSettingsTmuxRecorder(t)
	srv, _, _, _ := setupTestServerWithConfigContentAndOptions(t, `
sync_interval = "5m"
github_token_env = "KENN_FORGE_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "widget"
`, &mockGH{}, ServerOptions{
		HostCheckAllowLoopbackAnyPort: true,
		WorktreeDir:                   t.TempDir(),
	})
	require.NoError(os.WriteFile(record, nil, 0o600))

	srv.cfgMu.Lock()
	terminal := srv.cfg.Terminal
	srv.cfgMu.Unlock()
	terminal.TmuxMouse = new(false)
	rr := testutil.DoJSON(t, srv, http.MethodPut, "/api/v1/settings", spokeapi.UpdateSettingsRequest{
		Terminal: &terminal,
	})

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	assert.Equal(t, []string{
		"-L kenn-forge list-sessions -F #{session_name}:#{@forge_owner}",
		"-L kenn-forge set-option -q -g mouse off",
	}, readSettingsTmuxMouseCommands(t, record))
}

func TestHandleUpdateTerminalSettingsAppliesGraphicsToDedicatedTmuxServer(t *testing.T) {
	require := require.New(t)
	record := installSettingsTmuxRecorder(t)
	srv, _, _, _ := setupTestServerWithConfigContentAndOptions(t, `
sync_interval = "5m"
github_token_env = "KENN_FORGE_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "widget"
`, &mockGH{}, ServerOptions{
		HostCheckAllowLoopbackAnyPort: true,
		WorktreeDir:                   t.TempDir(),
	})
	require.NoError(os.WriteFile(record, nil, 0o600))

	srv.cfgMu.Lock()
	terminal := srv.cfg.Terminal
	srv.cfgMu.Unlock()
	terminal.Graphics = new(false)
	rr := testutil.DoJSON(t, srv, http.MethodPut, "/api/v1/settings", spokeapi.UpdateSettingsRequest{
		Terminal: &terminal,
	})

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	assert.Equal(t, []string{
		"-L kenn-forge set-option -q -g allow-passthrough off",
		"-L kenn-forge set-option -q -s -u terminal-features[100]",
		"-L kenn-forge set-option -q -p -u -t pane-A allow-passthrough",
		"-L kenn-forge set-option -q -p -u -t pane-B allow-passthrough",
	}, readSettingsTmuxGraphicsCommands(t, record))
}

func TestHandleUpdateSettingsRefreshesRuntimeTargets(t *testing.T) {
	dir := t.TempDir()
	agentPath := filepath.Join(dir, "codex-custom")
	require.NoError(t, os.WriteFile(
		agentPath,
		[]byte("#!/bin/sh\nexit 0\n"),
		0o755,
	))
	srv, _, _, _ := setupTestServerWithConfigContent(t, `
sync_interval = "5m"
github_token_env = "KENN_FORGE_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "widget"
`, &mockGH{})
	srv.runtime = localruntime.NewManager(localruntime.Options{
		Targets: []localruntime.LaunchTarget{{
			Key: "codex", Label: "Codex", Kind: localruntime.LaunchTargetAgent,
			Source: "builtin", Command: []string{"codex"},
			Available: false, DisabledReason: "codex not found on PATH",
		}},
	})
	t.Cleanup(srv.runtime.Shutdown)

	agents := []config.Agent{{
		Key:     "codex",
		Label:   "Custom Codex",
		Command: []string{agentPath, "--full-auto"},
	}}
	rr := testutil.DoJSON(
		t, srv, http.MethodPut, "/api/v1/settings",
		spokeapi.UpdateSettingsRequest{Agents: &agents})

	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	target := findRuntimeTargetForSettingsTest(
		t, srv.runtime.LaunchTargets(), "codex",
	)
	assert := assert.New(t)
	assert.Equal("Custom Codex", target.Label)
	assert.Equal([]string{agentPath, "--full-auto"}, target.Command)
	assert.True(target.Available)
}

func findRuntimeTargetForSettingsTest(
	t *testing.T,
	targets []localruntime.LaunchTarget,
	key string,
) localruntime.LaunchTarget {
	t.Helper()
	for _, target := range targets {
		if target.Key == key {
			return target
		}
	}
	require.Failf(t, "target not found", "key %q", key)
	return localruntime.LaunchTarget{}
}

func TestMergeTrackedReposReconcilesRenamedRouteByProviderIdentity(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	srv, _, _, syncer := setupTestServerWithConfig(t)
	syncer.SetRepos([]ghclient.RepoRef{{
		Platform: platform.KindGitHub, Owner: "acme", Name: "old-name",
		PlatformHost: "github.com", RepoPath: "acme/old-name",
		PlatformExternalID: "repo-x",
	}})

	// The same stable provider id resolves under a renamed route: the
	// tracked set must reconcile to one entry, not sync both routes.
	srv.settingsapi.MergeTrackedRepos([]ghclient.RepoRef{{
		Platform: platform.KindGitHub, Owner: "acme", Name: "new-name",
		PlatformHost: "github.com", RepoPath: "acme/new-name",
		PlatformExternalID: "repo-x", Archived: true,
	}})

	tracked := syncer.TrackedRepos()
	require.Len(tracked, 1)
	assert.Equal("new-name", tracked[0].Name)
	assert.True(tracked[0].Archived)
}

func TestMergeTrackedReposPreservesExactEntryProvenance(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	srv, _, _, syncer := setupTestServerWithConfig(t)
	syncer.SetRepos([]ghclient.RepoRef{{
		Platform: platform.KindGitHub, Owner: "acme", Name: "tools-new",
		PlatformHost: "github.com", RepoPath: "acme/tools-new",
		PlatformExternalID: "repo-x", ConfiguredRepoPath: "acme/tools",
	}})

	// A settings-resolved duplicate (glob refresh, API add) carries no
	// config-entry provenance; replacing the tracked ref must not erase
	// the correlation the exact entry needs on the next failed reload.
	srv.settingsapi.MergeTrackedRepos([]ghclient.RepoRef{{
		Platform: platform.KindGitHub, Owner: "acme", Name: "tools-new",
		PlatformHost: "github.com", RepoPath: "acme/tools-new",
		PlatformExternalID: "repo-x", Archived: true,
	}})

	tracked := syncer.TrackedRepos()
	require.Len(tracked, 1)
	assert.True(tracked[0].Archived)
	assert.Equal("acme/tools", tracked[0].ConfiguredRepoPath)
}

func TestMergeTrackedReposDoesNotTransferProvenanceAcrossProviderIdentities(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	srv, _, _, syncer := setupTestServerWithConfig(t)
	syncer.SetRepos([]ghclient.RepoRef{{
		Platform: platform.KindGitHub, Owner: "acme", Name: "tools",
		PlatformHost: "github.com", RepoPath: "acme/tools",
		PlatformExternalID: "repo-x", ConfiguredRepoPath: "acme/tools",
	}})

	// The tracked repo was renamed away and its old route reused by a
	// different repository. The renamed repo keeps its provenance through
	// stable identity; the route successor must not inherit it — two refs
	// claiming the same config entry would make a later fallback pick
	// whichever it sees first.
	srv.settingsapi.MergeTrackedRepos([]ghclient.RepoRef{
		{
			Platform: platform.KindGitHub, Owner: "acme", Name: "tools-new",
			PlatformHost: "github.com", RepoPath: "acme/tools-new",
			PlatformExternalID: "repo-x",
		},
		{
			Platform: platform.KindGitHub, Owner: "acme", Name: "tools",
			PlatformHost: "github.com", RepoPath: "acme/tools",
			PlatformExternalID: "repo-y",
		},
	})

	tracked := syncer.TrackedRepos()
	require.Len(tracked, 2)
	byName := make(map[string]ghclient.RepoRef, len(tracked))
	for _, repo := range tracked {
		byName[repo.Name] = repo
	}
	assert.Equal("acme/tools", byName["tools-new"].ConfiguredRepoPath,
		"stable identity carries provenance through the rename")
	assert.Empty(byName["tools"].ConfiguredRepoPath,
		"a different repository reusing the route must not inherit provenance")
}

func TestMergeTrackedReposTreatsCaseDifferingProviderIdsAsDistinct(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	srv, _, _, syncer := setupTestServerWithConfig(t)
	syncer.SetRepos([]ghclient.RepoRef{{
		Platform: platform.KindGitHub, Owner: "acme", Name: "tools",
		PlatformHost: "github.com", RepoPath: "acme/tools",
		PlatformExternalID: "repo-X", ConfiguredRepoPath: "acme/tools",
	}})

	// Provider ids are opaque, case-sensitive identities (identity keys
	// compare them exactly); a case-only difference is a different
	// repository and must not inherit route provenance.
	srv.settingsapi.MergeTrackedRepos([]ghclient.RepoRef{{
		Platform: platform.KindGitHub, Owner: "acme", Name: "tools",
		PlatformHost: "github.com", RepoPath: "acme/tools",
		PlatformExternalID: "repo-x",
	}})

	tracked := syncer.TrackedRepos()
	require.Len(tracked, 1)
	assert.Empty(tracked[0].ConfiguredRepoPath)
}

func TestReplaceGlobReposPreservesExactEntryProvenance(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	srv, _, _, syncer := setupTestServerWithConfig(t)
	syncer.SetRepos([]ghclient.RepoRef{{
		Platform: platform.KindGitHub, Owner: "acme", Name: "tools-new",
		PlatformHost: "github.com", RepoPath: "acme/tools-new",
		PlatformExternalID: "repo-x", ConfiguredRepoPath: "acme/tools",
	}})

	glob := config.Repo{Owner: "acme", Name: "*"}
	srv.settingsapi.ReplaceGlobRepos(glob, []ghclient.RepoRef{{
		Platform: platform.KindGitHub, Owner: "acme", Name: "tools-new",
		PlatformHost: "github.com", RepoPath: "acme/tools-new",
		PlatformExternalID: "repo-x", Archived: true,
	}}, []config.Repo{{Owner: "acme", Name: "tools"}, glob})

	tracked := syncer.TrackedRepos()
	require.Len(tracked, 1)
	assert.True(tracked[0].Archived)
	assert.Equal("acme/tools", tracked[0].ConfiguredRepoPath)
}

func trackedRepoArchived(syncer *ghclient.Syncer, owner, name string) bool {
	for _, repo := range syncer.TrackedRepos() {
		if strings.EqualFold(repo.Owner, owner) && strings.EqualFold(repo.Name, name) {
			return repo.Archived
		}
	}
	return false
}

func TestWorktreeBasePathResolverMatchesProviderIdentity(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	srv := wiredServer(&Server{cfg: &config.Config{Repos: []config.Repo{
		{
			Platform:         "github",
			PlatformHost:     "forge.example.com",
			PlatformRepoID:   "github-widget",
			Owner:            "acme",
			Name:             "widget",
			WorktreeBasePath: "/tmp/github-widget",
		},
		{
			Platform:         "gitlab",
			PlatformHost:     "forge.example.com",
			PlatformRepoID:   "gitlab-widget",
			Owner:            "acme",
			Name:             "widget",
			WorktreeBasePath: "/tmp/gitlab-widget",
		},
	}}})

	got, ok, err := srv.settingsapi.WorktreeBasePathForRepo(
		t.Context(), workspace.WorktreeBaseRepository{
			Platform: "gitlab", PlatformHost: "forge.example.com",
			PlatformRepoID: "gitlab-widget", Owner: "acme", Name: "widget",
		},
	)

	require.NoError(err)
	require.True(ok)
	assert.Equal("/tmp/gitlab-widget", got)

	_, ok, err = srv.settingsapi.WorktreeBasePathForRepo(
		t.Context(), workspace.WorktreeBaseRepository{
			Platform: "gitlab", PlatformHost: "forge.example.com",
			PlatformRepoID: "replacement-widget", Owner: "acme", Name: "widget",
		},
	)
	require.NoError(err)
	assert.False(ok)
}

func TestWorktreeBasePathResolverMatchesRegisteredProjectIdentity(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	database := dbtest.Open(t)
	repoID, err := database.UpsertRepo(t.Context(), db.RepoIdentity{
		Platform: "github", PlatformHost: "github.com",
		PlatformRepoID: "provider-widget", Owner: "acme", Name: "widget",
	})
	require.NoError(err)
	_, err = database.CreateProject(t.Context(), db.CreateProjectInput{
		DisplayName: "widget", LocalPath: "/work/widget",
		RepoID: sql.NullInt64{Int64: repoID, Valid: true},
	})
	require.NoError(err)
	srv := wiredServer(&Server{db: database})

	got, ok, err := srv.settingsapi.WorktreeBasePathForRepo(
		t.Context(), workspace.WorktreeBaseRepository{
			Platform: "github", PlatformHost: "github.com",
			PlatformRepoID: "provider-widget", Owner: "acme", Name: "widget",
		},
	)
	require.NoError(err)
	require.True(ok)
	assert.Equal("/work/widget", got)

	_, ok, err = srv.settingsapi.WorktreeBasePathForRepo(
		t.Context(), workspace.WorktreeBaseRepository{
			Platform: "github", PlatformHost: "github.com",
			PlatformRepoID: "replacement-widget", Owner: "acme", Name: "widget",
		},
	)
	require.NoError(err)
	assert.False(ok)
}

func TestProviderSettingsRepositoryObservationUsesHubTime(t *testing.T) {
	require := require.New(t)
	database := dbtest.Open(t)
	hubObservedAt := time.Date(2026, time.August, 24, 12, 0, 0, 0, time.UTC)
	srv := wiredServer(&Server{
		db:  database,
		now: func() time.Time { return hubObservedAt.Add(24 * time.Hour) },
	})
	provider := spokeapi.ProviderSettingsProjection{
		Settings: spokeapi.SettingsResponse{Repos: []ghclient.ConfiguredRepoStatus{{
			Provider: "github", PlatformHost: "github.com",
			PlatformRepoID: "repo-widget", Owner: "acme", Name: "widget",
			RepoPath: "acme/widget", TrackedRepoPath: "acme/widget",
		}}},
		RepositoryObservations: []spokeapi.ProviderRepositoryObservation{{
			Provider: "github", PlatformHost: "github.com",
			PlatformRepoID: "repo-widget", Owner: "acme", Name: "widget",
			RepoPath: "acme/widget", ObservedAt: hubObservedAt,
		}},
	}

	changed, err := srv.settingsapi.ObserveProviderSettingsRepositories(
		t.Context(), provider.RepositoryObservations,
	)
	require.NoError(err)
	require.True(changed)

	source := spokeapi.HubProviderSource{Db: database}
	require.NoError(source.ObserveRepositoryDescriptor(t.Context(), providerplane.RepositoryDescriptor{
		ProtocolVersion: federation.ProtocolVersion,
		Provider:        "github", PlatformHost: "github.com", PlatformRepoID: "repo-widget",
		Owner: "acme", Name: "widget", CloneURL: "https://github.com/acme/widget.git",
		DefaultBranch: "main", ObservedAt: hubObservedAt.Add(time.Minute),
	}))
}

func TestProviderSettingsProjectionCarriesCatalogObservationTime(t *testing.T) {
	require := require.New(t)
	database := dbtest.Open(t)
	observedAt := time.Date(2026, time.August, 24, 12, 0, 0, 0, time.UTC)
	_, accepted, err := database.ReconcileRepositoryObservation(
		t.Context(), db.RepoIdentity{
			Platform: "github", PlatformHost: "github.com",
			PlatformRepoID: "repo-widget", Owner: "acme", Name: "widget",
			RepoPath: "acme/widget",
		}, observedAt,
	)
	require.NoError(err)
	require.True(accepted)
	srv := wiredServer(&Server{db: database})

	projection, err := srv.syncevents.BuildProviderSettingsProjection(
		t.Context(), spokeapi.SettingsResponse{Repos: []ghclient.ConfiguredRepoStatus{{
			Provider: "github", PlatformHost: "github.com",
			PlatformRepoID: "repo-widget", Owner: "acme", Name: "widget",
			RepoPath: "acme/widget", TrackedRepoPath: "acme/widget",
		}}},
	)

	require.NoError(err)
	require.Len(projection.RepositoryObservations, 1)
	assert.Equal(t, observedAt, projection.RepositoryObservations[0].ObservedAt)
}

func TestLocalSettingsCorrelateRenamedRepositoryThroughCatalog(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	srv, database, _ := setupTestServerWithConfigContentNoSyncer(t, defaultTestConfigContent)
	srv.cfg.Repos[0].WorktreeBasePath = "/work/widget"
	observedAt := time.Now().UTC()
	seedVerifiedRepo(t, database, db.RepoIdentity{
		Platform: "github", PlatformHost: "github.com",
		PlatformRepoID: "repo-widget", Owner: "acme", Name: "widget",
	})
	_, accepted, err := database.ReconcileRepositoryObservation(
		t.Context(), db.RepoIdentity{
			Platform: "github", PlatformHost: "github.com",
			PlatformRepoID: "repo-widget", Owner: "acme-renamed", Name: "widget-renamed",
		}, observedAt.Add(time.Minute),
	)
	require.NoError(err)
	require.True(accepted)

	settings, err := srv.buildLocalSettingsResponse(t.Context())
	require.NoError(err)
	require.Len(settings.Repos, 1)
	assert.Equal("repo-widget", settings.Repos[0].PlatformRepoID)
	assert.Equal("acme-renamed/widget-renamed", settings.Repos[0].TrackedRepoPath)
}

func TestLocalSettingsDoNotCorrelateReusedRepositoryRoute(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	srv, database, _ := setupTestServerWithConfigContentNoSyncer(t, defaultTestConfigContent)
	seedVerifiedRepo(t, database, db.RepoIdentity{
		Platform: "github", PlatformHost: "github.com",
		PlatformRepoID: "repo-old", Owner: "acme", Name: "widget",
	})
	_, accepted, err := database.ReconcileRepositoryObservation(
		t.Context(), db.RepoIdentity{
			Platform: "github", PlatformHost: "github.com",
			PlatformRepoID: "repo-new", Owner: "acme", Name: "widget",
		}, time.Now().UTC().Add(time.Minute),
	)
	require.NoError(err)
	require.True(accepted)

	settings, err := srv.buildLocalSettingsResponse(t.Context())
	require.NoError(err)
	require.Len(settings.Repos, 1)
	assert.Empty(settings.Repos[0].PlatformRepoID)
	assert.Empty(settings.Repos[0].TrackedRepoPath)
}

func TestRoleAwareSettingsRequireOneOwnerPerNodeWrite(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	hub, hubDB, _, _ := setupTestServerWithConfigContentAndOptions(t, `
host = "127.0.0.1"
port = 8091

[detail]
initial_timeline_entry_limit = 50

[workspaces]
auto_assign_on_create = false
default_sidebar_view = "diff"

[roborev]
init_managed_clones = false

[[repos]]
platform = "github"
platform_host = "github.com"
owner = "acme"
name = "widget"

	[fleet]
	role = "hub"
	`, &mockGH{}, ServerOptions{HostCheckAllowLoopbackAnyPort: true})
	seedVerifiedRepo(t, hubDB, verifiedGitHubRepoIdentity(
		"github.com", "acme", "widget",
	))
	hubHTTP := httptest.NewTLSServer(hub)
	t.Cleanup(hubHTTP.Close)

	credentials, err := federationauth.Open(filepath.Join(t.TempDir(), "credentials.json"))
	require.NoError(err)
	require.NoError(credentials.StoreOutbound(
		proxyTestHubID, "settings-spoke-secret", federationauth.SpokeToHubScopes(),
	))
	nodeConfigDir := t.TempDir()
	nodeConfigPath := filepath.Join(nodeConfigDir, "config.toml")
	nodeConfigBody := fmt.Sprintf(`
host = "127.0.0.1"
port = 8091

[api]
require_auth = true

[detail]
initial_timeline_entry_limit = 25

[workspaces]
auto_assign_on_create = false
default_sidebar_view = "diff"

[[repos]]
platform = "github"
platform_host = "github.com"
owner = "acme"
name = "widget"

[fleet]
enabled = true
role = "spoke"
base_url = "https://spoke.example"

[fleet.hub]
node_id = %q
base_url = %q
`, proxyTestHubID, hubHTTP.URL)
	require.NoError(os.WriteFile(nodeConfigPath, []byte(nodeConfigBody), 0o600))
	nodeConfig, err := config.Load(nodeConfigPath)
	require.NoError(err)
	spoke := NewWithConfig(
		dbtest.Open(t), nil, nil, nil, nodeConfig, nodeConfigPath,
		ServerOptions{
			HostCheckAllowLoopbackAnyPort:      true,
			FederationSpokeID:                  proxyTestNodeID,
			FederationSpokeActive:              true,
			FederationCredentials:              credentials,
			FederationHTTPClient:               hubHTTP.Client(),
			DisableWorkspaceBackgroundMonitors: true,
		},
	)
	t.Cleanup(func() { gracefulShutdown(t, spoke) })

	autoAssign := true
	response := testutil.DoJSON(t, spoke, http.MethodPut, "/api/v1/settings", spokeapi.UpdateSettingsRequest{
		Detail:     &config.Detail{InitialTimelineEntryLimit: 75},
		Workspaces: &spokeapi.WorkspaceSettingsUpdate{AutoAssignOnCreate: &autoAssign},
	})

	require.Equal(http.StatusBadRequest, response.Code, response.Body.String())
	var problem httpapi.ProblemError
	require.NoError(json.NewDecoder(response.Body).Decode(&problem))
	assert.Equal(httpapi.CodeValidationError, problem.Code)
	assert.Equal("mixedSettingsOwnership", problem.Details["reason"])
	hub.cfgMu.Lock()
	assert.Equal(50, hub.cfg.Detail.InitialTimelineEntryLimit)
	assert.False(hub.cfg.Workspaces.AutoAssignOnCreate)
	hub.cfgMu.Unlock()
	spoke.cfgMu.Lock()
	assert.Equal(25, spoke.cfg.Detail.InitialTimelineEntryLimit)
	assert.False(spoke.cfg.Workspaces.AutoAssignOnCreate)
	spoke.cfgMu.Unlock()

	response = testutil.DoJSON(t, spoke, http.MethodPut, "/api/v1/settings", spokeapi.UpdateSettingsRequest{
		Detail: &config.Detail{InitialTimelineEntryLimit: 75},
	})

	require.Equal(http.StatusOK, response.Code, response.Body.String())
	response = testutil.DoJSON(t, spoke, http.MethodPut, "/api/v1/settings", spokeapi.UpdateSettingsRequest{
		Workspaces: &spokeapi.WorkspaceSettingsUpdate{AutoAssignOnCreate: &autoAssign},
	})

	require.Equal(http.StatusOK, response.Code, response.Body.String())
	response = testutil.DoJSON(t, spoke, http.MethodPut, "/api/v1/settings", spokeapi.UpdateSettingsRequest{
		Roborev: &spokeapi.RoborevSettingsUpdate{InitManagedClones: new(true)},
	})

	require.Equal(http.StatusOK, response.Code, response.Body.String())

	hub.cfgMu.Lock()
	assert.Equal(75, hub.cfg.Detail.InitialTimelineEntryLimit)
	assert.False(hub.cfg.Workspaces.AutoAssignOnCreate)
	hub.cfgMu.Unlock()
	spoke.cfgMu.Lock()
	assert.Equal(25, spoke.cfg.Detail.InitialTimelineEntryLimit)
	assert.True(spoke.cfg.Workspaces.AutoAssignOnCreate)
	assert.True(spoke.cfg.Roborev.InitManagedClones)
	spoke.cfgMu.Unlock()

	worktreeBase := t.TempDir()
	canonicalWorktreeBase, err := filepath.EvalSymlinks(worktreeBase)
	require.NoError(err)
	gitfixture.Run(t, worktreeBase, "init", "--initial-branch=main")
	gitfixture.Run(t, worktreeBase, "remote", "add", "origin", "https://github.com/acme/widget.git")
	response = testutil.DoJSON(
		t, spoke, http.MethodPut,
		"/api/v1/repo/github/acme/widget/worktree-base",
		settingsapi.RepoWorktreeBaseRequest{WorktreeBasePath: worktreeBase})

	require.Equal(http.StatusOK, response.Code, response.Body.String())
	assert.Equal(canonicalWorktreeBase, spoke.cfg.Repos[0].WorktreeBasePath)
	assert.Empty(hub.cfg.Repos[0].WorktreeBasePath)

	var settings spokeapi.SettingsResponse
	require.NoError(json.NewDecoder(response.Body).Decode(&settings))
	require.Len(settings.Repos, 1)
	assert.Equal(canonicalWorktreeBase, settings.Repos[0].WorktreeBasePath)
	assert.Equal(75, settings.Detail.InitialTimelineEntryLimit)
	assert.True(settings.Workspaces.AutoAssignOnCreate)
	assert.Equal(config.FleetRoleSpoke, settings.Fleet.Role)

	response = testutil.DoJSON(t, spoke, http.MethodGet, "/api/v1/settings", nil)
	require.Equal(http.StatusOK, response.Code, response.Body.String())
	require.NoError(json.NewDecoder(response.Body).Decode(&settings))
	require.Len(settings.Repos, 1)
	assert.Equal(canonicalWorktreeBase, settings.Repos[0].WorktreeBasePath)

	response = testutil.DoJSON(t, spoke, http.MethodGet, "/api/v1/settings/local", nil)
	require.Equal(http.StatusOK, response.Code, response.Body.String())
	require.NoError(json.NewDecoder(response.Body).Decode(&settings))
	assert.Equal(25, settings.Detail.InitialTimelineEntryLimit)
	assert.True(settings.Workspaces.AutoAssignOnCreate)
	assert.Equal(config.FleetRoleSpoke, settings.Fleet.Role)
	assert.False(settings.ProviderSettingsLoaded)
}

func TestNodeWorktreeBaseOverrideFollowsHubRepositoryIdentity(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	srv, database, configPath := setupTestServerWithConfigContentNoSyncer(t, `
host = "127.0.0.1"
port = 8091
`)
	observedAt := time.Now().UTC().Truncate(time.Second)
	projection := spokeapi.ProviderSettingsResponse{
		Repos: []ghclient.ConfiguredRepoStatus{{
			Provider: "github", PlatformHost: "github.com",
			PlatformRepoID: "repo-late", Owner: "acme", Name: "late",
			RepoPath: "acme/late", TrackedRepoPath: "acme/late",
		}},
		RepositoryObservations: []spokeapi.ProviderRepositoryObservation{{
			Provider: "github", PlatformHost: "github.com",
			PlatformRepoID: "repo-late", Owner: "acme", Name: "late",
			RepoPath: "acme/late", ObservedAt: observedAt,
		}},
		RepoPresets: []config.RepoPreset{},
	}
	var reads atomic.Int32
	srv.providerSource = &spokeapi.HubProviderSource{
		Db: database,
		Client: providerPlaneClientFunc(func(
			_ context.Context, scope federationauth.Scope, request *http.Request,
		) (*http.Response, error) {
			require.Equal(federationauth.ScopeProviderRead, scope)
			require.Equal("/api/v1/federation/provider/settings", request.URL.Path)
			reads.Add(1)
			encoded, err := json.Marshal(projection)
			require.NoError(err)
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(bytes.NewReader(encoded)),
				Request:    request,
			}, nil
		}),
	}
	worktreeBase := t.TempDir()
	canonicalWorktreeBase, err := filepath.EvalSymlinks(worktreeBase)
	require.NoError(err)
	gitfixture.Run(t, worktreeBase, "init", "--initial-branch=main")
	gitfixture.Run(t, worktreeBase, "remote", "add", "origin", "https://github.com/acme/late.git")

	response := testutil.DoJSON(
		t, srv, http.MethodPut, "/api/v1/repo/github/acme/late/worktree-base",
		settingsapi.RepoWorktreeBaseRequest{WorktreeBasePath: worktreeBase})

	require.Equal(http.StatusOK, response.Code, response.Body.String())
	srv.cfgMu.Lock()
	configuredRepos := configreload.CloneReloadedConfig(srv.cfg).Repos
	srv.cfgMu.Unlock()
	require.Len(configuredRepos, 1)
	assert.Equal(canonicalWorktreeBase, configuredRepos[0].WorktreeBasePath)

	projection.Repos[0].Owner = "renamed"
	projection.Repos[0].Name = "late-renamed"
	projection.Repos[0].RepoPath = "renamed/late-renamed"
	projection.Repos[0].TrackedRepoPath = "renamed/late-renamed"
	projection.RepositoryObservations[0].Owner = "renamed"
	projection.RepositoryObservations[0].Name = "late-renamed"
	projection.RepositoryObservations[0].RepoPath = "renamed/late-renamed"
	projection.RepositoryObservations[0].ObservedAt = observedAt.Add(time.Minute)
	response = testutil.DoJSON(
		t, srv, http.MethodPut,
		"/api/v1/repo/github/renamed/late-renamed/worktree-base",
		settingsapi.RepoWorktreeBaseRequest{})

	require.Equal(http.StatusOK, response.Code, response.Body.String())
	assert.Equal(int32(2), reads.Load(), "each mutation uses one pre-commit hub snapshot")
	srv.cfgMu.Lock()
	configuredRepos = configreload.CloneReloadedConfig(srv.cfg).Repos
	srv.cfgMu.Unlock()
	require.Len(configuredRepos, 1)
	assert.Equal("renamed", configuredRepos[0].Owner)
	assert.Equal("late-renamed", configuredRepos[0].Name)
	assert.Empty(configuredRepos[0].WorktreeBasePath)
	contents, err := os.ReadFile(configPath)
	require.NoError(err)
	assert.Contains(string(contents), `platform_repo_id = "repo-late"`)
	var settings spokeapi.SettingsResponse
	require.NoError(json.NewDecoder(response.Body).Decode(&settings))
	require.Len(settings.Repos, 1)
	assert.Equal("repo-late", settings.Repos[0].PlatformRepoID)
	assert.Empty(settings.Repos[0].WorktreeBasePath)
	assert.True(settings.ProviderSettingsLoaded)
}

func TestNodeLocalSettingsCommitWhileHubIsUnavailable(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	srv, _, configPath, _ := setupTestServerWithConfigContent(t, `
host = "127.0.0.1"
port = 8091

[workspaces]
auto_assign_on_create = false
`, &mockGH{})
	srv.providerSource = &spokeapi.HubProviderSource{
		Client: providerPlaneClientFunc(func(
			context.Context, federationauth.Scope, *http.Request,
		) (*http.Response, error) {
			return nil, providerplane.ErrHubUnavailable
		}),
	}
	autoAssign := true

	response := testutil.DoJSON(t, srv, http.MethodPut, "/api/v1/settings", spokeapi.UpdateSettingsRequest{
		Workspaces: &spokeapi.WorkspaceSettingsUpdate{AutoAssignOnCreate: &autoAssign},
	})

	require.Equal(http.StatusOK, response.Code, response.Body.String())
	var settings settingsResponse
	require.NoError(json.NewDecoder(response.Body).Decode(&settings))
	assert.True(settings.Workspaces.AutoAssignOnCreate)
	assert.False(settings.ProviderSettingsLoaded)
	persisted, err := config.Load(configPath)
	require.NoError(err)
	assert.True(persisted.Workspaces.AutoAssignOnCreate)
}

func TestNodeLocalSettingsSaveStopsWaitingForHubAtPeerTimeout(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	srv, _, configPath := setupTestServerWithConfigContent(t, `
host = "127.0.0.1"
port = 8091

[fleet]
peer_timeout = "50ms"
`, &mockGH{})
	srv.providerSource = &hubProviderSource{
		client: providerPlaneClientFunc(func(
			ctx context.Context, _ federationauth.Scope, _ *http.Request,
		) (*http.Response, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}),
	}
	callerContext, cancelCaller := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancelCaller()
	autoAssign := true
	started := time.Now()

	output, err := srv.updateSettings(callerContext, &updateSettingsInput{Body: updateSettingsRequest{
		Workspaces: &workspaceSettingsUpdate{AutoAssignOnCreate: &autoAssign},
	}})

	require.NoError(err)
	assert.Less(time.Since(started), 5*time.Second,
		"a committed spoke-local save must not wait on the hub past the peer timeout")
	assert.True(output.Body.Workspaces.AutoAssignOnCreate)
	assert.False(output.Body.ProviderSettingsLoaded)
	persisted, err := config.Load(configPath)
	require.NoError(err)
	assert.True(persisted.Workspaces.AutoAssignOnCreate)
}

func TestNodeSettingsLoadWhileFederationIsDisabled(t *testing.T) {
	require := require.New(t)
	srv, _, _, _ := setupTestServerWithConfig(t)
	srv.cfg.Fleet.Enabled = false
	srv.cfg.Fleet.Role = config.FleetRoleSpoke
	srv.providerSource = &spokeapi.HubProviderSource{
		Client: providerPlaneClientFunc(func(
			context.Context, federationauth.Scope, *http.Request,
		) (*http.Response, error) {
			require.Fail("disabled federation must not request hub settings")
			return nil, nil
		}),
		Enabled: srv.streamapi.FederationEnabled,
	}

	response := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/settings", nil)

	require.Equal(http.StatusOK, response.Code, response.Body.String())
	var settings spokeapi.SettingsResponse
	require.NoError(json.NewDecoder(response.Body).Decode(&settings))
	require.False(settings.Fleet.Enabled)
	require.False(settings.ProviderSettingsLoaded,
		"a spoke without its hub's settings must not present local values as hub-owned")
}

func TestInactiveSpokeSettingsStayLocal(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	srv, _, configPath, _ := setupTestServerWithConfigContentAndOptions(t, `
host = "127.0.0.1"
port = 8091

[api]
require_auth = true

[workspaces]
auto_assign_on_create = false

[fleet]
enabled = true
role = "spoke"
base_url = "https://spoke.example"

[fleet.hub]
node_id = "0123456789abcdef0123456789abcdef"
base_url = "https://hub.example"
`, &mockGH{}, ServerOptions{
		HostCheckAllowLoopbackAnyPort: true,
		FederationSpokeID:             proxyTestNodeID,
	})
	require.NotNil(srv.providerSource)
	require.Nil(srv.providerSource.Client)

	response := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/settings", nil)
	require.Equal(http.StatusOK, response.Code, response.Body.String())

	autoAssign := true
	response = testutil.DoJSON(t, srv, http.MethodPut, "/api/v1/settings", spokeapi.UpdateSettingsRequest{
		Workspaces: &spokeapi.WorkspaceSettingsUpdate{AutoAssignOnCreate: &autoAssign},
	})

	require.Equal(http.StatusOK, response.Code, response.Body.String())
	assert.True(srv.cfg.Workspaces.AutoAssignOnCreate)
	persisted, err := config.Load(configPath)
	require.NoError(err)
	assert.True(persisted.Workspaces.AutoAssignOnCreate)
}

func TestRoleAwareSettingsRejectFederationWriteToHubLocalPolicy(t *testing.T) {
	srv, _, _, _ := setupTestServerWithConfig(t)
	autoAssign := true
	ctx := federationauth.WithPrincipal(t.Context(), federationauth.Principal{
		NodeID: proxyTestNodeID,
		Scopes: map[federationauth.Scope]struct{}{
			federationauth.ScopeProviderWrite: {},
		},
	})
	_, err := srv.updateSettings(ctx, &settingsapi.UpdateSettingsInput{Body: spokeapi.UpdateSettingsRequest{
		Workspaces: &spokeapi.WorkspaceSettingsUpdate{AutoAssignOnCreate: &autoAssign},
	}})
	var statusErr huma.StatusError
	require.ErrorAs(t, err, &statusErr)
	assert.Equal(t, http.StatusForbidden, statusErr.GetStatus())
	assert.False(t, srv.cfg.Workspaces.AutoAssignOnCreate)
}

// TestReconcileNativeStackProjectionSkipsSupersededDisable covers the window
// between the swap and the projection lock. A disable that lost the race to a
// later enable must not replay: the enable has already published native
// ordering, and replaying branch inference over it would leave the projection
// disagreeing with the preference until another sync.
func TestReconcileNativeStackProjectionSkipsSupersededDisable(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	srv, database, _, syncer := setupTestServerWithConfigContent(t, `
sync_interval = "5m"
github_token_env = "KENN_FORGE_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "widget"

[pull_requests]
prefer_github_native_stacks = true
`, &mockGH{})
	ctx := t.Context()
	seedStackedPR(t, database, "acme", "widget", 10, "feat/base", "main", db.MergeRequestStateOpen, "", "")
	seedStackedPR(t, database, "acme", "widget", 11, "feat/tip", "feat/base", db.MergeRequestStateOpen, "", "")
	repo, err := database.GetRepoByIdentity(ctx, verifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	require.NotNil(repo)
	now := time.Now().UTC()
	require.NoError(database.ReplaceGitHubNativeStack(ctx, db.GitHubNativeStack{
		RepoID: repo.ID, GitHubID: 9001, Number: 42, Size: 2,
		BaseRef: "main", IsOpen: true, GitHubCreatedAt: now,
		ContentFingerprint: "native", LastObservedAt: now,
		Members: []db.GitHubNativeStackMember{
			{Position: 1, PullRequestNumber: 11, State: "open", HeadRef: "feat/tip", HeadSHA: "sha11"},
			{Position: 2, PullRequestNumber: 10, State: "open", HeadRef: "feat/base", HeadSHA: "sha10"},
		},
	}))
	require.NoError(stacks.RunDetectionWithNativeStacks(ctx, database, repo.ID, []int{42}))
	client := setupTestClientWithBaseURL(t, srv, "http://127.0.0.1:8091")
	require.True(syncer.PrefersGitHubNativeStacks())

	// A disable observed the enabled value, but by the time it reaches
	// reconciliation a later enable has already won the swap.
	srv.syncevents.ReconcileGitHubNativeStackProjection(true, false)

	after, err := client.HTTP.GetPullStackWithResponse(ctx, &generated.GetPullStackRequestOptions{PathParams: &generated.GetPullStackPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(10)}})
	require.NoError(err)
	require.NotNil(after.JSON200)
	require.NotNil(after.JSON200.Members)
	assert.Equal([]int64{11, 10}, stackMemberNumbers(after.JSON200.Members),
		"a superseded disable must not overwrite the projection the current preference produced")
}
func TestSpokeSyncBudgetFollowsHubSettings(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	srv, _, _ := setupTestServerWithConfigContent(t, `
host = "127.0.0.1"
port = 8091
`, &mockGH{})
	srv.syncer = nil
	srv.cfg.Fleet.Enabled = true
	srv.fleetEnabledAtBoot = true
	hubSettings := providerSettingsResponse{
		Repos: []ghclient.ConfiguredRepoStatus{}, RepoPresets: []config.RepoPreset{},
		RepositoryObservations: []providerRepositoryObservation{},
		Sync:                   syncSettingsResponse{BudgetPerHour: 2400},
	}
	var forwarded providerSettingsUpdate
	srv.providerSource = &hubProviderSource{
		client: providerPlaneClientFunc(func(
			_ context.Context, _ federationauth.Scope, request *http.Request,
		) (*http.Response, error) {
			if request.Method == http.MethodPut {
				require.NoError(json.NewDecoder(request.Body).Decode(&forwarded))
				hubSettings.Sync.BudgetPerHour = *forwarded.Sync.BudgetPerHour
			}
			encoded, err := json.Marshal(hubSettings)
			require.NoError(err)
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(bytes.NewReader(encoded)),
				Request:    request,
			}, nil
		}),
	}

	response := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/settings", nil)
	require.Equal(http.StatusOK, response.Code, response.Body.String())
	var settings settingsResponse
	require.NoError(json.NewDecoder(response.Body).Decode(&settings))
	assert.Equal(2400, settings.Sync.BudgetPerHour)

	budget := 1800
	response = testutil.DoJSON(t, srv, http.MethodPut, "/api/v1/settings", updateSettingsRequest{
		Sync: &syncSettingsUpdate{BudgetPerHour: &budget},
	})
	require.Equal(http.StatusOK, response.Code, response.Body.String())
	require.NotNil(forwarded.Sync)
	require.NotNil(forwarded.Sync.BudgetPerHour)
	assert.Equal(1800, *forwarded.Sync.BudgetPerHour)
	require.NoError(json.NewDecoder(response.Body).Decode(&settings))
	assert.Equal(1800, settings.Sync.BudgetPerHour)
}

func TestHubAppliesSyncBudgetFromSpoke(t *testing.T) {
	require := require.New(t)
	srv, _, configPath := setupTestServerWithConfig(t)
	srv.syncer = nil
	ctx := federationauth.WithPrincipal(t.Context(), federationauth.Principal{
		NodeID: proxyTestNodeID,
		Scopes: map[federationauth.Scope]struct{}{federationauth.ScopeProviderWrite: {}},
	})
	budget := 1800

	output, err := srv.federationUpdateProviderSettings(ctx, &federationUpdateProviderSettingsInput{
		Body: providerSettingsUpdate{Sync: &syncSettingsUpdate{BudgetPerHour: &budget}},
	})

	require.NoError(err)
	require.Equal(1800, output.Body.Sync.BudgetPerHour)
	persisted, err := config.Load(configPath)
	require.NoError(err)
	require.Equal(1800, persisted.SyncBudgetPerHour)
}
