package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime/debug"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/server/authapi"
	"go.kenn.io/forge/internal/server/routepolicy"
	"go.kenn.io/forge/internal/testutil/dbtest"
)

func TestHealthReportsRunningBuildWithoutBearer(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	srv := New(dbtest.Open(t), nil, nil, "/", nil, ServerOptions{
		DaemonAccess: authapi.DaemonAccessOptions{Token: "private-test-token", RequireAPIAuth: true},
	})
	srv.SetBuildInfo(BuildInfo{Version: "v1.2.3", Commit: strings.Repeat("a", 40)})
	response := httptest.NewRecorder()
	srv.ServeHTTP(response, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/healthz", nil))
	require.Equal(t, http.StatusOK, response.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
	assert.Equal("ok", body["status"])
	assert.Equal("v1.2.3", body["version"])
	assert.Regexp(`^[0-9a-f]{40}$`, body["revision"])
	assert.IsType(false, body["modified"])
}

func TestHealthResponseBuildIdentity(t *testing.T) {
	t.Parallel()
	info := BuildInfo{Version: "v1.2.3", Commit: strings.Repeat("a", 40)}
	for _, tc := range []struct {
		name     string
		build    *debug.BuildInfo
		revision string
		modified bool
	}{
		{name: "no executable metadata", revision: strings.Repeat("a", 40)},
		{name: "archive without VCS settings", build: &debug.BuildInfo{}, revision: strings.Repeat("a", 40)},
		{name: "VCS revision", build: &debug.BuildInfo{Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: strings.Repeat("b", 40)},
			{Key: "vcs.modified", Value: "false"},
		}}, revision: strings.Repeat("b", 40)},
		{name: "modified checkout", build: &debug.BuildInfo{Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: strings.Repeat("b", 40)},
			{Key: "vcs.modified", Value: "true"},
		}}, revision: strings.Repeat("b", 40), modified: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, routepolicy.HealthResponse{Status: "ok", Version: "v1.2.3", Revision: tc.revision, Modified: tc.modified}, routepolicy.HealthResponseForBuild(info.Version, info.Commit, tc.build))
		})
	}
}
