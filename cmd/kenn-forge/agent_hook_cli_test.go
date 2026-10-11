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
	lock, err := runtimelock.Acquire(dataDir)
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
	lock, err := runtimelock.Acquire(dataDir)
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

func TestAgentHookProfilesSelectOneIntegration(t *testing.T) {
	profiles, err := selectedAgentHookProfiles("GeMiNi")

	require.NoError(t, err)
	assert.Equal(t, []agenthook.Agent{agenthook.AgentGemini}, profiles)
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
