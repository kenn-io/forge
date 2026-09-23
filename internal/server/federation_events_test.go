package server

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/federationauth"
	"go.kenn.io/forge/internal/providerplane"
	"go.kenn.io/forge/internal/server/authapi"
	"go.kenn.io/forge/internal/server/syncevents"
	"go.kenn.io/forge/internal/testutil/dbtest"
	serverfake "go.kenn.io/forge/internal/testutil/serverfake"
)

type testSSEFrame struct {
	ID   string
	Type string
	Data string
}

type testFederationEventStream struct {
	response *http.Response
	frames   <-chan testSSEFrame
}

func TestReconnectStaleEventCarriesHubConnection(t *testing.T) {
	hub := syncevents.NewEventHubWithCapacity(4)
	t.Cleanup(hub.Close)
	server := wiredServer(&Server{hub: hub})

	for i := 1; i <= 10; i++ {
		hub.Broadcast(syncevents.Event{Type: "data_changed", Data: i})
	}
	server.syncevents.BroadcastHubConnection(false)

	event := server.syncevents.ReconnectStaleEvent()
	state, ok := event.Data.(syncevents.ReconnectStaleState)
	require.True(t, ok)
	require.NotNil(t, state.HubConnected)
	assert.False(t, *state.HubConnected)
}

func TestFederationEventEndpointReplaysFilteredEventsAndSignalsStale(t *testing.T) {
	assert := assert.New(t)
	server, httpServer, token := newFederationEventServer(t)
	server.Hub().Broadcast(syncevents.Event{Type: "workspace_created", Data: struct{}{}})
	firstProviderID := server.Hub().Broadcast(syncevents.Event{Type: "data_changed", Data: struct{}{}})
	secondProviderID := server.Hub().Broadcast(syncevents.Event{
		Type: "sync_status", Data: map[string]bool{"running": true},
	})

	replay := openFederationEventStream(t, httpServer, token, "1")
	first := nextTestSSEFrame(t, replay.frames)
	second := nextTestSSEFrame(t, replay.frames)
	assert.Equal(firstProviderID, mustEventID(t, first.ID))
	assert.Equal(secondProviderID, mustEventID(t, second.ID))
	replay.response.Body.Close()

	server.hub.Close()
	server.hub = syncevents.NewEventHubWithCapacity(2)
	server.Hub().Broadcast(syncevents.Event{Type: "data_changed", Data: struct{}{}})
	server.Hub().Broadcast(syncevents.Event{Type: "sync_status", Data: struct{}{}})
	server.Hub().Broadcast(syncevents.Event{Type: "pr_ci_refreshed", Data: struct{}{}})
	stale := openFederationEventStream(t, httpServer, token, "0")
	defer stale.response.Body.Close()
	frame := nextTestSSEFrame(t, stale.frames)
	assert.Equal("reconnect.stale", frame.Type)
	assert.JSONEq(`{}`, frame.Data)
}

func TestFederationEventEndpointEnforcesCredentialProtocolAndRequestBounds(t *testing.T) {
	server, httpServer, token := newFederationEventServer(t)
	wrongScopeNodeID := "88888888888888888888888888888888"
	wrongScopeToken, err := server.options.FederationCredentials.MintInbound(
		wrongScopeNodeID, []federationauth.Scope{federationauth.ScopeProviderRead},
	)
	require.NoError(t, err)

	tests := []struct {
		name       string
		token      string
		nodeID     string
		protocol   string
		cursor     string
		body       io.Reader
		wantStatus int
	}{
		{name: "missing credential", protocol: providerplane.ProtocolVersionHeaderValue(), wantStatus: http.StatusUnauthorized},
		{name: "wrong scope", token: wrongScopeToken, nodeID: wrongScopeNodeID, protocol: providerplane.ProtocolVersionHeaderValue(), wantStatus: http.StatusForbidden},
		{name: "wrong protocol", token: token, protocol: "999", wantStatus: http.StatusConflict},
		{name: "oversized cursor", token: token, protocol: providerplane.ProtocolVersionHeaderValue(), cursor: strings.Repeat("9", syncevents.MaxFederationCursorLength+1), wantStatus: http.StatusBadRequest},
		{name: "request body", token: token, protocol: providerplane.ProtocolVersionHeaderValue(), body: strings.NewReader("not allowed"), wantStatus: http.StatusBadRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request, err := http.NewRequestWithContext(t.Context(),
				http.MethodGet, httpServer.URL+"/api/v1/federation/events", test.body,
			)
			require.NoError(t, err)
			if test.token != "" {
				request.Header.Set("Authorization", "Bearer "+test.token)
				nodeID := test.nodeID
				if nodeID == "" {
					nodeID = serverfake.FederationEventTestNodeID
				}
				request.Header.Set(federationauth.NodeIDHeader, nodeID)
			}
			request.Header.Set(providerplane.ProtocolVersionHeader, test.protocol)
			request.Header.Set("Last-Event-ID", test.cursor)
			response, err := httpServer.Client().Do(request)
			require.NoError(t, err)
			defer response.Body.Close()
			assert.Equal(t, test.wantStatus, response.StatusCode)
		})
	}
}

func TestFederationEventCredentialRevocationAppliesToNextConnection(t *testing.T) {
	require := require.New(t)
	server, httpServer, token := newFederationEventServer(t)
	stream := openFederationEventStream(t, httpServer, token, "")
	stream.response.Body.Close()
	require.Eventually(func() bool {
		return server.SubscriberCount() == 0
	}, time.Second, time.Millisecond)

	require.NoError(server.options.FederationCredentials.RevokeInbound(token))
	request, err := federationEventRequest(t, httpServer.URL, token, "")
	require.NoError(err)
	response, err := httpServer.Client().Do(request)
	require.NoError(err)
	defer response.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, response.StatusCode)
}

func TestHubEventReceiveAssignsFreshLocalIDs(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	server := newTestServer(t)
	for range 7 {
		server.Hub().Broadcast(syncevents.Event{Type: "workspace_status", Data: struct{}{}})
	}
	stream, _ := server.Hub().Subscribe(t.Context(), false)

	require.NoError(server.syncevents.ReceiveHubEvent(t.Context(), providerplane.Event{
		ID: 40, Type: "data_changed", Data: []byte(`{}`),
	}))
	require.NoError(server.syncevents.ReceiveHubEvent(t.Context(), providerplane.Event{
		ID: 900, Type: "pr_detail_refreshed", Data: []byte(`{"number":1}`),
	}))

	first := <-stream
	second := <-stream
	assert.Equal([]uint64{8, 9}, []uint64{first.ID, second.ID})
	assert.Equal([]string{"data_changed", "pr_detail_refreshed"}, []string{
		first.Event.Type, second.Event.Type,
	})
}

func TestHubEventsStopWhileFleetIsDisabled(t *testing.T) {
	server := newTestServer(t)
	server.cfg = &config.Config{Fleet: config.Fleet{Enabled: false}}
	before := server.Hub().Generation()

	err := server.syncevents.ReceiveHubEvent(t.Context(), providerplane.Event{
		ID: 40, Type: "data_changed", Data: []byte(`{}`),
	})
	require.ErrorIs(t, err, providerplane.ErrHubUnavailable)
	assert.Equal(t, before, server.Hub().Generation())
}

func TestNodeStreamsHubEventsWithNodeLocalCursorIDs(t *testing.T) {
	require := require.New(t)
	hubID := "66666666666666666666666666666666"
	nodeID := "77777777777777777777777777777777"
	hubCredentials, err := federationauth.Open(t.TempDir() + "/hub-credentials.json")
	require.NoError(err)
	token, err := hubCredentials.MintInbound(nodeID, federationauth.SpokeToHubScopes())
	require.NoError(err)
	hub := New(dbtest.Open(t), nil, nil, "/", nil, ServerOptions{
		DaemonAccess:      authapi.DaemonAccessOptions{Token: "hub-secret", RequireAPIAuth: true},
		FederationSpokeID: hubID, FederationCredentials: hubCredentials,
		DisableWorkspaceBackgroundMonitors: true,
	})
	t.Cleanup(func() { serverfake.GracefulShutdown(t, hub) })
	hubHTTP := httptest.NewTLSServer(hub)
	t.Cleanup(hubHTTP.Close)
	for range 40 {
		hub.Hub().Broadcast(syncevents.Event{Type: "workspace_status", Data: struct{}{}})
	}

	spokeCredentials, err := federationauth.Open(t.TempDir() + "/spoke-credentials.json")
	require.NoError(err)
	require.NoError(spokeCredentials.StoreOutbound(
		hubID, token, federationauth.SpokeToHubScopes(),
	))
	spoke := New(dbtest.Open(t), nil, nil, "/", &config.Config{Fleet: config.Fleet{
		Enabled: true, Role: config.FleetRoleSpoke,
		Hub: &config.FleetHub{NodeID: hubID, BaseURL: hubHTTP.URL},
	}}, ServerOptions{
		FederationSpokeID: nodeID, FederationCredentials: spokeCredentials,
		FederationSpokeActive: true,
		FederationHTTPClient:  hubHTTP.Client(), DisableWorkspaceBackgroundMonitors: true,
	})
	t.Cleanup(func() { serverfake.GracefulShutdown(t, spoke) })

	require.Eventually(func() bool {
		records, _ := spoke.Hub().RingSnapshotSince(0)
		for _, record := range records {
			if record.Event.Type != "hub_connection_changed" {
				continue
			}
			state, ok := record.Event.Data.(syncevents.HubConnectionState)
			if ok && state.Connected {
				return true
			}
		}
		return false
	}, 2*time.Second, 5*time.Millisecond)
	localFloor := spoke.Hub().Generation()
	remoteID := hub.Hub().Broadcast(syncevents.Event{Type: "data_changed", Data: struct{}{}})
	require.Greater(remoteID, localFloor+1)

	require.Eventually(func() bool {
		records, stale := spoke.Hub().RingSnapshotSince(localFloor)
		return !stale && len(records) == 1 && records[0].Event.Type == "data_changed"
	}, 2*time.Second, 5*time.Millisecond)
	records, stale := spoke.Hub().RingSnapshotSince(localFloor)
	require.False(stale)
	require.Len(records, 1)
	assert.Equal(t, localFloor+1, records[0].ID)

	spoke.cfgMu.Lock()
	spoke.cfg.Fleet.Enabled = false
	spoke.streamapi.ApplyFleetConfigLocked()
	spoke.cfgMu.Unlock()
	require.Eventually(func() bool {
		return hub.SubscriberCount() == 0
	}, time.Second, 5*time.Millisecond)
	disabledFloor := spoke.Hub().Generation()
	hub.Hub().Broadcast(syncevents.Event{
		Type: "pr_detail_refreshed", Data: map[string]int{"number": 7},
	})
	assert.Never(t, func() bool {
		records, _ := spoke.Hub().RingSnapshotSince(disabledFloor)
		return slices.ContainsFunc(records, func(record syncevents.RecordedEvent) bool {
			return record.Event.Type == "pr_detail_refreshed"
		})
	}, 50*time.Millisecond, 5*time.Millisecond)

	spoke.cfgMu.Lock()
	spoke.cfg.Fleet.Enabled = true
	spoke.streamapi.ApplyFleetConfigLocked()
	spoke.cfgMu.Unlock()
	require.Eventually(func() bool {
		records, _ := spoke.Hub().RingSnapshotSince(disabledFloor)
		return slices.ContainsFunc(records, func(record syncevents.RecordedEvent) bool {
			return record.Event.Type == "data_changed"
		})
	}, 2*time.Second, 5*time.Millisecond)
}

func newFederationEventServer(
	t *testing.T,
) (*Server, *httptest.Server, string) {
	t.Helper()
	credentials, err := federationauth.Open(t.TempDir() + "/credentials.json")
	require.NoError(t, err)
	token, err := credentials.MintInbound(
		serverfake.FederationEventTestNodeID, federationauth.SpokeToHubScopes(),
	)
	require.NoError(t, err)
	server := New(dbtest.Open(t), nil, nil, "/", nil, ServerOptions{
		DaemonAccess: authapi.DaemonAccessOptions{
			Token: "local-daemon-secret", RequireAPIAuth: true,
		},
		FederationCredentials:              credentials,
		DisableWorkspaceBackgroundMonitors: true,
	})
	t.Cleanup(func() { serverfake.GracefulShutdown(t, server) })
	httpServer := httptest.NewServer(server)
	t.Cleanup(httpServer.Close)
	return server, httpServer, token
}

func openFederationEventStream(
	t *testing.T, server *httptest.Server, token, cursor string,
) testFederationEventStream {
	t.Helper()
	request, err := federationEventRequest(t, server.URL, token, cursor)
	require.NoError(t, err)
	response, err := server.Client().Do(request)
	require.NoError(t, err)
	t.Cleanup(func() {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
	})
	require.Equal(t, http.StatusOK, response.StatusCode)
	require.Equal(t, "text/event-stream", response.Header.Get("Content-Type"))
	frames := make(chan testSSEFrame, 8)
	go scanTestSSEFrames(response.Body, frames)
	return testFederationEventStream{response: response, frames: frames}
}

func federationEventRequest(t *testing.T, baseURL, token, cursor string) (*http.Request, error) {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(),
		http.MethodGet, baseURL+"/api/v1/federation/events", nil,
	)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set(federationauth.NodeIDHeader, serverfake.FederationEventTestNodeID)
	request.Header.Set(providerplane.ProtocolVersionHeader, providerplane.ProtocolVersionHeaderValue())
	if cursor != "" {
		request.Header.Set("Last-Event-ID", cursor)
	}
	return request, nil
}

func scanTestSSEFrames(body io.Reader, frames chan<- testSSEFrame) {
	defer close(frames)
	scanner := bufio.NewScanner(body)
	frame := testSSEFrame{}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if frame.Type != "" {
				frames <- frame
			}
			frame = testSSEFrame{}
			continue
		}
		if value, ok := strings.CutPrefix(line, "id: "); ok {
			frame.ID = value
		}
		if value, ok := strings.CutPrefix(line, "event: "); ok {
			frame.Type = value
		}
		if value, ok := strings.CutPrefix(line, "data: "); ok {
			frame.Data = value
		}
	}
}

func nextTestSSEFrame(t *testing.T, frames <-chan testSSEFrame) testSSEFrame {
	t.Helper()
	select {
	case frame, ok := <-frames:
		require.True(t, ok, "event stream closed before a frame arrived")
		return frame
	case <-time.After(time.Second):
		require.FailNow(t, "timed out waiting for federation event")
		return testSSEFrame{}
	}
}

func mustEventID(t *testing.T, raw string) uint64 {
	t.Helper()
	var id uint64
	_, err := fmt.Sscan(raw, &id)
	require.NoError(t, err)
	return id
}
