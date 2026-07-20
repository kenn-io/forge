package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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

	returned := make(chan kataFrontendEventHandle, 1)
	go func() {
		returned <- registry.Ensure(kata.Daemon{ID: "primary", URL: "http://kata.test"})
	}()

	var handle kataFrontendEventHandle
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
	target := handle.(*kataFrontendEventTarget)
	events, _ := target.hub.Subscribe(t.Context(), false)
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
	frame, ok := recorded.Event.Data.(kataFrontendEventFrame)
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

	target := registry.Ensure(kata.Daemon{ID: "primary", URL: "http://kata.test"}).(*kataFrontendEventTarget)
	cursor := target.Cursor()
	target.invalidateAndBroadcast()

	replay, stale := target.hub.RingSnapshotSince(cursor)
	require.False(t, stale)
	require.Len(t, replay, 1)
	assert.Equal(t, target.Cursor(), replay[0].ID)
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

	oldTarget := registry.Ensure(kata.Daemon{ID: "primary", URL: "http://old.test"}).(*kataFrontendEventTarget)
	oldTarget.invalidateAndBroadcast()
	oldCursor := oldTarget.Cursor()
	newTarget := registry.Ensure(kata.Daemon{ID: "primary", URL: "http://new.test"}).(*kataFrontendEventTarget)
	require.NotEqual(t, oldTarget.DaemonFingerprint(), newTarget.DaemonFingerprint())

	ctx, cancel := context.WithCancel(t.Context())
	controller := &cancelOnSecondFlushController{cancel: cancel}
	var wire bytes.Buffer
	done := make(chan struct{})
	go func() {
		defer close(done)
		newTarget.serve(ctx, &wire, controller, oldCursor, true)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		require.FailNow(t, "rotated stream did not emit reset")
	}

	frames := strings.Split(strings.TrimSpace(wire.String()), "\n\n")
	require.Len(t, frames, 1)
	assert.Contains(frames[0], "event: kata.tasks.reset")
	assert.Contains(frames[0], "id: 1")
	assert.Contains(frames[0], `"server_instance_id":"server-1"`)
	assert.Contains(frames[0], `"daemon_id":"primary"`)
	assert.Contains(frames[0], `"epoch":2`)
	assert.Contains(frames[0], `"cursor":1`)
	assert.NotContains(frames[0], "old.test")
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
