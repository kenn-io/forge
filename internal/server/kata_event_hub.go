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
	kataFrontendEventBufferSize     = 64
	kataFrontendEventCoalesceWindow = 25 * time.Millisecond
	kataFrontendEventRetryDelay     = time.Second
)

type kataFrontendEventHandle interface {
	DaemonFingerprint() string
	Cursor() uint64
}

type kataFrontendEventRegistryDeps struct {
	newClient        func(context.Context, kata.Daemon) (kataAPIClient, error)
	invalidate       func(string) uint64
	daemonEpoch      func(string) uint64
	serverInstanceID string
	coalesceWindow   time.Duration
	retryDelay       time.Duration
}

type kataFrontendEventRegistry struct {
	root    context.Context
	deps    kataFrontendEventRegistryDeps
	mu      sync.Mutex
	targets map[string]*kataFrontendEventTarget
	closed  bool
	wg      sync.WaitGroup
}

type kataFrontendEventFrame struct {
	ServerInstanceID string `json:"server_instance_id"`
	DaemonID         string `json:"daemon_id"`
	Epoch            uint64 `json:"epoch"`
	Cursor           uint64 `json:"cursor"`
}

type kataFrontendEventTarget struct {
	daemon      kata.Daemon
	fingerprint string
	hub         *EventHub
	cancel      context.CancelFunc
	invalidate  func(string) uint64
	serverID    string
	coalesce    time.Duration
	retryDelay  time.Duration
	epoch       atomic.Uint64
	invalidated chan struct{}
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
	return &kataFrontendEventRegistry{
		root:    root,
		deps:    deps,
		targets: make(map[string]*kataFrontendEventTarget),
	}
}

func (r *kataFrontendEventRegistry) Ensure(daemon kata.Daemon) kataFrontendEventHandle {
	fingerprint := kataDaemonTargetFingerprint(daemon)

	r.mu.Lock()
	defer r.mu.Unlock()
	if current := r.targets[daemon.ID]; current != nil && current.fingerprint == fingerprint {
		return current
	}
	epoch := r.deps.daemonEpoch(daemon.ID)
	if current := r.targets[daemon.ID]; current != nil {
		current.cancel()
		current.hub.Close()
		epoch = r.deps.invalidate(daemon.ID)
	}

	ctx, cancel := context.WithCancel(r.root)
	target := &kataFrontendEventTarget{
		daemon:      daemon,
		fingerprint: fingerprint,
		hub:         NewEventHubWithCapacity(kataFrontendEventBufferSize),
		cancel:      cancel,
		invalidate:  r.deps.invalidate,
		serverID:    r.deps.serverInstanceID,
		coalesce:    r.deps.coalesceWindow,
		retryDelay:  r.deps.retryDelay,
		invalidated: make(chan struct{}, 1),
	}
	target.epoch.Store(epoch)
	r.targets[daemon.ID] = target
	if !r.closed {
		r.wg.Add(2)
		go func() {
			defer r.wg.Done()
			target.runPublisher(ctx)
		}()
		go func() {
			defer r.wg.Done()
			target.runSupervisor(ctx, r.deps.newClient)
		}()
	}
	return target
}

func (t *kataFrontendEventTarget) DaemonFingerprint() string {
	return t.fingerprint
}

func (t *kataFrontendEventTarget) Cursor() uint64 {
	return t.hub.Generation()
}

func (t *kataFrontendEventTarget) serve(
	ctx context.Context,
	w io.Writer,
	rc sseController,
	cursor uint64,
	hasCursor bool,
) {
	ch, done := t.hub.Subscribe(ctx, false)
	serveSSESubscribedFromHub(
		ctx,
		w,
		rc,
		t.hub,
		cursor,
		hasCursor,
		ch,
		done,
		func(id uint64) Event {
			return Event{
				Type: "kata.tasks.reset",
				Data: kataFrontendEventFrame{
					ServerInstanceID: t.serverID,
					DaemonID:         t.daemon.ID,
					Epoch:            t.epoch.Load(),
					Cursor:           id,
				},
			}
		},
	)
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
	epoch := t.invalidate(t.daemon.ID)
	t.epoch.Store(epoch)
	t.hub.BroadcastBuild(func(cursor uint64) Event {
		return Event{
			Type: "kata.tasks.invalidated",
			Data: kataFrontendEventFrame{
				ServerInstanceID: t.serverID,
				DaemonID:         t.daemon.ID,
				Epoch:            epoch,
				Cursor:           cursor,
			},
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
	for {
		limit := int64(100)
		response, err := client.PollEventsWithResponse(ctx, &katagenerated.PollEventsRequestOptions{
			Query: &katagenerated.PollEventsQuery{AfterID: &afterID, Limit: &limit},
		})
		if err != nil {
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
			if dirty {
				t.queueInvalidation()
			}
			return afterID, nil
		}
		dirty = true
		lastID := body.Events[len(body.Events)-1].EventID
		if body.NextAfterID != lastID || lastID <= afterID {
			return afterID, errors.New("kata event catch-up cursor did not advance to the last event")
		}
		afterID = lastID
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
		target.cancel()
		target.hub.Close()
	}
	r.mu.Unlock()
	r.wg.Wait()
}
