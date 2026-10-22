package localruntime

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCodexACPDerivesMode(t *testing.T) {
	previous := codexEffectiveAccess
	t.Cleanup(func() { codexEffectiveAccess = previous })
	for _, tc := range []struct {
		name, sandbox, approval, mode, notice string
		err                                   error
	}{
		{name: "full access", sandbox: "dangerFullAccess", approval: "never", mode: "agent-full-access"},
		{name: "read only", sandbox: "readOnly", approval: "on-request", mode: "read-only"},
		{name: "full access with approvals", sandbox: "dangerFullAccess", approval: "on-request", notice: "dangerFullAccess with on-request"},
		{name: "read only without approvals", sandbox: "readOnly", approval: "never", notice: "readOnly with never"},
		{name: "adapter default", sandbox: "workspaceWrite", approval: "on-request"},
		{name: "custom approval", sandbox: "readOnly", notice: "readOnly with custom"},
		{name: "probe failure", err: errors.New("probe failed"), notice: "couldn't read Codex's access settings"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			codexEffectiveAccess = func(context.Context, []string, []string, string, map[string]any) (string, string, error) {
				return tc.sandbox, tc.approval, tc.err
			}
			a := &ACP{agentName: "@agentclientprotocol/codex-acp"}
			assert.Equal(t, tc.mode, a.deriveCodexMode(t.Context(), []string{"adapter"}, nil, "/workspace"))
			if tc.notice == "" {
				assert.Empty(t, a.state.Notices)
			} else {
				require.Len(t, a.state.Notices, 1)
				assert.Contains(t, a.state.Notices[0], tc.notice)
			}
		})
	}
}

func TestCodexACPProbeUsesLaunchContext(t *testing.T) {
	previous := codexEffectiveAccess
	t.Cleanup(func() { codexEffectiveAccess = previous })
	for _, tc := range []struct {
		name, agent               string
		command, env, wantCommand []string
		probe                     bool
	}{
		{name: "plain", agent: "@agentclientprotocol/codex-acp", command: []string{"codex-acp"}, wantCommand: []string{"codex-acp", "cli"}, probe: true},
		{name: "npx options", agent: "@agentclientprotocol/codex-acp", command: []string{"npx", "--package=@agentclientprotocol/codex-acp", "codex-acp"}, wantCommand: []string{"npx", "--package=@agentclientprotocol/codex-acp", "codex-acp", "cli"}, probe: true},
		{name: "node entry point", agent: "@agentclientprotocol/codex-acp", command: []string{"node", "/adapter/dist/index.js"}, wantCommand: []string{"node", "/adapter/dist/index.js", "cli"}, probe: true},
		{name: "other agent", agent: "other", command: []string{"codex-acp"}},
		{name: "missing identity", command: []string{"codex-acp"}},
		{name: "explicit mode", agent: "@agentclientprotocol/codex-acp", command: []string{"adapter"}, env: []string{"INITIAL_AGENT_MODE=read-only"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert := assert.New(t)
			calls := 0
			env := append(tc.env, `CODEX_CONFIG={"model":"example-model"}`)
			codexEffectiveAccess = func(ctx context.Context, command, gotEnv []string, cwd string, config map[string]any) (string, string, error) {
				calls++
				assert.Equal(tc.wantCommand, command)
				assert.Equal(env, gotEnv)
				assert.Equal("/workspace", cwd)
				assert.Equal(map[string]any{"model": "example-model"}, config)
				_, bounded := ctx.Deadline()
				assert.True(bounded)
				return "dangerFullAccess", "never", nil
			}
			a := &ACP{agentName: tc.agent}
			mode := a.deriveCodexMode(t.Context(), tc.command, env, "/workspace")
			if tc.probe {
				assert.Equal(1, calls)
				assert.Equal("agent-full-access", mode)
			} else {
				assert.Zero(calls)
				assert.Empty(mode)
			}
		})
	}
}

func TestCodexACPConnectionTestSkipsAccessProbe(t *testing.T) {
	t.Setenv("KENN_FORGE_LOCALRUNTIME_HELPER", "1")
	t.Setenv("KENN_FORGE_ACP_FIXTURE", "1")
	t.Setenv("KENN_FORGE_ACP_CODEX", "1")
	previous := codexEffectiveAccess
	t.Cleanup(func() { codexEffectiveAccess = previous })
	codexEffectiveAccess = func(context.Context, []string, []string, string, map[string]any) (string, string, error) {
		require.Fail(t, "connection test must not probe Codex access")
		return "", "", errors.New("unexpected probe")
	}
	executable, err := os.Executable()
	require.NoError(t, err)
	_, err = NewManager(Options{}).TestACP(t.Context(), []string{executable, "-test.run=^TestACPStdioHelper$"})
	require.NoError(t, err)
}
