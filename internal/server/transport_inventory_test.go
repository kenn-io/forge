package server

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTransportInventoryIncludesRegisteredLongLivedRoutes(t *testing.T) {
	inventory, err := NewTransportInventory()
	require.NoError(t, err)

	assert := assert.New(t)
	assert.Equal(1, inventory.SchemaVersion)
	for _, expected := range []TransportRoute{
		{
			Method: http.MethodGet, Path: "/api/v1/events",
			Transport: TransportHTTPStream, Accept: "text/event-stream",
		},
		{
			Method: http.MethodGet, Path: "/api/roborev/api/stream/events",
			Transport: TransportHTTPStream, Accept: "application/x-ndjson",
		},
		{
			Method: http.MethodGet, Path: "/api/roborev/api/job/output",
			Transport: TransportHTTPStream, Accept: "application/x-ndjson",
			Query: map[string]string{"stream": "1"},
		},
		{
			Method: http.MethodPost, Path: "/api/roborev/api/sync/now",
			Transport: TransportHTTPStream, Accept: "application/x-ndjson",
			Query: map[string]string{"stream": "1"},
		},
		{
			Method:    http.MethodGet,
			Path:      "/api/v1/workspaces/{id}/terminal",
			Transport: TransportWebSocket,
		},
		{
			Method:    http.MethodGet,
			Path:      "/ws/v1/workspaces/{id}/terminal",
			Transport: TransportWebSocket,
		},
		{
			Method:    http.MethodGet,
			Path:      "/ws/v1/workspaces/{id}/runtime/sessions/{session_key}/terminal",
			Transport: TransportWebSocket,
		},
		{
			Method:    http.MethodGet,
			Path:      "/ws/v1/fleet/hosts/{host_key}/workspaces/{id}/terminal",
			Transport: TransportWebSocket,
		},
		{
			Method:    http.MethodGet,
			Path:      "/ws/v1/fleet/hosts/{host_key}/workspaces/{id}/runtime/sessions/{session_key}/terminal",
			Transport: TransportWebSocket,
		},
	} {
		assert.Contains(inventory.Routes, expected)
	}
}

func TestNormalizeTransportRoutesRejectsInvalidContracts(t *testing.T) {
	tests := []struct {
		name   string
		routes []TransportRoute
		match  string
	}{
		{
			name: "relative path",
			routes: []TransportRoute{{
				Method: http.MethodGet, Path: "api/events",
				Transport: TransportHTTPStream, Accept: "text/event-stream",
			}},
			match: "absolute path",
		},
		{
			name: "unknown transport",
			routes: []TransportRoute{{
				Method: http.MethodGet, Path: "/api/events",
				Transport: "carrier-pigeon", Accept: "text/plain",
			}},
			match: "unsupported transport",
		},
		{
			name: "websocket with accept",
			routes: []TransportRoute{{
				Method: http.MethodGet, Path: "/ws/terminal",
				Transport: TransportWebSocket, Accept: "text/event-stream",
			}},
			match: "must not declare accept",
		},
		{
			name: "duplicate",
			routes: []TransportRoute{
				{Method: http.MethodGet, Path: "/ws/terminal", Transport: TransportWebSocket},
				{Method: http.MethodGet, Path: "/ws/terminal", Transport: TransportWebSocket},
			},
			match: "duplicate transport route",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := normalizeTransportRoutes(tt.routes)
			require.ErrorContains(t, err, tt.match)
		})
	}
}

func TestNormalizeTransportRoutesSortsDeterministically(t *testing.T) {
	routes, err := normalizeTransportRoutes([]TransportRoute{
		{Method: http.MethodPost, Path: "/z", Transport: TransportHTTPStream, Accept: "text/event-stream"},
		{Method: http.MethodGet, Path: "/b", Transport: TransportWebSocket},
		{Method: http.MethodGet, Path: "/a", Transport: TransportWebSocket},
		{Method: http.MethodGet, Path: "/q", Transport: TransportHTTPStream, Accept: "application/x-ndjson", Query: map[string]string{"stream": "2"}},
		{Method: http.MethodGet, Path: "/q", Transport: TransportHTTPStream, Accept: "application/x-ndjson", Query: map[string]string{"stream": "1"}},
	})
	require.NoError(t, err)

	assert.Equal(t, []TransportRoute{
		{Method: http.MethodGet, Path: "/a", Transport: TransportWebSocket},
		{Method: http.MethodGet, Path: "/b", Transport: TransportWebSocket},
		{Method: http.MethodGet, Path: "/q", Transport: TransportHTTPStream, Accept: "application/x-ndjson", Query: map[string]string{"stream": "1"}},
		{Method: http.MethodGet, Path: "/q", Transport: TransportHTTPStream, Accept: "application/x-ndjson", Query: map[string]string{"stream": "2"}},
		{Method: http.MethodPost, Path: "/z", Transport: TransportHTTPStream, Accept: "text/event-stream"},
	}, routes)
}
