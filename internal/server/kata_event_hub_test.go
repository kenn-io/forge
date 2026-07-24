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
	"strconv"
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
	"go.kenn.io/middleman/internal/server/kataapi"
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

	type ensureResult struct {
		binding *kataFrontendEventBinding
		err     error
	}
	returned := make(chan ensureResult, 1)
	go func() {
		binding, err := registry.Ensure(kata.Daemon{ID: "primary", URL: "http://kata.test"})
		returned <- ensureResult{binding: binding, err: err}
	}()

	var handle *kataFrontendEventBinding
	select {
	case result := <-returned:
		require.NoError(result.err)
		handle = result.binding
	case <-time.After(time.Second):
		require.FailNow("Ensure waited for asynchronous catch-up")
	}
	require.Equal(kataDaemonTargetFingerprint(kata.Daemon{ID: "primary", URL: "http://kata.test"}), handle.DaemonFingerprint())
	assert.Equal(uint64(1), handle.Cursor())

	select {
	case <-catchUpStarted:
	case <-time.After(time.Second):
		require.FailNow("supervisor did not start asynchronously")
	}
	close(releaseCatchUp)
}

func TestKataFrontendEventRegistryEnsureRejectsAfterClose(t *testing.T) {
	assert := assert.New(t)
	registry := newKataFrontendEventRegistry(t.Context(), kataFrontendEventRegistryDeps{})
	registry.Close()

	binding, err := registry.Ensure(kata.Daemon{ID: "primary", URL: "http://kata.test"})
	require.ErrorIs(t, err, errKataFrontendEventsClosed)
	assert.Nil(binding)

	registry.mu.Lock()
	defer registry.mu.Unlock()
	assert.Empty(registry.bindings)
	assert.Empty(registry.targets)
}

func TestKataFrontendEventRegistrySeedsRestartCursorLineage(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	newClient := func(ctx context.Context, _ kata.Daemon) (kataAPIClient, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	oldRegistry := newKataFrontendEventRegistry(t.Context(), kataFrontendEventRegistryDeps{
		newClient:        newClient,
		serverInstanceID: "server-old",
		generationSeed: func(serverInstanceID string) uint64 {
			require.Equal("server-old", serverInstanceID)
			return 100
		},
	})
	oldBinding := requireKataFrontendEventBinding(
		t, oldRegistry, kata.Daemon{ID: "primary", URL: "http://kata.test"},
	)
	oldCursor := oldBinding.Cursor()
	require.Equal(uint64(101), oldCursor)
	oldReplay, stale := oldBinding.hub.RingSnapshotSince(100)
	require.False(stale)
	require.Len(oldReplay, 1)
	assert.Equal("kata.tasks.reset", oldReplay[0].Event.Type)
	oldRegistry.Close()

	newRegistry := newKataFrontendEventRegistry(t.Context(), kataFrontendEventRegistryDeps{
		newClient:        newClient,
		serverInstanceID: "server-new",
		generationSeed: func(serverInstanceID string) uint64 {
			require.Equal("server-new", serverInstanceID)
			return 200
		},
	})
	defer newRegistry.Close()
	newBinding := requireKataFrontendEventBinding(
		t, newRegistry, kata.Daemon{ID: "primary", URL: "http://kata.test"},
	)
	require.Equal(uint64(201), newBinding.Cursor())

	var wire bytes.Buffer
	newBinding.serve(t.Context(), &wire, &stopOnFlushController{at: 2}, oldCursor, true)
	frame := strings.TrimSpace(wire.String())
	assert.Contains(frame, "id: 202")
	assert.Contains(frame, "event: kata.tasks.reset")
	assert.Contains(frame, `"server_instance_id":"server-new"`)
	assert.Contains(frame, `"cursor":202`)
}

func TestKataFrontendEventGenerationSeedLeavesSequenceHeadroom(t *testing.T) {
	assert := assert.New(t)
	seed := kataFrontendEventGenerationSeed("server-range")

	assert.NotZero(seed)
	assert.Equal(seed, kataFrontendEventGenerationSeed("server-range"))
	assert.Less(seed, uint64(1<<48))
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

	handle := requireKataFrontendEventBinding(
		t, registry, kata.Daemon{ID: "primary", URL: "http://kata.test"},
	)
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
	transformed, include := handle.transform(recorded)
	require.True(include)
	frame, ok := transformed.Event.Data.(kataFrontendEventFrame)
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

	binding := requireKataFrontendEventBinding(
		t, registry, kata.Daemon{ID: "primary", URL: "http://kata.test"},
	)
	cursor := binding.Cursor()
	binding.target.invalidateAndBroadcast()

	replay, stale := binding.hub.RingSnapshotSince(cursor)
	require.False(t, stale)
	require.Len(t, replay, 1)
	assert.Equal(t, binding.Cursor(), replay[0].ID)
}

func TestKataFrontendEventFreshSubscriberReplaysOnlyActivationThenLive(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	var epoch atomic.Uint64
	registry := newKataFrontendEventRegistry(t.Context(), kataFrontendEventRegistryDeps{
		newClient: func(ctx context.Context, _ kata.Daemon) (kataAPIClient, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
		invalidate:     func(string) uint64 { return epoch.Add(1) },
		generationSeed: func(string) uint64 { return 0 },
	})
	defer registry.Close()
	binding := requireKataFrontendEventBinding(
		t, registry, kata.Daemon{ID: "primary", URL: "http://kata.test"},
	)
	binding.target.invalidateAndBroadcast()
	firstRetainedTail := binding.Cursor()
	binding.target.invalidateAndBroadcast()
	secondRetainedTail := binding.Cursor()

	paused := make(chan struct{})
	release := make(chan struct{})
	controller := &stopOnFlushController{
		at:      3,
		pauseAt: 2,
		paused:  paused,
		release: release,
	}
	var wire bytes.Buffer
	done := make(chan struct{})
	go func() {
		defer close(done)
		binding.serve(t.Context(), &wire, controller, 0, false)
	}()
	select {
	case <-paused:
	case <-time.After(time.Second):
		require.FailNow("fresh subscriber did not emit activation reset")
	}
	binding.target.invalidateAndBroadcast()
	liveCursor := binding.Cursor()
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		require.FailNow("fresh subscriber did not deliver live invalidation")
	}

	frames := strings.Split(strings.TrimSpace(wire.String()), "\n\n")
	require.Len(frames, 2)
	assert.Contains(frames[0], "id: "+strconv.FormatUint(binding.activation, 10))
	assert.Contains(frames[0], "event: kata.tasks.reset")
	assert.Contains(frames[1], "id: "+strconv.FormatUint(liveCursor, 10))
	assert.Contains(frames[1], "event: kata.tasks.invalidated")
	assert.NotContains(wire.String(), "id: "+strconv.FormatUint(firstRetainedTail, 10)+"\n")
	assert.NotContains(wire.String(), "id: "+strconv.FormatUint(secondRetainedTail, 10)+"\n")
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

	oldBinding := requireKataFrontendEventBinding(
		t, registry, kata.Daemon{ID: "primary", URL: "http://old.test"},
	)
	oldBinding.target.invalidateAndBroadcast()
	oldCursor := oldBinding.Cursor()
	newBinding := requireKataFrontendEventBinding(
		t, registry, kata.Daemon{ID: "primary", URL: "http://new.test"},
	)
	require.NotEqual(t, oldBinding.DaemonFingerprint(), newBinding.DaemonFingerprint())

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	controller := &stopOnFlushController{at: 2}
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
	assert.Contains(frames[0], "id: "+strconv.FormatUint(oldCursor+1, 10))
	assert.Contains(frames[0], `"server_instance_id":"server-1"`)
	assert.Contains(frames[0], `"daemon_id":"primary"`)
	assert.Contains(frames[0], `"epoch":2`)
	assert.Contains(frames[0], `"cursor":`+strconv.FormatUint(oldCursor+1, 10))
	assert.NotContains(frames[0], "old.test")
}

func TestKataFrontendEventRotationPublishesResetFromInitialCursor(t *testing.T) {
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

	oldBinding := requireKataFrontendEventBinding(
		t, registry, kata.Daemon{ID: "primary", URL: "http://old.test"},
	)
	oldCursor := oldBinding.Cursor()
	require.Equal(uint64(1), oldCursor)

	newBinding := requireKataFrontendEventBinding(
		t, registry, kata.Daemon{ID: "primary", URL: "http://new.test"},
	)
	replay, stale := newBinding.hub.RingSnapshotSince(oldCursor)
	require.False(stale)
	require.Len(replay, 1)
	assert.Equal("kata.tasks.reset", replay[0].Event.Type)
	assert.Equal(oldCursor+1, replay[0].ID)
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

	oldBinding := requireKataFrontendEventBinding(
		t, registry, kata.Daemon{ID: "primary", URL: "http://old.test"},
	)
	oldBinding.target.invalidateAndBroadcast()
	oldCursor := oldBinding.Cursor()
	require.Equal(uint64(2), oldCursor)

	newBinding := requireKataFrontendEventBinding(
		t, registry, kata.Daemon{ID: "primary", URL: "http://new.test"},
	)
	replay, stale := newBinding.hub.RingSnapshotSince(oldCursor)
	require.False(stale)
	require.Len(replay, 1)
	assert.Equal("kata.tasks.reset", replay[0].Event.Type)
	assert.Equal(oldCursor+1, replay[0].ID)

	replayAtResetHead, stale := newBinding.hub.RingSnapshotSince(newBinding.Cursor())
	assert.False(stale)
	assert.Empty(replayAtResetHead, "reconnecting at the reset head must not replay a duplicate reset")
}

func TestKataFrontendEventRotationIntoSharedTargetStartsAtReset(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	registry := newKataFrontendEventRegistry(t.Context(), kataFrontendEventRegistryDeps{
		newClient: func(ctx context.Context, _ kata.Daemon) (kataAPIClient, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
		invalidate:       func(string) uint64 { return 1 },
		serverInstanceID: "server-1",
	})
	defer registry.Close()

	alias := requireKataFrontendEventBinding(
		t, registry, kata.Daemon{ID: "alias", URL: "http://new.test"},
	)
	alias.target.invalidateAndBroadcast()
	historicalCursor := alias.Cursor()

	oldBinding := requireKataFrontendEventBinding(
		t, registry, kata.Daemon{ID: "primary", URL: "http://old.test"},
	)
	oldCursor := oldBinding.Cursor()
	newBinding := requireKataFrontendEventBinding(
		t, registry, kata.Daemon{ID: "primary", URL: "http://new.test"},
	)
	resetCursor := newBinding.Cursor()
	require.Greater(resetCursor, historicalCursor)
	require.Greater(resetCursor, oldCursor)
	newBinding.target.invalidateAndBroadcast()
	invalidationCursor := newBinding.Cursor()

	var wire bytes.Buffer
	newBinding.serve(t.Context(), &wire, &stopOnFlushController{at: 3}, oldCursor, true)

	frames := strings.Split(strings.TrimSpace(wire.String()), "\n\n")
	require.Len(frames, 2)
	assert.Contains(frames[0], "id: "+strconv.FormatUint(resetCursor, 10))
	assert.Contains(frames[0], "event: kata.tasks.reset")
	assert.Contains(frames[1], "id: "+strconv.FormatUint(invalidationCursor, 10))
	assert.Contains(frames[1], "event: kata.tasks.invalidated")
	assert.NotContains(wire.String(), "id: "+strconv.FormatUint(historicalCursor, 10)+"\n")
}

func TestKataFrontendEventFreshAliasStartsWithOwnReset(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	invalidated := make(map[string]int)
	registry := newKataFrontendEventRegistry(t.Context(), kataFrontendEventRegistryDeps{
		newClient: func(ctx context.Context, _ kata.Daemon) (kataAPIClient, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
		invalidate: func(daemonID string) uint64 {
			invalidated[daemonID]++
			return uint64(invalidated[daemonID])
		},
		serverInstanceID: "server-1",
	})
	defer registry.Close()

	primary := requireKataFrontendEventBinding(
		t, registry, kata.Daemon{ID: "primary", URL: "http://kata.test"},
	)
	primary.target.invalidateAndBroadcast()
	historicalCursor := primary.Cursor()
	alias := requireKataFrontendEventBinding(
		t, registry, kata.Daemon{ID: "alias", URL: "http://kata.test"},
	)

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	var wire bytes.Buffer
	alias.serve(ctx, &wire, &stopOnFlushController{at: 2}, registry.generationSeed, true)

	frames := strings.Split(strings.TrimSpace(wire.String()), "\n\n")
	require.Len(frames, 1)
	assert.Contains(frames[0], "event: kata.tasks.reset")
	assert.Contains(frames[0], `"daemon_id":"alias"`)
	assert.Greater(alias.Cursor(), historicalCursor)
	assert.Equal(map[string]int{"alias": 1, "primary": 1}, invalidated)

	primaryCtx, cancelPrimary := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancelPrimary()
	var primaryWire bytes.Buffer
	primary.serve(primaryCtx, &primaryWire, &stopOnFlushController{at: 2}, historicalCursor, true)
	assert.Empty(primaryWire.String(), "existing binding must skip another alias's activation reset")
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

	primary := requireKataFrontendEventBinding(
		t, registry, kata.Daemon{ID: "primary", URL: "http://kata.test"},
	)
	alias := requireKataFrontendEventBinding(
		t, registry, kata.Daemon{ID: "alias", URL: "http://kata.test"},
	)
	require.Same(primary.target, alias.target)
	require.Same(primary.hub, alias.hub)
	require.Eventually(func() bool { return clients.Load() == 1 }, time.Second, time.Millisecond)

	primaryEvents, _ := primary.hub.Subscribe(t.Context(), false)
	aliasEvents, _ := alias.hub.Subscribe(t.Context(), false)
	primary.target.invalidateAndBroadcast()

	primaryEvent := <-primaryEvents
	aliasEvent := <-aliasEvents
	primaryRecord, include := primary.transform(primaryEvent)
	require.True(include)
	aliasRecord, include := alias.transform(aliasEvent)
	require.True(include)
	primaryFrame := primaryRecord.Event.Data.(kataFrontendEventFrame)
	aliasFrame := aliasRecord.Event.Data.(kataFrontendEventFrame)
	assert.Equal("primary", primaryFrame.DaemonID)
	assert.Equal("alias", aliasFrame.DaemonID)
	invalidatedMu.Lock()
	assert.Equal(map[string]int{"primary": 1, "alias": 2}, invalidated)
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

	oldBinding := requireKataFrontendEventBinding(
		t, registry, kata.Daemon{ID: "primary", URL: "http://old.test"},
	)
	publicationDone := make(chan struct{})
	go func() {
		oldBinding.target.invalidateAndBroadcast()
		close(publicationDone)
	}()
	<-entered

	replacementDone := make(chan *kataFrontendEventBinding, 1)
	go func() {
		binding, err := registry.Ensure(kata.Daemon{ID: "primary", URL: "http://new.test"})
		if err != nil {
			replacementDone <- nil
			return
		}
		replacementDone <- binding
	}()
	select {
	case <-replacementDone:
		require.FailNow("replacement completed before retiring publication drained")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	<-publicationDone
	finalOldCursor := oldBinding.Cursor()
	newBinding := <-replacementDone
	require.NotNil(newBinding)
	require.NotSame(oldBinding.target, newBinding.target)
	require.Equal(int64(2), invalidations.Load(), "old publication plus rotation invalidation")
	replay, stale := newBinding.hub.RingSnapshotSince(finalOldCursor)
	require.False(stale)
	require.Len(replay, 1)
	assert.Equal("kata.tasks.reset", replay[0].Event.Type)
	assert.Greater(replay[0].ID, finalOldCursor)

	oldBinding.target.invalidateAndBroadcast()
	assert.Equal(int64(2), invalidations.Load(), "retired target must not invalidate shared authority")
}

func TestKataFrontendEventRotationFencesSyntheticReplayCursor(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	registry := newKataFrontendEventRegistry(t.Context(), kataFrontendEventRegistryDeps{
		newClient: func(ctx context.Context, _ kata.Daemon) (kataAPIClient, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
		generationSeed: func(string) uint64 { return 0 },
	})

	oldBinding := requireKataFrontendEventBinding(
		t, registry, kata.Daemon{ID: "primary", URL: "http://old.test"},
	)
	oldBinding.hub.mu.Lock()
	ctx, cancel := context.WithCancel(t.Context())
	var wire bytes.Buffer
	serveDone := make(chan struct{})
	go func() {
		defer close(serveDone)
		oldBinding.serve(ctx, &wire, &stopOnFlushController{at: 2}, 99, true)
	}()

	replayFenced := false
	deadline := time.Now().Add(100 * time.Millisecond)
	for time.Now().Before(deadline) {
		if oldBinding.target.publishMu.TryLock() {
			oldBinding.target.publishMu.Unlock()
			time.Sleep(time.Millisecond)
			continue
		}
		replayFenced = true
		break
	}
	if !replayFenced {
		oldBinding.hub.mu.Unlock()
		cancel()
		<-serveDone
		registry.Close()
		require.True(replayFenced, "synthetic replay did not acquire the target publication fence")
		return
	}

	replacementDone := make(chan *kataFrontendEventBinding, 1)
	go func() {
		binding, err := registry.Ensure(kata.Daemon{ID: "primary", URL: "http://new.test"})
		if err != nil {
			replacementDone <- nil
			return
		}
		replacementDone <- binding
	}()
	select {
	case <-replacementDone:
		require.FailNow("rotation completed before synthetic replay allocated its cursor")
	case <-time.After(20 * time.Millisecond):
	}
	oldBinding.hub.mu.Unlock()
	<-serveDone
	finalOldCursor := oldBinding.Cursor()
	newBinding := <-replacementDone
	require.NotNil(newBinding)
	defer registry.Close()
	cancel()

	replay, stale := newBinding.hub.RingSnapshotSince(finalOldCursor)
	require.False(stale)
	require.Len(replay, 1)
	assert.Equal("kata.tasks.reset", replay[0].Event.Type)
	assert.Greater(replay[0].ID, finalOldCursor)
}

func TestKataFrontendEventRetiredBindingDoesNotReplay(t *testing.T) {
	registry := newKataFrontendEventRegistry(t.Context(), kataFrontendEventRegistryDeps{
		newClient: func(ctx context.Context, _ kata.Daemon) (kataAPIClient, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	})
	defer registry.Close()

	oldBinding := requireKataFrontendEventBinding(
		t, registry, kata.Daemon{ID: "primary", URL: "http://old.test"},
	)
	_ = requireKataFrontendEventBinding(
		t, registry, kata.Daemon{ID: "primary", URL: "http://new.test"},
	)
	controller := &stopOnFlushController{at: 2}
	var wire bytes.Buffer
	oldBinding.serve(t.Context(), &wire, controller, 99, true)

	assert.Empty(t, wire.String())
	assert.Zero(t, controller.count.Load(), "retired binding must abort before opening the stream")
}

func TestKataFrontendEventCatchUpQueuesInvalidationAfterPartialFailure(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
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

	cursor, liveOnly, err := target.catchUp(t.Context(), client, 0)
	require.EqualError(err, "second page failed")
	assert.Equal(int64(1), cursor)
	assert.False(liveOnly)
	select {
	case <-target.invalidated:
	default:
		require.FailNow("dirty catch-up returned without queuing invalidation")
	}
}

func TestKataFrontendEventCatchUpUsesHundredEventPagesAndDefersLiveOnlyInvalidation(t *testing.T) {
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

	cursor, liveOnly, err := target.catchUp(t.Context(), client, 0)
	require.NoError(err)
	assert.Equal(10, calls)
	assert.Equal(int64(1000), cursor)
	assert.True(liveOnly)
	select {
	case <-target.invalidated:
		require.FailNow("bounded live-only catch-up queued invalidation before stream establishment")
	default:
	}
}

func TestKataFrontendEventCatchUpStopsAtPrivateTimeBudgetAndDefersInvalidation(t *testing.T) {
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

	cursor, liveOnly, err := target.catchUp(t.Context(), client, 0)
	require.NoError(err)
	assert.Equal(int64(1), cursor)
	assert.Equal(2, call)
	assert.True(liveOnly)
	select {
	case <-target.invalidated:
		require.FailNow("time-bounded live-only catch-up queued invalidation before stream establishment")
	default:
	}
}

func TestKataFrontendEventSupervisorResumesCatchUpAfterLiveOnlyDisconnect(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	var operationsMu sync.Mutex
	var operations []string
	finalPoll := make(chan struct{})
	pollCalls := 0
	streamCalls := 0
	client := &fakeKataFrontendEventClient{
		poll: func(ctx context.Context, options *katagenerated.PollEventsRequestOptions) (*katagenerated.PollEventsResp, error) {
			pollCalls++
			afterID := int64(0)
			if options.Query.AfterID != nil {
				afterID = *options.Query.AfterID
			}
			operationsMu.Lock()
			operations = append(operations, "poll:"+strconv.FormatInt(afterID, 10))
			operationsMu.Unlock()
			switch pollCalls {
			case 1:
				// Exhaust the catch-up budget so the first stream is live-only.
				return &katagenerated.PollEventsResp{
					StatusCode: http.StatusOK,
					JSON200: &katagenerated.PollEventsBody{
						Events: []katagenerated.EventEnvelope{
							testKataEventEnvelope(1),
							testKataEventEnvelope(2),
						},
						NextAfterID: 2,
					},
				}, nil
			case 2:
				// The live-only stream dropped without delivering an event;
				// the reconnect must catch up from the last cursor.
				return &katagenerated.PollEventsResp{
					StatusCode: http.StatusOK,
					JSON200:    &katagenerated.PollEventsBody{NextAfterID: afterID},
				}, nil
			default:
				close(finalPoll)
				<-ctx.Done()
				return nil, ctx.Err()
			}
		},
		stream: func(_ context.Context, options *katagenerated.StreamEventsRequestOptions) (*http.Response, error) {
			streamCalls++
			afterID := "none"
			if options.Query.AfterID != nil {
				afterID = strconv.FormatInt(*options.Query.AfterID, 10)
			}
			operationsMu.Lock()
			operations = append(operations, "stream:"+afterID)
			operationsMu.Unlock()
			body := ""
			if streamCalls == 2 {
				body = "id: 77\nevent: issue.updated\n\n"
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(body)),
				Header:     make(http.Header),
			}, nil
		},
	}
	target := &kataFrontendEventTarget{
		daemon:           kata.Daemon{ID: "primary", URL: "http://kata.test"},
		invalidated:      make(chan struct{}, 1),
		retryDelay:       time.Millisecond,
		catchUpMaxEvents: 2,
		catchUpTimeout:   time.Second,
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		target.runSupervisor(ctx, func(context.Context, kata.Daemon) (kataAPIClient, error) {
			return client, nil
		})
	}()

	select {
	case <-finalPoll:
	case <-time.After(time.Second):
		require.FailNow("supervisor did not resume catch-up after the live-only disconnect")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		require.FailNow("supervisor did not stop")
	}

	operationsMu.Lock()
	assert.Equal([]string{"poll:0", "stream:none", "poll:2", "stream:2", "poll:77"}, operations)
	operationsMu.Unlock()
	assert.Equal(3, pollCalls)
	assert.Equal(2, streamCalls)
	select {
	case <-target.invalidated:
	default:
		require.FailNow("catch-up exhaustion did not queue invalidation")
	}
}

func TestKataFrontendEventSupervisorDefersLiveOnlyInvalidationUntilStreamEstablished(t *testing.T) {
	require := require.New(t)

	streamEstablishing := make(chan struct{})
	establishStream := make(chan struct{})
	mutationApplied := atomic.Bool{}
	client := &fakeKataFrontendEventClient{
		poll: func(context.Context, *katagenerated.PollEventsRequestOptions) (*katagenerated.PollEventsResp, error) {
			return &katagenerated.PollEventsResp{
				StatusCode: http.StatusOK,
				JSON200: &katagenerated.PollEventsBody{
					Events:      []katagenerated.EventEnvelope{testKataEventEnvelope(1)},
					NextAfterID: 1,
				},
			}, nil
		},
		stream: func(context.Context, *katagenerated.StreamEventsRequestOptions) (*http.Response, error) {
			close(streamEstablishing)
			<-establishStream
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader("")),
				Header:     make(http.Header),
			}, nil
		},
	}
	target := &kataFrontendEventTarget{
		daemon:           kata.Daemon{ID: "primary", URL: "http://kata.test"},
		invalidated:      make(chan struct{}, 1),
		retryDelay:       time.Hour,
		catchUpMaxEvents: 1,
		catchUpTimeout:   time.Second,
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		target.runSupervisor(ctx, func(context.Context, kata.Daemon) (kataAPIClient, error) {
			return client, nil
		})
	}()

	select {
	case <-streamEstablishing:
	case <-time.After(time.Second):
		require.FailNow("supervisor did not begin live-only stream establishment")
	}
	select {
	case <-target.invalidated:
		require.FailNow("live-only invalidation queued before stream establishment")
	default:
	}
	mutationApplied.Store(true)
	close(establishStream)
	select {
	case <-target.invalidated:
		require.True(mutationApplied.Load(), "mutation must precede the live-only invalidation")
	case <-time.After(time.Second):
		require.FailNow("live-only invalidation was not queued after stream establishment")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		require.FailNow("supervisor did not stop")
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

	binding := requireKataFrontendEventBinding(
		t, registry, kata.Daemon{ID: "primary", URL: "http://kata.test"},
	)
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

func TestKataTaskEventsEndpointEmitsResetThenMiddlemanInvalidationFrame(t *testing.T) {
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
	req.Header.Set(kataapi.DaemonHeaderName, "primary")
	req.Header.Set("Last-Event-ID", "0")
	response, err := middleman.Client().Do(req)
	require.NoError(t, err)
	defer func() { _ = response.Body.Close() }()
	scanner := bufio.NewScanner(response.Body)
	reset := readSSEFrameWithin(t, scanner, time.Second, cancel)
	frame := readSSEFrameWithin(t, scanner, time.Second, cancel)
	cancel()

	assert.Equal(http.StatusOK, response.StatusCode)
	assert.Equal("text/event-stream", response.Header.Get("Content-Type"))
	assert.Contains(response.Header.Values("Vary"), kataapi.DaemonHeaderName)
	assert.Equal("kata.tasks.reset", reset.Event)
	assert.Contains(reset.Data, `"daemon_id":"primary"`)
	assert.Equal("kata.tasks.invalidated", frame.Event)
	assert.Contains(frame.Data, `"daemon_id":"primary"`)
	assert.NotContains(frame.Data, "raw-upstream")
}

func TestKataTaskEventsEndpointWithoutCursorStartsAtBindingReset(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
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

	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	writeKataServerCatalog(t, home, `
[[daemon]]
name = "primary"
url = "`+upstream.URL+`"
	`)
	srv, _ := setupTestServer(t)
	middleman := httptest.NewServer(srv)
	daemon, problem := srv.kataAPI.SelectDaemonForID("primary")
	require.Nil(problem)
	binding := requireKataFrontendEventBinding(t, srv.kataEvents, daemon)
	binding.target.invalidateAndBroadcast()
	firstRetainedTail := binding.Cursor()
	binding.target.invalidateAndBroadcast()
	secondRetainedTail := binding.Cursor()

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(func() {
		cancel()
		srv.kataEvents.Close()
		middleman.Close()
		upstream.Close()
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, middleman.URL+"/api/v1/kata/tasks/events", nil)
	require.NoError(err)
	req.Header.Set(kataapi.DaemonHeaderName, "primary")
	response, err := middleman.Client().Do(req)
	require.NoError(err)
	defer func() { _ = response.Body.Close() }()
	scanner := bufio.NewScanner(response.Body)
	frame := readSSEFrameWithin(t, scanner, time.Second, cancel)
	binding.target.invalidateAndBroadcast()
	liveCursor := binding.Cursor()
	liveFrame := readSSEFrameWithin(t, scanner, time.Second, cancel)
	cancel()

	require.Equal(http.StatusOK, response.StatusCode)
	assert.Equal("kata.tasks.reset", frame.Event)
	assert.NotContains(frame.Data, "raw-upstream")
	cursor, err := strconv.ParseUint(frame.ID, 10, 64)
	require.NoError(err)
	var data kataFrontendEventFrame
	require.NoError(json.Unmarshal([]byte(frame.Data), &data))
	assert.NotEmpty(data.ServerInstanceID)
	assert.Equal("primary", data.DaemonID)
	assert.Zero(data.Epoch)
	assert.Equal(cursor, data.Cursor)
	assert.Equal(strconv.FormatUint(binding.activation, 10), frame.ID)
	assert.Equal("kata.tasks.invalidated", liveFrame.Event)
	assert.Equal(strconv.FormatUint(liveCursor, 10), liveFrame.ID)
	assert.NotEqual(strconv.FormatUint(firstRetainedTail, 10), liveFrame.ID)
	assert.NotEqual(strconv.FormatUint(secondRetainedTail, 10), liveFrame.ID)
}

func requireKataFrontendEventBinding(
	t *testing.T,
	registry *kataFrontendEventRegistry,
	daemon kata.Daemon,
) *kataFrontendEventBinding {
	t.Helper()
	binding, err := registry.Ensure(daemon)
	require.NoError(t, err)
	return binding
}

type stopOnFlushController struct {
	at        int64
	pauseAt   int64
	paused    chan<- struct{}
	release   <-chan struct{}
	pauseOnce sync.Once
	count     atomic.Int64
}

func (c *stopOnFlushController) SetWriteDeadline(time.Time) error { return nil }

func (c *stopOnFlushController) Flush() error {
	count := c.count.Add(1)
	if count == c.pauseAt {
		c.pauseOnce.Do(func() { close(c.paused) })
		<-c.release
	}
	if count == c.at {
		return io.EOF
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
