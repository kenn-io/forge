package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/internal/db"
	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/testutil/reposeed"
	serverfake "go.kenn.io/forge/internal/testutil/serverfake"
	"go.kenn.io/forge/platform"
	platformgitea "go.kenn.io/forge/platform/gitea"
	platformgitlab "go.kenn.io/forge/platform/gitlab"
)

const markdownMediaSource = "https://github.com/user-attachments/assets/11111111-2222-3333-4444-555555555555"

func markdownMediaRequest(t *testing.T, srv *Server, target, byteRange string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, target, nil)
	if byteRange != "" {
		req.Header.Set("Range", byteRange)
	}
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	return rr
}

func TestMarkdownMediaRouteStreamsPartialContent(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	var gotOwner, gotRepo, gotSource, gotRange string
	mock := &serverfake.MockGH{OpenMarkdownMediaFn: func(
		_ context.Context, owner, repo, sourceURL, byteRange string,
	) (platform.MarkdownMedia, error) {
		gotOwner, gotRepo, gotSource, gotRange = owner, repo, sourceURL, byteRange
		return platform.MarkdownMedia{
			Body: io.NopCloser(strings.NewReader("\x00")), ContentType: "video/mp4",
			ContentLength: 1, ContentRange: "bytes 0-0/10", Partial: true,
		}, nil
	}}
	srv, _, _ := setupTestServerWithMock(t, mock)

	rr := markdownMediaRequest(t, srv,
		"/api/v1/repo/github/acme/widget/markdown-media?source="+url.QueryEscape(markdownMediaSource), "bytes=0-0")

	require.Equal(http.StatusPartialContent, rr.Code, rr.Body.String())
	assert.Equal("acme", gotOwner)
	assert.Equal("widget", gotRepo)
	assert.Equal(markdownMediaSource, gotSource)
	assert.Equal("bytes=0-0", gotRange)
	assert.Equal("video/mp4", rr.Header().Get("Content-Type"))
	assert.Equal("1", rr.Header().Get("Content-Length"))
	assert.Equal("bytes 0-0/10", rr.Header().Get("Content-Range"))
	assert.Equal("bytes", rr.Header().Get("Accept-Ranges"))
	assert.Equal("nosniff", rr.Header().Get("X-Content-Type-Options"))
	assert.Equal("private, max-age=31536000, immutable", rr.Header().Get("Cache-Control"))
	assert.Equal("\x00", rr.Body.String())
}

func TestMarkdownMediaRouteStreamsBodyLargerThanImageCap(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	const size = 26 << 20
	closed := false
	mock := &serverfake.MockGH{OpenMarkdownMediaFn: func(
		context.Context, string, string, string, string,
	) (platform.MarkdownMedia, error) {
		return platform.MarkdownMedia{
			Body:        closeRecorder{Reader: bytes.NewReader(make([]byte, size)), closed: &closed},
			ContentType: "video/webm", ContentLength: -1,
		}, nil
	}}
	srv, _, _ := setupTestServerWithMock(t, mock)

	rr := markdownMediaRequest(t, srv,
		"/api/v1/repo/github/acme/widget/markdown-media?source="+url.QueryEscape(markdownMediaSource), "")

	require.Equal(http.StatusOK, rr.Code)
	require.Equal(size, rr.Body.Len())
	require.Empty(rr.Header().Get("Content-Length"))
	require.True(closed, "the route must close the upstream stream")
}

func TestMarkdownMediaRouteMapsMediaErrors(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	tests := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"not a video", &platform.Error{Code: platform.ErrCodeUnsupportedMediaType, Provider: platform.KindGitHub}, http.StatusUnsupportedMediaType, "unsupportedMediaType"},
		{"range outside the file", &platform.Error{Code: platform.ErrCodeRangeNotSatisfiable, Provider: platform.KindGitHub}, http.StatusRequestedRangeNotSatisfiable, "rangeNotSatisfiable"},
		{"foreign source", &platform.Error{Code: platform.ErrCodeInvalidArgument, Provider: platform.KindGitHub, Field: "source"}, http.StatusBadRequest, "badRequest"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mock := &serverfake.MockGH{OpenMarkdownMediaFn: func(
				context.Context, string, string, string, string,
			) (platform.MarkdownMedia, error) {
				return platform.MarkdownMedia{}, tc.err
			}}
			srv, _, _ := setupTestServerWithMock(t, mock)

			rr := markdownMediaRequest(t, srv,
				"/api/v1/repo/github/acme/widget/markdown-media?source="+url.QueryEscape(markdownMediaSource), "bytes=0-0")

			require.Equal(t, tc.status, rr.Code, rr.Body.String())
			var problem struct {
				Code string `json:"code"`
			}
			require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &problem))
			assert.Equal(t, tc.code, problem.Code)
		})
	}
}

func TestMarkdownMediaRouteOnGitLabHostRoute(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	gitlabServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal("/api/v4/projects/42/uploads/secret/DEMO.MOV", r.URL.EscapedPath())
		assert.Equal("bytes=0-0", r.Header.Get("Range"))
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Range", "bytes 0-0/4096")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte{7})
	}))
	t.Cleanup(gitlabServer.Close)
	provider, err := platformgitlab.NewClient(
		"gitlab.example.com",
		serverfake.TestTokenSource("gitlab-token"),
		platformgitlab.WithBaseURLForTesting(gitlabServer.URL+"/api/v4"),
		platformgitlab.WithoutRetriesForTesting(), platformgitlab.WithTransport(http.DefaultTransport),
	)
	require.NoError(err)
	srv := newSingleProviderServer(t, provider, db.RepoIdentity{
		Platform: "gitlab", PlatformHost: "gitlab.example.com", Key: platform.RepositoryIDKey(42),
		Owner: "group", Name: "project", RepoPath: "group/project",
	})

	rr := markdownMediaRequest(t, srv,
		"/api/v1/host/gitlab.example.com/repo/gitlab/group/project/markdown-media?source="+
			url.QueryEscape(gitlabServer.URL+"/group/project/uploads/secret/DEMO.MOV"), "bytes=0-0")

	require.Equal(http.StatusPartialContent, rr.Code, rr.Body.String())
	assert.Equal("video/quicktime", rr.Header().Get("Content-Type"))
	assert.Equal("bytes 0-0/4096", rr.Header().Get("Content-Range"))
	assert.Equal([]byte{7}, rr.Body.Bytes())
}

func TestMarkdownMediaRouteRequiresCapability(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		assert.Fail(t, "a provider without media support must not be called")
	}))
	t.Cleanup(upstream.Close)
	provider, err := platformgitea.NewClient("gitea.example.com", serverfake.TestTokenSource("gitea-token"),
		platformgitea.WithBaseURL(upstream.URL, true), platformgitea.WithTransport(http.DefaultTransport))
	require.NoError(err)
	srv := newSingleProviderServer(t, provider, db.RepoIdentity{
		Platform: "gitea", PlatformHost: "gitea.example.com", Key: platform.RepositoryIDKey(7),
		Owner: "acme", Name: "widgets", RepoPath: "acme/widgets",
	})

	rr := markdownMediaRequest(t, srv,
		"/api/v1/host/gitea.example.com/repo/gitea/acme/widgets/markdown-media?source="+
			url.QueryEscape("https://gitea.example.com/acme/widgets/attachments/u1"), "")

	require.Equal(http.StatusConflict, rr.Code, rr.Body.String())
	require.Contains(rr.Body.String(), `"code":"unsupportedCapability"`)
}

func newSingleProviderServer(t *testing.T, provider platform.Provider, identity db.RepoIdentity) *Server {
	t.Helper()
	registry, err := platform.NewRegistry(provider)
	require.NoError(t, err)
	database := dbtest.Open(t)
	_, err = reposeed.Seed(t.Context(), database, identity)
	require.NoError(t, err)
	syncer := ghclient.NewSyncerWithRegistry(registry, database, nil, nil, time.Minute, nil, nil)
	t.Cleanup(syncer.Stop)
	srv := New(database, syncer, nil, "/", nil, ServerOptions{})
	t.Cleanup(func() { serverfake.GracefulShutdown(t, srv) })
	return srv
}

type closeRecorder struct {
	io.Reader
	closed *bool
}

func (c closeRecorder) Close() error {
	*c.closed = true
	return nil
}

// blockedMediaBody returns its first chunk and then blocks until released,
// like an upstream that is still sending the rest of a long video.
type blockedMediaBody struct {
	first   []byte
	release chan struct{}
}

func (b *blockedMediaBody) Read(p []byte) (int, error) {
	if len(b.first) > 0 {
		n := copy(p, b.first)
		b.first = b.first[n:]
		return n, nil
	}
	<-b.release
	return 0, io.EOF
}

func (*blockedMediaBody) Close() error { return nil }

type headerSignalRecorder struct {
	*httptest.ResponseRecorder
	once  sync.Once
	wrote chan struct{}
}

func (r *headerSignalRecorder) WriteHeader(code int) {
	r.ResponseRecorder.WriteHeader(code)
	r.once.Do(func() { close(r.wrote) })
}

func (r *headerSignalRecorder) Write(p []byte) (int, error) {
	r.once.Do(func() { close(r.wrote) })
	return r.ResponseRecorder.Write(p)
}

func TestMarkdownMediaRouteSendsHeadersBeforeBodyEndsWhenCompressionAccepted(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	release := make(chan struct{})
	mock := &serverfake.MockGH{OpenMarkdownMediaFn: func(
		context.Context, string, string, string, string,
	) (platform.MarkdownMedia, error) {
		return platform.MarkdownMedia{
			Body:        &blockedMediaBody{first: make([]byte, 64<<10), release: release},
			ContentType: "video/mp4", ContentLength: -1,
		}, nil
	}}
	srv, _, _ := setupTestServerWithMock(t, mock)
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet,
		"/api/v1/repo/github/acme/widget/markdown-media?source="+url.QueryEscape(markdownMediaSource), nil)
	// fetch() always advertises compression; the media route must not hold
	// the stream back to compress it.
	req.Header.Set("Accept-Encoding", "gzip, deflate, br, zstd")
	rec := &headerSignalRecorder{ResponseRecorder: httptest.NewRecorder(), wrote: make(chan struct{})}
	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.ServeHTTP(rec, req)
	}()

	select {
	case <-rec.wrote:
	case <-time.After(5 * time.Second):
		close(release)
		<-done
		require.Fail(t, "response headers waited for the media body to finish")
		return
	}
	close(release)
	<-done
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Empty(t, rec.Header().Get("Content-Encoding"))
}

func TestMarkdownMediaRouteServesWholeFileForMultiRange(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	gotRange := "unset"
	mock := &serverfake.MockGH{OpenMarkdownMediaFn: func(
		_ context.Context, _, _, _, byteRange string,
	) (platform.MarkdownMedia, error) {
		gotRange = byteRange
		return platform.MarkdownMedia{
			Body: io.NopCloser(strings.NewReader("whole")), ContentType: "video/mp4", ContentLength: 5,
		}, nil
	}}
	srv, _, _ := setupTestServerWithMock(t, mock)

	rr := markdownMediaRequest(t, srv,
		"/api/v1/repo/github/acme/widget/markdown-media?source="+url.QueryEscape(markdownMediaSource), "bytes=0-1,5-6")

	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	assert.Empty(t, gotRange, "a multi-range request must not reach the provider")
	assert.Empty(t, rr.Header().Get("Content-Range"))
	assert.Equal(t, "whole", rr.Body.String())
}

// pipedMediaServer serves markdown media whose upstream body the test writes.
func pipedMediaServer(t *testing.T) (*Server, *io.PipeWriter) {
	t.Helper()
	body, upstream := io.Pipe()
	mock := &serverfake.MockGH{OpenMarkdownMediaFn: func(
		context.Context, string, string, string, string,
	) (platform.MarkdownMedia, error) {
		return platform.MarkdownMedia{Body: body, ContentType: "video/mp4", ContentLength: -1}, nil
	}}
	srv, _, _ := setupTestServerWithMock(t, mock)
	// Cleanups run last-first: end the upstream before anything waits on the
	// handler that reads it.
	t.Cleanup(func() { _ = upstream.Close() })
	return srv, upstream
}

func pipedMediaHTTPServer(t *testing.T) (*httptest.Server, *io.PipeWriter) {
	t.Helper()
	srv, upstream := pipedMediaServer(t)
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	t.Cleanup(func() { _ = upstream.Close() })
	return ts, upstream
}

func getMarkdownMedia(t *testing.T, baseURL string) (*http.Response, error) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet,
		baseURL+"/api/v1/repo/github/acme/widget/markdown-media?source="+url.QueryEscape(markdownMediaSource), nil)
	require.NoError(t, err)
	type result struct {
		resp *http.Response
		err  error
	}
	got := make(chan result, 1)
	go func() {
		resp, err := http.DefaultClient.Do(req)
		got <- result{resp, err}
	}()
	select {
	case r := <-got:
		if r.resp != nil {
			t.Cleanup(func() { _ = r.resp.Body.Close() })
		}
		return r.resp, r.err
	case <-time.After(5 * time.Second):
		require.FailNow(t, "response headers waited for the media body")
		return nil, nil
	}
}

func TestMarkdownMediaRouteSendsHeadersBeforeUpstreamSendsBody(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	ts, _ := pipedMediaHTTPServer(t)

	resp, err := getMarkdownMedia(t, ts.URL)

	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "video/mp4", resp.Header.Get("Content-Type"))
}

func TestMarkdownMediaRouteAbortsWhenUpstreamFailsMidStream(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	ts, upstream := pipedMediaHTTPServer(t)
	resp, err := getMarkdownMedia(t, ts.URL)
	require.NoError(t, err)

	_, _ = io.WriteString(upstream, "partial")
	_ = upstream.CloseWithError(io.ErrUnexpectedEOF)
	_, err = io.ReadAll(resp.Body)

	// A clean end would let the browser cache a truncated video as whole.
	require.Error(t, err)
}

func TestServerShutdownEndsActiveMarkdownMediaStream(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	srv, _ := pipedMediaServer(t)
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()
	resp, err := getMarkdownMedia(t, "http://"+ln.Addr().String())
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	require.NoError(t, srv.Shutdown(ctx))

	_, err = io.ReadAll(resp.Body)
	require.Error(t, err)
	require.ErrorIs(t, <-serveErr, http.ErrServerClosed)
}
