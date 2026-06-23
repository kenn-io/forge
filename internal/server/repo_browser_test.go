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
