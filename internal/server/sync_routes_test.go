package server

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/server/httpapi"
)

func TestArchiveStartRejectsDisabledSyncer(t *testing.T) {
	require := require.New(t)
	database := openTestDB(t)
	syncer := github.NewSyncer(nil, database, nil, nil, time.Minute, nil, nil)
	t.Cleanup(syncer.Stop)
	syncer.DisableSync()
	srv := New(database, syncer, nil, "/", nil, ServerOptions{})

	archiveRR := doJSON(t, srv, http.MethodPost, "/api/v1/archive/start", map[string]bool{"all": true})
	require.Equal(http.StatusServiceUnavailable, archiveRR.Code, archiveRR.Body.String())
}

func TestSyncRoutesWithoutSyncer(t *testing.T) {
	assert := assert.New(t)
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
	assert.Empty(rates.ProviderPools)
	assert.Empty(rates.LocalCeilings)

	syncRR := doJSON(t, srv, http.MethodPost, "/api/v1/sync", nil)
	require.Equal(http.StatusServiceUnavailable, syncRR.Code, syncRR.Body.String())

	var problem httpapi.ProblemError
	require.NoError(json.NewDecoder(syncRR.Body).Decode(&problem))
	assert.Equal(httpapi.CodeServiceUnavailable, problem.Code)
	assert.Equal("syncer not configured", problem.Detail)
}
