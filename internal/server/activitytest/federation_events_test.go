package activitytest

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/federation"
	"go.kenn.io/forge/internal/federationauth"
	"go.kenn.io/forge/internal/providerplane"
	forgeserver "go.kenn.io/forge/internal/server"
	"go.kenn.io/forge/internal/server/authapi"
	"go.kenn.io/forge/internal/server/syncevents"
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

func TestFederationEventEndpointFiltersNodeLocalEvents(t *testing.T) {
	server, httpServer, token := newFederationEventServer(t)
	// A fresh spoke refreshes sync status at the replay barrier, so the
	// hub must not inject an older cached value after that barrier.
	server.Hub().Broadcast(syncevents.Event{Type: "sync_status", Data: map[string]bool{"running": true}})
	stream := openFederationEventStream(t, httpServer, token, "")
	defer stream.response.Body.Close()

	server.Hub().Broadcast(syncevents.Event{Type: "workspace_created", Data: map[string]string{"id": "ws-1"}})
	server.Hub().Broadcast(syncevents.Event{Type: "workspace_status", Data: map[string]string{"id": "ws-1"}})
	server.Hub().Broadcast(syncevents.Event{Type: "config.changed", Data: map[string]bool{"valid": true}})
	providerID := server.Hub().Broadcast(syncevents.Event{Type: "data_changed", Data: struct{}{}})

	frame := nextTestSSEFrame(t, stream.frames)
	assert.Equal(t, providerID, mustEventID(t, frame.ID))
	assert.Equal(t, "data_changed", frame.Type)
	assert.JSONEq(t, `{}`, frame.Data)
	assertNoTestSSEFrame(t, stream.frames)
}

func TestFederationEventEndpointTreatsMalformedCursorAsFresh(t *testing.T) {
	server, httpServer, token := newFederationEventServer(t)
	stream := openFederationEventStream(t, httpServer, token, "not-a-number")
	defer stream.response.Body.Close()

	server.Hub().Broadcast(syncevents.Event{Type: "pr_ci_refresh_queued", Data: struct{}{}})

	assert.Equal(t, "pr_ci_refresh_queued", nextTestSSEFrame(t, stream.frames).Type)
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
		},
	}
	cfgPath := filepath.Join(dir, "config.toml")
	require.NoError(cfg.Save(cfgPath))
	server := forgeserver.NewWithConfig(dbtest.Open(t), nil, nil, nil, cfg, cfgPath, forgeserver.ServerOptions{
		DaemonAccess: authapi.DaemonAccessOptions{
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

	request, err := http.NewRequestWithContext(t.Context(),
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

	server.Hub().Broadcast(syncevents.Event{Type: "data_changed", Data: struct{}{}})
	select {
	case _, open := <-stream.frames:
		require.False(open, "revoked spoke event stream remained open")
	case <-time.After(time.Second):
		require.FailNow("revoked spoke event stream did not close")
	}
}

func newFederationEventServer(
	t *testing.T,
) (*forgeserver.Server, *httptest.Server, string) {
	t.Helper()
	credentials, err := federationauth.Open(t.TempDir() + "/credentials.json")
	require.NoError(t, err)
	token, err := credentials.MintInbound(
		federationEventTestNodeID, federationauth.SpokeToHubScopes(),
	)
	require.NoError(t, err)
	server := forgeserver.New(dbtest.Open(t), nil, nil, "/", nil, forgeserver.ServerOptions{
		DaemonAccess: authapi.DaemonAccessOptions{
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
