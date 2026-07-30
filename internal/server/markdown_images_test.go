package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/platform"
	platformgitlab "go.kenn.io/forge/internal/platform/gitlab"
	"go.kenn.io/forge/internal/testutil/dbtest"
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
		owner, _, sourceURL string,
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

// Production GitHub hosts are served by a RoutedClient, not a bare client, so
// this drives the markdown-image route through one. The route here is
// repo-scoped with no host fallback: the credential that owns acme/widget must
// serve the fetch, and the capability probe must not report the whole host as
// unable to read markdown images.
func TestMarkdownImageRouteFetchesThroughRoutedRepositoryCredential(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	const source = "https://github.com/user-attachments/assets/11111111-2222-3333-4444-555555555555"
	var gotOwner, gotRepo string
	mock := &mockGH{getMarkdownImageFn: func(
		_ context.Context,
		owner, repo, _ string,
	) (platform.MarkdownImage, error) {
		gotOwner, gotRepo = owner, repo
		return platform.MarkdownImage{
			Content: []byte("routed-bytes"), ContentType: "image/png",
		}, nil
	}}
	router, err := ghclient.NewHostRouter("github.com", &ghclient.Route{
		Key:    ghclient.RouteKey{Host: "github.com", Owner: "acme", Name: "widget"},
		Client: mock,
	})
	require.NoError(err)
	routed, err := ghclient.NewRoutedClient(router)
	require.NoError(err)

	database := dbtest.Open(t)
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": routed},
		database, nil, defaultTestRepos, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	srv := New(database, syncer, nil, "/", nil, ServerOptions{})
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	srv.markdownImages = newMarkdownImageCache(t.TempDir())
	_, err = database.UpsertRepo(
		t.Context(), db.GitHubRepoIdentity("github.com", "acme", "widget"),
	)
	require.NoError(err)

	rr := repoBrowserRequest(t, srv, http.MethodGet,
		"/api/v1/repo/github/acme/widget/markdown-image?source="+url.QueryEscape(source),
	)

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	assert.Equal("image/png", rr.Header().Get("Content-Type"))
	assert.Equal("routed-bytes", rr.Body.String())
	assert.Equal("acme", gotOwner)
	assert.Equal("widget", gotRepo)
}

func TestMarkdownImageRouteMapsProviderDeadlineToUpstreamError(t *testing.T) {
	mock := &mockGH{getMarkdownImageFn: func(
		context.Context,
		string,
		string,
		string,
	) (platform.MarkdownImage, error) {
		return platform.MarkdownImage{}, context.DeadlineExceeded
	}}
	srv, database := setupTestServerWithMock(t, mock)
	srv.markdownImages = newMarkdownImageCache(t.TempDir())
	_, err := database.UpsertRepo(t.Context(), db.GitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(t, err)

	rr := repoBrowserRequest(
		t,
		srv,
		http.MethodGet,
		"/api/v1/repo/github/acme/widget/markdown-image?source="+
			url.QueryEscape("https://github.com/user-attachments/assets/11111111-2222-3333-4444-555555555555"),
	)

	require.Equal(t, http.StatusBadGateway, rr.Code, rr.Body.String())
}

func TestMarkdownImageRouteMapsGitLabServerErrorToUpstreamError(t *testing.T) {
	require := require.New(t)
	gitlabServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v4/projects/42/uploads/secret/private.png", r.URL.EscapedPath())
		assert.Equal(t, "gitlab-token", r.Header.Get("PRIVATE-TOKEN"))
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	t.Cleanup(gitlabServer.Close)

	provider, err := platformgitlab.NewClient(
		"gitlab.example.com",
		testTokenSource("gitlab-token"),
		platformgitlab.WithBaseURLForTesting(gitlabServer.URL+"/api/v4"),
		platformgitlab.WithoutRetriesForTesting(),
	)
	require.NoError(err)
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)
	database := dbtest.Open(t)
	_, err = database.UpsertRepo(t.Context(), db.RepoIdentity{
		Platform:       "gitlab",
		PlatformHost:   "gitlab.example.com",
		PlatformRepoID: "42",
		Owner:          "group",
		Name:           "project",
		RepoPath:       "group/project",
	})
	require.NoError(err)
	syncer := ghclient.NewSyncerWithRegistry(registry, database, nil, nil, time.Minute, nil, nil)
	t.Cleanup(syncer.Stop)
	srv := New(database, syncer, nil, "/", nil, ServerOptions{})
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	srv.markdownImages = newMarkdownImageCache(t.TempDir())

	rr := repoBrowserRequest(
		t,
		srv,
		http.MethodGet,
		"/api/v1/host/gitlab.example.com/repo/gitlab/group/project/markdown-image?source="+
			url.QueryEscape(gitlabServer.URL+"/group/project/uploads/secret/private.png"),
	)

	require.Equal(http.StatusBadGateway, rr.Code, rr.Body.String())
}

func TestMarkdownImageRouteResolvesOpaqueGitLabProjectID(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	imageBytes := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}
	var paths []string
	gitlabServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.EscapedPath())
		assert.Equal("gitlab-token", r.Header.Get("PRIVATE-TOKEN"))
		switch r.URL.EscapedPath() {
		case "/api/v4/projects/group%2Fproject":
			w.Header().Set("Content-Type", "application/json")
			_, err := w.Write([]byte(`{
				"id": 42,
				"path": "project",
				"path_with_namespace": "group/project",
				"name": "Project"
			}`))
			assert.NoError(err)
		case "/api/v4/projects/42/uploads/secret/private.png":
			w.Header().Set("Content-Type", "image/png")
			_, err := w.Write(imageBytes)
			assert.NoError(err)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(gitlabServer.Close)

	provider, err := platformgitlab.NewClient(
		"gitlab.example.com",
		testTokenSource("gitlab-token"),
		platformgitlab.WithBaseURLForTesting(gitlabServer.URL+"/api/v4"),
		platformgitlab.WithoutRetriesForTesting(),
	)
	require.NoError(err)
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)
	database := dbtest.Open(t)
	_, err = database.UpsertRepo(t.Context(), db.RepoIdentity{
		Platform:       "gitlab",
		PlatformHost:   "gitlab.example.com",
		PlatformRepoID: "gid://gitlab/Project/4242",
		Owner:          "group",
		Name:           "project",
		RepoPath:       "group/project",
	})
	require.NoError(err)
	syncer := ghclient.NewSyncerWithRegistry(registry, database, nil, nil, time.Minute, nil, nil)
	t.Cleanup(syncer.Stop)
	srv := New(database, syncer, nil, "/", nil, ServerOptions{})
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	srv.markdownImages = newMarkdownImageCache(t.TempDir())
	source := gitlabServer.URL + "/group/project/uploads/secret/private.png"

	rr := repoBrowserRequest(
		t,
		srv,
		http.MethodGet,
		"/api/v1/host/gitlab.example.com/repo/gitlab/group/project/markdown-image?source="+url.QueryEscape(source),
	)

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	assert.Equal("image/png", rr.Header().Get("Content-Type"))
	assert.Equal(imageBytes, rr.Body.Bytes())
	assert.Equal([]string{
		"/api/v4/projects/group%2Fproject",
		"/api/v4/projects/42/uploads/secret/private.png",
	}, paths)
}
