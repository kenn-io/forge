package server

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	katagenerated "go.kenn.io/kata/pkg/client/generated"

	"go.kenn.io/middleman/internal/kata"
)

const (
	kataFrontendEventBufferSize      = 64
	kataFrontendEventCoalesceWindow  = 25 * time.Millisecond
	kataFrontendEventRetryDelay      = time.Second
	kataFrontendEventCatchUpPageSize = 100
	kataFrontendEventCatchUpMax      = 1000
	kataFrontendEventCatchUpTimeout  = 2 * time.Second
)

type kataFrontendEventRegistryDeps struct {
	newClient        func(context.Context, kata.Daemon) (kataAPIClient, error)
	invalidate       func(string) uint64
	daemonEpoch      func(string) uint64
	serverInstanceID string
	coalesceWindow   time.Duration
	retryDelay       time.Duration
	catchUpMaxEvents int
	catchUpTimeout   time.Duration
}

type kataFrontendEventRegistry struct {
	root     context.Context
	deps     kataFrontendEventRegistryDeps
	mu       sync.Mutex
	bindings map[string]*kataFrontendEventBinding
	targets  map[string]*kataFrontendEventTarget
	closed   bool
	wg       sync.WaitGroup
}

type kataFrontendEventFrame struct {
	ServerInstanceID string `json:"server_instance_id"`
	DaemonID         string `json:"daemon_id"`
	Epoch            uint64 `json:"epoch"`
	Cursor           uint64 `json:"cursor"`
}

type kataFrontendEventRecord struct {
	epochs map[string]uint64
}

type kataFrontendEventBinding struct {
	daemonID    string
	fingerprint string
	target      *kataFrontendEventTarget
	hub         *EventHub
	epoch       atomic.Uint64
	done        chan struct{}
	closeOnce   sync.Once
}

type kataFrontendEventTarget struct {
	daemon           kata.Daemon
	fingerprint      string
	hub              *EventHub
	cancel           context.CancelFunc
	invalidate       func(string) uint64
	serverID         string
	coalesce         time.Duration
	retryDelay       time.Duration
	catchUpMaxEvents int
	catchUpTimeout   time.Duration
	invalidated      chan struct{}
	publishMu        sync.Mutex
	bindings         map[string]*kataFrontendEventBinding
	retired          bool
}

func newKataFrontendEventRegistry(
	root context.Context,
	deps kataFrontendEventRegistryDeps,
) *kataFrontendEventRegistry {
	if root == nil {
		root = context.Background()
	}
	if deps.newClient == nil {
		deps.newClient = newKataAPIClient
	}
	if deps.invalidate == nil {
		deps.invalidate = func(string) uint64 { return 0 }
	}
	if deps.daemonEpoch == nil {
		deps.daemonEpoch = func(string) uint64 { return 0 }
	}
	if deps.coalesceWindow <= 0 {
		deps.coalesceWindow = kataFrontendEventCoalesceWindow
	}
	if deps.retryDelay <= 0 {
		deps.retryDelay = kataFrontendEventRetryDelay
	}
	if deps.catchUpMaxEvents <= 0 {
		deps.catchUpMaxEvents = kataFrontendEventCatchUpMax
	}
	if deps.catchUpTimeout <= 0 {
		deps.catchUpTimeout = kataFrontendEventCatchUpTimeout
	}
	return &kataFrontendEventRegistry{
		root:     root,
		deps:     deps,
		bindings: make(map[string]*kataFrontendEventBinding),
		targets:  make(map[string]*kataFrontendEventTarget),
	}
}

func (r *kataFrontendEventRegistry) Ensure(daemon kata.Daemon) *kataFrontendEventBinding {
	fingerprint := kataDaemonTargetFingerprint(daemon)

	r.mu.Lock()
	defer r.mu.Unlock()
	if current := r.bindings[daemon.ID]; current != nil && current.fingerprint == fingerprint {
		return current
	}

	var cursorFloor uint64
	replacing := false
	if current := r.bindings[daemon.ID]; current != nil {
		replacing = true
		cursorFloor = current.Cursor()
		oldTarget := current.target
		oldTarget.publishMu.Lock()
		delete(oldTarget.bindings, daemon.ID)
		current.close()
		if len(oldTarget.bindings) == 0 {
			oldTarget.retired = true
			oldTarget.cancel()
			oldTarget.hub.Close()
			delete(r.targets, oldTarget.fingerprint)
		}
		oldTarget.publishMu.Unlock()
		delete(r.bindings, daemon.ID)
	}

	target := r.targets[fingerprint]
	createdTarget := target == nil
	var targetCtx context.Context
	if createdTarget {
		var cancel context.CancelFunc
		targetCtx, cancel = context.WithCancel(r.root)
		target = &kataFrontendEventTarget{
			daemon:           daemon,
			fingerprint:      fingerprint,
			hub:              NewEventHubWithCapacity(kataFrontendEventBufferSize),
			cancel:           cancel,
			invalidate:       r.deps.invalidate,
			serverID:         r.deps.serverInstanceID,
			coalesce:         r.deps.coalesceWindow,
			retryDelay:       r.deps.retryDelay,
			catchUpMaxEvents: r.deps.catchUpMaxEvents,
			catchUpTimeout:   r.deps.catchUpTimeout,
			invalidated:      make(chan struct{}, 1),
			bindings:         make(map[string]*kataFrontendEventBinding),
		}
		r.targets[fingerprint] = target
	}

	epoch := r.deps.daemonEpoch(daemon.ID)
	if replacing {
		epoch = r.deps.invalidate(daemon.ID)
	}
	binding := &kataFrontendEventBinding{
		daemonID:    daemon.ID,
		fingerprint: fingerprint,
		target:      target,
		hub:         target.hub,
		done:        make(chan struct{}),
	}
	binding.epoch.Store(epoch)
	target.publishMu.Lock()
	target.bindings[daemon.ID] = binding
	if replacing {
		target.broadcastResetAtLeastLocked(cursorFloor)
	}
	target.publishMu.Unlock()
	r.bindings[daemon.ID] = binding

	if createdTarget && !r.closed {
		r.wg.Add(2)
		go func() {
			defer r.wg.Done()
			target.runPublisher(targetCtx)
		}()
		go func() {
			defer r.wg.Done()
			target.runSupervisor(targetCtx, r.deps.newClient)
		}()
	}
	return binding
}

func (b *kataFrontendEventBinding) DaemonFingerprint() string {
	return b.fingerprint
}

func (b *kataFrontendEventBinding) Cursor() uint64 {
	return b.hub.Generation()
}

func (b *kataFrontendEventBinding) close() {
	b.closeOnce.Do(func() { close(b.done) })
}

func (b *kataFrontendEventBinding) serve(
	ctx context.Context,
	w io.Writer,
	rc sseController,
	cursor uint64,
	hasCursor bool,
) {
	serveCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		select {
		case <-b.done:
			cancel()
		case <-serveCtx.Done():
		}
	}()
	ch, done := b.hub.Subscribe(serveCtx, false)
	serveSSESubscribedFromHubTransformed(
		serveCtx,
		w,
		rc,
		b.hub,
		cursor,
		hasCursor,
		ch,
		done,
		func(id uint64) Event {
			return Event{
				Type: "kata.tasks.reset",
				Data: kataFrontendEventFrame{
					ServerInstanceID: b.target.serverID,
					DaemonID:         b.daemonID,
					Epoch:            b.epoch.Load(),
					Cursor:           id,
				},
			}
		},
		b.transform,
	)
}

func (b *kataFrontendEventBinding) transform(rec RecordedEvent) RecordedEvent {
	internal, _ := rec.Event.Data.(kataFrontendEventRecord)
	epoch := b.epoch.Load()
	if value, ok := internal.epochs[b.daemonID]; ok {
		epoch = value
	}
	return RecordedEvent{
		ID: rec.ID,
		Event: Event{
			Type: rec.Event.Type,
			Data: kataFrontendEventFrame{
				ServerInstanceID: b.target.serverID,
				DaemonID:         b.daemonID,
				Epoch:            epoch,
				Cursor:           rec.ID,
			},
		},
	}
}

func (t *kataFrontendEventTarget) queueInvalidation() {
	select {
	case t.invalidated <- struct{}{}:
	default:
	}
}

func (t *kataFrontendEventTarget) runPublisher(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.invalidated:
		}

		timer := time.NewTimer(t.coalesce)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return
		case <-timer.C:
		}
		draining := true
		for draining {
			select {
			case <-t.invalidated:
			default:
				draining = false
			}
		}
		t.invalidateAndBroadcast()
	}
}

func (t *kataFrontendEventTarget) invalidateAndBroadcast() {
	t.publishMu.Lock()
	defer t.publishMu.Unlock()
	if t.retired || len(t.bindings) == 0 {
		return
	}
	epochs := make(map[string]uint64, len(t.bindings))
	for daemonID, binding := range t.bindings {
		epoch := t.invalidate(daemonID)
		binding.epoch.Store(epoch)
		epochs[daemonID] = epoch
	}
	t.hub.BroadcastBuild(func(uint64) Event {
		return Event{
			Type: "kata.tasks.invalidated",
			Data: kataFrontendEventRecord{epochs: epochs},
		}
	})
}

func (t *kataFrontendEventTarget) broadcastResetAtLeastLocked(cursorFloor uint64) {
	epochs := make(map[string]uint64, len(t.bindings))
	for daemonID, binding := range t.bindings {
		epochs[daemonID] = binding.epoch.Load()
	}
	t.hub.BroadcastBuildAtLeast(cursorFloor, func(uint64) Event {
		return Event{
			Type: "kata.tasks.reset",
			Data: kataFrontendEventRecord{epochs: epochs},
		}
	})
}

func (t *kataFrontendEventTarget) runSupervisor(
	ctx context.Context,
	newClient func(context.Context, kata.Daemon) (kataAPIClient, error),
) {
	var upstreamCursor int64
	for ctx.Err() == nil {
		client, err := newClient(ctx, t.daemon)
		if err == nil {
			upstreamCursor, err = t.catchUp(ctx, client, upstreamCursor)
		}
		if err == nil {
			_ = t.stream(ctx, client, &upstreamCursor)
		}
		if ctx.Err() != nil {
			return
		}
		timer := time.NewTimer(t.retryDelay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return
		case <-timer.C:
		}
	}
}

func (t *kataFrontendEventTarget) catchUp(
	ctx context.Context,
	client kataAPIClient,
	afterID int64,
) (int64, error) {
	dirty := false
	defer func() {
		if dirty {
			t.queueInvalidation()
		}
	}()
	catchUpCtx, cancel := context.WithTimeout(ctx, t.catchUpTimeout)
	defer cancel()
	eventCount := 0
	for {
		limit := int64(kataFrontendEventCatchUpPageSize)
		response, err := client.PollEventsWithResponse(catchUpCtx, &katagenerated.PollEventsRequestOptions{
			Query: &katagenerated.PollEventsQuery{AfterID: &afterID, Limit: &limit},
		})
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
				return afterID, nil
			}
			return afterID, err
		}
		if response == nil || response.StatusCode != http.StatusOK || response.JSON200 == nil {
			return afterID, errors.New("kata event catch-up returned an invalid response")
		}
		body := response.JSON200
		if body.ResetRequired {
			dirty = true
			if body.ResetAfterID != nil {
				afterID = *body.ResetAfterID
			}
		}
		if len(body.Events) == 0 {
			if body.NextAfterID != afterID {
				return afterID, errors.New("kata event catch-up cursor did not match the empty page request")
			}
			return afterID, nil
		}
		dirty = true
		lastID := body.Events[len(body.Events)-1].EventID
		if body.NextAfterID != lastID || lastID <= afterID {
			return afterID, errors.New("kata event catch-up cursor did not advance to the last event")
		}
		afterID = lastID
		eventCount += len(body.Events)
		if eventCount >= t.catchUpMaxEvents {
			return afterID, nil
		}
	}
}

func (t *kataFrontendEventTarget) stream(
	ctx context.Context,
	client kataAPIClient,
	upstreamCursor *int64,
) error {
	response, err := client.StreamEventsRaw(ctx, &katagenerated.StreamEventsRequestOptions{
		Query: &katagenerated.StreamEventsQuery{AfterID: upstreamCursor},
	})
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	closed := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = response.Body.Close()
		case <-closed:
		}
	}()
	defer close(closed)

	reader := bufio.NewReaderSize(response.Body, 4096)
	var eventID int64
	var eventType string
	for {
		line, readErr := readKataSSELine(reader)
		if len(line) > 0 {
			switch {
			case bytes.HasPrefix(line, []byte("id:")):
				parsed, parseErr := strconv.ParseInt(strings.TrimSpace(string(line[3:])), 10, 64)
				if parseErr == nil {
					eventID = parsed
				}
			case bytes.HasPrefix(line, []byte("event:")):
				eventType = strings.TrimSpace(string(line[6:]))
			}
		} else if readErr == nil && eventType != "" && eventID > *upstreamCursor {
			*upstreamCursor = eventID
			t.queueInvalidation()
			eventID = 0
			eventType = ""
		}
		if readErr != nil {
			return readErr
		}
	}
}

func readKataSSELine(reader *bufio.Reader) ([]byte, error) {
	line, err := reader.ReadSlice('\n')
	if !errors.Is(err, bufio.ErrBufferFull) {
		return bytes.TrimSpace(line), err
	}
	for errors.Is(err, bufio.ErrBufferFull) {
		_, err = reader.ReadSlice('\n')
	}
	if errors.Is(err, io.EOF) {
		return nil, io.EOF
	}
	return nil, err
}

func (r *kataFrontendEventRegistry) Close() {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.closed = true
	for _, target := range r.targets {
		target.publishMu.Lock()
		target.retired = true
		for _, binding := range target.bindings {
			binding.close()
		}
		target.bindings = nil
		target.cancel()
		target.hub.Close()
		target.publishMu.Unlock()
	}
	r.mu.Unlock()
	r.wg.Wait()
}
