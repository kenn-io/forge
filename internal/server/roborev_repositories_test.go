package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/server/itemapi"
	"go.kenn.io/forge/internal/server/roborevapi"
	"go.kenn.io/forge/internal/testutil"
)

func TestListRoborevConfiguredRepositories(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	srv := setupTestServerWithRoborev(t, "http://127.0.0.1:1")
	srv.roborevRepositories = roborevapi.NewRoborevRepositoryProbeWithDeps(nil, roborevapi.RoborevRepositoryProbeDeps{
		Now: time.Now,
		LoadInventory: func(context.Context) ([]roborevapi.RoborevTrackedRepository, error) {
			return []roborevapi.RoborevTrackedRepository{
				{RootPath: "/checkout/widgets", Identity: "https://github.com/acme/widgets.git"},
			}, nil
		},
		ResolveHookPath: func(context.Context, string) (string, error) { return "/hooks/post-commit", nil },
		InspectHook:     func(string) (bool, error) { return true, nil },
	})

	rr := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/roborev/configured-repositories", nil)
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	var body struct {
		Repositories []itemapi.RoborevConfiguredRepositoryResponse `json:"repositories"`
		Complete     bool                                          `json:"complete"`
	}
	require.NoError(json.NewDecoder(rr.Body).Decode(&body))
	require.Len(body.Repositories, 1)
	assert.True(body.Complete)
	assert.Equal("github", body.Repositories[0].Provider)
	assert.Equal("github.com", body.Repositories[0].PlatformHost)
	assert.Equal("acme/widgets", body.Repositories[0].RepoPath)
	assert.NotContains(rr.Body.String(), "/checkout/widgets")
}

func TestListRoborevConfiguredRepositoriesMarksPartialResultsIncomplete(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	srv := setupTestServerWithRoborev(t, "http://127.0.0.1:1")
	srv.roborevRepositories = roborevapi.NewRoborevRepositoryProbeWithDeps(nil, roborevapi.RoborevRepositoryProbeDeps{
		Now: time.Now,
		LoadInventory: func(context.Context) ([]roborevapi.RoborevTrackedRepository, error) {
			return []roborevapi.RoborevTrackedRepository{
				{RootPath: "/configured", Identity: "https://github.com/acme/widgets.git"},
				{RootPath: "/unresolved", Identity: "https://github.com/acme/tools.git"},
			}, nil
		},
		ResolveHookPath: func(_ context.Context, root string) (string, error) {
			if root == "/unresolved" {
				return "", errors.New("temporary git failure")
			}
			return "/hooks/post-commit", nil
		},
		InspectHook: func(string) (bool, error) { return true, nil },
	})

	rr := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/roborev/configured-repositories", nil)
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	var body itemapi.RoborevConfiguredRepositoriesResponse
	require.NoError(json.NewDecoder(rr.Body).Decode(&body))
	require.Len(body.Repositories, 1)
	assert.Equal("acme/widgets", body.Repositories[0].RepoPath)
	assert.False(body.Complete)
}

func TestListRoborevConfiguredRepositoriesReturnsTypedUnavailableWithoutBlockingSummaries(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	srv := setupTestServerWithRoborev(t, "http://private.invalid:7373")
	srv.roborevRepositories = roborevapi.NewRoborevRepositoryProbeWithDeps(nil, roborevapi.RoborevRepositoryProbeDeps{
		Now: time.Now,
		LoadInventory: func(context.Context) ([]roborevapi.RoborevTrackedRepository, error) {
			return nil, errors.New("private daemon /private/checkout")
		},
	})

	rr := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/roborev/configured-repositories", nil)
	require.Equal(http.StatusServiceUnavailable, rr.Code)
	assert.Equal("application/problem+json", rr.Header().Get("Content-Type"))
	var problem struct {
		Code   string `json:"code"`
		Detail string `json:"detail"`
	}
	require.NoError(json.NewDecoder(rr.Body).Decode(&problem))
	assert.Equal("serviceUnavailable", problem.Code)
	assert.Equal("roborev repository configuration unavailable", problem.Detail)
	assert.NotContains(rr.Body.String(), "private.invalid")
	assert.NotContains(rr.Body.String(), "/private/checkout")

	summaries := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/repos/summary", nil)
	assert.Equal(http.StatusOK, summaries.Code)
}
