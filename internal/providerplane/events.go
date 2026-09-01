package providerplane

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"go.kenn.io/forge/internal/federationauth"
)

const (
	hubEventsPath = "/api/v1/federation/events"
	// FederationReplayCompleteComment is the SSE comment that separates
	// hub replay from the live stream without expanding its event
	// vocabulary.
	FederationReplayCompleteComment = "kenn-forge-federation-replay-complete"
	maxEventLineBytes               = 256 << 10
	maxEventDataBytes               = 1 << 20
	maxEventErrorBytes              = 64 << 10
	maxEventRetryDelay              = 4 * time.Second
)

var ErrEventProtocol = errors.New("invalid hub event stream")

var hubProviderEventTypes = map[string]struct{}{
	"data_changed":             {},
	"sync_status":              {},
	"pr_detail_refreshed":      {},
	"pr_ci_refresh_queued":     {},
	"pr_ci_refreshed":          {},
	"deferred_merge_completed": {},
}

// IsHubProviderEvent reports whether an event is provider-owned and
// may cross the hub-to-spoke event boundary. Workspace, runtime, Docs,
// Kata, and config events are deliberately absent.
func IsHubProviderEvent(eventType string) bool {
	_, ok := hubProviderEventTypes[eventType]
	return ok
}

// Event is one decoded hub event. ID is private federation cursor
// state; callers must assign a new ID before publishing the event locally.
type Event struct {
	ID   uint64
	Type string
	Data json.RawMessage
}

// EventClientOptions defines the spoke-side consequences of one hub
// stream. OnResync must refresh provider state before the connection is
// reported healthy.
type EventClientOptions struct {
	Client              Client
	OnEvent             func(context.Context, Event) error
	OnResync            func(context.Context) error
	OnConnectionChanged func(bool)
	RetryDelay          func(failureCount int) time.Duration
	Wait                func(context.Context, time.Duration) error
}

// EventClient consumes the hub's private SSE stream and retains its
// remote cursor without exposing that cursor to the spoke's local EventHub.
type EventClient struct {
	client              Client
	onEvent             func(context.Context, Event) error
	onResync            func(context.Context) error
	onConnectionChanged func(bool)
	retryDelay          func(int) time.Duration
	wait                func(context.Context, time.Duration) error
	connected           bool
	connectionKnown     bool
}

func NewEventClient(options EventClientOptions) (*EventClient, error) {
	if options.Client == nil {
		return nil, errors.New("hub event client is required")
	}
	retryDelay := options.RetryDelay
	if retryDelay == nil {
		retryDelay = jitteredEventRetryDelay
	}
	wait := options.Wait
	if wait == nil {
		wait = waitForEventRetry
	}
	return &EventClient{
		client: options.Client, onEvent: options.OnEvent,
		onResync:            options.OnResync,
		onConnectionChanged: options.OnConnectionChanged,
		retryDelay:          retryDelay, wait: wait,
	}, nil
}

// Run reconnects until ctx is cancelled. Every failure, including an
// authorization or protocol failure, passes through the bounded backoff.
func (c *EventClient) Run(ctx context.Context) {
	failures := 0
	cursor := uint64(0)
	for ctx.Err() == nil {
		opened, err := c.runOnce(ctx, &cursor)
		if ctx.Err() != nil {
			break
		}
		if opened {
			failures = 0
		}
		if err == nil {
			err = io.ErrUnexpectedEOF
		}
		c.setConnection(false)
		failures++
		delay := c.retryDelay(failures)
		slog.Debug("retrying hub event stream", "next", delay, "err", err)
		if waitErr := c.wait(ctx, delay); waitErr != nil {
			break
		}
	}
	c.setConnection(false)
}

func (c *EventClient) runOnce(ctx context.Context, cursor *uint64) (bool, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, hubEventsPath, nil)
	if err != nil {
		return false, err
	}
	if *cursor > 0 {
		request.Header.Set("Last-Event-ID", strconv.FormatUint(*cursor, 10))
	}
	response, err := c.client.Do(ctx, federationauth.ScopeEventsRead, request)
	if err != nil {
		return false, err
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		defer response.Body.Close()
		body, readErr := io.ReadAll(io.LimitReader(response.Body, maxEventErrorBytes+1))
		if readErr != nil {
			return false, fmt.Errorf("read hub event error: %w", readErr)
		}
		if len(body) > maxEventErrorBytes {
			return false, fmt.Errorf("%w: hub error body exceeds limit", ErrEventProtocol)
		}
		return false, &ResponseError{
			Status: response.StatusCode, Header: response.Header.Clone(), Body: body,
		}
	}
	defer response.Body.Close()
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "text/event-stream" {
		return false, fmt.Errorf("%w: expected text/event-stream", ErrEventProtocol)
	}
	return c.readStream(ctx, response.Body, cursor)
}

func (c *EventClient) readStream(
	ctx context.Context, body io.Reader, cursor *uint64,
) (bool, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 4096), maxEventLineBytes)
	frame := eventFrameBuilder{}
	ready := false
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" {
			if err := c.consumeFrame(ctx, frame, cursor, ready); err != nil {
				return ready, err
			}
			frame = eventFrameBuilder{}
			continue
		}
		if comment, ok := strings.CutPrefix(line, ":"); ok {
			comment = strings.TrimSpace(comment)
			if comment == FederationReplayCompleteComment && !ready {
				if err := c.resynchronize(ctx); err != nil {
					return false, err
				}
				ready = true
			}
			continue
		}
		field, value, found := strings.Cut(line, ":")
		if found {
			value = strings.TrimPrefix(value, " ")
		}
		switch field {
		case "id":
			frame.id = value
		case "event":
			frame.eventType = value
		case "data":
			if frame.data.Len()+len(value)+1 > maxEventDataBytes {
				return ready, fmt.Errorf("%w: event data exceeds limit", ErrEventProtocol)
			}
			if frame.hasData {
				frame.data.WriteByte('\n')
			}
			frame.data.WriteString(value)
			frame.hasData = true
		}
	}
	if err := scanner.Err(); err != nil {
		return ready, fmt.Errorf("%w: read event stream: %v", ErrEventProtocol, err)
	}
	if !ready {
		return false, fmt.Errorf(
			"%w: replay completion marker is missing", ErrEventProtocol,
		)
	}
	return true, io.ErrUnexpectedEOF
}

type eventFrameBuilder struct {
	id        string
	eventType string
	data      strings.Builder
	hasData   bool
}

func (c *EventClient) consumeFrame(
	ctx context.Context, frame eventFrameBuilder, cursor *uint64, ready bool,
) error {
	if frame.eventType == "" && !frame.hasData && frame.id == "" {
		return nil
	}
	id, err := strconv.ParseUint(strings.TrimSpace(frame.id), 10, 64)
	if err != nil || id == 0 {
		return fmt.Errorf("%w: event id is not a positive integer", ErrEventProtocol)
	}
	payload := json.RawMessage(frame.data.String())
	if frame.eventType == "reconnect.stale" {
		if !json.Valid(payload) {
			return fmt.Errorf("%w: stale event payload is not JSON", ErrEventProtocol)
		}
		// A restarted hub begins a new event-ID lifetime. Its stale
		// marker can therefore be lower than the spoke's previous private
		// cursor and must replace, rather than compare against, that cursor.
		*cursor = id
		if !ready {
			return nil
		}
		return c.resynchronize(ctx)
	}
	if id <= *cursor {
		return nil
	}
	if !IsHubProviderEvent(frame.eventType) || !json.Valid(payload) {
		// A numeric cursor lets the spoke skip a poison frame safely. Refreshing
		// authoritative state recovers the consequence the frame may represent.
		*cursor = id
		if !ready {
			return nil
		}
		return c.resynchronize(ctx)
	}
	if c.onEvent != nil {
		if err := c.onEvent(ctx, Event{ID: id, Type: frame.eventType, Data: payload}); err != nil {
			return err
		}
	}
	*cursor = id
	return nil
}

func (c *EventClient) resynchronize(ctx context.Context) error {
	if c.connectionKnown {
		c.setConnection(false)
	}
	if c.onResync != nil {
		if err := c.onResync(ctx); err != nil {
			return err
		}
	}
	c.setConnection(true)
	return nil
}

func (c *EventClient) setConnection(connected bool) {
	if c.connectionKnown && c.connected == connected {
		return
	}
	c.connectionKnown = true
	c.connected = connected
	if c.onConnectionChanged != nil {
		c.onConnectionChanged(connected)
	}
}

func jitteredEventRetryDelay(failureCount int) time.Duration {
	if failureCount < 1 {
		failureCount = 1
	}
	delay := time.Second
	for range failureCount - 1 {
		if delay >= maxEventRetryDelay {
			break
		}
		delay *= 2
	}
	if delay > maxEventRetryDelay {
		delay = maxEventRetryDelay
	}
	// Symmetric 20% jitter prevents a fleet from reconnecting in lockstep.
	spread := delay / 5
	jittered := delay - spread + time.Duration(rand.Int64N(int64(spread*2)+1))
	if jittered > maxEventRetryDelay {
		return maxEventRetryDelay
	}
	return jittered
}

func waitForEventRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
