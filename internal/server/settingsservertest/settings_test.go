package settingsservertest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	gh "github.com/google/go-github/v91/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/apiclient/generated"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/federation"
	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/platformdb"
	"go.kenn.io/forge/internal/providerplane"
	"go.kenn.io/forge/internal/server"
	"go.kenn.io/forge/internal/server/repoapi"
	"go.kenn.io/forge/internal/server/spokeapi"
	"go.kenn.io/forge/internal/stacks"
	"go.kenn.io/forge/internal/testutil"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/platform"
	"go.kenn.io/forge/platform/gitealike"
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
) (*server.Server, *db.DB, string, *ghclient.Syncer) {
	return setupTestServerWithConfigContent(t, defaultTestConfigContent, &mockGH{})
}

func setupTestServerWithConfigContent(
	t *testing.T,
	cfgContent string,
	mock *mockGH,
) (*server.Server, *db.DB, string, *ghclient.Syncer) {
	return setupTestServerWithConfigContentAndOptions(
		t, cfgContent, mock, server.ServerOptions{HostCheckAllowLoopbackAnyPort: true},
	)
}

func setupTestServerWithConfigContentAndOptions(
	t *testing.T,
	cfgContent string,
	mock *mockGH,
	options server.ServerOptions,
) (*server.Server, *db.DB, string, *ghclient.Syncer) {
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
	srv := server.NewWithConfig(
		database, syncer, nil, nil, cfg, cfgPath,
		options,
	)
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	return srv, database, cfgPath, syncer
}

func setupTestServerWithConfigProviders(
	t *testing.T,
	cfgContent string,
	mock *mockGH,
	providers ...platform.Provider,
) (*server.Server, *db.DB, string, *ghclient.Syncer) {
	t.Helper()

	dir := t.TempDir()
	database := dbtest.Open(t)
	cfgPath := filepath.Join(dir, "config.toml")
	err := os.WriteFile(cfgPath, []byte(cfgContent), 0o644)
	require.NoError(t, err)

	cfg, err := config.Load(cfgPath)
	require.NoError(t, err)

	clients := map[string]ghclient.Client{"github.com": mock}
	registry, err := ghclient.NewProviderRegistry(clients, providers...)
	require.NoError(t, err)
	resolved := ghclient.ResolveConfiguredReposWithRegistry(
		t.Context(), registry, cfg.Repos,
	)
	syncer := ghclient.NewSyncerWithRegistry(
		registry, database, nil, resolved.Expanded, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	srv := server.NewWithConfig(
		database, syncer, nil, nil, cfg, cfgPath,
		server.ServerOptions{HostCheckAllowLoopbackAnyPort: true},
	)
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	return srv, database, cfgPath, syncer
}

type repoImportTestProvider struct {
	kind  platform.Kind
	host  string
	repos []platform.Repository
}

func (p repoImportTestProvider) Platform() platform.Kind { return p.kind }

func (p repoImportTestProvider) Host() string { return p.host }

func (p repoImportTestProvider) Capabilities() platform.Capabilities {
	return platform.Capabilities{ReadRepositories: true}
}

func (p repoImportTestProvider) GetRepository(
	_ context.Context,
	ref platform.RepoRef,
) (platform.Repository, error) {
	for _, repo := range p.repos {
		repoPath := strings.TrimSpace(repo.Ref.RepoPath)
		if repoPath == "" {
			repoPath = repo.Ref.Owner + "/" + repo.Ref.Name
		}
		refPath := strings.TrimSpace(ref.RepoPath)
		if refPath == "" {
			refPath = ref.Owner + "/" + ref.Name
		}
		if strings.EqualFold(repoPath, refPath) ||
			(strings.EqualFold(repo.Ref.Owner, ref.Owner) &&
				strings.EqualFold(repo.Ref.Name, ref.Name)) {
			return repo, nil
		}
	}
	return platform.Repository{}, errors.New("not found")
}

func (p repoImportTestProvider) ListRepositories(
	_ context.Context,
	owner string,
	_ platform.RepositoryListOptions,
) ([]platform.Repository, error) {
	repos := make([]platform.Repository, 0, len(p.repos))
	for _, repo := range p.repos {
		if strings.EqualFold(repo.Ref.Owner, owner) {
			repos = append(repos, repo)
		}
	}
	return repos, nil
}

type gitealikeImportTransport struct {
	userRepos     []gitealike.RepositoryDTO
	userReposErr  error
	orgRepos      []gitealike.RepositoryDTO
	orgReposErr   error
	repository    gitealike.RepositoryDTO
	repositoryErr error
}

func (t *gitealikeImportTransport) GetRepository(
	context.Context,
	string,
	string,
) (gitealike.RepositoryDTO, error) {
	return t.repository, t.repositoryErr
}

func (t *gitealikeImportTransport) ListUserRepositories(
	context.Context,
	string,
	gitealike.PageOptions,
) ([]gitealike.RepositoryDTO, gitealike.Page, error) {
	return t.userRepos, gitealike.Page{}, t.userReposErr
}

func (t *gitealikeImportTransport) ListOrgRepositories(
	context.Context,
	string,
	gitealike.PageOptions,
) ([]gitealike.RepositoryDTO, gitealike.Page, error) {
	return t.orgRepos, gitealike.Page{}, t.orgReposErr
}

func (t *gitealikeImportTransport) ListOpenPullRequests(
	context.Context,
	platform.RepoRef,
	gitealike.PageOptions,
) ([]gitealike.PullRequestDTO, gitealike.Page, error) {
	return nil, gitealike.Page{}, errors.New("unexpected ListOpenPullRequests call")
}

func (t *gitealikeImportTransport) GetPullRequest(
	context.Context,
	platform.RepoRef,
	int,
) (gitealike.PullRequestDTO, error) {
	return gitealike.PullRequestDTO{}, errors.New("unexpected GetPullRequest call")
}

func (t *gitealikeImportTransport) ListPullRequestComments(
	context.Context,
	platform.RepoRef,
	int,
	gitealike.PageOptions,
) ([]gitealike.CommentDTO, gitealike.Page, error) {
	return nil, gitealike.Page{}, errors.New("unexpected ListPullRequestComments call")
}

func (t *gitealikeImportTransport) ListPullRequestReviews(
	context.Context,
	platform.RepoRef,
	int,
	gitealike.PageOptions,
) ([]gitealike.ReviewDTO, gitealike.Page, error) {
	return nil, gitealike.Page{}, errors.New("unexpected ListPullRequestReviews call")
}

func (t *gitealikeImportTransport) ListPullRequestCommits(
	context.Context,
	platform.RepoRef,
	int,
	gitealike.PageOptions,
) ([]gitealike.CommitDTO, gitealike.Page, error) {
	return nil, gitealike.Page{}, errors.New("unexpected ListPullRequestCommits call")
}

func (t *gitealikeImportTransport) ListOpenIssues(
	context.Context,
	platform.RepoRef,
	gitealike.PageOptions,
) ([]gitealike.IssueDTO, gitealike.Page, error) {
	return nil, gitealike.Page{}, errors.New("unexpected ListOpenIssues call")
}

func (t *gitealikeImportTransport) GetIssue(
	context.Context,
	platform.RepoRef,
	int,
) (gitealike.IssueDTO, error) {
	return gitealike.IssueDTO{}, errors.New("unexpected GetIssue call")
}

func (t *gitealikeImportTransport) ListIssueComments(
	context.Context,
	platform.RepoRef,
	int,
	gitealike.PageOptions,
) ([]gitealike.CommentDTO, gitealike.Page, error) {
	return nil, gitealike.Page{}, errors.New("unexpected ListIssueComments call")
}

func (t *gitealikeImportTransport) ListReleases(
	context.Context,
	platform.RepoRef,
	gitealike.PageOptions,
) ([]gitealike.ReleaseDTO, gitealike.Page, error) {
	return nil, gitealike.Page{}, errors.New("unexpected ListReleases call")
}

func (t *gitealikeImportTransport) ListTags(
	context.Context,
	platform.RepoRef,
	gitealike.PageOptions,
) ([]gitealike.TagDTO, gitealike.Page, error) {
	return nil, gitealike.Page{}, errors.New("unexpected ListTags call")
}

func (t *gitealikeImportTransport) ListStatuses(
	context.Context,
	platform.RepoRef,
	string,
	gitealike.PageOptions,
) ([]gitealike.StatusDTO, gitealike.Page, error) {
	return nil, gitealike.Page{}, errors.New("unexpected ListStatuses call")
}

func TestHandleGetSettings(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	srv, _, _, _ := setupTestServerWithConfigContent(t, `
sync_interval = "5m"
github_token_env = "KENN_FORGE_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "widget"

[[agents]]
key = "codex"
label = "Codex"
command = ["codex", "--full-auto"]
`, &mockGH{})

	rr := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/settings", nil)
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	assert.NotContains(rr.Body.String(), `"default_agent"`)
	assert.Contains(rr.Body.String(), `"launch_targets"`)

	var resp spokeapi.SettingsResponse
	require.NoError(json.NewDecoder(rr.Body).Decode(&resp))
	require.Len(resp.Repos, 1)
	assert.Equal("acme", resp.Repos[0].Owner)
	assert.Equal("repo-acme-widget", resp.Repos[0].PlatformRepoID)
	assert.Equal(1, resp.Repos[0].MatchedRepoCount)
	assert.True(resp.Repos[0].IssuePRReferences)
	assert.Equal("threaded", resp.Activity.ViewMode)
	assert.True(resp.Notifications.Enabled)
	assert.Empty(resp.Terminal.FontFamily)
	assert.Equal(config.DefaultTerminalFontSize, resp.Terminal.FontSize)
	assert.Equal(config.DefaultTerminalScrollback, resp.Terminal.Scrollback)
	assert.InDelta(
		config.DefaultTerminalLineHeight,
		resp.Terminal.LineHeight,
		0.001,
	)
	require.NotNil(resp.Terminal.CursorBlink)
	assert.True(*resp.Terminal.CursorBlink)
	assert.False(resp.Terminal.FontLigatures)
	assert.False(resp.Terminal.HideTmuxStatus)
	require.NotNil(resp.Terminal.Graphics)
	assert.True(*resp.Terminal.Graphics)
	assertDefaultModeVisibility(t, resp.Modes)
	require.Len(resp.Agents, 1)
	assert.Equal("codex", resp.Agents[0].Key)
	assert.Equal([]string{"codex", "--full-auto"}, resp.Agents[0].Command)
	assert.True(resp.ProviderSettingsLoaded, "a Forge that owns its provider settings always has them loaded")
}

func TestHandleUpdateSettingsPersistsMCPAndReportsRestartRequired(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	srv, _, cfgPath, _ := setupTestServerWithConfig(t)

	mcp := config.MCP{Enabled: true, Port: 9092, DiffCacheMB: 256}
	rr := testutil.DoJSON(t, srv, http.MethodPut, "/api/v1/settings", spokeapi.UpdateSettingsRequest{MCP: &spokeapi.McpSettingsUpdate{
		Enabled: new(true), Port: new(9092), DiffCacheMB: new(256),
	}})

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())

	var resp spokeapi.SettingsResponse
	require.NoError(json.NewDecoder(rr.Body).Decode(&resp))
	assert.Equal(config.MCP{
		Enabled:     resp.MCP.Enabled,
		Port:        resp.MCP.Port,
		DiffCacheMB: resp.MCP.DiffCacheMB,
	}, mcp)
	assert.True(resp.MCP.RestartRequired)
	assert.Empty(resp.MCP.ActiveURL)

	reloaded, err := config.Load(cfgPath)
	require.NoError(err)
	assert.Equal(mcp, reloaded.MCP)
}

func TestHandleUpdateSettingsMergesMCPFields(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	srv, _, cfgPath, _ := setupTestServerWithConfigContent(t, `
sync_interval = "5m"
github_token_env = "KENN_FORGE_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[mcp]
port = 9092
diff_cache_mb = 256
`, &mockGH{})

	rr := testutil.DoJSON(t, srv, http.MethodPut, "/api/v1/settings", map[string]any{
		"mcp": map[string]any{"enabled": true},
	})

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	var response spokeapi.SettingsResponse
	require.NoError(json.NewDecoder(rr.Body).Decode(&response))
	assert.True(response.MCP.Enabled)
	assert.Equal(9092, response.MCP.Port)
	assert.Equal(256, response.MCP.DiffCacheMB)

	rr = testutil.DoJSON(t, srv, http.MethodPut, "/api/v1/settings", map[string]any{
		"mcp": map[string]any{"port": 0},
	})

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	response = spokeapi.SettingsResponse{}
	require.NoError(json.NewDecoder(rr.Body).Decode(&response))
	assert.True(response.MCP.Enabled)
	assert.Zero(response.MCP.Port)
	assert.Equal(256, response.MCP.DiffCacheMB)

	reloaded, err := config.Load(cfgPath)
	require.NoError(err)
	assert.Equal(config.MCP{Enabled: true, DiffCacheMB: 256}, reloaded.MCP)
}

func TestHandleUpdateSettingsPersistsRoborevManagedCloneInit(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	srv, _, cfgPath, _ := setupTestServerWithConfig(t)

	rr := testutil.DoJSON(t, srv, http.MethodPut, "/api/v1/settings", spokeapi.UpdateSettingsRequest{
		Roborev: &spokeapi.RoborevSettingsUpdate{InitManagedClones: new(true)},
	})

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())

	var resp spokeapi.SettingsResponse
	require.NoError(json.NewDecoder(rr.Body).Decode(&resp))
	assert.True(resp.Roborev.InitManagedClones)
	reloaded, err := config.Load(cfgPath)
	require.NoError(err)
	assert.True(reloaded.Roborev.InitManagedClones)
}

func TestRepoPresetMutationsAreAtomic(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	srv, _, cfgPath, _ := setupTestServerWithConfig(t)
	first := config.RepoPreset{Name: "Review queue", Repos: []config.RepoPresetRepository{{
		Provider: "github", PlatformHost: "github.com", PlatformRepoID: "R_widgets", RepoPath: "acme/widgets",
	}}}
	second := config.RepoPreset{Name: "Docs", Repos: []config.RepoPresetRepository{{
		Provider: "gitlab", PlatformHost: "git.example.com", PlatformRepoID: "42", RepoPath: "group/docs",
	}}}

	rr := testutil.DoJSON(t, srv, http.MethodPost, "/api/v1/settings/repo-presets", first)
	require.Equal(http.StatusCreated, rr.Code, rr.Body.String())
	rr = testutil.DoJSON(t, srv, http.MethodPost, "/api/v1/settings/repo-presets", second)
	require.Equal(http.StatusCreated, rr.Code, rr.Body.String())

	updatedRepos := []config.RepoPresetRepository{{
		Provider: "github", PlatformHost: "github.com", PlatformRepoID: "R_tools", RepoPath: "acme/tools",
	}}
	rr = testutil.DoJSON(t, srv, http.MethodPut, "/api/v1/settings/repo-presets/Review%20queue", struct {
		Repos []config.RepoPresetRepository `json:"repos"`
	}{Repos: updatedRepos})

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())

	rr = testutil.DoJSON(t, srv, http.MethodDelete, "/api/v1/settings/repo-presets/Review%20queue", nil)
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())

	var resp spokeapi.SettingsResponse
	require.NoError(json.NewDecoder(rr.Body).Decode(&resp))
	assert.Equal([]config.RepoPreset{second}, resp.RepoPresets)

	reloaded, err := config.Load(cfgPath)
	require.NoError(err)
	assert.Equal([]config.RepoPreset{second}, reloaded.RepoPresets)

	rr = testutil.DoJSON(t, srv, http.MethodPost, "/api/v1/settings/repo-presets", second)
	assert.Equal(http.StatusConflict, rr.Code)
}

func TestHandleUpdateSettingsPersistsKataProjectMappings(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	srv, _, cfgPath, _ := setupTestServerWithConfig(t)

	mappings := []config.KataProjectRepoMapping{
		{
			DaemonID:     "desktop",
			ProjectUID:   "project-kata",
			Provider:     "github",
			PlatformHost: "github.com",
			RepoPath:     "acme/widget",
		},
	}
	rr := testutil.DoJSON(
		t, srv, http.MethodPut, "/api/v1/settings",
		spokeapi.UpdateSettingsRequest{KataProjects: &mappings})

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())

	var resp spokeapi.SettingsResponse
	require.NoError(json.NewDecoder(rr.Body).Decode(&resp))
	require.Len(resp.KataProjects, 1)
	assert.Equal(mappings[0], resp.KataProjects[0])

	cfg2, err := config.Load(cfgPath)
	require.NoError(err)
	require.Len(cfg2.KataProjects, 1)
	assert.Equal(mappings[0], cfg2.KataProjects[0])
}

func assertDefaultModeVisibility(t *testing.T, modes config.ModeVisibility) {
	t.Helper()
	assert := assert.New(t)
	assert.True(*modes.Activity)
	assert.True(*modes.Repos)
	assert.False(*modes.Docs)
	assert.False(*modes.Actions)
	assert.True(*modes.Pulls)
	assert.True(*modes.Issues)
	assert.True(*modes.Workspaces)
}

func TestHandleUpdateSettings(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	srv, _, cfgPath, _ := setupTestServerWithConfig(t)

	activity := config.Activity{
		ViewMode:   "threaded",
		TimeRange:  "30d",
		HideClosed: true,
		HideBots:   true,
	}
	issues := config.Issues{HideBots: true}
	autoAssign := true
	defaultSidebarView := "item"
	workspaces := spokeapi.WorkspaceSettingsUpdate{
		AutoAssignOnCreate:     &autoAssign,
		ShowAgentStatusInLists: new(true),
		DefaultSidebarView:     &defaultSidebarView,
	}
	terminal := config.Terminal{
		FontFamily:       "\"Fira Code\", monospace",
		FontSize:         16,
		Scrollback:       5000,
		LineHeight:       1.15,
		CursorBlink:      new(true),
		FontLigatures:    true,
		HideTmuxStatus:   true,
		Graphics:         new(true),
		TmuxMouse:        new(false),
		RetainedSessions: new(4),
	}
	body := spokeapi.UpdateSettingsRequest{
		Activity:   &activity,
		Issues:     &issues,
		Workspaces: &workspaces,
		Terminal:   &terminal,
	}
	rr := testutil.DoJSON(
		t, srv, http.MethodPut, "/api/v1/settings", body)

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())

	// Verify persisted to disk.
	cfg2, err := config.Load(cfgPath)
	require.NoError(err)
	assert.Equal("threaded", cfg2.Activity.ViewMode)
	assert.Equal("30d", cfg2.Activity.TimeRange)
	assert.True(cfg2.Issues.HideBots)
	assert.True(cfg2.Workspaces.AutoAssignOnCreate)
	assert.True(cfg2.Workspaces.ShowAgentStatusInLists)
	assert.Equal("item", cfg2.Workspaces.DefaultSidebarView)
	assert.Equal("\"Fira Code\", monospace", cfg2.Terminal.FontFamily)
	assert.Equal(16, cfg2.Terminal.FontSize)
	assert.Equal(5000, cfg2.Terminal.Scrollback)
	assert.InDelta(1.15, cfg2.Terminal.LineHeight, 0.001)
	assert.True(cfg2.Terminal.FontLigatures)
	assert.True(cfg2.Terminal.HideTmuxStatus)
	require.NotNil(cfg2.Terminal.TmuxMouse)
	assert.False(*cfg2.Terminal.TmuxMouse)
	require.NotNil(cfg2.Terminal.Graphics)
	assert.True(*cfg2.Terminal.Graphics)
	require.NotNil(cfg2.Terminal.RetainedSessions)
	assert.Equal(4, *cfg2.Terminal.RetainedSessions)
}

func TestHandleUpdateSettingsDisablesNativeStackProjectionImmediately(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	srv, database, _, _ := setupTestServerWithConfigContent(t, `
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

	before, err := client.HTTP.GetPullStackWithResponse(ctx, &generated.GetPullStackRequestOptions{PathParams: &generated.GetPullStackPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(10)}})
	require.NoError(err)
	require.NotNil(before.JSON200)
	require.NotNil(before.JSON200.Members)
	assert.Equal([]int64{11, 10}, stackMemberNumbers(before.JSON200.Members))

	disabled := config.PullRequests{}
	rr := testutil.DoJSON(t, srv, http.MethodPut, "/api/v1/settings", spokeapi.UpdateSettingsRequest{
		PullRequests: &disabled,
	})

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	after, err := client.HTTP.GetPullStackWithResponse(ctx, &generated.GetPullStackRequestOptions{PathParams: &generated.GetPullStackPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(10)}})
	require.NoError(err)
	require.NotNil(after.JSON200)
	require.NotNil(after.JSON200.Members)
	assert.Equal([]int64{10, 11}, stackMemberNumbers(after.JSON200.Members))
}

func TestHandleUpdateTerminalSettingsPreservesActivity(t *testing.T) {
	assert := assert.New(t)
	srv, _, cfgPath, _ := setupTestServerWithConfigContent(t, `
sync_interval = "5m"
github_token_env = "KENN_FORGE_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "widget"

[activity]
view_mode = "flat"
time_range = "30d"
hide_closed = true
hide_bots = true

[modes]
docs = false
`, &mockGH{})

	terminal := config.Terminal{
		FontFamily:       "\"Iosevka Term\", monospace",
		FontSize:         15,
		Scrollback:       2000,
		LetterSpacing:    1,
		CursorBlink:      new(true),
		Graphics:         new(true),
		TmuxMouse:        new(true),
		RetainedSessions: new(config.DefaultTerminalRetainedSessions),
	}
	body := spokeapi.UpdateSettingsRequest{
		Terminal: &terminal,
	}
	rr := testutil.DoJSON(
		t, srv, http.MethodPut, "/api/v1/settings", body)

	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	cfg2, err := config.Load(cfgPath)
	require.NoError(t, err)
	assert.Equal("flat", cfg2.Activity.ViewMode)
	assert.Equal("30d", cfg2.Activity.TimeRange)
	assert.True(cfg2.Activity.HideClosed)
	assert.True(cfg2.Activity.HideBots)
	assert.Equal("\"Iosevka Term\", monospace", cfg2.Terminal.FontFamily)
	assert.Equal(15, cfg2.Terminal.FontSize)
	assert.Equal(2000, cfg2.Terminal.Scrollback)
	assert.Equal(1, cfg2.Terminal.LetterSpacing)
	assert.False(*cfg2.Modes.Docs)
}

func TestHandleUpdateSettingsPersistsAgents(t *testing.T) {
	assert := assert.New(t)
	srv, _, cfgPath, _ := setupTestServerWithConfig(t)
	disabled := false
	agents := []config.Agent{{
		Key:     "codex",
		Label:   "Codex with flags",
		Command: []string{"/opt/codex", "--full-auto", "--search"},
	}, {
		Key:     "notes",
		Label:   "Notes",
		Command: []string{"/usr/local/bin/notes-agent", "--draft"},
	}, {
		Key:     "claude",
		Label:   "Claude",
		Enabled: &disabled,
	}}

	body := spokeapi.UpdateSettingsRequest{Agents: &agents}
	rr := testutil.DoJSON(
		t, srv, http.MethodPut, "/api/v1/settings", body)

	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	cfg2, err := config.Load(cfgPath)
	require.NoError(t, err)
	require.Len(t, cfg2.Agents, 3)
	assert.Equal("codex", cfg2.Agents[0].Key)
	assert.Equal(
		[]string{"/opt/codex", "--full-auto", "--search"},
		cfg2.Agents[0].Command,
	)
	assert.Equal("notes", cfg2.Agents[1].Key)
	assert.False(cfg2.Agents[2].EnabledOrDefault())
}

func TestHandleUpdateSettingsInvalid(t *testing.T) {
	srv, _, cfgPath, _ := setupTestServerWithConfig(t)

	activity := config.Activity{
		ViewMode:  "kanban",
		TimeRange: "7d",
	}
	body := spokeapi.UpdateSettingsRequest{
		Activity: &activity,
	}
	rr := testutil.DoJSON(
		t, srv, http.MethodPut, "/api/v1/settings", body)

	require.Equal(t, http.StatusUnprocessableEntity, rr.Code, rr.Body.String())

	// Verify config was NOT modified (rollback).
	cfg2, err := config.Load(cfgPath)
	require.NoError(t, err)
	assert.Equal(t, "threaded", cfg2.Activity.ViewMode)
}

func TestHandleAddRepoAcceptsArchivedRepo(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	mock := &mockGH{
		getRepositoryFn: func(
			_ context.Context, owner, repo string,
		) (*gh.Repository, error) {
			return &gh.Repository{
				Name:     new(repo),
				Owner:    &gh.User{Login: new(owner)},
				Archived: new(true),
			}, nil
		},
	}
	srv, _, cfgPath, syncer := setupTestServerWithConfigContent(t, `
sync_interval = "5m"
github_token_env = "KENN_FORGE_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "widget"
`, mock)

	rr := testutil.DoJSON(t, srv, http.MethodPost, "/api/v1/repos", map[string]string{
		"provider": "github",
		"host":     "github.com",
		"owner":    "other-org",
		"name":     "frozen",
	})

	require.Equal(http.StatusCreated, rr.Code, rr.Body.String())

	cfg2, err := config.Load(cfgPath)
	require.NoError(err)
	require.Len(cfg2.Repos, 2)
	assert.True(syncer.IsTrackedRepo("other-org", "frozen"),
		"archived repo is tracked archive-only after add")
}

func TestHandleAddRepoRefreshesArchivedStateForTrackedRepo(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	archivedNow := atomic.Bool{}
	mock := &mockGH{
		getRepositoryFn: func(
			_ context.Context, owner, repo string,
		) (*gh.Repository, error) {
			return &gh.Repository{
				Name:     new(repo),
				Owner:    &gh.User{Login: new(owner)},
				Archived: new(archivedNow.Load()),
			}, nil
		},
		listReposByOwnerFn: func(
			_ context.Context, owner string,
		) ([]*gh.Repository, error) {
			return []*gh.Repository{{
				Name:     new("widget"),
				Owner:    &gh.User{Login: new(owner)},
				Archived: new(archivedNow.Load()),
			}}, nil
		},
	}
	srv, _, _, syncer := setupTestServerWithConfigContent(t, `
sync_interval = "5m"
github_token_env = "KENN_FORGE_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "*"
`, mock)
	require.True(syncer.IsTrackedRepo("acme", "widget"))

	// The repo gets archived on the provider; adding an overlapping exact
	// entry must refresh the tracked ref, not keep the stale live one.
	archivedNow.Store(true)
	rr := testutil.DoJSON(t, srv, http.MethodPost, "/api/v1/repos", map[string]string{
		"provider": "github",
		"host":     "github.com",
		"owner":    "acme",
		"name":     "widget",
	})

	require.Equal(http.StatusCreated, rr.Code, rr.Body.String())

	assert.True(trackedRepoArchived(syncer, "acme", "widget"),
		"overlapping add must apply fresh archived state")
}

func TestHandleRefreshRepoUpdatesArchivedStateForOverlappingEntries(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	archivedNow := atomic.Bool{}
	mock := &mockGH{
		getRepositoryFn: func(
			_ context.Context, owner, repo string,
		) (*gh.Repository, error) {
			return &gh.Repository{
				Name:     new(repo),
				Owner:    &gh.User{Login: new(owner)},
				Archived: new(archivedNow.Load()),
			}, nil
		},
		listReposByOwnerFn: func(
			_ context.Context, owner string,
		) ([]*gh.Repository, error) {
			return []*gh.Repository{{
				Name:     new("widget"),
				Owner:    &gh.User{Login: new(owner)},
				Archived: new(archivedNow.Load()),
			}}, nil
		},
	}
	srv, _, _, syncer := setupTestServerWithConfigContent(t, `
sync_interval = "5m"
github_token_env = "KENN_FORGE_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "widget"

[[repos]]
owner = "acme"
name = "*"
`, mock)
	require.True(syncer.IsTrackedRepo("acme", "widget"))
	require.False(trackedRepoArchived(syncer, "acme", "widget"))

	// widget matches both the exact entry and the glob; a refresh after the
	// provider archives it must update the tracked ref even though the
	// exact entry keeps it in the tracked set.
	archivedNow.Store(true)
	rr := testutil.DoJSON(
		t, srv, http.MethodPost,
		"/api/v1/repo/gh/acme/*/refresh", nil)

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())

	assert.True(trackedRepoArchived(syncer, "acme", "widget"),
		"glob refresh must apply fresh archived state to overlapping repos")
	assert.Equal("acme/widget", trackedRepoProvenancePath(syncer, "acme", "widget"),
		"glob refresh through the API must keep the exact entry's provenance")
}

func trackedRepoProvenancePath(syncer *ghclient.Syncer, owner, name string) string {
	for _, repo := range syncer.TrackedRepos() {
		if strings.EqualFold(repo.Owner, owner) && strings.EqualFold(repo.Name, name) {
			return repo.ConfiguredRepoPath
		}
	}
	return ""
}

func TestHandleRefreshRepoStopsLiveLanesForArchivedRepo(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	archivedNow := atomic.Bool{}
	var detailRepos sync.Map
	detailErr := errors.New("detail fetch short-circuited")
	mock := &mockGH{
		getPullRequestIfChangedFn: func(
			_ context.Context, _, repo string, _ int, _ string,
		) (*gh.PullRequest, string, bool, error) {
			detailRepos.Store(repo, true)
			return nil, "", false, detailErr
		},
		getPullRequestFn: func(
			_ context.Context, _, repo string, _ int,
		) (*gh.PullRequest, error) {
			detailRepos.Store(repo, true)
			return nil, detailErr
		},
		getRepositoryFn: func(
			_ context.Context, owner, repo string,
		) (*gh.Repository, error) {
			return &gh.Repository{
				NodeID:   new("repo-acme-" + repo),
				Name:     new(repo),
				Owner:    &gh.User{Login: new(owner)},
				Archived: new(repo == "widget" && archivedNow.Load()),
			}, nil
		},
		listReposByOwnerFn: func(
			_ context.Context, owner string,
		) ([]*gh.Repository, error) {
			return []*gh.Repository{
				{
					NodeID:   new("repo-acme-widget"),
					Name:     new("widget"),
					Owner:    &gh.User{Login: new(owner)},
					Archived: new(archivedNow.Load()),
				},
				{
					NodeID:   new("repo-acme-tools"),
					Name:     new("tools"),
					Owner:    &gh.User{Login: new(owner)},
					Archived: new(false),
				},
			}, nil
		},
	}
	srv, database, _, syncer := setupTestServerWithConfigContent(t, `
sync_interval = "5m"
github_token_env = "KENN_FORGE_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "widget"

[[repos]]
owner = "acme"
name = "*"
`, mock)
	recentActivity := time.Now().UTC().Add(-10 * time.Minute)
	for i, name := range []string{"widget", "tools"} {
		repoID, err := database.UpsertRepo(t.Context(), db.RepoIdentity{
			Platform: "github", PlatformHost: "github.com",
			PlatformRepoID: "repo-acme-" + name, Owner: "acme", Name: name,
		})
		require.NoError(err)
		_, err = database.UpsertMergeRequest(t.Context(), &db.MergeRequest{
			RepoID: repoID, PlatformID: int64(i + 1), Number: i + 1,
			Title: "PR", Author: "octo", State: db.MergeRequestStateOpen,
			HeadBranch: "feature", BaseBranch: "main",
			CreatedAt: recentActivity.Add(-24 * time.Hour),
			UpdatedAt: recentActivity, LastActivityAt: recentActivity,
		})
		require.NoError(err)
	}
	syncer.SetActiveMRWindow(4 * time.Hour)
	require.True(syncer.IsTrackedRepo("acme", "widget"))
	// Stop background sync loops so the refresh-triggered async full sync
	// cannot populate the lane recorders; each lane runs synchronously below.
	syncer.Stop()

	// The provider archives widget; the refresh applies the transition and
	// the live lanes must stop touching it while the live sibling keeps
	// syncing.
	archivedNow.Store(true)
	rr := testutil.DoJSON(
		t, srv, http.MethodPost,
		"/api/v1/repo/gh/acme/*/refresh", nil)

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	require.True(trackedRepoArchived(syncer, "acme", "widget"))

	require.NoError(syncer.SyncNotifications(t.Context()))
	toolsWatermark, err := database.GetNotificationSyncWatermark(
		t.Context(), "github", "github.com", "acme", "tools",
	)
	require.NoError(err)
	assert.NotNil(toolsWatermark, "live repo notifications should sync")
	widgetWatermark, err := database.GetNotificationSyncWatermark(
		t.Context(), "github", "github.com", "acme", "widget",
	)
	require.NoError(err)
	assert.Nil(widgetWatermark,
		"archived repo must not receive notification polling after refresh")

	// Clear anything recorded by earlier phases so the assertions below
	// reflect the watched-MR lane exclusively.
	detailRepos.Range(func(key, _ any) bool {
		detailRepos.Delete(key)
		return true
	})
	syncer.SyncWatchedMRs(t.Context())
	_, detailTools := detailRepos.Load("tools")
	assert.True(detailTools, "live repo open MR should fast-sync")
	_, detailWidget := detailRepos.Load("widget")
	assert.False(detailWidget,
		"archived repo open MRs must not enter fast sync after refresh")
}

func trackedRepoArchived(syncer *ghclient.Syncer, owner, name string) bool {
	for _, repo := range syncer.TrackedRepos() {
		if strings.EqualFold(repo.Owner, owner) && strings.EqualFold(repo.Name, name) {
			return repo.Archived
		}
	}
	return false
}

func TestHandleDeleteRepoPreservesKataProjectMappings(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	srv, _, cfgPath, _ := setupTestServerWithConfig(t)

	addBody := map[string]string{
		"provider": "github",
		"host":     "github.com",
		"owner":    "other-org",
		"name":     "other-repo",
	}
	addRR := testutil.DoJSON(
		t, srv, http.MethodPost, "/api/v1/repos", addBody)

	require.Equal(http.StatusCreated, addRR.Code, addRR.Body.String())

	mappings := []config.KataProjectRepoMapping{
		{
			DaemonID:     "desktop",
			ProjectUID:   "project-widget",
			Provider:     "github",
			PlatformHost: "github.com",
			RepoPath:     "acme/widget",
		},
		{
			DaemonID:     "desktop",
			ProjectUID:   "project-other",
			Provider:     "github",
			PlatformHost: "github.com",
			RepoPath:     "other-org/other-repo",
		},
	}
	updateRR := testutil.DoJSON(
		t, srv, http.MethodPut, "/api/v1/settings",
		spokeapi.UpdateSettingsRequest{KataProjects: &mappings})

	require.Equal(http.StatusOK, updateRR.Code, updateRR.Body.String())

	deleteRR := testutil.DoJSON(
		t, srv, http.MethodDelete,
		"/api/v1/repo/gh/acme/widget", nil)

	require.Equal(http.StatusNoContent, deleteRR.Code, deleteRR.Body.String())

	cfg2, err := config.Load(cfgPath)
	require.NoError(err)
	require.Len(cfg2.Repos, 1)
	assert.Equal("other-org", cfg2.Repos[0].Owner)
	require.Len(cfg2.KataProjects, 2)
	assert.Equal("project-widget", cfg2.KataProjects[0].ProjectUID)
	assert.Equal("acme/widget", cfg2.KataProjects[0].RepoPath)
}

func TestGetSettingsWithoutPersistence(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	dir := t.TempDir()
	database := dbtest.Open(t)

	cfg := &config.Config{
		SyncInterval:   "5m",
		GitHubTokenEnv: "UNUSED",
		Host:           "127.0.0.1",
		Port:           8091,
		BasePath:       "/",
		DataDir:        dir,
		Repos: []config.Repo{
			{Owner: "acme", Name: "widget"},
		},
		Activity: config.Activity{
			ViewMode:  "flat",
			TimeRange: "30d",
		},
	}
	mock := &mockGH{}
	syncer := ghclient.NewSyncer(map[string]ghclient.Client{"github.com": mock}, database, nil, nil, time.Minute, nil, nil)
	t.Cleanup(syncer.Stop)
	srv := server.New(database, syncer, nil, "/", cfg, server.ServerOptions{})

	// GET /settings should work (read-only).
	rr := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/settings", nil)
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())

	var resp spokeapi.SettingsResponse
	require.NoError(json.NewDecoder(rr.Body).Decode(&resp))
	require.Len(resp.Repos, 1)
	assert.Equal("acme", resp.Repos[0].Owner)
	assert.Equal("flat", resp.Activity.ViewMode)

	// Mutations should be rejected (no cfgPath).
	mutRR := testutil.DoJSON(t, srv, http.MethodPut, "/api/v1/settings",
		spokeapi.UpdateSettingsRequest{Activity: &cfg.Activity})

	assert.Equal(http.StatusNotFound, mutRR.Code)

	addRR := testutil.DoJSON(t, srv, http.MethodPost, "/api/v1/repos",
		map[string]string{
			"provider": "github",
			"host":     "github.com",
			"owner":    "x",
			"name":     "y",
		})

	assert.Equal(http.StatusNotFound, addRR.Code)

	delRR := testutil.DoJSON(t, srv, http.MethodDelete,
		"/api/v1/repo/gh/acme/widget", nil)

	assert.Equal(http.StatusNotFound, delRR.Code)
}

func TestDetailSettingsReadPersistAndRejectInvalidLimit(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	srv, _, cfgPath, _ := setupTestServerWithConfig(t)

	rr := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/settings", nil)
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	var initial spokeapi.SettingsResponse
	require.NoError(json.NewDecoder(rr.Body).Decode(&initial))
	assert.Equal(config.DefaultInitialTimelineEntryLimit, initial.Detail.InitialTimelineEntryLimit)

	updated := config.Detail{InitialTimelineEntryLimit: 80}
	rr = testutil.DoJSON(t, srv, http.MethodPut, "/api/v1/settings", spokeapi.UpdateSettingsRequest{Detail: &updated})
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	var saved spokeapi.SettingsResponse
	require.NoError(json.NewDecoder(rr.Body).Decode(&saved))
	assert.Equal(80, saved.Detail.InitialTimelineEntryLimit)
	persisted, err := config.Load(cfgPath)
	require.NoError(err)
	assert.Equal(80, persisted.Detail.InitialTimelineEntryLimit)

	invalid := config.Detail{InitialTimelineEntryLimit: 9}
	rr = testutil.DoJSON(t, srv, http.MethodPut, "/api/v1/settings", spokeapi.UpdateSettingsRequest{Detail: &invalid})
	require.Equal(http.StatusUnprocessableEntity, rr.Code, rr.Body.String())
	persisted, err = config.Load(cfgPath)
	require.NoError(err)
	assert.Equal(80, persisted.Detail.InitialTimelineEntryLimit)
}

func TestHandleGetSettingsIncludesGlobCounts(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	mock := &mockGH{
		listReposByOwnerFn: func(
			_ context.Context, owner string,
		) ([]*gh.Repository, error) {
			return []*gh.Repository{
				{
					Name:     new("kenn-forge"),
					Owner:    &gh.User{Login: new(owner)},
					Archived: new(false),
				},
				{
					Name:     new("globber"),
					Owner:    &gh.User{Login: new(owner)},
					Archived: new(false),
				},
				{
					Name:     new("archived"),
					Owner:    &gh.User{Login: new(owner)},
					Archived: new(true),
				},
			}, nil
		},
	}
	srv, _, _, _ := setupTestServerWithConfigContent(t, `
sync_interval = "5m"
github_token_env = "KENN_FORGE_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "roborev-dev"
name = "*"
`, mock)

	rr := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/settings", nil)
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())

	var resp spokeapi.SettingsResponse
	require.NoError(json.NewDecoder(rr.Body).Decode(&resp))
	require.Len(resp.Repos, 1)
	assert.Equal("roborev-dev", resp.Repos[0].Owner)
	assert.Equal("*", resp.Repos[0].Name)
	assert.True(resp.Repos[0].IsGlob)
	assert.Equal(3, resp.Repos[0].MatchedRepoCount,
		"archived repos count as archive-only glob matches")
}

func TestHandleRefreshRepoRebuildsExpandedSyncSet(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	mock := &mockGH{
		listReposByOwnerFn: func(
			_ context.Context, owner string,
		) ([]*gh.Repository, error) {
			return []*gh.Repository{
				{
					Name:     new("kenn-forge"),
					Owner:    &gh.User{Login: new(owner)},
					Archived: new(false),
				},
				{
					Name:     new("globber"),
					Owner:    &gh.User{Login: new(owner)},
					Archived: new(false),
				},
				{
					Name:     new("archived"),
					Owner:    &gh.User{Login: new(owner)},
					Archived: new(true),
				},
			}, nil
		},
	}
	srv, _, _, syncer := setupTestServerWithConfigContent(t, `
sync_interval = "5m"
github_token_env = "KENN_FORGE_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "roborev-dev"
name = "*"
`, mock)

	rr := testutil.DoJSON(
		t, srv, http.MethodPost,
		"/api/v1/repo/gh/roborev-dev/*/refresh", nil)

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())

	var resp spokeapi.SettingsResponse
	require.NoError(json.NewDecoder(rr.Body).Decode(&resp))
	require.Equal(3, resp.Repos[0].MatchedRepoCount)
	assert.True(syncer.IsTrackedRepo("roborev-dev", "kenn-forge"))
	assert.True(syncer.IsTrackedRepo("roborev-dev", "globber"))
	assert.True(syncer.IsTrackedRepo("roborev-dev", "archived"),
		"archived repos stay tracked as archive-only")
}

func TestHandleRefreshRepoPersistsExpandedReposBeforeAsyncSync(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	includeRefreshRepo := atomic.Bool{}
	mock := &mockGH{
		listReposByOwnerFn: func(
			_ context.Context, owner string,
		) ([]*gh.Repository, error) {
			repos := []*gh.Repository{
				{
					ID:       new(int64(101)),
					NodeID:   new("repo-101"),
					Name:     new("kenn-forge"),
					Owner:    &gh.User{Login: new(owner)},
					Archived: new(false),
				},
				{
					ID:       new(int64(102)),
					NodeID:   new("repo-102"),
					Name:     new("archived"),
					Owner:    &gh.User{Login: new(owner)},
					Archived: new(true),
				},
			}
			if includeRefreshRepo.Load() {
				repos = append(repos, &gh.Repository{
					ID:       new(int64(103)),
					NodeID:   new("repo-103"),
					Name:     new("review-bot"),
					Owner:    &gh.User{Login: new(owner)},
					Archived: new(false),
				})
			}
			return repos, nil
		},
	}
	srv, database, _, syncer := setupTestServerWithConfigContent(t, `
sync_interval = "5m"
github_token_env = "KENN_FORGE_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "roborev-dev"
name = "*"
`, mock)
	syncer.Stop()
	includeRefreshRepo.Store(true)

	rr := testutil.DoJSON(
		t, srv, http.MethodPost,
		"/api/v1/repo/gh/roborev-dev/*/refresh", nil)

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())

	repos, err := database.ListRepos(t.Context())
	require.NoError(err)
	names := make([]string, 0, len(repos))
	for _, repo := range repos {
		if repo.Owner == "roborev-dev" {
			names = append(names, repo.Name)
		}
	}
	assert.ElementsMatch([]string{"kenn-forge", "archived", "review-bot"}, names)
}

func TestHandleRefreshRepoKeepsReposMatchedByOtherConfigEntries(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	mock := &mockGH{
		getRepositoryFn: func(
			_ context.Context, owner, repo string,
		) (*gh.Repository, error) {
			return &gh.Repository{
				Name:     new(repo),
				Owner:    &gh.User{Login: new(owner)},
				Archived: new(false),
			}, nil
		},
		listReposByOwnerFn: func(
			_ context.Context, owner string,
		) ([]*gh.Repository, error) {
			return []*gh.Repository{
				{
					Name:     new("kenn-forge"),
					Owner:    &gh.User{Login: new(owner)},
					Archived: new(false),
				},
			}, nil
		},
	}
	srv, _, _, syncer := setupTestServerWithConfigContent(t, `
sync_interval = "5m"
github_token_env = "KENN_FORGE_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "roborev-dev"
name = "*"

[[repos]]
owner = "roborev-dev"
name = "worker"
`, mock)

	rr := testutil.DoJSON(
		t, srv, http.MethodPost,
		"/api/v1/repo/gh/roborev-dev/*/refresh", nil)

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())

	var resp spokeapi.SettingsResponse
	require.NoError(json.NewDecoder(rr.Body).Decode(&resp))
	require.Len(resp.Repos, 2)
	assert.True(syncer.IsTrackedRepo("roborev-dev", "kenn-forge"))
	assert.True(syncer.IsTrackedRepo("roborev-dev", "worker"))
}

func TestHandleDeleteRepoRebuildsExpandedSetFromRemainingPatterns(t *testing.T) {
	mock := &mockGH{
		getRepositoryFn: func(
			_ context.Context, owner, repo string,
		) (*gh.Repository, error) {
			return &gh.Repository{
				Name:     new(repo),
				Owner:    &gh.User{Login: new(owner)},
				Archived: new(false),
			}, nil
		},
		listReposByOwnerFn: func(
			_ context.Context, owner string,
		) ([]*gh.Repository, error) {
			return []*gh.Repository{
				{
					Name:     new("kenn-forge"),
					Owner:    &gh.User{Login: new(owner)},
					Archived: new(false),
				},
			}, nil
		},
	}
	srv, _, _, syncer := setupTestServerWithConfigContent(t, `
sync_interval = "5m"
github_token_env = "KENN_FORGE_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "roborev-dev"
name = "*"

[[repos]]
owner = "roborev-dev"
name = "tools"
`, mock)

	rr := testutil.DoJSON(
		t, srv, http.MethodDelete,
		"/api/v1/repo/gh/roborev-dev/*", nil)

	require.Equal(t, http.StatusNoContent, rr.Code, rr.Body.String())
	assert.True(t, syncer.IsTrackedRepo("roborev-dev", "tools"))
	assert.False(t, syncer.IsTrackedRepo("roborev-dev", "kenn-forge"))
}

func TestHandleDeleteGlobKeepsRenamedExactEntryRepo(t *testing.T) {
	mock := &mockGH{
		getRepositoryFn: func(
			_ context.Context, owner, repo string,
		) (*gh.Repository, error) {
			return &gh.Repository{
				Name:  new(repo),
				Owner: &gh.User{Login: new(owner)},
			}, nil
		},
		listReposByOwnerFn: func(
			_ context.Context, owner string,
		) ([]*gh.Repository, error) {
			return []*gh.Repository{{
				Name:  new("tools"),
				Owner: &gh.User{Login: new(owner)},
			}}, nil
		},
	}
	srv, _, _, syncer := setupTestServerWithConfigContent(t, `
sync_interval = "5m"
github_token_env = "KENN_FORGE_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "tools"

[[repos]]
owner = "acme"
name = "*"
`, mock)

	// The exact entry's repo was renamed provider-side; only provenance
	// still ties the tracked ref to the entry. A second repo matches only
	// the glob.
	syncer.SetRepos([]ghclient.RepoRef{
		{
			Platform: platform.KindGitHub, Owner: "acme", Name: "tools-new",
			PlatformHost: "github.com", RepoPath: "acme/tools-new",
			PlatformExternalID: "repo-x", ConfiguredRepoPath: "acme/tools",
		},
		{
			Platform: platform.KindGitHub, Owner: "acme", Name: "widgets",
			PlatformHost: "github.com", RepoPath: "acme/widgets",
			PlatformExternalID: "repo-w",
		},
	})

	rr := testutil.DoJSON(
		t, srv, http.MethodDelete,
		"/api/v1/repo/gh/acme/*", nil)

	require.Equal(t, http.StatusNoContent, rr.Code, rr.Body.String())
	assert.True(t, syncer.IsTrackedRepo("acme", "tools-new"),
		"deleting the glob must keep the renamed repo its exact entry still claims")
	assert.False(t, syncer.IsTrackedRepo("acme", "widgets"))
}

func TestHandleDeleteExactEntryClearsProvenanceOnGlobKeptRepo(t *testing.T) {
	mock := &mockGH{
		getRepositoryFn: func(
			_ context.Context, owner, repo string,
		) (*gh.Repository, error) {
			return &gh.Repository{
				Name:  new(repo),
				Owner: &gh.User{Login: new(owner)},
			}, nil
		},
		listReposByOwnerFn: func(
			_ context.Context, owner string,
		) ([]*gh.Repository, error) {
			return []*gh.Repository{{
				Name:  new("tools-new"),
				Owner: &gh.User{Login: new(owner)},
			}}, nil
		},
	}
	srv, _, _, syncer := setupTestServerWithConfigContent(t, `
sync_interval = "5m"
github_token_env = "KENN_FORGE_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "tools"

[[repos]]
owner = "acme"
name = "*"
`, mock)

	syncer.SetRepos([]ghclient.RepoRef{{
		Platform: platform.KindGitHub, Owner: "acme", Name: "tools-new",
		PlatformHost: "github.com", RepoPath: "acme/tools-new",
		PlatformExternalID: "repo-x", ConfiguredRepoPath: "acme/tools",
	}})

	// Removing the exact entry keeps the repo through the glob, but its
	// provenance now points at a config entry that no longer exists and
	// must not survive to claim a future entry with the same path.
	rr := testutil.DoJSON(
		t, srv, http.MethodDelete,
		"/api/v1/repo/gh/acme/tools", nil)

	require.Equal(t, http.StatusNoContent, rr.Code, rr.Body.String())
	require.True(t, syncer.IsTrackedRepo("acme", "tools-new"))
	assert.Empty(t, trackedRepoProvenancePath(syncer, "acme", "tools-new"),
		"provenance must clear when its exact entry is removed")
}

func TestHandleDeleteExactEntryIgnoresSamePathOnOtherHost(t *testing.T) {
	mock := &mockGH{
		getRepositoryFn: func(
			_ context.Context, owner, repo string,
		) (*gh.Repository, error) {
			return &gh.Repository{
				Name:  new(repo),
				Owner: &gh.User{Login: new(owner)},
			}, nil
		},
		listReposByOwnerFn: func(
			_ context.Context, owner string,
		) ([]*gh.Repository, error) {
			return []*gh.Repository{{
				Name:  new("tools-new"),
				Owner: &gh.User{Login: new(owner)},
			}}, nil
		},
	}
	srv, _, _, syncer := setupTestServerWithConfigContent(t, `
sync_interval = "5m"
github_token_env = "KENN_FORGE_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "tools"

[[repos]]
owner = "acme"
name = "*"

[[repos]]
owner = "acme"
name = "tools"
platform_host = "ghe.example.com"
`, mock)

	syncer.SetRepos([]ghclient.RepoRef{{
		Platform: platform.KindGitHub, Owner: "acme", Name: "tools-new",
		PlatformHost: "github.com", RepoPath: "acme/tools-new",
		PlatformExternalID: "repo-x", ConfiguredRepoPath: "acme/tools",
	}})

	// The remaining acme/tools entry lives on a different host; it cannot
	// keep the deleted github.com entry's provenance alive.
	rr := testutil.DoJSON(
		t, srv, http.MethodDelete,
		"/api/v1/repo/gh/acme/tools", nil)

	require.Equal(t, http.StatusNoContent, rr.Code, rr.Body.String())
	require.True(t, syncer.IsTrackedRepo("acme", "tools-new"))
	assert.Empty(t, trackedRepoProvenancePath(syncer, "acme", "tools-new"),
		"an entry with the same path on another host must not retain provenance")
}

func TestHandleDeleteExactEntryIgnoresSamePathOnOtherProvider(t *testing.T) {
	mock := &mockGH{
		getRepositoryFn: func(
			_ context.Context, owner, repo string,
		) (*gh.Repository, error) {
			return &gh.Repository{
				Name:  new(repo),
				Owner: &gh.User{Login: new(owner)},
			}, nil
		},
		listReposByOwnerFn: func(
			_ context.Context, owner string,
		) ([]*gh.Repository, error) {
			return []*gh.Repository{{
				Name:  new("tools-new"),
				Owner: &gh.User{Login: new(owner)},
			}}, nil
		},
	}
	srv, _, _, syncer := setupTestServerWithConfigContent(t, `
sync_interval = "5m"
github_token_env = "KENN_FORGE_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "tools"

[[repos]]
owner = "acme"
name = "*"

[[repos]]
platform = "gitlab"
platform_host = "github.com"
owner = "acme"
name = "tools"
`, mock)

	syncer.SetRepos([]ghclient.RepoRef{{
		Platform: platform.KindGitHub, Owner: "acme", Name: "tools-new",
		PlatformHost: "github.com", RepoPath: "acme/tools-new",
		PlatformExternalID: "repo-x", ConfiguredRepoPath: "acme/tools",
	}})

	// The remaining acme/tools entry shares the host but belongs to a
	// different provider; it cannot keep the deleted GitHub entry's
	// provenance alive.
	rr := testutil.DoJSON(
		t, srv, http.MethodDelete,
		"/api/v1/repo/gh/acme/tools", nil)

	require.Equal(t, http.StatusNoContent, rr.Code, rr.Body.String())
	require.True(t, syncer.IsTrackedRepo("acme", "tools-new"))
	assert.Empty(t, trackedRepoProvenancePath(syncer, "acme", "tools-new"),
		"an entry with the same path on another provider must not retain provenance")
}

func TestHandleDeleteRepoUsesProviderHostQuery(t *testing.T) {
	require := require.New(t)
	srv, _, _, _ := setupTestServerWithConfigContent(t, `
sync_interval = "5m"
github_token_env = "KENN_FORGE_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "widget"

[[repos]]
platform = "gitlab"
platform_host = "gitlab.com"
owner = "acme"
name = "widget"
`, &mockGH{})

	rr := testutil.DoJSON(
		t, srv, http.MethodDelete,
		"/api/v1/host/gitlab.com/repo/gl/acme/widget", nil)

	require.Equal(http.StatusNoContent, rr.Code, rr.Body.String())

	settingsRR := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/settings", nil)
	require.Equal(http.StatusOK, settingsRR.Code, settingsRR.Body.String())
	var resp spokeapi.SettingsResponse
	require.NoError(json.NewDecoder(settingsRR.Body).Decode(&resp))
	require.Len(resp.Repos, 1)
	assert.Equal(t, "github", resp.Repos[0].Provider)
	assert.Equal(t, "github.com", resp.Repos[0].PlatformHost)
}

func TestRefreshRepoPreservesExistingWhenResolutionFails(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	fail := true
	mock := &mockGH{
		listReposByOwnerFn: func(
			_ context.Context, owner string,
		) ([]*gh.Repository, error) {
			if fail {
				return nil, errors.New("boom")
			}
			return []*gh.Repository{{
				Name:     new("kenn-forge"),
				Owner:    &gh.User{Login: new(owner)},
				Archived: new(false),
			}}, nil
		},
	}
	srv, _, _, syncer := setupTestServerWithConfigContent(t, `
sync_interval = "5m"
github_token_env = "KENN_FORGE_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "roborev-dev"
name = "*"
`, mock)
	// Prime the syncer with a previously resolved match so we can
	// verify it survives a failed refresh.
	syncer.SetRepos([]ghclient.RepoRef{{
		Owner:        "roborev-dev",
		Name:         "kenn-forge",
		PlatformHost: "github.com",
	}})

	rr := testutil.DoJSON(
		t, srv, http.MethodPost,
		"/api/v1/repo/gh/roborev-dev/*/refresh", nil)

	require.Equal(http.StatusBadGateway, rr.Code, rr.Body.String())
	assert.True(syncer.IsTrackedRepo("roborev-dev", "kenn-forge"))
}

func TestGetSettingsDoesNotCallGitHub(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	mock := &mockGH{
		getRepositoryFn: func(
			_ context.Context, _, _ string,
		) (*gh.Repository, error) {
			require.FailNow("GET /settings must not call GetRepository")
			return nil, nil
		},
		listReposByOwnerFn: func(
			_ context.Context, _ string,
		) ([]*gh.Repository, error) {
			require.FailNow("GET /settings must not call ListRepositoriesByOwner")
			return nil, nil
		},
	}
	// Build the server directly (bypass setup helper) to avoid
	// its startup call to ResolveConfiguredRepos, which would
	// trip the failing mock during seeding.
	dir := t.TempDir()
	database := dbtest.Open(t)
	cfgPath := filepath.Join(dir, "config.toml")
	require.NoError(os.WriteFile(cfgPath, []byte(`
sync_interval = "5m"
github_token_env = "KENN_FORGE_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "widget"
`), 0o644))
	cfg, err := config.Load(cfgPath)
	require.NoError(err)

	clients := map[string]ghclient.Client{"github.com": mock}
	syncer := ghclient.NewSyncer(
		clients, database, nil,
		[]ghclient.RepoRef{{
			Owner: "acme", Name: "widget",
			PlatformHost: "github.com",
		}},
		time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	srv := server.NewWithConfig(
		database, syncer, nil, nil, cfg, cfgPath,
		server.ServerOptions{},
	)

	rr := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/settings", nil)
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())

	var resp spokeapi.SettingsResponse
	require.NoError(json.NewDecoder(rr.Body).Decode(&resp))
	require.Len(resp.Repos, 1)
	assert.Equal(1, resp.Repos[0].MatchedRepoCount)
}

func TestGlobMatchingIsCaseInsensitive(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	mock := &mockGH{
		listReposByOwnerFn: func(
			_ context.Context, owner string,
		) ([]*gh.Repository, error) {
			return []*gh.Repository{{
				Name:     new("Widget-API"),
				Owner:    &gh.User{Login: new(owner)},
				Archived: new(false),
			}}, nil
		},
	}
	srv, _, _, syncer := setupTestServerWithConfigContent(t, `
sync_interval = "5m"
github_token_env = "KENN_FORGE_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "Widget-*"
`, mock)

	rr := testutil.DoJSON(
		t, srv, http.MethodPost,
		"/api/v1/repo/gh/acme/Widget-*/refresh", nil)

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	assert.True(syncer.IsTrackedRepo("acme", "Widget-API"))
}

func TestAddRepoDoesNotDropConcurrentActivityChange(t *testing.T) {
	// Pre-check for the race fix: handleAddRepo must not
	// overwrite a concurrent handleUpdateSettings change.
	// The setup mutates s.cfg.Activity after the add's
	// pre-check but before its save, then verifies the
	// activity change survives in both memory and on disk.
	assert := assert.New(t)
	require := require.New(t)
	srv, _, cfgPath, _ := setupTestServerWithConfig(t)

	// Change activity via the update handler.
	rr := testutil.DoJSON(
		t, srv, http.MethodPut, "/api/v1/settings",
		spokeapi.UpdateSettingsRequest{
			Activity: &config.Activity{
				ViewMode:  "threaded",
				TimeRange: "30d",
			},
		})

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())

	// Add a new repo; handler should preserve activity.
	addRR := testutil.DoJSON(
		t, srv, http.MethodPost, "/api/v1/repos",
		map[string]string{
			"provider": "github", "host": "github.com",
			"owner": "other-org", "name": "other-repo",
		})

	require.Equal(http.StatusCreated, addRR.Code, addRR.Body.String())

	cfg2, err := config.Load(cfgPath)
	require.NoError(err)
	assert.Equal("30d", cfg2.Activity.TimeRange)
	assert.Len(cfg2.Repos, 2)
}

// TestConcurrentRefreshAndDeleteDoesNotResurrect exercises the
// race where a refresh of a glob is in-flight (blocked on the
// GitHub call) while a DELETE removes that glob. Before the fix
// the refresh would apply its stale expansion after the delete
// and re-add the removed repos to the syncer's tracked set.
func TestConcurrentRefreshAndDeleteDoesNotResurrect(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	var calls atomic.Int32
	ghBlocked := make(chan struct{}, 1)
	ghUnblock := make(chan struct{})
	mock := &mockGH{
		listReposByOwnerFn: func(
			_ context.Context, owner string,
		) ([]*gh.Repository, error) {
			// The setup helper resolves the glob once at
			// server construction; block only on the second
			// call (the refresh request under test).
			if calls.Add(1) == 2 {
				ghBlocked <- struct{}{}
				<-ghUnblock
			}
			return []*gh.Repository{{
				Name:     new("kenn-forge"),
				Owner:    &gh.User{Login: new(owner)},
				Archived: new(false),
			}}, nil
		},
	}
	srv, _, _, syncer := setupTestServerWithConfigContent(t, `
sync_interval = "5m"
github_token_env = "KENN_FORGE_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "roborev-dev"
name = "*"
`, mock)
	require.True(syncer.IsTrackedRepo("roborev-dev", "kenn-forge"))

	refreshDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		// Inline the request (no testify helpers) so the
		// linter does not flag assertions inside the goroutine.
		req := httptest.NewRequestWithContext(t.Context(),
			http.MethodPost,
			"/api/v1/repo/gh/roborev-dev/*/refresh", nil,
		)
		req.Host = "127.0.0.1:8091"
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, req)
		refreshDone <- rr
	}()

	select {
	case <-ghBlocked:
	case <-time.After(5 * time.Second):
		require.FailNow("refresh did not reach the GH mock")
	}

	delRR := testutil.DoJSON(
		t, srv, http.MethodDelete,
		"/api/v1/repo/gh/roborev-dev/*", nil)

	require.Equal(http.StatusNoContent, delRR.Code, delRR.Body.String())
	require.False(syncer.IsTrackedRepo("roborev-dev", "kenn-forge"))

	close(ghUnblock)
	var refreshRR *httptest.ResponseRecorder
	select {
	case refreshRR = <-refreshDone:
	case <-time.After(5 * time.Second):
		require.FailNow("refresh did not complete")
	}
	// Refresh should observe that the glob no longer exists
	// and report 404 rather than applying its stale expansion.
	assert.Equal(http.StatusNotFound, refreshRR.Code, refreshRR.Body.String())

	// The deleted repo must not have reappeared after the
	// refresh completed.
	assert.False(syncer.IsTrackedRepo("roborev-dev", "kenn-forge"),
		"deleted repo resurrected by concurrent refresh")
}

// TestHandleUpdateSettingsPreservesTmuxCommand drives a real
// settings-mutation HTTP call against a config that has a [tmux]
// section on disk, then reloads the config and asserts the Tmux
// command array survived the Save round-trip. This pins down the
// operator-visible contract: mutating activity settings (or any
// other field the UI touches) must not silently erase tmux.command.
func TestHandleUpdateSettingsPreservesTmuxCommand(t *testing.T) {
	assert := assert.New(t)
	srv, _, cfgPath, _ := setupTestServerWithConfigContent(t, `
sync_interval = "5m"
github_token_env = "KENN_FORGE_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "widget"

[tmux]
command = ["systemd-run", "--user", "--scope", "tmux"]
`, &mockGH{})

	body := spokeapi.UpdateSettingsRequest{
		Activity: &config.Activity{
			ViewMode:  "threaded",
			TimeRange: "30d",
		},
	}
	rr := testutil.DoJSON(t, srv, http.MethodPut, "/api/v1/settings", body)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	reloaded, err := config.Load(cfgPath)
	require.NoError(t, err)
	assert.Equal(
		[]string{"systemd-run", "--user", "--scope", "tmux"},
		reloaded.Tmux.Command,
	)
	// Sanity: the mutation actually took effect, so Save did write.
	assert.Equal("30d", reloaded.Activity.TimeRange)
}

func TestHandlePreviewReposFiltersAndMarksAlreadyConfigured(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	pushedNewer := gh.Timestamp{Time: time.Date(2026, 4, 22, 10, 0, 0, 0, time.UTC)}
	pushedOlder := gh.Timestamp{Time: time.Date(2026, 4, 20, 9, 0, 0, 0, time.UTC)}
	privateRepo := true
	publicRepo := false
	regularRepo := false
	forkRepo := true
	mock := &mockGH{
		listReposByOwnerFn: func(_ context.Context, owner string) ([]*gh.Repository, error) {
			return []*gh.Repository{
				{
					Name:        new("widget"),
					Owner:       &gh.User{Login: new(owner)},
					Description: new("already configured widget"),
					Private:     &privateRepo,
					Fork:        &regularRepo,
					Archived:    new(false),
					PushedAt:    &pushedOlder,
				},
				{
					Name:        new("widget-api"),
					Owner:       &gh.User{Login: new(owner)},
					Description: new("api service"),
					Private:     &publicRepo,
					Fork:        &forkRepo,
					Archived:    new(false),
					PushedAt:    &pushedNewer,
				},
				{
					Name:     new("widget-archive"),
					Owner:    &gh.User{Login: new(owner)},
					Archived: new(true),
					PushedAt: &pushedNewer,
				},
				{
					Name:     new("other"),
					Owner:    &gh.User{Login: new(owner)},
					Archived: new(false),
				},
			}, nil
		},
	}
	srv, _, _, _ := setupTestServerWithConfigContent(t, `
sync_interval = "5m"
github_token_env = "KENN_FORGE_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "widget"

[[repos]]
owner = "acme"
name = "widget-*"
`, mock)

	rr := testutil.DoJSON(t, srv, http.MethodPost, "/api/v1/repos/preview", map[string]string{
		"provider": "github",
		"host":     "github.com",
		"owner":    " ACME ",
		"pattern":  " Widget* ",
	})

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())

	var resp repoapi.RepoPreviewResponse
	require.NoError(json.NewDecoder(rr.Body).Decode(&resp))
	require.Len(resp.Repos, 2)
	assert.Equal("ACME", resp.Owner)
	assert.Equal("Widget*", resp.Pattern)
	assert.Equal("acme", resp.Repos[0].Owner)
	assert.Equal("widget", resp.Repos[0].Name)
	assert.Equal("already configured widget", *resp.Repos[0].Description)
	assert.True(resp.Repos[0].Private)
	assert.True(resp.Repos[0].AlreadyConfigured)
	require.NotNil(resp.Repos[0].PushedAt)
	assert.Equal(pushedOlder.Time.UTC().Format(time.RFC3339), *resp.Repos[0].PushedAt)
	assert.Equal("widget-api", resp.Repos[1].Name)
	assert.False(resp.Repos[1].Private)
	assert.True(resp.Repos[1].Fork)
	assert.False(resp.Repos[1].AlreadyConfigured)
	assert.NotContains(rr.Body.String(), "widget-archive")
	assert.NotContains(rr.Body.String(), "other")
}

func TestHandlePreviewReposRoutesGitHubByOwner(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	ownerClient := func(expected, repoName string) *mockGH {
		return &mockGH{listReposByOwnerFn: func(_ context.Context, owner string) ([]*gh.Repository, error) {
			if !strings.EqualFold(owner, expected) {
				return nil, fmt.Errorf("wrong owner route: %s", owner)
			}
			return []*gh.Repository{{
				Name: new(repoName), Owner: &gh.User{Login: new(expected)},
				Archived: new(false),
			}}, nil
		}}
	}
	router, err := ghclient.NewHostRouter(
		"github.com",
		&ghclient.Route{Key: ghclient.RouteKey{Host: "github.com"}, Client: &mockGH{}},
		&ghclient.Route{Key: ghclient.RouteKey{Host: "github.com", Owner: "org-a"}, Client: ownerClient("org-a", "repo-a")},
		&ghclient.Route{Key: ghclient.RouteKey{Host: "github.com", Owner: "org-b"}, Client: ownerClient("org-b", "repo-b")},
	)
	require.NoError(err)
	routed, err := ghclient.NewRoutedClient(router)
	require.NoError(err)
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	require.NoError(os.WriteFile(cfgPath, []byte(`
sync_interval = "5m"
host = "127.0.0.1"
port = 8091
`), 0o644))
	cfg, err := config.Load(cfgPath)
	require.NoError(err)
	database := dbtest.Open(t)
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": routed}, database, nil,
		nil, time.Minute, nil, nil,
	)
	syncer.SetGitHubRouters(map[string]*ghclient.HostRouter{"github.com": router})
	t.Cleanup(syncer.Stop)
	srv := server.NewWithConfig(database, syncer, nil, nil, cfg, cfgPath,
		server.ServerOptions{HostCheckAllowLoopbackAnyPort: true})

	preview := func(owner string) repoapi.RepoPreviewResponse {
		rr := testutil.DoJSON(t, srv, http.MethodPost, "/api/v1/repos/preview", map[string]string{
			"provider": "github", "host": "github.com",
			"owner": owner, "pattern": "*",
		})

		require.Equal(http.StatusOK, rr.Code, rr.Body.String())
		var resp repoapi.RepoPreviewResponse
		require.NoError(json.NewDecoder(rr.Body).Decode(&resp))
		return resp
	}

	orgA := preview("org-a")
	require.Len(orgA.Repos, 1)
	assert.Equal("repo-a", orgA.Repos[0].Name)
	orgB := preview("org-b")
	require.Len(orgB.Repos, 1)
	assert.Equal("repo-b", orgB.Repos[0].Name)
}

func TestHandlePreviewReposFallsBackToListWhenExactLookupFails(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	privateRepo := true
	forkRepo := false
	pushedAt := gh.Timestamp{Time: time.Date(2026, 4, 28, 12, 0, 0, 0, time.UTC)}
	name := "dotfiles2026"
	ownerLogin := "mariusvniekerk"
	description := "personal dotfiles"
	var listCalls atomic.Int32
	var getCalls atomic.Int32
	mock := &mockGH{
		listReposByOwnerFn: func(_ context.Context, owner string) ([]*gh.Repository, error) {
			listCalls.Add(1)
			assert.Equal("mariusvniekerk", owner)
			return []*gh.Repository{
				{
					Name:        &name,
					Owner:       &gh.User{Login: &ownerLogin},
					Description: &description,
					Private:     &privateRepo,
					Fork:        &forkRepo,
					Archived:    new(false),
					PushedAt:    &pushedAt,
				},
			}, nil
		},
		getRepositoryFn: func(_ context.Context, owner, repo string) (*gh.Repository, error) {
			getCalls.Add(1)
			assert.Equal("mariusvniekerk", owner)
			assert.Equal("dotfiles2026", repo)
			return nil, errors.New("not found")
		},
	}
	srv, _, _, _ := setupTestServerWithConfigContent(t, `
sync_interval = "5m"
github_token_env = "KENN_FORGE_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091
`, mock)

	rr := testutil.DoJSON(t, srv, http.MethodPost, "/api/v1/repos/preview", map[string]string{
		"provider": "github",
		"host":     "github.com",
		"owner":    "mariusvniekerk",
		"pattern":  "dotfiles2026",
	})

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())

	var resp repoapi.RepoPreviewResponse
	require.NoError(json.NewDecoder(rr.Body).Decode(&resp))
	require.Len(resp.Repos, 1)
	assert.Equal(int32(1), getCalls.Load())
	assert.Equal(int32(1), listCalls.Load())
	assert.Equal("mariusvniekerk", resp.Repos[0].Owner)
	assert.Equal("dotfiles2026", resp.Repos[0].Name)
	assert.Equal("personal dotfiles", *resp.Repos[0].Description)
	assert.True(resp.Repos[0].Private)
	assert.False(resp.Repos[0].Fork)
	assert.False(resp.Repos[0].AlreadyConfigured)
	require.NotNil(resp.Repos[0].PushedAt)
	assert.Equal(pushedAt.Time.UTC().Format(time.RFC3339), *resp.Repos[0].PushedAt)
}

func TestHandlePreviewReposUsesExactLookupForConcreteRepo(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	privateRepo := true
	forkRepo := false
	pushedAt := gh.Timestamp{Time: time.Date(2026, 4, 28, 12, 0, 0, 0, time.UTC)}
	name := "tesseract-feedstock"
	ownerLogin := "anacondarecipes"
	description := "A conda-smithy repository for tesseract"
	var listCalls atomic.Int32
	var getCalls atomic.Int32
	mock := &mockGH{
		listReposByOwnerFn: func(_ context.Context, owner string) ([]*gh.Repository, error) {
			listCalls.Add(1)
			assert.Fail("concrete repo preview should not list repositories", "owner: %s", owner)
			return []*gh.Repository{}, nil
		},
		getRepositoryFn: func(_ context.Context, owner, repo string) (*gh.Repository, error) {
			getCalls.Add(1)
			assert.Equal("anacondarecipes", owner)
			assert.Equal("tesseract-feedstock", repo)
			return &gh.Repository{
				Name:        &name,
				Owner:       &gh.User{Login: &ownerLogin},
				Description: &description,
				Private:     &privateRepo,
				Fork:        &forkRepo,
				Archived:    new(false),
				PushedAt:    &pushedAt,
			}, nil
		},
	}
	srv, _, _, _ := setupTestServerWithConfigContent(t, `
sync_interval = "5m"
github_token_env = "KENN_FORGE_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091
`, mock)

	rr := testutil.DoJSON(t, srv, http.MethodPost, "/api/v1/repos/preview", map[string]string{
		"provider": "github",
		"host":     "github.com",
		"owner":    "anacondarecipes",
		"pattern":  "tesseract-feedstock",
	})

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())

	var resp repoapi.RepoPreviewResponse
	require.NoError(json.NewDecoder(rr.Body).Decode(&resp))
	require.Len(resp.Repos, 1)
	assert.Equal(int32(1), getCalls.Load())
	assert.Equal(int32(0), listCalls.Load())
	assert.Equal("anacondarecipes", resp.Repos[0].Owner)
	assert.Equal("tesseract-feedstock", resp.Repos[0].Name)
	assert.Equal("A conda-smithy repository for tesseract", *resp.Repos[0].Description)
	assert.True(resp.Repos[0].Private)
	assert.False(resp.Repos[0].Fork)
	assert.False(resp.Repos[0].AlreadyConfigured)
	require.NotNil(resp.Repos[0].PushedAt)
	assert.Equal(pushedAt.Time.UTC().Format(time.RFC3339), *resp.Repos[0].PushedAt)
}

func TestHandlePreviewReposSupportsGitLabNamespaces(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	updatedAt := time.Date(2026, 5, 1, 10, 30, 0, 0, time.UTC)
	provider := repoImportTestProvider{
		kind: platform.KindGitLab,
		host: "gitlab.example.com",
		repos: []platform.Repository{
			{
				Ref: platform.RepoRef{
					Platform: platform.KindGitLab,
					Host:     "gitlab.example.com",
					Owner:    "Group/Subgroup",
					Name:     "Project",
					RepoPath: "Group/Subgroup/Project",
				},
				Description: "gitlab project",
				Private:     true,
				UpdatedAt:   updatedAt,
			},
			{
				Ref: platform.RepoRef{
					Platform: platform.KindGitLab,
					Host:     "gitlab.example.com",
					Owner:    "Group/Subgroup",
					Name:     "Project-Archived",
					RepoPath: "Group/Subgroup/Project-Archived",
				},
				Archived: true,
			},
			{
				Ref: platform.RepoRef{
					Platform: platform.KindGitLab,
					Host:     "gitlab.example.com",
					Owner:    "Group/Subgroup",
					Name:     "Other",
					RepoPath: "Group/Subgroup/Other",
				},
			},
		},
	}
	srv, _, _, _ := setupTestServerWithConfigProviders(t, `
sync_interval = "5m"
github_token_env = "KENN_FORGE_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
platform = "gitlab"
platform_host = "gitlab.example.com"
owner = "Group/Subgroup"
name = "Project"
`, &mockGH{}, provider)

	rr := testutil.DoJSON(t, srv, http.MethodPost, "/api/v1/repos/preview", map[string]string{
		"provider": "gitlab",
		"host":     "gitlab.example.com",
		"owner":    "Group/Subgroup",
		"pattern":  "Project*",
	})

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())

	var resp repoapi.RepoPreviewResponse
	require.NoError(json.NewDecoder(rr.Body).Decode(&resp))
	require.Len(resp.Repos, 1)
	assert.Equal("gitlab", resp.Provider)
	assert.Equal("gitlab.example.com", resp.PlatformHost)
	assert.Equal("Group/Subgroup", resp.Owner)
	assert.Equal("Project*", resp.Pattern)
	assert.Equal("gitlab", resp.Repos[0].Provider)
	assert.Equal("gitlab.example.com", resp.Repos[0].PlatformHost)
	assert.Equal("Group/Subgroup", resp.Repos[0].Owner)
	assert.Equal("Project", resp.Repos[0].Name)
	assert.Equal("Group/Subgroup/Project", resp.Repos[0].RepoPath)
	assert.Equal("gitlab project", *resp.Repos[0].Description)
	assert.True(resp.Repos[0].Private)
	assert.True(resp.Repos[0].AlreadyConfigured)
	require.NotNil(resp.Repos[0].PushedAt)
	assert.Equal(updatedAt.Format(time.RFC3339), *resp.Repos[0].PushedAt)
	assert.NotContains(rr.Body.String(), "Project-Archived")
	assert.NotContains(rr.Body.String(), "Other")
}

func TestHandlePreviewReposSupportsForgejoOrgFallback(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	updatedAt := time.Date(2026, 5, 2, 14, 0, 0, 0, time.UTC)
	transport := &gitealikeImportTransport{
		userReposErr: platform.ErrNotFound,
		orgRepos: []gitealike.RepositoryDTO{
			{
				ID:          101,
				Owner:       gitealike.UserDTO{UserName: "ForgeOrg"},
				Name:        "Widget",
				FullName:    "ForgeOrg/Widget",
				Description: "forgejo widget",
				Private:     true,
				Updated:     updatedAt,
			},
			{
				ID:       102,
				Owner:    gitealike.UserDTO{UserName: "ForgeOrg"},
				Name:     "Widget-Archived",
				FullName: "ForgeOrg/Widget-Archived",
				Archived: true,
			},
			{
				ID:       103,
				Owner:    gitealike.UserDTO{UserName: "ForgeOrg"},
				Name:     "Other",
				FullName: "ForgeOrg/Other",
			},
		},
	}
	provider := gitealike.NewProvider(
		platform.KindForgejo, "codeberg.example.com", transport,
	)
	srv, _, _, _ := setupTestServerWithConfigProviders(t, `
sync_interval = "5m"
github_token_env = "KENN_FORGE_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
platform = "forgejo"
platform_host = "codeberg.example.com"
owner = "ForgeOrg"
name = "Widget"
repo_path = "ForgeOrg/Widget"
`, &mockGH{}, provider)

	rr := testutil.DoJSON(t, srv, http.MethodPost, "/api/v1/repos/preview", map[string]string{
		"provider": "forgejo",
		"host":     "codeberg.example.com",
		"owner":    "ForgeOrg",
		"pattern":  "Widget*",
	})

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())

	body := rr.Body.String()
	var resp repoapi.RepoPreviewResponse
	require.NoError(json.NewDecoder(strings.NewReader(body)).Decode(&resp))
	require.Len(resp.Repos, 1)
	assert.Equal("forgejo", resp.Provider)
	assert.Equal("codeberg.example.com", resp.PlatformHost)
	assert.Equal("ForgeOrg", resp.Owner)
	assert.Equal("forgejo", resp.Repos[0].Provider)
	assert.Equal("codeberg.example.com", resp.Repos[0].PlatformHost)
	assert.Equal("ForgeOrg", resp.Repos[0].Owner)
	assert.Equal("Widget", resp.Repos[0].Name)
	assert.Equal("ForgeOrg/Widget", resp.Repos[0].RepoPath)
	assert.Equal("forgejo widget", *resp.Repos[0].Description)
	assert.True(resp.Repos[0].Private)
	assert.True(resp.Repos[0].AlreadyConfigured)
	require.NotNil(resp.Repos[0].PushedAt)
	assert.Equal(updatedAt.Format(time.RFC3339), *resp.Repos[0].PushedAt)
	assert.NotContains(body, "Widget-Archived")
	assert.NotContains(body, "Other")
}

func TestHandleBulkAddReposPersistsExactRepos(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	var getCalls atomic.Int32
	mock := &mockGH{
		getRepositoryFn: func(_ context.Context, owner, repo string) (*gh.Repository, error) {
			getCalls.Add(1)
			return &gh.Repository{
				Name:     new(strings.ToUpper(repo)),
				Owner:    &gh.User{Login: new(strings.ToUpper(owner))},
				Archived: new(false),
			}, nil
		},
	}
	srv, _, cfgPath, syncer := setupTestServerWithConfigContent(t, `
sync_interval = "5m"
github_token_env = "KENN_FORGE_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "widget"
`, mock)
	callsAfterSetup := getCalls.Load()

	rr := testutil.DoJSON(t, srv, http.MethodPost, "/api/v1/repos/bulk", map[string]any{
		"repos": []map[string]string{
			{"provider": "github", "host": "github.com", "owner": " acme ", "name": " api ", "repo_path": " acme/api "},
			{"provider": "github", "host": "github.com", "owner": "acme", "name": "worker", "repo_path": "acme/worker"},
			{"provider": "github", "host": "github.com", "owner": "acme", "name": "api", "repo_path": "acme/api"},
		},
	})

	require.Equal(http.StatusCreated, rr.Code, rr.Body.String())
	assert.GreaterOrEqual(getCalls.Load(), callsAfterSetup+2)

	var resp spokeapi.SettingsResponse
	require.NoError(json.NewDecoder(rr.Body).Decode(&resp))
	require.Len(resp.Repos, 3)
	assert.Equal("acme", resp.Repos[1].Owner)
	assert.Equal("api", resp.Repos[1].Name)
	assert.Equal("worker", resp.Repos[2].Name)
	assert.True(syncer.IsTrackedRepo("acme", "api"))
	assert.True(syncer.IsTrackedRepo("acme", "worker"))

	cfg2, err := config.Load(cfgPath)
	require.NoError(err)
	require.Len(cfg2.Repos, 3)
	assert.Equal("api", cfg2.Repos[1].Name)
	assert.Equal("worker", cfg2.Repos[2].Name)
}

func TestHandleBulkAddReposPersistsGitLabProviderIdentity(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	ref := platform.RepoRef{
		Platform:           platform.KindGitLab,
		Host:               "gitlab.example.com",
		Owner:              "Group/Subgroup",
		Name:               "Project",
		RepoPath:           "Group/Subgroup/Project",
		PlatformID:         4242,
		PlatformExternalID: "gid://gitlab/Project/4242",
		WebURL:             "https://gitlab.example.com/Group/Subgroup/Project",
		CloneURL:           "https://gitlab.example.com/Group/Subgroup/Project.git",
		DefaultBranch:      "main",
	}
	provider := repoImportTestProvider{
		kind: platform.KindGitLab,
		host: "gitlab.example.com",
		repos: []platform.Repository{{
			Ref:                ref,
			PlatformID:         ref.PlatformID,
			PlatformExternalID: ref.PlatformExternalID,
			WebURL:             ref.WebURL,
			CloneURL:           ref.CloneURL,
			DefaultBranch:      ref.DefaultBranch,
		}},
	}
	srv, database, cfgPath, syncer := setupTestServerWithConfigProviders(t, `
sync_interval = "5m"
github_token_env = "KENN_FORGE_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091
`, &mockGH{}, provider)

	rr := testutil.DoJSON(t, srv, http.MethodPost, "/api/v1/repos/bulk", map[string]any{
		"repos": []map[string]string{
			{
				"provider":  "gitlab",
				"host":      "gitlab.example.com",
				"repo_path": "Group/Subgroup/Project",
			},
		},
	})

	require.Equal(http.StatusCreated, rr.Code, rr.Body.String())

	var resp spokeapi.SettingsResponse
	require.NoError(json.NewDecoder(rr.Body).Decode(&resp))
	require.Len(resp.Repos, 1)
	assert.Equal("gitlab", resp.Repos[0].Provider)
	assert.Equal("gitlab.example.com", resp.Repos[0].PlatformHost)
	assert.Equal("Group/Subgroup", resp.Repos[0].Owner)
	assert.Equal("Project", resp.Repos[0].Name)
	assert.Equal("Group/Subgroup/Project", resp.Repos[0].RepoPath)

	cfg2, err := config.Load(cfgPath)
	require.NoError(err)
	require.Len(cfg2.Repos, 1)
	assert.Equal("gitlab", cfg2.Repos[0].Platform)
	assert.Equal("gitlab.example.com", cfg2.Repos[0].PlatformHost)
	assert.Equal("Group/Subgroup", cfg2.Repos[0].Owner)
	assert.Equal("Project", cfg2.Repos[0].Name)
	assert.Equal("Group/Subgroup/Project", cfg2.Repos[0].RepoPath)
	assert.True(syncer.IsTrackedRepoOnHost("Group/Subgroup", "Project", "gitlab.example.com"))

	dbRepo, err := database.GetRepoByIdentity(t.Context(), platformdb.DBRepoIdentity(ref))
	require.NoError(err)
	require.NotNil(dbRepo)
	assert.Equal("gitlab", dbRepo.Platform)
	assert.Equal("gitlab.example.com", dbRepo.PlatformHost)
	assert.Equal("Group/Subgroup/Project", dbRepo.RepoPath)
}

func TestApplyProviderSettingsMatchesWorktreePathByStableIdentity(t *testing.T) {
	local := spokeapi.SettingsResponse{Repos: []ghclient.ConfiguredRepoStatus{
		{
			Provider: "github", PlatformHost: "github.com",
			PlatformRepoID: "repo-widget", Owner: "acme", Name: "widget",
			WorktreeBasePath: "/work/widget",
		},
		{
			Provider: "github", PlatformHost: "github.com",
			PlatformRepoID: "repo-old", Owner: "acme", Name: "reused",
			WorktreeBasePath: "/work/old",
		},
	}}
	provider := spokeapi.SettingsResponse{Repos: []ghclient.ConfiguredRepoStatus{
		{
			Provider: "github", PlatformHost: "github.com",
			PlatformRepoID: "repo-widget", Owner: "acme-renamed", Name: "widget-renamed",
		},
		{
			Provider: "github", PlatformHost: "github.com",
			PlatformRepoID: "repo-new", Owner: "acme", Name: "reused",
		},
	}}

	local.ApplyProviderSettings(provider)

	require.Len(t, local.Repos, 2)
	assert.Equal(t, "/work/widget", local.Repos[0].WorktreeBasePath)
	assert.Empty(t, local.Repos[1].WorktreeBasePath)
}

func TestRepositoryDescriptorAcceptsSupersededSameRouteObservation(t *testing.T) {
	require := require.New(t)
	database := dbtest.Open(t)
	newer := time.Date(2026, time.August, 24, 12, 1, 0, 0, time.UTC)
	identity := db.RepoIdentity{
		Platform: "github", PlatformHost: "github.com",
		PlatformRepoID: "repo-widget", Owner: "acme", Name: "widget",
	}
	_, accepted, err := database.ReconcileRepositoryObservation(
		t.Context(), identity, newer,
	)
	require.NoError(err)
	require.True(accepted)

	source := spokeapi.HubProviderSource{Db: database}
	require.NoError(source.ObserveRepositoryDescriptor(t.Context(), providerplane.RepositoryDescriptor{
		ProtocolVersion: federation.ProtocolVersion,
		Provider:        "github", PlatformHost: "github.com", PlatformRepoID: "repo-widget",
		Owner: "acme", Name: "widget", CloneURL: "https://github.com/acme/widget.git",
		DefaultBranch: "main", SnapshotRevision: 1, ObservedAt: newer.Add(-time.Second),
	}))

	identity.Owner = "acme-renamed"
	_, accepted, err = database.ReconcileRepositoryObservation(
		t.Context(), identity, newer.Add(time.Minute),
	)
	require.NoError(err)
	require.True(accepted)
	require.Error(source.ObserveRepositoryDescriptor(t.Context(), providerplane.RepositoryDescriptor{
		ProtocolVersion: federation.ProtocolVersion,
		Provider:        "github", PlatformHost: "github.com", PlatformRepoID: "repo-widget",
		Owner: "acme", Name: "widget", CloneURL: "https://github.com/acme/widget.git",
		DefaultBranch: "main", SnapshotRevision: 1, ObservedAt: newer,
	}))
}

func TestHandleBulkAddReposPersistsGiteaProviderIdentity(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	transport := &gitealikeImportTransport{
		repository: gitealike.RepositoryDTO{
			ID:            6262,
			Owner:         gitealike.UserDTO{UserName: "Team"},
			Name:          "Service",
			FullName:      "Team/Service",
			HTMLURL:       "https://gitea.example.com/Team/Service",
			CloneURL:      "https://gitea.example.com/Team/Service.git",
			DefaultBranch: "main",
		},
	}
	provider := gitealike.NewProvider(
		platform.KindGitea, "gitea.example.com", transport,
	)
	srv, database, cfgPath, syncer := setupTestServerWithConfigProviders(t, `
sync_interval = "5m"
github_token_env = "KENN_FORGE_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091
`, &mockGH{}, provider)

	rr := testutil.DoJSON(t, srv, http.MethodPost, "/api/v1/repos/bulk", map[string]any{
		"repos": []map[string]string{
			{
				"provider":  "gitea",
				"host":      "gitea.example.com",
				"repo_path": "Team/Service",
			},
		},
	})

	require.Equal(http.StatusCreated, rr.Code, rr.Body.String())

	var resp spokeapi.SettingsResponse
	require.NoError(json.NewDecoder(rr.Body).Decode(&resp))
	require.Len(resp.Repos, 1)
	assert.Equal("gitea", resp.Repos[0].Provider)
	assert.Equal("gitea.example.com", resp.Repos[0].PlatformHost)
	assert.Equal("Team", resp.Repos[0].Owner)
	assert.Equal("Service", resp.Repos[0].Name)
	assert.Equal("Team/Service", resp.Repos[0].RepoPath)

	cfg2, err := config.Load(cfgPath)
	require.NoError(err)
	require.Len(cfg2.Repos, 1)
	assert.Equal("gitea", cfg2.Repos[0].Platform)
	assert.Equal("gitea.example.com", cfg2.Repos[0].PlatformHost)
	assert.Equal("Team", cfg2.Repos[0].Owner)
	assert.Equal("Service", cfg2.Repos[0].Name)
	assert.Equal("Team/Service", cfg2.Repos[0].RepoPath)
	assert.True(syncer.IsTrackedRepoOnHost("Team", "Service", "gitea.example.com"))

	ref := platform.RepoRef{
		Platform:           platform.KindGitea,
		Host:               "gitea.example.com",
		Owner:              "Team",
		Name:               "Service",
		RepoPath:           "Team/Service",
		PlatformID:         6262,
		PlatformExternalID: "6262",
	}
	dbRepo, err := database.GetRepoByIdentity(t.Context(), platformdb.DBRepoIdentity(ref))
	require.NoError(err)
	require.NotNil(dbRepo)
	assert.Equal("gitea", dbRepo.Platform)
	assert.Equal("gitea.example.com", dbRepo.PlatformHost)
	assert.Equal("Team/Service", dbRepo.RepoPath)
}

func TestHandleBulkAddReposValidationFailureChangesNothing(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	mock := &mockGH{
		getRepositoryFn: func(_ context.Context, owner, repo string) (*gh.Repository, error) {
			if repo == "missing" {
				return nil, errors.New("not found")
			}
			return &gh.Repository{
				Name:     new(repo),
				Owner:    &gh.User{Login: new(owner)},
				Archived: new(false),
			}, nil
		},
	}
	srv, _, cfgPath, syncer := setupTestServerWithConfigContent(t, `
sync_interval = "5m"
github_token_env = "KENN_FORGE_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "widget"
`, mock)

	rr := testutil.DoJSON(t, srv, http.MethodPost, "/api/v1/repos/bulk", map[string]any{
		"repos": []map[string]string{
			{"provider": "github", "host": "github.com", "owner": "acme", "name": "api", "repo_path": "acme/api"},
			{"provider": "github", "host": "github.com", "owner": "acme", "name": "missing", "repo_path": "acme/missing"},
		},
	})

	require.Equal(http.StatusBadGateway, rr.Code, rr.Body.String())
	assert.False(syncer.IsTrackedRepo("acme", "api"))

	cfg2, err := config.Load(cfgPath)
	require.NoError(err)
	require.Len(cfg2.Repos, 1)
	assert.Equal("widget", cfg2.Repos[0].Name)
}

func TestHandleBulkAddReposSkipsAlreadyConfiguredBeforeValidation(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	var apiCalls atomic.Int32
	mock := &mockGH{
		getRepositoryFn: func(_ context.Context, owner, repo string) (*gh.Repository, error) {
			if repo == "api" {
				apiCalls.Add(1)
				return nil, errors.New("stale configured repo should not be validated")
			}
			return &gh.Repository{
				Name:     new(repo),
				Owner:    &gh.User{Login: new(owner)},
				Archived: new(false),
			}, nil
		},
	}
	srv, _, cfgPath, syncer := setupTestServerWithConfigContent(t, `
sync_interval = "5m"
github_token_env = "KENN_FORGE_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "api"
`, mock)
	rr := testutil.DoJSON(t, srv, http.MethodPost, "/api/v1/repos/bulk", map[string]any{
		"repos": []map[string]string{
			{"provider": "github", "host": "github.com", "owner": "acme", "name": "api", "repo_path": "acme/api"},
			{"provider": "github", "host": "github.com", "owner": "acme", "name": "worker", "repo_path": "acme/worker"},
		},
	})

	require.Equal(http.StatusCreated, rr.Code, rr.Body.String())
	assert.True(syncer.IsTrackedRepo("acme", "worker"))

	cfg2, err := config.Load(cfgPath)
	require.NoError(err)
	require.Len(cfg2.Repos, 2)
	assert.Equal("worker", cfg2.Repos[1].Name)
}

func TestHandleBulkAddReposSkipsAlreadyConfiguredAtApplyTime(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	unblockGet := make(chan struct{})
	getStarted := make(chan struct{}, 1)
	var apiCalls atomic.Int32
	mock := &mockGH{
		getRepositoryFn: func(_ context.Context, owner, repo string) (*gh.Repository, error) {
			if repo == "api" && apiCalls.Add(1) == 1 {
				getStarted <- struct{}{}
				<-unblockGet
			}
			return &gh.Repository{
				Name:     new(repo),
				Owner:    &gh.User{Login: new(owner)},
				Archived: new(false),
			}, nil
		},
	}
	srv, _, _, _ := setupTestServerWithConfigContent(t, `
sync_interval = "5m"
github_token_env = "KENN_FORGE_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "widget"
`, mock)

	var bulkBody bytes.Buffer
	require.NoError(json.NewEncoder(&bulkBody).Encode(map[string]any{
		"repos": []map[string]string{
			{"provider": "github", "host": "github.com", "owner": "acme", "name": "api", "repo_path": "acme/api"},
			{"provider": "github", "host": "github.com", "owner": "acme", "name": "worker", "repo_path": "acme/worker"},
		},
	}))
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		// Inline request avoids testify assertions inside this goroutine.
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/repos/bulk", bytes.NewReader(bulkBody.Bytes()))
		req.Host = "127.0.0.1:8091"
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, req)
		done <- rr
	}()

	select {
	case <-getStarted:
	case <-time.After(5 * time.Second):
		require.FailNow("bulk validation did not start")
	}
	addRR := testutil.DoJSON(t, srv, http.MethodPost, "/api/v1/repos", map[string]string{
		"provider": "github", "host": "github.com", "owner": "acme", "name": "api",
	})

	require.Equal(http.StatusCreated, addRR.Code, addRR.Body.String())
	close(unblockGet)

	var rr *httptest.ResponseRecorder
	select {
	case rr = <-done:
	case <-time.After(5 * time.Second):
		require.FailNow("bulk add did not finish")
	}
	require.Equal(http.StatusCreated, rr.Code, rr.Body.String())
	var resp spokeapi.SettingsResponse
	require.NoError(json.NewDecoder(rr.Body).Decode(&resp))
	assert.Equal([]string{"widget", "api", "worker"}, []string{resp.Repos[0].Name, resp.Repos[1].Name, resp.Repos[2].Name})
}

func TestFleetSettingsPreserveEnrollmentOwnedRoleAndMembers(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	srv, _, cfgPath, _ := setupTestServerWithConfigContent(t, `
host = "127.0.0.1"
port = 8091

[api]
require_auth = true

[fleet]
enabled = true
role = "hub"
base_url = "https://hub.example"

[[fleet.members]]
node_id = "fedcba9876543210fedcba9876543210"
name = "Build Box"
base_url = "https://spoke.example"
state = "active"
	`, &mockGH{})

	get := func() spokeapi.FleetSettingsResponse {
		response := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/settings/fleet", nil)
		require.Equal(http.StatusOK, response.Code)
		var result spokeapi.FleetSettingsResponse
		require.NoError(json.NewDecoder(response.Body).Decode(&result))
		return result
	}
	initial := get()
	assert.Equal(config.FleetRoleHub, initial.Role)
	require.Len(initial.Members, 1)
	assert.Equal("Build Box", initial.Members[0].Name)
	assert.Empty(initial.Enrollments)

	forbiddenLifecyclePayload := map[string]any{
		"enabled": true, "role": "spoke",
		"sessions": map[string]any{"include_unmanaged_details": false},
		"members": []map[string]any{{
			"node_id": "fedcba9876543210fedcba9876543210",
			"name":    "Renamed Build Box", "base_url": "https://spoke.example",
			"state": "active",
		}},
	}
	response := testutil.DoJSON(
		t, srv, http.MethodPut, "/api/v1/settings/fleet", forbiddenLifecyclePayload)

	require.Equal(http.StatusUnprocessableEntity, response.Code)

	payload := map[string]any{
		"enabled":  true,
		"sessions": map[string]any{"include_unmanaged_details": false},
	}
	response = testutil.DoJSON(t, srv, http.MethodPut, "/api/v1/settings/fleet", payload)
	require.Equal(http.StatusOK, response.Code)
	var updated spokeapi.FleetSettingsResponse
	require.NoError(json.NewDecoder(response.Body).Decode(&updated))
	assert.Equal(config.FleetRoleHub, updated.Role)
	assert.Equal("Build Box", updated.Members[0].Name)
	assert.False(updated.RestartRequired)

	raw, err := os.ReadFile(cfgPath)
	require.NoError(err)
	assert.Contains(string(raw), "Build Box")
	assert.NotContains(string(raw), "Renamed Build Box")
	assert.NotContains(string(raw), `role = "spoke"`)
}

// TestHandleUpdateSettingsRestoresProjectionAfterRequestCancellation covers the
// window after the setting is persisted and the syncer has already switched: the
// committed state must be reconciled even if the client is gone, or native
// ordering would keep driving the UI and the merge safeguard until some later
// sync happened to re-detect.
func TestHandleUpdateSettingsRestoresProjectionAfterRequestCancellation(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	srv, database, _, _ := setupTestServerWithConfigContent(t, `
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
	before, err := client.HTTP.GetPullStackWithResponse(ctx, &generated.GetPullStackRequestOptions{PathParams: &generated.GetPullStackPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(10)}})
	require.NoError(err)
	require.NotNil(before.JSON200)
	require.NotNil(before.JSON200.Members)
	require.Equal([]int64{11, 10}, stackMemberNumbers(before.JSON200.Members))

	var buf bytes.Buffer
	require.NoError(json.NewEncoder(&buf).Encode(spokeapi.UpdateSettingsRequest{
		PullRequests: &config.PullRequests{},
	}))
	reqCtx, cancel := context.WithCancel(ctx)
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPut, "/api/v1/settings", &buf).WithContext(reqCtx)
	req.Host = "127.0.0.1:8091"
	req.Header.Set("Content-Type", "application/json")
	// The client disconnects while the request is being served.
	cancel()
	srv.ServeHTTP(httptest.NewRecorder(), req)

	after, err := client.HTTP.GetPullStackWithResponse(ctx, &generated.GetPullStackRequestOptions{PathParams: &generated.GetPullStackPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(10)}})
	require.NoError(err)
	require.NotNil(after.JSON200)
	require.NotNil(after.JSON200.Members)
	assert.Equal([]int64{10, 11}, stackMemberNumbers(after.JSON200.Members),
		"committed-state reconciliation must not depend on the request context")
}

// TestHandleUpdateSettingsRestoresProjectionForUntrackedRepo covers a
// repository dropped from config before the preview is disabled. Nothing will
// sync it again, so if reconciliation only walked the tracked set its stored
// pull requests would keep serving native ordering forever.
func TestHandleUpdateSettingsRestoresProjectionForUntrackedRepo(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	srv, database, _, _ := setupTestServerWithConfigContent(t, `
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
	// "removed" is absent from config, so the syncer never tracked it.
	seedStackedPR(t, database, "acme", "removed", 10, "feat/base", "main", db.MergeRequestStateOpen, "", "")
	seedStackedPR(t, database, "acme", "removed", 11, "feat/tip", "feat/base", db.MergeRequestStateOpen, "", "")
	repo, err := database.GetRepoByIdentity(ctx, verifiedGitHubRepoIdentity("github.com", "acme", "removed"))
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
	// The cache row is gone but the projection it produced remains, so the
	// native ordering cannot be found by looking for native rows.
	require.NoError(database.DeleteGitHubNativeStacks(ctx, repo.ID, []int{42}))
	client := setupTestClientWithBaseURL(t, srv, "http://127.0.0.1:8091")
	before, err := client.HTTP.GetPullStackWithResponse(ctx, &generated.GetPullStackRequestOptions{PathParams: &generated.GetPullStackPath{Provider: "gh", Owner: "acme", Name: "removed", Number: int64(10)}})
	require.NoError(err)
	require.NotNil(before.JSON200)
	require.NotNil(before.JSON200.Members)
	require.Equal([]int64{11, 10}, stackMemberNumbers(before.JSON200.Members))

	disabled := config.PullRequests{}
	rr := testutil.DoJSON(t, srv, http.MethodPut, "/api/v1/settings", spokeapi.UpdateSettingsRequest{
		PullRequests: &disabled,
	})

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())

	after, err := client.HTTP.GetPullStackWithResponse(ctx, &generated.GetPullStackRequestOptions{PathParams: &generated.GetPullStackPath{Provider: "gh", Owner: "acme", Name: "removed", Number: int64(10)}})
	require.NoError(err)
	require.NotNil(after.JSON200)
	require.NotNil(after.JSON200.Members)
	assert.Equal([]int64{10, 11}, stackMemberNumbers(after.JSON200.Members),
		"a repository no longer tracked must still lose native ordering when the preview is disabled")
}

func TestHandleUpdateSettingsPersistsQuickActions(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	srv, _, cfgPath, _ := setupTestServerWithConfig(t)
	actions := []config.QuickAction{{
		Label:  "Rebase",
		Agent:  "codex",
		Prompt: "rebase this pull request onto main",
	}, {
		Label:  "Triage",
		Agent:  "opencode",
		Prompt: "/triage-pr\nquestion all assumptions",
	}}

	rr := testutil.DoJSON(
		t, srv, http.MethodPut, "/api/v1/settings", spokeapi.UpdateSettingsRequest{QuickActions: &actions})
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())

	cfg2, err := config.Load(cfgPath)
	require.NoError(err)
	assert.Equal(actions, cfg2.QuickActions)

	rr = testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/settings", nil)
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	var body struct {
		QuickActions []config.QuickAction `json:"quick_actions"`
	}
	require.NoError(json.Unmarshal(rr.Body.Bytes(), &body))
	assert.Equal(actions, body.QuickActions)

	// An invalid entry rolls the whole write back.
	invalid := []config.QuickAction{{Label: "", Agent: "codex", Prompt: "go"}}
	rr = testutil.DoJSON(
		t, srv, http.MethodPut, "/api/v1/settings", spokeapi.UpdateSettingsRequest{QuickActions: &invalid})
	require.Equal(http.StatusBadRequest, rr.Code, rr.Body.String())
	cfg3, err := config.Load(cfgPath)
	require.NoError(err)
	assert.Equal(actions, cfg3.QuickActions)
}
