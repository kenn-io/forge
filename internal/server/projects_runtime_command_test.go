package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	shellquote "github.com/kballard/go-shellquote"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/db"
	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/testutil/dbtest"
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
if [ "$1" = "-u" ]; then shift; fi
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
      if [ "$arg" = "@forge_owner" ]; then found_marker="next"; fi
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
github_token_env = "KENN_FORGE_GITHUB_TOKEN"
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
		t.Context(), db.CreateProjectWorktreeInput{
			ProjectID: project.ID,
			Branch:    "runtime",
			Path:      t.TempDir(),
		},
	)
	require.NoError(t, err)
	return srv, project.ID, worktree.ID, recordPath
}

func TestProjectWorktreeRuntimeListsStoredCommandSessionLabel(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	srv, projectID, worktreeID := setupProjectWorktreeCommandSessionTest(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	// A stored row without a live runtime session models a command session
	// surviving from before a kenn-forge restart.
	require.NoError(srv.db.UpsertProjectWorktreeTmuxSession(
		t.Context(), &db.ProjectWorktreeTmuxSession{
			WorktreeID:  worktreeID,
			SessionKey:  "surface:host:wt:shell:leaf",
			SessionName: "kenn-forge-stored-command",
			Label:       "Stored Shell",
		},
	))

	resp := httpDo(t, ts, http.MethodGet,
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
	require.Len(runtimeBody.Sessions, 1)
	assert.Equal("Stored Shell", runtimeBody.Sessions[0]["label"])
	assert.Equal("command", runtimeBody.Sessions[0]["kind"])
	assert.Equal("kenn-forge-stored-command", runtimeBody.Sessions[0]["tmux_session"])
}
