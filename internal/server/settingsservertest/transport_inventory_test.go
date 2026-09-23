package settingsservertest

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/server/routepolicy"
)

func TestNormalizeTransportRoutesRejectsInvalidContracts(t *testing.T) {
	tests := []struct {
		name   string
		routes []routepolicy.TransportRoute
		match  string
	}{
		{
			name: "relative path",
			routes: []routepolicy.TransportRoute{{
				Method: http.MethodGet, Path: "api/events",
				Transport: routepolicy.TransportHTTPStream, Accept: "text/event-stream",
			}},
			match: "absolute path",
		},
		{
			name: "unknown transport",
			routes: []routepolicy.TransportRoute{{
				Method: http.MethodGet, Path: "/api/events",
				Transport: "carrier-pigeon", Accept: "text/plain",
			}},
			match: "unsupported transport",
		},
		{
			name: "websocket with accept",
			routes: []routepolicy.TransportRoute{{
				Method: http.MethodGet, Path: "/ws/terminal",
				Transport: routepolicy.TransportWebSocket, Accept: "text/event-stream",
			}},
			match: "must not declare accept",
		},
		{
			name: "duplicate",
			routes: []routepolicy.TransportRoute{
				{Method: http.MethodGet, Path: "/ws/terminal", Transport: routepolicy.TransportWebSocket},
				{Method: http.MethodGet, Path: "/ws/terminal", Transport: routepolicy.TransportWebSocket},
			},
			match: "duplicate transport route",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := routepolicy.NormalizeTransportRoutes(tt.routes)
			require.ErrorContains(t, err, tt.match)
		})
	}
}

func TestNormalizeTransportRoutesSortsDeterministically(t *testing.T) {
	routes, err := routepolicy.NormalizeTransportRoutes([]routepolicy.TransportRoute{
		{Method: http.MethodPost, Path: "/z", Transport: routepolicy.TransportHTTPStream, Accept: "text/event-stream"},
		{Method: http.MethodGet, Path: "/b", Transport: routepolicy.TransportWebSocket},
		{Method: http.MethodGet, Path: "/a", Transport: routepolicy.TransportWebSocket},
		{Method: http.MethodGet, Path: "/q", Transport: routepolicy.TransportHTTPStream, Accept: "application/x-ndjson", Query: map[string]string{"stream": "2"}},
		{Method: http.MethodGet, Path: "/q", Transport: routepolicy.TransportHTTPStream, Accept: "application/x-ndjson", Query: map[string]string{"stream": "1"}},
	})
	require.NoError(t, err)

	assert.Equal(t, []routepolicy.TransportRoute{
		{Method: http.MethodGet, Path: "/a", Transport: routepolicy.TransportWebSocket},
		{Method: http.MethodGet, Path: "/b", Transport: routepolicy.TransportWebSocket},
		{Method: http.MethodGet, Path: "/q", Transport: routepolicy.TransportHTTPStream, Accept: "application/x-ndjson", Query: map[string]string{"stream": "1"}},
		{Method: http.MethodGet, Path: "/q", Transport: routepolicy.TransportHTTPStream, Accept: "application/x-ndjson", Query: map[string]string{"stream": "2"}},
		{Method: http.MethodPost, Path: "/z", Transport: routepolicy.TransportHTTPStream, Accept: "text/event-stream"},
	}, routes)
}
