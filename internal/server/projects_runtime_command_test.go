package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	shellquote "github.com/kballard/go-shellquote"
	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/middleman/internal/config"
	"go.kenn.io/middleman/internal/db"
	ghclient "go.kenn.io/middleman/internal/github"
	"go.kenn.io/middleman/internal/testutil/dbtest"
)

// writeRuntimeCommandFakeTmux writes a fake tmux for server-level command
// session tests: argv is recorded, session existence is tracked in a state
// file, the owner marker query echoes whatever marker new-session stored,
// and attach blocks so launched sessions stay running.
func writeRuntimeCommandFakeTmux(t *testing.T) (tmuxPath, recordPath string) {
	t.Helper()
	dir := t.TempDir()
	recordPath = filepath.Join(dir, "tmux-record")
	statePath := filepath.Join(dir, "tmux-session-exists")
	markerPath := filepath.Join(dir, "tmux-owner-marker")
	tmuxPath = filepath.Join(dir, "tmux")
	script := fmt.Sprintf(`#!/bin/sh
printf '%%s\0' "$#" "$@" >> %[1]s
case "$1" in
  has-session)
    if [ -f %[2]s ]; then exit 0; fi
    echo "can't find session: $3" >&2
    exit 1
    ;;
  new-session)
    touch %[2]s
    found_marker=""
    for arg in "$@"; do
      if [ "$found_marker" = "next" ]; then
        printf '%%s\n' "$arg" > %[3]s
        found_marker="done"
      fi
      if [ "$arg" = "@middleman_owner" ]; then found_marker="next"; fi
    done
    exit 0
    ;;
  show-options)
    if [ -f %[3]s ]; then cat %[3]s; fi
    exit 0
    ;;
  kill-session)
    rm -f %[2]s
    exit 0
    ;;
  attach-session)
    exec sleep 60
    ;;
esac
exit 0
`,
		shellquote.Join(recordPath),
		shellquote.Join(statePath),
		shellquote.Join(markerPath),
	)
	require.NoError(t, os.WriteFile(tmuxPath, []byte(script), 0o755))
	return tmuxPath, recordPath
}

func setupProjectWorktreeCommandSessionTest(
	t *testing.T,
) (srv *Server, projectID string, worktreeID string) {
	t.Helper()
	srv, projectID, worktreeID, _ = setupProjectWorktreeCommandSessionTestWithRecord(t)
	return srv, projectID, worktreeID
}

func setupProjectWorktreeCommandSessionTestWithRecord(
	t *testing.T,
) (srv *Server, projectID string, worktreeID string, recordPath string) {
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
`
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(cfgContent), 0o644))
	cfg, err := config.Load(cfgPath)
	require.NoError(t, err)
	tmuxPath, recordPath := writeRuntimeCommandFakeTmux(t)
	cfg.Tmux.Command = []string{tmuxPath}
	database := dbtest.Open(t)
	mock := &mockGH{}
	clients := map[string]ghclient.Client{"github.com": mock}
	resolved := ghclient.ResolveConfiguredRepos(t.Context(), clients, cfg.Repos)
	syncer := ghclient.NewSyncer(
		clients, database, nil, resolved.Expanded, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	srv = NewWithConfig(
		database, syncer, nil, nil, cfg, cfgPath,
		ServerOptions{
			WorktreeDir:                   filepath.Join(dir, "managed-worktrees"),
			PtyOwnerInProcess:             true,
			HostCheckAllowLoopbackAnyPort: true,
		},
	)
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	project := createRuntimeTestProject(t, database, t.TempDir())
	worktree, err := database.CreateProjectWorktree(
		context.Background(), db.CreateProjectWorktreeInput{
			ProjectID: project.ID,
			Branch:    "runtime",
			Path:      t.TempDir(),
		},
	)
	require.NoError(t, err)
	return srv, project.ID, worktree.ID, recordPath
}

func TestProjectWorktreeRuntimeCommandSessionLifecycle(t *testing.T) {
	requirePTYAvailable(t)
	require := require.New(t)
	assert := Assert.New(t)

	srv, projectID, worktreeID := setupProjectWorktreeCommandSessionTest(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	sessionsPath := "/api/v1/projects/" + projectID +
		"/worktrees/" + worktreeID + "/runtime/sessions"

	body := mustMarshal(t, map[string]any{
		"session_key": "surface:host:wt:shell:leaf",
		"command":     []string{"/bin/sh", "-lc", "exec sleep 60"},
		"env":         map[string]string{"CUSTOM_SESSION_VAR": "custom-value"},
		"label":       "My Shell",
	})
	resp := httpDo(t, ts, http.MethodPost, sessionsPath, body)
	require.Equal(http.StatusOK, resp.StatusCode)
	var session map[string]any
	require.NoError(json.NewDecoder(resp.Body).Decode(&session))
	resp.Body.Close()
	assert.Equal("surface:host:wt:shell:leaf", session["key"])
	assert.Equal("My Shell", session["label"])
	assert.Equal("command", session["kind"])
	tmuxSession, _ := session["tmux_session"].(string)
	require.NotEmpty(tmuxSession)

	// Re-ensure with the same session key returns the live session
	// instead of launching a duplicate.
	resp = httpDo(t, ts, http.MethodPost, sessionsPath, body)
	require.Equal(http.StatusOK, resp.StatusCode)
	var second map[string]any
	require.NoError(json.NewDecoder(resp.Body).Decode(&second))
	resp.Body.Close()
	assert.Equal(session["key"], second["key"])
	assert.Equal(tmuxSession, second["tmux_session"])

	resp = httpDo(t, ts, http.MethodGet,
		"/api/v1/projects/"+projectID+"/worktrees/"+worktreeID+"/runtime", nil,
	)
	require.Equal(http.StatusOK, resp.StatusCode)
	var runtimeBody struct {
		Sessions []map[string]any `json:"sessions"`
	}
	require.NoError(json.NewDecoder(resp.Body).Decode(&runtimeBody))
	resp.Body.Close()
	require.Len(runtimeBody.Sessions, 1)
	assert.Equal("My Shell", runtimeBody.Sessions[0]["label"])

	resp = httpDo(t, ts, http.MethodGet,
		sessionsPath+"/surface:host:wt:shell:leaf/attach-spec", nil,
	)
	require.Equal(http.StatusOK, resp.StatusCode)
	var spec map[string]any
	require.NoError(json.NewDecoder(resp.Body).Decode(&spec))
	resp.Body.Close()
	assert.Equal("tmux", spec["kind"])
	assert.Equal(tmuxSession, spec["tmux_session"])

	resp = httpDo(t, ts, http.MethodDelete,
		sessionsPath+"/surface:host:wt:shell:leaf", nil,
	)
	require.Equal(http.StatusNoContent, resp.StatusCode)
	resp.Body.Close()
}

func TestProjectWorktreeRuntimeCommandSessionValidation(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

	srv, projectID, worktreeID := setupProjectWorktreeCommandSessionTest(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	sessionsPath := "/api/v1/projects/" + projectID +
		"/worktrees/" + worktreeID + "/runtime/sessions"

	cases := []map[string]any{
		// target_key and command are mutually exclusive.
		{"target_key": "helper", "command": []string{"/bin/sh"}},
		// session_key requires a command launch.
		{"target_key": "helper", "session_key": "surface:k"},
		// env requires a command launch.
		{"target_key": "helper", "env": map[string]string{"A_B": "v"}},
		// env keys must be shell identifiers.
		{"command": []string{"/bin/sh"}, "env": map[string]string{"BAD-KEY": "v"}},
		// neither target_key nor command.
		{},
	}
	for _, payload := range cases {
		resp := httpDo(t, ts, http.MethodPost,
			sessionsPath, mustMarshal(t, payload),
		)
		body, err := io.ReadAll(resp.Body)
		require.NoError(err)
		resp.Body.Close()
		assert.Equal(
			http.StatusBadRequest, resp.StatusCode,
			"payload %v: %s", payload, string(body),
		)
		assert.Contains(
			string(body), "validationError",
			"payload %v", payload,
		)
	}
}

func TestProjectWorktreeRuntimeListsStoredCommandSessionLabel(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

	srv, projectID, worktreeID := setupProjectWorktreeCommandSessionTest(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	// A stored row without a live runtime session models a command session
	// surviving from before a middleman restart.
	require.NoError(srv.db.UpsertProjectWorktreeTmuxSession(
		context.Background(), &db.ProjectWorktreeTmuxSession{
			WorktreeID:  worktreeID,
			SessionKey:  "surface:host:wt:shell:leaf",
			SessionName: "middleman-stored-command",
			Label:       "Stored Shell",
		},
	))

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
	assert.Equal("Stored Shell", runtimeBody.Sessions[0]["label"])
	assert.Equal("command", runtimeBody.Sessions[0]["kind"])
	assert.Equal("middleman-stored-command", runtimeBody.Sessions[0]["tmux_session"])
}

// TestProjectWorktreeCommandSessionExpandsHomeCWD mirrors the host-level
// expansion proof on the worktree launch route: a fleet client's
// home-relative cwd resolves against this daemon's home before tmux
// launches, because only the executing host knows its home directory.
func TestProjectWorktreeCommandSessionExpandsHomeCWD(t *testing.T) {
	require := require.New(t)

	home, err := os.UserHomeDir()
	require.NoError(err)

	srv, projectID, worktreeID, recordPath :=
		setupProjectWorktreeCommandSessionTestWithRecord(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	body := mustMarshal(t, map[string]any{
		"session_key": "surface:p:w:shell:home",
		"command":     []string{"/bin/sh", "-lc", "exec sleep 60"},
		"cwd":         "~",
	})
	resp := httpDo(t, ts, http.MethodPost,
		"/api/v1/projects/"+projectID+"/worktrees/"+worktreeID+
			"/runtime/sessions", body)
	require.Equal(http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	record, err := os.ReadFile(recordPath)
	require.NoError(err)
	args := strings.Split(string(record), "\x00")
	cwdArg := ""
	for i, arg := range args {
		if arg == "-c" && i+1 < len(args) {
			cwdArg = args[i+1]
		}
	}
	require.Equal(home, cwdArg,
		"tmux new-session must receive the expanded home, not a literal ~")
}
