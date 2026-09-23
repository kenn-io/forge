package settingstest

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/db"
	serverfake "go.kenn.io/forge/internal/testutil/serverfake"

	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/server"
	"go.kenn.io/forge/internal/testutil/dbtest"

	gitcmd "go.kenn.io/kit/git/cmd"
)

func TestProjectWorktreeRuntimeShellLifecycle(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	srv, projectID, worktreeID := setupProjectWorktreeRuntimeTest(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	resp := serverfake.HttpDo(t, ts, http.MethodGet,
		"/api/v1/projects/"+projectID+"/worktrees/"+worktreeID+"/runtime", nil,
	)
	t.Cleanup(func() {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
	})
	require.Equal(http.StatusOK, resp.StatusCode)
	var runtimeBody struct {
		LaunchTargets []map[string]any `json:"launch_targets"`
		Sessions      []map[string]any `json:"sessions"`
		ShellSession  *map[string]any  `json:"shell_session,omitempty"`
	}
	require.NoError(json.NewDecoder(resp.Body).Decode(&runtimeBody))
	resp.Body.Close()
	assert.NotEmpty(runtimeBody.LaunchTargets)
	assert.Empty(runtimeBody.Sessions)
	assert.Nil(runtimeBody.ShellSession)

	resp = serverfake.HttpDo(t, ts, http.MethodPost,
		"/api/v1/projects/"+projectID+"/worktrees/"+worktreeID+"/runtime/shell", nil,
	)
	t.Cleanup(func() {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
	})
	require.Equal(http.StatusOK, resp.StatusCode)
	var shell map[string]any
	require.NoError(json.NewDecoder(resp.Body).Decode(&shell))
	resp.Body.Close()
	shellKey, _ := shell["key"].(string)
	require.NotEmpty(shellKey)
	assert.Equal(projectID, shell["project_id"])
	assert.Equal(worktreeID, shell["worktree_id"])
	assert.Equal("plain_shell", shell["target_key"])
	assert.NotContains(shell, "workspace_id")

	resp = serverfake.HttpDo(t, ts, http.MethodGet,
		"/api/v1/projects/"+projectID+"/worktrees/"+worktreeID+"/runtime", nil,
	)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NoError(json.NewDecoder(resp.Body).Decode(&runtimeBody))
	resp.Body.Close()
	require.NotNil(runtimeBody.ShellSession)
	assert.Equal(shellKey, (*runtimeBody.ShellSession)["key"])

	resp = serverfake.HttpDo(t, ts, http.MethodGet,
		"/api/v1/projects/"+projectID+"/worktrees/"+worktreeID+
			"/runtime/sessions/"+shellKey+"/attach-spec",
		nil,
	)
	t.Cleanup(func() {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
	})
	require.Equal(http.StatusBadRequest, resp.StatusCode)
	payload, err := io.ReadAll(resp.Body)
	require.NoError(err)
	resp.Body.Close()
	assert.Contains(string(payload), "badRequest")
	assert.Contains(string(payload), "not tmux-backed")

	resp = serverfake.HttpDo(t, ts, http.MethodDelete,
		"/api/v1/projects/"+projectID+"/worktrees/"+worktreeID+"/runtime/sessions/"+shellKey,
		nil,
	)
	require.Equal(http.StatusNoContent, resp.StatusCode)
	resp.Body.Close()
}

func TestProjectWorktreeRuntimeLaunchTargetLifecycle(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	srv, projectID, worktreeID := setupProjectWorktreeRuntimeTest(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	body := serverfake.MustMarshal(t, map[string]any{"target_key": "helper"})
	resp := serverfake.HttpDo(t, ts, http.MethodPost,
		"/api/v1/projects/"+projectID+"/worktrees/"+worktreeID+"/runtime/sessions", body,
	)
	t.Cleanup(func() {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
	})
	require.Equal(http.StatusOK, resp.StatusCode)
	var session map[string]any
	require.NoError(json.NewDecoder(resp.Body).Decode(&session))
	resp.Body.Close()
	sessionKey, _ := session["key"].(string)
	require.NotEmpty(sessionKey)
	assert.Equal(projectID, session["project_id"])
	assert.Equal(worktreeID, session["worktree_id"])
	assert.Equal("helper", session["target_key"])
	assert.Equal("agent", session["kind"])
	assert.NotContains(session, "workspace_id")

	// Agent launches are never singletons: a second launch of the same target
	// starts a distinct session. Only plain_shell is reused, via /runtime/shell.
	resp = serverfake.HttpDo(t, ts, http.MethodPost,
		"/api/v1/projects/"+projectID+"/worktrees/"+worktreeID+"/runtime/sessions", body,
	)
	t.Cleanup(func() {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
	})
	require.Equal(http.StatusOK, resp.StatusCode)
	var second map[string]any
	require.NoError(json.NewDecoder(resp.Body).Decode(&second))
	resp.Body.Close()
	secondKey, _ := second["key"].(string)
	require.NotEmpty(secondKey)
	assert.NotEqual(sessionKey, secondKey)

	resp = serverfake.HttpDo(t, ts, http.MethodGet,
		"/api/v1/projects/"+projectID+"/worktrees/"+worktreeID+"/runtime", nil,
	)
	t.Cleanup(func() {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
	})
	require.Equal(http.StatusOK, resp.StatusCode)
	var runtimeBody struct {
		Sessions []map[string]any `json:"sessions"`
	}
	require.NoError(json.NewDecoder(resp.Body).Decode(&runtimeBody))
	resp.Body.Close()
	require.Len(runtimeBody.Sessions, 2)
	listedKeys := make([]string, 0, len(runtimeBody.Sessions))
	for _, s := range runtimeBody.Sessions {
		key, _ := s["key"].(string)
		listedKeys = append(listedKeys, key)
	}
	assert.ElementsMatch([]string{sessionKey, secondKey}, listedKeys)

	for _, key := range []string{sessionKey, secondKey} {
		resp = serverfake.HttpDo(t, ts, http.MethodDelete,
			"/api/v1/projects/"+projectID+"/worktrees/"+worktreeID+"/runtime/sessions/"+key,
			nil,
		)
		require.Equal(http.StatusNoContent, resp.StatusCode)
		resp.Body.Close()
	}
}

func TestProjectWorktreeRuntimeRejectsPlainShellOnSessionsRoute(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	srv, projectID, worktreeID := setupProjectWorktreeRuntimeTest(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	body := serverfake.MustMarshal(t, map[string]any{"target_key": "plain_shell"})
	resp := serverfake.HttpDo(t, ts, http.MethodPost,
		"/api/v1/projects/"+projectID+"/worktrees/"+worktreeID+"/runtime/sessions", body,
	)
	require.Equal(http.StatusBadRequest, resp.StatusCode)
	defer resp.Body.Close()
	payload, err := io.ReadAll(resp.Body)
	require.NoError(err)
	assert.Contains(string(payload), "badRequest")
	assert.Contains(string(payload), "runtime/shell")
}

func TestProjectWorktreeRuntimeAttachSpecRejectsNonOwnedSession(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	srv, projectID, worktreeID := setupProjectWorktreeRuntimeTest(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	resp := serverfake.HttpDo(t, ts, http.MethodGet,
		"/api/v1/projects/"+projectID+"/worktrees/"+worktreeID+
			"/runtime/sessions/missing-session/attach-spec",
		nil,
	)
	require.Equal(http.StatusNotFound, resp.StatusCode)
	defer resp.Body.Close()
	payload, err := io.ReadAll(resp.Body)
	require.NoError(err)
	assert.Contains(string(payload), "notFound")
}

func setupProjectWorktreeRuntimeTest(t *testing.T) (*server.Server, string, string) {
	return setupProjectWorktreeRuntimeTestWithTmux(t, nil)
}

func setupProjectWorktreeRuntimeTestWithTmux(
	t *testing.T, tmuxCommand []string,
) (*server.Server, string, string) {
	t.Helper()
	cfgContent := `
sync_interval = "5m"
github_token_env = "KENN_FORGE_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "widget"

[tmux]
agent_sessions = false

[[agents]]
key = "helper"
label = "Helper"
command = ["/bin/sh", "-c", "sleep 60"]
`
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(cfgContent), 0o644))
	cfg, err := config.Load(cfgPath)
	require.NoError(t, err)
	// Force tmux unavailable so runtime sessions start through the in-process
	// pty owner deterministically, regardless of whether the test host has
	// tmux installed (a real tmux would otherwise back the plain shell).
	if len(tmuxCommand) == 0 {
		tmuxCommand = []string{filepath.Join(dir, "missing-tmux")}
	}
	cfg.Tmux.Command = slices.Clone(tmuxCommand)
	database := dbtest.Open(t)
	mock := &serverfake.MockGH{}
	clients := map[string]ghclient.Client{"github.com": mock}
	resolved := ghclient.ResolveConfiguredRepos(t.Context(), clients, cfg.Repos)
	syncer := ghclient.NewSyncer(
		clients, database, nil, resolved.Expanded, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	srv := server.NewWithConfig(
		database, syncer, nil, nil, cfg, cfgPath,
		server.ServerOptions{
			WorktreeDir:                   filepath.Join(dir, "managed-worktrees"),
			PtyOwnerInProcess:             true,
			HostCheckAllowLoopbackAnyPort: true,
		},
	)
	t.Cleanup(func() { serverfake.GracefulShutdown(t, srv) })
	project := serverfake.CreateRuntimeTestProject(t, database, t.TempDir())
	worktreePath := t.TempDir()
	worktree, err := database.CreateProjectWorktree(t.Context(), db.CreateProjectWorktreeInput{
		ProjectID: project.ID,
		Branch:    "runtime",
		Path:      worktreePath,
	})
	require.NoError(t, err)
	return srv, project.ID, worktree.ID
}

func TestInitLocalOnlyGitRepoIgnoresInheritedGitEnv(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	require := require.New(t)
	assert := assert.New(t)

	host := t.TempDir()
	initCmd := gitcmd.New().Command(t.Context(), "", "init", "-q", "-b", "main", host)
	require.NoError(initCmd.Run(), "seed host repo")

	hostConfig := filepath.Join(host, ".git", "config")
	before, err := os.ReadFile(hostConfig)
	require.NoError(err)

	target := t.TempDir()
	t.Setenv("GIT_DIR", filepath.Join(host, ".git"))
	t.Setenv("GIT_WORK_TREE", target)

	require.NoError(initLocalOnlyGitRepo(t.Context(), target))

	after, err := os.ReadFile(hostConfig)
	require.NoError(err)
	assert.Equal(string(before), string(after),
		"git init helper must not write core.worktree to inherited host config")
	assert.FileExists(filepath.Join(target, ".git", "config"))
}

// initLocalOnlyGitRepo runs `git init` in dir without configuring any remote,
// matching the no-`gh` Add Existing path.
func initLocalOnlyGitRepo(ctx context.Context, dir string) error {
	cmd := gitcmd.New().Command(ctx, dir, "init", "-q")
	if err := cmd.Run(); err != nil {
		return err
	}
	return nil
}
