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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/kit/agenthook"
	"go.kenn.io/middleman/internal/agentactivity"
	"go.kenn.io/middleman/internal/runtimelock"
)

func TestAgentHookRunRelaysLifecyclePayloadToDaemon(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	var receivedPath, receivedRuntimeKey, receivedAuthorization string
	var receivedPayload []byte
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		receivedRuntimeKey = r.Header.Get("X-Middleman-Runtime-Session-Key")
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
		"--source", "middleman-agent-activity",
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
		"--source", "middleman-agent-activity",
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

func TestAgentHookProfilesDefaultToEveryKitProfile(t *testing.T) {
	profiles, err := selectedAgentHookProfiles("")

	require.NoError(t, err)
	assert.Equal(t, []agenthook.Agent{
		agenthook.AgentClaude,
		agenthook.AgentCodex,
		agenthook.AgentCopilot,
		agenthook.AgentCursor,
		agenthook.AgentDroid,
		agenthook.AgentGemini,
		agenthook.AgentHermes,
		agenthook.AgentQwen,
	}, profiles)
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
	configPath := filepath.Join(home, "middleman.toml")
	require.NoError(os.WriteFile(
		configPath,
		fmt.Appendf(nil, "data_dir = %q\n", filepath.Join(home, "data")),
		0o600,
	))
	var output bytes.Buffer

	require.NoError(runAgentHookInstall("install", []string{
		"--config", configPath,
		"--binary", "/opt/middleman",
	}, &output))

	for _, profile := range agenthook.Profiles() {
		path, err := agenthook.ConfigPath(profile.Agent)
		require.NoError(err)
		data, err := os.ReadFile(path)
		require.NoError(err)
		assert.Contains(string(data), "--source middleman-agent-activity")
		assert.Contains(string(data), "--agent "+string(profile.Agent))
		assert.Contains(output.String(), "Installed middleman "+profile.DisplayName+" hooks")
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
		"--binary", "/opt/middleman",
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
			"run", "--agent", "claude", "--source", "middleman-agent-activity",
		},
		strings.NewReader(`not json`),
		&stdout,
	)

	require.NoError(t, err)
	assert.Empty(t, stdout.String())
}
