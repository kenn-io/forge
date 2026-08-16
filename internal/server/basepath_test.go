package server

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/testutil/dbtest"
)

func setupWithBasePath(t *testing.T, basePath string, frontend fs.FS) *Server {
	t.Helper()
	database := dbtest.Open(t)

	mock := &mockGH{}
	syncer := ghclient.NewSyncer(map[string]ghclient.Client{"github.com": mock}, database, nil, nil, time.Minute, nil, nil)
	t.Cleanup(syncer.Stop)
	srv := New(
		database, syncer, frontend, basePath,
		nil, ServerOptions{},
	)
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	return srv
}

func TestBasePathAPIRouting(t *testing.T) {
	frontend := fstest.MapFS{
		"index.html": &fstest.MapFile{
			Data: []byte(`<!DOCTYPE html><html><head></head><body>app</body></html>`),
		},
	}

	tests := []struct {
		name            string
		basePath        string
		reqPath         string
		wantStatus      int
		wantContentType string
		wantBody        string
	}{
		{"root: API returns JSON", "/", "/api/v1/sync/status", 200, "application/json", `"running"`},
		{"root: SPA returns HTML", "/", "/pulls", 200, "text/html", "<body>app</body>"},
		{"prefix: API returns JSON", "/kenn-forge/", "/kenn-forge/api/v1/sync/status", 200, "application/json", `"running"`},
		{"prefix: SPA returns HTML", "/kenn-forge/", "/kenn-forge/pulls", 200, "text/html", "<body>app</body>"},
		{"prefix: bare API 404s", "/kenn-forge/", "/api/v1/sync/status", 404, "text/plain", "404 page not found"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert := assert.New(t)
			srv := setupWithBasePath(t, tt.basePath, frontend)
			req := httptest.NewRequest(http.MethodGet, tt.reqPath, nil)
			rr := httptest.NewRecorder()
			srv.ServeHTTP(rr, req)

			require.Equal(t, tt.wantStatus, rr.Code, rr.Body.String())
			ct := rr.Header().Get("Content-Type")
			assert.True(strings.HasPrefix(ct, tt.wantContentType), "expected %s response for %q, got Content-Type %q: %s", tt.wantContentType, tt.reqPath, ct, rr.Body.String())
			assert.Contains(rr.Body.String(), tt.wantBody)
		})
	}
}

func TestBasePathHealthEndpointsStayAtRoot(t *testing.T) {
	srv := setupWithBasePath(t, "/kenn-forge/", nil)

	for _, path := range []string{"/healthz", "/livez"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, req)

		require.Equal(t, http.StatusOK, rr.Code, path)
		assert.Contains(t, rr.Body.String(), `"status":"ok"`, path)
	}
}

func TestBasePathInjectsScript(t *testing.T) {
	frontend := fstest.MapFS{
		"index.html": &fstest.MapFile{
			Data: []byte(`<!DOCTYPE html><html><head></head><body>app</body></html>`),
		},
	}

	srv := setupWithBasePath(t, "/kenn-forge/", frontend)
	req := httptest.NewRequest(http.MethodGet, "/kenn-forge/", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	body := rr.Body.String()
	assert.Contains(t, body, `window.__BASE_PATH__="/kenn-forge/"`)
}

func TestBasePathRewritesAssetURLs(t *testing.T) {
	frontend := fstest.MapFS{
		"index.html": &fstest.MapFile{
			Data: []byte(`<!DOCTYPE html><html><head><link href="/assets/index.css"></head><body><script src="/assets/index.js"></script></body></html>`),
		},
	}

	srv := setupWithBasePath(t, "/kenn-forge/", frontend)
	req := httptest.NewRequest(http.MethodGet, "/kenn-forge/", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	body := rr.Body.String()
	assert := assert.New(t)
	assert.NotContains(body, `href="/assets/`)
	assert.Contains(body, `href="/kenn-forge/assets/`)
	assert.Contains(body, `src="/kenn-forge/assets/`)
}

func TestCSRFRejectsCrossSite(t *testing.T) {
	srv := setupWithBasePath(t, "/", nil)

	body := strings.NewReader(`{"body":"test"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sync", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	require.Equal(t, http.StatusForbidden, rr.Code)
}

func TestPastedImageUploadRejectsCrossSite(t *testing.T) {
	srv := setupWithBasePath(t, "/", nil)
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/workspaces/ws-1/pasted-images",
		strings.NewReader(`{"data":"aW1hZ2U="}`),
	)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	recorder := httptest.NewRecorder()

	srv.ServeHTTP(recorder, req)

	require.Equal(t, http.StatusForbidden, recorder.Code, recorder.Body.String())
}

func TestCSRFRejectsWrongContentType(t *testing.T) {
	srv := setupWithBasePath(t, "/", nil)

	body := strings.NewReader(`body=test`)
	req := httptest.NewRequest(
		http.MethodPost, "/api/v1/sync", body,
	)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	require.Equal(t, http.StatusUnsupportedMediaType, rr.Code)
}

func TestCSRFAllowsSameOrigin(t *testing.T) {
	srv := setupWithBasePath(t, "/", nil)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sync", nil)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	// Should pass CSRF and reach the handler (202 Accepted).
	require.Equal(t, http.StatusAccepted, rr.Code, rr.Body.String())
}

func TestCSRFAllowsNoSecFetchSite(t *testing.T) {
	srv := setupWithBasePath(t, "/", nil)

	// Non-browser clients (curl, API tools) won't send
	// Sec-Fetch-Site but must still set Content-Type.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sync", nil)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	require.Equal(t, http.StatusAccepted, rr.Code, rr.Body.String())
}

func TestCSRFRejectsNoContentType(t *testing.T) {
	srv := setupWithBasePath(t, "/", nil)

	// Zero-body POST without Content-Type should be blocked.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sync", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	require.Equal(t, http.StatusUnsupportedMediaType, rr.Code, rr.Body.String())
}

func TestCSRFAppliesUnderBasePath(t *testing.T) {
	srv := setupWithBasePath(t, "/kenn-forge/", nil)

	body := strings.NewReader(`{"body":"test"}`)
	req := httptest.NewRequest(
		http.MethodPost,
		"/kenn-forge/api/v1/sync", body,
	)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	require.Equal(t, http.StatusForbidden, rr.Code)
}

func TestBasePathDocsAndOpenAPIUsePrefixedURLs(t *testing.T) {
	assert := assert.New(t)
	frontend := fstest.MapFS{
		"index.html": &fstest.MapFile{
			Data: []byte(`<!DOCTYPE html><html><head></head><body>app</body></html>`),
		},
	}

	srv := setupWithBasePath(t, "/kenn-forge/", frontend)

	docsReq := httptest.NewRequest(http.MethodGet, "/kenn-forge/api/v1/docs", nil)
	docsRR := httptest.NewRecorder()
	srv.ServeHTTP(docsRR, docsReq)

	require.Equal(t, http.StatusOK, docsRR.Code, docsRR.Body.String())
	assert.Contains(docsRR.Body.String(), `apiDescriptionUrl="/kenn-forge/api/v1/openapi.yaml"`)

	openAPIReq := httptest.NewRequest(http.MethodGet, "/kenn-forge/api/v1/openapi.json", nil)
	openAPIRR := httptest.NewRecorder()
	srv.ServeHTTP(openAPIRR, openAPIReq)

	require.Equal(t, http.StatusOK, openAPIRR.Code, openAPIRR.Body.String())
	assert.Contains(openAPIRR.Body.String(), `"url":"/kenn-forge/api/v1"`)
}
