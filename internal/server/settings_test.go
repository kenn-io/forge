package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	gh "github.com/google/go-github/v84/github"
	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/config"
	"go.kenn.io/middleman/internal/db"
	ghclient "go.kenn.io/middleman/internal/github"
	"go.kenn.io/middleman/internal/platform"
	"go.kenn.io/middleman/internal/platform/gitealike"
	"go.kenn.io/middleman/internal/testutil/dbtest"
	"go.kenn.io/middleman/internal/workspace/localruntime"
)

func setupTestServerWithConfig(
	t *testing.T,
) (*Server, *db.DB, string) {
	return setupTestServerWithConfigContent(t, `
sync_interval = "5m"
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "widget"
`, &mockGH{})
}

func setupTestServerWithConfigContent(
	t *testing.T,
	cfgContent string,
	mock *mockGH,
) (*Server, *db.DB, string) {
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
		ServerOptions{HostCheckAllowLoopbackAnyPort: true},
	)
	return srv, database, cfgPath
}

func setupTestServerWithConfigProviders(
	t *testing.T,
	cfgContent string,
	mock *mockGH,
	providers ...platform.Provider,
) (*Server, *db.DB, string) {
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
	srv := NewWithConfig(
		database, syncer, nil, nil, cfg, cfgPath,
		ServerOptions{HostCheckAllowLoopbackAnyPort: true},
	)
	return srv, database, cfgPath
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

func doJSON(
	t *testing.T,
	srv *Server,
	method, path string,
	body any,
) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		require.NoError(t, json.NewEncoder(&buf).Encode(body))
	}
	req := httptest.NewRequest(method, path, &buf)
	// httptest.NewRequest defaults the Host to "example.com" via the
	// "/path" URL; tests built on doJSON use either the cfg=nil
	// test-friendly default (loopback IP at any port is allowed)
	// or a validated cfg whose bind is 127.0.0.1:8091. Sending the
	// bind value here keeps both paths happy through the Host
	// validation middleware without per-test churn.
	req.Host = "127.0.0.1:8091"
	if method != http.MethodGet {
		req.Header.Set("Content-Type", "application/json")
	}
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	return rr
}

func TestHandleGetSettings(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	srv, _, _ := setupTestServerWithConfigContent(t, `
sync_interval = "5m"
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"
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

	rr := doJSON(t, srv, http.MethodGet, "/api/v1/settings", nil)
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())

	var resp settingsResponse
	require.NoError(json.NewDecoder(rr.Body).Decode(&resp))
	require.Len(resp.Repos, 1)
	assert.Equal("acme", resp.Repos[0].Owner)
	assert.Equal(1, resp.Repos[0].MatchedRepoCount)
	assert.Equal("threaded", resp.Activity.ViewMode)
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
	assertDefaultModeVisibility(t, resp.Modes)
	require.Len(resp.Agents, 1)
	assert.Equal("codex", resp.Agents[0].Key)
	assert.Equal([]string{"codex", "--full-auto"}, resp.Agents[0].Command)
}

func TestHandleUpdateSettingsPersistsModes(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	srv, _, cfgPath := setupTestServerWithConfig(t)

	modes := config.DefaultModeVisibility()
	*modes.Kata = true
	*modes.Docs = true
	*modes.Messages = true
	*modes.Workspaces = false

	rr := doJSON(
		t, srv, http.MethodPut, "/api/v1/settings",
		updateSettingsRequest{Modes: &modes},
	)
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())

	var resp settingsResponse
	require.NoError(json.NewDecoder(rr.Body).Decode(&resp))
	assert.True(*resp.Modes.Kata)
	assert.True(*resp.Modes.Docs)
	assert.True(*resp.Modes.Messages)
	assert.False(*resp.Modes.Workspaces)
	assert.True(*resp.Modes.Activity)
	assert.True(*resp.Modes.Repos)
	assert.True(*resp.Modes.Pulls)
	assert.True(*resp.Modes.Issues)
	assert.True(*resp.Modes.Board)
	assert.True(*resp.Modes.Reviews)

	cfg2, err := config.Load(cfgPath)
	require.NoError(err)
	assert.True(*cfg2.Modes.Kata)
	assert.True(*cfg2.Modes.Docs)
	assert.True(*cfg2.Modes.Messages)
	assert.False(*cfg2.Modes.Workspaces)
	assert.True(*cfg2.Modes.Activity)
	assert.True(*cfg2.Modes.Repos)
	assert.True(*cfg2.Modes.Pulls)
	assert.True(*cfg2.Modes.Issues)
	assert.True(*cfg2.Modes.Board)
	assert.True(*cfg2.Modes.Reviews)
}

func assertDefaultModeVisibility(t *testing.T, modes config.ModeVisibility) {
	t.Helper()
	assert := Assert.New(t)
	assert.True(*modes.Activity)
	assert.True(*modes.Repos)
	assert.False(*modes.Kata)
	assert.False(*modes.Docs)
	assert.False(*modes.Messages)
	assert.True(*modes.Pulls)
	assert.True(*modes.Issues)
	assert.True(*modes.Board)
	assert.True(*modes.Reviews)
	assert.True(*modes.Workspaces)
}

func TestHandleUpdateSettings(t *testing.T) {
	assert := Assert.New(t)
	srv, _, cfgPath := setupTestServerWithConfig(t)

	activity := config.Activity{
		ViewMode:   "threaded",
		TimeRange:  "30d",
		HideClosed: true,
		HideBots:   true,
	}
	terminal := config.Terminal{
		FontFamily:    "\"Fira Code\", monospace",
		FontSize:      16,
		Scrollback:    5000,
		LineHeight:    1.15,
		CursorBlink:   new(true),
		FontLigatures: true,
		Renderer:      config.TerminalRendererXterm,
	}
	body := updateSettingsRequest{
		Activity: &activity,
		Terminal: &terminal,
	}
	rr := doJSON(
		t, srv, http.MethodPut, "/api/v1/settings", body,
	)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	// Verify persisted to disk.
	cfg2, err := config.Load(cfgPath)
	require.NoError(t, err)
	assert.Equal("threaded", cfg2.Activity.ViewMode)
	assert.Equal("30d", cfg2.Activity.TimeRange)
	assert.Equal("\"Fira Code\", monospace", cfg2.Terminal.FontFamily)
	assert.Equal(16, cfg2.Terminal.FontSize)
	assert.Equal(5000, cfg2.Terminal.Scrollback)
	assert.InDelta(1.15, cfg2.Terminal.LineHeight, 0.001)
	assert.True(cfg2.Terminal.FontLigatures)
}

func TestHandleUpdateTerminalSettingsPreservesActivity(t *testing.T) {
	assert := Assert.New(t)
	srv, _, cfgPath := setupTestServerWithConfigContent(t, `
sync_interval = "5m"
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"
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
messages = false
`, &mockGH{})

	terminal := config.Terminal{
		FontFamily:    "\"Iosevka Term\", monospace",
		FontSize:      15,
		Scrollback:    2000,
		LetterSpacing: 1,
		CursorBlink:   new(true),
		Renderer:      config.TerminalRendererXterm,
	}
	body := updateSettingsRequest{
		Terminal: &terminal,
	}
	rr := doJSON(
		t, srv, http.MethodPut, "/api/v1/settings", body,
	)
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
	assert.False(*cfg2.Modes.Messages)
}

func TestHandleUpdateSettingsPersistsAgents(t *testing.T) {
	assert := Assert.New(t)
	srv, _, cfgPath := setupTestServerWithConfig(t)
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

	body := updateSettingsRequest{Agents: &agents}
	rr := doJSON(
		t, srv, http.MethodPut, "/api/v1/settings", body,
	)
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

func TestHandleUpdateSettingsRefreshesRuntimeTargets(t *testing.T) {
	dir := t.TempDir()
	agentPath := filepath.Join(dir, "codex-custom")
	require.NoError(t, os.WriteFile(
		agentPath,
		[]byte("#!/bin/sh\nexit 0\n"),
		0o755,
	))
	srv, _, _ := setupTestServerWithConfigContent(t, `
sync_interval = "5m"
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"
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
	rr := doJSON(
		t, srv, http.MethodPut, "/api/v1/settings",
		updateSettingsRequest{Agents: &agents},
	)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	target := findRuntimeTargetForSettingsTest(
		t, srv.runtime.LaunchTargets(), "codex",
	)
	assert := Assert.New(t)
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

func TestHandleUpdateSettingsInvalid(t *testing.T) {
	srv, _, cfgPath := setupTestServerWithConfig(t)

	activity := config.Activity{
		ViewMode:  "kanban",
		TimeRange: "7d",
	}
	body := updateSettingsRequest{
		Activity: &activity,
	}
	rr := doJSON(
		t, srv, http.MethodPut, "/api/v1/settings", body,
	)
	require.Equal(t, http.StatusUnprocessableEntity, rr.Code, rr.Body.String())

	// Verify config was NOT modified (rollback).
	cfg2, err := config.Load(cfgPath)
	require.NoError(t, err)
	Assert.Equal(t, "threaded", cfg2.Activity.ViewMode)
}

func TestHandleAddRepo(t *testing.T) {
	srv, _, cfgPath := setupTestServerWithConfig(t)

	body := map[string]string{
		"provider": "github",
		"host":     "github.com",
		"owner":    "other-org",
		"name":     "other-repo",
	}
	rr := doJSON(
		t, srv, http.MethodPost, "/api/v1/repos", body,
	)
	require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())

	cfg2, err := config.Load(cfgPath)
	require.NoError(t, err)
	require.Len(t, cfg2.Repos, 2)
}

func TestHandleAddRepoTriggersImmediateSyncDuringCooldown(t *testing.T) {
	require := require.New(t)

	dir := t.TempDir()
	database := dbtest.Open(t)

	cfgPath := filepath.Join(dir, "config.toml")
	require.NoError(os.WriteFile(cfgPath, []byte(`
sync_interval = "5m"
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "widget"
`), 0o644))

	cfg, err := config.Load(cfgPath)
	require.NoError(err)

	mock := &mockGH{}
	trackers := map[string]*ghclient.RateTracker{
		"github.com": ghclient.NewRateTracker(
			database, "github.com", "rest",
		),
	}
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": mock},
		database,
		nil,
		[]ghclient.RepoRef{{
			Owner:        "acme",
			Name:         "widget",
			PlatformHost: "github.com",
		}},
		time.Minute,
		trackers,
		nil,
	)
	t.Cleanup(syncer.Stop)

	srv := NewWithConfig(
		database, syncer, nil, nil, cfg, cfgPath,
		ServerOptions{},
	)

	// Prime nextSyncAfter so the add-repo trigger exercises the same
	// cooldown path as a user clicking Sync right after a recent sync.
	syncer.RunOnce(t.Context())

	rr := doJSON(
		t, srv, http.MethodPost, "/api/v1/repos",
		map[string]string{
			"provider": "github",
			"host":     "github.com",
			"owner":    "other-org",
			"name":     "other-repo",
		},
	)
	require.Equal(http.StatusCreated, rr.Code, rr.Body.String())

	require.Eventually(func() bool {
		repos, err := database.ListRepos(t.Context())
		if err != nil {
			return false
		}
		if len(repos) != 2 {
			return false
		}
		for _, repo := range repos {
			if repo.Owner == "other-org" &&
				repo.Name == "other-repo" {
				return true
			}
		}
		return false
	}, 2*time.Second, 10*time.Millisecond)
}

func TestHandleAddRepoDuplicate(t *testing.T) {
	srv, _, _ := setupTestServerWithConfig(t)

	body := map[string]string{
		"provider": "github",
		"host":     "github.com",
		"owner":    "acme",
		"name":     "widget",
	}
	rr := doJSON(
		t, srv, http.MethodPost, "/api/v1/repos", body,
	)
	require.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())
}

func TestHandleDeleteRepo(t *testing.T) {
	require := require.New(t)
	srv, _, cfgPath := setupTestServerWithConfig(t)

	// Add a second repo first so we can delete one.
	addBody := map[string]string{
		"provider": "github",
		"host":     "github.com",
		"owner":    "other-org",
		"name":     "other-repo",
	}
	addRR := doJSON(
		t, srv, http.MethodPost, "/api/v1/repos", addBody,
	)
	require.Equal(http.StatusCreated, addRR.Code, addRR.Body.String())

	rr := doJSON(
		t, srv, http.MethodDelete,
		"/api/v1/repo/gh/acme/widget", nil,
	)
	require.Equal(http.StatusNoContent, rr.Code, rr.Body.String())

	cfg2, err := config.Load(cfgPath)
	require.NoError(err)
	require.Len(cfg2.Repos, 1)
	Assert.Equal(t, "other-org", cfg2.Repos[0].Owner)
}

func TestGetSettingsWithoutPersistence(t *testing.T) {
	assert := Assert.New(t)
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
	srv := New(database, syncer, nil, "/", cfg, ServerOptions{})

	// GET /settings should work (read-only).
	rr := doJSON(t, srv, http.MethodGet, "/api/v1/settings", nil)
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())

	var resp settingsResponse
	require.NoError(json.NewDecoder(rr.Body).Decode(&resp))
	require.Len(resp.Repos, 1)
	assert.Equal("acme", resp.Repos[0].Owner)
	assert.Equal("flat", resp.Activity.ViewMode)

	// Mutations should be rejected (no cfgPath).
	mutRR := doJSON(t, srv, http.MethodPut, "/api/v1/settings",
		updateSettingsRequest{Activity: &cfg.Activity})
	assert.Equal(http.StatusNotFound, mutRR.Code)

	addRR := doJSON(t, srv, http.MethodPost, "/api/v1/repos",
		map[string]string{
			"provider": "github",
			"host":     "github.com",
			"owner":    "x",
			"name":     "y",
		})
	assert.Equal(http.StatusNotFound, addRR.Code)

	delRR := doJSON(t, srv, http.MethodDelete,
		"/api/v1/repo/gh/acme/widget", nil)
	assert.Equal(http.StatusNotFound, delRR.Code)
}

func TestHandleDeleteLastRepo(t *testing.T) {
	srv, _, cfgPath := setupTestServerWithConfig(t)

	rr := doJSON(
		t, srv, http.MethodDelete,
		"/api/v1/repo/gh/acme/widget", nil,
	)
	require.Equal(t, http.StatusNoContent, rr.Code, rr.Body.String())

	cfg2, err := config.Load(cfgPath)
	require.NoError(t, err)
	Assert.Empty(t, cfg2.Repos)
}

func TestHandleGetSettingsIncludesGlobCounts(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	mock := &mockGH{
		listReposByOwnerFn: func(
			_ context.Context, owner string,
		) ([]*gh.Repository, error) {
			return []*gh.Repository{
				{
					Name:     new("middleman"),
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
	srv, _, _ := setupTestServerWithConfigContent(t, `
sync_interval = "5m"
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "roborev-dev"
name = "*"
`, mock)

	rr := doJSON(t, srv, http.MethodGet, "/api/v1/settings", nil)
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())

	var resp settingsResponse
	require.NoError(json.NewDecoder(rr.Body).Decode(&resp))
	require.Len(resp.Repos, 1)
	assert.Equal("roborev-dev", resp.Repos[0].Owner)
	assert.Equal("*", resp.Repos[0].Name)
	assert.True(resp.Repos[0].IsGlob)
	assert.Equal(2, resp.Repos[0].MatchedRepoCount)
}

func TestHandleRefreshRepoRebuildsExpandedSyncSet(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	mock := &mockGH{
		listReposByOwnerFn: func(
			_ context.Context, owner string,
		) ([]*gh.Repository, error) {
			return []*gh.Repository{
				{
					Name:     new("middleman"),
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
	srv, _, _ := setupTestServerWithConfigContent(t, `
sync_interval = "5m"
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "roborev-dev"
name = "*"
`, mock)

	rr := doJSON(
		t, srv, http.MethodPost,
		"/api/v1/repo/gh/roborev-dev/*/refresh", nil,
	)
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())

	var resp settingsResponse
	require.NoError(json.NewDecoder(rr.Body).Decode(&resp))
	require.Equal(2, resp.Repos[0].MatchedRepoCount)
	assert.True(srv.syncer.IsTrackedRepo("roborev-dev", "middleman"))
	assert.True(srv.syncer.IsTrackedRepo("roborev-dev", "globber"))
	assert.False(srv.syncer.IsTrackedRepo("roborev-dev", "archived"))
}

func TestHandleRefreshRepoPersistsExpandedReposBeforeAsyncSync(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	includeRefreshRepo := atomic.Bool{}
	mock := &mockGH{
		listReposByOwnerFn: func(
			_ context.Context, owner string,
		) ([]*gh.Repository, error) {
			repos := []*gh.Repository{
				{
					Name:     new("middleman"),
					Owner:    &gh.User{Login: new(owner)},
					Archived: new(false),
				},
				{
					Name:     new("archived"),
					Owner:    &gh.User{Login: new(owner)},
					Archived: new(true),
				},
			}
			if includeRefreshRepo.Load() {
				repos = append(repos, &gh.Repository{
					Name:     new("review-bot"),
					Owner:    &gh.User{Login: new(owner)},
					Archived: new(false),
				})
			}
			return repos, nil
		},
	}
	srv, database, _ := setupTestServerWithConfigContent(t, `
sync_interval = "5m"
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "roborev-dev"
name = "*"
`, mock)
	srv.syncer.Stop()
	includeRefreshRepo.Store(true)

	rr := doJSON(
		t, srv, http.MethodPost,
		"/api/v1/repo/gh/roborev-dev/*/refresh", nil,
	)
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())

	repos, err := database.ListRepos(t.Context())
	require.NoError(err)
	names := make([]string, 0, len(repos))
	for _, repo := range repos {
		if repo.Owner == "roborev-dev" {
			names = append(names, repo.Name)
		}
	}
	assert.ElementsMatch([]string{"middleman", "review-bot"}, names)
}

func TestHandleRefreshRepoKeepsReposMatchedByOtherConfigEntries(t *testing.T) {
	assert := Assert.New(t)
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
					Name:     new("middleman"),
					Owner:    &gh.User{Login: new(owner)},
					Archived: new(false),
				},
			}, nil
		},
	}
	srv, _, _ := setupTestServerWithConfigContent(t, `
sync_interval = "5m"
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "roborev-dev"
name = "*"

[[repos]]
owner = "roborev-dev"
name = "worker"
`, mock)

	rr := doJSON(
		t, srv, http.MethodPost,
		"/api/v1/repo/gh/roborev-dev/*/refresh", nil,
	)
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())

	var resp settingsResponse
	require.NoError(json.NewDecoder(rr.Body).Decode(&resp))
	require.Len(resp.Repos, 2)
	assert.True(srv.syncer.IsTrackedRepo("roborev-dev", "middleman"))
	assert.True(srv.syncer.IsTrackedRepo("roborev-dev", "worker"))
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
					Name:     new("middleman"),
					Owner:    &gh.User{Login: new(owner)},
					Archived: new(false),
				},
			}, nil
		},
	}
	srv, _, _ := setupTestServerWithConfigContent(t, `
sync_interval = "5m"
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "roborev-dev"
name = "*"

[[repos]]
owner = "roborev-dev"
name = "tools"
`, mock)

	rr := doJSON(
		t, srv, http.MethodDelete,
		"/api/v1/repo/gh/roborev-dev/*", nil,
	)
	require.Equal(t, http.StatusNoContent, rr.Code, rr.Body.String())
	Assert.True(t, srv.syncer.IsTrackedRepo("roborev-dev", "tools"))
	Assert.False(t, srv.syncer.IsTrackedRepo("roborev-dev", "middleman"))
}

func TestHandleDeleteRepoUsesProviderHostQuery(t *testing.T) {
	require := require.New(t)
	srv, _, _ := setupTestServerWithConfigContent(t, `
sync_interval = "5m"
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"
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

	rr := doJSON(
		t, srv, http.MethodDelete,
		"/api/v1/host/gitlab.com/repo/gl/acme/widget", nil,
	)
	require.Equal(http.StatusNoContent, rr.Code, rr.Body.String())

	settingsRR := doJSON(t, srv, http.MethodGet, "/api/v1/settings", nil)
	require.Equal(http.StatusOK, settingsRR.Code, settingsRR.Body.String())
	var resp settingsResponse
	require.NoError(json.NewDecoder(settingsRR.Body).Decode(&resp))
	require.Len(resp.Repos, 1)
	Assert.Equal(t, "github", resp.Repos[0].Provider)
	Assert.Equal(t, "github.com", resp.Repos[0].PlatformHost)
}

func TestRefreshRepoPreservesExistingWhenResolutionFails(t *testing.T) {
	assert := Assert.New(t)
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
				Name:     new("middleman"),
				Owner:    &gh.User{Login: new(owner)},
				Archived: new(false),
			}}, nil
		},
	}
	srv, _, _ := setupTestServerWithConfigContent(t, `
sync_interval = "5m"
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "roborev-dev"
name = "*"
`, mock)
	// Prime the syncer with a previously resolved match so we can
	// verify it survives a failed refresh.
	srv.syncer.SetRepos([]ghclient.RepoRef{{
		Owner:        "roborev-dev",
		Name:         "middleman",
		PlatformHost: "github.com",
	}})

	rr := doJSON(
		t, srv, http.MethodPost,
		"/api/v1/repo/gh/roborev-dev/*/refresh", nil,
	)
	require.Equal(http.StatusBadGateway, rr.Code, rr.Body.String())
	assert.True(srv.syncer.IsTrackedRepo("roborev-dev", "middleman"))
}

func TestGetSettingsDoesNotCallGitHub(t *testing.T) {
	assert := Assert.New(t)
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
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"
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
	srv := NewWithConfig(
		database, syncer, nil, nil, cfg, cfgPath,
		ServerOptions{},
	)

	rr := doJSON(t, srv, http.MethodGet, "/api/v1/settings", nil)
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())

	var resp settingsResponse
	require.NoError(json.NewDecoder(rr.Body).Decode(&resp))
	require.Len(resp.Repos, 1)
	assert.Equal(1, resp.Repos[0].MatchedRepoCount)
}

func TestGlobMatchingIsCaseInsensitive(t *testing.T) {
	assert := Assert.New(t)
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
	srv, _, _ := setupTestServerWithConfigContent(t, `
sync_interval = "5m"
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "Widget-*"
`, mock)

	rr := doJSON(
		t, srv, http.MethodPost,
		"/api/v1/repo/gh/acme/Widget-*/refresh", nil,
	)
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	assert.True(srv.syncer.IsTrackedRepo("acme", "Widget-API"))
}

func TestAddRepoDoesNotDropConcurrentActivityChange(t *testing.T) {
	// Pre-check for the race fix: handleAddRepo must not
	// overwrite a concurrent handleUpdateSettings change.
	// The setup mutates s.cfg.Activity after the add's
	// pre-check but before its save, then verifies the
	// activity change survives in both memory and on disk.
	assert := Assert.New(t)
	require := require.New(t)
	srv, _, cfgPath := setupTestServerWithConfig(t)

	// Change activity via the update handler.
	rr := doJSON(
		t, srv, http.MethodPut, "/api/v1/settings",
		updateSettingsRequest{
			Activity: &config.Activity{
				ViewMode:  "threaded",
				TimeRange: "30d",
			},
		},
	)
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())

	// Add a new repo; handler should preserve activity.
	addRR := doJSON(
		t, srv, http.MethodPost, "/api/v1/repos",
		map[string]string{
			"provider": "github", "host": "github.com",
			"owner": "other-org", "name": "other-repo",
		},
	)
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
	assert := Assert.New(t)
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
				Name:     new("middleman"),
				Owner:    &gh.User{Login: new(owner)},
				Archived: new(false),
			}}, nil
		},
	}
	srv, _, _ := setupTestServerWithConfigContent(t, `
sync_interval = "5m"
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "roborev-dev"
name = "*"
`, mock)
	require.True(srv.syncer.IsTrackedRepo("roborev-dev", "middleman"))

	refreshDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		// Inline the request (no testify helpers) so the
		// linter does not flag assertions inside the goroutine.
		req := httptest.NewRequest(
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

	delRR := doJSON(
		t, srv, http.MethodDelete,
		"/api/v1/repo/gh/roborev-dev/*", nil,
	)
	require.Equal(http.StatusNoContent, delRR.Code, delRR.Body.String())
	require.False(srv.syncer.IsTrackedRepo("roborev-dev", "middleman"))

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
	assert.False(srv.syncer.IsTrackedRepo("roborev-dev", "middleman"),
		"deleted repo resurrected by concurrent refresh")
}

// TestHandleUpdateSettingsPreservesTmuxCommand drives a real
// settings-mutation HTTP call against a config that has a [tmux]
// section on disk, then reloads the config and asserts the Tmux
// command array survived the Save round-trip. This pins down the
// operator-visible contract: mutating activity settings (or any
// other field the UI touches) must not silently erase tmux.command.
func TestHandleUpdateSettingsPreservesTmuxCommand(t *testing.T) {
	assert := Assert.New(t)
	srv, _, cfgPath := setupTestServerWithConfigContent(t, `
sync_interval = "5m"
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "widget"

[tmux]
command = ["systemd-run", "--user", "--scope", "tmux"]
`, &mockGH{})

	body := updateSettingsRequest{
		Activity: &config.Activity{
			ViewMode:  "threaded",
			TimeRange: "30d",
		},
	}
	rr := doJSON(t, srv, http.MethodPut, "/api/v1/settings", body)
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
	assert := Assert.New(t)
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
	srv, _, _ := setupTestServerWithConfigContent(t, `
sync_interval = "5m"
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "widget"

[[repos]]
owner = "acme"
name = "widget-*"
`, mock)

	rr := doJSON(t, srv, http.MethodPost, "/api/v1/repos/preview", map[string]string{
		"provider": "github",
		"host":     "github.com",
		"owner":    " ACME ",
		"pattern":  " Widget* ",
	})
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())

	var resp repoPreviewResponse
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

func TestHandlePreviewReposFallsBackToListWhenExactLookupFails(t *testing.T) {
	assert := Assert.New(t)
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
	srv, _, _ := setupTestServerWithConfigContent(t, `
sync_interval = "5m"
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091
`, mock)

	rr := doJSON(t, srv, http.MethodPost, "/api/v1/repos/preview", map[string]string{
		"provider": "github",
		"host":     "github.com",
		"owner":    "mariusvniekerk",
		"pattern":  "dotfiles2026",
	})
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())

	var resp repoPreviewResponse
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
	assert := Assert.New(t)
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
	srv, _, _ := setupTestServerWithConfigContent(t, `
sync_interval = "5m"
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091
`, mock)

	rr := doJSON(t, srv, http.MethodPost, "/api/v1/repos/preview", map[string]string{
		"provider": "github",
		"host":     "github.com",
		"owner":    "anacondarecipes",
		"pattern":  "tesseract-feedstock",
	})
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())

	var resp repoPreviewResponse
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

func TestHandlePreviewReposRejectsInvalidPattern(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	srv, _, _ := setupTestServerWithConfig(t)

	rr := doJSON(t, srv, http.MethodPost, "/api/v1/repos/preview", map[string]string{
		"provider": "github",
		"host":     "github.com",
		"owner":    "acme*",
		"pattern":  "widget",
	})
	require.Equal(http.StatusBadRequest, rr.Code, rr.Body.String())
	assert.Contains(rr.Body.String(), "glob syntax in owner is not supported")

	rr = doJSON(t, srv, http.MethodPost, "/api/v1/repos/preview", map[string]string{
		"provider": "github",
		"host":     "github.com",
		"owner":    "acme",
		"pattern":  "widget[",
	})
	require.Equal(http.StatusBadRequest, rr.Code, rr.Body.String())
	assert.Contains(rr.Body.String(), "invalid glob pattern")
}

func TestHandlePreviewReposSupportsGitLabNamespaces(t *testing.T) {
	assert := Assert.New(t)
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
	srv, _, _ := setupTestServerWithConfigProviders(t, `
sync_interval = "5m"
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
platform = "gitlab"
platform_host = "gitlab.example.com"
owner = "Group/Subgroup"
name = "Project"
`, &mockGH{}, provider)

	rr := doJSON(t, srv, http.MethodPost, "/api/v1/repos/preview", map[string]string{
		"provider": "gitlab",
		"host":     "gitlab.example.com",
		"owner":    "Group/Subgroup",
		"pattern":  "Project*",
	})
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())

	var resp repoPreviewResponse
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
	assert := Assert.New(t)
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
	srv, _, _ := setupTestServerWithConfigProviders(t, `
sync_interval = "5m"
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
platform = "forgejo"
platform_host = "codeberg.example.com"
owner = "ForgeOrg"
name = "Widget"
repo_path = "ForgeOrg/Widget"
`, &mockGH{}, provider)

	rr := doJSON(t, srv, http.MethodPost, "/api/v1/repos/preview", map[string]string{
		"provider": "forgejo",
		"host":     "codeberg.example.com",
		"owner":    "ForgeOrg",
		"pattern":  "Widget*",
	})
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())

	body := rr.Body.String()
	var resp repoPreviewResponse
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
	assert := Assert.New(t)
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
	srv, _, cfgPath := setupTestServerWithConfigContent(t, `
sync_interval = "5m"
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "widget"
`, mock)
	callsAfterSetup := getCalls.Load()

	rr := doJSON(t, srv, http.MethodPost, "/api/v1/repos/bulk", map[string]any{
		"repos": []map[string]string{
			{"provider": "github", "host": "github.com", "owner": " acme ", "name": " api ", "repo_path": " acme/api "},
			{"provider": "github", "host": "github.com", "owner": "acme", "name": "worker", "repo_path": "acme/worker"},
			{"provider": "github", "host": "github.com", "owner": "acme", "name": "api", "repo_path": "acme/api"},
		},
	})
	require.Equal(http.StatusCreated, rr.Code, rr.Body.String())
	assert.GreaterOrEqual(getCalls.Load(), callsAfterSetup+2)

	var resp settingsResponse
	require.NoError(json.NewDecoder(rr.Body).Decode(&resp))
	require.Len(resp.Repos, 3)
	assert.Equal("acme", resp.Repos[1].Owner)
	assert.Equal("api", resp.Repos[1].Name)
	assert.Equal("worker", resp.Repos[2].Name)
	assert.True(srv.syncer.IsTrackedRepo("acme", "api"))
	assert.True(srv.syncer.IsTrackedRepo("acme", "worker"))

	cfg2, err := config.Load(cfgPath)
	require.NoError(err)
	require.Len(cfg2.Repos, 3)
	assert.Equal("api", cfg2.Repos[1].Name)
	assert.Equal("worker", cfg2.Repos[2].Name)
}

func TestHandleBulkAddReposPersistsGitLabProviderIdentity(t *testing.T) {
	assert := Assert.New(t)
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
	srv, database, cfgPath := setupTestServerWithConfigProviders(t, `
sync_interval = "5m"
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091
`, &mockGH{}, provider)

	rr := doJSON(t, srv, http.MethodPost, "/api/v1/repos/bulk", map[string]any{
		"repos": []map[string]string{
			{
				"provider":  "gitlab",
				"host":      "gitlab.example.com",
				"repo_path": "Group/Subgroup/Project",
			},
		},
	})
	require.Equal(http.StatusCreated, rr.Code, rr.Body.String())

	var resp settingsResponse
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
	assert.True(srv.syncer.IsTrackedRepoOnHost("Group/Subgroup", "Project", "gitlab.example.com"))

	dbRepo, err := database.GetRepoByIdentity(t.Context(), platform.DBRepoIdentity(ref))
	require.NoError(err)
	require.NotNil(dbRepo)
	assert.Equal("gitlab", dbRepo.Platform)
	assert.Equal("gitlab.example.com", dbRepo.PlatformHost)
	assert.Equal("Group/Subgroup/Project", dbRepo.RepoPath)
}

func TestWorktreeBasePathResolverMatchesProviderIdentity(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	srv := &Server{cfg: &config.Config{Repos: []config.Repo{
		{
			Platform:         "github",
			PlatformHost:     "forge.example.com",
			Owner:            "acme",
			Name:             "widget",
			WorktreeBasePath: "/tmp/github-widget",
		},
		{
			Platform:         "gitlab",
			PlatformHost:     "forge.example.com",
			Owner:            "acme",
			Name:             "widget",
			WorktreeBasePath: "/tmp/gitlab-widget",
		},
	}}}

	got, ok, err := srv.worktreeBasePathForRepo(
		t.Context(), "gitlab", "forge.example.com", "acme", "widget",
	)

	require.NoError(err)
	require.True(ok)
	assert.Equal("/tmp/gitlab-widget", got)
}

func TestHandleBulkAddReposPersistsGiteaProviderIdentity(t *testing.T) {
	assert := Assert.New(t)
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
	srv, database, cfgPath := setupTestServerWithConfigProviders(t, `
sync_interval = "5m"
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091
`, &mockGH{}, provider)

	rr := doJSON(t, srv, http.MethodPost, "/api/v1/repos/bulk", map[string]any{
		"repos": []map[string]string{
			{
				"provider":  "gitea",
				"host":      "gitea.example.com",
				"repo_path": "Team/Service",
			},
		},
	})
	require.Equal(http.StatusCreated, rr.Code, rr.Body.String())

	var resp settingsResponse
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
	assert.True(srv.syncer.IsTrackedRepoOnHost("Team", "Service", "gitea.example.com"))

	ref := platform.RepoRef{
		Platform:   platform.KindGitea,
		Host:       "gitea.example.com",
		Owner:      "Team",
		Name:       "Service",
		RepoPath:   "Team/Service",
		PlatformID: 6262,
	}
	dbRepo, err := database.GetRepoByIdentity(t.Context(), platform.DBRepoIdentity(ref))
	require.NoError(err)
	require.NotNil(dbRepo)
	assert.Equal("gitea", dbRepo.Platform)
	assert.Equal("gitea.example.com", dbRepo.PlatformHost)
	assert.Equal("Team/Service", dbRepo.RepoPath)
}

func TestHandleBulkAddReposValidationFailureChangesNothing(t *testing.T) {
	assert := Assert.New(t)
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
	srv, _, cfgPath := setupTestServerWithConfigContent(t, `
sync_interval = "5m"
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "widget"
`, mock)

	rr := doJSON(t, srv, http.MethodPost, "/api/v1/repos/bulk", map[string]any{
		"repos": []map[string]string{
			{"provider": "github", "host": "github.com", "owner": "acme", "name": "api", "repo_path": "acme/api"},
			{"provider": "github", "host": "github.com", "owner": "acme", "name": "missing", "repo_path": "acme/missing"},
		},
	})
	require.Equal(http.StatusBadGateway, rr.Code, rr.Body.String())
	assert.False(srv.syncer.IsTrackedRepo("acme", "api"))

	cfg2, err := config.Load(cfgPath)
	require.NoError(err)
	require.Len(cfg2.Repos, 1)
	assert.Equal("widget", cfg2.Repos[0].Name)
}

func TestHandleBulkAddReposSkipsAlreadyConfiguredBeforeValidation(t *testing.T) {
	assert := Assert.New(t)
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
	srv, _, cfgPath := setupTestServerWithConfigContent(t, `
sync_interval = "5m"
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "api"
`, mock)
	rr := doJSON(t, srv, http.MethodPost, "/api/v1/repos/bulk", map[string]any{
		"repos": []map[string]string{
			{"provider": "github", "host": "github.com", "owner": "acme", "name": "api", "repo_path": "acme/api"},
			{"provider": "github", "host": "github.com", "owner": "acme", "name": "worker", "repo_path": "acme/worker"},
		},
	})
	require.Equal(http.StatusCreated, rr.Code, rr.Body.String())
	assert.True(srv.syncer.IsTrackedRepo("acme", "worker"))

	cfg2, err := config.Load(cfgPath)
	require.NoError(err)
	require.Len(cfg2.Repos, 2)
	assert.Equal("worker", cfg2.Repos[1].Name)
}

func TestHandleBulkAddReposReturnsAlreadyConfiguredWhenAllSkippedBeforeValidation(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	var apiCalls atomic.Int32
	mock := &mockGH{
		getRepositoryFn: func(_ context.Context, owner, repo string) (*gh.Repository, error) {
			if repo == "api" {
				apiCalls.Add(1)
			}
			return &gh.Repository{
				Name:     new(repo),
				Owner:    &gh.User{Login: new(owner)},
				Archived: new(false),
			}, nil
		},
	}
	srv, _, _ := setupTestServerWithConfigContent(t, `
sync_interval = "5m"
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "api"
`, mock)
	rr := doJSON(t, srv, http.MethodPost, "/api/v1/repos/bulk", map[string]any{
		"repos": []map[string]string{
			{"provider": "github", "host": "github.com", "owner": "acme", "name": "api", "repo_path": "acme/api"},
		},
	})
	require.Equal(http.StatusBadRequest, rr.Code, rr.Body.String())
	assert.Contains(rr.Body.String(), "all selected repositories are already configured")
}

func TestHandleBulkAddReposSkipsAlreadyConfiguredAtApplyTime(t *testing.T) {
	assert := Assert.New(t)
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
	srv, _, _ := setupTestServerWithConfigContent(t, `
sync_interval = "5m"
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"
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
		req := httptest.NewRequest(http.MethodPost, "/api/v1/repos/bulk", bytes.NewReader(bulkBody.Bytes()))
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
	addRR := doJSON(t, srv, http.MethodPost, "/api/v1/repos", map[string]string{
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
	var resp settingsResponse
	require.NoError(json.NewDecoder(rr.Body).Decode(&resp))
	assert.Equal([]string{"widget", "api", "worker"}, []string{resp.Repos[0].Name, resp.Repos[1].Name, resp.Repos[2].Name})
}

// TestSetActiveWorktreeRoute pins the UI focus contract thin clients
// use: PUT /api/v1/ui/active-worktree records the focused worktree
// key, the served SPA config carries it, and an empty key clears it.
func TestSetActiveWorktreeRoute(t *testing.T) {
	require := require.New(t)
	srv, _ := setupTestServer(t)
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)

	put := func(body string) *http.Response {
		req, err := http.NewRequest(
			http.MethodPut,
			ts.URL+"/api/v1/ui/active-worktree",
			strings.NewReader(body),
		)
		require.NoError(err)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(err)
		return resp
	}

	resp := put(`{"key":"local:wt-alpha"}`)
	resp.Body.Close()
	require.Equal(http.StatusNoContent, resp.StatusCode)
	key, set := srv.ActiveWorktreeKey()
	require.True(set)
	require.Equal("local:wt-alpha", key)

	// Empty key clears the focus.
	resp = put(`{"key":""}`)
	resp.Body.Close()
	require.Equal(http.StatusNoContent, resp.StatusCode)
	key, set = srv.ActiveWorktreeKey()
	require.True(set)
	require.Empty(key)
}

// TestFleetSSHPeerSettingsRoutes pins the fleet host editing contract
// thin clients use: GET lists the configured ssh peers, PUT replaces
// the set (validated, persisted, rolled back on failure) and reports
// restart_required because the transport is wired at startup.
func TestFleetSSHPeerSettingsRoutes(t *testing.T) {
	require := require.New(t)
	srv, _, cfgPath := setupTestServerWithConfig(t)
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)

	type peersBody struct {
		SSHPeers        []config.FleetSSHPeer `json:"ssh_peers"`
		RestartRequired bool                  `json:"restart_required"`
	}
	get := func() peersBody {
		resp, err := http.Get(ts.URL + "/api/v1/settings/fleet/ssh-peers")
		require.NoError(err)
		defer resp.Body.Close()
		require.Equal(http.StatusOK, resp.StatusCode)
		var body peersBody
		require.NoError(json.NewDecoder(resp.Body).Decode(&body))
		return body
	}
	put := func(payload string) *http.Response {
		req, err := http.NewRequest(
			http.MethodPut,
			ts.URL+"/api/v1/settings/fleet/ssh-peers",
			strings.NewReader(payload),
		)
		require.NoError(err)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(err)
		return resp
	}

	require.Empty(get().SSHPeers)

	resp := put(`{"ssh_peers":[
		{"key":"epyc","name":"EPYC","destination":"wes@epyc.local","platform":"linux"}
	]}`)
	defer resp.Body.Close()
	require.Equal(http.StatusOK, resp.StatusCode)
	var updated peersBody
	require.NoError(json.NewDecoder(resp.Body).Decode(&updated))
	require.Len(updated.SSHPeers, 1)
	require.Equal("epyc", updated.SSHPeers[0].Key)
	require.True(updated.RestartRequired,
		"ssh peers are startup-bound; an edit demands a restart")

	// Persisted: the config file carries the peer.
	raw, err := os.ReadFile(cfgPath)
	require.NoError(err)
	require.Contains(string(raw), `wes@epyc.local`)

	// Invalid set (duplicate keys) rejects and rolls back.
	bad := put(`{"ssh_peers":[
		{"key":"dup","destination":"a@b"},
		{"key":"dup","destination":"c@d"}
	]}`)
	defer bad.Body.Close()
	require.Equal(http.StatusBadRequest, bad.StatusCode)
	require.Len(get().SSHPeers, 1, "failed update must not persist")
}
