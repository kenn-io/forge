package server

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
)

// The Messages mode and its msgvault backend were removed. The former
// API surface must stay gone: a route that quietly came back would
// resurrect credential proxying middleman no longer guards.
func TestRemovedMessagesRoutesReturnNotFound(t *testing.T) {
	srv, _ := setupTestServer(t)

	requests := []struct {
		method string
		path   string
		body   any
	}{
		{http.MethodGet, "/api/v1/msgvault/health", nil},
		{http.MethodPost, "/api/v1/msgvault/configure", map[string]any{
			"url":         "http://127.0.0.1:8080",
			"api_key_env": "MSGVAULT_API_KEY",
		}},
		{http.MethodGet, "/api/v1/messages/saved-searches", nil},
		{http.MethodPut, "/api/v1/messages/saved-searches", map[string]any{
			"searches": []any{},
		}},
	}
	for _, tc := range requests {
		rr := doJSON(t, srv, tc.method, tc.path, tc.body)
		assert.Equal(t, http.StatusNotFound, rr.Code,
			"%s %s: %s", tc.method, tc.path, rr.Body.String())
	}
}
