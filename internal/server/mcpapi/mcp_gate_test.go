package mcpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRequestGateDrainsThenCancelsInFlightRequests(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		assert := assert.New(t)
		require := require.New(t)
		base, cancelBase := context.WithCancel(t.Context())
		defer cancelBase()
		gate := NewRequestGate(base)
		started := make(chan struct{})
		finished := make(chan error, 1)
		handler := gate.Wrap(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			close(started)
			<-r.Context().Done()
			finished <- r.Context().Err()
		}))
		go handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/mcp", nil))
		<-started

		gate.Stop()
		rejected := httptest.NewRecorder()
		handler.ServeHTTP(rejected, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/mcp", nil))
		assert.Equal(http.StatusServiceUnavailable, rejected.Code)

		drainCtx, cancelDrain := context.WithTimeout(t.Context(), 20*time.Millisecond)
		defer cancelDrain()
		require.ErrorIs(gate.Wait(drainCtx), context.DeadlineExceeded)

		cancelBase()
		select {
		case err := <-finished:
			require.ErrorIs(err, context.Canceled)
		case <-time.After(5 * time.Second):
			require.Fail("in-flight request was not canceled")
		}
		require.NoError(gate.Wait(t.Context()))
	})
}
