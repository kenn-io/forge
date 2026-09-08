package localruntime

import (
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
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
}

func TestAgentResumePreservesCodexFullAuto(t *testing.T) {
	command, err := agentResumeCommand([]string{"codex", "--full-auto", "--search"}, "codex", "saved-session")
	require.NoError(t, err)
	assert.Equal(t, []string{"codex", "--full-auto", "--search", "resume", "saved-session"}, command)
}

func TestAgentResumeRequiresConfiguredCommand(t *testing.T) {
	for _, command := range [][]string{nil, {""}} {
		_, err := agentResumeCommand(command, "codex", "saved-session")
		require.Error(t, err)
	}
}
