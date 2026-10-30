package accesstest

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"go.kenn.io/forge/internal/server"
	servertest "go.kenn.io/forge/internal/testutil/servertest"
)

var browserTraceFrontend = fstest.MapFS{
	"index.html": &fstest.MapFile{Data: []byte(`<!DOCTYPE html><html><head></head><body>app</body></html>`)},
}

func postBrowserTraces(t *testing.T, srv *server.Server) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/telemetry/traces", strings.NewReader(`{"resourceSpans":[]}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	return rr
}

func pageBody(t *testing.T, srv *server.Server) string {
	t.Helper()
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))
	return rr.Body.String()
}

// Not parallel: the relay reads the daemon's exporter environment.
func TestBrowserTracesReachTheDaemonCollector(t *testing.T) {
	assert := assert.New(t)
	var received string
	collector := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		received = r.URL.Path + " " + string(body)
	}))
	t.Cleanup(collector.Close)
	t.Setenv("OTEL_TRACES_EXPORTER", "otlp")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", collector.URL)
	srv := servertest.SetupSPAAssetServer(t, "/", browserTraceFrontend, server.ServerOptions{})

	rr := postBrowserTraces(t, srv)
	assert.Equal(http.StatusNoContent, rr.Code, rr.Body.String())
	assert.Equal(`/v1/traces {"resourceSpans":[]}`, received)
	assert.Contains(pageBody(t, srv), `window.__KENN_FORGE_TRACE_EXPORT__=true;`)
}

func TestBrowserTracesAreRefusedWithoutDaemonTraceExport(t *testing.T) {
	assert := assert.New(t)
	t.Setenv("OTEL_TRACES_EXPORTER", "")
	srv := servertest.SetupSPAAssetServer(t, "/", browserTraceFrontend, server.ServerOptions{})

	assert.Equal(http.StatusNotFound, postBrowserTraces(t, srv).Code)
	assert.NotContains(pageBody(t, srv), `__KENN_FORGE_TRACE_EXPORT__`)
}
