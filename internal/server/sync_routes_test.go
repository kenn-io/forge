package server

import (
	"encoding/json"
	"net/http"
	"testing"

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/github"
)

func TestSyncRoutesWithoutSyncer(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	srv := New(openTestDB(t), nil, nil, "/", nil, ServerOptions{})

	statusRR := doJSON(t, srv, http.MethodGet, "/api/v1/sync/status", nil)
	require.Equal(http.StatusOK, statusRR.Code, statusRR.Body.String())

	var status github.SyncStatus
	require.NoError(json.NewDecoder(statusRR.Body).Decode(&status))
	assert.False(status.Running)
	assert.Empty(status.LastRunAt)
	assert.Empty(status.LastError)

	ratesRR := doJSON(t, srv, http.MethodGet, "/api/v1/rate-limits", nil)
	require.Equal(http.StatusOK, ratesRR.Code, ratesRR.Body.String())

	var rates rateLimitsResponse
	require.NoError(json.NewDecoder(ratesRR.Body).Decode(&rates))
	assert.Empty(rates.Hosts)

	syncRR := doJSON(t, srv, http.MethodPost, "/api/v1/sync", nil)
	require.Equal(http.StatusServiceUnavailable, syncRR.Code, syncRR.Body.String())

	var problem ProblemError
	require.NoError(json.NewDecoder(syncRR.Body).Decode(&problem))
	assert.Equal(CodeServiceUnavailable, problem.Code)
	assert.Equal("syncer not configured", problem.Detail)
}
