package e2etest

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHealthEndpointsE2E_ReturnOKWhenReady(t *testing.T) {
	srv, _ := setupTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	tests := []struct {
		name string
		path string
	}{
		{name: "readiness", path: "/healthz"},
		{name: "liveness", path: "/livez"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)

			resp, err := ts.Client().Get(ts.URL + tt.path)
			require.NoError(err)
			defer resp.Body.Close()

			assert.Equal(http.StatusOK, resp.StatusCode)

			var body localHealthResponse
			err = json.NewDecoder(resp.Body).Decode(&body)
			require.NoError(err)

			assert.Equal("ok", body.Status)
		})
	}
}

func TestHealthEndpointsE2E_RemainAvailableAtRootWithBasePath(t *testing.T) {
	srv := setupWithBasePath(t, "/middleman/", nil)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	tests := []struct {
		name string
		path string
	}{
		{name: "readiness", path: "/healthz"},
		{name: "liveness", path: "/livez"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)

			resp, err := ts.Client().Get(ts.URL + tt.path)
			require.NoError(err)
			defer resp.Body.Close()

			assert.Equal(http.StatusOK, resp.StatusCode)

			var body localHealthResponse
			err = json.NewDecoder(resp.Body).Decode(&body)
			require.NoError(err)

			assert.Equal("ok", body.Status)
		})
	}
}

func TestHealthzE2E_ReturnsServiceUnavailableWhenDatabaseClosed(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	srv, database := setupTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	require.NoError(database.Close())

	resp, err := ts.Client().Get(ts.URL + "/healthz")
	require.NoError(err)
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	require.NoError(err)

	assert.Equal(http.StatusServiceUnavailable, resp.StatusCode)
	assert.Contains(string(body), "database unavailable")
}
