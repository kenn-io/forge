package settingstest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	gh "github.com/google/go-github/v91/github"
	shellquote "github.com/kballard/go-shellquote"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/apiclient/generated"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/federation"
	"go.kenn.io/forge/internal/federationauth"
	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/server"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/stacks"
	"go.kenn.io/forge/internal/testutil"
	"go.kenn.io/forge/internal/testutil/dbtest"
)

func setupTestServerWithConfig(
	t *testing.T,
) (*server.Server, *db.DB, string) {
	return setupTestServerWithConfigContent(t, `
sync_interval = "5m"
github_token_env = "KENN_FORGE_GITHUB_TOKEN"
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
) (*server.Server, *db.DB, string) {
	return setupTestServerWithConfigContentAndOptions(
		t, cfgContent, mock, server.ServerOptions{HostCheckAllowLoopbackAnyPort: true},
	)
}

func setupTestServerWithConfigContentAndOptions(
	t *testing.T,
	cfgContent string,
	mock *mockGH,
	options server.ServerOptions,
) (*server.Server, *db.DB, string) {
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
	return srv, database, cfgPath
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

func TestServerStartupAppliesTmuxSettingsToExistingDedicatedServer(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	record := installSettingsTmuxRecorder(t)
	srv, _, _ := setupTestServerWithConfigContentAndOptions(t, `
[terminal]
graphics = true
tmux_mouse = false
`, &mockGH{}, server.ServerOptions{
		HostCheckAllowLoopbackAnyPort: true,
		WorktreeDir:                   t.TempDir(),
	})
	t.Cleanup(func() { gracefulShutdown(t, srv) })

	commands, err := os.ReadFile(record)
	require.NoError(err)
	text := string(commands)
	assert.Contains(text, "-L kenn-forge set-option -q -g allow-passthrough on")
	assert.Contains(text, "-L kenn-forge set-option -q -s terminal-features[100] xterm-256color:sixel")
	assert.Contains(text, "-L kenn-forge set-option -q -g mouse off")
}

func TestHandleGetSettingsEncodesEmptyKataProjectsAsArray(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	// No [[kata_projects]] configured, so cfg.KataProjects is nil.
	srv, _, _ := setupTestServerWithConfigContent(t, `
sync_interval = "5m"
github_token_env = "KENN_FORGE_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "widget"
`, &mockGH{})

	rr := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/settings", nil)
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())

	// kata_projects is a required non-null array in the schema. Assert on the
	// raw wire value because decoding into a Go slice would hide a null/[]
	// difference.
	var raw map[string]json.RawMessage
	require.NoError(json.Unmarshal(rr.Body.Bytes(), &raw))
	require.Contains(raw, "kata_projects")
	assert.JSONEq("[]", string(raw["kata_projects"]))
}

func TestHandleGetSettingsEncodesEmptyRepoPresetsAsArray(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	srv, _, _ := setupTestServerWithConfig(t)

	rr := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/settings", nil)
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())

	var raw map[string]json.RawMessage
	require.NoError(json.Unmarshal(rr.Body.Bytes(), &raw))
	require.Contains(raw, "repo_presets")
	assert.JSONEq("[]", string(raw["repo_presets"]))
}

func TestHandleUpdateSettingsDefaultExecutionTarget(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	srv, _, cfgPath := setupTestServerWithConfig(t)
	client := setupTestClientWithBaseURL(t, srv, "http://127.0.0.1:8091")

	// A disconnected target stays selected; saving settings must not require it online.
	response, err := client.HTTP.UpdateSettingsWithResponse(t.Context(), &generated.UpdateSettingsRequestOptions{
		Body: &generated.UpdateSettingsBody{Workspaces: &generated.WorkspaceSettingsUpdate{
			DefaultExecutionTarget: new("devbox:connection-a"),
		}},
	})
	require.NoError(err)
	require.Equal(http.StatusOK, response.StatusCode, string(response.Body))
	require.NotNil(response.JSON200)
	assert.Equal(new("devbox:connection-a"), response.JSON200.Workspaces.DefaultExecutionTarget)
	persisted, err := config.Load(cfgPath)
	require.NoError(err)
	assert.Equal("devbox:connection-a", persisted.Workspaces.DefaultExecutionTarget)
	before, err := os.ReadFile(cfgPath)
	require.NoError(err)

	response, err = client.HTTP.UpdateSettingsWithResponse(t.Context(), &generated.UpdateSettingsRequestOptions{
		Body: &generated.UpdateSettingsBody{
			AirplaneMode: new(true),
			Workspaces: &generated.WorkspaceSettingsUpdate{
				DefaultExecutionTarget: new("devbox:"),
				AutoAssignOnCreate:     new(true),
			},
		},
	})
	require.Error(err)
	require.NotNil(response)
	require.Equal(http.StatusBadRequest, response.StatusCode, string(response.Body))
	require.NotNil(response.Error)
	require.NotNil(response.Error.Detail)
	assert.Contains(*response.Error.Detail, "workspaces.default_execution_target")
	current, err := client.HTTP.GetSettingsWithResponse(t.Context())
	require.NoError(err)
	require.NotNil(current.JSON200)
	assert.Equal(new("devbox:connection-a"), current.JSON200.Workspaces.DefaultExecutionTarget)
	assert.False(current.JSON200.Workspaces.AutoAssignOnCreate)
	assert.False(current.JSON200.AirplaneMode)
	after, err := os.ReadFile(cfgPath)
	require.NoError(err)
	assert.Equal(before, after, "rejected settings must not change the config file")

	response, err = client.HTTP.UpdateSettingsWithResponse(t.Context(), &generated.UpdateSettingsRequestOptions{
		Body: &generated.UpdateSettingsBody{Workspaces: &generated.WorkspaceSettingsUpdate{
			DefaultExecutionTarget: new(""),
		}},
	})
	require.NoError(err)
	require.Equal(http.StatusOK, response.StatusCode, string(response.Body))
	persisted, err = config.Load(cfgPath)
	require.NoError(err)
	assert.Empty(persisted.Workspaces.DefaultExecutionTarget)
}

func TestHandleAddRepo(t *testing.T) {
	srv, _, cfgPath := setupTestServerWithConfig(t)

	body := map[string]string{
		"provider": "github",
		"host":     "github.com",
		"owner":    "other-org",
		"name":     "other-repo",
	}
	rr := testutil.DoJSON(
		t, srv, http.MethodPost, "/api/v1/repos", body)

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
github_token_env = "KENN_FORGE_GITHUB_TOKEN"
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
			database, "github.com", "host", "rest",
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

	srv := server.NewWithConfig(
		database, syncer, nil, nil, cfg, cfgPath,
		server.ServerOptions{},
	)

	// Prime nextSyncAfter so the add-repo trigger exercises the same
	// cooldown path as a user clicking Sync right after a recent sync.
	syncer.RunOnce(t.Context())

	rr := testutil.DoJSON(
		t, srv, http.MethodPost, "/api/v1/repos",
		map[string]string{
			"provider": "github",
			"host":     "github.com",
			"owner":    "other-org",
			"name":     "other-repo",
		})

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
	rr := testutil.DoJSON(
		t, srv, http.MethodPost, "/api/v1/repos", body)

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
	addRR := testutil.DoJSON(
		t, srv, http.MethodPost, "/api/v1/repos", addBody)

	require.Equal(http.StatusCreated, addRR.Code, addRR.Body.String())

	rr := testutil.DoJSON(
		t, srv, http.MethodDelete,
		"/api/v1/repo/gh/acme/widget", nil)

	require.Equal(http.StatusNoContent, rr.Code, rr.Body.String())

	cfg2, err := config.Load(cfgPath)
	require.NoError(err)
	require.Len(cfg2.Repos, 1)
	assert.Equal(t, "other-org", cfg2.Repos[0].Owner)
}

func TestHandleDeleteLastRepo(t *testing.T) {
	srv, _, cfgPath := setupTestServerWithConfig(t)

	rr := testutil.DoJSON(
		t, srv, http.MethodDelete,
		"/api/v1/repo/gh/acme/widget", nil)

	require.Equal(t, http.StatusNoContent, rr.Code, rr.Body.String())

	cfg2, err := config.Load(cfgPath)
	require.NoError(t, err)
	assert.Empty(t, cfg2.Repos)
}

func TestHandlePreviewReposReportsUnconfiguredGitHubProvider(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	t.Setenv("MIDDLEMAN_GITHUB_TOKEN", "")

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	require.NoError(os.WriteFile(cfgPath, []byte(`
sync_interval = "5m"
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091
`), 0o644))
	cfg, err := config.Load(cfgPath)
	require.NoError(err)
	database := dbtest.Open(t)
	syncer := ghclient.NewSyncer(nil, database, nil, nil, time.Minute, nil, nil)
	t.Cleanup(syncer.Stop)
	srv := server.NewWithConfig(database, syncer, nil, nil, cfg, cfgPath,
		server.ServerOptions{HostCheckAllowLoopbackAnyPort: true})
	t.Cleanup(func() { gracefulShutdown(t, srv) })

	rr := testutil.DoJSON(t, srv, http.MethodPost, "/api/v1/repos/preview", map[string]string{
		"provider": "github", "host": "github.com",
		"owner": "acme", "pattern": "widget",
	})

	require.Equal(http.StatusBadRequest, rr.Code, rr.Body.String())
	assert.True(strings.HasPrefix(
		rr.Header().Get("Content-Type"), "application/problem+json",
	))
	var problem httpapi.ProblemError
	require.NoError(json.NewDecoder(rr.Body).Decode(&problem))
	assert.Equal(httpapi.CodeBadRequest, problem.Code)
	assert.Contains(problem.Detail, "provider_not_configured")
	assert.Contains(problem.Detail, "github.com")
}

func TestHandlePreviewReposReportsMissingOwnerRoute(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	router, err := ghclient.NewHostRouter(
		"github.com",
		&ghclient.Route{
			Key:    ghclient.RouteKey{Host: "github.com", Owner: "org-a"},
			Client: &mockGH{},
		},
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

	rr := testutil.DoJSON(t, srv, http.MethodPost, "/api/v1/repos/preview", map[string]string{
		"provider": "github", "host": "github.com",
		"owner": "org-b", "pattern": "*",
	})

	require.Equal(http.StatusBadGateway, rr.Code, rr.Body.String())
	assert.Contains(rr.Body.String(), "org-b")
	assert.Contains(rr.Body.String(), "github.com")
	assert.NotContains(rr.Body.String(), "org-a")
	var problem httpapi.ProblemError
	require.NoError(json.NewDecoder(rr.Body).Decode(&problem))
	assert.Equal(httpapi.CodeUpstreamError, problem.Code)
}

func TestHandlePreviewReposRejectsInvalidPattern(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	srv, _, _ := setupTestServerWithConfig(t)

	rr := testutil.DoJSON(t, srv, http.MethodPost, "/api/v1/repos/preview", map[string]string{
		"provider": "github",
		"host":     "github.com",
		"owner":    "acme*",
		"pattern":  "widget",
	})

	require.Equal(http.StatusBadRequest, rr.Code, rr.Body.String())
	assert.Contains(rr.Body.String(), "glob syntax in owner is not supported")

	rr = testutil.DoJSON(t, srv, http.MethodPost, "/api/v1/repos/preview", map[string]string{
		"provider": "github",
		"host":     "github.com",
		"owner":    "acme",
		"pattern":  "widget[",
	})

	require.Equal(http.StatusBadRequest, rr.Code, rr.Body.String())
	assert.Contains(rr.Body.String(), "invalid glob pattern")
}

func TestHandleBulkAddReposReturnsAlreadyConfiguredWhenAllSkippedBeforeValidation(t *testing.T) {
	assert := assert.New(t)
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
		},
	})

	require.Equal(http.StatusBadRequest, rr.Code, rr.Body.String())
	assert.Contains(rr.Body.String(), "all selected repositories are already configured")
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
		req, err := http.NewRequestWithContext(t.Context(),
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

func TestFleetSettingsExposePendingEnrollmentWithoutCredentialMaterial(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	dataDir := t.TempDir()
	enrollments, err := federation.Open(
		filepath.Join(dataDir, "enrollments.json"), federation.StoreOptions{},
	)
	require.NoError(err)
	credentials, err := federationauth.Open(filepath.Join(dataDir, "credentials.json"))
	require.NoError(err)
	const (
		hubID        = "0123456789abcdef0123456789abcdef"
		nodeID       = "fedcba9876543210fedcba9876543210"
		enrollmentID = "11111111111111111111111111111111"
		peerSecret   = "hub-calls-spoke-secret"
	)
	token, err := enrollments.CreateOneTimeToken(federation.Identity{
		NodeID: hubID, BaseURL: "https://hub.example",
	}, time.Now().Add(time.Minute))
	require.NoError(err)
	_, err = enrollments.Begin(t.Context(), token.Token, federation.JoinRequest{
		EnrollmentID: enrollmentID, NodeID: nodeID, Platform: "linux",
		BaseURL: "https://spoke.example", ProtocolVersion: federation.ProtocolVersion,
		HubCredential: peerSecret,
	})
	require.NoError(err)

	srv, _, _ := setupTestServerWithConfigContentAndOptions(t, `
host = "127.0.0.1"
port = 8091
[api]
require_auth = true
[fleet]
enabled = true
role = "hub"
base_url = "https://hub.example"
`, &mockGH{}, server.ServerOptions{
		HostCheckAllowLoopbackAnyPort: true,
		FederationSpokeID:             hubID,
		FederationEnrollments:         enrollments,
		FederationCredentials:         credentials,
	})
	response := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/settings/fleet", nil)
	require.Equal(http.StatusOK, response.Code)
	assert.Contains(response.Body.String(), enrollmentID)
	assert.NotContains(response.Body.String(), token.Token)
	assert.NotContains(response.Body.String(), peerSecret)
}

// TestNewServerRestoresProjectionWhenNativeStacksBootDisabled covers a daemon
// that starts with the preview already off. The setting can be edited while the
// daemon is stopped, or a previous run can save it and exit before reconciling,
// so binding the syncer preference is not enough: stored native ordering would
// drive the merge safeguard until each repository next synced, and forever for
// repositories no longer tracked.
func TestNewServerRestoresProjectionWhenNativeStacksBootDisabled(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	dir := t.TempDir()
	database := dbtest.Open(t)
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
	// The last run left native ordering behind.
	require.NoError(stacks.RunDetectionWithNativeStacks(ctx, database, repo.ID, []int{42}))

	// This run boots with the preview off.
	cfgPath := filepath.Join(dir, "config.toml")
	require.NoError(os.WriteFile(cfgPath, []byte(`
sync_interval = "5m"
github_token_env = "KENN_FORGE_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "widget"

[pull_requests]
prefer_github_native_stacks = false
`), 0o644))
	cfg, err := config.Load(cfgPath)
	require.NoError(err)
	clients := map[string]ghclient.Client{"github.com": &mockGH{}}
	syncer := ghclient.NewSyncer(clients, database, nil, nil, time.Minute, nil, nil)
	t.Cleanup(syncer.Stop)
	srv := server.NewWithConfig(database, syncer, nil, nil, cfg, cfgPath,
		server.ServerOptions{HostCheckAllowLoopbackAnyPort: true})
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	client := setupTestClientWithBaseURL(t, srv, "http://127.0.0.1:8091")

	// No sync has run, and the repository is not even tracked.
	resp, err := client.HTTP.GetPullStackWithResponse(ctx, &generated.GetPullStackRequestOptions{PathParams: &generated.GetPullStackPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(10)}})
	require.NoError(err)
	require.NotNil(resp.JSON200)
	require.NotNil(resp.JSON200.Members)
	assert.Equal([]int64{10, 11}, stackMemberNumbers(resp.JSON200.Members),
		"a server booting with the preview disabled must not serve native ordering")
}

func TestHandleGetSettingsReportsEmptyQuickActionsArray(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	srv, _, _ := setupTestServerWithConfig(t)
	rr := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/settings", nil)
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	var raw map[string]json.RawMessage
	require.NoError(json.Unmarshal(rr.Body.Bytes(), &raw))
	require.Contains(raw, "quick_actions")
	assert.JSONEq("[]", string(raw["quick_actions"]))
}
