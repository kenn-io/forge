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
			command, err := agentResumeCommand([]string{tc.agent, "--model", "model-a"}, tc.agent, "conversation")
			require.NoError(t, err)
			assert.Equal(t, append([]string{tc.agent, "--model", "model-a"}, tc.suffix...), command)
		})
	}
	_, err := agentResumeCommand([]string{"other"}, "other", "conversation")
	require.Error(t, err)
}

func TestAgentResumeRejectsAmbiguousConfiguredArguments(t *testing.T) {
	for _, tc := range []struct {
		agent string
		args  []string
	}{
		{"codex", []string{"--", "original prompt"}},
		{"codex", []string{"exec", "original prompt"}},
		{"codex", []string{"resume", "old-session"}},
		{"codex", []string{"--model"}},
		{"claude", []string{"original prompt"}},
		{"claude", []string{"--resume=old-session"}},
		{"claude", []string{"--continue"}},
		{"pi", []string{"--session-id", "other-session"}},
		{"pi", []string{"--", "original prompt"}},
	} {
		t.Run(tc.agent+"/"+tc.args[0], func(t *testing.T) {
			_, err := agentResumeCommand(append([]string{tc.agent}, tc.args...), tc.agent, "saved-session")
			require.Error(t, err)
		})
	}
}
