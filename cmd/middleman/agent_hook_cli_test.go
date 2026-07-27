package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/agentactivity"
)

func TestAgentHookRunReceivesLifecyclePayload(t *testing.T) {
	require := require.New(t)
	stateDir := t.TempDir()
	workspace := t.TempDir()
	t.Setenv(agentactivity.RuntimeSessionKeyEnv, "runtime-1")
	payload, err := json.Marshal(map[string]string{
		"session_id": "agent-1", "cwd": workspace,
		"hook_event_name": "UserPromptSubmit",
	})
	require.NoError(err)

	require.NoError(runAgentHookCLI([]string{
		"run", "--state-dir", stateDir, "--source", "middleman-agent-activity",
	}, strings.NewReader(string(payload)), io.Discard))

	snapshot, ok := agentactivity.NewStore(stateDir).SnapshotForWorkspace(
		workspace, []string{"runtime-1"},
	)
	require.True(ok)
	assert.Equal(t, agentactivity.StateWorking, snapshot.State)
}

func TestAgentHookInstallRejectsRelativeDataDirectory(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	require.NoError(os.WriteFile(configPath, []byte("data_dir = \"relative-data\"\n"), 0o600))
	configDir := filepath.Join(dir, "codex")
	t.Setenv("CODEX_HOME", configDir)

	err := runAgentHookInstall([]string{
		"--config", configPath,
		"--agent", "codex",
		"--binary", "/opt/middleman",
	}, io.Discard)

	require.Error(err)
	assert.Contains(err.Error(), "absolute data_dir")
	_, statErr := os.Stat(filepath.Join(configDir, "hooks.json"))
	assert.ErrorIs(statErr, os.ErrNotExist)
}
