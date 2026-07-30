package fleetapi

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/internal/config"
)

func TestHandlerConfigSnapshotIsImmutable(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)

	initial := ConfigSnapshot{
		Fleet: config.Fleet{
			Enabled: true,
			Key:     "local",
			Peers: []config.FleetPeer{{
				Key:     "peer-a",
				BaseURL: "http://peer-a.test",
			}},
		},
		TmuxCommand: []string{"tmux"},
	}
	h := New(Deps{Config: initial})
	initial.Fleet.Peers[0].Key = "mutated"
	initial.TmuxCommand[0] = "mutated"

	snapshot := h.configSnapshot()
	assert.Equal("peer-a", snapshot.Fleet.Peers[0].Key)
	assert.Equal("tmux", snapshot.TmuxCommand[0])

	h.ApplyConfig(ConfigSnapshot{
		Fleet:       config.Fleet{Key: "next"},
		TmuxCommand: []string{"custom-tmux"},
	})
	snapshot = h.configSnapshot()
	assert.Equal("next", snapshot.Fleet.Key)
	assert.Equal([]string{"custom-tmux"}, snapshot.TmuxCommand)
}

func TestHandlerShutdownIsIdempotentAndContextBounded(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	require := require.New(t)

	started := make(chan struct{})
	release := make(chan struct{})
	h := New(Deps{})
	require.True(h.runBackground(func(ctx context.Context) {
		close(started)
		<-release
	}))
	<-started

	deadlineCtx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	require.ErrorIs(h.Shutdown(deadlineCtx), context.DeadlineExceeded)

	close(release)
	assert.NoError(h.Shutdown(context.Background()))
	assert.NoError(h.Shutdown(context.Background()))
}
