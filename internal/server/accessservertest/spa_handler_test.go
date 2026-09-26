package accessservertest

import (
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/andybalholm/brotli"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/server"
	"go.kenn.io/forge/internal/server/compression"
	servertest "go.kenn.io/forge/internal/testutil/servertest"
)

func TestSPAFrameProtectionHeaders(t *testing.T) {
	frontend := fstest.MapFS{
		"index.html": &fstest.MapFile{
			Data: []byte(`<!DOCTYPE html><html><head></head><body>app</body></html>`),
		},
		"assets/index-DEADBEEF.js": &fstest.MapFile{
			Data: []byte(`console.log("bundle");`),
		},
	}

	srv := servertest.SetupSPAAssetServer(t, "/", frontend, server.ServerOptions{})

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
			assert.Equal(compression.SpaFrameAncestorsPolicy, rr.Header().Get("Content-Security-Policy"))
			assert.Equal(compression.SpaXFrameOptions, rr.Header().Get("X-Frame-Options"))
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
	handler := compression.NewSPAAssetHandler(fs.FS(frontend), "/", nil)

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
