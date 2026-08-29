package server

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
)

func TestSPAAssetsCompressFullResponsesAndPreserveRanges(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	asset := []byte(strings.Repeat("export const payload = 'value';\n", 256))
	frontend := fstest.MapFS{
		"index.html":      &fstest.MapFile{Data: []byte("<!doctype html><html></html>")},
		"assets/index.js": &fstest.MapFile{Data: asset},
	}
	handler := newSPAAssetHandler(fs.FS(frontend), "/", nil)

	compressedRequest := httptest.NewRequest(http.MethodGet, "/assets/index.js", nil)
	compressedRequest.Header.Set("Accept-Encoding", "br")
	compressedResponse := httptest.NewRecorder()
	handler.ServeHTTP(compressedResponse, compressedRequest)

	require.Equal(http.StatusOK, compressedResponse.Code)
	assert.Equal("br", compressedResponse.Header().Get("Content-Encoding"))
	assert.Equal("Accept-Encoding", compressedResponse.Header().Get("Vary"))
	decoded, err := io.ReadAll(brotli.NewReader(compressedResponse.Body))
	require.NoError(err)
	assert.Equal(asset, decoded)

	rangeRequest := httptest.NewRequest(http.MethodGet, "/assets/index.js", nil)
	rangeRequest.Header.Set("Accept-Encoding", "br")
	rangeRequest.Header.Set("Range", "bytes=0-5")
	rangeResponse := httptest.NewRecorder()
	handler.ServeHTTP(rangeResponse, rangeRequest)

	assert.Equal(http.StatusPartialContent, rangeResponse.Code)
	assert.Empty(rangeResponse.Header().Get("Content-Encoding"))
	assert.Equal(asset[:6], rangeResponse.Body.Bytes())
}
