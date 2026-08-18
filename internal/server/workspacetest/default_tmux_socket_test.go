package workspacetest

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/apiclient/generated"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/procutil"
	"go.kenn.io/forge/internal/server"
)

// TestWorkspaceUnconfiguredTmuxUsesForgeSocketE2E proves that a server
// without a configured [tmux] command runs workspace sessions on the
// dedicated kenn-forge tmux socket, not the user's global server. The
// socket name is hardcoded on purpose: deriving it from
// config.DefaultTmuxCommand would follow a regressed default and pass.
// TMUX_TMPDIR points the -L socket directory at a private sandbox so the
// test never touches a real kenn-forge server.
func TestWorkspaceUnconfiguredTmuxUsesForgeSocketE2E(t *testing.T) {
	if len(workspaceTestTmuxCommand) == 0 {
		t.Skip("tmux is required")
	}
	require := require.New(t)
	tmuxPath := workspaceTestTmuxCommand[0]

	sockDir, err := os.MkdirTemp("/tmp", "kenn-forge-default-tmux-*")
	require.NoError(err)
	t.Setenv("TMUX_TMPDIR", sockDir)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		killCmd := procutil.CommandContext(
			ctx, tmuxPath, "-L", "kenn-forge", "kill-server",
		)
		killCmd.Env = append(os.Environ(), "TMUX_TMPDIR="+sockDir)
		_ = procutil.Run(ctx, killCmd, "kill sandboxed kenn-forge tmux server")
		_ = os.RemoveAll(sockDir)
	})

	fixture := setupWorkspaceServerFixtureUnconfiguredTmux(t, &config.Config{
		Agents: []config.Agent{{
			Key:     "helper",
			Label:   "Helper",
			Command: workspaceRuntimeHelperCommand("echo"),
		}},
	}, server.ServerOptions{
		DisableWorkspaceBackgroundMonitors: true,
		PtyOwnerInProcess:                  true,
		HostCheckAllowLoopbackAnyPort:      true,
	})
	ctx := t.Context()
	ws := createReadyWorkspace(t, ctx, fixture.client)

	// Attaching the base workspace terminal creates the tmux session
	// through the server's default tmux command.
	ts := httptest.NewServer(fixture.server)
	t.Cleanup(ts.Close)
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") +
		"/ws/v1/workspaces/" + ws.Id + "/terminal?cols=80&rows=24"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(err)
	defer conn.Close(websocket.StatusNormalClosure, "done")

	hasSessionOnForgeSocket := func() bool {
		probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		cmd := procutil.CommandContext(
			probeCtx, tmuxPath, "-L", "kenn-forge",
			"has-session", "-t", ws.TmuxSession,
		)
		cmd.Env = append(os.Environ(), "TMUX_TMPDIR="+sockDir)
		return procutil.Run(
			probeCtx, cmd, "probe sandboxed kenn-forge tmux server",
		) == nil
	}
	require.Eventually(
		hasSessionOnForgeSocket, 15*time.Second, 100*time.Millisecond,
		"workspace session %s never appeared on the kenn-forge socket",
		ws.TmuxSession,
	)

	require.NoError(conn.Close(websocket.StatusNormalClosure, "done"))

	// A tmux-wrapped agent launch must land on the same sandboxed
	// kenn-forge socket: the launcher's tmux client resolves -L under
	// TMUX_TMPDIR, so a client environment that dropped it would create
	// the session on a different tmux server than the manager owns.
	launch, err := fixture.client.HTTP.LaunchWorkspaceRuntimeSessionWithResponse(
		ctx, ws.Id,
		generated.LaunchWorkspaceRuntimeSessionInputBody{TargetKey: "helper"},
	)
	require.NoError(err)
	require.Equal(http.StatusOK, launch.StatusCode(), string(launch.Body))
	agentSessionPrefix := "forge-" + ws.Id + "-"
	require.Eventually(func() bool {
		probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		cmd := procutil.CommandContext(
			probeCtx, tmuxPath, "-L", "kenn-forge",
			"list-sessions", "-F", "#{session_name}",
		)
		cmd.Env = append(os.Environ(), "TMUX_TMPDIR="+sockDir)
		var stdout bytes.Buffer
		cmd.Stdout = &stdout
		if procutil.Run(
			probeCtx, cmd, "list sandboxed kenn-forge tmux sessions",
		) != nil {
			return false
		}
		return strings.Contains(stdout.String(), agentSessionPrefix)
	}, 15*time.Second, 100*time.Millisecond,
		"agent session %s* never appeared on the sandboxed kenn-forge socket",
		agentSessionPrefix,
	)

	deleteWorkspaceForPtyOwnerTest(t, ctx, fixture, ws.Id)
	require.Eventually(
		func() bool { return !hasSessionOnForgeSocket() },
		15*time.Second, 100*time.Millisecond,
		"workspace deletion left session %s on the kenn-forge socket",
		ws.TmuxSession,
	)
}
