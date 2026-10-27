package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/internal/procutil"
	"go.kenn.io/forge/internal/server/workspaceapi"
	"go.kenn.io/forge/internal/testutil/testtmux"
	"go.kenn.io/forge/internal/testutil/workspacetest"
	"go.kenn.io/forge/internal/workspace/localruntime"
)

func TestRuntimeShutdownRecovery(t *testing.T) {
	tmuxPath, err := exec.LookPath("tmux")
	if err != nil || !testtmux.Supported() {
		t.Skip("requires tmux on a supported host")
	}
	owner, err := testtmux.New()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, owner.Cleanup()) })

	for _, tc := range []struct {
		name      string
		env       string
		worker    bool
		action    string
		wantExit  *bool
		wantAlive bool
	}{
		{name: "default graceful shutdown", action: "shutdown", wantExit: new(false), wantAlive: true},
		{name: "explicit recovery", env: "1", action: "shutdown", wantExit: new(false), wantAlive: true},
		{name: "legacy opt out", env: "0", action: "shutdown", wantExit: new(true), wantAlive: true},
		{name: "other explicit value stays disabled", env: "false", action: "shutdown", wantExit: new(true), wantAlive: true},
		{name: "worker ignores opt out", env: "0", worker: true, action: "shutdown", wantExit: new(false), wantAlive: true},
		{name: "normal exit", action: "exit", wantExit: new(true)},
		{name: "explicit stop", action: "stop", wantExit: new(true)},
		{name: "backend killed while attached", action: "kill", wantExit: new(true)},
		{name: "backend killed after detach", action: "shutdown-then-kill", wantExit: new(false)},
		{name: "exit observed before shutdown", action: "exit-then-shutdown", wantExit: new(true)},
		{name: "backend exit races shutdown", action: "race"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			assert := assert.New(t)
			t.Setenv("KENN_FORGE_DEV_RESTART", tc.env)
			fixture := workspacetest.NewTmuxRuntimeFixture(t, owner, tmuxPath)
			command := fixture.ServerCommand
			options := localruntime.Options{
				Targets: []localruntime.LaunchTarget{
					{
						Key: "probe", Label: "Synthetic agent", Kind: localruntime.LaunchTargetAgent, Available: true,
						Command: []string{"/bin/sh", "-c", "printf 'PROBE_READY\\n'; read -r line; exit 7"},
					},
					{Key: "shell", Kind: localruntime.LaunchTargetShell, Available: true, Command: command},
				},
				DetachSessionsForServerRestart: detachRuntimeSessionsForRestart(tc.worker),
			}
			manager := fixture.NewAgentManager(command, options)
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			t.Cleanup(cancel)
			cwd := t.TempDir()
			info, err := manager.Launch(ctx, "synthetic-workspace", cwd, "probe")
			require.NoError(err)
			attachment, err := manager.AttachSession(info.WorkspaceID, info.Key)
			require.NoError(err)
			t.Cleanup(attachment.Close)
			bridgeResult := make(chan bool, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				connection, err := websocket.Accept(w, r, nil)
				if err != nil {
					return
				}
				defer connection.CloseNow()
				bridgeResult <- workspaceapi.BridgeRuntimeAttachment(ctx, connection, attachment)
			}))
			t.Cleanup(server.Close)
			connection, response, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
			if response != nil && response.Body != nil {
				t.Cleanup(func() { _ = response.Body.Close() })
			}
			require.NoError(err)
			t.Cleanup(func() { _ = connection.CloseNow() })
			output := ""
			for !strings.Contains(output, "PROBE_READY") {
				_, data, err := connection.Read(ctx)
				require.NoError(err)
				output += string(data)
			}
			tmux := func(args ...string) error {
				return procutil.CommandContext(ctx, command[0], append(command[1:], args...)...).Run()
			}
			switch tc.action {
			case "shutdown":
				manager.Shutdown()
			case "exit":
				require.NoError(tmux("send-keys", "-t", info.TmuxSession, "quit", "Enter"))
			case "stop":
				require.NoError(manager.Stop(ctx, info.WorkspaceID, info.Key))
			case "kill":
				require.NoError(tmux("kill-session", "-t", info.TmuxSession))
			case "shutdown-then-kill":
				manager.Shutdown()
				require.NoError(tmux("kill-session", "-t", info.TmuxSession))
			case "exit-then-shutdown":
				require.NoError(tmux("send-keys", "-t", info.TmuxSession, "quit", "Enter"))
				require.Eventually(func() bool { return manager.Exited(info.Key) }, 5*time.Second, 10*time.Millisecond)
				manager.Shutdown()
			case "race":
				var wait sync.WaitGroup
				wait.Go(manager.Shutdown)
				var killErr error
				wait.Go(func() { killErr = tmux("kill-session", "-t", info.TmuxSession) })
				wait.Wait()
				require.NoError(killErr)
			}
			gotExit := false
			for {
				kind, data, err := connection.Read(ctx)
				if err != nil {
					break
				}
				if kind == websocket.MessageText {
					var frame struct {
						Type string `json:"type"`
					}
					require.NoError(json.Unmarshal(data, &frame))
					if frame.Type == "exited" {
						gotExit = true
					}
				}
			}
			require.NoError(ctx.Err(), "bridge must close before its deadline")
			var classifiedExit bool
			select {
			case classifiedExit = <-bridgeResult:
			case <-ctx.Done():
				require.NoError(ctx.Err(), "bridge did not finish")
			}
			if tc.wantExit != nil {
				assert.Equal(*tc.wantExit, gotExit, "browser exited frame")
				assert.Equal(*tc.wantExit, classifiedExit, "bridge classification")
			}
			alive := tmux("has-session", "-t", info.TmuxSession) == nil
			assert.Equal(tc.wantAlive, alive, "underlying tmux session")
			restored := fixture.NewAgentManager(command, options)
			restoreErr := restored.RestoreRuntimeSessions(ctx, []localruntime.RestoredRuntimeSession{{
				WorkspaceID: info.WorkspaceID, SessionKey: info.Key, TargetKey: info.TargetKey,
				Label: info.Label, Kind: info.Kind, TmuxSession: info.TmuxSession,
				CWD: cwd, CreatedAt: info.CreatedAt,
			}})
			if tc.wantAlive {
				require.NoError(restoreErr)
				reattached, err := restored.AttachSession(info.WorkspaceID, info.Key)
				require.NoError(err)
				defer reattached.Close()
				assert.Equal(info.Key, reattached.Info().Key)
				assert.Equal(info.CreatedAt, reattached.Info().CreatedAt)
				require.NoError(restored.Stop(ctx, info.WorkspaceID, info.Key))
			} else {
				require.ErrorIs(restoreErr, localruntime.ErrSessionNotFound)
				_, err := restored.AttachSession(info.WorkspaceID, info.Key)
				assert.Error(err)
			}
		})
	}
}
