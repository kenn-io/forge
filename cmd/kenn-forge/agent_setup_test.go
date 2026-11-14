package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/config"
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
	home := setupAgentTest(t, "/tmp/forge-test-data")
	for _, enabled := range []bool{true, false} {
		var out bytes.Buffer
		cmd := newAgentCommand(func(_ context.Context, path string, _ time.Duration) (mcpQuickstartInfo, error) {
			return stoppedMCPQuickstart(enabled, path), nil
		})
		cmd.SetOut(&out)
		cmd.SetArgs([]string{"setup", "--agent", "claude", "--check"})
		require.ErrorIs(t, cmd.ExecuteContext(t.Context()), agentsetup.ErrNotReady)
		assert.Contains(t, out.String(), "Forge daemon isn't running")
		assert.Contains(t, out.String(), "kenn-forge daemon start")
		assert.Contains(t, out.String(), "then run `kenn-forge agent setup --agent claude`")
		if !enabled {
			assert.Less(t, strings.Index(out.String(), "[mcp].enabled = true"), strings.Index(out.String(), "kenn-forge daemon start"))
		}
		_, err := os.Stat(filepath.Join(home, "claude", "settings.json"))
		require.ErrorIs(t, err, os.ErrNotExist)
	}
}

func TestAgentSetupInactiveMCP(t *testing.T) {
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

func TestAgentSetupRelativeDataDir(t *testing.T) {
	home := setupAgentTest(t, "relative")
	info := daemonMCPQuickstart(mcpSettingsInfo{Enabled: true, ActiveURL: "http://127.0.0.1:8091/mcp"}, "")
	loader := func(context.Context, string, time.Duration) (mcpQuickstartInfo, error) { return info, nil }
	var out bytes.Buffer
	cmd := newAgentCommand(loader)
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"setup", "--agent", "codex", "--print"})
	require.NoError(t, cmd.ExecuteContext(t.Context()))
	assert.Contains(t, out.String(), "codex mcp add")
	out.Reset()
	cmd = newAgentCommand(loader)
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"setup", "--agent", "codex", "--json"})
	require.ErrorIs(t, cmd.ExecuteContext(t.Context()), agentsetup.ErrNotReady)
	var report agentsetup.Report
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Len(t, report.Rows, 2)
	assert.Equal(t, agentsetup.StateFailed, report.Rows[0].State)
	assert.Contains(t, report.Rows[0].NextStep, "absolute data_dir")
	data, err := os.ReadFile(filepath.Join(home, "codex", "config.toml"))
	require.NoError(t, err)
	assert.Contains(t, string(data), info.Endpoint)
}

func TestAgentSetupInstallsSelectedAgents(t *testing.T) {
	home := setupAgentTest(t, "/tmp/forge-test-data")
	bin := filepath.Join(home, "bin")
	require.NoError(t, os.MkdirAll(bin, 0o700))
	for _, name := range []string{"claude", "codex"} {
		require.NoError(t, os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\nexit 1\n"), 0o700))
	}
	info := daemonMCPQuickstart(mcpSettingsInfo{Enabled: true, ActiveURL: "http://127.0.0.1:8091/mcp", ActiveRequiresAuth: true}, "/tmp/token")
	calls := 0
	loader := func(context.Context, string, time.Duration) (mcpQuickstartInfo, error) { calls++; return info, nil }
	var before [][]byte
	paths := []string{filepath.Join(home, "claude", ".claude.json"), filepath.Join(home, "claude", "settings.json"), filepath.Join(home, "codex", "config.toml")}
	for range 2 {
		var out bytes.Buffer
		cmd := newAgentCommand(loader)
		cmd.SetOut(&out)
		cmd.SetArgs([]string{"setup", "--json"})
		require.ErrorIs(t, cmd.ExecuteContext(t.Context()), agentsetup.ErrNotReady)
		var report agentsetup.Report
		require.NoError(t, json.Unmarshal(out.Bytes(), &report))
		require.Len(t, report.Rows, 4)
		for i, path := range paths {
			data, err := os.ReadFile(path)
			require.NoError(t, err)
			if before == nil {
				continue
			}
			assert.Equal(t, before[i], data)
		}
		if before == nil {
			for _, path := range paths {
				data, err := os.ReadFile(path)
				require.NoError(t, err)
				before = append(before, data)
			}
		}
	}
	assert.Equal(t, 2, calls)
	assert.Contains(t, string(before[2]), `bearer_token_env_var = "KENN_FORGE_API_TOKEN"`)
	assert.Contains(t, string(before[0]), info.Endpoint)
}

func TestAgentSetupAuthGuidanceAndCursor(t *testing.T) {
	home := setupAgentTest(t, "/tmp/forge-test-data")
	require.NoError(t, os.MkdirAll(filepath.Join(home, "codex"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(home, "codex", "config.toml"), []byte("[mcp_servers.kenn-forge]\nurl = 'http://127.0.0.1:8091/mcp'\nbearer_token_env_var = 'KENN_FORGE_API_TOKEN'\nenabled = false\n"), 0o600))
	info := daemonMCPQuickstart(mcpSettingsInfo{Enabled: true, ActiveURL: "http://127.0.0.1:8091/mcp", ActiveRequiresAuth: true}, "/tmp/token")
	for _, agent := range []string{"codex", "cursor"} {
		var out bytes.Buffer
		cmd := newAgentCommand(func(context.Context, string, time.Duration) (mcpQuickstartInfo, error) { return info, nil })
		cmd.SetOut(&out)
		args := []string{"setup", "--agent", agent, "--json"}
		if agent == "codex" {
			args = append(args, "--check")
		}
		cmd.SetArgs(args)
		require.ErrorIs(t, cmd.ExecuteContext(t.Context()), agentsetup.ErrNotReady)
		var report agentsetup.Report
		require.NoError(t, json.Unmarshal(out.Bytes(), &report))
		require.Len(t, report.Rows, 2)
		if agent == "codex" {
			assert.Contains(t, report.Rows[1].NextStep, "enabled = true")
			assert.Contains(t, report.Rows[1].NextStep, "export KENN_FORGE_API_TOKEN from /tmp/token")
		} else {
			assert.Equal(t, agentsetup.StateUnknown, report.Rows[1].State)
			assert.Contains(t, report.Rows[1].NextStep, "kenn-forge mcp quickstart")
			data, err := os.ReadFile(filepath.Join(home, ".cursor", "hooks.json"))
			require.NoError(t, err)
			assert.Contains(t, string(data), "kenn-forge-agent-activity")
			_, err = os.Stat(filepath.Join(home, ".cursor", "mcp.json"))
			require.ErrorIs(t, err, os.ErrNotExist)
		}
	}
}
