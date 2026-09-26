package syncevents

import (
	"context"
	"io"

	"go.kenn.io/forge/internal/apiclient/generated"
	"go.kenn.io/forge/internal/federationauth"
	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/providerplane"
	"go.kenn.io/forge/internal/server/authapi"
	"go.kenn.io/forge/internal/server/spokeapi"
)

const MaxFederationCursorLength = 32

func NewHubEventLifecycle(
	enabled bool, run func(context.Context),
) *spokeapi.HubEventLifecycle {
	return newHubEventLifecycleWithRestart(enabled, run, true)
}

func NewHubEventLifecycleStoppingOnCleanReturn(
	enabled bool, run func(context.Context),
) *spokeapi.HubEventLifecycle {
	return newHubEventLifecycleWithRestart(enabled, run, false)
}

func newHubEventLifecycleWithRestart(
	enabled bool, run func(context.Context), restartOnCleanReturn bool,
) *spokeapi.HubEventLifecycle {
	return &spokeapi.HubEventLifecycle{
		RunFunc: run, RestartOnCleanReturn: restartOnCleanReturn,
		Enabled: enabled, Changed: make(chan struct{}),
	}
}

type HubConnectionState struct {
	Connected bool `json:"connected"`
}

type ReconnectStaleState struct {
	HubConnected *bool `json:"hub_connected,omitempty"`
}

type FederationEventsInput struct {
	Protocol      string `header:"X-Kenn-Forge-Federation-Protocol"`
	LastEventID   string `header:"Last-Event-ID"`
	ContentLength string `header:"Content-Length"`
	Since         string `query:"since"`
}

func (s *Handlers) TrackFederationEventStream(
	parent context.Context, nodeID string,
) (context.Context, func()) {
	ctx, cancel := context.WithCancel(parent)
	s.FederationStreamsMu.Lock()
	streamID := (*s.FederationStreamsNext)
	(*s.FederationStreamsNext)++
	if (*s.FederationStreams) == nil {
		(*s.FederationStreams) = make(map[string]map[uint64]context.CancelFunc)
	}
	streams := (*s.FederationStreams)[nodeID]
	if streams == nil {
		streams = make(map[uint64]context.CancelFunc)
		(*s.FederationStreams)[nodeID] = streams
	}
	streams[streamID] = cancel
	s.FederationStreamsMu.Unlock()

	return ctx, func() {
		cancel()
		s.FederationStreamsMu.Lock()
		delete((*s.FederationStreams)[nodeID], streamID)
		if len((*s.FederationStreams)[nodeID]) == 0 {
			delete((*s.FederationStreams), nodeID)
		}
		s.FederationStreamsMu.Unlock()
	}
}

func (s *Handlers) CancelFederationEventStreams(nodeID string) {
	s.FederationStreamsMu.Lock()
	streams := (*s.FederationStreams)[nodeID]
	delete((*s.FederationStreams), nodeID)
	s.FederationStreamsMu.Unlock()
	for _, cancel := range streams {
		cancel()
	}
}

func WriteFederationReplayComplete(w io.Writer, rc authapi.SseController) bool {
	if _, err := io.WriteString(
		w, ": "+providerplane.FederationReplayCompleteComment+"\n\n",
	); err != nil {
		return false
	}
	return rc.Flush() == nil
}

func (s *Handlers) ReceiveHubEvent(
	_ context.Context, event providerplane.Event,
) error {
	if !s.FederationEnabled() {
		return providerplane.ErrHubUnavailable
	}
	// Hub IDs belong only to the private inbound cursor. Ordinary
	// Broadcast assigns a fresh spoke-local ID for the browser's one cursor.
	(*s.Hub).Broadcast(Event{Type: event.Type, Data: event.Data})
	return nil
}

func (s *Handlers) ResynchronizeHubProviderState(ctx context.Context) error {
	if !s.FederationEnabled() {
		return providerplane.ErrHubUnavailable
	}
	(*s.Hub).Broadcast(Event{Type: "data_changed", Data: struct{}{}})
	if (*s.ProviderSource) == nil || (*s.ProviderSource).Client == nil {
		return providerplane.ErrHubUnavailable
	}
	request, err := generated.NewGetSyncStatusRequest(ctx, "/api/v1")
	if err != nil {
		return err
	}
	var status ghclient.SyncStatus
	if err := providerplane.ReadJSON(
		ctx, (*s.ProviderSource).Client, federationauth.ScopeProviderRead,
		request, &status,
	); err != nil {
		return err
	}
	(*s.Hub).Broadcast(Event{Type: "sync_status", Data: status})
	return nil
}

func (s *Handlers) BroadcastHubConnection(connected bool) {
	(*s.Hub).Broadcast(Event{
		Type: "hub_connection_changed",
		Data: HubConnectionState{Connected: connected},
	})
}

func (s *Handlers) ReconnectStaleEvent() Event {
	data := ReconnectStaleState{}
	if event, ok := (*s.Hub).LatestHubConnection(); ok {
		if state, valid := event.Data.(HubConnectionState); valid {
			connected := state.Connected
			data.HubConnected = &connected
		}
	}
	return Event{Type: "reconnect.stale", Data: data}
}
