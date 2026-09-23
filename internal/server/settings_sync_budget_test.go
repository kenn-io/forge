package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/config"
	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/server/itemapi"
	"go.kenn.io/forge/internal/server/spokeapi"
	"go.kenn.io/forge/internal/testutil"
	"go.kenn.io/forge/internal/testutil/dbtest"
)

func setupSyncBudgetTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(validReloadConfig), 0o644))
	cfg, err := config.Load(cfgPath)
	require.NoError(t, err)

	database := dbtest.Open(t)
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": &mockGH{}},
		database, nil, nil, time.Minute, nil,
		map[string]*ghclient.SyncBudget{
			"github.com": ghclient.NewSyncBudgetWithEssentialReserve(cfg.BudgetPerHour()),
		},
	)
	t.Cleanup(syncer.Stop)
	srv := NewWithConfig(
		database, syncer, nil, nil, cfg, cfgPath,
		ServerOptions{HostCheckAllowLoopbackAnyPort: true},
	)
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	return srv, cfgPath
}

func liveSyncCeiling(t *testing.T, srv *Server) itemapi.LocalSyncCeilingStatus {
	t.Helper()
	rr := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/rate-limits", nil)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var limits itemapi.RateLimitsResponse
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&limits))
	require.Len(t, limits.LocalCeilings, 1)
	for _, ceiling := range limits.LocalCeilings {
		return ceiling
	}
	return itemapi.LocalSyncCeilingStatus{}
}

func TestSyncBudgetSettingAppliesToLiveCeilingAndPersists(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	srv, cfgPath := setupSyncBudgetTestServer(t)

	rr := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/settings", nil)
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	var initial spokeapi.SettingsResponse
	require.NoError(json.NewDecoder(rr.Body).Decode(&initial))
	assert.Equal(500, initial.Sync.BudgetPerHour)
	assert.Equal(500, liveSyncCeiling(t, srv).Limit)

	budget := 3000
	rr = testutil.DoJSON(t, srv, http.MethodPut, "/api/v1/settings", spokeapi.UpdateSettingsRequest{
		Sync: &spokeapi.SyncSettingsUpdate{BudgetPerHour: &budget},
	})
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	var saved spokeapi.SettingsResponse
	require.NoError(json.NewDecoder(rr.Body).Decode(&saved))
	assert.Equal(3000, saved.Sync.BudgetPerHour)

	ceiling := liveSyncCeiling(t, srv)
	assert.Equal(3000, ceiling.Limit, "the running budget must not wait for a restart")
	assert.Equal(2700, ceiling.BackgroundLimit)
	persisted, err := config.Load(cfgPath)
	require.NoError(err)
	assert.Equal(3000, persisted.BudgetPerHour())
}

func TestSyncBudgetSettingRejectsOutOfRangeValues(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	srv, cfgPath := setupSyncBudgetTestServer(t)

	for _, budget := range []int{
		config.MinSyncBudgetPerHour - 1,
		config.MaxSyncBudgetPerHour + 1,
	} {
		rr := testutil.DoJSON(t, srv, http.MethodPut, "/api/v1/settings", spokeapi.UpdateSettingsRequest{
			Sync: &spokeapi.SyncSettingsUpdate{BudgetPerHour: &budget},
		})
		require.Equal(http.StatusUnprocessableEntity, rr.Code, rr.Body.String())
	}

	assert.Equal(500, liveSyncCeiling(t, srv).Limit)
	persisted, err := config.Load(cfgPath)
	require.NoError(err)
	assert.Equal(500, persisted.BudgetPerHour())
}

func TestConfigReloadAppliesSyncBudgetWithoutRestart(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	srv, cfgPath := setupSyncBudgetTestServer(t)

	writeConfigToml(t, cfgPath, "sync_budget_per_hour = 1200\n"+validReloadConfig)
	event := srv.configreload.ApplyConfigChange(t.Context())

	require.True(event.Valid, event.Error)
	assert.False(event.RestartRequired)
	assert.Equal(1200, liveSyncCeiling(t, srv).Limit)
}
