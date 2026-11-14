package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/procutil"
	"go.kenn.io/kit/agentsetup"
)

func setupAgentTest(t *testing.T, dataDir string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
	t.Setenv("KENN_FORGE_HOME", filepath.Join(home, "forge"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "claude"))
	t.Setenv("CODEX_HOME", filepath.Join(home, "codex"))
	t.Setenv("PATH", filepath.Join(home, "bin"))
	t.Setenv("KENN_FORGE_API_TOKEN", "")
	require.NoError(t, os.MkdirAll(filepath.Dir(config.DefaultConfigPath()), 0o700))
	require.NoError(t, os.WriteFile(config.DefaultConfigPath(), []byte("data_dir = '"+dataDir+"'\n"), 0o600))
	return home
}

func TestAgentSetupStopped(t *testing.T) {
	setupAgentTest(t, "/tmp/forge-test-data")
	var out bytes.Buffer
	cmd := newAgentCommand(func(context.Context, string, time.Duration) (mcpQuickstartInfo, error) {
		info := daemonMCPQuickstart(mcpSettingsInfo{Enabled: true}, "")
		info.pid = 123
		return info, nil
	})
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"setup", "--agent", "claude", "--check"})
	require.ErrorIs(t, cmd.ExecuteContext(t.Context()), agentsetup.ErrNotReady)
	assert.Contains(t, out.String(), "Forge MCP isn't active")
	assert.Contains(t, out.String(), "Restart the Forge daemon")
}

func TestAgentSetupStoppedPrint(t *testing.T) {
	setupAgentTest(t, "/tmp/forge-test-data")
	cmd := newAgentCommand(func(_ context.Context, path string, _ time.Duration) (mcpQuickstartInfo, error) {
		return stoppedMCPQuickstart(true, path), nil
	})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"setup", "--agent", "claude", "--print"})
	err := cmd.ExecuteContext(t.Context())
	require.ErrorContains(t, err, "Forge daemon isn't running")
	require.ErrorContains(t, err, "kenn-forge daemon start")
}

func TestAgentSetupRelativeDataDir(t *testing.T) {
	home := setupAgentTest(t, "relative")
	info := daemonMCPQuickstart(mcpSettingsInfo{Enabled: true, ActiveURL: "http://127.0.0.1:8091/mcp"}, "")
	loader := func(context.Context, string, time.Duration) (mcpQuickstartInfo, error) { return info, nil }
	var out bytes.Buffer
	cmd := newAgentCommand(loader)
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"setup", "--agent", "codex", "--json"})
	require.ErrorIs(t, cmd.ExecuteContext(t.Context()), agentsetup.ErrNotReady)
	var report agentsetup.Report
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Len(t, report.Rows, 2)
	assert.Equal(t, agentsetup.StateFailed, report.Rows[0].State)
	assert.Contains(t, report.Rows[0].Reason, "absolute data_dir")
	assert.Equal(t, "Check the Forge config and retry.", report.Rows[0].NextStep)
	data, err := os.ReadFile(filepath.Join(home, "codex", "config.toml"))
	require.NoError(t, err)
	assert.Contains(t, string(data), info.Endpoint)
}

func TestAgentSetupMalformedConfig(t *testing.T) {
	setupAgentTest(t, "/tmp/forge-test-data")
	require.NoError(t, os.WriteFile(config.DefaultConfigPath(), []byte("[invalid\n"), 0o600))
	var out bytes.Buffer
	cmd := newAgentCommand(loadMCPQuickstart)
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"setup", "--agent", "codex", "--json"})
	require.ErrorIs(t, cmd.ExecuteContext(t.Context()), agentsetup.ErrNotReady)
	var report agentsetup.Report
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Len(t, report.Rows, 2)
	assert.Contains(t, report.Rows[1].Reason, "load config")
	assert.Contains(t, report.Rows[1].NextStep, "config")
	assert.NotContains(t, report.Rows[1].NextStep, "mcp quickstart")
}

func TestAgentSetupSelectedConfig(t *testing.T) {
	home := setupAgentTest(t, "/tmp/forge-test-data")
	path := filepath.Join(home, "custom config.toml")
	require.NoError(t, os.WriteFile(path, []byte("data_dir = '/tmp/custom-data'\n"), 0o600))
	var out bytes.Buffer
	cmd := newAgentCommand(func(_ context.Context, got string, _ time.Duration) (mcpQuickstartInfo, error) {
		assert.Equal(t, path, got)
		return stoppedMCPQuickstart(true, got), nil
	})
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"setup", "--agent", "claude", "--config", path})
	require.ErrorIs(t, cmd.ExecuteContext(t.Context()), agentsetup.ErrNotReady)
	data, err := os.ReadFile(filepath.Join(home, "claude", "settings.json"))
	require.NoError(t, err)
	assert.Contains(t, string(data), path)
	assert.Contains(t, out.String(), "then run `kenn-forge agent setup --config '"+path+"' --agent claude`")
}

func TestAgentSetupForwardsSelectedAgents(t *testing.T) {
	setupAgentTest(t, "/tmp/forge-test-data")
	var out bytes.Buffer
	cmd := newAgentCommand(func(_ context.Context, path string, _ time.Duration) (mcpQuickstartInfo, error) {
		return stoppedMCPQuickstart(true, path), nil
	})
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"setup", "--agent", "claude,codex", "--check", "--json"})
	require.ErrorIs(t, cmd.ExecuteContext(t.Context()), agentsetup.ErrNotReady)
	var report agentsetup.Report
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Len(t, report.Rows, 4)
	assert.Equal(t, "claude", report.Rows[0].Agent)
	assert.Equal(t, "codex", report.Rows[2].Agent)
}

func TestAgentSetupMCPTokenSettings(t *testing.T) {
	for _, tc := range []struct {
		name      string
		auth      bool
		token     string
		wantToken string
	}{
		{name: "auth on", auth: true, wantToken: "KENN_FORGE_API_TOKEN"},
		{name: "auth off removes Forge token", token: "KENN_FORGE_API_TOKEN"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := setupAgentTest(t, "/tmp/forge-test-data")
			path := filepath.Join(home, "claude", ".claude.json")
			contents := `{"mcpServers":{"kenn-forge":{"type":"http","url":"http://127.0.0.1:8091/mcp"`
			if tc.token != "" {
				contents += `,"headers":{"Authorization":"Bearer ${` + tc.token + `}"}`
			}
			contents += `}}}`
			require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
			require.NoError(t, os.WriteFile(path, []byte(contents), 0o600))
			info := daemonMCPQuickstart(mcpSettingsInfo{Enabled: true, ActiveURL: "http://127.0.0.1:8091/mcp", ActiveRequiresAuth: tc.auth}, "/tmp/token")
			item := forgeSetupMCP("claude", info, nil, "kenn-forge agent setup")
			require.NoError(t, item.Install(t.Context()))
			data, err := os.ReadFile(path)
			require.NoError(t, err)
			var document map[string]any
			require.NoError(t, json.Unmarshal(data, &document))
			server := document["mcpServers"].(map[string]any)["kenn-forge"].(map[string]any)
			headers, _ := server["headers"].(map[string]any)
			if tc.wantToken == "" {
				assert.NotContains(t, headers, "Authorization")
			} else {
				assert.Equal(t, "Bearer ${"+tc.wantToken+"}", headers["Authorization"])
			}
		})
	}
}

func TestAgentSetupAuthGuidance(t *testing.T) {
	setupAgentTest(t, "/tmp/forge-test-data")
	info := daemonMCPQuickstart(mcpSettingsInfo{Enabled: true, ActiveURL: "http://127.0.0.1:8091/mcp", ActiveRequiresAuth: true}, "/tmp/token")
	var out bytes.Buffer
	cmd := newAgentCommand(func(context.Context, string, time.Duration) (mcpQuickstartInfo, error) { return info, nil })
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"setup", "--agent", "codex", "--print"})
	require.NoError(t, cmd.ExecuteContext(t.Context()))
	assert.Contains(t, out.String(), "export KENN_FORGE_API_TOKEN=\"$(cat /tmp/token)\"")
	out.Reset()
	cmd = newAgentCommand(func(context.Context, string, time.Duration) (mcpQuickstartInfo, error) { return info, nil })
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"setup", "--agent", "codex", "--check", "--json"})
	require.ErrorIs(t, cmd.ExecuteContext(t.Context()), agentsetup.ErrNotReady)
	var report agentsetup.Report
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Len(t, report.Rows, 2)
	assert.Contains(t, report.Rows[1].NextStep, "export KENN_FORGE_API_TOKEN from /tmp/token")
}

func TestAgentSetupPowerShellTokenPath(t *testing.T) {
	pwsh, err := exec.LookPath("pwsh")
	if err != nil {
		t.Skip("pwsh is not installed")
	}
	t.Setenv(mcpTokenEnvironment, "")
	for _, name := range []string{"token's file", "token[1]"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), name)
			require.NoError(t, os.WriteFile(path, []byte("example-token\n"), 0o600))
			info := daemonMCPQuickstart(mcpSettingsInfo{Enabled: true, ActiveURL: "http://127.0.0.1:8091/mcp", ActiveRequiresAuth: true}, path)
			item := forgeSetupMCP("codex", info, nil, "kenn-forge agent setup")
			commands, err := item.Commands()
			require.NoError(t, err)
			line, _, found := strings.Cut(commands.PowerShell, "\n")
			require.True(t, found)
			cmd := procutil.CommandContext(t.Context(), pwsh, "-NoProfile", "-NonInteractive", "-Command", "$ErrorActionPreference = 'Stop'\n"+line+"\n[Console]::Write($env:KENN_FORGE_API_TOKEN)")
			out, err := cmd.CombinedOutput()
			require.NoError(t, err, string(out))
			assert.Equal(t, "example-token", string(out))
		})
	}
}

func TestAgentSetupWithoutConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("KENN_FORGE_HOME", filepath.Join(home, "forge"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "claude"))
	var out bytes.Buffer
	cmd := newAgentCommand(loadMCPQuickstart)
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"setup", "--agent", "claude", "--check", "--json"})
	require.ErrorIs(t, cmd.ExecuteContext(t.Context()), agentsetup.ErrNotReady)
	var report agentsetup.Report
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Len(t, report.Rows, 2)
	assert.Equal(t, agentsetup.StateMissing, report.Rows[0].State)
	assert.Contains(t, report.Rows[1].Reason, "Forge daemon isn't running")
	assert.Contains(t, report.Rows[1].NextStep, "kenn-forge daemon start")
	entries, err := os.ReadDir(home)
	require.NoError(t, err)
	assert.Empty(t, entries)
}
