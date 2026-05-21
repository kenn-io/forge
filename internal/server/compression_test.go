package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/andybalholm/brotli"
	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/klauspost/compress/zstd"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/middleman/internal/testutil"
	"github.com/wesm/middleman/internal/testutil/dbtest"
)

func TestHumaResponseCompressionNegotiatesZstdAndBrotli(t *testing.T) {
	mux := http.NewServeMux()
	api := humago.NewWithPrefix(mux, "/api/v1", apiConfig("/"))
	api.UseMiddleware(newResponseCompressionMiddleware(128))
	registerCompressionTestRoutes(api)

	cases := []struct {
		name           string
		acceptEncoding string
		wantEncoding   string
		decode         func(t *testing.T, body io.Reader) string
	}{
		{
			name:           "brotli preferred when both encodings are accepted equally",
			acceptEncoding: "br, zstd",
			wantEncoding:   "br",
			decode:         decodeBrotliBody,
		},
		{
			name:           "brotli selected when client gives it higher quality",
			acceptEncoding: "zstd;q=0.4, br;q=1.0",
			wantEncoding:   "br",
			decode:         decodeBrotliBody,
		},
		{
			name:           "gzip is ignored",
			acceptEncoding: "gzip",
			wantEncoding:   "",
			decode: func(t *testing.T, body io.Reader) string {
				t.Helper()
				data, err := io.ReadAll(body)
				require.NoError(t, err)
				return string(data)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/payload", nil)
			req.Header.Set("Accept-Encoding", tc.acceptEncoding)
			rr := httptest.NewRecorder()

			mux.ServeHTTP(rr, req)

			assert := assert.New(t)
			assert.Equal(http.StatusOK, rr.Code)
			assert.Equal(tc.wantEncoding, rr.Header().Get("Content-Encoding"))
			assert.Equal("Accept-Encoding", rr.Header().Get("Vary"))
			assert.Empty(rr.Header().Get("Content-Length"))
			assert.Contains(tc.decode(t, rr.Body), strings.Repeat("frontend-payload ", 20))
		})
	}
}

func TestHumaResponseCompressionSkipsSmallResponses(t *testing.T) {
	mux := http.NewServeMux()
	api := humago.NewWithPrefix(mux, "/api/v1", apiConfig("/"))
	api.UseMiddleware(newResponseCompressionMiddleware(128))
	registerCompressionTestRoutes(api)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/small", nil)
	req.Header.Set("Accept-Encoding", "zstd, br")
	rr := httptest.NewRecorder()

	mux.ServeHTTP(rr, req)

	assert := assert.New(t)
	assert.Equal(http.StatusOK, rr.Code)
	assert.Empty(rr.Header().Get("Content-Encoding"))
	assert.Equal("Accept-Encoding", rr.Header().Get("Vary"))
	assert.Contains(rr.Body.String(), `"text":"tiny"`)
}

func TestHumaResponseCompressionPreservesHumagoUnwrap(t *testing.T) {
	mux := http.NewServeMux()
	api := humago.NewWithPrefix(mux, "/api/v1", apiConfig("/"))
	api.UseMiddleware(newResponseCompressionMiddleware(128))
	api.UseMiddleware(func(ctx huma.Context, next func(huma.Context)) {
		_, w := humago.Unwrap(ctx)
		w.Header().Set("X-Unwrapped", "true")
		next(ctx)
	})
	registerCompressionTestRoutes(api)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/payload", nil)
	req.Header.Set("Accept-Encoding", "br")
	rr := httptest.NewRecorder()

	mux.ServeHTTP(rr, req)

	assert.Equal(t, "true", rr.Header().Get("X-Unwrapped"))
	assert.Equal(t, "br", rr.Header().Get("Content-Encoding"))
}

func TestHumaResponseCompressionStreamsUncompressedWhenBodyExceedsCap(t *testing.T) {
	mux := http.NewServeMux()
	api := humago.NewWithPrefix(mux, "/api/v1", apiConfig("/"))
	api.UseMiddleware(newResponseCompressionMiddleware(128))
	registerCompressionTestRoutes(api)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/huge", nil)
	req.Header.Set("Accept-Encoding", "br")
	rr := httptest.NewRecorder()

	mux.ServeHTTP(rr, req)

	assert := assert.New(t)
	assert.Equal(http.StatusOK, rr.Code)
	assert.Empty(rr.Header().Get("Content-Encoding"))
	assert.Equal("Accept-Encoding", rr.Header().Get("Vary"))
	assert.Contains(rr.Body.String(), strings.Repeat("huge-payload ", 20))
}

func TestServerUsesResponseCompressionMiddleware(t *testing.T) {
	database := dbtest.Open(t)
	_, err := testutil.SeedFixtures(t.Context(), database)
	require.NoError(t, err)
	srv := New(database, nil, nil, "/", nil, ServerOptions{})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/pulls", nil)
	req.Header.Set("Accept-Encoding", "zstd")
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	assert := assert.New(t)
	assert.Equal(http.StatusOK, rr.Code)
	assert.Equal("zstd", rr.Header().Get("Content-Encoding"))
	assert.Equal("Accept-Encoding", rr.Header().Get("Vary"))
	assert.Contains(decodeZstdBody(t, rr.Body), "Add widget caching layer")
}

func registerCompressionTestRoutes(api huma.API) {
	type output struct {
		Body struct {
			Text string `json:"text"`
		}
	}

	huma.Get(api, "/payload", func(ctx context.Context, input *struct{}) (*output, error) {
		resp := &output{}
		resp.Body.Text = strings.Repeat("frontend-payload ", 300)
		return resp, nil
	})
	huma.Get(api, "/small", func(ctx context.Context, input *struct{}) (*output, error) {
		resp := &output{}
		resp.Body.Text = "tiny"
		return resp, nil
	})
	huma.Get(api, "/huge", func(ctx context.Context, input *struct{}) (*output, error) {
		resp := &output{}
		resp.Body.Text = strings.Repeat("huge-payload ", 100_000)
		return resp, nil
	})
}

func decodeZstdBody(t *testing.T, body io.Reader) string {
	t.Helper()
	reader, err := zstd.NewReader(body)
	require.NoError(t, err)
	defer reader.Close()
	data, err := io.ReadAll(reader)
	require.NoError(t, err)
	return string(data)
}

func decodeBrotliBody(t *testing.T, body io.Reader) string {
	t.Helper()
	data, err := io.ReadAll(brotli.NewReader(body))
	require.NoError(t, err)
	return string(data)
}
