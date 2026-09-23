package server

import (
	"context"
	"io"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/internal/federationauth"
	"go.kenn.io/forge/internal/providerplane"
)

type blockingHubEventTransport struct {
	started  chan struct{}
	canceled chan struct{}
	release  chan struct{}
	once     sync.Once
}

func (c *blockingHubEventTransport) Do(
	ctx context.Context, _ federationauth.Scope, _ *http.Request,
) (*http.Response, error) {
	c.once.Do(func() { close(c.started) })
	<-ctx.Done()
	close(c.canceled)
	<-c.release
	return nil, ctx.Err()
}

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
	srv, _, _ := setupTestServer(t)

	release := make(chan struct{})
	var finished atomic.Bool
	srv.streamapi.RunBackground(func(_ context.Context) {
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
	srv, _, _ := setupTestServer(t)

	stuck := make(chan struct{})
	srv.streamapi.RunBackground(func(_ context.Context) {
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
	srv, _, _ := setupTestServer(t)

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	require.NoError(t, srv.Shutdown(ctx))

	var ran atomic.Bool
	started := srv.streamapi.RunBackground(func(_ context.Context) {
		ran.Store(true)
	})

	require.False(t, started, "runBackground must report dropped work after Shutdown")
	require.False(t, ran.Load(), "runBackground must not spawn work after Shutdown")
}

// TestServerShutdownRaceNoPanic exercises runBackground concurrently
// with Shutdown to catch WaitGroup Add/Wait races under -race.
func TestServerShutdownRaceNoPanic(t *testing.T) {
	srv, _, _ := setupTestServer(t)

	done := make(chan struct{})
	go func() {
		for range 200 {
			srv.streamapi.RunBackground(func(_ context.Context) {})
		}
		close(done)
	}()

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	require.NoError(t, srv.Shutdown(ctx))
	<-done
}

// TestServerShutdownRetryWithLongerCtx verifies that a second
// Shutdown call with a longer deadline can still drain background
// work that the first call timed out waiting for.
func TestServerShutdownRetryWithLongerCtx(t *testing.T) {
	srv, _, _ := setupTestServer(t)

	release := make(chan struct{})
	srv.streamapi.RunBackground(func(_ context.Context) {
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
	srv, _, _ := setupTestServer(t)
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
	srv.streamapi.RunWorkspaceDependent(func(ctx context.Context) {
		<-ctx.Done()
		<-releaseConsumer
	})
	srv.streamapi.RunBackground(func(ctx context.Context) {
		<-ctx.Done()
		<-releaseRootWork
	})
	srv.workspaceDependencyStop.ShutdownWorkspace = func(context.Context) error {
		workspaceStops.Add(1)
		return nil
	}
	srv.workspaceDependencyStop.ShutdownDependents = func() {
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

func TestServerShutdownWaitsForHubEventClient(t *testing.T) {
	require := require.New(t)
	srv, _, _ := setupTestServer(t)
	transport := &blockingHubEventTransport{
		started: make(chan struct{}), canceled: make(chan struct{}), release: make(chan struct{}),
	}
	events, err := providerplane.NewEventClient(providerplane.EventClientOptions{
		Client: transport,
	})
	require.NoError(err)
	srv.streamapi.RunWorkspaceDependent(events.Run)
	select {
	case <-transport.started:
	case <-time.After(time.Second):
		require.FailNow("hub event client did not start")
	}

	shutdownDone := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		defer cancel()
		shutdownDone <- srv.Shutdown(ctx)
	}()
	select {
	case <-transport.canceled:
	case <-time.After(time.Second):
		require.FailNow("hub event client was not canceled")
	}
	select {
	case <-shutdownDone:
		require.FailNow("shutdown returned before hub event client exited")
	case <-time.After(25 * time.Millisecond):
	}

	close(transport.release)
	require.NoError(<-shutdownDone)
}

// TestServerShutdownRetryWaitsForHTTPHandler verifies that when the
// first Shutdown call times out while an HTTP handler is in flight,
// a later call with a longer deadline still invokes
// http.Server.Shutdown and blocks until the handler drains.
func TestServerShutdownRetryWaitsForHTTPHandler(t *testing.T) {
	require := require.New(t)
	srv, _, _ := setupTestServer(t)

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

	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(err)
	addr := ln.Addr().String()

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- srv.Serve(ln)
	}()

	reqDone := make(chan struct{})
	go func() {
		respReq, reqErr := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+addr+"/slow", nil)
		if reqErr != nil {
			close(reqDone)
			return
		}
		resp, reqErr := (&http.Client{Timeout: 5 * time.Second}).Do(respReq)
		if reqErr == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		} else if resp != nil {
			_ = resp.Body.Close()
		}
		close(reqDone)
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		require.FailNow("slow handler never started")
	}

	shortCtx, shortCancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer shortCancel()
	err = srv.Shutdown(shortCtx)
	require.ErrorIs(err, context.DeadlineExceeded)

	longErrCh := make(chan error, 1)
	go func() {
		longCtx, longCancel := context.WithTimeout(t.Context(), 2*time.Second)
		defer longCancel()
		longErrCh <- srv.Shutdown(longCtx)
	}()

	select {
	case <-longErrCh:
		require.FailNow("second Shutdown returned before HTTP handler drained")
	case <-time.After(100 * time.Millisecond):
	}

	close(release)
	<-reqDone
	require.NoError(<-longErrCh)

	select {
	case e := <-serveErr:
		require.ErrorIs(e, http.ErrServerClosed)
	case <-time.After(time.Second):
		require.FailNow("Serve did not return after Shutdown")
	}
}

func TestServerShutdownStopsPullBeforeHTTPDrainAndRetriesDependencyWait(t *testing.T) {
	require := require.New(t)
	srv, _, _ := setupTestServer(t)
	pull := newPullLifecycleRecorder()
	srv.pullLifecycle = pull

	var dependencyOrder []string
	srv.workspaceDependencyStop.ShutdownWorkspace = func(ctx context.Context) error {
		if err := srv.pullLifecycle.Shutdown(ctx); err != nil {
			return err
		}
		dependencyOrder = append(dependencyOrder, "pull", "fleet", "workspace")
		return nil
	}
	srv.workspaceDependencyStop.ShutdownDependents = func() {
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

	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(err)
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()
	requestDone := make(chan struct{})
	go func() {
		slowReq, requestErr := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+ln.Addr().String()+"/slow", nil)
		if requestErr != nil {
			close(requestDone)
			return
		}
		resp, requestErr := (&http.Client{Timeout: 5 * time.Second}).Do(slowReq)
		if requestErr == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		} else if resp != nil {
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
	require.Equal([]string{"pull", "fleet", "workspace", "runtime"}, dependencyOrder)

	select {
	case err := <-serveErr:
		require.ErrorIs(err, http.ErrServerClosed)
	case <-time.After(time.Second):
		require.FailNow("Serve did not return after Shutdown")
	}
}
