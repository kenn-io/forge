package workspacetest

import (
	"bytes"
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/procutil"
	"go.kenn.io/forge/internal/server"
)

// TestWorkspaceTmuxServerEnvironmentExcludesTokensE2E proves the tmux
// server spawned for a workspace never sees provider tokens from the
// daemon environment. tmux retains its spawn environment globally
// (`show-environment -g`), so an agent inside any pane could otherwise
// read tokens the runtime environment stripping already hides from pane
// processes. The test uses a test-scoped socket rather than the shared
// package server so the spawn happens inside this test with the token
// variables present in the daemon (test process) environment.
func TestWorkspaceTmuxServerEnvironmentExcludesTokensE2E(t *testing.T) {
	if len(workspaceTestTmuxCommand) == 0 {
		t.Skip("tmux is required")
	}
	require := require.New(t)
	assert := assert.New(t)
	tmuxPath := workspaceTestTmuxCommand[0]

	t.Setenv("GITHUB_TOKEN", "workspacetest-builtin-secret")
	t.Setenv("WORKSPACETEST_CUSTOM_TOKEN", "workspacetest-custom-secret")
	// The server environment is allowlisted, not strip-listed: secrets
	// the config never names must stay out too.
	t.Setenv("KATA_AUTH_TOKEN", "workspacetest-kata-secret")
	t.Setenv("KENN_FORGE_FORGEJO_TOKEN", "workspacetest-forgejo-secret")
	t.Setenv("WORKSPACETEST_UNDECLARED_SECRET", "workspacetest-undeclared")
	// A configured token name hiding under an allowlisted prefix must
	// still be stripped, and benign daemon variables must reach the
	// base terminal pane through the env-file handoff.
	t.Setenv("XDG_WORKSPACETEST_TOKEN", "workspacetest-xdg-secret")
	t.Setenv("WKSP_BENIGN_PROXY", "workspacetest-proxy-value")

	sockDir, err := os.MkdirTemp("/tmp", "kenn-forge-tmux-env-*")
	require.NoError(err)
	socket := filepath.Join(sockDir, "tmux.sock")
	tmuxCommand := []string{tmuxPath, "-f", "/dev/null", "-S", socket}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = procutil.Run(ctx, procutil.CommandContext(
			ctx, tmuxPath, "-f", "/dev/null", "-S", socket, "kill-server",
		), "kill test-scoped tmux server")
		_ = os.RemoveAll(sockDir)
	})

	fixture := setupWorkspaceServerFixture(t, &config.Config{
		GitHubTokenEnv: "WORKSPACETEST_CUSTOM_TOKEN",
		GitHubOwnerTokens: []config.GitHubOwnerTokenConfig{
			{Owner: "acme", TokenEnv: "XDG_WORKSPACETEST_TOKEN"},
		},
		Tmux: config.Tmux{Command: tmuxCommand},
	}, server.ServerOptions{
		DisableWorkspaceBackgroundMonitors: true,
		PtyOwnerInProcess:                  true,
		HostCheckAllowLoopbackAnyPort:      true,
	})
	ctx := t.Context()
	ws := createReadyWorkspace(t, ctx, fixture.client)

	// Attaching the base workspace terminal spawns the tmux server on
	// the test-scoped socket via the manager's new-session path.
	ts := httptest.NewServer(fixture.server)
	t.Cleanup(ts.Close)
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") +
		"/ws/v1/workspaces/" + ws.Id + "/terminal?cols=80&rows=24"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(err)
	defer conn.Close(websocket.StatusNormalClosure, "done")

	require.Eventually(func() bool {
		probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		return procutil.Run(probeCtx, procutil.CommandContext(
			probeCtx, tmuxPath, "-f", "/dev/null", "-S", socket,
			"has-session", "-t", ws.TmuxSession,
		), "probe test-scoped tmux server") == nil
	}, 15*time.Second, 100*time.Millisecond,
		"workspace session never appeared on the test-scoped socket")

	envCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	envCmd := procutil.CommandContext(
		envCtx, tmuxPath, "-f", "/dev/null", "-S", socket,
		"show-environment", "-g",
	)
	var stdout bytes.Buffer
	envCmd.Stdout = &stdout
	require.NoError(procutil.Run(
		envCtx, envCmd, "read tmux server global environment",
	))
	globalEnv := stdout.String()

	require.Contains(globalEnv, "PATH=",
		"probe must return a real global environment")
	assert.NotContains(globalEnv, "GITHUB_TOKEN",
		"built-in token variables must not reach the tmux server environment")
	assert.NotContains(globalEnv, "WORKSPACETEST_CUSTOM_TOKEN",
		"configured token variables must not reach the tmux server environment")
	assert.NotContains(globalEnv, "workspacetest-builtin-secret")
	assert.NotContains(globalEnv, "workspacetest-custom-secret")
	assert.NotContains(globalEnv, "KATA_AUTH_TOKEN",
		"Kata credentials must not reach the tmux server environment")
	assert.NotContains(globalEnv, "KENN_FORGE_FORGEJO_TOKEN",
		"provider tokens must not reach the tmux server environment even when unconfigured")
	assert.NotContains(globalEnv, "WORKSPACETEST_UNDECLARED_SECRET",
		"variables outside the allowlist must never reach the tmux server environment")
	assert.NotContains(globalEnv, "workspacetest-kata-secret")
	assert.NotContains(globalEnv, "workspacetest-forgejo-secret")
	assert.NotContains(globalEnv, "workspacetest-undeclared")
	assert.NotContains(globalEnv, "XDG_WORKSPACETEST_TOKEN",
		"configured token names must be stripped even under allowlisted prefixes")
	assert.NotContains(globalEnv, "workspacetest-xdg-secret")

	// The base terminal pane receives the credential-sanitized full
	// daemon environment through the env-file handoff: benign daemon
	// variables stay usable while tokens never appear.
	probe := "printf 'PROBE:%s:%s:%s:%s:%s:%s:%s\\n' \"$WKSP_BENIGN_PROXY\" " +
		"\"${GITHUB_TOKEN:-unset}\" \"${KATA_AUTH_TOKEN:-unset}\" " +
		"\"${KENN_FORGE_FORGEJO_TOKEN:-unset}\" " +
		"\"${WORKSPACETEST_CUSTOM_TOKEN:-unset}\" " +
		"\"${XDG_WORKSPACETEST_TOKEN:-unset}\" " +
		"\"${TMUX:+tmux-set}\"\r"
	require.NoError(conn.Write(ctx, websocket.MessageBinary, []byte(probe)))
	readCtx, cancelRead := context.WithTimeout(ctx, 15*time.Second)
	defer cancelRead()
	var seen strings.Builder
	for !strings.Contains(
		seen.String(),
		"PROBE:workspacetest-proxy-value:unset:unset:unset:unset:unset:tmux-set",
	) {
		typ, data, readErr := conn.Read(readCtx)
		require.NoError(readErr,
			"terminal closed before the env probe answered: %s", seen.String())
		if typ != websocket.MessageBinary {
			continue
		}
		seen.Write(data)
	}
}
