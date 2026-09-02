package server

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/federation"
	"go.kenn.io/forge/internal/federationauth"
	"go.kenn.io/forge/internal/providerplane"
	"go.kenn.io/forge/internal/testutil/dbtest"
)

const federationEventTestNodeID = "55555555555555555555555555555555"

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
	hub := NewEventHubWithCapacity(4)
	t.Cleanup(hub.Close)
	server := &Server{hub: hub}

	for i := 1; i <= 10; i++ {
		hub.Broadcast(Event{Type: "data_changed", Data: i})
	}
	server.broadcastHubConnection(false)

	event := server.reconnectStaleEvent()
	state, ok := event.Data.(reconnectStaleState)
	require.True(t, ok)
	require.NotNil(t, state.HubConnected)
	assert.False(t, *state.HubConnected)
}

func TestFederationEventEndpointFiltersNodeLocalEvents(t *testing.T) {
	server, httpServer, token := newFederationEventServer(t)
	// A fresh spoke refreshes sync status at the replay barrier, so the
	// hub must not inject an older cached value after that barrier.
	server.Hub().Broadcast(Event{Type: "sync_status", Data: map[string]bool{"running": true}})
	stream := openFederationEventStream(t, httpServer, token, "")
	defer stream.response.Body.Close()

	server.Hub().Broadcast(Event{Type: "workspace_created", Data: map[string]string{"id": "ws-1"}})
	server.Hub().Broadcast(Event{Type: "workspace_status", Data: map[string]string{"id": "ws-1"}})
	server.Hub().Broadcast(Event{Type: "config.changed", Data: map[string]bool{"valid": true}})
	providerID := server.Hub().Broadcast(Event{Type: "data_changed", Data: struct{}{}})

	frame := nextTestSSEFrame(t, stream.frames)
	assert.Equal(t, providerID, mustEventID(t, frame.ID))
	assert.Equal(t, "data_changed", frame.Type)
	assert.JSONEq(t, `{}`, frame.Data)
	assertNoTestSSEFrame(t, stream.frames)
}

func TestFederationEventEndpointReplaysFilteredEventsAndSignalsStale(t *testing.T) {
	assert := assert.New(t)
	server, httpServer, token := newFederationEventServer(t)
	server.Hub().Broadcast(Event{Type: "workspace_created", Data: struct{}{}})
	firstProviderID := server.Hub().Broadcast(Event{Type: "data_changed", Data: struct{}{}})
	secondProviderID := server.Hub().Broadcast(Event{
		Type: "sync_status", Data: map[string]bool{"running": true},
	})

	replay := openFederationEventStream(t, httpServer, token, "1")
	first := nextTestSSEFrame(t, replay.frames)
	second := nextTestSSEFrame(t, replay.frames)
	assert.Equal(firstProviderID, mustEventID(t, first.ID))
	assert.Equal(secondProviderID, mustEventID(t, second.ID))
	replay.response.Body.Close()

	server.hub.Close()
	server.hub = NewEventHubWithCapacity(2)
	server.Hub().Broadcast(Event{Type: "data_changed", Data: struct{}{}})
	server.Hub().Broadcast(Event{Type: "sync_status", Data: struct{}{}})
	server.Hub().Broadcast(Event{Type: "pr_ci_refreshed", Data: struct{}{}})
	stale := openFederationEventStream(t, httpServer, token, "0")
	defer stale.response.Body.Close()
	frame := nextTestSSEFrame(t, stale.frames)
	assert.Equal("reconnect.stale", frame.Type)
	assert.JSONEq(`{}`, frame.Data)
}

func TestFederationEventEndpointTreatsMalformedCursorAsFresh(t *testing.T) {
	server, httpServer, token := newFederationEventServer(t)
	stream := openFederationEventStream(t, httpServer, token, "not-a-number")
	defer stream.response.Body.Close()

	server.Hub().Broadcast(Event{Type: "pr_ci_refresh_queued", Data: struct{}{}})

	assert.Equal(t, "pr_ci_refresh_queued", nextTestSSEFrame(t, stream.frames).Type)
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
		{name: "oversized cursor", token: token, protocol: providerplane.ProtocolVersionHeaderValue(), cursor: strings.Repeat("9", maxFederationCursorLength+1), wantStatus: http.StatusBadRequest},
		{name: "request body", token: token, protocol: providerplane.ProtocolVersionHeaderValue(), body: strings.NewReader("not allowed"), wantStatus: http.StatusBadRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request, err := http.NewRequest(
				http.MethodGet, httpServer.URL+"/api/v1/federation/events", test.body,
			)
			require.NoError(t, err)
			if test.token != "" {
				request.Header.Set("Authorization", "Bearer "+test.token)
				nodeID := test.nodeID
				if nodeID == "" {
					nodeID = federationEventTestNodeID
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
	request, err := federationEventRequest(httpServer.URL, token, "")
	require.NoError(err)
	response, err := httpServer.Client().Do(request)
	require.NoError(err)
	defer response.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, response.StatusCode)
}

func TestEnrollmentRevocationClosesExistingFederationEventStream(t *testing.T) {
	require := require.New(t)
	const (
		hubID        = "0123456789abcdef0123456789abcdef"
		enrollmentID = "11111111111111111111111111111111"
	)
	spoke := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodDelete, r.Method)
		assert.Equal(t, "/api/v1/fleet/enrollments/"+enrollmentID, r.URL.Path)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(spoke.Close)
	dir := t.TempDir()
	enrollments, err := federation.Open(
		filepath.Join(dir, "enrollments.json"), federation.StoreOptions{},
	)
	require.NoError(err)
	oneTime, err := enrollments.CreateOneTimeToken(federation.Identity{
		NodeID: hubID, BaseURL: "https://hub.example",
	}, time.Now().Add(time.Minute))
	require.NoError(err)
	_, err = enrollments.Begin(t.Context(), oneTime.Token, federation.JoinRequest{
		EnrollmentID: enrollmentID, NodeID: federationEventTestNodeID,
		BaseURL: spoke.URL, Platform: "linux",
		ProtocolVersion: federation.ProtocolVersion,
		HubCredential:   "hub-credential",
	})
	require.NoError(err)
	require.NoError(enrollments.Activate(
		t.Context(), enrollmentID, time.Now().Add(time.Hour),
	))

	credentials, err := federationauth.Open(filepath.Join(dir, "credentials.json"))
	require.NoError(err)
	token, err := credentials.MintInbound(
		federationEventTestNodeID, federationauth.SpokeToHubScopes(),
	)
	require.NoError(err)
	require.NoError(credentials.StoreOutbound(
		federationEventTestNodeID, "hub-calls-spoke-token",
		federationauth.HubToSpokeScopes(),
	))
	cfg := &config.Config{
		Host: "127.0.0.1", Port: 8091, DataDir: dir, BasePath: "/",
		SyncInterval: "5m", Activity: config.Activity{ViewMode: "threaded", TimeRange: "7d"},
		API: config.API{RequireAuth: true}, Fleet: config.Fleet{
			Enabled: true, Role: config.FleetRoleHub,
			BaseURL: "https://hub.example",
			Members: []config.FleetMember{{
				NodeID: federationEventTestNodeID, BaseURL: spoke.URL,
				State: federation.EnrollmentActive,
			}},
		}}
	cfgPath := filepath.Join(dir, "config.toml")
	require.NoError(cfg.Save(cfgPath))
	server := NewWithConfig(dbtest.Open(t), nil, nil, nil, cfg, cfgPath, ServerOptions{
		DaemonAccess: DaemonAccessOptions{
			Token: "local-daemon-secret", RequireAPIAuth: true,
		},
		FederationCredentials:              credentials,
		FederationEnrollments:              enrollments,
		FederationSpokeID:                  hubID,
		FederationHTTPClient:               spoke.Client(),
		HostCheckAllowLoopbackAnyPort:      true,
		DisableWorkspaceBackgroundMonitors: true,
	})
	t.Cleanup(func() { gracefulShutdown(t, server) })
	httpServer := httptest.NewServer(server)
	t.Cleanup(httpServer.Close)
	stream := openFederationEventStream(t, httpServer, token, "")
	defer stream.response.Body.Close()

	request, err := http.NewRequest(
		http.MethodDelete,
		httpServer.URL+"/api/v1/fleet/enrollments/"+enrollmentID,
		nil,
	)
	require.NoError(err)
	request.Header.Set("Authorization", "Bearer local-daemon-secret")
	request.Header.Set("Content-Type", "application/json")
	response, err := httpServer.Client().Do(request)
	require.NoError(err)
	response.Body.Close()
	require.Equal(http.StatusNoContent, response.StatusCode)

	server.Hub().Broadcast(Event{Type: "data_changed", Data: struct{}{}})
	select {
	case _, open := <-stream.frames:
		require.False(open, "revoked spoke event stream remained open")
	case <-time.After(time.Second):
		require.FailNow("revoked spoke event stream did not close")
	}
}

func TestHubEventReceiveAssignsFreshLocalIDs(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	server := newTestServer(t)
	for range 7 {
		server.Hub().Broadcast(Event{Type: "workspace_status", Data: struct{}{}})
	}
	stream, _ := server.Hub().Subscribe(t.Context(), false)

	require.NoError(server.receiveHubEvent(t.Context(), providerplane.Event{
		ID: 40, Type: "data_changed", Data: []byte(`{}`),
	}))
	require.NoError(server.receiveHubEvent(t.Context(), providerplane.Event{
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

	err := server.receiveHubEvent(t.Context(), providerplane.Event{
		ID: 40, Type: "data_changed", Data: []byte(`{}`),
	})
	require.ErrorIs(t, err, providerplane.ErrHubUnavailable)
	assert.Equal(t, before, server.Hub().Generation())
}

func TestHubEventLifecyclePausesUntilFleetIsEnabled(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	started := make(chan struct{}, 2)
	stopped := make(chan struct{}, 2)
	lifecycle := newHubEventLifecycle(true, func(ctx context.Context) {
		started <- struct{}{}
		<-ctx.Done()
		stopped <- struct{}{}
	})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		lifecycle.Run(ctx)
		close(done)
	}()

	require.Eventually(func() bool { return len(started) == 1 }, time.Second, time.Millisecond)
	lifecycle.SetEnabled(false)
	require.Eventually(func() bool { return len(stopped) == 1 }, time.Second, time.Millisecond)
	assert.Never(func() bool { return len(started) > 1 }, 50*time.Millisecond, time.Millisecond)

	lifecycle.SetEnabled(true)
	require.Eventually(func() bool { return len(started) == 2 }, time.Second, time.Millisecond)
	cancel()
	require.Eventually(func() bool { return len(stopped) == 2 }, time.Second, time.Millisecond)
	require.Eventually(func() bool {
		select {
		case <-done:
			return true
		default:
			return false
		}
	}, time.Second, time.Millisecond)
}

func TestHubEventLifecycleCanStopAfterCleanReturn(t *testing.T) {
	runs := 0
	lifecycle := newHubEventLifecycleStoppingOnCleanReturn(true, func(context.Context) {
		runs++
	})

	lifecycle.Run(t.Context())

	assert.Equal(t, 1, runs)
}

func TestDisabledFederationSpokeSeedsDisconnectedStateForFreshSubscriber(t *testing.T) {
	const hubID = "66666666666666666666666666666666"
	require := require.New(t)
	credentials, err := federationauth.Open(t.TempDir() + "/credentials.json")
	require.NoError(err)
	require.NoError(credentials.StoreOutbound(
		hubID, "disabled-spoke-secret",
		federationauth.SpokeToHubScopes(),
	))
	spoke := New(dbtest.Open(t), nil, nil, "/", &config.Config{Fleet: config.Fleet{
		Enabled: false, Role: config.FleetRoleSpoke,
		Hub: &config.FleetHub{
			NodeID: hubID, BaseURL: "https://hub.example",
		},
	}}, ServerOptions{
		FederationSpokeID: federationEventTestNodeID, FederationSpokeActive: true,
		FederationCredentials: credentials, DisableWorkspaceBackgroundMonitors: true,
	})
	t.Cleanup(func() { gracefulShutdown(t, spoke) })

	events, _ := spoke.Hub().Subscribe(t.Context(), true)
	select {
	case record := <-events:
		require.Equal("hub_connection_changed", record.Event.Type)
		state, ok := record.Event.Data.(hubConnectionState)
		require.True(ok)
		assert.False(t, state.Connected)
	case <-time.After(time.Second):
		require.FailNow("fresh subscriber did not receive hub availability")
	}
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
		DaemonAccess:      DaemonAccessOptions{Token: "hub-secret", RequireAPIAuth: true},
		FederationSpokeID: hubID, FederationCredentials: hubCredentials,
		DisableWorkspaceBackgroundMonitors: true,
	})
	t.Cleanup(func() { gracefulShutdown(t, hub) })
	hubHTTP := httptest.NewTLSServer(hub)
	t.Cleanup(hubHTTP.Close)
	for range 40 {
		hub.Hub().Broadcast(Event{Type: "workspace_status", Data: struct{}{}})
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
	t.Cleanup(func() { gracefulShutdown(t, spoke) })

	require.Eventually(func() bool {
		records, _ := spoke.Hub().RingSnapshotSince(0)
		for _, record := range records {
			if record.Event.Type != "hub_connection_changed" {
				continue
			}
			state, ok := record.Event.Data.(hubConnectionState)
			if ok && state.Connected {
				return true
			}
		}
		return false
	}, 2*time.Second, 5*time.Millisecond)
	localFloor := spoke.Hub().Generation()
	remoteID := hub.Hub().Broadcast(Event{Type: "data_changed", Data: struct{}{}})
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
	spoke.applyFleetConfigLocked()
	spoke.cfgMu.Unlock()
	require.Eventually(func() bool {
		return hub.SubscriberCount() == 0
	}, time.Second, 5*time.Millisecond)
	disabledFloor := spoke.Hub().Generation()
	hub.Hub().Broadcast(Event{
		Type: "pr_detail_refreshed", Data: map[string]int{"number": 7},
	})
	assert.Never(t, func() bool {
		records, _ := spoke.Hub().RingSnapshotSince(disabledFloor)
		return slices.ContainsFunc(records, func(record RecordedEvent) bool {
			return record.Event.Type == "pr_detail_refreshed"
		})
	}, 50*time.Millisecond, 5*time.Millisecond)

	spoke.cfgMu.Lock()
	spoke.cfg.Fleet.Enabled = true
	spoke.applyFleetConfigLocked()
	spoke.cfgMu.Unlock()
	require.Eventually(func() bool {
		records, _ := spoke.Hub().RingSnapshotSince(disabledFloor)
		return slices.ContainsFunc(records, func(record RecordedEvent) bool {
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
		federationEventTestNodeID, federationauth.SpokeToHubScopes(),
	)
	require.NoError(t, err)
	server := New(dbtest.Open(t), nil, nil, "/", nil, ServerOptions{
		DaemonAccess: DaemonAccessOptions{
			Token: "local-daemon-secret", RequireAPIAuth: true,
		},
		FederationCredentials:              credentials,
		DisableWorkspaceBackgroundMonitors: true,
	})
	t.Cleanup(func() { gracefulShutdown(t, server) })
	httpServer := httptest.NewServer(server)
	t.Cleanup(httpServer.Close)
	return server, httpServer, token
}

func openFederationEventStream(
	t *testing.T, server *httptest.Server, token, cursor string,
) testFederationEventStream {
	t.Helper()
	request, err := federationEventRequest(server.URL, token, cursor)
	require.NoError(t, err)
	response, err := server.Client().Do(request)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, response.StatusCode)
	require.Equal(t, "text/event-stream", response.Header.Get("Content-Type"))
	frames := make(chan testSSEFrame, 8)
	go scanTestSSEFrames(response.Body, frames)
	return testFederationEventStream{response: response, frames: frames}
}

func federationEventRequest(baseURL, token, cursor string) (*http.Request, error) {
	request, err := http.NewRequest(
		http.MethodGet, baseURL+"/api/v1/federation/events", nil,
	)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set(federationauth.NodeIDHeader, federationEventTestNodeID)
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

func assertNoTestSSEFrame(t *testing.T, frames <-chan testSSEFrame) {
	t.Helper()
	select {
	case frame := <-frames:
		require.Fail(t, "unexpected federation event", "%+v", frame)
	case <-time.After(50 * time.Millisecond):
	}
}

func mustEventID(t *testing.T, raw string) uint64 {
	t.Helper()
	var id uint64
	_, err := fmt.Sscan(raw, &id)
	require.NoError(t, err)
	return id
}
