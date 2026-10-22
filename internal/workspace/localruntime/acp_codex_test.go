package localruntime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
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
			a := &ACP{agentName: "@agentclientprotocol/codex-acp", state: ACPState{ConfigOptions: []ACPConfigOption{{ID: "mode", Type: "select", CurrentValue: "agent"}}}}
			assert.Equal(t, tc.mode, a.deriveCodexMode(t.Context(), []string{filepath.Join(t.TempDir(), "codex-acp")}, nil, "/workspace", ""))
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
		name, agent, initialMode  string
		command, env, wantCommand []string
		probe                     bool
	}{
		{name: "plain", agent: "@agentclientprotocol/codex-acp", command: []string{"codex-acp"}, wantCommand: []string{"codex-acp", "cli"}, probe: true},
		{name: "npx options", agent: "@agentclientprotocol/codex-acp", command: []string{"npx", "--package=@agentclientprotocol/codex-acp", "codex-acp"}, wantCommand: []string{"npx", "--package=@agentclientprotocol/codex-acp", "codex-acp", "cli"}, probe: true},
		{name: "node entry point", agent: "@agentclientprotocol/codex-acp", command: []string{"node", "/adapter/dist/index.js"}, wantCommand: []string{"node", "/adapter/dist/index.js", "cli"}, probe: true},
		{name: "bunx", agent: "@agentclientprotocol/codex-acp", command: []string{"bunx", "@agentclientprotocol/codex-acp"}, wantCommand: []string{"bunx", "@agentclientprotocol/codex-acp", "cli"}, probe: true},
		{name: "standalone", agent: "@agentclientprotocol/codex-acp", command: []string{"codex-acp-x86_64-linux"}, wantCommand: []string{"codex-acp-x86_64-linux", "cli"}, probe: true},
		{name: "Windows executable", agent: "@agentclientprotocol/codex-acp", command: []string{"Codex-ACP.EXE"}, wantCommand: []string{"Codex-ACP.EXE", "cli"}, probe: runtime.GOOS == "windows"},
		{name: "Windows package runner", agent: "@agentclientprotocol/codex-acp", command: []string{"NPX.CMD", "codex-acp"}, wantCommand: []string{"NPX.CMD", "codex-acp", "cli"}, probe: runtime.GOOS == "windows"},
		{name: "other agent", agent: "other", command: []string{"codex-acp"}},
		{name: "missing identity", command: []string{"codex-acp"}},
		{name: "explicit mode", agent: "@agentclientprotocol/codex-acp", initialMode: "read-only", command: []string{"codex-acp"}},
		{name: "explicit agent env", agent: "@agentclientprotocol/codex-acp", command: []string{"codex-acp"}, env: []string{"INITIAL_AGENT_MODE=agent"}},
		{name: "empty initial mode env", agent: "@agentclientprotocol/codex-acp", command: []string{"codex-acp"}, env: []string{"INITIAL_AGENT_MODE="}, wantCommand: []string{"codex-acp", "cli"}, probe: true},
		{name: "env key case", agent: "@agentclientprotocol/codex-acp", command: []string{"codex-acp"}, env: []string{"initial_agent_mode=agent"}, wantCommand: []string{"codex-acp", "cli"}, probe: runtime.GOOS != "windows"},
		{name: "env wrapper", agent: "@agentclientprotocol/codex-acp", command: []string{"env", `CODEX_CONFIG={"sandbox_mode":"read-only"}`, "codex-acp"}},
		{name: "shell wrapper", agent: "@agentclientprotocol/codex-acp", command: []string{"sh", "adapter.sh"}},
		{name: "cmd wrapper", agent: "@agentclientprotocol/codex-acp", command: []string{"cmd", "/c", "codex-acp"}},
		{name: "custom script", agent: "@agentclientprotocol/codex-acp", command: []string{"custom-adapter"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert := assert.New(t)
			calls := 0
			// Absolute launch paths keep these policy tests independent of installed tools.
			tc.command[0] = filepath.Join(t.TempDir(), tc.command[0])
			if tc.wantCommand != nil {
				tc.wantCommand[0] = tc.command[0]
			}
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
			initialMode := tc.initialMode
			if initialMode == "" {
				initialMode = "agent"
			}
			a := &ACP{agentName: tc.agent, state: ACPState{ConfigOptions: []ACPConfigOption{{ID: "mode", Type: "select", CurrentValue: initialMode}}}}
			mode := a.deriveCodexMode(t.Context(), tc.command, env, "/workspace", "")
			if tc.probe {
				assert.Equal(1, calls)
				assert.Equal("agent-full-access", mode)
			} else {
				assert.Zero(calls)
				assert.Empty(mode)
				assert.Empty(a.state.Notices)
			}
		})
	}
}

func TestCodexACPProbeResolvesExecutable(t *testing.T) {
	previous := codexEffectiveAccess
	t.Cleanup(func() { codexEffectiveAccess = previous })
	dir := t.TempDir()
	name := "codex-acp"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	executable := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(executable, nil, 0o700))
	for _, tc := range []struct {
		name, path string
		probe      bool
	}{
		{name: "PATH hit becomes absolute", path: dir, probe: true},
		{name: "missing executable adds notice", path: t.TempDir()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("PATH", tc.path)
			calls := 0
			codexEffectiveAccess = func(_ context.Context, command, _ []string, _ string, _ map[string]any) (string, string, error) {
				calls++
				assert.Equal(t, []string{executable, "cli"}, command)
				return "dangerFullAccess", "never", nil
			}
			a := &ACP{agentName: "@agentclientprotocol/codex-acp", state: ACPState{ConfigOptions: []ACPConfigOption{{ID: "mode", Type: "select", CurrentValue: "agent"}}}}
			mode := a.deriveCodexMode(t.Context(), []string{name}, nil, t.TempDir(), "")
			if tc.probe {
				assert.Equal(t, 1, calls)
				assert.Equal(t, "agent-full-access", mode)
			} else {
				assert.Zero(t, calls)
				assert.Empty(t, mode)
				require.Len(t, a.state.Notices, 1)
				assert.Contains(t, a.state.Notices[0], "couldn't read Codex's access settings")
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
