package server

import (
	"context"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"go.kenn.io/forge/internal/federationauth"
	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/providerplane"
	"go.kenn.io/forge/internal/server/httpapi"
)

const maxFederationCursorLength = 32

type hubEventLifecycle struct {
	mu           sync.Mutex
	run          func(context.Context)
	enabled      bool
	changed      chan struct{}
	activeCancel context.CancelFunc
}

func newHubEventLifecycle(
	enabled bool, run func(context.Context),
) *hubEventLifecycle {
	return &hubEventLifecycle{
		run: run, enabled: enabled, changed: make(chan struct{}),
	}
}

func (l *hubEventLifecycle) SetEnabled(enabled bool) {
	if l == nil {
		return
	}
	l.mu.Lock()
	if l.enabled == enabled {
		l.mu.Unlock()
		return
	}
	l.enabled = enabled
	close(l.changed)
	l.changed = make(chan struct{})
	cancel := l.activeCancel
	l.mu.Unlock()
	if !enabled && cancel != nil {
		cancel()
	}
}

func (l *hubEventLifecycle) Run(ctx context.Context) {
	for ctx.Err() == nil {
		l.mu.Lock()
		enabled := l.enabled
		changed := l.changed
		var runCtx context.Context
		if enabled {
			var cancel context.CancelFunc
			runCtx, cancel = context.WithCancel(ctx)
			l.activeCancel = cancel
		}
		l.mu.Unlock()

		if !enabled {
			select {
			case <-ctx.Done():
				return
			case <-changed:
				continue
			}
		}
		l.run(runCtx)
		l.mu.Lock()
		l.activeCancel = nil
		l.mu.Unlock()
	}
}

type hubConnectionState struct {
	Connected bool `json:"connected"`
}

type reconnectStaleState struct {
	HubConnected *bool `json:"hub_connected,omitempty"`
}

type federationEventsInput struct {
	Protocol      string `header:"X-Kenn-Forge-Federation-Protocol"`
	LastEventID   string `header:"Last-Event-ID"`
	ContentLength string `header:"Content-Length"`
	Since         string `query:"since"`
}

func (s *Server) registerFederationEventAPI(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "stream-federation-provider-events",
		Method:      http.MethodGet,
		Path:        "/federation/events",
		Summary:     "Stream hub-owned provider events",
		Tags:        []string{"Fleet"},
		Responses: map[string]*huma.Response{
			"200": {
				Description: "Filtered provider event stream",
				Content: map[string]*huma.MediaType{
					"text/event-stream": {},
				},
			},
		},
	}, s.streamFederationEvents)
}

func (s *Server) streamFederationEvents(
	requestContext context.Context, input *federationEventsInput,
) (*huma.StreamResponse, error) {
	if input.Protocol != providerplane.ProtocolVersionHeaderValue() {
		return nil, httpapi.Conflict(
			httpapi.CodeConflict,
			"federation protocol version does not match",
			map[string]any{"reason": "protocolMismatch"},
		)
	}
	if len(input.LastEventID) > maxFederationCursorLength ||
		len(input.Since) > maxFederationCursorLength {
		return nil, httpapi.BadRequest(
			httpapi.CodeValidationError,
			"federation event cursor exceeds its size limit",
			nil,
		)
	}
	if input.ContentLength != "" && input.ContentLength != "0" {
		return nil, httpapi.BadRequest(
			httpapi.CodeValidationError,
			"federation event requests must not contain a body",
			nil,
		)
	}
	if s.providerRouteSpoke {
		return nil, httpapi.ServiceUnavailable(
			"provider events are available only from the federation hub",
		)
	}
	streamContext := requestContext
	cleanup := func() {}
	if principal, ok := federationauth.PrincipalFromContext(requestContext); ok {
		streamContext, cleanup = s.trackFederationEventStream(
			requestContext, principal.NodeID,
		)
		if _, authorized := s.federationPrincipalEnrollmentState(principal); !authorized {
			cleanup()
			return nil, httpapi.Forbidden(
				"federation enrollment is no longer active",
				map[string]any{"reason": "federationEnrollmentInactive"},
			)
		}
	}
	return &huma.StreamResponse{
		Body: func(ctx huma.Context) {
			defer cleanup()
			ctx.SetHeader("Content-Type", "text/event-stream")
			ctx.SetHeader("Cache-Control", "no-cache")
			ctx.SetHeader("Connection", "keep-alive")

			r, w := humago.Unwrap(ctx)
			rc := http.NewResponseController(w)
			_ = rc.SetWriteDeadline(time.Time{})
			cursor, hasCursor := parseLastEventID(r)
			// The spoke performs an authoritative refresh at the replay barrier,
			// so injecting cached status afterward could overwrite that refresh.
			ch, done := s.hub.Subscribe(streamContext, false)
			serveSSESubscribedFromHubTransformed(
				streamContext, w, rc, s.hub, cursor, hasCursor, ch, done,
				func(uint64) Event {
					return Event{Type: "reconnect.stale", Data: struct{}{}}
				},
				func(record RecordedEvent) (RecordedEvent, bool) {
					return record, providerplane.IsHubProviderEvent(record.Event.Type)
				},
				writeFederationReplayComplete,
				nil,
			)
		},
	}, nil
}

func (s *Server) trackFederationEventStream(
	parent context.Context, nodeID string,
) (context.Context, func()) {
	ctx, cancel := context.WithCancel(parent)
	s.federationStreamsMu.Lock()
	streamID := s.federationStreamsNext
	s.federationStreamsNext++
	if s.federationStreams == nil {
		s.federationStreams = make(map[string]map[uint64]context.CancelFunc)
	}
	streams := s.federationStreams[nodeID]
	if streams == nil {
		streams = make(map[uint64]context.CancelFunc)
		s.federationStreams[nodeID] = streams
	}
	streams[streamID] = cancel
	s.federationStreamsMu.Unlock()

	return ctx, func() {
		cancel()
		s.federationStreamsMu.Lock()
		delete(s.federationStreams[nodeID], streamID)
		if len(s.federationStreams[nodeID]) == 0 {
			delete(s.federationStreams, nodeID)
		}
		s.federationStreamsMu.Unlock()
	}
}

func (s *Server) cancelFederationEventStreams(nodeID string) {
	s.federationStreamsMu.Lock()
	streams := s.federationStreams[nodeID]
	delete(s.federationStreams, nodeID)
	s.federationStreamsMu.Unlock()
	for _, cancel := range streams {
		cancel()
	}
}

func writeFederationReplayComplete(w io.Writer, rc sseController) bool {
	if _, err := io.WriteString(
		w, ": "+providerplane.FederationReplayCompleteComment+"\n\n",
	); err != nil {
		return false
	}
	return rc.Flush() == nil
}

func (s *Server) receiveHubEvent(
	_ context.Context, event providerplane.Event,
) error {
	if !s.federationEnabled() {
		return providerplane.ErrHubUnavailable
	}
	// Hub IDs belong only to the private inbound cursor. Ordinary
	// Broadcast assigns a fresh spoke-local ID for the browser's one cursor.
	s.hub.Broadcast(Event{Type: event.Type, Data: event.Data})
	return nil
}

func (s *Server) resynchronizeHubProviderState(ctx context.Context) error {
	if !s.federationEnabled() {
		return providerplane.ErrHubUnavailable
	}
	s.hub.Broadcast(Event{Type: "data_changed", Data: struct{}{}})
	if s.providerSource == nil || s.providerSource.client == nil {
		return providerplane.ErrHubUnavailable
	}
	request, err := http.NewRequestWithContext(
		ctx, http.MethodGet, "/api/v1/sync/status", nil,
	)
	if err != nil {
		return err
	}
	var status ghclient.SyncStatus
	if err := providerplane.ReadJSON(
		ctx, s.providerSource.client, federationauth.ScopeProviderRead,
		request, &status,
	); err != nil {
		return err
	}
	s.hub.Broadcast(Event{Type: "sync_status", Data: status})
	return nil
}

func (s *Server) broadcastHubConnection(connected bool) {
	s.hub.Broadcast(Event{
		Type: "hub_connection_changed",
		Data: hubConnectionState{Connected: connected},
	})
}

func (s *Server) reconnectStaleEvent() Event {
	data := reconnectStaleState{}
	if event, ok := s.hub.LatestHubConnection(); ok {
		if state, valid := event.Data.(hubConnectionState); valid {
			connected := state.Connected
			data.HubConnected = &connected
		}
	}
	return Event{Type: "reconnect.stale", Data: data}
}
