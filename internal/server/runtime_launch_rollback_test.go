package server

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	shellquote "github.com/kballard/go-shellquote"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/db"
	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/server/workspaceapi"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/workspace/localruntime"
)

type runtimeRollbackFakeTmux struct {
	command            []string
	recordPath         string
	statePath          string
	attachStartedPath  string
	exitAttachPath     string
	killFailurePath    string
	launchPath         string
	launchHistoryPath  string
	killedLaunchesPath string
}

type runtimeLaunchRollbackFixture struct {
	server     *Server
	projectID  string
	worktreeID string
	tmux       runtimeRollbackFakeTmux
}

func setupRuntimeLaunchRollbackFixture(
	t *testing.T,
	failKill bool,
) runtimeLaunchRollbackFixture {
	t.Helper()
	dir := t.TempDir()
	tmux := runtimeRollbackFakeTmux{
		recordPath:         filepath.Join(dir, "tmux-record"),
		statePath:          filepath.Join(dir, "tmux-session-exists"),
		attachStartedPath:  filepath.Join(dir, "tmux-attach-started"),
		exitAttachPath:     filepath.Join(dir, "tmux-exit-attach"),
		killFailurePath:    filepath.Join(dir, "tmux-kill-failure"),
		launchPath:         filepath.Join(dir, "tmux-launch-id"),
		launchHistoryPath:  filepath.Join(dir, "tmux-launch-history"),
		killedLaunchesPath: filepath.Join(dir, "tmux-killed-launches"),
	}
	if failKill {
		require.NoError(t, os.WriteFile(tmux.killFailurePath, nil, 0o600))
	}
	ownerPath := filepath.Join(dir, "tmux-owner-marker")
	tmuxPath := filepath.Join(dir, "fake-tmux")
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
    : > %[2]s
    marker=""
    for arg in "$@"; do
      if [ "$marker" = "owner" ]; then
        printf '%%s\n' "$arg" > %[3]s
        marker="done"
      elif [ "$marker" = "launch" ]; then
        printf '%%s\n' "$arg" > %[7]s
        printf '%%s\n' "$arg" >> %[8]s
        marker="done"
      elif [ "$arg" = "@forge_owner" ]; then
        marker="owner"
      elif [ "$arg" = "@forge_launch" ]; then
        marker="launch"
      fi
    done
    exit 0
    ;;
  show-options)
    option=""
    for arg in "$@"; do option="$arg"; done
    if [ "$option" = "@forge_launch" ]; then
      if [ -f %[7]s ]; then cat %[7]s; fi
    elif [ -f %[3]s ]; then
      cat %[3]s
    fi
    exit 0
    ;;
  set-option)
    exit 0
    ;;
  attach-session)
    : > %[4]s
    while [ ! -f %[5]s ]; do sleep 0.01; done
    exit 0
    ;;
  kill-session)
    if [ -f %[7]s ]; then cat %[7]s >> %[9]s; fi
    if [ -f %[6]s ]; then
      echo "forced kill-session failure" >&2
      exit 1
    fi
    rm -f %[2]s %[7]s
    exit 0
    ;;
esac
exit 0
`,
		shellquote.Join(tmux.recordPath),
		shellquote.Join(tmux.statePath),
		shellquote.Join(ownerPath),
		shellquote.Join(tmux.attachStartedPath),
		shellquote.Join(tmux.exitAttachPath),
		shellquote.Join(tmux.killFailurePath),
		shellquote.Join(tmux.launchPath),
		shellquote.Join(tmux.launchHistoryPath),
		shellquote.Join(tmux.killedLaunchesPath),
	)
	require.NoError(t, os.WriteFile(tmuxPath, []byte(script), 0o755))
	tmux.command = []string{tmuxPath}

	cfgPath := filepath.Join(dir, "config.toml")
	cfgContent := `
sync_interval = "5m"
github_token_env = "KENN_FORGE_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "widget"

[[agents]]
key = "helper"
label = "Helper"
command = ["/bin/sh", "-lc", "exec sleep 60"]

[tmux]
agent_sessions = true
`
	require.NoError(t, os.WriteFile(cfgPath, []byte(cfgContent), 0o644))
	cfg, err := config.Load(cfgPath)
	require.NoError(t, err)
	cfg.Tmux.Command = tmux.command
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
	worktree, err := database.CreateProjectWorktree(
		context.Background(), db.CreateProjectWorktreeInput{
			ProjectID: project.ID,
			Branch:    "runtime",
			Path:      t.TempDir(),
		},
	)
	require.NoError(t, err)
	return runtimeLaunchRollbackFixture{
		server:     srv,
		projectID:  project.ID,
		worktreeID: worktree.ID,
		tmux:       tmux,
	}
}

func occupyRuntimeWriter(t *testing.T, database *db.DB) *sql.Tx {
	t.Helper()
	tx, err := database.WriteDB().BeginTx(t.Context(), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback() })
	return tx
}

func serveRuntimeRequestAsync(
	srv *Server,
	ctx context.Context,
	method string,
	path string,
	body []byte,
) <-chan *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, path, bytes.NewReader(body)).WithContext(ctx)
	request.Host = "127.0.0.1:8091"
	if method == http.MethodPost {
		request.Header.Set("Content-Type", "application/json")
	}
	responses := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		srv.ServeHTTP(recorder, request)
		responses <- recorder
	}()
	return responses
}

func waitForRuntimeWriterWait(t *testing.T, database *db.DB, baseline int64) {
	t.Helper()
	require.Eventually(t, func() bool {
		return database.WriteDB().Stats().WaitCount > baseline
	}, time.Second, 10*time.Millisecond)
}

func runtimeLaunchRequestBody(t *testing.T, sessionKey string) []byte {
	t.Helper()
	return mustMarshal(t, map[string]any{
		"session_key": sessionKey,
		"command":     []string{"/bin/sh", "-lc", "exec sleep 60"},
		"label":       "Rollback shell",
		"cwd":         t.TempDir(),
	})
}

func awaitRuntimeResponse(
	t *testing.T,
	responses <-chan *httptest.ResponseRecorder,
) *httptest.ResponseRecorder {
	t.Helper()
	select {
	case recorder := <-responses:
		return recorder
	case <-time.After(5 * time.Second):
		require.FailNow(t, "runtime request did not complete within 5 seconds")
		return nil
	}
}

func readRuntimeLaunchIDs(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return strings.Fields(string(data))
}

func TestCommandSameKeyPersistenceOwnership(t *testing.T) {
	requirePTYAvailable(t)
	tests := []struct {
		name             string
		sessionKey       string
		label            string
		path             func(runtimeLaunchRollbackFixture) string
		scope            func(runtimeLaunchRollbackFixture) string
		persistenceError string
		assertDurable    func(
			*testing.T,
			runtimeLaunchRollbackFixture,
			localruntime.SessionInfo,
			string,
		)
	}{
		{
			name:       "host_command",
			sessionKey: "surface:same-key:host",
			label:      "Same-key host command",
			path: func(runtimeLaunchRollbackFixture) string {
				return "/api/v1/runtime/sessions"
			},
			scope: func(runtimeLaunchRollbackFixture) string {
				return hostRuntimeScope
			},
			persistenceError: "record host runtime tmux session",
			assertDurable: func(
				t *testing.T,
				fixture runtimeLaunchRollbackFixture,
				session localruntime.SessionInfo,
				cwd string,
			) {
				rows, err := fixture.server.db.ListHostRuntimeTmuxSessions(t.Context())
				require.NoError(t, err)
				require.Len(t, rows, 1)
				assert.Equal(t, session.Key, rows[0].SessionKey)
				assert.Equal(t, session.TmuxSession, rows[0].SessionName)
				assert.Equal(t, session.Label, rows[0].Label)
				assert.Equal(t, cwd, rows[0].CWD)
				assert.Equal(t, session.CreatedAt, rows[0].CreatedAt)
			},
		},
		{
			name:       "project_command",
			sessionKey: "surface:same-key:project",
			label:      "Same-key project command",
			path: func(fixture runtimeLaunchRollbackFixture) string {
				return "/api/v1/projects/" + fixture.projectID +
					"/worktrees/" + fixture.worktreeID + "/runtime/sessions"
			},
			scope: func(fixture runtimeLaunchRollbackFixture) string {
				return workspaceapi.ProjectWorktreeRuntimeScope(fixture.worktreeID)
			},
			persistenceError: "record project worktree runtime tmux session",
			assertDurable: func(
				t *testing.T,
				fixture runtimeLaunchRollbackFixture,
				session localruntime.SessionInfo,
				_ string,
			) {
				rows, err := fixture.server.db.ListProjectWorktreeTmuxSessions(
					t.Context(), fixture.worktreeID,
				)
				require.NoError(t, err)
				require.Len(t, rows, 1)
				assert.Equal(t, fixture.projectID, rows[0].ProjectID)
				assert.Equal(t, fixture.worktreeID, rows[0].WorktreeID)
				assert.Equal(t, session.Key, rows[0].SessionKey)
				assert.Equal(t, session.TmuxSession, rows[0].SessionName)
				assert.Equal(t, session.Label, rows[0].Label)
				assert.Empty(t, rows[0].TargetKey)
				assert.Equal(t, session.CreatedAt, rows[0].CreatedAt)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			fixture := setupRuntimeLaunchRollbackFixture(t, false)
			scope := test.scope(fixture)
			cwd := t.TempDir()
			body := mustMarshal(t, map[string]any{
				"session_key": test.sessionKey,
				"command":     []string{"/bin/sh", "-lc", "exec sleep 60"},
				"label":       test.label,
				"cwd":         cwd,
			})
			transaction := occupyRuntimeWriter(t, fixture.server.db)
			baseline := fixture.server.db.WriteDB().Stats().WaitCount
			ctxA, cancelA := context.WithCancel(t.Context())
			t.Cleanup(cancelA)

			aResponses := serveRuntimeRequestAsync(
				fixture.server, ctxA, http.MethodPost, test.path(fixture), body,
			)
			require.Eventually(func() bool {
				return len(fixture.server.runtime.ListSessions(scope)) == 1
			}, time.Second, 10*time.Millisecond)
			waitForRuntimeWriterWait(t, fixture.server.db, baseline)
			generationOne := fixture.server.runtime.ListSessions(scope)[0]
			assert.True(
				fixture.server.runtime.CommandSessionStartLockHeldForTest(test.sessionKey),
				"A must hold the keyed transaction lock while its metadata write is blocked",
			)

			bResponses := serveRuntimeRequestAsync(
				fixture.server, t.Context(), http.MethodPost, test.path(fixture), body,
			)
			require.Never(func() bool {
				return fixture.server.db.WriteDB().Stats().WaitCount > baseline+1
			}, 150*time.Millisecond, 10*time.Millisecond,
				"request B must not reach persistence while A owns rollback authority",
			)

			cancelA()
			aRecorder := awaitRuntimeResponse(t, aResponses)
			waitForRuntimeWriterWait(t, fixture.server.db, baseline+1)
			require.Eventually(func() bool {
				launches, err := os.ReadFile(fixture.tmux.launchHistoryPath)
				return err == nil && len(strings.Fields(string(launches))) == 2
			}, time.Second, 10*time.Millisecond)
			require.NoError(transaction.Rollback())
			bRecorder := awaitRuntimeResponse(t, bResponses)

			assert.Equal(http.StatusInternalServerError, aRecorder.Code)
			assert.Contains(aRecorder.Body.String(), test.persistenceError)
			assert.Contains(aRecorder.Body.String(), "context canceled")
			assert.Equal(http.StatusOK, bRecorder.Code)

			sessions := fixture.server.runtime.ListSessions(scope)
			require.Len(sessions, 1)
			generationTwo := sessions[0]
			assert.Equal(generationOne.Key, generationTwo.Key)
			assert.NotEqual(generationOne.CreatedAt, generationTwo.CreatedAt)
			assert.Equal(localruntime.SessionStatusRunning, generationTwo.Status)
			assert.FileExists(fixture.tmux.statePath)
			currentLaunch, err := os.ReadFile(fixture.tmux.launchPath)
			require.NoError(err)
			launches := readRuntimeLaunchIDs(t, fixture.tmux.launchHistoryPath)
			require.Len(launches, 2)
			assert.NotEqual(launches[0], launches[1])
			assert.Equal(launches[1], strings.TrimSpace(string(currentLaunch)))
			for _, killed := range readRuntimeLaunchIDs(
				t, fixture.tmux.killedLaunchesPath,
			) {
				assert.Equal(launches[0], killed)
				assert.NotEqual(launches[1], killed)
			}
			test.assertDurable(t, fixture, generationTwo, cwd)
		})
	}
}

func TestProjectWorktreeShellPersistenceOwnership(t *testing.T) {
	requirePTYAvailable(t)
	assert := assert.New(t)
	require := require.New(t)
	fixture := setupRuntimeLaunchRollbackFixture(t, false)
	scope := workspaceapi.ProjectWorktreeRuntimeScope(fixture.worktreeID)
	path := "/api/v1/projects/" + fixture.projectID +
		"/worktrees/" + fixture.worktreeID + "/runtime/shell"
	transaction := occupyRuntimeWriter(t, fixture.server.db)
	baseline := fixture.server.db.WriteDB().Stats().WaitCount
	ctxA, cancelA := context.WithCancel(t.Context())
	t.Cleanup(cancelA)

	aResponses := serveRuntimeRequestAsync(
		fixture.server, ctxA, http.MethodPost, path, nil,
	)
	require.Eventually(func() bool {
		return len(fixture.server.runtime.ListSessions(scope)) == 1
	}, time.Second, 10*time.Millisecond)
	waitForRuntimeWriterWait(t, fixture.server.db, baseline)
	generationOne := fixture.server.runtime.ListSessions(scope)[0]

	bResponses := serveRuntimeRequestAsync(
		fixture.server, t.Context(), http.MethodPost, path, nil,
	)
	require.Never(func() bool {
		return fixture.server.db.WriteDB().Stats().WaitCount > baseline+1
	}, 150*time.Millisecond, 10*time.Millisecond,
		"the follower must not persist while the creator owns rollback authority",
	)

	cancelA()
	aRecorder := awaitRuntimeResponse(t, aResponses)
	waitForRuntimeWriterWait(t, fixture.server.db, baseline+1)
	require.Eventually(func() bool {
		launches, err := os.ReadFile(fixture.tmux.launchHistoryPath)
		return err == nil && len(strings.Fields(string(launches))) == 2
	}, time.Second, 10*time.Millisecond)
	require.NoError(transaction.Rollback())
	bRecorder := awaitRuntimeResponse(t, bResponses)

	assert.Equal(http.StatusInternalServerError, aRecorder.Code)
	assert.Contains(aRecorder.Body.String(), "record project worktree runtime tmux session")
	assert.Contains(aRecorder.Body.String(), "context canceled")
	assert.Equal(http.StatusOK, bRecorder.Code)

	sessions := fixture.server.runtime.ListSessions(scope)
	require.Len(sessions, 1)
	generationTwo := sessions[0]
	assert.NotEqual(generationOne.Key, generationTwo.Key)
	assert.NotEqual(generationOne.CreatedAt, generationTwo.CreatedAt)
	assert.Equal(localruntime.SessionStatusRunning, generationTwo.Status)
	assert.FileExists(fixture.tmux.statePath)
	currentLaunch, err := os.ReadFile(fixture.tmux.launchPath)
	require.NoError(err)
	launches := readRuntimeLaunchIDs(t, fixture.tmux.launchHistoryPath)
	require.Len(launches, 2)
	assert.NotEqual(launches[0], launches[1])
	assert.Equal(launches[1], strings.TrimSpace(string(currentLaunch)))
	for _, killed := range readRuntimeLaunchIDs(t, fixture.tmux.killedLaunchesPath) {
		assert.Equal(launches[0], killed)
		assert.NotEqual(launches[1], killed)
	}
	rows, err := fixture.server.db.ListProjectWorktreeTmuxSessions(
		context.Background(), fixture.worktreeID,
	)
	require.NoError(err)
	require.Len(rows, 1)
	assert.Equal(generationTwo.Key, rows[0].SessionKey)
	assert.Equal(generationTwo.TmuxSession, rows[0].SessionName)
	assert.Equal(generationTwo.CreatedAt, rows[0].CreatedAt)
}

func TestHostRuntimeLaunchPersistenceFailureRollsBackNewTmuxSession(
	t *testing.T,
) {
	requirePTYAvailable(t)
	assert := assert.New(t)
	require := require.New(t)
	fixture := setupRuntimeLaunchRollbackFixture(t, false)
	transaction := occupyRuntimeWriter(t, fixture.server.db)
	baseline := fixture.server.db.WriteDB().Stats().WaitCount
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	responses := serveRuntimeRequestAsync(
		fixture.server, ctx, http.MethodPost, "/api/v1/runtime/sessions",
		runtimeLaunchRequestBody(t, "surface:host:console:rollback-new"),
	)
	require.Eventually(func() bool {
		_, err := os.Stat(fixture.tmux.attachStartedPath)
		return err == nil
	}, time.Second, 10*time.Millisecond)
	require.Eventually(func() bool {
		return len(fixture.server.runtime.ListSessions(hostRuntimeScope)) == 1
	}, time.Second, 10*time.Millisecond)
	waitForRuntimeWriterWait(t, fixture.server.db, baseline)
	launched := fixture.server.runtime.ListSessions(hostRuntimeScope)[0]

	require.NoError(os.WriteFile(fixture.tmux.exitAttachPath, nil, 0o600))
	require.Eventually(func() bool {
		return len(fixture.server.runtime.ListSessions(hostRuntimeScope)) == 0
	}, time.Second, 10*time.Millisecond)
	cancel()
	recorder := awaitRuntimeResponse(t, responses)
	require.NoError(transaction.Rollback())

	assert.Equal(http.StatusInternalServerError, recorder.Code)
	assert.Contains(recorder.Body.String(), "record host runtime tmux session")
	assert.Contains(recorder.Body.String(), "context canceled")
	assert.NoFileExists(fixture.tmux.statePath)
	assertFakeTmuxKilledSession(t, fixture.tmux.recordPath, launched.TmuxSession)
	assert.Empty(fixture.server.runtime.ListSessions(hostRuntimeScope))
	rows, err := fixture.server.db.ListHostRuntimeTmuxSessions(context.Background())
	require.NoError(err)
	assert.Empty(rows)
}

func TestHostRuntimeLaunchPersistenceFailurePreservesReusedTmuxSession(
	t *testing.T,
) {
	requirePTYAvailable(t)
	assert := assert.New(t)
	require := require.New(t)
	fixture := setupRuntimeLaunchRollbackFixture(t, false)
	body := runtimeLaunchRequestBody(t, "surface:host:console:rollback-reused")
	initial := serveRuntimeRequestAsync(
		fixture.server, t.Context(), http.MethodPost, "/api/v1/runtime/sessions", body,
	)
	require.Equal(http.StatusOK, awaitRuntimeResponse(t, initial).Code)
	sessions := fixture.server.runtime.ListSessions(hostRuntimeScope)
	require.Len(sessions, 1)
	original := sessions[0]

	transaction := occupyRuntimeWriter(t, fixture.server.db)
	baseline := fixture.server.db.WriteDB().Stats().WaitCount
	ctx, cancel := context.WithCancel(t.Context())
	responses := serveRuntimeRequestAsync(
		fixture.server, ctx, http.MethodPost, "/api/v1/runtime/sessions", body,
	)
	waitForRuntimeWriterWait(t, fixture.server.db, baseline)
	cancel()
	recorder := awaitRuntimeResponse(t, responses)
	require.NoError(transaction.Rollback())

	assert.Equal(http.StatusInternalServerError, recorder.Code)
	assert.FileExists(fixture.tmux.statePath)
	sessions = fixture.server.runtime.ListSessions(hostRuntimeScope)
	require.Len(sessions, 1)
	assert.Equal(original.Key, sessions[0].Key)
	assert.Equal(original.TmuxSession, sessions[0].TmuxSession)
	record, err := os.ReadFile(fixture.tmux.recordPath)
	require.NoError(err)
	assert.NotContains(
		string(record), "kill-session\x00-t\x00"+original.TmuxSession,
	)
	rows, err := fixture.server.db.ListHostRuntimeTmuxSessions(context.Background())
	require.NoError(err)
	require.Len(rows, 1)
	assert.Equal(original.Key, rows[0].SessionKey)
	assert.Equal(original.TmuxSession, rows[0].SessionName)
}

func TestHostRuntimeLaunchPersistenceFailurePreservesReattachedTmuxBackend(
	t *testing.T,
) {
	requirePTYAvailable(t)
	assert := assert.New(t)
	require := require.New(t)
	fixture := setupRuntimeLaunchRollbackFixture(t, false)
	body := runtimeLaunchRequestBody(t, "surface:host:console:rollback-reattached")
	initial := serveRuntimeRequestAsync(
		fixture.server, t.Context(), http.MethodPost, "/api/v1/runtime/sessions", body,
	)
	require.Equal(http.StatusOK, awaitRuntimeResponse(t, initial).Code)
	sessions := fixture.server.runtime.ListSessions(hostRuntimeScope)
	require.Len(sessions, 1)
	original := sessions[0]
	require.NoError(fixture.server.runtime.Detach(hostRuntimeScope, original.Key))
	require.Empty(fixture.server.runtime.ListSessions(hostRuntimeScope))

	transaction := occupyRuntimeWriter(t, fixture.server.db)
	baseline := fixture.server.db.WriteDB().Stats().WaitCount
	ctx, cancel := context.WithCancel(t.Context())
	responses := serveRuntimeRequestAsync(
		fixture.server, ctx, http.MethodPost, "/api/v1/runtime/sessions", body,
	)
	waitForRuntimeWriterWait(t, fixture.server.db, baseline)
	cancel()
	recorder := awaitRuntimeResponse(t, responses)
	require.NoError(transaction.Rollback())

	assert.Equal(http.StatusInternalServerError, recorder.Code)
	assert.Contains(recorder.Body.String(), "record host runtime tmux session")
	assert.Contains(recorder.Body.String(), "context canceled")
	assert.FileExists(fixture.tmux.statePath)
	sessions = fixture.server.runtime.ListSessions(hostRuntimeScope)
	require.Len(sessions, 1)
	assert.Equal(original.Key, sessions[0].Key)
	assert.Equal(original.TmuxSession, sessions[0].TmuxSession)
	record, err := os.ReadFile(fixture.tmux.recordPath)
	require.NoError(err)
	assert.NotContains(
		string(record), "kill-session\x00-t\x00"+original.TmuxSession,
	)
	rows, err := fixture.server.db.ListHostRuntimeTmuxSessions(context.Background())
	require.NoError(err)
	require.Len(rows, 1)
	assert.Equal(original.Key, rows[0].SessionKey)
	assert.Equal(original.TmuxSession, rows[0].SessionName)
}

func TestHostRuntimeLaunchPersistenceFailureLogsRollbackFailureAndPreservesPersistenceError(
	t *testing.T,
) {
	requirePTYAvailable(t)
	assert := assert.New(t)
	require := require.New(t)
	fixture := setupRuntimeLaunchRollbackFixture(t, true)
	var logs lockedBuffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })
	transaction := occupyRuntimeWriter(t, fixture.server.db)
	baseline := fixture.server.db.WriteDB().Stats().WaitCount
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	responses := serveRuntimeRequestAsync(
		fixture.server, ctx, http.MethodPost, "/api/v1/runtime/sessions",
		runtimeLaunchRequestBody(t, "surface:host:console:rollback-failure"),
	)
	require.Eventually(func() bool {
		_, err := os.Stat(fixture.tmux.attachStartedPath)
		return err == nil
	}, time.Second, 10*time.Millisecond)
	waitForRuntimeWriterWait(t, fixture.server.db, baseline)
	launched := fixture.server.runtime.ListSessions(hostRuntimeScope)[0]
	require.NoError(os.WriteFile(fixture.tmux.exitAttachPath, nil, 0o600))
	require.Eventually(func() bool {
		return len(fixture.server.runtime.ListSessions(hostRuntimeScope)) == 0
	}, time.Second, 10*time.Millisecond)
	cancel()
	recorder := awaitRuntimeResponse(t, responses)
	require.NoError(transaction.Rollback())

	assert.Equal(http.StatusInternalServerError, recorder.Code)
	assert.Contains(recorder.Body.String(), "record host runtime tmux session")
	assert.Contains(recorder.Body.String(), "context canceled")
	assert.NotContains(recorder.Body.String(), "forced kill-session failure")
	assert.Contains(logs.String(), "roll back unrecorded host runtime session")
	assert.Contains(logs.String(), launched.Key)
	assert.Contains(logs.String(), launched.TmuxSession)
	assert.Contains(logs.String(), "forced kill-session failure")
	assert.FileExists(fixture.tmux.statePath)
}

func TestProjectWorktreeRuntimeLaunchPersistenceFailureRollsBackNewTmuxSession(
	t *testing.T,
) {
	requirePTYAvailable(t)
	tests := []struct {
		name string
		path func(projectID, worktreeID string) string
		body func(t *testing.T) []byte
	}{
		{
			name: "shell",
			path: func(projectID, worktreeID string) string {
				return "/api/v1/projects/" + projectID +
					"/worktrees/" + worktreeID + "/runtime/shell"
			},
			body: func(*testing.T) []byte { return nil },
		},
		{
			name: "configured_target",
			path: func(projectID, worktreeID string) string {
				return "/api/v1/projects/" + projectID +
					"/worktrees/" + worktreeID + "/runtime/sessions"
			},
			body: func(t *testing.T) []byte {
				return mustMarshal(t, map[string]any{"target_key": "helper"})
			},
		},
		{
			name: "command",
			path: func(projectID, worktreeID string) string {
				return "/api/v1/projects/" + projectID +
					"/worktrees/" + worktreeID + "/runtime/sessions"
			},
			body: func(t *testing.T) []byte {
				return mustMarshal(t, map[string]any{
					"session_key": "surface:project:rollback:command",
					"command":     []string{"/bin/sh", "-lc", "exec sleep 60"},
					"label":       "Rollback Command",
				})
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			fixture := setupRuntimeLaunchRollbackFixture(t, false)
			scope := workspaceapi.ProjectWorktreeRuntimeScope(fixture.worktreeID)
			transaction := occupyRuntimeWriter(t, fixture.server.db)
			baseline := fixture.server.db.WriteDB().Stats().WaitCount
			ctx, cancel := context.WithCancel(t.Context())
			t.Cleanup(cancel)

			responses := serveRuntimeRequestAsync(
				fixture.server, ctx, http.MethodPost,
				test.path(fixture.projectID, fixture.worktreeID), test.body(t),
			)
			require.Eventually(func() bool {
				_, err := os.Stat(fixture.tmux.attachStartedPath)
				return err == nil
			}, time.Second, 10*time.Millisecond)
			require.Eventually(func() bool {
				return len(fixture.server.runtime.ListSessions(scope)) == 1
			}, time.Second, 10*time.Millisecond)
			waitForRuntimeWriterWait(t, fixture.server.db, baseline)
			launched := fixture.server.runtime.ListSessions(scope)[0]

			require.NoError(os.WriteFile(fixture.tmux.exitAttachPath, nil, 0o600))
			require.Eventually(func() bool {
				return len(fixture.server.runtime.ListSessions(scope)) == 0
			}, time.Second, 10*time.Millisecond)
			cancel()
			recorder := awaitRuntimeResponse(t, responses)
			require.NoError(transaction.Rollback())

			assert.Equal(http.StatusInternalServerError, recorder.Code)
			assert.Contains(recorder.Body.String(), "record project worktree runtime tmux session")
			assert.Contains(recorder.Body.String(), "context canceled")
			assert.NoFileExists(fixture.tmux.statePath)
			assertFakeTmuxKilledSession(t, fixture.tmux.recordPath, launched.TmuxSession)
			assert.Empty(fixture.server.runtime.ListSessions(scope))
			rows, err := fixture.server.db.ListProjectWorktreeTmuxSessions(
				context.Background(), fixture.worktreeID,
			)
			require.NoError(err)
			assert.Empty(rows)
		})
	}
}

func TestProjectWorktreeRuntimeLaunchPersistenceFailurePreservesReusedTmuxSession(
	t *testing.T,
) {
	requirePTYAvailable(t)
	tests := []struct {
		name string
		path func(projectID, worktreeID string) string
		body func(t *testing.T) []byte
	}{
		{
			name: "shell",
			path: func(projectID, worktreeID string) string {
				return "/api/v1/projects/" + projectID +
					"/worktrees/" + worktreeID + "/runtime/shell"
			},
			body: func(*testing.T) []byte { return nil },
		},
		{
			name: "command",
			path: func(projectID, worktreeID string) string {
				return "/api/v1/projects/" + projectID +
					"/worktrees/" + worktreeID + "/runtime/sessions"
			},
			body: func(t *testing.T) []byte {
				return mustMarshal(t, map[string]any{
					"session_key": "surface:project:rollback:reused",
					"command":     []string{"/bin/sh", "-lc", "exec sleep 60"},
					"label":       "Rollback Reused Command",
				})
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			fixture := setupRuntimeLaunchRollbackFixture(t, false)
			scope := workspaceapi.ProjectWorktreeRuntimeScope(fixture.worktreeID)
			path := test.path(fixture.projectID, fixture.worktreeID)
			body := test.body(t)

			initial := serveRuntimeRequestAsync(
				fixture.server, t.Context(), http.MethodPost, path, body,
			)
			require.Equal(http.StatusOK, awaitRuntimeResponse(t, initial).Code)
			sessions := fixture.server.runtime.ListSessions(scope)
			require.Len(sessions, 1)
			original := sessions[0]

			transaction := occupyRuntimeWriter(t, fixture.server.db)
			baseline := fixture.server.db.WriteDB().Stats().WaitCount
			ctx, cancel := context.WithCancel(t.Context())
			responses := serveRuntimeRequestAsync(
				fixture.server, ctx, http.MethodPost, path, body,
			)
			waitForRuntimeWriterWait(t, fixture.server.db, baseline)
			cancel()
			recorder := awaitRuntimeResponse(t, responses)
			require.NoError(transaction.Rollback())

			assert.Equal(http.StatusInternalServerError, recorder.Code)
			assert.Contains(recorder.Body.String(), "record project worktree runtime tmux session")
			assert.Contains(recorder.Body.String(), "context canceled")
			assert.FileExists(fixture.tmux.statePath)
			sessions = fixture.server.runtime.ListSessions(scope)
			require.Len(sessions, 1)
			assert.Equal(original.Key, sessions[0].Key)
			assert.Equal(original.TmuxSession, sessions[0].TmuxSession)
			record, err := os.ReadFile(fixture.tmux.recordPath)
			require.NoError(err)
			assert.NotContains(
				string(record), "kill-session\x00-t\x00"+original.TmuxSession,
			)
			rows, err := fixture.server.db.ListProjectWorktreeTmuxSessions(
				context.Background(), fixture.worktreeID,
			)
			require.NoError(err)
			require.Len(rows, 1)
			assert.Equal(original.Key, rows[0].SessionKey)
			assert.Equal(original.TmuxSession, rows[0].SessionName)
		})
	}
}

func TestProjectWorktreeRuntimeLaunchPersistenceFailurePreservesReattachedCommandBackend(
	t *testing.T,
) {
	requirePTYAvailable(t)
	assert := assert.New(t)
	require := require.New(t)
	fixture := setupRuntimeLaunchRollbackFixture(t, false)
	scope := workspaceapi.ProjectWorktreeRuntimeScope(fixture.worktreeID)
	path := "/api/v1/projects/" + fixture.projectID + "/worktrees/" +
		fixture.worktreeID + "/runtime/sessions"
	body := mustMarshal(t, map[string]any{
		"session_key": "surface:project:rollback:reattached",
		"command":     []string{"/bin/sh", "-lc", "exec sleep 60"},
		"label":       "Rollback Reattached Command",
	})
	initial := serveRuntimeRequestAsync(
		fixture.server, t.Context(), http.MethodPost, path, body,
	)
	require.Equal(http.StatusOK, awaitRuntimeResponse(t, initial).Code)
	sessions := fixture.server.runtime.ListSessions(scope)
	require.Len(sessions, 1)
	original := sessions[0]
	require.NoError(fixture.server.runtime.Detach(scope, original.Key))
	require.Empty(fixture.server.runtime.ListSessions(scope))

	transaction := occupyRuntimeWriter(t, fixture.server.db)
	baseline := fixture.server.db.WriteDB().Stats().WaitCount
	ctx, cancel := context.WithCancel(t.Context())
	responses := serveRuntimeRequestAsync(
		fixture.server, ctx, http.MethodPost, path, body,
	)
	waitForRuntimeWriterWait(t, fixture.server.db, baseline)
	cancel()
	recorder := awaitRuntimeResponse(t, responses)
	require.NoError(transaction.Rollback())

	assert.Equal(http.StatusInternalServerError, recorder.Code)
	assert.Contains(
		recorder.Body.String(), "record project worktree runtime tmux session",
	)
	assert.Contains(recorder.Body.String(), "context canceled")
	assert.FileExists(fixture.tmux.statePath)
	sessions = fixture.server.runtime.ListSessions(scope)
	require.Len(sessions, 1)
	assert.Equal(original.Key, sessions[0].Key)
	assert.Equal(original.TmuxSession, sessions[0].TmuxSession)
	record, err := os.ReadFile(fixture.tmux.recordPath)
	require.NoError(err)
	assert.NotContains(
		string(record), "kill-session\x00-t\x00"+original.TmuxSession,
	)
	rows, err := fixture.server.db.ListProjectWorktreeTmuxSessions(
		context.Background(), fixture.worktreeID,
	)
	require.NoError(err)
	require.Len(rows, 1)
	assert.Equal(original.Key, rows[0].SessionKey)
	assert.Equal(original.TmuxSession, rows[0].SessionName)
}

func TestProjectWorktreeRuntimeLaunchPersistenceFailureLogsRollbackFailureAndPreservesPersistenceError(
	t *testing.T,
) {
	requirePTYAvailable(t)
	assert := assert.New(t)
	require := require.New(t)
	fixture := setupRuntimeLaunchRollbackFixture(t, true)
	scope := workspaceapi.ProjectWorktreeRuntimeScope(fixture.worktreeID)
	var logs lockedBuffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })
	transaction := occupyRuntimeWriter(t, fixture.server.db)
	baseline := fixture.server.db.WriteDB().Stats().WaitCount
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	responses := serveRuntimeRequestAsync(
		fixture.server, ctx, http.MethodPost,
		"/api/v1/projects/"+fixture.projectID+"/worktrees/"+fixture.worktreeID+
			"/runtime/sessions",
		mustMarshal(t, map[string]any{
			"session_key": "surface:project:rollback:failure",
			"command":     []string{"/bin/sh", "-lc", "exec sleep 60"},
			"label":       "Rollback Failure Command",
		}),
	)
	require.Eventually(func() bool {
		_, err := os.Stat(fixture.tmux.attachStartedPath)
		return err == nil
	}, time.Second, 10*time.Millisecond)
	waitForRuntimeWriterWait(t, fixture.server.db, baseline)
	launched := fixture.server.runtime.ListSessions(scope)[0]
	require.NoError(os.WriteFile(fixture.tmux.exitAttachPath, nil, 0o600))
	require.Eventually(func() bool {
		return len(fixture.server.runtime.ListSessions(scope)) == 0
	}, time.Second, 10*time.Millisecond)
	cancel()
	recorder := awaitRuntimeResponse(t, responses)
	require.NoError(transaction.Rollback())

	assert.Equal(http.StatusInternalServerError, recorder.Code)
	assert.Contains(recorder.Body.String(), "record project worktree runtime tmux session")
	assert.Contains(recorder.Body.String(), "context canceled")
	assert.NotContains(recorder.Body.String(), "forced kill-session failure")
	assert.Contains(logs.String(), "roll back unrecorded project worktree runtime session")
	assert.Contains(logs.String(), fixture.projectID)
	assert.Contains(logs.String(), fixture.worktreeID)
	assert.Contains(logs.String(), launched.Key)
	assert.Contains(logs.String(), launched.TmuxSession)
	assert.Contains(logs.String(), "forced kill-session failure")
	assert.FileExists(fixture.tmux.statePath)
}

func TestRuntimeSessionExitDuringPersistenceLeavesNoDurableRow(t *testing.T) {
	requirePTYAvailable(t)
	tests := []struct {
		name      string
		path      func(runtimeLaunchRollbackFixture) string
		body      func(*testing.T) []byte
		scope     func(runtimeLaunchRollbackFixture) string
		countRows func(*testing.T, runtimeLaunchRollbackFixture) int
	}{
		{
			name: "host_command",
			path: func(runtimeLaunchRollbackFixture) string {
				return "/api/v1/runtime/sessions"
			},
			body: func(t *testing.T) []byte {
				return mustMarshal(t, map[string]any{
					"session_key": "surface:exit-race:host",
					"command":     []string{"/bin/sh", "-lc", "exec sleep 60"},
					"label":       "Exit-race command",
					"cwd":         t.TempDir(),
				})
			},
			scope: func(runtimeLaunchRollbackFixture) string {
				return hostRuntimeScope
			},
			countRows: func(t *testing.T, fixture runtimeLaunchRollbackFixture) int {
				rows, err := fixture.server.db.ListHostRuntimeTmuxSessions(
					context.Background(),
				)
				require.NoError(t, err)
				return len(rows)
			},
		},
		{
			name: "project_shell",
			path: func(fixture runtimeLaunchRollbackFixture) string {
				return "/api/v1/projects/" + fixture.projectID +
					"/worktrees/" + fixture.worktreeID + "/runtime/shell"
			},
			body: func(*testing.T) []byte { return nil },
			scope: func(fixture runtimeLaunchRollbackFixture) string {
				return workspaceapi.ProjectWorktreeRuntimeScope(fixture.worktreeID)
			},
			countRows: func(t *testing.T, fixture runtimeLaunchRollbackFixture) int {
				rows, err := fixture.server.db.ListProjectWorktreeTmuxSessions(
					context.Background(), fixture.worktreeID,
				)
				require.NoError(t, err)
				return len(rows)
			},
		},
		{
			name: "project_configured_target",
			path: func(fixture runtimeLaunchRollbackFixture) string {
				return "/api/v1/projects/" + fixture.projectID +
					"/worktrees/" + fixture.worktreeID + "/runtime/sessions"
			},
			body: func(t *testing.T) []byte {
				return mustMarshal(t, map[string]any{"target_key": "helper"})
			},
			scope: func(fixture runtimeLaunchRollbackFixture) string {
				return workspaceapi.ProjectWorktreeRuntimeScope(fixture.worktreeID)
			},
			countRows: func(t *testing.T, fixture runtimeLaunchRollbackFixture) int {
				rows, err := fixture.server.db.ListProjectWorktreeTmuxSessions(
					context.Background(), fixture.worktreeID,
				)
				require.NoError(t, err)
				return len(rows)
			},
		},
		{
			name: "project_command",
			path: func(fixture runtimeLaunchRollbackFixture) string {
				return "/api/v1/projects/" + fixture.projectID +
					"/worktrees/" + fixture.worktreeID + "/runtime/sessions"
			},
			body: func(t *testing.T) []byte {
				return mustMarshal(t, map[string]any{
					"session_key": "surface:exit-race:project",
					"command":     []string{"/bin/sh", "-lc", "exec sleep 60"},
					"label":       "Exit-race command",
					"cwd":         t.TempDir(),
				})
			},
			scope: func(fixture runtimeLaunchRollbackFixture) string {
				return workspaceapi.ProjectWorktreeRuntimeScope(fixture.worktreeID)
			},
			countRows: func(t *testing.T, fixture runtimeLaunchRollbackFixture) int {
				rows, err := fixture.server.db.ListProjectWorktreeTmuxSessions(
					context.Background(), fixture.worktreeID,
				)
				require.NoError(t, err)
				return len(rows)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			fixture := setupRuntimeLaunchRollbackFixture(t, false)
			scope := test.scope(fixture)
			transaction := occupyRuntimeWriter(t, fixture.server.db)
			baseline := fixture.server.db.WriteDB().Stats().WaitCount

			responses := serveRuntimeRequestAsync(
				fixture.server, t.Context(), http.MethodPost, test.path(fixture),
				test.body(t),
			)
			require.Eventually(func() bool {
				return len(fixture.server.runtime.ListSessions(scope)) == 1
			}, time.Second, 10*time.Millisecond)
			waitForRuntimeWriterWait(t, fixture.server.db, baseline)

			// The command exits while its metadata write is still blocked, so
			// the async exit cleanup can observe zero rows before the write
			// lands.
			require.NoError(os.WriteFile(fixture.tmux.exitAttachPath, nil, 0o600))
			require.Eventually(func() bool {
				return len(fixture.server.runtime.ListSessions(scope)) == 0
			}, time.Second, 10*time.Millisecond)

			require.NoError(transaction.Rollback())
			recorder := awaitRuntimeResponse(t, responses)

			assert.Equal(http.StatusOK, recorder.Code)
			assert.Zero(test.countRows(t, fixture),
				"a session that exited before its metadata write completed"+
					" must not leave a durable row")
		})
	}
}

func TestForgetHostRuntimeCommandSessionIfExitedKeepsLiveAndNewerRows(
	t *testing.T,
) {
	requirePTYAvailable(t)
	assert := assert.New(t)
	require := require.New(t)
	fixture := setupRuntimeLaunchRollbackFixture(t, false)
	ctx := context.Background()

	responses := serveRuntimeRequestAsync(
		fixture.server, t.Context(), http.MethodPost, "/api/v1/runtime/sessions",
		runtimeLaunchRequestBody(t, "surface:exit-race:live"),
	)
	recorder := awaitRuntimeResponse(t, responses)
	require.Equal(http.StatusOK, recorder.Code)
	live := fixture.server.runtime.ListSessions(hostRuntimeScope)[0]

	// A live session must keep its durable row.
	fixture.server.forgetHostRuntimeCommandSessionIfExited(ctx, live)
	rows, err := fixture.server.db.ListHostRuntimeTmuxSessions(ctx)
	require.NoError(err)
	require.Len(rows, 1)

	// Reconciling a dead older generation must not delete a newer
	// generation's row recorded under the same reusable key.
	stale := live
	stale.Key = "surface:exit-race:stale"
	stale.CreatedAt = live.CreatedAt.Add(-time.Minute)
	require.NoError(fixture.server.db.UpsertHostRuntimeTmuxSession(
		ctx, &db.HostRuntimeTmuxSession{
			SessionKey:  stale.Key,
			SessionName: "newer-generation",
			Label:       "Newer generation",
			CWD:         t.TempDir(),
			CreatedAt:   live.CreatedAt,
		},
	))
	fixture.server.forgetHostRuntimeCommandSessionIfExited(ctx, stale)
	rows, err = fixture.server.db.ListHostRuntimeTmuxSessions(ctx)
	require.NoError(err)
	assert.Len(rows, 2, "an older dead generation must not delete a newer row")

	// The exact dead generation is deleted once no newer row replaced it.
	stale.CreatedAt = live.CreatedAt
	fixture.server.forgetHostRuntimeCommandSessionIfExited(ctx, stale)
	rows, err = fixture.server.db.ListHostRuntimeTmuxSessions(ctx)
	require.NoError(err)
	assert.Len(rows, 1)
	assert.Equal(live.Key, rows[0].SessionKey)

	// A live replacement with the same reusable key must not be mistaken for
	// the exited generation. If its persistence later fails and rolls back,
	// the exited generation's durable row must already be gone.
	require.NoError(fixture.server.runtime.Detach(hostRuntimeScope, live.Key))
	replacement, err := fixture.server.runtime.EnsureCommandSessionAndPersist(
		ctx,
		hostRuntimeScope,
		localruntime.CommandLaunchSpec{
			SessionKey: live.Key,
			Command:    []string{"/bin/sh", "-lc", "exec sleep 60"},
			Label:      "Replacement generation",
			CWD:        t.TempDir(),
		},
		func(context.Context, localruntime.SessionInfo) error { return nil },
	)
	require.NoError(err)
	require.NotEqual(live.CreatedAt, replacement.CreatedAt)
	require.NoError(fixture.server.db.UpsertHostRuntimeTmuxSession(
		ctx, &db.HostRuntimeTmuxSession{
			SessionKey:  live.Key,
			SessionName: live.TmuxSession,
			Label:       live.Label,
			CWD:         t.TempDir(),
			CreatedAt:   live.CreatedAt,
		},
	))

	fixture.server.forgetHostRuntimeCommandSessionIfExited(ctx, live)
	rows, err = fixture.server.db.ListHostRuntimeTmuxSessions(ctx)
	require.NoError(err)
	assert.Empty(rows)
}
