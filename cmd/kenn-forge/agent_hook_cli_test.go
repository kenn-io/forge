package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/agentactivity"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/procutil"
	"go.kenn.io/forge/internal/runtimelock"
	"go.kenn.io/kit/agenthook"
)

func TestAgentHookRunRelaysLifecyclePayloadToDaemon(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	var receivedPath, receivedRuntimeKey, receivedAuthorization string
	var receivedPayload []byte
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		receivedRuntimeKey = r.Header.Get("X-Kenn-Forge-Runtime-Session-Key")
		receivedAuthorization = r.Header.Get("Authorization")
		var err error
		receivedPayload, err = io.ReadAll(r.Body)
		assert.NoError(err)
		w.Header().Set("Content-Type", "application/json")
		_, err = io.WriteString(w, `{"hook_output":{"hookSpecificOutput":{"hookEventName":"SessionStart","additionalContext":"generated workspace context"}}}`)
		assert.NoError(err)
	}))
	t.Cleanup(daemon.Close)
	dataDir := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(os.WriteFile(
		configPath,
		fmt.Appendf(nil, "data_dir = %q\n", filepath.ToSlash(dataDir)),
		0o600,
	))
	token, err := runtimelock.EnsureAuthToken(dataDir)
	require.NoError(err)
	lock, err := runtimelock.Acquire(t.Context(), dataDir)
	require.NoError(err)
	t.Cleanup(func() { _ = lock.Release() })
	require.NoError(lock.WriteMetadata(runtimelock.Metadata{
		ListenAddr:  strings.TrimPrefix(daemon.URL, "http://"),
		TokenPath:   runtimelock.AuthTokenPath(dataDir),
		RequireAuth: true,
	}))
	t.Setenv(agentactivity.RuntimeSessionKeyEnv, "runtime-1")
	payload := `{"session_id":"agent-1","cwd":"/tmp/worktree","hook_event_name":"SessionStart","source":"startup"}`
	var stdout bytes.Buffer

	require.NoError(runAgentHookCLI([]string{
		"run", "--agent", "claude", "--config", configPath,
		"--source", "kenn-forge-agent-activity",
	}, strings.NewReader(payload), &stdout))

	var output struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	require.NoError(json.Unmarshal(stdout.Bytes(), &output))
	assert.Equal("SessionStart", output.HookSpecificOutput.HookEventName)
	assert.Equal("generated workspace context", output.HookSpecificOutput.AdditionalContext)
	assert.Equal("/api/v1/agent-hooks/claude", receivedPath)
	assert.Equal("runtime-1", receivedRuntimeKey)
	assert.Equal("Bearer "+token, receivedAuthorization)
	assert.JSONEq(payload, string(receivedPayload))
}

func TestAgentHookRunNormalizesGeminiLifecyclePayload(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	var receivedPath string
	var receivedPayload []byte
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		var err error
		receivedPayload, err = io.ReadAll(r.Body)
		assert.NoError(err)
		w.Header().Set("Content-Type", "application/json")
		_, err = io.WriteString(w, `{}`)
		assert.NoError(err)
	}))
	t.Cleanup(daemon.Close)
	dataDir := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(os.WriteFile(
		configPath,
		fmt.Appendf(nil, "data_dir = %q\n", filepath.ToSlash(dataDir)),
		0o600,
	))
	lock, err := runtimelock.Acquire(t.Context(), dataDir)
	require.NoError(err)
	t.Cleanup(func() { _ = lock.Release() })
	require.NoError(lock.WriteMetadata(runtimelock.Metadata{
		ListenAddr: strings.TrimPrefix(daemon.URL, "http://"),
	}))
	payload := `{
  "session_id":"gemini-1",
  "cwd":"/tmp/worktree",
  "hook_event_name":"BeforeTool",
  "tool_name":"run_shell_command",
  "tool_input":{"command":"true"}
}`
	var stdout bytes.Buffer

	require.NoError(runAgentHookCLI([]string{
		"run", "--agent", "gemini", "--config", configPath,
		"--source", "kenn-forge-agent-activity",
	}, strings.NewReader(payload), &stdout))

	assert.JSONEq(`{
  "session_id":"gemini-1",
  "cwd":"/tmp/worktree",
  "hook_event_name":"PreToolUse",
  "tool_name":"Bash",
  "tool_input":{"command":"true"}
}`, string(receivedPayload))
	assert.Equal("/api/v1/agent-hooks/gemini", receivedPath)
	assert.JSONEq(`{}`, stdout.String())
}

func TestAgentHookInstallDefaultsToEveryKitProfile(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("LOCALAPPDATA", home)
	t.Setenv("USERPROFILE", home)
	for _, env := range []string{
		"CLAUDE_CONFIG_DIR",
		"CODEX_HOME",
		"COPILOT_HOME",
		"GEMINI_CLI_HOME",
		"HERMES_HOME",
		"QWEN_HOME",
	} {
		t.Setenv(env, "")
	}
	configPath := filepath.Join(home, "forge.toml")
	require.NoError(os.WriteFile(
		configPath,
		fmt.Appendf(nil, "data_dir = %q\n", filepath.Join(home, "data")),
		0o600,
	))
	var output bytes.Buffer

	require.NoError(runAgentHookInstall("install", []string{
		"--config", configPath,
		"--binary", "/opt/kenn-forge",
	}, &output))

	for _, profile := range agenthook.Profiles() {
		plan, err := agenthook.PlanUninstall(profile.Agent, "", agentHookMarker)
		require.NoError(err)
		assert.True(plan.Changed, "%s hooks should carry the kenn-forge marker", profile.Agent)
		plan, err = agenthook.PlanUninstall(profile.Agent, "", "--agent "+string(profile.Agent))
		require.NoError(err)
		assert.True(plan.Changed, "%s hooks should route to their own agent", profile.Agent)
		assert.Contains(output.String(), "Installed kenn-forge "+profile.DisplayName+" hooks")
	}
}

func TestAgentHookInstallRejectsRelativeDataDirectory(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	require.NoError(os.WriteFile(configPath, []byte("data_dir = \"relative-data\"\n"), 0o600))
	configDir := filepath.Join(dir, "codex")
	t.Setenv("CODEX_HOME", configDir)

	err := runAgentHookInstall("install", []string{
		"--config", configPath,
		"--agent", "codex",
		"--binary", "/opt/kenn-forge",
	}, io.Discard)

	require.Error(err)
	assert.Contains(err.Error(), "absolute data_dir")
	_, statErr := os.Stat(filepath.Join(configDir, "hooks.json"))
	assert.ErrorIs(statErr, os.ErrNotExist)
}

func TestAgentHookInstallRejectsMissingConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CODEX_HOME", filepath.Join(dir, "codex"))
	configPath := filepath.Join(dir, "missing.toml")

	err := runAgentHookInstall("install", []string{
		"--config", configPath, "--agent", "codex", "--binary", "/opt/kenn-forge",
	}, io.Discard)

	require.ErrorIs(t, err, os.ErrNotExist)
	assert.NoFileExists(t, filepath.Join(dir, "codex", "hooks.json"))
}

func TestAgentHookRunIgnoresMalformedHookFlags(t *testing.T) {
	var stdout bytes.Buffer

	err := runAgentHookCLI(
		[]string{"run", "--not-a-hook-flag"},
		strings.NewReader(`{"hook_event_name":"SessionStart"}`),
		&stdout,
	)

	require.NoError(t, err)
	assert.Empty(t, stdout.String())
}

func TestAgentHookRunFailsOpenForMalformedPayload(t *testing.T) {
	var stdout bytes.Buffer

	err := runAgentHookCLI(
		[]string{
			"run", "--agent", "claude", "--source", "kenn-forge-agent-activity",
		},
		strings.NewReader(`not json`),
		&stdout,
	)

	require.NoError(t, err)
	assert.Empty(t, stdout.String())
}

func TestAgentHookSessionEndReportsDaemonFailure(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var status int
	var body string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.WriteHeader(status)
		_, err := io.WriteString(w, body)
		assert.NoError(t, err)
	}))
	t.Cleanup(server.Close)
	configPath := archiveCLITestConfig(t, server.URL, "", "test-token")
	receive := func(event string) error {
		input := `{"session_id":"chat","hook_event_name":"` + event +
			`","source":"startup","reason":"prompt_input_exit"}`
		return receiveAgentHook(
			t.Context(), "claude", configPath, agentHookSource, strings.NewReader(input), io.Discard,
		)
	}
	for _, tc := range []struct {
		name    string
		status  int
		body    string
		wantErr []string
	}{
		{
			name:   "server failure",
			status: http.StatusInternalServerError,
			body:   `{"detail":"record agent hook activity: remove D:\\activity\\report.json: access denied"}`,
			wantErr: []string{
				"500 Internal Server Error",
				`remove D:\activity\report.json: access denied`,
			},
		},
		{
			name:   "unavailable",
			status: http.StatusServiceUnavailable,
			body:   `{"detail":"daemon restarting"}`,
		},
		{
			name:    "unparseable body",
			status:  http.StatusInternalServerError,
			body:    "invalid",
			wantErr: []string{"500 Internal Server Error"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			mu.Lock()
			status, body = tc.status, tc.body
			mu.Unlock()
			err := receive("SessionEnd")
			if tc.wantErr == nil {
				require.NoError(err)
			} else {
				require.ErrorIs(err, errAgentHookResponse)
				for _, want := range tc.wantErr {
					require.ErrorContains(err, want)
				}
			}
			require.NoError(receive("SessionStart"), "only SessionEnd reports daemon failures")
		})
	}
	server.Close()
	require.NoError(t, receive("SessionEnd"), "an unreachable daemon must not fail the hook")
}

func TestSetupHookKeepsSelectedHome(t *testing.T) {
	for _, tc := range []struct {
		name, installHome, runHome, dataDir   string
		missingConfig, fileData, customConfig bool
	}{
		{name: "custom home", installHome: "custom"},
		{name: "default home", runHome: "other"},
		{name: "data directory override", installHome: "custom", dataDir: "override"},
		{name: "override before config exists", installHome: "custom", dataDir: "override", missingConfig: true},
		{name: "override with file data directory", dataDir: "override", fileData: true, customConfig: true},
		{name: "file data directory", fileData: true, customConfig: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := setupAgentTest(t, "/tmp/unused")
			t.Setenv("KENN_FORGE_DATA_DIR", "")
			require.NoError(t, os.Unsetenv("KENN_FORGE_DATA_DIR"))
			if tc.installHome == "" {
				t.Setenv("KENN_FORGE_HOME", "")
			}
			forgeHome := config.DefaultDataDir()
			if tc.dataDir != "" {
				forgeHome = filepath.Join(home, tc.dataDir)
				t.Setenv("KENN_FORGE_DATA_DIR", forgeHome)
			}
			configPath := config.DefaultConfigPath()
			if tc.customConfig {
				configPath = filepath.Join(home, "custom.toml")
			}
			require.NoError(t, os.MkdirAll(forgeHome, 0o700))
			fileData := ""
			if tc.fileData {
				fileDir := forgeHome
				if tc.dataDir != "" {
					fileDir = filepath.Join(home, "file-data")
				}
				fileData = fmt.Sprintf("data_dir = %q\n", fileDir)
			}
			require.NoError(t, os.WriteFile(configPath, []byte(fileData), 0o600))
			if tc.missingConfig {
				require.NoError(t, os.Remove(configPath))
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))
				_, _ = io.WriteString(w, `{"hook_output":{"hookSpecificOutput":{"additionalContext":"delivered"}}}`)
			}))
			t.Cleanup(server.Close)
			lock, err := runtimelock.Acquire(t.Context(), forgeHome)
			require.NoError(t, err)
			t.Cleanup(func() { _ = lock.Release() })
			require.NoError(t, lock.WriteMetadata(runtimelock.Metadata{PID: os.Getpid(), ListenAddr: strings.TrimPrefix(server.URL, "http://")}))
			require.NoError(t, os.WriteFile(filepath.Join(forgeHome, "auth_token"), []byte("test-token"), 0o600))
			exe, err := os.Executable()
			require.NoError(t, err)
			wrapper := filepath.Join(home, "hook-wrapper")
			require.NoError(t, os.WriteFile(wrapper, fmt.Appendf(nil, "#!/bin/sh\nGO_WANT_HELPER_PROCESS=1 exec %q -test.run=^TestSetupHookHelper$ -- \"$@\"\n", exe), 0o700))
			require.NoError(t, installAgentHooks("install", configPath, "claude", wrapper, io.Discard))
			if tc.missingConfig {
				require.NoError(t, os.WriteFile(configPath, []byte(""), 0o600))
			}
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
			if tc.fileData && tc.dataDir == "" {
				assert.NotContains(t, command, "--home")
				require.NoError(t, os.Unsetenv("KENN_FORGE_DATA_DIR"))
			} else {
				t.Setenv("KENN_FORGE_DATA_DIR", filepath.Join(home, "later-override"))
			}
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
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			if err := runAgentHookCLI(os.Args[i+2:], os.Stdin, os.Stdout); err != nil {
				os.Exit(1)
			}
			os.Exit(0)
		}
	}
}
