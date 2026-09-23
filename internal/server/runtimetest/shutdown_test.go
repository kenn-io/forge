package runtimetest

import (
	"context"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestServerShutdownIsIdempotent verifies that Shutdown can be called
// more than once without panicking on the internal WaitGroup.
func TestServerShutdownIsIdempotent(t *testing.T) {
	srv, _ := setupTestServer(t)

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	require.NoError(t, srv.Shutdown(ctx))
	require.NoError(t, srv.Shutdown(ctx))
}

// TestServerShutdownStopsHTTPListener verifies that Shutdown closes
// the HTTP listener passed to Serve and that subsequent requests
// fail fast.
func TestServerShutdownStopsHTTPListener(t *testing.T) {
	require := require.New(t)
	srv, _ := setupTestServer(t)

	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(err)
	addr := ln.Addr().String()

	listenErrCh := make(chan error, 1)
	go func() {
		listenErrCh <- srv.Serve(ln)
	}()

	require.Eventually(func() bool {
		respReq, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+addr+"/api/v1/version", nil)
		require.NoError(err)
		resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(respReq)
		if err != nil {
			return false
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, 2*time.Second, 10*time.Millisecond, "server never accepted requests")

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	require.NoError(srv.Shutdown(ctx))

	select {
	case listenErr := <-listenErrCh:
		require.ErrorIs(listenErr, http.ErrServerClosed)
	case <-time.After(time.Second):
		require.FailNow("Serve did not return after Shutdown")
	}

	closedReq, closedErr := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+addr+"/api/v1/version", nil)
	require.NoError(closedErr)
	closedResp, err := (&http.Client{Timeout: 5 * time.Second}).Do(closedReq)
	if closedResp != nil {
		_ = closedResp.Body.Close()
	}
	require.Error(err)
}

func TestServerShutdownClosesSSESubscribers(t *testing.T) {
	require := require.New(t)
	srv, _ := setupTestServer(t)

	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(err)
	addr := ln.Addr().String()

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()

	// Open an SSE connection and pull the first line so we know
	// the handler is actively streaming.
	respReq, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+addr+"/api/v1/events", nil)
	require.NoError(err)
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(respReq)
	require.NoError(err)
	t.Cleanup(func() {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
	})
	defer resp.Body.Close()
	require.Equal(http.StatusOK, resp.StatusCode)

	// Read in a goroutine so we can observe the connection close.
	readDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		close(readDone)
	}()

	// Shutdown must complete well within ctx — if the hub is not
	// closed, http.Server.Shutdown would hang on the SSE handler
	// until the 2 s deadline.
	start := time.Now()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	require.NoError(srv.Shutdown(ctx))
	require.Less(time.Since(start), time.Second,
		"Shutdown took too long; SSE hub likely not closed")

	select {
	case <-readDone:
	case <-time.After(time.Second):
		require.FailNow("SSE connection did not close after Shutdown")
	}

	select {
	case e := <-serveErr:
		require.ErrorIs(e, http.ErrServerClosed)
	case <-time.After(time.Second):
		require.FailNow("Serve did not return after Shutdown")
	}
}
