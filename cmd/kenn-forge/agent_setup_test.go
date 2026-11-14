package main

import (
	"bytes"
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

	"github.com/BurntSushi/toml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/procutil"
	"go.kenn.io/forge/internal/runtimelock"
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

func TestSetupHookKeepsSelectedHome(t *testing.T) {
	for _, tc := range []struct{ name, installHome, runHome string }{
		{"custom home", "custom", ""},
		{"default home", "", "other"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := setupAgentTest(t, "/tmp/unused")
			if tc.installHome == "" {
				t.Setenv("KENN_FORGE_HOME", "")
			}
			forgeHome := config.DefaultDataDir()
			configPath := config.DefaultConfigPath()
			require.NoError(t, os.MkdirAll(forgeHome, 0o700))
			require.NoError(t, os.WriteFile(configPath, []byte(""), 0o600))
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.WriteString(w, `{"hook_output":{"hookSpecificOutput":{"additionalContext":"delivered"}}}`)
			}))
			t.Cleanup(server.Close)
			lock, err := runtimelock.Acquire(t.Context(), forgeHome)
			require.NoError(t, err)
			t.Cleanup(func() { _ = lock.Release() })
			require.NoError(t, lock.WriteMetadata(runtimelock.Metadata{PID: os.Getpid(), ListenAddr: strings.TrimPrefix(server.URL, "http://")}))
			exe, err := os.Executable()
			require.NoError(t, err)
			wrapper := filepath.Join(home, "hook-wrapper")
			require.NoError(t, os.WriteFile(wrapper, fmt.Appendf(nil, "#!/bin/sh\nexec %q -test.run=^TestSetupHookHelper$ -- \"$@\"\n", exe), 0o700))
			require.NoError(t, installAgentHooks("install", configPath, "claude", wrapper, io.Discard))
			data, err := os.ReadFile(filepath.Join(home, "claude", "settings.json"))
			require.NoError(t, err)
			var settings struct {
				Hooks map[string][]struct {
					Hooks []struct {
						Command string `json:"command"`
					} `json:"hooks"`
				} `json:"hooks"`
			}
			require.NoError(t, json.Unmarshal(data, &settings))
			require.NotEmpty(t, settings.Hooks["SessionStart"])
			command := settings.Hooks["SessionStart"][0].Hooks[0].Command
			if tc.runHome == "" {
				t.Setenv("KENN_FORGE_HOME", "")
			} else {
				t.Setenv("KENN_FORGE_HOME", filepath.Join(home, tc.runHome))
			}
			cmd := procutil.CommandContext(t.Context(), "/bin/sh", "-c", command)
			cmd.Stdin = strings.NewReader(`{"session_id":"a","cwd":"/tmp","hook_event_name":"SessionStart","source":"startup"}`)
			out, err := cmd.CombinedOutput()
			require.NoError(t, err, string(out))
			assert.Contains(t, string(out), "delivered")
		})
	}
}

func TestSetupHookHelper(t *testing.T) {
	for i, arg := range os.Args {
		if arg == "--" {
			if err := runAgentHookCLI(os.Args[i+2:], os.Stdin, os.Stdout); err != nil {
				os.Exit(1)
			}
			os.Exit(0)
		}
	}
}

func TestAgentSetupStopped(t *testing.T) {
	for _, tc := range []struct {
		name     string
		enabled  bool
		inactive bool
		print    bool
	}{
		{name: "check enabled", enabled: true},
		{name: "check disabled"},
		{name: "check inactive", enabled: true, inactive: true},
		{name: "print", enabled: true, print: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setupAgentTest(t, "/tmp/forge-test-data")
			var out bytes.Buffer
			cmd := newAgentCommand(func(_ context.Context, path string, _ time.Duration) (mcpQuickstartInfo, error) {
				if tc.inactive {
					info := daemonMCPQuickstart(mcpSettingsInfo{Enabled: true}, "")
					info.pid = 123
					return info, nil
				}
				return stoppedMCPQuickstart(tc.enabled, path), nil
			})
			cmd.SetOut(&out)
			flag := "--check"
			if tc.print {
				flag = "--print"
			}
			cmd.SetArgs([]string{"setup", "--agent", "claude", flag})
			err := cmd.ExecuteContext(t.Context())
			if tc.print {
				require.ErrorContains(t, err, "Forge daemon isn't running")
				require.ErrorContains(t, err, "kenn-forge daemon start")
				return
			}
			require.ErrorIs(t, err, agentsetup.ErrNotReady)
			text := out.String()
			assert.Contains(t, text, "then run `kenn-forge agent setup --agent claude`")
			assert.NotContains(t, text, "., then run")
			switch {
			case tc.inactive:
				assert.Contains(t, text, "Forge MCP isn't active")
				assert.Contains(t, text, "Restart the Forge daemon")
			case !tc.enabled:
				assert.Less(t, strings.Index(text, "[mcp].enabled = true"), strings.Index(text, "kenn-forge daemon start"))
				fallthrough
			default:
				assert.Contains(t, text, "Forge daemon isn't running")
				assert.Contains(t, text, "kenn-forge daemon start")
			}
		})
	}
}

func TestAgentSetupHookConfigErrors(t *testing.T) {
	for _, tc := range []struct {
		name      string
		dataDir   string
		malformed bool
	}{
		{name: "relative data directory", dataDir: "relative"},
		{name: "malformed config", dataDir: "/tmp/forge-test-data", malformed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := setupAgentTest(t, tc.dataDir)
			info := daemonMCPQuickstart(mcpSettingsInfo{Enabled: true, ActiveURL: "http://127.0.0.1:8091/mcp"}, "")
			loader := func(ctx context.Context, path string, timeout time.Duration) (mcpQuickstartInfo, error) {
				if tc.malformed {
					return loadMCPQuickstart(ctx, path, timeout)
				}
				return info, nil
			}
			if tc.malformed {
				require.NoError(t, os.WriteFile(config.DefaultConfigPath(), []byte("[invalid\n"), 0o600))
			} else {
				var out bytes.Buffer
				cmd := newAgentCommand(loader)
				cmd.SetOut(&out)
				cmd.SetArgs([]string{"setup", "--agent", "codex", "--print"})
				require.NoError(t, cmd.ExecuteContext(t.Context()))
				assert.Contains(t, out.String(), "codex mcp add")
			}
			var out bytes.Buffer
			cmd := newAgentCommand(loader)
			cmd.SetOut(&out)
			cmd.SetArgs([]string{"setup", "--agent", "codex", "--json"})
			require.ErrorIs(t, cmd.ExecuteContext(t.Context()), agentsetup.ErrNotReady)
			var report agentsetup.Report
			require.NoError(t, json.Unmarshal(out.Bytes(), &report))
			require.Len(t, report.Rows, 2)
			assert.Equal(t, agentsetup.StateFailed, report.Rows[0].State)
			if tc.malformed {
				assert.Equal(t, "Check the Forge config and retry.", report.Rows[0].NextStep)
				assert.Contains(t, report.Rows[1].Reason, "load config")
				assert.Contains(t, report.Rows[1].NextStep, "config")
				assert.NotContains(t, report.Rows[1].NextStep, "mcp quickstart")
			} else {
				assert.Contains(t, report.Rows[0].Reason, "absolute data_dir")
				assert.Equal(t, "Set an absolute data_dir in the Forge config.", report.Rows[0].NextStep)
				data, err := os.ReadFile(filepath.Join(home, "codex", "config.toml"))
				require.NoError(t, err)
				assert.Contains(t, string(data), info.Endpoint)
			}
		})
	}
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
	var out bytes.Buffer
	cmd := newAgentCommand(loader)
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"setup", "--json"})
	require.ErrorIs(t, cmd.ExecuteContext(t.Context()), agentsetup.ErrNotReady)
	var report agentsetup.Report
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Len(t, report.Rows, 4)
	assert.Equal(t, 1, calls)
	claude, err := os.ReadFile(filepath.Join(home, "claude", ".claude.json"))
	require.NoError(t, err)
	assert.Contains(t, string(claude), info.Endpoint)
}

func TestAgentSetupMCPTokenSettings(t *testing.T) {
	for _, agent := range []string{"claude", "codex"} {
		for _, tc := range []struct {
			name      string
			auth      bool
			token     string
			wantToken string
		}{
			{name: "auth on", auth: true, wantToken: "KENN_FORGE_API_TOKEN"},
			{name: "auth off removes Forge token", token: "KENN_FORGE_API_TOKEN"},
			{name: "auth off preserves user token", token: "USER_TOKEN", wantToken: "USER_TOKEN"},
		} {
			t.Run(agent+"/"+tc.name, func(t *testing.T) {
				home := setupAgentTest(t, "/tmp/forge-test-data")
				var path, contents string
				if agent == "codex" {
					path = filepath.Join(home, "codex", "config.toml")
					contents = "[mcp_servers.kenn-forge]\nurl = 'http://127.0.0.1:8091/mcp'\n"
					if tc.token != "" {
						contents += "bearer_token_env_var = '" + tc.token + "'\n"
					}
				} else {
					path = filepath.Join(home, "claude", ".claude.json")
					contents = `{"mcpServers":{"kenn-forge":{"type":"http","url":"http://127.0.0.1:8091/mcp"`
					if tc.token != "" {
						contents += `,"headers":{"Authorization":"Bearer ${` + tc.token + `}"}`
					}
					contents += `}}}`
				}
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
				require.NoError(t, os.WriteFile(path, []byte(contents), 0o600))
				info := daemonMCPQuickstart(mcpSettingsInfo{Enabled: true, ActiveURL: "http://127.0.0.1:8091/mcp", ActiveRequiresAuth: tc.auth}, "/tmp/token")
				item := forgeSetupMCP(agent, info, nil, "kenn-forge agent setup", config.DefaultConfigPath())
				require.NoError(t, item.Install(t.Context()))
				data, err := os.ReadFile(path)
				require.NoError(t, err)
				var document map[string]any
				if agent == "codex" {
					require.NoError(t, toml.Unmarshal(data, &document))
					server := document["mcp_servers"].(map[string]any)["kenn-forge"].(map[string]any)
					if tc.wantToken == "" {
						assert.NotContains(t, server, "bearer_token_env_var")
					} else {
						assert.Equal(t, tc.wantToken, server["bearer_token_env_var"])
					}
				} else {
					require.NoError(t, json.Unmarshal(data, &document))
					server := document["mcpServers"].(map[string]any)["kenn-forge"].(map[string]any)
					headers, _ := server["headers"].(map[string]any)
					if tc.wantToken == "" {
						assert.NotContains(t, headers, "Authorization")
					} else {
						assert.Equal(t, "Bearer ${"+tc.wantToken+"}", headers["Authorization"])
					}
				}
			})
		}
	}
}

func TestAgentSetupAuthGuidance(t *testing.T) {
	home := setupAgentTest(t, "/tmp/forge-test-data")
	require.NoError(t, os.MkdirAll(filepath.Join(home, "codex"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(home, "codex", "config.toml"), []byte("[mcp_servers.kenn-forge]\nurl = 'http://127.0.0.1:8091/mcp'\nbearer_token_env_var = 'KENN_FORGE_API_TOKEN'\nenabled = false\n"), 0o600))
	info := daemonMCPQuickstart(mcpSettingsInfo{Enabled: true, ActiveURL: "http://127.0.0.1:8091/mcp", ActiveRequiresAuth: true}, "/tmp/token")
	run := func(args ...string) (string, error) {
		var out bytes.Buffer
		cmd := newAgentCommand(func(context.Context, string, time.Duration) (mcpQuickstartInfo, error) { return info, nil })
		cmd.SetOut(&out)
		cmd.SetArgs(append([]string{"setup", "--agent", "codex"}, args...))
		err := cmd.ExecuteContext(t.Context())
		return out.String(), err
	}
	t.Run("check", func(t *testing.T) {
		out, err := run("--json", "--check")
		require.ErrorIs(t, err, agentsetup.ErrNotReady)
		var report agentsetup.Report
		require.NoError(t, json.Unmarshal([]byte(out), &report))
		require.Len(t, report.Rows, 2)
		assert.Contains(t, report.Rows[1].NextStep, "export KENN_FORGE_API_TOKEN from /tmp/token")
	})
	t.Run("print", func(t *testing.T) {
		out, err := run("--print")
		require.NoError(t, err)
		assert.Contains(t, out, "export KENN_FORGE_API_TOKEN=\"$(cat /tmp/token)\"")
	})
}

func TestAgentSetupCursor(t *testing.T) {
	home := setupAgentTest(t, "/tmp/forge-test-data")
	info := daemonMCPQuickstart(mcpSettingsInfo{Enabled: true, ActiveURL: "http://127.0.0.1:8091/mcp", ActiveRequiresAuth: true}, "/tmp/token")
	t.Run("json", func(t *testing.T) {
		var out bytes.Buffer
		cmd := newAgentCommand(func(context.Context, string, time.Duration) (mcpQuickstartInfo, error) { return info, nil })
		cmd.SetOut(&out)
		cmd.SetArgs([]string{"setup", "--agent", "cursor", "--json"})
		require.ErrorIs(t, cmd.ExecuteContext(t.Context()), agentsetup.ErrNotReady)
		var report agentsetup.Report
		require.NoError(t, json.Unmarshal(out.Bytes(), &report))
		require.Len(t, report.Rows, 2)
		assert.Equal(t, agentsetup.StateUnknown, report.Rows[1].State)
		assert.Contains(t, report.Rows[1].NextStep, "kenn-forge mcp quickstart")
		data, err := os.ReadFile(filepath.Join(home, ".cursor", "hooks.json"))
		require.NoError(t, err)
		assert.Contains(t, string(data), "kenn-forge-agent-activity")
		_, err = os.Stat(filepath.Join(home, ".cursor", "mcp.json"))
		require.ErrorIs(t, err, os.ErrNotExist)
	})
	t.Run("print", func(t *testing.T) {
		cmd := newAgentCommand(func(context.Context, string, time.Duration) (mcpQuickstartInfo, error) { return info, nil })
		cmd.SetArgs([]string{"setup", "--agent", "cursor", "--print"})
		err := cmd.ExecuteContext(t.Context())
		require.ErrorContains(t, err, "token reference is unavailable for cursor")
		require.ErrorContains(t, err, "kenn-forge mcp quickstart")
	})
}
