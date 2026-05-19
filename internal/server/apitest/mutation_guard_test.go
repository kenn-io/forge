package apitest

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMutationGuardRejectsNonJSONContentTypeWithProblemBody is a paving
// wire-level test: it covers the path where the test deliberately violates
// a precondition the generated client always satisfies. The generated
// client sets Content-Type: application/json automatically on mutation
// requests, so the only way to exercise the CSRF/Content-Type guard from
// a wire-level test is to construct a raw http.Request.
//
// The request still flows through srv.ServeHTTP, which means the full
// middleware chain runs. A handler-internal test that called the handler
// function directly would never trigger the guard at all, because the
// guard short-circuits the request before handler dispatch.
//
// The asserted shape is the wire response: status 415, the JSON error
// envelope writeError produces, and the response Content-Type header
// writeJSON sets.
func TestMutationGuardRejectsNonJSONContentTypeWithProblemBody(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	srv, _ := setupTestServer(t)

	req := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/sync",
		bytes.NewReader(nil),
	)
	req.Header.Set("Content-Type", "text/plain")

	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	resp := rr.Result()
	defer resp.Body.Close()

	require.Equal(http.StatusUnsupportedMediaType, resp.StatusCode)
	assert.Equal("application/json", resp.Header.Get("Content-Type"))

	var body map[string]string
	require.NoError(json.NewDecoder(resp.Body).Decode(&body))
	assert.Contains(body["error"], "Content-Type must be application/json")
}
