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
	payload := `{"session_id":"agent-1","cwd":"/tmp/worktree","hook_event_name":"SessionStart"}`
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
