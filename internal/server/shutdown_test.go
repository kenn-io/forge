package server

import (
	"context"
	"io"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestServerShutdownWaitsForBackgroundTask verifies that Shutdown
// blocks until an in-flight runBackground task returns.
func TestServerShutdownWaitsForBackgroundTask(t *testing.T) {
	srv, _ := setupTestServer(t)

	release := make(chan struct{})
	var finished atomic.Bool
	srv.runBackground(func(_ context.Context) {
		<-release
		finished.Store(true)
	})

	shutdownDone := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		defer cancel()
		shutdownDone <- srv.Shutdown(ctx)
	}()

	select {
	case <-shutdownDone:
		require.FailNow(t, "Shutdown returned before background task finished")
	case <-time.After(50 * time.Millisecond):
	}

	close(release)
	require.NoError(t, <-shutdownDone)
	require.True(t, finished.Load(), "background task should have run to completion")
}

// TestServerShutdownTimesOut verifies that Shutdown honours the
// caller's ctx when a background task ignores its own cancellation.
func TestServerShutdownTimesOut(t *testing.T) {
	srv, _ := setupTestServer(t)

	stuck := make(chan struct{})
	srv.runBackground(func(_ context.Context) {
		<-stuck
	})
	defer close(stuck)

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	err := srv.Shutdown(ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

// TestServerShutdownPreventsNewBackgroundTasks verifies that after
// Shutdown starts, runBackground drops new submissions so bg.Add
// cannot race with bg.Wait.
func TestServerShutdownPreventsNewBackgroundTasks(t *testing.T) {
	srv, _ := setupTestServer(t)

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	require.NoError(t, srv.Shutdown(ctx))

	var ran atomic.Bool
	started := srv.runBackground(func(_ context.Context) {
		ran.Store(true)
	})

	require.False(t, started, "runBackground must report dropped work after Shutdown")
	require.False(t, ran.Load(), "runBackground must not spawn work after Shutdown")
}

// TestServerShutdownIsIdempotent verifies that Shutdown can be called
// more than once without panicking on the internal WaitGroup.
func TestServerShutdownIsIdempotent(t *testing.T) {
	srv, _ := setupTestServer(t)

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	require.NoError(t, srv.Shutdown(ctx))
	require.NoError(t, srv.Shutdown(ctx))
}

// TestServerShutdownRaceNoPanic exercises runBackground concurrently
// with Shutdown to catch WaitGroup Add/Wait races under -race.
func TestServerShutdownRaceNoPanic(t *testing.T) {
	srv, _ := setupTestServer(t)

	done := make(chan struct{})
	go func() {
		for range 200 {
			srv.runBackground(func(_ context.Context) {})
		}
		close(done)
	}()

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	require.NoError(t, srv.Shutdown(ctx))
	<-done
}

// TestServerShutdownStopsHTTPListener verifies that Shutdown closes
// the HTTP listener passed to Serve and that subsequent requests
// fail fast.
func TestServerShutdownStopsHTTPListener(t *testing.T) {
	req := require.New(t)
	srv, _ := setupTestServer(t)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	req.NoError(err)
	addr := ln.Addr().String()

	listenErrCh := make(chan error, 1)
	go func() {
		listenErrCh <- srv.Serve(ln)
	}()

	req.Eventually(func() bool {
		resp, err := http.Get("http://" + addr + "/api/v1/version")
		if err != nil {
			return false
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, 2*time.Second, 10*time.Millisecond, "server never accepted requests")

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	req.NoError(srv.Shutdown(ctx))

	select {
	case listenErr := <-listenErrCh:
		req.ErrorIs(listenErr, http.ErrServerClosed)
	case <-time.After(time.Second):
		req.FailNow("Serve did not return after Shutdown")
	}

	_, err = http.Get("http://" + addr + "/api/v1/version")
	req.Error(err)
}

// TestServerShutdownRetryWithLongerCtx verifies that a second
// Shutdown call with a longer deadline can still drain background
// work that the first call timed out waiting for.
func TestServerShutdownRetryWithLongerCtx(t *testing.T) {
	srv, _ := setupTestServer(t)

	release := make(chan struct{})
	srv.runBackground(func(_ context.Context) {
		<-release
	})

	shortCtx, shortCancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer shortCancel()
	err := srv.Shutdown(shortCtx)
	require.ErrorIs(t, err, context.DeadlineExceeded)

	close(release)

	longCtx, longCancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer longCancel()
	require.NoError(t, srv.Shutdown(longCtx))
}

// TestServerShutdownRetryWaitsForHTTPHandler verifies that when the
// first Shutdown call times out while an HTTP handler is in flight,
// a later call with a longer deadline still invokes
// http.Server.Shutdown and blocks until the handler drains.
func TestServerShutdownRetryWaitsForHTTPHandler(t *testing.T) {
	req := require.New(t)
	srv, _ := setupTestServer(t)

	release := make(chan struct{})
	started := make(chan struct{}, 1)
	srv.handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-release
		w.WriteHeader(http.StatusOK)
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	req.NoError(err)
	addr := ln.Addr().String()

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- srv.Serve(ln)
	}()

	reqDone := make(chan struct{})
	go func() {
		resp, err := http.Get("http://" + addr + "/slow")
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
		close(reqDone)
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		req.FailNow("slow handler never started")
	}

	shortCtx, shortCancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer shortCancel()
	err = srv.Shutdown(shortCtx)
	req.ErrorIs(err, context.DeadlineExceeded)

	longErrCh := make(chan error, 1)
	go func() {
		longCtx, longCancel := context.WithTimeout(t.Context(), 2*time.Second)
		defer longCancel()
		longErrCh <- srv.Shutdown(longCtx)
	}()

	select {
	case <-longErrCh:
		req.FailNow("second Shutdown returned before HTTP handler drained")
	case <-time.After(100 * time.Millisecond):
	}

	close(release)
	<-reqDone
	req.NoError(<-longErrCh)

	select {
	case e := <-serveErr:
		req.ErrorIs(e, http.ErrServerClosed)
	case <-time.After(time.Second):
		req.FailNow("Serve did not return after Shutdown")
	}
}

// TestServerShutdownClosesKataProxyIdleConnectionsAfterHTTPDrain verifies
// that upstream proxy transports are closed only after active handlers finish.
func TestServerShutdownClosesKataProxyIdleConnectionsAfterHTTPDrain(t *testing.T) {
	req := require.New(t)
	srv, _ := setupTestServer(t)

	release := make(chan struct{})
	started := make(chan struct{}, 1)
	var handlerFinished atomic.Bool
	srv.handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-release
		handlerFinished.Store(true)
		w.WriteHeader(http.StatusOK)
	})

	closeCalled := make(chan bool, 1)
	srv.kataProxyCache = map[kataProxyCacheKey]kataProxyCacheEntry{
		{id: "primary", url: "http://127.0.0.1:1"}: {
			closeIdle: func() {
				closeCalled <- handlerFinished.Load()
			},
		},
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	req.NoError(err)
	addr := ln.Addr().String()

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- srv.Serve(ln)
	}()

	reqDone := make(chan struct{})
	go func() {
		resp, err := http.Get("http://" + addr + "/slow")
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
		close(reqDone)
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		req.FailNow("slow handler never started")
	}

	shutdownDone := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		defer cancel()
		shutdownDone <- srv.Shutdown(ctx)
	}()

	closedBeforeRelease := false
	closedBeforeReleaseAfterFinish := false
	select {
	case closedAfterFinish := <-closeCalled:
		closedBeforeRelease = true
		closedBeforeReleaseAfterFinish = closedAfterFinish
	case <-time.After(100 * time.Millisecond):
	}

	close(release)
	<-reqDone
	req.NoError(<-shutdownDone)

	if closedBeforeRelease {
		req.True(closedBeforeReleaseAfterFinish, "proxy idle connections closed before HTTP handlers drained")
		return
	}

	select {
	case closedAfterFinish := <-closeCalled:
		req.True(closedAfterFinish, "proxy idle connections should close after HTTP handlers drain")
	case <-time.After(time.Second):
		req.FailNow("proxy idle connections were not closed")
	}

	select {
	case e := <-serveErr:
		req.ErrorIs(e, http.ErrServerClosed)
	case <-time.After(time.Second):
		req.FailNow("Serve did not return after Shutdown")
	}
}

// TestServerShutdownRetryClosesKataProxyIdleConnectionsAfterHTTPDrain verifies
// that a timed-out first Shutdown does not spend the one cleanup pass before a
// later retry drains active handlers.
func TestServerShutdownRetryClosesKataProxyIdleConnectionsAfterHTTPDrain(t *testing.T) {
	req := require.New(t)
	srv, _ := setupTestServer(t)

	release := make(chan struct{})
	started := make(chan struct{}, 1)
	var handlerFinished atomic.Bool
	srv.handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-release
		handlerFinished.Store(true)
		w.WriteHeader(http.StatusOK)
	})

	closeCalled := make(chan bool, 2)
	srv.kataProxyCache = map[kataProxyCacheKey]kataProxyCacheEntry{
		{id: "primary", url: "http://127.0.0.1:1"}: {
			closeIdle: func() {
				closeCalled <- handlerFinished.Load()
			},
		},
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	req.NoError(err)
	addr := ln.Addr().String()

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- srv.Serve(ln)
	}()

	reqDone := make(chan struct{})
	go func() {
		resp, err := http.Get("http://" + addr + "/slow")
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
		close(reqDone)
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		req.FailNow("slow handler never started")
	}

	shortCtx, shortCancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer shortCancel()
	err = srv.Shutdown(shortCtx)
	req.ErrorIs(err, context.DeadlineExceeded)

	select {
	case closedAfterFinish := <-closeCalled:
		req.True(closedAfterFinish, "proxy idle connections closed before HTTP handlers drained")
		req.FailNow("proxy idle connections should not close after a timed-out drain")
	case <-time.After(100 * time.Millisecond):
	}

	longErrCh := make(chan error, 1)
	go func() {
		longCtx, longCancel := context.WithTimeout(t.Context(), 2*time.Second)
		defer longCancel()
		longErrCh <- srv.Shutdown(longCtx)
	}()

	select {
	case <-longErrCh:
		req.FailNow("second Shutdown returned before HTTP handler drained")
	case <-time.After(100 * time.Millisecond):
	}

	close(release)
	<-reqDone
	req.NoError(<-longErrCh)

	select {
	case closedAfterFinish := <-closeCalled:
		req.True(closedAfterFinish, "proxy idle connections should close after retry drains")
	case <-time.After(time.Second):
		req.FailNow("proxy idle connections were not closed after successful retry")
	}

	select {
	case e := <-serveErr:
		req.ErrorIs(e, http.ErrServerClosed)
	case <-time.After(time.Second):
		req.FailNow("Serve did not return after Shutdown")
	}
}

// TestServerShutdownClosesSSESubscribers verifies that Shutdown
// closes the EventHub so `handleSSE` handlers exit on their
// <-done arm. Without this, http.Server.Shutdown would hang
// waiting on the never-returning SSE handler until ctx timeout.
func TestServerShutdownClosesSSESubscribers(t *testing.T) {
	req := require.New(t)
	srv, _ := setupTestServer(t)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	req.NoError(err)
	addr := ln.Addr().String()

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()

	// Open an SSE connection and pull the first line so we know
	// the handler is actively streaming.
	resp, err := http.Get("http://" + addr + "/api/v1/events")
	req.NoError(err)
	defer resp.Body.Close()
	req.Equal(http.StatusOK, resp.StatusCode)

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
	req.NoError(srv.Shutdown(ctx))
	req.Less(time.Since(start), time.Second,
		"Shutdown took too long; SSE hub likely not closed")

	select {
	case <-readDone:
	case <-time.After(time.Second):
		req.FailNow("SSE connection did not close after Shutdown")
	}

	select {
	case e := <-serveErr:
		req.ErrorIs(e, http.ErrServerClosed)
	case <-time.After(time.Second):
		req.FailNow("Serve did not return after Shutdown")
	}
}
