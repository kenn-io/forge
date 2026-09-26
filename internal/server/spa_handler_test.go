package server

import (
	"bytes"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/testutil/dbtest"
)

func setupSPAAssetServer(
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

// TestBootstrapActiveWorktreeKey covers daemon-side focus state in the SPA bootstrap.
func TestBootstrapActiveWorktreeKey(t *testing.T) {
	frontend := fstest.MapFS{
		"index.html": &fstest.MapFile{
			Data: []byte(`<!DOCTYPE html><html><head></head><body>app</body></html>`),
		},
	}

	t.Run("set key is served", func(t *testing.T) {
		srv := setupSPAAssetServer(t, "/app/", frontend, ServerOptions{})
		srv.SetActiveWorktreeKey("wt-123")

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/app/", nil)
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, req)

		body := rr.Body.String()
		assert := assert.New(t)
		assert.Contains(body, `window.__kenn_forge_active_worktree_key="wt-123";`)
	})

	t.Run("no key means no served config", func(t *testing.T) {
		srv := setupSPAAssetServer(t, "/app/", frontend, ServerOptions{})
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

	srv := setupSPAAssetServer(t, "/", frontend, ServerOptions{})

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

func TestSPAFrameProtectionHeaders(t *testing.T) {
	frontend := fstest.MapFS{
		"index.html": &fstest.MapFile{
			Data: []byte(`<!DOCTYPE html><html><head></head><body>app</body></html>`),
		},
		"assets/index-DEADBEEF.js": &fstest.MapFile{
			Data: []byte(`console.log("bundle");`),
		},
	}

	srv := setupSPAAssetServer(t, "/", frontend, ServerOptions{})

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
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, tc.path, nil)
			rr := httptest.NewRecorder()
			srv.ServeHTTP(rr, req)
			assert := assert.New(t)
			assert.Equal(http.StatusOK, rr.Code)
			assert.Equal(spaFrameAncestorsPolicy, rr.Header().Get("Content-Security-Policy"))
			assert.Equal(spaXFrameOptions, rr.Header().Get("X-Frame-Options"))
		})
	}

	t.Run("asset", func(t *testing.T) {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/assets/index-DEADBEEF.js", nil)
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, req)
		assert := assert.New(t)
		assert.Equal(http.StatusOK, rr.Code)
		assert.Empty(rr.Header().Get("Content-Security-Policy"))
		assert.Empty(rr.Header().Get("X-Frame-Options"))
	})
}

func TestSPAAssetsCompressFullResponsesAndPreserveRanges(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	asset := []byte(strings.Repeat("export const payload = 'value';\n", 256))
	frontend := fstest.MapFS{
		"index.html":      &fstest.MapFile{Data: []byte("<!doctype html><html></html>")},
		"assets/index.js": &fstest.MapFile{Data: asset},
	}
	handler := newSPAAssetHandler(fs.FS(frontend), "/", nil)

	compressedRequest := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/assets/index.js", nil)
	compressedRequest.Header.Set("Accept-Encoding", "br")
	compressedResponse := httptest.NewRecorder()
	handler.ServeHTTP(compressedResponse, compressedRequest)

	require.Equal(http.StatusOK, compressedResponse.Code)
	assert.Equal("br", compressedResponse.Header().Get("Content-Encoding"))
	assert.Equal("Accept-Encoding", compressedResponse.Header().Get("Vary"))
	decoded, err := io.ReadAll(brotli.NewReader(compressedResponse.Body))
	require.NoError(err)
	assert.Equal(asset, decoded)

	rangeRequest := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/assets/index.js", nil)
	rangeRequest.Header.Set("Accept-Encoding", "br")
	rangeRequest.Header.Set("Range", "bytes=0-5")
	rangeResponse := httptest.NewRecorder()
	handler.ServeHTTP(rangeResponse, rangeRequest)

	assert.Equal(http.StatusPartialContent, rangeResponse.Code)
	assert.Empty(rangeResponse.Header().Get("Content-Encoding"))
	assert.Equal(asset[:6], rangeResponse.Body.Bytes())
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
			srv := setupSPAAssetServer(t, basePath, frontend, ServerOptions{})
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
