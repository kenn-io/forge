package workspaceapi

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/projects"
)

func TestConfigSnapshotIsImmutableAndApplyConfigPublishesCommittedState(t *testing.T) {
	assert := assert.New(t)
	enabled := true
	initial := ConfigSnapshot{
		KnownPlatformHosts:       []projects.KnownPlatformHost{{Platform: "github", Host: "github.com"}},
		Agents:                   []config.Agent{{Key: "codex", Command: []string{"codex"}, Enabled: &enabled}},
		TmuxCommand:              []string{"tmux", "-L", "initial"},
		RoborevInitManagedClones: true,
	}
	h := New(Deps{Config: initial})

	initial.KnownPlatformHosts[0].Host = "mutated.example"
	initial.Agents[0].Command[0] = "mutated"
	*initial.Agents[0].Enabled = false
	initial.TmuxCommand[0] = "mutated"

	assert.Equal("github.com", h.knownProjectPlatformHosts()[0].Host)
	assert.Equal("codex", h.configSnapshot().Agents[0].Command[0])
	assert.True(*h.configSnapshot().Agents[0].Enabled)
	assert.Equal([]string{"tmux", "-L", "initial"}, h.tmuxCommand())
	assert.True(h.configSnapshot().RoborevInitManagedClones)

	h.ApplyConfig(ConfigSnapshot{
		KnownPlatformHosts:       []projects.KnownPlatformHost{{Platform: "gitlab", Host: "gitlab.example"}},
		Agents:                   []config.Agent{{Key: "claude", Command: []string{"claude"}}},
		TmuxCommand:              []string{"tmux", "-L", "reloaded"},
		RoborevInitManagedClones: false,
	})

	assert.Equal("gitlab.example", h.knownProjectPlatformHosts()[0].Host)
	assert.Equal("claude", h.configSnapshot().Agents[0].Command[0])
	assert.Equal([]string{"tmux", "-L", "reloaded"}, h.tmuxCommand())
	assert.False(h.configSnapshot().RoborevInitManagedClones)
}

func TestLifecycleShutdownHonorsContextAndCanBeWaitedAgain(t *testing.T) {
	require := require.New(t)
	h := New(Deps{})
	h.Start(context.Background(), true)
	h.Start(context.Background(), true)

	started := make(chan struct{})
	release := make(chan struct{})
	require.True(h.runBackground(func(context.Context) {
		close(started)
		<-release
	}))
	<-started

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(h.Shutdown(cancelled), context.Canceled)

	close(release)
	require.NoError(h.Shutdown(context.Background()))
	require.False(h.runBackground(func(context.Context) {}))
}
