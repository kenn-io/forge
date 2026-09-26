package settingsservertest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/server/roborevapi"
	"go.kenn.io/forge/internal/testutil"
	servertest "go.kenn.io/forge/internal/testutil/servertest"
)

func TestRoborevHealthProbeAvailable(t *testing.T) {
	assert := assert.New(t)

	daemon := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/status" {
				w.Header().Set(
					"Content-Type", "application/json",
				)
				_, _ = w.Write(
					[]byte(`{"version":"1.2.3"}`),
				)
				return
			}
			http.NotFound(w, r)
		},
	))
	defer daemon.Close()

	srv := servertest.SetupTestServerWithRoborev(t, daemon.URL)

	rr := testutil.DoJSON(
		t, srv, http.MethodGet,
		"/api/v1/roborev/status", nil)

	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	var resp roborevapi.RoborevStatusResponse
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	assert.True(resp.Available)
	assert.Equal("1.2.3", resp.Version)
	assert.Equal(daemon.URL, resp.Endpoint)
}

func TestRoborevHealthProbeUnavailable(t *testing.T) {
	assert := assert.New(t)

	srv := servertest.SetupTestServerWithRoborev(t, "http://127.0.0.1:1")

	rr := testutil.DoJSON(
		t, srv, http.MethodGet,
		"/api/v1/roborev/status", nil)

	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	var resp roborevapi.RoborevStatusResponse
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	assert.False(resp.Available)
	assert.Empty(resp.Version)
}
