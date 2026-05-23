package e2etest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTelemetryEndpointE2E_ReturnsDisabledWithoutReporter(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	srv, _ := setupTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	req, err := http.NewRequest(
		http.MethodPost,
		ts.URL+"/api/v1/telemetry/events",
		strings.NewReader(`{"event":"app_loaded"}`),
	)
	require.NoError(err)
	req.Header.Set("Content-Type", "application/json")

	resp, err := ts.Client().Do(req)
	require.NoError(err)
	defer resp.Body.Close()

	var body struct {
		Status string `json:"status"`
	}
	err = json.NewDecoder(resp.Body).Decode(&body)
	require.NoError(err)

	assert.Equal(http.StatusAccepted, resp.StatusCode)
	assert.Equal("disabled", body.Status)
}

func TestTelemetryEndpointE2E_RejectsUnsupportedEvents(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	srv, _ := setupTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	req, err := http.NewRequest(
		http.MethodPost,
		ts.URL+"/api/v1/telemetry/events",
		strings.NewReader(`{"event":"repo_opened"}`),
	)
	require.NoError(err)
	req.Header.Set("Content-Type", "application/json")

	resp, err := ts.Client().Do(req)
	require.NoError(err)
	defer resp.Body.Close()

	assert.Equal(http.StatusBadRequest, resp.StatusCode)
}
