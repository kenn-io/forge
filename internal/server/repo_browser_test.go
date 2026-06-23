package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/db"
	"go.kenn.io/middleman/internal/gitclone"
	ghclient "go.kenn.io/middleman/internal/github"
)

func TestRepoBrowserRefsUsesRepoPathIdentity(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	srv, _ := setupRepoBrowserServer(t, "gitlab", "gitlab.example.com", "group/subgroup/project")

	rr := repoBrowserRequest(t, srv, http.MethodGet,
		"/api/v1/host/gitlab.example.com/repo/gitlab/group/project/browser/refs?repo_path="+url.QueryEscape("group/subgroup/project"),
	)

	require.Equal(http.StatusOK, rr.Code)
	var body repoBrowserRefsResponse
	require.NoError(json.Unmarshal(rr.Body.Bytes(), &body))
	assert.Equal("gitlab", body.Repo.Provider)
	assert.Equal("gitlab.example.com", body.Repo.PlatformHost)
	assert.Equal("group/subgroup/project", body.Repo.RepoPath)
	assert.Equal("main", body.DefaultRef.Name)
	assert.Equal(gitclone.RepoBrowserRefBranch, body.DefaultRef.Type)
}

func TestRepoBrowserRefsUsesProviderRouteWhenRepoPathMissing(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	srv, _ := setupRepoBrowserServer(t, "github", "github.com", "acme/widgets")

	rr := repoBrowserRequest(t, srv, http.MethodGet,
		"/api/v1/repo/github/acme/widgets/browser/refs",
	)

	require.Equal(http.StatusOK, rr.Code)
	var body repoBrowserRefsResponse
	require.NoError(json.Unmarshal(rr.Body.Bytes(), &body))
	assert.Equal("github", body.Repo.Provider)
	assert.Equal("github.com", body.Repo.PlatformHost)
	assert.Equal("acme/widgets", body.Repo.RepoPath)
	assert.Equal("main", body.DefaultRef.Name)
}

func TestRepoBrowserBlobReturnsTypedLargeAndBinaryStates(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	srv, work := setupRepoBrowserServer(t, "github", "github.com", "acme/widgets")
	require.NoError(os.WriteFile(filepath.Join(work, "large.txt"), make([]byte, gitclone.RepoBrowserBlobSizeLimit+1), 0o644))
	require.NoError(os.WriteFile(filepath.Join(work, "bin.dat"), []byte{0, 1, 2, 3}, 0o644))
	serverRepoBrowserGit(t, work, "add", ".")
	serverRepoBrowserGit(t, work, "commit", "-m", "blob states")
	serverRepoBrowserGit(t, work, "push", "origin", "main")

	large := repoBrowserRequest(t, srv, http.MethodGet,
		"/api/v1/repo/github/acme/widgets/browser/blob?repo_path=acme%2Fwidgets&ref_type=branch&ref_name=main&path=large.txt",
	)
	require.Equal(http.StatusOK, large.Code)
	var largeBody repoBrowserBlobResponse
	require.NoError(json.Unmarshal(large.Body.Bytes(), &largeBody))
	assert.True(largeBody.Blob.TooLarge)
	assert.False(largeBody.Blob.Binary)
	assert.Empty(largeBody.Blob.Content)

	binary := repoBrowserRequest(t, srv, http.MethodGet,
		"/api/v1/repo/github/acme/widgets/browser/blob?repo_path=acme%2Fwidgets&ref_type=branch&ref_name=main&path=bin.dat",
	)
	require.Equal(http.StatusOK, binary.Code)
	var binaryBody repoBrowserBlobResponse
	require.NoError(json.Unmarshal(binary.Body.Bytes(), &binaryBody))
	assert.True(binaryBody.Blob.Binary)
	assert.False(binaryBody.Blob.TooLarge)
	assert.Empty(binaryBody.Blob.Content)
}

func TestRepoBrowserBranchRefReportsStaleRequestedSHA(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	srv, work := setupRepoBrowserServer(t, "github", "github.com", "acme/widgets")
	oldSHA := testGitSHA(t, work, "main")
	require.NoError(os.WriteFile(filepath.Join(work, "README.md"), []byte("# Test\n\nUpdated\n"), 0o644))
	serverRepoBrowserGit(t, work, "add", ".")
	serverRepoBrowserGit(t, work, "commit", "-m", "move main")
	currentSHA := testGitSHA(t, work, "main")
	serverRepoBrowserGit(t, work, "push", "origin", "main")

	tree := repoBrowserRequest(t, srv, http.MethodGet,
		"/api/v1/repo/github/acme/widgets/browser/tree?ref_type=branch&ref_name=main&ref_sha="+url.QueryEscape(oldSHA),
	)

	require.Equal(http.StatusOK, tree.Code)
	var body repoBrowserTreeResponse
	require.NoError(json.Unmarshal(tree.Body.Bytes(), &body))
	assert.Equal(gitclone.RepoBrowserRefBranch, body.Ref.Type)
	assert.Equal("main", body.Ref.Name)
	assert.Equal(currentSHA, body.Ref.SHA)
	assert.Equal(oldSHA, body.Ref.RequestedSHA)
	assert.True(body.Ref.Stale)
}

func TestRepoBrowserTreeAssetLastChangedAndHistory(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	srv, work := setupRepoBrowserServer(t, "github", "github.com", "acme/widgets")
	require.NoError(os.MkdirAll(filepath.Join(work, "docs"), 0o755))
	image := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}
	require.NoError(os.WriteFile(filepath.Join(work, "docs", "image.png"), image, 0o644))
	require.NoError(os.WriteFile(filepath.Join(work, "README.md"), []byte("# Test\n\nUpdated\n"), 0o644))
	serverRepoBrowserGit(t, work, "add", ".")
	serverRepoBrowserGit(t, work, "commit", "-m", "docs asset")
	serverRepoBrowserGit(t, work, "push", "origin", "main")

	tree := repoBrowserRequest(t, srv, http.MethodGet,
		"/api/v1/repo/github/acme/widgets/browser/tree?repo_path=acme%2Fwidgets&ref_type=branch&ref_name=main",
	)
	require.Equal(http.StatusOK, tree.Code)
	var treeBody repoBrowserTreeResponse
	require.NoError(json.Unmarshal(tree.Body.Bytes(), &treeBody))
	assert.False(treeBody.Truncated)
	assert.Contains(repoBrowserEntryPaths(treeBody.Entries), "docs/image.png")

	asset := repoBrowserRequest(t, srv, http.MethodGet,
		"/api/v1/repo/github/acme/widgets/browser/asset?repo_path=acme%2Fwidgets&ref_type=branch&ref_name=main&path=docs%2Fimage.png",
	)
	require.Equal(http.StatusOK, asset.Code)
	assert.Equal("image/png", asset.Header().Get("Content-Type"))
	assert.Equal("nosniff", asset.Header().Get("X-Content-Type-Options"))
	assert.Equal(image, asset.Body.Bytes())

	lastChanged := repoBrowserRequest(t, srv, http.MethodGet,
		"/api/v1/repo/github/acme/widgets/browser/last-changed?repo_path=acme%2Fwidgets&ref_type=branch&ref_name=main&path=README.md&path=docs%2Fimage.png",
	)
	require.Equal(http.StatusOK, lastChanged.Code)
	var lastChangedBody repoBrowserLastChangedResponse
	require.NoError(json.Unmarshal(lastChanged.Body.Bytes(), &lastChangedBody))
	assert.Equal("docs asset", lastChangedBody.Commits["README.md"].Subject)
	assert.Equal("docs asset", lastChangedBody.Commits["docs/image.png"].Subject)

	history := repoBrowserRequest(t, srv, http.MethodGet,
		"/api/v1/repo/github/acme/widgets/browser/history?repo_path=acme%2Fwidgets&ref_type=branch&ref_name=main&path=README.md",
	)
	require.Equal(http.StatusOK, history.Code)
	var historyBody repoBrowserHistoryResponse
	require.NoError(json.Unmarshal(history.Body.Bytes(), &historyBody))
	require.NotEmpty(historyBody.Commits)
	assert.Equal("docs asset", historyBody.Commits[0].Subject)

	commitDetail := repoBrowserRequest(t, srv, http.MethodGet,
		"/api/v1/repo/github/acme/widgets/browser/commit?repo_path=acme%2Fwidgets&ref_type=branch&ref_name=main&path=README.md&sha="+url.QueryEscape(historyBody.Commits[0].SHA),
	)
	require.Equal(http.StatusOK, commitDetail.Code)
	var commitBody repoBrowserCommitResponse
	require.NoError(json.Unmarshal(commitDetail.Body.Bytes(), &commitBody))
	assert.Equal(historyBody.Commits[0].SHA, commitBody.Commit.SHA)
}

func TestRepoBrowserAssetRejectsActiveContentTypes(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	srv, work := setupRepoBrowserServer(t, "github", "github.com", "acme/widgets")
	require.NoError(os.WriteFile(filepath.Join(work, "image.svg"), []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`), 0o644))
	require.NoError(os.WriteFile(filepath.Join(work, "page.html"), []byte(`<script>alert(1)</script>`), 0o644))
	require.NoError(os.WriteFile(filepath.Join(work, "script.js"), []byte(`alert(1)`), 0o644))
	serverRepoBrowserGit(t, work, "add", ".")
	serverRepoBrowserGit(t, work, "commit", "-m", "active assets")
	serverRepoBrowserGit(t, work, "push", "origin", "main")

	for _, path := range []string{"image.svg", "page.html", "script.js"} {
		rr := repoBrowserRequest(t, srv, http.MethodGet,
			"/api/v1/repo/github/acme/widgets/browser/asset?ref_type=branch&ref_name=main&path="+url.QueryEscape(path),
		)
		assert.Equal(http.StatusUnsupportedMediaType, rr.Code, path)
		assert.Contains(rr.Body.String(), "unsupported_asset")
	}
}

func TestRepoBrowserAssetOpenAPIResponseIsBinary(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	doc := NewOpenAPI()
	for _, path := range []string{
		"/repo/{provider}/{owner}/{name}/browser/asset",
		"/host/{platform_host}/repo/{provider}/{owner}/{name}/browser/asset",
	} {
		item := doc.Paths[path]
		require.NotNil(item, path)
		require.NotNil(item.Get, path)
		resp := item.Get.Responses["200"]
		require.NotNil(resp, path)

		assert.Contains(resp.Content, "image/png", path)
		assert.Contains(resp.Content, "image/jpeg", path)
		assert.NotContains(resp.Content, "application/json", path)
		schema := resp.Content["image/png"].Schema
		require.NotNil(schema, path)
		assert.Equal("string", schema.Type, path)
		assert.Equal("binary", schema.Format, path)
	}
}

func TestRepoBrowserCommitRejectsSHAOutsideSelectedFileHistory(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	srv, work := setupRepoBrowserServer(t, "github", "github.com", "acme/widgets")
	require.NoError(os.WriteFile(filepath.Join(work, "other.txt"), []byte("other\n"), 0o644))
	serverRepoBrowserGit(t, work, "add", ".")
	serverRepoBrowserGit(t, work, "commit", "-m", "other file")
	otherSHA := testGitSHA(t, work, "HEAD")
	serverRepoBrowserGit(t, work, "push", "origin", "main")

	rr := repoBrowserRequest(t, srv, http.MethodGet,
		"/api/v1/repo/github/acme/widgets/browser/commit?ref_type=branch&ref_name=main&path=README.md&sha="+url.QueryEscape(otherSHA),
	)

	require.Equal(http.StatusNotFound, rr.Code)
	assert.Contains(rr.Body.String(), "commit_out_of_scope")

	ok := repoBrowserRequest(t, srv, http.MethodGet,
		"/api/v1/repo/github/acme/widgets/browser/commit?ref_type=branch&ref_name=main&path=other.txt&sha="+url.QueryEscape(otherSHA),
	)
	require.Equal(http.StatusOK, ok.Code)
	var body repoBrowserCommitResponse
	require.NoError(json.Unmarshal(ok.Body.Bytes(), &body))
	assert.Equal(otherSHA, body.Commit.SHA)
	assert.Equal("other file", body.Commit.Subject)
}

func TestRepoBrowserRejectsUnsafePath(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	srv, _ := setupRepoBrowserServer(t, "github", "github.com", "acme/widgets")

	rr := repoBrowserRequest(t, srv, http.MethodGet,
		"/api/v1/repo/github/acme/widgets/browser/blob?repo_path=acme%2Fwidgets&ref_type=branch&ref_name=main&path=..%2Fsecret.txt",
	)

	require.Equal(http.StatusBadRequest, rr.Code)
	var problem map[string]any
	require.NoError(json.Unmarshal(rr.Body.Bytes(), &problem))
	assert.Contains(rr.Body.String(), "unsafe_path")
}

func repoBrowserEntryPaths(entries []gitclone.RepoBrowserTreeEntry) []string {
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		paths = append(paths, entry.Path)
	}
	return paths
}

func setupRepoBrowserServer(t *testing.T, provider, host, repoPath string) (*Server, string) {
	t.Helper()
	database := openTestDB(t)
	remote, work := setupServerRepoBrowserGitRepo(t)
	owner, name := splitServerRepoPathForTest(repoPath)
	repoID, err := database.UpsertRepo(t.Context(), db.RepoIdentity{
		Platform:     provider,
		PlatformHost: host,
		Owner:        owner,
		Name:         name,
		RepoPath:     repoPath,
	})
	require.NoError(t, err)
	require.NoError(t, database.UpdateRepoProviderMetadata(
		t.Context(),
		repoID,
		db.RepoProviderMetadata{
			WebURL:        "https://" + host + "/" + repoPath,
			CloneURL:      remote,
			DefaultBranch: "main",
		},
	))
	clones := gitclone.New(filepath.Join(t.TempDir(), "clones"), nil)
	syncer := ghclient.NewSyncer(nil, database, nil, nil, time.Minute, nil, nil)
	t.Cleanup(syncer.Stop)
	srv := New(database, syncer, nil, "/", nil, ServerOptions{Clones: clones})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, srv.Shutdown(ctx))
	})
	return srv, work
}

func setupServerRepoBrowserGitRepo(t *testing.T) (remote string, work string) {
	t.Helper()
	dir := t.TempDir()
	remote = filepath.Join(dir, "remote.git")
	serverRepoBrowserGit(t, dir, "init", "--bare", "--initial-branch=main", remote)
	work = filepath.Join(dir, "work")
	serverRepoBrowserGit(t, dir, "clone", remote, work)
	serverRepoBrowserGit(t, work, "config", "user.email", "alice@example.com")
	serverRepoBrowserGit(t, work, "config", "user.name", "Alice")
	require.NoError(t, os.WriteFile(filepath.Join(work, "README.md"), []byte("# Test\n"), 0o644))
	serverRepoBrowserGit(t, work, "add", ".")
	serverRepoBrowserGit(t, work, "commit", "-m", "initial")
	serverRepoBrowserGit(t, work, "push", "origin", "main")
	return remote, work
}

func serverRepoBrowserGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	runGit(t, dir, args...)
}

func repoBrowserRequest(t *testing.T, srv *Server, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	return rr
}

func splitServerRepoPathForTest(repoPath string) (string, string) {
	for i := len(repoPath) - 1; i >= 0; i-- {
		if repoPath[i] == '/' {
			return repoPath[:i], repoPath[i+1:]
		}
	}
	return "", repoPath
}
