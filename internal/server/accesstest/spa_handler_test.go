package accesstest

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/server"
	servertest "go.kenn.io/forge/internal/testutil/servertest"
)

// TestBootstrapActiveWorktreeKey covers daemon-side focus state in the SPA bootstrap.
func TestBootstrapActiveWorktreeKey(t *testing.T) {
	frontend := fstest.MapFS{
		"index.html": &fstest.MapFile{
			Data: []byte(`<!DOCTYPE html><html><head></head><body>app</body></html>`),
		},
	}

	t.Run("set key is served", func(t *testing.T) {
		srv := servertest.SetupSPAAssetServer(t, "/app/", frontend, server.ServerOptions{})
		srv.SetActiveWorktreeKey("wt-123")

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/app/", nil)
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, req)

		body := rr.Body.String()
		assert := assert.New(t)
		assert.Contains(body, `window.__kenn_forge_active_worktree_key="wt-123";`)
	})

	t.Run("no key means no served config", func(t *testing.T) {
		srv := servertest.SetupSPAAssetServer(t, "/app/", frontend, server.ServerOptions{})
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/app/", nil)
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, req)

		body := rr.Body.String()
		assert := assert.New(t)
		assert.NotContains(body, `__kenn_forge_active_worktree_key`)
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

	srv := servertest.SetupSPAAssetServer(t, "/", frontend, server.ServerOptions{})

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
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, tc.path, nil)
			rr := httptest.NewRecorder()
			srv.ServeHTTP(rr, req)
			assert := assert.New(t)
			assert.Equal(tc.wantStatus, rr.Code)
			assert.Equal(tc.wantCacheHdr, rr.Header().Get("Cache-Control"))
			assert.Equal(tc.wantPragma, rr.Header().Get("Pragma"))
			assert.Equal(tc.wantExpires, rr.Header().Get("Expires"))
		})
	}
}

func TestSPAAssetsServePrecompressedRepresentations(t *testing.T) {
	asset := []byte(strings.Repeat("export const payload = 'value';\n", 256))
	var brotliBody bytes.Buffer
	brotliWriter := brotli.NewWriterLevel(&brotliBody, brotli.BestCompression)
	_, err := brotliWriter.Write(asset)
	require.NoError(t, err)
	require.NoError(t, brotliWriter.Close())
	zstdWriter, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedBestCompression), zstd.WithEncoderCRC(false))
	require.NoError(t, err)
	zstdBody := zstdWriter.EncodeAll(asset, nil)
	zstdWriter.Close()
	frontend := fstest.MapFS{
		"index.html":                  &fstest.MapFile{Data: []byte("<!doctype html><html></html>")},
		"assets/index-ABC123.js":      &fstest.MapFile{Data: asset},
		"assets/index-ABC123.js.br":   &fstest.MapFile{Data: brotliBody.Bytes()},
		"assets/index-ABC123.js.zstd": &fstest.MapFile{Data: zstdBody},
	}

	for _, basePath := range []string{"/", "/app/"} {
		t.Run(basePath, func(t *testing.T) {
			srv := servertest.SetupSPAAssetServer(t, basePath, frontend, server.ServerOptions{})
			for _, tc := range []struct {
				name, method, acceptEncoding, byteRange, wantEncoding string
				wantStatus                                            int
				wantBody                                              []byte
				wantLength                                            int
			}{
				{"brotli preferred", http.MethodGet, "br, zstd", "", "br", http.StatusOK, brotliBody.Bytes(), brotliBody.Len()},
				{"zstd higher quality", http.MethodGet, "br;q=0.5, zstd", "", "zstd", http.StatusOK, zstdBody, len(zstdBody)},
				{"identity", http.MethodGet, "identity", "", "", http.StatusOK, asset, len(asset)},
				{"head", http.MethodHead, "br", "", "", http.StatusOK, nil, len(asset)},
				{"range", http.MethodGet, "br", "bytes=0-5", "", http.StatusPartialContent, asset[:6], 6},
			} {
				t.Run(tc.name, func(t *testing.T) {
					req := httptest.NewRequestWithContext(t.Context(), tc.method, basePath+"assets/index-ABC123.js", nil)
					req.Header.Set("Accept-Encoding", tc.acceptEncoding)
					req.Header.Set("Range", tc.byteRange)
					rr := httptest.NewRecorder()
					srv.ServeHTTP(rr, req)

					assert := assert.New(t)
					assert.Equal(tc.wantStatus, rr.Code)
					assert.Equal(tc.wantEncoding, rr.Header().Get("Content-Encoding"))
					assert.Equal(strconv.Itoa(tc.wantLength), rr.Header().Get("Content-Length"))
					assert.Equal("Accept-Encoding", rr.Header().Get("Vary"))
					assert.Equal("public, max-age=31536000, immutable", rr.Header().Get("Cache-Control"))
					assert.Contains(rr.Header().Get("Content-Type"), "javascript")
					assert.Equal(tc.wantBody, rr.Body.Bytes())
				})
			}
		})
	}
}
