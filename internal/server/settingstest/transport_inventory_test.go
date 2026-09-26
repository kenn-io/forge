package settingstest

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/server"
	"go.kenn.io/forge/internal/server/routepolicy"
)

func TestTransportInventoryIncludesRegisteredLongLivedRoutes(t *testing.T) {
	inventory, err := server.NewTransportInventory()
	require.NoError(t, err)

	assert := assert.New(t)
	assert.Equal(1, inventory.SchemaVersion)
	for _, expected := range []routepolicy.TransportRoute{
		{
			Method: http.MethodGet, Path: "/api/v1/events",
			Transport: routepolicy.TransportHTTPStream, Accept: "text/event-stream",
		},
		{
			Method: http.MethodGet, Path: "/api/roborev/api/stream/events",
			Transport: routepolicy.TransportHTTPStream, Accept: "application/x-ndjson",
		},
		{
			Method: http.MethodGet, Path: "/api/roborev/api/job/output",
			Transport: routepolicy.TransportHTTPStream, Accept: "application/x-ndjson",
			Query: map[string]string{"stream": "1"},
		},
		{
			Method: http.MethodPost, Path: "/api/roborev/api/sync/now",
			Transport: routepolicy.TransportHTTPStream, Accept: "application/x-ndjson",
			Query: map[string]string{"stream": "1"},
		},
		{
			Method:    http.MethodGet,
			Path:      "/api/v1/workspaces/{id}/terminal",
			Transport: routepolicy.TransportWebSocket,
		},
		{
			Method:    http.MethodGet,
			Path:      "/ws/v1/workspaces/{id}/terminal",
			Transport: routepolicy.TransportWebSocket,
		},
		{
			Method:    http.MethodGet,
			Path:      "/ws/v1/workspaces/{id}/runtime/sessions/{session_key}/terminal",
			Transport: routepolicy.TransportWebSocket,
		},
		{
			Method:    http.MethodGet,
			Path:      "/ws/v1/fleet/hosts/{host_key}/workspaces/{id}/terminal",
			Transport: routepolicy.TransportWebSocket,
		},
		{
			Method:    http.MethodGet,
			Path:      "/ws/v1/fleet/hosts/{host_key}/workspaces/{id}/runtime/sessions/{session_key}/terminal",
			Transport: routepolicy.TransportWebSocket,
		},
	} {
		assert.Contains(inventory.Routes, expected)
	}
}
