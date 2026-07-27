package main

import (
	"encoding/json"
	"io"
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
