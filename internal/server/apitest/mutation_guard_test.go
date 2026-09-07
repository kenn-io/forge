package apitest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMutationGuardRejectsCrossOriginRequestWithJSONError(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	srv, _ := setupTestServer(t)

	req := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/sync",
		nil,
	)
	req.Header.Set("Sec-Fetch-Site", "cross-site")

	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	resp := rr.Result()
	defer resp.Body.Close()

	require.Equal(http.StatusForbidden, resp.StatusCode)
	assert.Equal("application/json", resp.Header.Get("Content-Type"))

	var body map[string]string
	require.NoError(json.NewDecoder(resp.Body).Decode(&body))
	assert.Contains(body["error"], "cross-origin requests are not allowed")
}
