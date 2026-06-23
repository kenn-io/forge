package server

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
	"time"

	Assert "github.com/stretchr/testify/assert"
	ghclient "go.kenn.io/middleman/internal/github"
	"go.kenn.io/middleman/internal/testutil/dbtest"
)

func setupEmbeddedServer(
	t *testing.T,
	basePath string,
	frontend fs.FS,
	options ServerOptions,
) *Server {
	t.Helper()
	database := dbtest.Open(t)

	mock := &mockGH{}
	syncer := ghclient.NewSyncer(map[string]ghclient.Client{"github.com": mock}, database, nil, nil, time.Minute, nil, nil)
	t.Cleanup(syncer.Stop)
	return New(
		database,
		syncer,
		frontend,
		basePath,
		nil,
		options,
	)
}

// TestBootstrapActiveWorktreeKey pins the served-config contract:
// the SPA bootstrap script carries the daemon-side UI focus state
// (set over PUT /api/v1/ui/active-worktree) and nothing else —
// presentation preferences are injected client-side by webview
// hosts.
func TestBootstrapActiveWorktreeKey(t *testing.T) {
	frontend := fstest.MapFS{
		"index.html": &fstest.MapFile{
			Data: []byte(`<!DOCTYPE html><html><head></head><body>app</body></html>`),
		},
	}

	t.Run("set key is served", func(t *testing.T) {
		srv := setupEmbeddedServer(t, "/app/", frontend, ServerOptions{})
		srv.SetActiveWorktreeKey("wt-123")

		req := httptest.NewRequest(http.MethodGet, "/app/", nil)
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, req)

		body := rr.Body.String()
		assert := Assert.New(t)
		assert.Contains(body, `"activeWorktreeKey":"wt-123"`)
		assert.Contains(body, `window.__middleman_config=`)
	})

	t.Run("no key means no served config", func(t *testing.T) {
		srv := setupEmbeddedServer(t, "/app/", frontend, ServerOptions{})
		req := httptest.NewRequest(http.MethodGet, "/app/", nil)
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, req)

		body := rr.Body.String()
		assert := Assert.New(t)
		assert.NotContains(body, `__middleman_config`)
		assert.Contains(body, `window.__BASE_PATH__="/app/"`)
	})
}

func TestSPACacheHeaders(t *testing.T) {
	frontend := fstest.MapFS{
		"index.html": &fstest.MapFile{
			Data: []byte(`<!DOCTYPE html><html><head></head><body>app</body></html>`),
		},
		"assets/index-DEADBEEF.js": &fstest.MapFile{
			Data: []byte(`console.log("bundle");`),
		},
		"favicon.ico": &fstest.MapFile{
			Data: []byte(`icon`),
		},
	}

	srv := setupEmbeddedServer(t, "/", frontend, ServerOptions{})

	cases := []struct {
		name         string
		path         string
		wantStatus   int
		wantCacheHdr string
		wantPragma   string
		wantExpires  string
	}{
		{
			name:         "index served at root must not be cached",
			path:         "/",
			wantStatus:   http.StatusOK,
			wantCacheHdr: "no-store, no-cache, must-revalidate, max-age=0",
			wantPragma:   "no-cache",
			wantExpires:  "0",
		},
		{
			name:         "spa fallback must not be cached",
			path:         "/some/spa/route",
			wantStatus:   http.StatusOK,
			wantCacheHdr: "no-store, no-cache, must-revalidate, max-age=0",
			wantPragma:   "no-cache",
			wantExpires:  "0",
		},
		{
			name:         "hashed assets are immutable",
			path:         "/assets/index-DEADBEEF.js",
			wantStatus:   http.StatusOK,
			wantCacheHdr: "public, max-age=31536000, immutable",
		},
		{
			name:         "missing hashed asset returns 404",
			path:         "/assets/index-MISSING.js",
			wantStatus:   http.StatusNotFound,
			wantCacheHdr: "",
		},
		{
			name:         "non-hashed top-level files are not given immutable headers",
			path:         "/favicon.ico",
			wantStatus:   http.StatusOK,
			wantCacheHdr: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			rr := httptest.NewRecorder()
			srv.ServeHTTP(rr, req)
			assert := Assert.New(t)
			assert.Equal(tc.wantStatus, rr.Code)
			assert.Equal(tc.wantCacheHdr, rr.Header().Get("Cache-Control"))
			assert.Equal(tc.wantPragma, rr.Header().Get("Pragma"))
			assert.Equal(tc.wantExpires, rr.Header().Get("Expires"))
		})
	}
}

func TestSPAFrameProtectionHeaders(t *testing.T) {
	frontend := fstest.MapFS{
		"index.html": &fstest.MapFile{
			Data: []byte(`<!DOCTYPE html><html><head></head><body>app</body></html>`),
		},
		"assets/index-DEADBEEF.js": &fstest.MapFile{
			Data: []byte(`console.log("bundle");`),
		},
	}

	srv := setupEmbeddedServer(t, "/", frontend, ServerOptions{})

	cases := []struct {
		name string
		path string
	}{
		{name: "index", path: "/"},
		{name: "spa fallback", path: "/workspaces"},
		{name: "terminal route", path: "/terminal/ws-123"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			rr := httptest.NewRecorder()
			srv.ServeHTTP(rr, req)
			assert := Assert.New(t)
			assert.Equal(http.StatusOK, rr.Code)
			assert.Equal(spaFrameAncestorsPolicy, rr.Header().Get("Content-Security-Policy"))
			assert.Equal(spaXFrameOptions, rr.Header().Get("X-Frame-Options"))
		})
	}

	t.Run("asset", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/assets/index-DEADBEEF.js", nil)
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, req)
		assert := Assert.New(t)
		assert.Equal(http.StatusOK, rr.Code)
		assert.Empty(rr.Header().Get("Content-Security-Policy"))
		assert.Empty(rr.Header().Get("X-Frame-Options"))
	})

	t.Run("workspace embed route remains frameable", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/workspaces/embed/list", nil)
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, req)
		assert := Assert.New(t)
		assert.Equal(http.StatusOK, rr.Code)
		assert.Empty(rr.Header().Get("Content-Security-Policy"))
		assert.Empty(rr.Header().Get("X-Frame-Options"))
	})
}
