package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
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

func TestRepoBrowserRefsReportsTruncationForLargeRefSets(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	srv, work := setupRepoBrowserServer(t, "github", "github.com", "acme/widgets")
	remote := filepath.Join(filepath.Dir(work), "remote.git")
	mainSHA := testGitSHA(t, work, "main")
	for i := range gitclone.RepoBrowserRefLimit + 1 {
		serverRepoBrowserGit(t, remote, "update-ref", fmt.Sprintf("refs/heads/branch-%04d", i), mainSHA)
	}

	rr := repoBrowserRequest(t, srv, http.MethodGet,
		"/api/v1/repo/github/acme/widgets/browser/refs",
	)

	require.Equal(http.StatusOK, rr.Code)
	var body repoBrowserRefsResponse
	require.NoError(json.Unmarshal(rr.Body.Bytes(), &body))
	assert.True(body.Truncated)
	assert.Len(body.Refs, gitclone.RepoBrowserRefLimit)
	assert.Equal("main", body.DefaultRef.Name)
	assert.Equal(mainSHA, body.DefaultRef.SHA)
}

func TestRepoBrowserCloneCacheSeparatesProvidersWithSameHostAndPath(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	database := openTestDB(t)
	githubRemote, githubWork := setupServerRepoBrowserGitRepo(t)
	gitlabRemote, gitlabWork := setupServerRepoBrowserGitRepo(t)
	require.NoError(os.WriteFile(filepath.Join(githubWork, "README.md"), []byte("github repo\n"), 0o644))
	serverRepoBrowserGit(t, githubWork, "add", ".")
	serverRepoBrowserGit(t, githubWork, "commit", "-m", "github content")
	serverRepoBrowserGit(t, githubWork, "push", "origin", "main")
	require.NoError(os.WriteFile(filepath.Join(gitlabWork, "README.md"), []byte("gitlab repo\n"), 0o644))
	serverRepoBrowserGit(t, gitlabWork, "add", ".")
	serverRepoBrowserGit(t, gitlabWork, "commit", "-m", "gitlab content")
	serverRepoBrowserGit(t, gitlabWork, "push", "origin", "main")

	for _, repo := range []struct {
		provider string
		cloneURL string
	}{
		{provider: "github", cloneURL: githubRemote},
		{provider: "gitlab", cloneURL: gitlabRemote},
	} {
		repoID, err := database.UpsertRepo(t.Context(), db.RepoIdentity{
			Platform:     repo.provider,
			PlatformHost: "git.example.com",
			Owner:        "acme",
			Name:         "widgets",
			RepoPath:     "acme/widgets",
		})
		require.NoError(err)
		require.NoError(database.UpdateRepoProviderMetadata(
			t.Context(),
			repoID,
			db.RepoProviderMetadata{
				WebURL:        "https://git.example.com/acme/widgets",
				CloneURL:      repo.cloneURL,
				DefaultBranch: "main",
			},
		))
	}
	clones := gitclone.New(filepath.Join(t.TempDir(), "clones"), nil)
	syncer := ghclient.NewSyncer(nil, database, nil, nil, time.Minute, nil, nil)
	t.Cleanup(syncer.Stop)
	srv := New(database, syncer, nil, "/", nil, ServerOptions{Clones: clones})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(srv.Shutdown(ctx))
	})

	githubBlob := repoBrowserRequest(t, srv, http.MethodGet,
		"/api/v1/host/git.example.com/repo/github/acme/widgets/browser/blob?repo_path=acme%2Fwidgets&ref_type=branch&ref_name=main&path=README.md",
	)
	require.Equal(http.StatusOK, githubBlob.Code)
	var githubBody repoBrowserBlobResponse
	require.NoError(json.Unmarshal(githubBlob.Body.Bytes(), &githubBody))
	assert.Equal("github repo\n", githubBody.Blob.Content)

	gitlabBlob := repoBrowserRequest(t, srv, http.MethodGet,
		"/api/v1/host/git.example.com/repo/gitlab/acme/widgets/browser/blob?repo_path=acme%2Fwidgets&ref_type=branch&ref_name=main&path=README.md",
	)
	require.Equal(http.StatusOK, gitlabBlob.Code)
	var gitlabBody repoBrowserBlobResponse
	require.NoError(json.Unmarshal(gitlabBlob.Body.Bytes(), &gitlabBody))
	assert.Equal("gitlab repo\n", gitlabBody.Blob.Content)
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

func TestRepoBrowserReadsStayPinnedAfterBranchAdvances(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	srv, work := setupRepoBrowserServer(t, "github", "github.com", "acme/widgets")
	require.NoError(os.WriteFile(filepath.Join(work, "README.md"), []byte("# Selected\n"), 0o644))
	serverRepoBrowserGit(t, work, "add", ".")
	serverRepoBrowserGit(t, work, "commit",
		"--date=2026-06-01T12:00:00Z",
		"-m", "selected readme",
		"-m", "Selected body.",
	)
	selectedSHA := testGitSHA(t, work, "HEAD")
	serverRepoBrowserGit(t, work, "push", "origin", "main")

	tree := repoBrowserRequest(t, srv, http.MethodGet,
		"/api/v1/repo/github/acme/widgets/browser/tree?repo_path=acme%2Fwidgets&ref_type=branch&ref_name=main",
	)
	require.Equal(http.StatusOK, tree.Code)
	var treeBody repoBrowserTreeResponse
	require.NoError(json.Unmarshal(tree.Body.Bytes(), &treeBody))
	require.Equal(selectedSHA, treeBody.Ref.SHA)

	require.NoError(os.WriteFile(filepath.Join(work, "README.md"), []byte("# Advanced\n"), 0o644))
	serverRepoBrowserGit(t, work, "add", ".")
	serverRepoBrowserGit(t, work, "commit",
		"--date=2026-06-02T12:00:00Z",
		"-m", "advanced readme",
	)
	advancedSHA := testGitSHA(t, work, "HEAD")
	serverRepoBrowserGit(t, work, "push", "origin", "main")
	srv.clones.RefreshRepoBrowserClones(t.Context())

	branchBlob := repoBrowserRequest(t, srv, http.MethodGet,
		"/api/v1/repo/github/acme/widgets/browser/blob?repo_path=acme%2Fwidgets&ref_type=branch&ref_name=main&path=README.md",
	)
	require.Equal(http.StatusOK, branchBlob.Code)
	var branchBlobBody repoBrowserBlobResponse
	require.NoError(json.Unmarshal(branchBlob.Body.Bytes(), &branchBlobBody))
	assert.Equal(advancedSHA, branchBlobBody.Ref.SHA)
	assert.Equal("# Advanced\n", branchBlobBody.Blob.Content)

	pinnedBlob := repoBrowserRequest(t, srv, http.MethodGet,
		"/api/v1/repo/github/acme/widgets/browser/blob?repo_path=acme%2Fwidgets&ref_type=commit&ref_sha="+url.QueryEscape(selectedSHA)+"&path=README.md",
	)
	require.Equal(http.StatusOK, pinnedBlob.Code)
	var pinnedBlobBody repoBrowserBlobResponse
	require.NoError(json.Unmarshal(pinnedBlob.Body.Bytes(), &pinnedBlobBody))
	assert.Equal(selectedSHA, pinnedBlobBody.Ref.SHA)
	assert.Equal("# Selected\n", pinnedBlobBody.Blob.Content)

	lastChanged := repoBrowserRequest(t, srv, http.MethodGet,
		"/api/v1/repo/github/acme/widgets/browser/last-changed?repo_path=acme%2Fwidgets&ref_type=commit&ref_sha="+url.QueryEscape(selectedSHA)+"&path=README.md",
	)
	require.Equal(http.StatusOK, lastChanged.Code)
	var lastChangedBody repoBrowserLastChangedResponse
	require.NoError(json.Unmarshal(lastChanged.Body.Bytes(), &lastChangedBody))
	assert.Equal(selectedSHA, lastChangedBody.Commits["README.md"].SHA)
	assert.Equal("selected readme", lastChangedBody.Commits["README.md"].Subject)

	history := repoBrowserRequest(t, srv, http.MethodGet,
		"/api/v1/repo/github/acme/widgets/browser/history?repo_path=acme%2Fwidgets&ref_type=commit&ref_sha="+url.QueryEscape(selectedSHA)+"&path=README.md",
	)
	require.Equal(http.StatusOK, history.Code)
	var historyBody repoBrowserHistoryResponse
	require.NoError(json.Unmarshal(history.Body.Bytes(), &historyBody))
	require.NotEmpty(historyBody.Commits)
	assert.Equal(selectedSHA, historyBody.Commits[0].SHA)
	assert.Equal("selected readme", historyBody.Commits[0].Subject)

	commitDetail := repoBrowserRequest(t, srv, http.MethodGet,
		"/api/v1/repo/github/acme/widgets/browser/commit?repo_path=acme%2Fwidgets&ref_type=commit&ref_sha="+url.QueryEscape(selectedSHA)+"&path=README.md&sha="+url.QueryEscape(selectedSHA),
	)
	require.Equal(http.StatusOK, commitDetail.Code)
	var commitBody repoBrowserCommitResponse
	require.NoError(json.Unmarshal(commitDetail.Body.Bytes(), &commitBody))
	assert.Equal(selectedSHA, commitBody.Commit.SHA)
	assert.Equal("Selected body.", commitBody.Commit.Body)
}

func TestRepoBrowserStaleInitialRefCanRecoverThroughValidRef(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	srv, work := setupRepoBrowserServer(t, "github", "github.com", "acme/widgets")
	mainSHA := testGitSHA(t, work, "main")

	refs := repoBrowserRequest(t, srv, http.MethodGet,
		"/api/v1/repo/github/acme/widgets/browser/refs?repo_path=acme%2Fwidgets",
	)
	require.Equal(http.StatusOK, refs.Code)
	var refsBody repoBrowserRefsResponse
	require.NoError(json.Unmarshal(refs.Body.Bytes(), &refsBody))
	assert.Equal("main", refsBody.DefaultRef.Name)
	assert.Equal(mainSHA, refsBody.DefaultRef.SHA)

	stale := repoBrowserRequest(t, srv, http.MethodGet,
		"/api/v1/repo/github/acme/widgets/browser/tree?repo_path=acme%2Fwidgets&ref_type=branch&ref_name=deleted&ref_sha="+url.QueryEscape(mainSHA),
	)
	require.Equal(http.StatusNotFound, stale.Code)
	assert.Contains(stale.Body.String(), "not_found")

	recovered := repoBrowserRequest(t, srv, http.MethodGet,
		"/api/v1/repo/github/acme/widgets/browser/tree?repo_path=acme%2Fwidgets&ref_type=branch&ref_name=main&ref_sha="+url.QueryEscape(mainSHA),
	)
	require.Equal(http.StatusOK, recovered.Code)
	var recoveredBody repoBrowserTreeResponse
	require.NoError(json.Unmarshal(recovered.Body.Bytes(), &recoveredBody))
	assert.Equal(mainSHA, recoveredBody.Ref.SHA)
	assert.Contains(repoBrowserEntryPaths(recoveredBody.Entries), "README.md")
}

func TestRepoBrowserRejectsRevisionExpressionRefs(t *testing.T) {
	assert := assert.New(t)
	srv, work := setupRepoBrowserServer(t, "github", "github.com", "acme/widgets")
	mainSHA := testGitSHA(t, work, "main")
	serverRepoBrowserGit(t, work, "tag", "release", mainSHA)
	serverRepoBrowserGit(t, work, "push", "origin", "refs/tags/release")

	for _, path := range []string{
		"/api/v1/repo/github/acme/widgets/browser/blob?repo_path=acme%2Fwidgets&ref_type=branch&ref_name=main~1&path=README.md",
		"/api/v1/repo/github/acme/widgets/browser/blob?repo_path=acme%2Fwidgets&ref_type=tag&ref_name=release%5E%7B%7D&path=README.md",
	} {
		rr := repoBrowserRequest(t, srv, http.MethodGet, path)
		assert.Equal(http.StatusNotFound, rr.Code, path)
		assert.Contains(rr.Body.String(), "not_found", path)
	}
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
	serverRepoBrowserGit(t, work, "commit",
		"--date=2026-06-01T12:34:56-07:00",
		"-m", "docs asset",
		"-m", "Document asset changes.\n\nKeep preview useful.",
	)
	currentSHA := testGitSHA(t, work, "HEAD")
	serverRepoBrowserGit(t, work, "push", "origin", "main")
	expectedAuthoredAt := time.Date(2026, 6, 1, 19, 34, 56, 0, time.UTC)

	tree := repoBrowserRequest(t, srv, http.MethodGet,
		"/api/v1/repo/github/acme/widgets/browser/tree?repo_path=acme%2Fwidgets&ref_type=branch&ref_name=main",
	)
	require.Equal(http.StatusOK, tree.Code)
	var treeBody repoBrowserTreeResponse
	require.NoError(json.Unmarshal(tree.Body.Bytes(), &treeBody))
	assert.False(treeBody.Truncated)
	assert.Contains(repoBrowserEntryPaths(treeBody.Entries), "docs/image.png")

	asset := repoBrowserRequest(t, srv, http.MethodGet,
		"/api/v1/repo/github/acme/widgets/browser/asset?repo_path=acme%2Fwidgets&ref_type=commit&ref_sha="+url.QueryEscape(currentSHA)+"&path=docs%2Fimage.png",
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
	assert.Equal(expectedAuthoredAt, lastChangedBody.Commits["README.md"].AuthoredAt)
	assert.Equal(expectedAuthoredAt, lastChangedBody.Commits["docs/image.png"].AuthoredAt)

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
	assert.Equal("Document asset changes.\n\nKeep preview useful.", commitBody.Commit.Body)

	mutableAsset := repoBrowserRequest(t, srv, http.MethodGet,
		"/api/v1/repo/github/acme/widgets/browser/asset?repo_path=acme%2Fwidgets&ref_type=branch&ref_name=main&path=docs%2Fimage.png",
	)
	require.Equal(http.StatusBadRequest, mutableAsset.Code)
	assert.Contains(mutableAsset.Body.String(), "mutable_ref_not_allowed")

	missingHistory := repoBrowserRequest(t, srv, http.MethodGet,
		"/api/v1/repo/github/acme/widgets/browser/history?repo_path=acme%2Fwidgets&ref_type=commit&ref_sha="+url.QueryEscape(currentSHA)+"&path=missing.md",
	)
	require.Equal(http.StatusNotFound, missingHistory.Code)
	assert.Contains(missingHistory.Body.String(), "not_found")
}

func TestRepoBrowserLastChangedFallsBackPastBatchLogLimit(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	srv, work := setupRepoBrowserServer(t, "github", "github.com", "acme/widgets")
	readmeSHA := testGitSHA(t, work, "HEAD")

	for i := range gitclone.RepoBrowserLastChangedLogLimit + 1 {
		require.NoError(os.WriteFile(filepath.Join(work, "churn.txt"), fmt.Appendf(nil, "%d\n", i), 0o644))
		serverRepoBrowserGit(t, work, "add", ".")
		serverRepoBrowserGit(t, work, "commit", "-m", fmt.Sprintf("churn %03d", i))
	}
	churnSHA := testGitSHA(t, work, "HEAD")
	serverRepoBrowserGit(t, work, "push", "origin", "main")

	rr := repoBrowserRequest(t, srv, http.MethodGet,
		"/api/v1/repo/github/acme/widgets/browser/last-changed?repo_path=acme%2Fwidgets&ref_type=branch&ref_name=main&path=README.md&path=churn.txt",
	)

	require.Equal(http.StatusOK, rr.Code)
	var body repoBrowserLastChangedResponse
	require.NoError(json.Unmarshal(rr.Body.Bytes(), &body))
	assert.Equal(readmeSHA, body.Commits["README.md"].SHA)
	assert.Equal(churnSHA, body.Commits["churn.txt"].SHA)
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
	currentSHA := testGitSHA(t, work, "HEAD")
	serverRepoBrowserGit(t, work, "push", "origin", "main")

	for _, path := range []string{"image.svg", "page.html", "script.js"} {
		rr := repoBrowserRequest(t, srv, http.MethodGet,
			"/api/v1/repo/github/acme/widgets/browser/asset?ref_type=commit&ref_sha="+url.QueryEscape(currentSHA)+"&path="+url.QueryEscape(path),
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
		assert.Contains(item.Get.Description, "ref_type=commit", path)
		assert.Contains(item.Get.Description, "mutable_ref_not_allowed", path)
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
	serverRepoBrowserGit(t, work, "commit",
		"-m", "other file",
		"-m", "Explain the file change.\n\nKeep the body visible.",
	)
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
	assert.Equal("Explain the file change.\n\nKeep the body visible.", body.Commit.Body)
}

func TestRepoBrowserCommitAcceptsMergeCommitTouchingPathThroughHTTP(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	srv, work := setupRepoBrowserServer(t, "github", "github.com", "acme/widgets")

	serverRepoBrowserGit(t, work, "checkout", "-b", "feature")
	require.NoError(os.WriteFile(filepath.Join(work, "README.md"), []byte("# Widgets\n\nFeature\n"), 0o644))
	serverRepoBrowserGit(t, work, "add", ".")
	serverRepoBrowserGit(t, work, "commit", "-m", "feature readme")
	serverRepoBrowserGit(t, work, "checkout", "main")
	require.NoError(os.WriteFile(filepath.Join(work, "main.txt"), []byte("main\n"), 0o644))
	serverRepoBrowserGit(t, work, "add", ".")
	serverRepoBrowserGit(t, work, "commit", "-m", "main work")
	serverRepoBrowserGit(t, work, "merge", "--no-ff", "feature", "-m", "merge feature")
	mergeSHA := testGitSHA(t, work, "HEAD")
	serverRepoBrowserGit(t, work, "push", "origin", "main")

	rr := repoBrowserRequest(t, srv, http.MethodGet,
		"/api/v1/repo/github/acme/widgets/browser/commit?ref_type=branch&ref_name=main&path=README.md&sha="+url.QueryEscape(mergeSHA),
	)

	require.Equal(http.StatusOK, rr.Code)
	var body repoBrowserCommitResponse
	require.NoError(json.Unmarshal(rr.Body.Bytes(), &body))
	assert.Equal(mergeSHA, body.Commit.SHA)
	assert.Equal("merge feature", body.Commit.Subject)
}

func TestRepoBrowserCommitRejectsUnknownFullSHA(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	srv, _ := setupRepoBrowserServer(t, "github", "github.com", "acme/widgets")

	rr := repoBrowserRequest(t, srv, http.MethodGet,
		"/api/v1/repo/github/acme/widgets/browser/commit?ref_type=branch&ref_name=main&path=README.md&sha="+strings.Repeat("a", 40),
	)

	require.Equal(http.StatusNotFound, rr.Code)
	assert.Contains(rr.Body.String(), "not_found")
}

func TestRepoBrowserCommitAcceptsOlderFileHistoryThroughHTTP(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	srv, work := setupRepoBrowserServer(t, "github", "github.com", "acme/widgets")
	readmeSHA := testGitSHA(t, work, "HEAD")

	for i := range gitclone.RepoBrowserHistoryLimit + 1 {
		require.NoError(os.WriteFile(
			filepath.Join(work, fmt.Sprintf("later-%02d.txt", i)),
			[]byte("later\n"),
			0o644,
		))
		serverRepoBrowserGit(t, work, "add", ".")
		serverRepoBrowserGit(t, work, "commit", "-m", fmt.Sprintf("later %02d", i))
	}
	serverRepoBrowserGit(t, work, "push", "origin", "main")

	rr := repoBrowserRequest(t, srv, http.MethodGet,
		"/api/v1/repo/github/acme/widgets/browser/commit?ref_type=branch&ref_name=main&path=README.md&sha="+url.QueryEscape(readmeSHA),
	)

	require.Equal(http.StatusOK, rr.Code)
	var body repoBrowserCommitResponse
	require.NoError(json.Unmarshal(rr.Body.Bytes(), &body))
	assert.Equal(readmeSHA, body.Commit.SHA)
	assert.Equal("initial", body.Commit.Subject)
}

func TestRepoBrowserStartupRefreshSeedsExistingClone(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	database := openTestDB(t)
	remote, work := setupServerRepoBrowserGitRepo(t)
	repoID, err := database.UpsertRepo(t.Context(), db.GitHubRepoIdentity("github.com", "acme", "widgets"))
	require.NoError(err)
	require.NoError(database.UpdateRepoProviderMetadata(
		t.Context(),
		repoID,
		db.RepoProviderMetadata{
			WebURL:        "https://github.com/acme/widgets",
			CloneURL:      remote,
			DefaultBranch: "main",
		},
	))
	cloneBase := filepath.Join(t.TempDir(), "clones")
	initialClones := gitclone.New(cloneBase, nil)
	initialSyncer := ghclient.NewSyncer(nil, database, nil, nil, time.Minute, nil, nil)
	t.Cleanup(initialSyncer.Stop)
	initialServer := New(database, initialSyncer, nil, "/", nil, ServerOptions{
		Clones:                             initialClones,
		DisableWorkspaceBackgroundMonitors: true,
	})
	initialRefs := repoBrowserRequest(t, initialServer, http.MethodGet,
		"/api/v1/repo/github/acme/widgets/browser/refs",
	)
	require.Equal(http.StatusOK, initialRefs.Code)
	gracefulShutdown(t, initialServer)

	require.NoError(os.WriteFile(filepath.Join(work, "README.md"), []byte("# Updated\n"), 0o644))
	serverRepoBrowserGit(t, work, "add", ".")
	serverRepoBrowserGit(t, work, "commit", "-m", "update readme")
	updatedSHA := testGitSHA(t, work, "main")
	serverRepoBrowserGit(t, work, "push", "origin", "main")

	restartedClones := gitclone.New(cloneBase, nil)
	restartedSyncer := ghclient.NewSyncer(nil, database, nil, nil, time.Minute, nil, nil)
	t.Cleanup(restartedSyncer.Stop)
	restartedServer := New(database, restartedSyncer, nil, "/", nil, ServerOptions{Clones: restartedClones})
	t.Cleanup(func() { gracefulShutdown(t, restartedServer) })

	require.Eventually(func() bool {
		rr := repoBrowserRequest(t, restartedServer, http.MethodGet,
			"/api/v1/repo/github/acme/widgets/browser/refs",
		)
		if rr.Code != http.StatusOK {
			return false
		}
		var body repoBrowserRefsResponse
		if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
			return false
		}
		return body.DefaultRef.SHA == updatedSHA
	}, 2*time.Second, 25*time.Millisecond)

	rr := repoBrowserRequest(t, restartedServer, http.MethodGet,
		"/api/v1/repo/github/acme/widgets/browser/blob?ref_type=branch&ref_name=main&path=README.md",
	)
	require.Equal(http.StatusOK, rr.Code)
	var body repoBrowserBlobResponse
	require.NoError(json.Unmarshal(rr.Body.Bytes(), &body))
	assert.Equal("# Updated\n", body.Blob.Content)
}

func TestRepoBrowserStartupRefreshHonorsDisabledBackgroundMonitors(t *testing.T) {
	require := require.New(t)
	database := openTestDB(t)
	remote, work := setupServerRepoBrowserGitRepo(t)
	repoID, err := database.UpsertRepo(t.Context(), db.GitHubRepoIdentity("github.com", "acme", "widgets"))
	require.NoError(err)
	require.NoError(database.UpdateRepoProviderMetadata(
		t.Context(),
		repoID,
		db.RepoProviderMetadata{
			WebURL:        "https://github.com/acme/widgets",
			CloneURL:      remote,
			DefaultBranch: "main",
		},
	))
	cloneBase := filepath.Join(t.TempDir(), "clones")
	initialClones := gitclone.New(cloneBase, nil)
	initialSyncer := ghclient.NewSyncer(nil, database, nil, nil, time.Minute, nil, nil)
	t.Cleanup(initialSyncer.Stop)
	initialServer := New(database, initialSyncer, nil, "/", nil, ServerOptions{
		Clones:                             initialClones,
		DisableWorkspaceBackgroundMonitors: true,
	})
	initialRefs := repoBrowserRequest(t, initialServer, http.MethodGet,
		"/api/v1/repo/github/acme/widgets/browser/refs",
	)
	require.Equal(http.StatusOK, initialRefs.Code)
	gracefulShutdown(t, initialServer)

	require.NoError(os.WriteFile(filepath.Join(work, "README.md"), []byte("# Updated\n"), 0o644))
	serverRepoBrowserGit(t, work, "add", ".")
	serverRepoBrowserGit(t, work, "commit", "-m", "update readme")
	updatedSHA := testGitSHA(t, work, "main")
	serverRepoBrowserGit(t, work, "push", "origin", "main")

	disabledClones := gitclone.New(cloneBase, nil)
	disabledSyncer := ghclient.NewSyncer(nil, database, nil, nil, time.Minute, nil, nil)
	t.Cleanup(disabledSyncer.Stop)
	disabledServer := New(database, disabledSyncer, nil, "/", nil, ServerOptions{
		Clones:                             disabledClones,
		DisableWorkspaceBackgroundMonitors: true,
	})
	t.Cleanup(func() { gracefulShutdown(t, disabledServer) })

	require.Never(func() bool {
		resolved, err := disabledClones.ResolveRepoBrowserRef(t.Context(), gitclone.RepoBrowserRepoRef{
			Provider:  "github",
			Host:      "github.com",
			Owner:     "acme",
			Name:      "widgets",
			RepoPath:  "acme/widgets",
			RemoteURL: remote,
		}, gitclone.RepoBrowserRef{
			Type: gitclone.RepoBrowserRefBranch,
			Name: "main",
		})
		if err != nil {
			return false
		}
		return resolved.SHA == updatedSHA
	}, 250*time.Millisecond, 25*time.Millisecond)
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
