package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/testutil/dbtest"
)

func TestHealthReportsRunningBuildWithoutBearer(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	srv := New(dbtest.Open(t), nil, nil, "/", nil, ServerOptions{
		DaemonAccess: DaemonAccessOptions{Token: "private-test-token", RequireAPIAuth: true},
	})
	srv.SetBuildInfo(BuildInfo{Version: "v1.2.3", Commit: strings.Repeat("a", 40)})
	response := httptest.NewRecorder()
	srv.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	require.Equal(t, http.StatusOK, response.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
	assert.Equal("ok", body["status"])
	assert.Equal("v1.2.3", body["version"])
	assert.Equal(strings.Repeat("a", 40), body["revision"])
	assert.Equal(false, body["modified"])
}
