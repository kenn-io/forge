package server

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"go.kenn.io/middleman/internal/server/kataapi"
)

type pullLifecycleRecorder struct {
	stopOnce      sync.Once
	stopCalls     atomic.Int32
	shutdownCalls atomic.Int32
	admissionOpen atomic.Bool
	canceled      chan struct{}
	release       chan struct{}
}

func newPullLifecycleRecorder() *pullLifecycleRecorder {
	recorder := &pullLifecycleRecorder{
		canceled: make(chan struct{}),
		release:  make(chan struct{}),
	}
	recorder.admissionOpen.Store(true)
	return recorder
}

func (r *pullLifecycleRecorder) Stop() {
	r.stopCalls.Add(1)
	r.stopOnce.Do(func() {
		r.admissionOpen.Store(false)
		close(r.canceled)
	})
}

func (r *pullLifecycleRecorder) Shutdown(ctx context.Context) error {
	r.shutdownCalls.Add(1)
	r.Stop()
	select {
	case <-r.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

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

func TestServerShutdownDoesNotAdvancePastActiveWorkspaceConsumers(t *testing.T) {
	require := require.New(t)
	srv, _ := setupTestServer(t)
	releaseConsumer := make(chan struct{})
	releaseRootWork := make(chan struct{})
	consumerReleased := false
	rootWorkReleased := false
	t.Cleanup(func() {
		if !consumerReleased {
			close(releaseConsumer)
		}
		if !rootWorkReleased {
			close(releaseRootWork)
		}
	})
	var workspaceStops atomic.Int32
	var runtimeStops atomic.Int32
	srv.runWorkspaceDependent(func(ctx context.Context) {
		<-ctx.Done()
		<-releaseConsumer
	})
	srv.runBackground(func(ctx context.Context) {
		<-ctx.Done()
		<-releaseRootWork
	})
	srv.workspaceDependencyStop.shutdownWorkspace = func(context.Context) error {
		workspaceStops.Add(1)
		return nil
	}
	srv.workspaceDependencyStop.shutdownDependents = func() {
		runtimeStops.Add(1)
	}

	shortCtx, shortCancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer shortCancel()
	require.ErrorIs(srv.Shutdown(shortCtx), context.DeadlineExceeded)
	require.Zero(workspaceStops.Load(), "Workspace stopped before its consumer drained")
	require.Zero(runtimeStops.Load(), "runtime stopped before its consumer drained")

	close(releaseConsumer)
	consumerReleased = true
	rootCtx, rootCancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer rootCancel()
	require.ErrorIs(srv.Shutdown(rootCtx), context.DeadlineExceeded)
	require.Zero(workspaceStops.Load(), "Workspace stopped before root work drained")
	require.Zero(runtimeStops.Load(), "runtime stopped before root work drained")

	close(releaseRootWork)
	rootWorkReleased = true
	longCtx, longCancel := context.WithTimeout(t.Context(), time.Second)
	defer longCancel()
	require.NoError(srv.Shutdown(longCtx))
	require.Equal(int32(1), workspaceStops.Load())
	require.Equal(int32(1), runtimeStops.Load())
}

func TestWorkspaceDependencyShutdownPreservesOrderAcrossTimeoutRetry(t *testing.T) {
	require := require.New(t)
	releaseWorkspace := make(chan struct{})
	var runtimeStops atomic.Int32

	shutdown := newWorkspaceDependencyShutdown(
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

func TestServerShutdownStopsPullBeforeHTTPDrainAndRetriesDependencyWait(t *testing.T) {
	require := require.New(t)
	srv, _ := setupTestServer(t)
	pull := newPullLifecycleRecorder()
	srv.pullLifecycle = pull

	var dependencyOrder []string
	srv.workspaceDependencyStop.shutdownWorkspace = func(ctx context.Context) error {
		if err := srv.pullLifecycle.Shutdown(ctx); err != nil {
			return err
		}
		dependencyOrder = append(dependencyOrder, "pull", "fleet", "kata", "workspace")
		return nil
	}
	srv.workspaceDependencyStop.shutdownDependents = func() {
		dependencyOrder = append(dependencyOrder, "runtime")
	}

	httpRelease := make(chan struct{})
	httpStarted := make(chan struct{}, 1)
	srv.handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		select {
		case httpStarted <- struct{}{}:
		default:
		}
		<-httpRelease
		w.WriteHeader(http.StatusOK)
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(err)
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()
	requestDone := make(chan struct{})
	go func() {
		resp, requestErr := http.Get("http://" + ln.Addr().String() + "/slow")
		if requestErr == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
		close(requestDone)
	}()

	select {
	case <-httpStarted:
	case <-time.After(time.Second):
		require.FailNow("slow HTTP handler did not start")
	}

	shortCtx, shortCancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer shortCancel()
	require.ErrorIs(srv.Shutdown(shortCtx), context.DeadlineExceeded)
	select {
	case <-pull.canceled:
	default:
		require.FailNow("Pull workers were not canceled before HTTP drain returned")
	}
	require.False(pull.admissionOpen.Load(), "Pull admission remained open after shutdown began")
	require.Zero(pull.shutdownCalls.Load(), "dependency wait advanced before HTTP drained")
	require.Empty(dependencyOrder)

	shutdownDone := make(chan error, 1)
	go func() {
		longCtx, longCancel := context.WithTimeout(t.Context(), 2*time.Second)
		defer longCancel()
		shutdownDone <- srv.Shutdown(longCtx)
	}()
	close(httpRelease)
	<-requestDone
	select {
	case <-shutdownDone:
		require.FailNow("shutdown advanced past an active Pull worker")
	case <-time.After(20 * time.Millisecond):
	}
	close(pull.release)
	require.NoError(<-shutdownDone)
	require.Equal(int32(1), pull.shutdownCalls.Load())
	require.Equal([]string{"pull", "fleet", "kata", "workspace", "runtime"}, dependencyOrder)

	select {
	case err := <-serveErr:
		require.ErrorIs(err, http.ErrServerClosed)
	case <-time.After(time.Second):
		require.FailNow("Serve did not return after Shutdown")
	}
}

// TestServerShutdownStopsKataAfterHTTPDrain verifies that Kata lifecycle
// cleanup starts only after active handlers finish.
func TestServerShutdownStopsKataAfterHTTPDrain(t *testing.T) {
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
	srv.shutdownKata = func(context.Context) error {
		closeCalled <- handlerFinished.Load()
		return nil
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

// TestServerShutdownRetryStopsKataAfterHTTPDrain verifies that a timed-out
// first Shutdown does not stop Kata before a later retry drains handlers.
func TestServerShutdownRetryStopsKataAfterHTTPDrain(t *testing.T) {
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
	srv.shutdownKata = func(context.Context) error {
		closeCalled <- handlerFinished.Load()
		return nil
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

func TestServerShutdownClosesKataSSESubscribersBeforeHTTPDrain(t *testing.T) {
	req := require.New(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/events":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"events":[],"next_after_id":0,"reset_required":false}`)
		case "/api/v1/events/stream":
			w.Header().Set("Content-Type", "text/event-stream")
			_ = http.NewResponseController(w).Flush()
			<-r.Context().Done()
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	writeKataServerCatalog(t, home, `
[[daemon]]
name = "primary"
url = "`+upstream.URL+`"
	`)
	srv, _ := setupTestServer(t)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	req.NoError(err)
	addr := ln.Addr().String()
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()

	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+addr+"/api/v1/kata/tasks/events", nil)
	req.NoError(err)
	request.Header.Set(kataapi.DaemonHeaderName, "primary")
	response, err := http.DefaultClient.Do(request)
	req.NoError(err)
	defer response.Body.Close()
	req.Equal(http.StatusOK, response.StatusCode)

	readDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, response.Body)
		close(readDone)
	}()

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	start := time.Now()
	req.NoError(srv.Shutdown(ctx))
	req.Less(time.Since(start), time.Second, "Shutdown waited on the Kata SSE handler")

	select {
	case <-readDone:
	case <-time.After(time.Second):
		req.FailNow("Kata SSE connection did not close after Shutdown")
	}
	select {
	case err := <-serveErr:
		req.ErrorIs(err, http.ErrServerClosed)
	case <-time.After(time.Second):
		req.FailNow("Serve did not return after Shutdown")
	}
}

func TestServerShutdownRejectsKataSSEAdmittedAfterEventRegistryCloses(t *testing.T) {
	req := require.New(t)
	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	writeKataServerCatalog(t, home, `
[[daemon]]
name = "primary"
url = "http://127.0.0.1:1"
	`)
	srv, _ := setupTestServer(t)
	srv.kataEvents.Close()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	req.NoError(err)
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()

	requestCtx, cancelRequest := context.WithTimeout(t.Context(), time.Second)
	defer cancelRequest()
	request, err := http.NewRequestWithContext(
		requestCtx,
		http.MethodGet,
		"http://"+ln.Addr().String()+"/api/v1/kata/tasks/events",
		nil,
	)
	req.NoError(err)
	request.Header.Set(kataapi.DaemonHeaderName, "primary")
	response, err := http.DefaultClient.Do(request)
	req.NoError(err)
	body, readErr := io.ReadAll(response.Body)
	_ = response.Body.Close()
	req.NoError(readErr)
	req.Equal(http.StatusServiceUnavailable, response.StatusCode, string(body))
	req.Contains(response.Header.Values("Vary"), kataapi.DaemonHeaderName)
	req.NotEqual("text/event-stream", response.Header.Get("Content-Type"))
	req.Contains(string(body), "serviceUnavailable")

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	start := time.Now()
	req.NoError(srv.Shutdown(ctx))
	req.Less(time.Since(start), time.Second, "Shutdown waited on a post-close Kata event stream")

	select {
	case err := <-serveErr:
		req.ErrorIs(err, http.ErrServerClosed)
	case <-time.After(time.Second):
		req.FailNow("Serve did not return after Shutdown")
	}
}
