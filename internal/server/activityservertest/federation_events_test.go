package activityservertest

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/federationauth"
	"go.kenn.io/forge/internal/server"
	"go.kenn.io/forge/internal/server/syncevents"
	"go.kenn.io/forge/internal/testutil/dbtest"
	serverfake "go.kenn.io/forge/internal/testutil/serverfake"
)

func TestHubEventLifecyclePausesUntilFleetIsEnabled(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		assert := assert.New(t)
		require := require.New(t)
		started := make(chan struct{}, 2)
		stopped := make(chan struct{}, 2)
		lifecycle := syncevents.NewHubEventLifecycle(true, func(ctx context.Context) {
			started <- struct{}{}
			<-ctx.Done()
			stopped <- struct{}{}
		})
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan struct{})
		go func() {
			lifecycle.Run(ctx)
			close(done)
		}()

		synctest.Wait()
		require.Len(started, 1)
		lifecycle.SetEnabled(false)
		synctest.Wait()
		require.Len(stopped, 1)
		synctest.Wait()
		assert.Len(started, 1)

		lifecycle.SetEnabled(true)
		synctest.Wait()
		require.Len(started, 2)
		cancel()
		synctest.Wait()
		require.Len(stopped, 2)
		synctest.Wait()
		<-done
	})
}

func TestHubEventLifecycleCanStopAfterCleanReturn(t *testing.T) {
	runs := 0
	lifecycle := syncevents.NewHubEventLifecycleStoppingOnCleanReturn(true, func(context.Context) {
		runs++
	})

	lifecycle.Run(t.Context())

	assert.Equal(t, 1, runs)
}

func TestDisabledFederationSpokeSeedsDisconnectedStateForFreshSubscriber(t *testing.T) {
	const hubID = "66666666666666666666666666666666"
	require := require.New(t)
	credentials, err := federationauth.Open(t.TempDir() + "/credentials.json")
	require.NoError(err)
	require.NoError(credentials.StoreOutbound(
		hubID, "disabled-spoke-secret",
		federationauth.SpokeToHubScopes(),
	))
	spoke := server.New(dbtest.Open(t), nil, nil, "/", &config.Config{Fleet: config.Fleet{
		Enabled: false, Role: config.FleetRoleSpoke,
		Hub: &config.FleetHub{
			NodeID: hubID, BaseURL: "https://hub.example",
		},
	}}, server.ServerOptions{
		FederationSpokeID: serverfake.FederationEventTestNodeID, FederationSpokeActive: true,
		FederationCredentials: credentials, DisableWorkspaceBackgroundMonitors: true,
	})
	t.Cleanup(func() { serverfake.GracefulShutdown(t, spoke) })

	events, _ := spoke.Hub().Subscribe(t.Context(), true)
	select {
	case record := <-events:
		require.Equal("hub_connection_changed", record.Event.Type)
		state, ok := record.Event.Data.(syncevents.HubConnectionState)
		require.True(ok)
		assert.False(t, state.Connected)
	case <-time.After(time.Second):
		require.FailNow("fresh subscriber did not receive hub availability")
	}
}
