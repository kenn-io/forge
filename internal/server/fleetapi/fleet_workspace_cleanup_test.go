package fleetapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/internal/config"
)

func TestRequestWorkspaceCleanupAdmitsCleanupOnOwningSpoke(t *testing.T) {
	assert := assert.New(t)
	var requestMethod string
	var requestPath string
	var authorization string
	var nodeID string
	spoke := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestMethod = r.Method
		requestPath = r.URL.Path
		authorization = r.Header.Get("Authorization")
		nodeID = r.Header.Get("X-Kenn-Forge-Node-ID")
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(spoke.Close)

	handler := New(Deps{})
	tokens := configureTestMembers(
		t,
		handler,
		testTLSClient(t, spoke),
		config.FleetMember{NodeID: testMemberNodeID, BaseURL: spoke.URL},
	)

	err := handler.RequestWorkspaceCleanup(t.Context(), testMemberNodeID, "ws-one")
	require.NoError(t, err)
	assert.Equal(http.MethodPost, requestMethod)
	assert.Equal("/api/v1/federation/workspaces/ws-one/cleanup", requestPath)
	assert.Equal("Bearer "+tokens[testMemberNodeID], authorization)
	assert.Equal(testHubNodeID, nodeID)
}

func TestQueueFederationWorkspaceCleanupUsesLocalQueue(t *testing.T) {
	var workspaceID string
	handler := New(Deps{
		QueueWorkspaceDeletion: func(id string) error {
			workspaceID = id
			return nil
		},
	})

	api := newFleetTestAPI()
	handler.Register(api)
	request := httptest.NewRequest(
		http.MethodPost, "/federation/workspaces/ws-1/cleanup", nil,
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	api.Adapter().ServeHTTP(response, request)

	require.Equal(t, http.StatusAccepted, response.Code, response.Body.String())
	assert.Equal(t, "ws-1", workspaceID)
}
