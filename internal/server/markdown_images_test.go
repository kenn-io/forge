package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/db"
	"go.kenn.io/middleman/internal/platform"
)

func repoBrowserRequest(
	t *testing.T,
	srv *Server,
	method, target string,
) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	return rr
}

func TestMarkdownImageRouteFetchesThroughProvider(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	const source = "https://github.com/user-attachments/assets/11111111-2222-3333-4444-555555555555"
	var gotOwner, gotSource string
	fetches := 0
	mock := &mockGH{getMarkdownImageFn: func(
		_ context.Context,
		owner, sourceURL string,
	) (platform.MarkdownImage, error) {
		fetches++
		gotOwner, gotSource = owner, sourceURL
		return platform.MarkdownImage{Content: []byte("png-bytes"), ContentType: "image/png"}, nil
	}}
	srv, database := setupTestServerWithMock(t, mock)
	srv.markdownImages = newMarkdownImageCache(t.TempDir())
	_, err := database.UpsertRepo(t.Context(), db.GitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)

	rr := repoBrowserRequest(t, srv, http.MethodGet,
		"/api/v1/repo/github/acme/widget/markdown-image?source="+url.QueryEscape(source),
	)

	require.Equal(http.StatusOK, rr.Code)
	assert.Equal("image/png", rr.Header().Get("Content-Type"))
	assert.Equal("private, max-age=31536000, immutable", rr.Header().Get("Cache-Control"))
	assert.Equal("nosniff", rr.Header().Get("X-Content-Type-Options"))
	assert.Equal("png-bytes", rr.Body.String())
	assert.Equal("acme", gotOwner)
	assert.Equal(source, gotSource)

	cached := repoBrowserRequest(t, srv, http.MethodGet,
		"/api/v1/repo/github/acme/widget/markdown-image?source="+url.QueryEscape(source),
	)
	require.Equal(http.StatusOK, cached.Code)
	assert.Equal("png-bytes", cached.Body.String())
	assert.Equal(1, fetches)
	entries, err := os.ReadDir(srv.markdownImages.root)
	require.NoError(err)
	assert.Len(entries, 1)
}
