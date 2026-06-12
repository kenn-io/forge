package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/config"
)

func TestSwitchHandlerSwapsDifferentHandlerTypes(t *testing.T) {
	switcher := NewSwitchHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))

	firstRR := httptest.NewRecorder()
	switcher.ServeHTTP(firstRR, httptest.NewRequest(http.MethodGet, "/", nil))
	require.Equal(t, http.StatusAccepted, firstRR.Code)

	next := http.NewServeMux()
	next.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	switcher.Swap(next)

	secondRR := httptest.NewRecorder()
	switcher.ServeHTTP(secondRR, httptest.NewRequest(http.MethodGet, "/", nil))
	require.Equal(t, http.StatusNoContent, secondRR.Code)
}

func TestStartupHandlerServesSPAWhileAPIUnavailable(t *testing.T) {
	frontend := fstest.MapFS{
		"index.html": &fstest.MapFile{
			Data: []byte(`<!DOCTYPE html><html><head></head><body>app</body></html>`),
		},
		"assets/index-DEADBEEF.js": &fstest.MapFile{
			Data: []byte(`console.log("bundle");`),
		},
	}
	cfg := &config.Config{
		Host:     "127.0.0.1",
		Port:     8091,
		BasePath: "/",
	}
	handler := NewStartupHandler(
		frontend,
		cfg,
		ServerOptions{},
		staticListener{addr: staticListenerAddr("127.0.0.1:8091")},
	)

	rootReq := httptest.NewRequest(http.MethodGet, "/", nil)
	rootReq.Host = "127.0.0.1:8091"
	rootReq.RemoteAddr = "127.0.0.1:1234"
	rootRR := httptest.NewRecorder()
	handler.ServeHTTP(rootRR, rootReq)

	assert := Assert.New(t)
	assert.Equal(http.StatusOK, rootRR.Code)
	assert.Contains(rootRR.Body.String(), `window.__BASE_PATH__="/"`)
	assert.Contains(rootRR.Body.String(), "app")

	apiReq := httptest.NewRequest(http.MethodGet, "/api/v1/settings", nil)
	apiReq.Host = "127.0.0.1:8091"
	apiReq.RemoteAddr = "127.0.0.1:1234"
	apiRR := httptest.NewRecorder()
	handler.ServeHTTP(apiRR, apiReq)

	assert.Equal(http.StatusServiceUnavailable, apiRR.Code)
	assert.Equal("application/problem+json", apiRR.Header().Get("Content-Type"))
	var problem ProblemError
	require.NoError(t, json.Unmarshal(apiRR.Body.Bytes(), &problem))
	assert.Equal(CodeServiceUnavailable, problem.Code)

	assetReq := httptest.NewRequest(http.MethodGet, "/assets/index-DEADBEEF.js", nil)
	assetReq.Host = "127.0.0.1:8091"
	assetReq.RemoteAddr = "127.0.0.1:1234"
	assetRR := httptest.NewRecorder()
	handler.ServeHTTP(assetRR, assetReq)

	assert.Equal(http.StatusOK, assetRR.Code)
	assert.Equal("public, max-age=31536000, immutable", assetRR.Header().Get("Cache-Control"))
}

func TestStartupHandlerUsesHostValidation(t *testing.T) {
	frontend := fstest.MapFS{
		"index.html": &fstest.MapFile{
			Data: []byte(`<!DOCTYPE html><html><head></head><body>app</body></html>`),
		},
	}
	cfg := &config.Config{
		Host:     "127.0.0.1",
		Port:     8091,
		BasePath: "/",
	}
	handler := NewStartupHandler(
		frontend,
		cfg,
		ServerOptions{},
		staticListener{addr: staticListenerAddr("127.0.0.1:8091")},
	)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "attacker.example:8091"
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	Assert.Equal(t, http.StatusForbidden, rr.Code, rr.Body.String())
}
