package providerplane

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/internal/federationauth"
)

func TestProviderEventFilterOwnsOnlyHubProviderEvents(t *testing.T) {
	t.Parallel()

	for _, eventType := range []string{
		"data_changed", "sync_status", "pr_detail_refreshed",
		"pr_ci_refresh_queued", "pr_ci_refreshed", "deferred_merge_completed",
	} {
		assert.True(t, IsHubProviderEvent(eventType), eventType)
	}
	for _, eventType := range []string{
		"workspace_created", "workspace_status", "workspace_diff_changed",
		"runtime_status", "config.changed", "docs.changed", "kata.changed",
		"hub_connection_changed", "reconnect.stale",
	} {
		assert.False(t, IsHubProviderEvent(eventType), eventType)
	}
}

func TestEventClientReconnectBackoffResetsAfterSuccessfulConnection(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	t.Parallel()

	var calls atomic.Int64
	client := stubClient(func(
		_ context.Context, scope federationauth.Scope, request *http.Request,
	) (*http.Response, error) {
		assert.Equal(federationauth.ScopeEventsRead, scope)
		assert.Equal(hubEventsPath, request.URL.Path)
		call := calls.Add(1)
		if call <= 3 {
			return eventResponse(http.StatusServiceUnavailable, "application/problem+json", `{}`), nil
		}
		return eventResponse(
			http.StatusOK, "text/event-stream",
			": "+FederationReplayCompleteComment+"\n\n"+
				"id: 40\nevent: data_changed\ndata: {}\n\n",
		), nil
	})
	ctx, cancel := context.WithCancel(t.Context())
	var delays []time.Duration
	events := make(chan Event, 1)
	eventClient, err := NewEventClient(EventClientOptions{
		Client: client,
		OnEvent: func(_ context.Context, event Event) error {
			events <- event
			return nil
		},
		RetryDelay: func(failureCount int) time.Duration {
			delay := time.Second << (failureCount - 1)
			if delay > maxEventRetryDelay {
				return maxEventRetryDelay
			}
			return delay
		},
		Wait: func(_ context.Context, delay time.Duration) error {
			delays = append(delays, delay)
			if len(delays) == 4 {
				cancel()
				return context.Canceled
			}
			return nil
		},
	})
	require.NoError(err)

	eventClient.Run(ctx)

	assert.Equal([]time.Duration{
		time.Second, 2 * time.Second, 4 * time.Second, time.Second,
	}, delays)
	select {
	case event := <-events:
		assert.Equal(uint64(40), event.ID)
		assert.Equal("data_changed", event.Type)
	default:
		require.Fail("expected a decoded event")
	}
}

func TestEventClientRecoversFromPoisonFrameWithoutPublishingIt(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	t.Parallel()

	body := strings.Join([]string{
		": " + FederationReplayCompleteComment, "",
		"id: 41", "event: pr_detail_refreshed", "data: {", "",
		"id: 42", "event: data_changed", "data: {}", "", "",
	}, "\n")
	client := stubClient(func(
		_ context.Context, _ federationauth.Scope, _ *http.Request,
	) (*http.Response, error) {
		return eventResponse(http.StatusOK, "text/event-stream", body), nil
	})
	var resyncs atomic.Int64
	var published []Event
	eventClient, err := NewEventClient(EventClientOptions{
		Client: client,
		OnResync: func(context.Context) error {
			resyncs.Add(1)
			return nil
		},
		OnEvent: func(_ context.Context, event Event) error {
			published = append(published, event)
			return nil
		},
	})
	require.NoError(err)
	cursor := uint64(0)

	opened, err := eventClient.runOnce(t.Context(), &cursor)

	assert.True(opened)
	require.ErrorIs(err, io.ErrUnexpectedEOF)
	assert.Equal(int64(2), resyncs.Load())
	require.Len(published, 1)
	assert.Equal(uint64(42), published[0].ID)
	assert.Equal(uint64(42), cursor)
}

func TestEventClientAcceptsLowerStaleIDAfterHubRestart(t *testing.T) {
	assert := assert.New(t)
	t.Parallel()

	client := stubClient(func(
		_ context.Context, _ federationauth.Scope, _ *http.Request,
	) (*http.Response, error) {
		return eventResponse(
			http.StatusOK, "text/event-stream",
			"id: 2\nevent: reconnect.stale\ndata: {}\n\n"+
				": "+FederationReplayCompleteComment+"\n\n",
		), nil
	})
	var resyncs atomic.Int64
	eventClient, err := NewEventClient(EventClientOptions{
		Client: client,
		OnResync: func(context.Context) error {
			resyncs.Add(1)
			return nil
		},
	})
	require.NoError(t, err)
	cursor := uint64(900)

	opened, err := eventClient.runOnce(t.Context(), &cursor)

	assert.True(opened)
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
	assert.Equal(int64(1), resyncs.Load())
	assert.Equal(uint64(2), cursor)
}

func TestEventRetryDelayIsJitteredAndBounded(t *testing.T) {
	assert := assert.New(t)
	t.Parallel()

	for range 1_000 {
		first := jitteredEventRetryDelay(1)
		assert.GreaterOrEqual(first, 800*time.Millisecond)
		assert.LessOrEqual(first, 1200*time.Millisecond)

		ceiling := jitteredEventRetryDelay(99)
		assert.GreaterOrEqual(ceiling, 3200*time.Millisecond)
		assert.LessOrEqual(ceiling, maxEventRetryDelay)
	}
}

func TestEventClientShutdownInterruptsReconnectWait(t *testing.T) {
	require := require.New(t)
	t.Parallel()

	var calls atomic.Int64
	requestStarted := make(chan struct{}, 1)
	client := stubClient(func(
		_ context.Context, _ federationauth.Scope, _ *http.Request,
	) (*http.Response, error) {
		calls.Add(1)
		requestStarted <- struct{}{}
		return eventResponse(http.StatusUnauthorized, "application/problem+json", `{}`), nil
	})
	eventClient, err := NewEventClient(EventClientOptions{
		Client: client,
	})
	require.NoError(err)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		eventClient.Run(ctx)
		close(done)
	}()
	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		require.FailNow("event client did not start")
	}

	cancel()

	select {
	case <-done:
	case <-time.After(250 * time.Millisecond):
		require.Fail("event client did not stop after cancellation")
	}
	assert.Equal(t, int64(1), calls.Load())
}

func TestEventClientProtocolFailuresRetainBackoffAttempt(t *testing.T) {
	t.Parallel()

	client := stubClient(func(
		_ context.Context, _ federationauth.Scope, _ *http.Request,
	) (*http.Response, error) {
		return eventResponse(http.StatusOK, "application/json", `{}`), nil
	})
	ctx, cancel := context.WithCancel(t.Context())
	var attempts []int
	eventClient, err := NewEventClient(EventClientOptions{
		Client: client,
		RetryDelay: func(failureCount int) time.Duration {
			attempts = append(attempts, failureCount)
			return time.Second
		},
		Wait: func(context.Context, time.Duration) error {
			if len(attempts) == 3 {
				cancel()
				return context.Canceled
			}
			return nil
		},
	})
	require.NoError(t, err)

	eventClient.Run(ctx)

	assert.Equal(t, []int{1, 2, 3}, attempts)
}

func TestEventClientReportsInitialConnectionAfterAuthoritativeRefresh(t *testing.T) {
	t.Parallel()

	client := stubClient(func(
		_ context.Context, _ federationauth.Scope, _ *http.Request,
	) (*http.Response, error) {
		return eventResponse(
			http.StatusOK, "text/event-stream",
			": "+FederationReplayCompleteComment+"\n\n",
		), nil
	})
	var mu sync.Mutex
	var steps []string
	eventClient, err := NewEventClient(EventClientOptions{
		Client: client,
		OnResync: func(context.Context) error {
			mu.Lock()
			steps = append(steps, "refresh")
			mu.Unlock()
			return nil
		},
		OnConnectionChanged: func(connected bool) {
			mu.Lock()
			steps = append(steps, "connected="+map[bool]string{true: "true", false: "false"}[connected])
			mu.Unlock()
		},
	})
	require.NoError(t, err)
	cursor := uint64(0)

	_, err = eventClient.runOnce(t.Context(), &cursor)

	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []string{"refresh", "connected=true"}, steps)
}

func TestEventClientRefreshesAfterReplayBeforeReportingConnected(t *testing.T) {
	t.Parallel()

	client := stubClient(func(
		_ context.Context, _ federationauth.Scope, _ *http.Request,
	) (*http.Response, error) {
		return eventResponse(
			http.StatusOK, "text/event-stream",
			"id: 5\nevent: sync_status\ndata: {\"running\":true}\n\n"+
				": "+FederationReplayCompleteComment+"\n\n",
		), nil
	})
	var steps []string
	eventClient, err := NewEventClient(EventClientOptions{
		Client: client,
		OnEvent: func(_ context.Context, event Event) error {
			steps = append(steps, "replay="+event.Type)
			return nil
		},
		OnResync: func(context.Context) error {
			steps = append(steps, "refresh")
			return nil
		},
		OnConnectionChanged: func(connected bool) {
			steps = append(steps, "connected="+map[bool]string{true: "true", false: "false"}[connected])
		},
	})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	eventClient.wait = func(context.Context, time.Duration) error {
		cancel()
		return context.Canceled
	}

	eventClient.Run(ctx)

	assert.Equal(t, []string{
		"replay=sync_status", "refresh",
		"connected=true", "connected=false",
	}, steps)
}

func eventResponse(status int, contentType, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{contentType}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
