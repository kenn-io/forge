package gitlab

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/platform"
)

func newMarkdownMediaTestClient(t *testing.T, server *httptest.Server) *Client {
	t.Helper()
	client, err := NewClient(
		server.Listener.Addr().String(),
		testTokenSource("gitlab-token"),
		WithBaseURLForTesting(server.URL+"/api/v4"), WithTransport(http.DefaultTransport))
	require.NoError(t, err)
	return client
}

func markdownMediaRef(server *httptest.Server) platform.RepoRef {
	return platform.RepoRef{
		Platform: platform.KindGitLab, Host: server.Listener.Addr().String(),
		RepoPath: "group/project", Key: platform.RepositoryIDKey(42),
	}
}

func TestOpenMarkdownMediaTypesByExtension(t *testing.T) {
	tests := []struct {
		filename    string
		contentType string
	}{
		{"demo.mp4", "video/mp4"},
		{"DEMO.MOV", "video/quicktime"},
		{"clip.m4v", "video/mp4"},
		{"clip.webm", "video/webm"},
		{"clip.ogv", "video/ogg"},
	}
	for _, tc := range tests {
		t.Run(tc.filename, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "/api/v4/projects/42/uploads/0123456789abcdef/"+tc.filename, r.URL.Path)
				assert.Equal(t, "gitlab-token", r.Header.Get("PRIVATE-TOKEN"))
				w.Header().Set("Content-Type", "application/octet-stream")
				w.Header().Set("Content-Disposition", "attachment")
				_, _ = w.Write([]byte("video"))
			}))
			t.Cleanup(server.Close)

			media, err := newMarkdownMediaTestClient(t, server).OpenMarkdownMedia(t.Context(), markdownMediaRef(server),
				server.URL+"/group/project/uploads/0123456789abcdef/"+tc.filename, "")
			require.NoError(t, err)
			defer media.Body.Close()

			assert.Equal(t, tc.contentType, media.ContentType)
			body, err := io.ReadAll(media.Body)
			require.NoError(t, err)
			assert.Equal(t, "video", string(body))
		})
	}
}

func TestOpenMarkdownMediaRejectsNonVideoExtension(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		assert.Fail(t, "a non-video upload must be rejected before any request")
	}))
	t.Cleanup(server.Close)

	_, err := newMarkdownMediaTestClient(t, server).OpenMarkdownMedia(t.Context(), markdownMediaRef(server),
		server.URL+"/group/project/uploads/0123456789abcdef/diagram.png", "")

	platformErr, ok := errors.AsType[*platform.Error](err)
	require.True(t, ok, "expected *platform.Error, got %v", err)
	assert.Equal(t, platform.ErrCodeUnsupportedMediaType, platformErr.Code)
}

func TestOpenMarkdownMediaRejectsOtherRepositoryUpload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		assert.Fail(t, "an upload from another repository must not be fetched")
	}))
	t.Cleanup(server.Close)

	_, err := newMarkdownMediaTestClient(t, server).OpenMarkdownMedia(t.Context(), markdownMediaRef(server),
		server.URL+"/other/project/uploads/0123456789abcdef/demo.mp4", "")

	platformErr, ok := errors.AsType[*platform.Error](err)
	require.True(t, ok, "expected *platform.Error, got %v", err)
	assert.Equal(t, platform.ErrCodeInvalidArgument, platformErr.Code)
}

func TestOpenMarkdownMediaForwardsRange(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "bytes=100-", r.Header.Get("Range"))
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Range", "bytes 100-199/200")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(make([]byte, 100))
	}))
	t.Cleanup(server.Close)

	media, err := newMarkdownMediaTestClient(t, server).OpenMarkdownMedia(t.Context(), markdownMediaRef(server),
		server.URL+"/group/project/uploads/0123456789abcdef/demo.webm", "bytes=100-")
	require.NoError(t, err)
	defer media.Body.Close()

	assert.True(t, media.Partial)
	assert.Equal(t, "bytes 100-199/200", media.ContentRange)
	assert.Equal(t, int64(100), media.ContentLength)
}

func TestOpenMarkdownMediaMapsRangeNotSatisfiable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
	}))
	t.Cleanup(server.Close)

	_, err := newMarkdownMediaTestClient(t, server).OpenMarkdownMedia(t.Context(), markdownMediaRef(server),
		server.URL+"/group/project/uploads/0123456789abcdef/demo.mp4", "bytes=900-")

	platformErr, ok := errors.AsType[*platform.Error](err)
	require.True(t, ok, "expected *platform.Error, got %v", err)
	assert.Equal(t, platform.ErrCodeRangeNotSatisfiable, platformErr.Code)
}
