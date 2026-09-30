package main

import (
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/archive/snapshot"
	"go.kenn.io/forge/internal/web"
)

func TestArchiveContextScenarioExportsEvidenceAndCoverage(t *testing.T) {
	assert := assert.New(t)
	assets, err := web.Assets()
	require.NoError(t, err)
	state, err := buildAppState(t.Context(), assets, appOptions{scenario: "archive-context", roborevEndpoint: defaultRoborevEndpoint})
	require.NoError(t, err)
	t.Cleanup(state.close)
	end := time.Now().UTC().Add(time.Minute)
	path := "http://127.0.0.1/api/v1/archive/snapshot?issue_scope=open&start=" + end.Add(-7*24*time.Hour).Format(time.RFC3339) + "&end=" + end.Format(time.RFC3339)
	response := httptest.NewRecorder()
	state.handler.ServeHTTP(response, httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil))
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var result snapshot.ArchiveSnapshot
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &result))
	require.Len(t, result.Repositories, 2)
	assert.NotEqual(result.Repositories[0].ProviderID, result.Repositories[1].ProviderID)
	for _, repo := range result.Repositories {
		assert.Positive(repo.ProviderID)
		require.NotNil(t, repo.Coverage)
		assert.Equal("current", repo.Coverage.Status)
		assert.NotNil(repo.Coverage.InitialCompletedAt)
		assert.NotNil(repo.Coverage.MaintenanceSucceededAt)
	}
	assert.NotEmpty(result.Issues)
	assert.NotEmpty(result.PullRequests)
	for _, pr := range result.PullRequests {
		assert.Len(pr.HeadSHA, 40)
	}
	first := result.PullRequests[0]
	response = httptest.NewRecorder()
	state.handler.ServeHTTP(response, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "http://127.0.0.1/__e2e/archive-context", strings.NewReader(`{"complete":false,"head_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`)))
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	response = httptest.NewRecorder()
	state.handler.ServeHTTP(response, httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil))
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &result))
	assert.NotEqual("current", result.Repositories[0].Coverage.Status)
	for _, pr := range result.PullRequests {
		if pr.ID == first.ID {
			assert.Equal("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", pr.HeadSHA)
		}
	}
	response = httptest.NewRecorder()
	state.handler.ServeHTTP(response, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "http://127.0.0.1/__e2e/archive-context", strings.NewReader(`{"complete":true}`)))
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	response = httptest.NewRecorder()
	state.handler.ServeHTTP(response, httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil))
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &result))
	assert.Equal("current", result.Repositories[0].Coverage.Status)
}
