package compression_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"go.kenn.io/forge/internal/server/compression"
)

func TestSPAAssetHandlerMethods(t *testing.T) {
	frontend := fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("<!doctype html><html><head></head><body>app</body></html>")},
	}
	handler := compression.NewSPAAssetHandler(frontend, "/", nil)

	for _, method := range []string{http.MethodDelete, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodOptions} {
		for _, path := range []string{"/repo/github/owner/name", "/", "/index.html"} {
			t.Run(method+path, func(t *testing.T) {
				request := httptest.NewRequestWithContext(t.Context(), method, path, nil)
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, request)

				assert.Equal(t, http.StatusNotFound, response.Code)
				assert.NotContains(t, response.Body.String(), "<html>")
			})
		}
	}

	for _, method := range []string{http.MethodGet, http.MethodHead} {
		for _, path := range []string{"/repo/github/owner/name", "/", "/index.html"} {
			t.Run(method+path, func(t *testing.T) {
				request := httptest.NewRequestWithContext(t.Context(), method, path, nil)
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, request)

				assert.Equal(t, http.StatusOK, response.Code)
				assert.Equal(t, "text/html; charset=utf-8", response.Header().Get("Content-Type"))
				if method == http.MethodGet {
					assert.Contains(t, response.Body.String(), "<body>app</body>")
				}
			})
		}
	}
}
