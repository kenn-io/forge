package apitest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/db"
)

func TestPullStackMissingPRReturnsPullNotFound(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	srv, database := setupTestServer(t)
	_, err := database.UpsertRepo(t.Context(), db.GitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/pulls/gh/acme/widget/99/stack", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	require.Equal(http.StatusNotFound, rr.Code)
	var body map[string]any
	require.NoError(json.Unmarshal(rr.Body.Bytes(), &body))
	assert.Equal("pullNotFound", body["code"])
	assert.Equal("pull request not found", body["detail"])
}
