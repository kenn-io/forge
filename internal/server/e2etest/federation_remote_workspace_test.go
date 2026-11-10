package e2etest

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/internal/federationauth"
	"go.kenn.io/forge/internal/server/workspaceapi"
	"go.kenn.io/forge/internal/terminalwebsocket"
	serverfake "go.kenn.io/forge/internal/testutil/serverfake"
)

func (f *federatedForgesFixture) requestJSON(
	t *testing.T, daemon *federatedDaemonFixture, method, path string, body any,
) (int, []byte) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		require.NoError(t, err)
		reader = bytes.NewReader(raw)
	}
	response := f.request(t, daemon, method, path, reader)
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	return response.StatusCode, raw
}

func (f *federatedForgesFixture) dialTerminal(
	t *testing.T, daemon *federatedDaemonFixture, path string,
) (*websocket.Conn, error) {
	t.Helper()
	url := "wss" + strings.TrimPrefix(daemon.HTTP.URL, "https") + path
	conn, response, err := terminalwebsocket.Dial(
		t.Context(), url,
		http.Header{"Authorization": {"Bearer " + daemon.LocalToken}},
		f.HTTPClient,
	)
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	return conn, err
}

// TestSpokeDrivesWorkspaceOnAnotherSpokeThroughHub drives spoke A's own API,
// as its UI does, to create, list, refresh, attach to, and remove a workspace
// that runs on spoke B. Spoke A sends each request to the hub, which forwards
// it to spoke B; spoke A never holds a credential for spoke B.
func TestSpokeDrivesWorkspaceOnAnotherSpokeThroughHub(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	fixture := newFederatedForgesFixture(t)
	_, ok := fixture.NodeA.Credentials.Outbound(federatedNodeBID)
	require.False(ok, "spokes hold no credential for each other")
	onB := "/api/v1/fleet/hosts/" + federatedNodeBID

	// The pull request route reaches spoke B's own handler: the fixture gives
	// spoke B no Git credential, and its refusal comes back unchanged.
	status, body := fixture.requestJSON(t, fixture.NodeA, http.MethodPost, onB+"/workspaces", map[string]any{
		"provider": "github", "platform_host": "github.com",
		"owner": "acme", "name": "widget", "mr_number": 1,
	})
	assert.Equal(http.StatusServiceUnavailable, status, string(body))
	assert.Contains(string(body), "gitCredentialUnavailable")

	status, body = fixture.requestJSON(t, fixture.NodeA, http.MethodPost,
		onB+"/repo/gh/acme/widget/workspaces", map[string]any{"branch": "work/remote-default"})
	require.Equal(http.StatusAccepted, status, string(body))
	var created workspaceapi.WorkspaceResponse
	require.NoError(json.Unmarshal(body, &created))
	require.NotEmpty(created.ID)
	remote, err := fixture.NodeB.Database.GetWorkspace(t.Context(), created.ID)
	require.NoError(err)
	require.NotNil(remote, "spoke B owns the workspace")
	onA, err := fixture.NodeA.Database.GetWorkspace(t.Context(), created.ID)
	require.NoError(err)
	assert.Nil(onA, "spoke A created nothing locally")

	require.Eventually(func() bool {
		row := workspaceByID(fixture.snapshot(t, fixture.NodeA).Workspaces, created.ID)
		return row != nil && row.FleetHostKey == federatedNodeBID
	}, 5*time.Second, 50*time.Millisecond, "spoke A lists the workspace under spoke B")

	status, body = fixture.requestJSON(t, fixture.NodeA, http.MethodGet, onB+"/workspaces/"+created.ID, nil)
	assert.Equal(http.StatusOK, status, string(body))
	assert.Contains(string(body), created.ID)

	status, body = fixture.requestJSON(t, fixture.NodeA, http.MethodPost, onB+"/workspaces/"+created.ID+"/refresh", nil)
	assert.Equal(http.StatusOK, status, string(body))
	assert.Contains(string(body), created.ID)

	// A quick action's agent handoff reaches spoke B's own handler, which
	// rejects a launch target it does not have.
	status, body = fixture.requestJSON(t, fixture.NodeA, http.MethodPost,
		onB+"/workspaces/ws-spoke-b/runtime/agent-handoffs",
		map[string]any{"target_key": "no-such-agent", "message": "review this"})
	assert.Equal(http.StatusBadRequest, status, string(body))
	assert.Contains(string(body), "target_key is not an available agent launch target")

	conn, err := fixture.dialTerminal(t, fixture.NodeA,
		"/ws/v1/fleet/hosts/"+federatedNodeBID+"/workspaces/ws-spoke-b/terminal")
	require.NoError(err, "the terminal bridge reaches spoke B through the hub")
	_ = conn.Close(websocket.StatusNormalClosure, "test done")
	_, err = fixture.dialTerminal(t, fixture.NodeA,
		"/ws/v1/fleet/hosts/"+federatedNodeBID+"/workspaces/ws-spoke-a/terminal")
	require.Error(err, "spoke B does not own spoke A's workspace")

	status, body = fixture.requestJSON(t, fixture.NodeA, http.MethodDelete, onB+"/workspaces/"+created.ID+"?force=true", nil)
	require.Equal(http.StatusNoContent, status, string(body))
	require.Eventually(func() bool {
		stored, err := fixture.NodeB.Database.GetWorkspace(t.Context(), created.ID)
		return err == nil && stored == nil
	}, 5*time.Second, 50*time.Millisecond, "spoke B removes the workspace")

	status, body = fixture.requestJSON(t, fixture.NodeA, http.MethodGet,
		"/api/v1/fleet/hosts/"+federatedHubID+"/workspaces", nil)
	assert.Equal(http.StatusOK, status, "spoke A reaches the hub's own workspaces: %s", body)
}

// TestSpokeDoesNotForwardPeerFleetRoutes keeps a peer from bouncing a fleet
// route off a spoke: only the spoke's own UI may send one through its hub.
func TestSpokeDoesNotForwardPeerFleetRoutes(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	fixture := newFederatedForgesFixture(t)
	hubToA, ok := fixture.Hub.Credentials.Outbound(federatedNodeAID)
	require.True(t, ok)
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet,
		fixture.NodeA.HTTP.URL+"/api/v1/fleet/hosts/"+federatedNodeBID+"/workspaces", nil)
	require.NoError(t, err)
	request.Header.Set("Authorization", "Bearer "+hubToA.Token)
	request.Header.Set(federationauth.NodeIDHeader, federatedHubID)
	response, err := fixture.HTTPClient.Do(request)
	require.NoError(t, err)
	body, err := io.ReadAll(response.Body)
	require.NoError(t, response.Body.Close())
	require.NoError(t, err)
	assert.Equal(t, http.StatusNotFound, response.StatusCode, string(body))
	assert.NotContains(t, string(body), "ws-spoke-b")
}
