package localruntime

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAgentResumeCommand(t *testing.T) {
	for _, tc := range []struct {
		agent  string
		suffix []string
	}{
		{"codex", []string{"resume", "conversation"}},
		{"claude", []string{"--resume", "conversation"}},
		{"pi", []string{"--session", "conversation"}},
	} {
		t.Run(tc.agent, func(t *testing.T) {
			command, err := agentResumeCommand([]string{"custom-worker", "--model", "model-a"}, tc.agent, "conversation")
			require.NoError(t, err)
			assert.Equal(t, append([]string{"custom-worker", "--model", "model-a"}, tc.suffix...), command)
		})
	}
	_, err := agentResumeCommand([]string{"other"}, "other", "conversation")
	require.Error(t, err)
	for _, command := range [][]string{nil, {""}} {
		_, err = agentResumeCommand(command, "codex", "conversation")
		require.Error(t, err)
	}
}

func TestAgentResumableRequiresTerminalAgentTarget(t *testing.T) {
	manager := NewManager(Options{Targets: []LaunchTarget{
		{Key: "worker", Kind: LaunchTargetAgent, Available: true, Command: []string{"claude"}},
		{Key: "chat", Kind: LaunchTargetACP, Available: true, Command: []string{"claude"}},
	}})
	assert.True(t, manager.AgentResumable("worker", "claude", "conversation"))
	assert.False(t, manager.AgentResumable("chat", "claude", "conversation"), "a target reloaded to ACP can't resume a terminal agent")
}
