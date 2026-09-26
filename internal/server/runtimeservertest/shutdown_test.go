package runtimeservertest

import (
	"context"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/server/streamapi"
)

func TestWorkspaceDependencyShutdownPreservesOrderAcrossTimeoutRetry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		require := require.New(t)
		releaseWorkspace := make(chan struct{})
		var runtimeStops atomic.Int32

		shutdown := streamapi.NewWorkspaceDependencyShutdown(
			nil,
			func(ctx context.Context) error {
				select {
				case <-releaseWorkspace:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			},
			func() { runtimeStops.Add(1) },
		)

		shortCtx, shortCancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
		defer shortCancel()
		require.ErrorIs(shutdown.Shutdown(shortCtx), context.DeadlineExceeded)
		require.Zero(runtimeStops.Load(), "runtime stopped before Workspace completed")

		close(releaseWorkspace)
		longCtx, longCancel := context.WithTimeout(t.Context(), time.Second)
		defer longCancel()
		require.NoError(shutdown.Shutdown(longCtx))
		require.Equal(int32(1), runtimeStops.Load())
		require.NoError(shutdown.Shutdown(longCtx))
		require.Equal(int32(1), runtimeStops.Load(), "runtime shutdown must remain idempotent")
	})
}
