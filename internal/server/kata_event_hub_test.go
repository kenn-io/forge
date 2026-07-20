package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/doordash-oss/oapi-codegen-dd/v3/pkg/runtime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	katagenerated "go.kenn.io/kata/pkg/client/generated"

	"go.kenn.io/middleman/internal/kata"
)

func TestKataFrontendEventRegistryEnsureDoesNotWaitForCatchUp(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	catchUpStarted := make(chan struct{})
	releaseCatchUp := make(chan struct{})
	registry := newKataFrontendEventRegistry(t.Context(), kataFrontendEventRegistryDeps{
		newClient: func(context.Context, kata.Daemon) (kataAPIClient, error) {
			return &fakeKataFrontendEventClient{
				poll: func(context.Context, *katagenerated.PollEventsRequestOptions) (*katagenerated.PollEventsResp, error) {
					close(catchUpStarted)
					<-releaseCatchUp
					return &katagenerated.PollEventsResp{
						StatusCode: http.StatusOK,
						JSON200:    &katagenerated.PollEventsBody{},
					}, nil
				},
			}, nil
		},
	})
	defer registry.Close()

	returned := make(chan *kataFrontendEventBinding, 1)
	go func() {
		returned <- registry.Ensure(kata.Daemon{ID: "primary", URL: "http://kata.test"})
	}()

	var handle *kataFrontendEventBinding
	select {
	case handle = <-returned:
	case <-time.After(time.Second):
		require.FailNow("Ensure waited for asynchronous catch-up")
	}
	require.Equal(kataDaemonTargetFingerprint(kata.Daemon{ID: "primary", URL: "http://kata.test"}), handle.DaemonFingerprint())
	assert.Zero(handle.Cursor())

	select {
	case <-catchUpStarted:
	case <-time.After(time.Second):
		require.FailNow("supervisor did not start asynchronously")
	}
	close(releaseCatchUp)
}

func TestKataFrontendEventSupervisorInvalidatesBeforeBroadcastAndHidesRawPayload(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	streamReader, streamWriter := io.Pipe()
	streamOpened := make(chan struct{})
	client := &fakeKataFrontendEventClient{
		stream: func(context.Context, *katagenerated.StreamEventsRequestOptions) (*http.Response, error) {
			close(streamOpened)
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       streamReader,
				Header:     make(http.Header),
			}, nil
		},
	}
	var invalidated atomic.Bool
	registry := newKataFrontendEventRegistry(t.Context(), kataFrontendEventRegistryDeps{
		newClient: func(context.Context, kata.Daemon) (kataAPIClient, error) {
			return client, nil
		},
		invalidate: func(daemonID string) uint64 {
			assert.Equal("primary", daemonID)
			invalidated.Store(true)
			return 9
		},
		serverInstanceID: "server-1",
		coalesceWindow:   time.Millisecond,
	})
	defer registry.Close()

	handle := registry.Ensure(kata.Daemon{ID: "primary", URL: "http://kata.test"})
	events, _ := handle.hub.Subscribe(t.Context(), false)
	select {
	case <-streamOpened:
	case <-time.After(time.Second):
		require.FailNow("supervisor did not open live stream")
	}

	_, err := io.WriteString(streamWriter, "id: 41\nevent: issue.updated\ndata: {\"secret\":\"raw-upstream\"}\n\n")
	require.NoError(err)

	var recorded RecordedEvent
	select {
	case recorded = <-events:
	case <-time.After(time.Second):
		require.FailNow("supervisor did not broadcast invalidation")
	}
	assert.True(invalidated.Load(), "cache invalidation must be visible before broadcast delivery")
	assert.Equal("kata.tasks.invalidated", recorded.Event.Type)
	frame, ok := handle.transform(recorded).Event.Data.(kataFrontendEventFrame)
	require.True(ok)
	assert.Equal("server-1", frame.ServerInstanceID)
	assert.Equal("primary", frame.DaemonID)
	assert.Equal(uint64(9), frame.Epoch)
	assert.Equal(recorded.ID, frame.Cursor)
	payload, err := json.Marshal(recorded.Event.Data)
	require.NoError(err)
	assert.NotContains(string(payload), "raw-upstream")
	assert.Equal(recorded.ID, handle.Cursor())
}

func TestKataFrontendEventHandleCursorReplaysNextInvalidation(t *testing.T) {
	registry := newKataFrontendEventRegistry(t.Context(), kataFrontendEventRegistryDeps{
		newClient: func(ctx context.Context, _ kata.Daemon) (kataAPIClient, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
		invalidate:       func(string) uint64 { return 3 },
		serverInstanceID: "server-1",
	})
	defer registry.Close()

	binding := registry.Ensure(kata.Daemon{ID: "primary", URL: "http://kata.test"})
	cursor := binding.Cursor()
	binding.target.invalidateAndBroadcast()

	replay, stale := binding.hub.RingSnapshotSince(cursor)
	require.False(t, stale)
	require.Len(t, replay, 1)
	assert.Equal(t, binding.Cursor(), replay[0].ID)
}

func TestKataFrontendEventRotationRejectsOldCursorWithCompactReset(t *testing.T) {
	assert := assert.New(t)
	var epoch atomic.Uint64
	registry := newKataFrontendEventRegistry(t.Context(), kataFrontendEventRegistryDeps{
		newClient: func(ctx context.Context, _ kata.Daemon) (kataAPIClient, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
		invalidate:       func(string) uint64 { return epoch.Add(1) },
		daemonEpoch:      func(string) uint64 { return epoch.Load() },
		serverInstanceID: "server-1",
	})
	defer registry.Close()

	oldBinding := registry.Ensure(kata.Daemon{ID: "primary", URL: "http://old.test"})
	oldBinding.target.invalidateAndBroadcast()
	oldCursor := oldBinding.Cursor()
	newBinding := registry.Ensure(kata.Daemon{ID: "primary", URL: "http://new.test"})
	require.NotEqual(t, oldBinding.DaemonFingerprint(), newBinding.DaemonFingerprint())

	ctx, cancel := context.WithCancel(t.Context())
	controller := &cancelOnSecondFlushController{cancel: cancel}
	var wire bytes.Buffer
	done := make(chan struct{})
	go func() {
		defer close(done)
		newBinding.serve(ctx, &wire, controller, oldCursor, true)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		require.FailNow(t, "rotated stream did not emit reset")
	}

	frames := strings.Split(strings.TrimSpace(wire.String()), "\n\n")
	require.Len(t, frames, 1)
	assert.Contains(frames[0], "event: kata.tasks.reset")
	assert.Contains(frames[0], "id: 2")
	assert.Contains(frames[0], `"server_instance_id":"server-1"`)
	assert.Contains(frames[0], `"daemon_id":"primary"`)
	assert.Contains(frames[0], `"epoch":2`)
	assert.Contains(frames[0], `"cursor":2`)
	assert.NotContains(frames[0], "old.test")
}

func TestKataFrontendEventRotationPublishesResetFromZeroCursor(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	registry := newKataFrontendEventRegistry(t.Context(), kataFrontendEventRegistryDeps{
		newClient: func(ctx context.Context, _ kata.Daemon) (kataAPIClient, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
		invalidate: func(string) uint64 { return 1 },
	})
	defer registry.Close()

	oldBinding := registry.Ensure(kata.Daemon{ID: "primary", URL: "http://old.test"})
	require.Zero(oldBinding.Cursor())

	newBinding := registry.Ensure(kata.Daemon{ID: "primary", URL: "http://new.test"})
	replay, stale := newBinding.hub.RingSnapshotSince(0)
	require.False(stale)
	require.Len(replay, 1)
	assert.Equal("kata.tasks.reset", replay[0].Event.Type)
	assert.Equal(uint64(1), replay[0].ID)
}

func TestKataFrontendEventRotationKeepsCursorMonotonicAcrossEqualNumericHeads(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	registry := newKataFrontendEventRegistry(t.Context(), kataFrontendEventRegistryDeps{
		newClient: func(ctx context.Context, _ kata.Daemon) (kataAPIClient, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
		invalidate: func(string) uint64 { return 1 },
	})
	defer registry.Close()

	oldBinding := registry.Ensure(kata.Daemon{ID: "primary", URL: "http://old.test"})
	oldBinding.target.invalidateAndBroadcast()
	oldCursor := oldBinding.Cursor()
	require.Equal(uint64(1), oldCursor)

	newBinding := registry.Ensure(kata.Daemon{ID: "primary", URL: "http://new.test"})
	replay, stale := newBinding.hub.RingSnapshotSince(oldCursor)
	require.False(stale)
	require.Len(replay, 1)
	assert.Equal("kata.tasks.reset", replay[0].Event.Type)
	assert.Equal(oldCursor+1, replay[0].ID)

	replayAtResetHead, stale := newBinding.hub.RingSnapshotSince(newBinding.Cursor())
	assert.False(stale)
	assert.Empty(replayAtResetHead, "reconnecting at the reset head must not replay a duplicate reset")
}

func TestKataFrontendEventRegistryDeduplicatesResolvedTargetAliases(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	var clients atomic.Int64
	var invalidatedMu sync.Mutex
	invalidated := make(map[string]int)
	registry := newKataFrontendEventRegistry(t.Context(), kataFrontendEventRegistryDeps{
		newClient: func(ctx context.Context, _ kata.Daemon) (kataAPIClient, error) {
			clients.Add(1)
			<-ctx.Done()
			return nil, ctx.Err()
		},
		invalidate: func(daemonID string) uint64 {
			invalidatedMu.Lock()
			defer invalidatedMu.Unlock()
			invalidated[daemonID]++
			return uint64(invalidated[daemonID])
		},
		coalesceWindow: time.Millisecond,
	})
	defer registry.Close()

	primary := registry.Ensure(kata.Daemon{ID: "primary", URL: "http://kata.test"})
	alias := registry.Ensure(kata.Daemon{ID: "alias", URL: "http://kata.test"})
	require.Same(primary.target, alias.target)
	require.Same(primary.hub, alias.hub)
	require.Eventually(func() bool { return clients.Load() == 1 }, time.Second, time.Millisecond)

	primaryEvents, _ := primary.hub.Subscribe(t.Context(), false)
	aliasEvents, _ := alias.hub.Subscribe(t.Context(), false)
	primary.target.invalidateAndBroadcast()

	primaryEvent := <-primaryEvents
	aliasEvent := <-aliasEvents
	primaryFrame := primary.transform(primaryEvent).Event.Data.(kataFrontendEventFrame)
	aliasFrame := alias.transform(aliasEvent).Event.Data.(kataFrontendEventFrame)
	assert.Equal("primary", primaryFrame.DaemonID)
	assert.Equal("alias", aliasFrame.DaemonID)
	invalidatedMu.Lock()
	assert.Equal(map[string]int{"primary": 1, "alias": 1}, invalidated)
	invalidatedMu.Unlock()
}

func TestKataFrontendEventRetiredPublisherCannotInvalidateReplacement(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	var invalidations atomic.Int64
	registry := newKataFrontendEventRegistry(t.Context(), kataFrontendEventRegistryDeps{
		newClient: func(ctx context.Context, _ kata.Daemon) (kataAPIClient, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
		invalidate: func(string) uint64 {
			value := invalidations.Add(1)
			if value == 1 {
				close(entered)
				<-release
			}
			return uint64(value)
		},
	})
	defer registry.Close()

	oldBinding := registry.Ensure(kata.Daemon{ID: "primary", URL: "http://old.test"})
	publicationDone := make(chan struct{})
	go func() {
		oldBinding.target.invalidateAndBroadcast()
		close(publicationDone)
	}()
	<-entered

	replacementDone := make(chan *kataFrontendEventBinding, 1)
	go func() {
		replacementDone <- registry.Ensure(kata.Daemon{ID: "primary", URL: "http://new.test"})
	}()
	select {
	case <-replacementDone:
		require.FailNow("replacement completed before retiring publication drained")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	<-publicationDone
	newBinding := <-replacementDone
	require.NotSame(oldBinding.target, newBinding.target)
	require.Equal(int64(2), invalidations.Load(), "old publication plus rotation invalidation")

	oldBinding.target.invalidateAndBroadcast()
	assert.Equal(int64(2), invalidations.Load(), "retired target must not invalidate shared authority")
}

func TestKataFrontendEventCatchUpQueuesInvalidationAfterPartialFailure(t *testing.T) {
	call := 0
	target := &kataFrontendEventTarget{
		invalidated:      make(chan struct{}, 1),
		catchUpMaxEvents: 1000,
		catchUpTimeout:   2 * time.Second,
	}
	client := &fakeKataFrontendEventClient{
		poll: func(_ context.Context, _ *katagenerated.PollEventsRequestOptions) (*katagenerated.PollEventsResp, error) {
			call++
			if call == 2 {
				return nil, errors.New("second page failed")
			}
			return &katagenerated.PollEventsResp{
				StatusCode: http.StatusOK,
				JSON200: &katagenerated.PollEventsBody{
					Events:      []katagenerated.EventEnvelope{testKataEventEnvelope(1)},
					NextAfterID: 1,
				},
			}, nil
		},
	}

	cursor, err := target.catchUp(t.Context(), client, 0)
	require.EqualError(t, err, "second page failed")
	assert.Equal(t, int64(1), cursor)
	select {
	case <-target.invalidated:
	default:
		require.FailNow(t, "dirty catch-up returned without queuing invalidation")
	}
}

func TestKataFrontendEventCatchUpUsesHundredEventPagesAndStopsAtThousand(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	var calls int
	target := &kataFrontendEventTarget{
		invalidated:      make(chan struct{}, 1),
		catchUpMaxEvents: 1000,
		catchUpTimeout:   2 * time.Second,
	}
	client := &fakeKataFrontendEventClient{
		poll: func(_ context.Context, options *katagenerated.PollEventsRequestOptions) (*katagenerated.PollEventsResp, error) {
			calls++
			require.NotNil(options.Query.Limit)
			assert.Equal(int64(100), *options.Query.Limit)
			start := int64((calls - 1) * 100)
			events := make([]katagenerated.EventEnvelope, 100)
			for i := range events {
				events[i] = testKataEventEnvelope(start + int64(i) + 1)
			}
			return &katagenerated.PollEventsResp{
				StatusCode: http.StatusOK,
				JSON200: &katagenerated.PollEventsBody{
					Events:      events,
					NextAfterID: start + 100,
				},
			}, nil
		},
	}

	cursor, err := target.catchUp(t.Context(), client, 0)
	require.NoError(err)
	assert.Equal(10, calls)
	assert.Equal(int64(1000), cursor)
	select {
	case <-target.invalidated:
	default:
		require.FailNow("bounded dirty catch-up did not queue invalidation")
	}
}

func TestKataFrontendEventCatchUpStopsAtPrivateTimeBudget(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	target := &kataFrontendEventTarget{
		invalidated:      make(chan struct{}, 1),
		catchUpMaxEvents: 1000,
		catchUpTimeout:   10 * time.Millisecond,
	}
	call := 0
	client := &fakeKataFrontendEventClient{
		poll: func(ctx context.Context, _ *katagenerated.PollEventsRequestOptions) (*katagenerated.PollEventsResp, error) {
			call++
			if call == 1 {
				return &katagenerated.PollEventsResp{
					StatusCode: http.StatusOK,
					JSON200: &katagenerated.PollEventsBody{
						Events:      []katagenerated.EventEnvelope{testKataEventEnvelope(1)},
						NextAfterID: 1,
					},
				}, nil
			}
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}

	cursor, err := target.catchUp(t.Context(), client, 0)
	require.NoError(err)
	assert.Equal(int64(1), cursor)
	assert.Equal(2, call)
	select {
	case <-target.invalidated:
	default:
		require.FailNow("time-bounded dirty catch-up did not queue invalidation")
	}
}

func TestKataFrontendEventPublisherCoalescesBurst(t *testing.T) {
	registry := newKataFrontendEventRegistry(t.Context(), kataFrontendEventRegistryDeps{
		newClient: func(ctx context.Context, _ kata.Daemon) (kataAPIClient, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
		invalidate:     func(string) uint64 { return 1 },
		coalesceWindow: 10 * time.Millisecond,
	})
	defer registry.Close()

	binding := registry.Ensure(kata.Daemon{ID: "primary", URL: "http://kata.test"})
	events, _ := binding.hub.Subscribe(t.Context(), false)
	for range 20 {
		binding.target.queueInvalidation()
	}

	select {
	case <-events:
	case <-time.After(time.Second):
		require.FailNow(t, "coalesced invalidation was not published")
	}
	select {
	case <-events:
		require.FailNow(t, "burst produced more than one invalidation frame")
	case <-time.After(30 * time.Millisecond):
	}
}

func TestKataTaskEventsEndpointEmitsOnlyMiddlemanInvalidationFrame(t *testing.T) {
	assert := assert.New(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/events":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"events":[],"next_after_id":0,"reset_required":false}`)
		case "/api/v1/events/stream":
			w.Header().Set("Content-Type", "text/event-stream")
			_ = http.NewResponseController(w).Flush()
			_, _ = io.WriteString(w, "id: 1\nevent: issue.updated\ndata: {\"secret\":\"raw-upstream\"}\n\n")
			_ = http.NewResponseController(w).Flush()
			<-r.Context().Done()
		default:
			http.NotFound(w, r)
		}
	}))

	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	writeKataServerCatalog(t, home, `
[[daemon]]
name = "primary"
url = "`+upstream.URL+`"
	`)
	srv, _ := setupTestServer(t)
	middleman := httptest.NewServer(srv)

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(func() {
		cancel()
		srv.kataEvents.Close()
		middleman.Close()
		upstream.Close()
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, middleman.URL+"/api/v1/kata/tasks/events", nil)
	require.NoError(t, err)
	req.Header.Set(kataDaemonHeaderName, "primary")
	req.Header.Set("Last-Event-ID", "0")
	response, err := middleman.Client().Do(req)
	require.NoError(t, err)
	defer func() { _ = response.Body.Close() }()
	frame := readSSEFrameWithin(t, bufio.NewScanner(response.Body), time.Second, cancel)
	cancel()

	assert.Equal(http.StatusOK, response.StatusCode)
	assert.Equal("text/event-stream", response.Header.Get("Content-Type"))
	assert.Contains(response.Header.Values("Vary"), kataDaemonHeaderName)
	assert.Equal("kata.tasks.invalidated", frame.Event)
	assert.Contains(frame.Data, `"daemon_id":"primary"`)
	assert.NotContains(frame.Data, "raw-upstream")
}

type cancelOnSecondFlushController struct {
	cancel context.CancelFunc
	count  atomic.Int64
}

func (c *cancelOnSecondFlushController) SetWriteDeadline(time.Time) error { return nil }

func (c *cancelOnSecondFlushController) Flush() error {
	if c.count.Add(1) == 2 {
		c.cancel()
	}
	return nil
}

type fakeKataFrontendEventClient struct {
	kataAPIClient
	poll   func(context.Context, *katagenerated.PollEventsRequestOptions) (*katagenerated.PollEventsResp, error)
	stream func(context.Context, *katagenerated.StreamEventsRequestOptions) (*http.Response, error)
}

func testKataEventEnvelope(id int64) katagenerated.EventEnvelope {
	return katagenerated.EventEnvelope{EventID: id}
}

func (c *fakeKataFrontendEventClient) PollEventsWithResponse(
	ctx context.Context,
	options *katagenerated.PollEventsRequestOptions,
	_ ...runtime.RequestEditorFn,
) (*katagenerated.PollEventsResp, error) {
	if c.poll == nil {
		return &katagenerated.PollEventsResp{
			StatusCode: http.StatusOK,
			JSON200:    &katagenerated.PollEventsBody{},
		}, nil
	}
	return c.poll(ctx, options)
}

func (c *fakeKataFrontendEventClient) StreamEventsRaw(
	ctx context.Context,
	options *katagenerated.StreamEventsRequestOptions,
	_ ...runtime.RequestEditorFn,
) (*http.Response, error) {
	if c.stream == nil {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return c.stream(ctx, options)
}
