package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gitcmd "go.kenn.io/kit/git/cmd"
	"go.kenn.io/middleman/internal/config"
	"go.kenn.io/middleman/internal/db"
	ghclient "go.kenn.io/middleman/internal/github"
	"go.kenn.io/middleman/internal/server/workspaceapi"
	"go.kenn.io/middleman/internal/testutil/dbtest"
	"go.kenn.io/middleman/internal/workspace/localruntime"
)

// TestW1SliceAGate is the falsifiable capability gate from the convergence
// plan: it exercises the generic project + worktree registry plus
// launch-target discovery against a path with no `gh` context and an
// unrecognizable remote, and finishes by asserting neutral operation IDs in
// the live OpenAPI document. If this test passes, the W1 milestone is
// unblocked on the Middleman side.

func TestProjectWorktreeRuntimeShellLifecycle(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	srv, projectID, worktreeID := setupProjectWorktreeRuntimeTest(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	resp := httpDo(t, ts, http.MethodGet,
		"/api/v1/projects/"+projectID+"/worktrees/"+worktreeID+"/runtime", nil,
	)
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

	resp = httpDo(t, ts, http.MethodPost,
		"/api/v1/projects/"+projectID+"/worktrees/"+worktreeID+"/runtime/shell", nil,
	)
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

	resp = httpDo(t, ts, http.MethodGet,
		"/api/v1/projects/"+projectID+"/worktrees/"+worktreeID+"/runtime", nil,
	)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NoError(json.NewDecoder(resp.Body).Decode(&runtimeBody))
	resp.Body.Close()
	require.NotNil(runtimeBody.ShellSession)
	assert.Equal(shellKey, (*runtimeBody.ShellSession)["key"])

	resp = httpDo(t, ts, http.MethodGet,
		"/api/v1/projects/"+projectID+"/worktrees/"+worktreeID+
			"/runtime/sessions/"+shellKey+"/attach-spec",
		nil,
	)
	require.Equal(http.StatusBadRequest, resp.StatusCode)
	payload, err := io.ReadAll(resp.Body)
	require.NoError(err)
	resp.Body.Close()
	assert.Contains(string(payload), "badRequest")
	assert.Contains(string(payload), "not tmux-backed")

	resp = httpDo(t, ts, http.MethodDelete,
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

	body := mustMarshal(t, map[string]any{"target_key": "helper"})
	resp := httpDo(t, ts, http.MethodPost,
		"/api/v1/projects/"+projectID+"/worktrees/"+worktreeID+"/runtime/sessions", body,
	)
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
	resp = httpDo(t, ts, http.MethodPost,
		"/api/v1/projects/"+projectID+"/worktrees/"+worktreeID+"/runtime/sessions", body,
	)
	require.Equal(http.StatusOK, resp.StatusCode)
	var second map[string]any
	require.NoError(json.NewDecoder(resp.Body).Decode(&second))
	resp.Body.Close()
	secondKey, _ := second["key"].(string)
	require.NotEmpty(secondKey)
	assert.NotEqual(sessionKey, secondKey)

	resp = httpDo(t, ts, http.MethodGet,
		"/api/v1/projects/"+projectID+"/worktrees/"+worktreeID+"/runtime", nil,
	)
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
		resp = httpDo(t, ts, http.MethodDelete,
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

	body := mustMarshal(t, map[string]any{"target_key": "plain_shell"})
	resp := httpDo(t, ts, http.MethodPost,
		"/api/v1/projects/"+projectID+"/worktrees/"+worktreeID+"/runtime/sessions", body,
	)
	require.Equal(http.StatusBadRequest, resp.StatusCode)
	defer resp.Body.Close()
	payload, err := io.ReadAll(resp.Body)
	require.NoError(err)
	assert.Contains(string(payload), "badRequest")
	assert.Contains(string(payload), "runtime/shell")
}

func TestProjectWorktreeRuntimeRejectsMismatchedProject(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	srv, projectID, worktreeID := setupProjectWorktreeRuntimeTest(t)
	other := createRuntimeTestProject(t, srv.db, t.TempDir())
	ts := httptest.NewServer(srv)
	defer ts.Close()

	resp := httpDo(t, ts, http.MethodGet,
		"/api/v1/projects/"+other.ID+"/worktrees/"+worktreeID+"/runtime", nil,
	)
	require.Equal(http.StatusNotFound, resp.StatusCode)
	defer resp.Body.Close()
	payload, err := io.ReadAll(resp.Body)
	require.NoError(err)
	assert.Contains(string(payload), "notFound")
	assert.NotContains(string(payload), projectID)
}

func TestProjectWorktreeRuntimeAttachSpecUsesStoredTmuxSession(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	tmuxScript := writeProjectRuntimeTmuxProbe(t, "project-runtime-live", 0, "")
	srv, projectID, worktreeID := setupProjectWorktreeRuntimeTestWithTmux(
		t, []string{tmuxScript, "--socket", "runtime"},
	)
	sessionKey := worktreeID + "_helper"
	require.NoError(srv.db.UpsertProjectWorktreeTmuxSession(
		context.Background(),
		&db.ProjectWorktreeTmuxSession{
			WorktreeID:  worktreeID,
			SessionKey:  sessionKey,
			SessionName: "project-runtime-live",
			TargetKey:   "helper",
			CreatedAt:   time.Now().UTC(),
		},
	))

	ts := httptest.NewServer(srv)
	defer ts.Close()

	resp := httpDo(t, ts, http.MethodGet,
		"/api/v1/projects/"+projectID+"/worktrees/"+worktreeID+
			"/runtime/sessions/"+sessionKey+"/attach-spec",
		nil,
	)
	require.Equal(http.StatusOK, resp.StatusCode)
	defer resp.Body.Close()
	var spec workspaceapi.RuntimeAttachSpecResponse
	require.NoError(json.NewDecoder(resp.Body).Decode(&spec))
	assert.Equal(1, spec.Version)
	assert.Equal("tmux", spec.Kind)
	assert.Equal(sessionKey, spec.SessionKey)
	assert.Equal("helper", spec.TargetKey)
	assert.Equal("project-runtime-live", spec.TmuxSession)
	assert.Equal(
		[]string{
			tmuxScript, "--socket", "runtime",
			"-u", "attach-session", "-t", "project-runtime-live",
		},
		spec.Command,
	)
	assert.True(spec.RequiresLocalHost)
}

func TestProjectWorktreeRuntimeAttachSpecRejectsMissingTmuxSession(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	tmuxScript := writeProjectRuntimeTmuxProbe(t, "project-runtime-live", 1, "can't find session")
	srv, projectID, worktreeID := setupProjectWorktreeRuntimeTestWithTmux(
		t, []string{tmuxScript},
	)
	sessionKey := worktreeID + "_helper"
	require.NoError(srv.db.UpsertProjectWorktreeTmuxSession(
		context.Background(),
		&db.ProjectWorktreeTmuxSession{
			WorktreeID:  worktreeID,
			SessionKey:  sessionKey,
			SessionName: "project-runtime-live",
			TargetKey:   "helper",
			CreatedAt:   time.Now().UTC(),
		},
	))

	ts := httptest.NewServer(srv)
	defer ts.Close()

	resp := httpDo(t, ts, http.MethodGet,
		"/api/v1/projects/"+projectID+"/worktrees/"+worktreeID+
			"/runtime/sessions/"+sessionKey+"/attach-spec",
		nil,
	)
	require.Equal(http.StatusNotFound, resp.StatusCode)
	defer resp.Body.Close()
	payload, err := io.ReadAll(resp.Body)
	require.NoError(err)
	assert.Contains(string(payload), "notFound")
}

func TestProjectWorktreeRuntimeAttachSpecRejectsNonOwnedSession(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	srv, projectID, worktreeID := setupProjectWorktreeRuntimeTest(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	resp := httpDo(t, ts, http.MethodGet,
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

func TestProjectWorktreeRuntimeStopFallsBackToStoredTmuxSession(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	record := filepath.Join(t.TempDir(), "tmux-record")
	tmuxScript := writeProjectRuntimeTmuxRecorder(t)
	t.Setenv("TMUX_RECORD", record)
	srv, projectID, worktreeID := setupProjectWorktreeRuntimeTestWithTmux(
		t, []string{tmuxScript},
	)

	targetKey := "helper"
	sessionName := "project-runtime-stored"
	sessionKey := worktreeID + "_helper"
	require.NoError(srv.db.UpsertProjectWorktreeTmuxSession(
		context.Background(),
		&db.ProjectWorktreeTmuxSession{
			WorktreeID:  worktreeID,
			SessionKey:  sessionKey,
			SessionName: sessionName,
			TargetKey:   targetKey,
			CreatedAt:   time.Now().UTC(),
		},
	))

	ts := httptest.NewServer(srv)
	defer ts.Close()

	resp := httpDo(t, ts, http.MethodGet,
		"/api/v1/projects/"+projectID+"/worktrees/"+worktreeID+"/runtime", nil,
	)
	require.Equal(http.StatusOK, resp.StatusCode)
	var runtimeBody struct {
		Sessions []map[string]any `json:"sessions"`
	}
	require.NoError(json.NewDecoder(resp.Body).Decode(&runtimeBody))
	resp.Body.Close()
	require.Len(runtimeBody.Sessions, 1)
	assert.Equal(sessionKey, runtimeBody.Sessions[0]["key"])
	assert.Equal(targetKey, runtimeBody.Sessions[0]["target_key"])

	resp = httpDo(t, ts, http.MethodDelete,
		"/api/v1/projects/"+projectID+"/worktrees/"+worktreeID+
			"/runtime/sessions/"+sessionKey,
		nil,
	)
	require.Equal(http.StatusNoContent, resp.StatusCode)
	resp.Body.Close()

	stored, err := srv.db.ListProjectWorktreeTmuxSessions(
		context.Background(), worktreeID,
	)
	require.NoError(err)
	assert.Empty(stored)
	recorded, err := os.ReadFile(record)
	require.NoError(err)
	assert.Contains(string(recorded), "kill-session -t "+sessionName)
}

func TestProjectWorktreeRuntimeExitForgetsStoredTmuxSession(t *testing.T) {
	require := require.New(t)

	srv, _, worktreeID := setupProjectWorktreeRuntimeTest(t)
	scope := workspaceapi.ProjectWorktreeRuntimeScope(worktreeID)
	targetKey := "helper"
	sessionName := "project-runtime-exited"
	sessionKey := worktreeID + "_helper"
	createdAt := time.Now().UTC()
	require.NoError(srv.db.UpsertProjectWorktreeTmuxSession(
		context.Background(),
		&db.ProjectWorktreeTmuxSession{
			WorktreeID:  worktreeID,
			SessionKey:  sessionKey,
			SessionName: sessionName,
			TargetKey:   targetKey,
			CreatedAt:   createdAt,
		},
	))

	srv.handleRuntimeSessionExit(localruntime.SessionInfo{
		Key:         sessionKey,
		WorkspaceID: scope,
		TargetKey:   targetKey,
		TmuxSession: sessionName,
		CreatedAt:   createdAt,
	})

	require.Eventually(func() bool {
		stored, err := srv.db.ListProjectWorktreeTmuxSessions(
			context.Background(), worktreeID,
		)
		return err == nil && len(stored) == 0
	}, time.Second, 10*time.Millisecond)
}

// TestRegisterProject_RejectsPartialPlatformIdentity pins the contract that
// platform_identity is all-or-nothing. Two paths reject it:
//   - Missing field: Huma's JSON Schema validator returns 422 (the
//     platformIdentityPayload struct fields are non-pointer and
//     non-omitempty, so all three are required).
//   - Whitespace-only field: passes the schema validator but fails the
//     handler's TrimSpace check and returns 400. This is the embedder-
//     facing failure mode for "I sent the field but the value is junk".

func setupProjectWorktreeRuntimeTest(t *testing.T) (*Server, string, string) {
	return setupProjectWorktreeRuntimeTestWithTmux(t, nil)
}

func setupProjectWorktreeRuntimeTestWithTmux(
	t *testing.T, tmuxCommand []string,
) (*Server, string, string) {
	t.Helper()
	cfgContent := `
sync_interval = "5m"
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"
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
	mock := &mockGH{}
	clients := map[string]ghclient.Client{"github.com": mock}
	resolved := ghclient.ResolveConfiguredRepos(t.Context(), clients, cfg.Repos)
	syncer := ghclient.NewSyncer(
		clients, database, nil, resolved.Expanded, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	srv := NewWithConfig(
		database, syncer, nil, nil, cfg, cfgPath,
		ServerOptions{
			WorktreeDir:                   filepath.Join(dir, "managed-worktrees"),
			PtyOwnerInProcess:             true,
			HostCheckAllowLoopbackAnyPort: true,
		},
	)
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	project := createRuntimeTestProject(t, database, t.TempDir())
	worktreePath := t.TempDir()
	worktree, err := database.CreateProjectWorktree(context.Background(), db.CreateProjectWorktreeInput{
		ProjectID: project.ID,
		Branch:    "runtime",
		Path:      worktreePath,
	})
	require.NoError(t, err)
	return srv, project.ID, worktree.ID
}

func createRuntimeTestProject(t *testing.T, database *db.DB, localPath string) *db.Project {
	t.Helper()
	project, err := database.CreateProject(context.Background(), db.CreateProjectInput{
		DisplayName: "runtime-project",
		LocalPath:   localPath,
	})
	require.NoError(t, err)
	return project
}

func writeProjectRuntimeTmuxRecorder(t *testing.T) string {
	t.Helper()
	script := filepath.Join(t.TempDir(), "fake-tmux")
	body := "#!/bin/sh\n" +
		`printf '%s\n' "$*" >> "$TMUX_RECORD"` + "\n" +
		"exit 0\n"
	require.NoError(t, os.WriteFile(script, []byte(body), 0o755))
	return script
}

func writeProjectRuntimeTmuxProbe(
	t *testing.T,
	expectedSession string,
	exitCode int,
	stderr string,
) string {
	t.Helper()
	script := filepath.Join(t.TempDir(), "fake-tmux")
	body := "#!/bin/sh\n" +
		"if [ \"$1\" = \"--socket\" ]; then shift 2; fi\n" +
		"if [ \"$1\" != \"has-session\" ] || [ \"$2\" != \"-t\" ] || [ \"$3\" != \"" + expectedSession + "\" ]; then\n" +
		"  echo unexpected tmux argv: \"$@\" >&2\n" +
		"  exit 2\n" +
		"fi\n"
	if stderr != "" {
		body += "echo " + shellQuoteTest(stderr) + " >&2\n"
	}
	body += "exit " + strconv.Itoa(exitCode) + "\n"
	require.NoError(t, os.WriteFile(script, []byte(body), 0o755))
	return script
}

func shellQuoteTest(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
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

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	out, err := json.Marshal(v)
	require.NoError(t, err)
	return out
}

func httpDo(t *testing.T, ts *httptest.Server, method, path string, body []byte) *http.Response {
	t.Helper()
	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, ts.URL+path, bodyReader)
	require.NoError(t, err)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	} else if method == http.MethodPost || method == http.MethodDelete ||
		method == http.MethodPut || method == http.MethodPatch {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := ts.Client().Do(req)
	require.NoError(t, err)
	return resp
}
