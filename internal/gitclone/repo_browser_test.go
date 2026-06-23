package gitclone

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRepoBrowserListRefsDisambiguatesBranchAndTag(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	mgr, repo, work := setupRepoBrowserTestRepo(t)

	mainSHA := gitSHA(t, work, "main")
	commitTestRun(t, work, "git", "checkout", "-b", "release")
	require.NoError(os.WriteFile(filepath.Join(work, "release.txt"), []byte("release\n"), 0o644))
	commitTestRun(t, work, "git", "add", ".")
	commitTestRun(t, work, "git", "commit", "-m", "release branch")
	branchSHA := gitSHA(t, work, "HEAD")
	commitTestRun(t, work, "git", "push", "origin", "HEAD:refs/heads/release")
	commitTestRun(t, work, "git", "checkout", "main")
	commitTestRun(t, work, "git", "tag", "release", mainSHA)
	commitTestRun(t, work, "git", "push", "origin", "refs/tags/release")
	require.NoError(mgr.EnsureClone(t.Context(), repo.Host, repo.Owner, repo.Name, repo.RemoteURL))

	refs, defaultRef, err := mgr.ListRepoBrowserRefs(t.Context(), repo, "main")
	require.NoError(err)

	assert.Equal(RepoBrowserRefBranch, defaultRef.Type)
	assert.Equal("main", defaultRef.Name)
	assert.Equal(mainSHA, defaultRef.SHA)
	assert.Contains(refs, RepoBrowserRef{Type: RepoBrowserRefBranch, Name: "release", SHA: branchSHA})
	assert.Contains(refs, RepoBrowserRef{Type: RepoBrowserRefTag, Name: "release", SHA: mainSHA})
}

func TestRepoBrowserFetchDoesNotPruneTagsOnHotPath(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	mgr, repo, work := setupRepoBrowserTestRepo(t)
	mainSHA := gitSHA(t, work, "main")

	commitTestRun(t, work, "git", "tag", "v1.0.0", mainSHA)
	commitTestRun(t, work, "git", "push", "origin", "refs/tags/v1.0.0")
	require.NoError(mgr.EnsureClone(t.Context(), repo.Host, repo.Owner, repo.Name, repo.RemoteURL))
	refs, _, err := mgr.ListRepoBrowserRefs(t.Context(), repo, "main")
	require.NoError(err)
	assert.Contains(refs, RepoBrowserRef{Type: RepoBrowserRefTag, Name: "v1.0.0", SHA: mainSHA})

	commitTestRun(t, work, "git", "tag", "-d", "v1.0.0")
	commitTestRun(t, work, "git", "push", "origin", ":refs/tags/v1.0.0")
	require.NoError(mgr.EnsureClone(t.Context(), repo.Host, repo.Owner, repo.Name, repo.RemoteURL))

	refs, _, err = mgr.ListRepoBrowserRefs(t.Context(), repo, "main")
	require.NoError(err)
	assert.Contains(refs, RepoBrowserRef{Type: RepoBrowserRefTag, Name: "v1.0.0", SHA: mainSHA})
}

func TestRepoBrowserListTreeReaderStopsAtEntryLimit(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	var input strings.Builder
	for i := range RepoBrowserTreeEntryLimit + 1 {
		_, err := fmt.Fprintf(&input, "100644 blob %040d %d\tfile-%05d.txt\x00", i, i, i)
		require.NoError(err)
	}
	canceled := false

	entries, truncated, err := readRepoBrowserTreeEntries(strings.NewReader(input.String()), func() {
		canceled = true
	})

	require.NoError(err)
	assert.True(truncated)
	assert.True(canceled)
	assert.Len(entries, RepoBrowserTreeEntryLimit)
}

func TestRepoBrowserListTreeIncludesTrackedDotfiles(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	mgr, repo, work := setupRepoBrowserTestRepo(t)
	ref := repoBrowserMainRef(t, mgr, repo)

	entries, truncated, err := mgr.ListRepoBrowserTree(t.Context(), repo, ref)
	require.NoError(err)

	var paths []string
	for _, entry := range entries {
		paths = append(paths, entry.Path)
	}
	assert.False(truncated)
	assert.Contains(paths, ".github/workflows/ci.yml")
	assert.Contains(paths, ".gitignore")
	assert.Contains(paths, "README.md")
	assert.Contains(paths, "src/main.go")
	assert.NotContains(paths, ".git")
	assert.Equal(gitSHA(t, work, "main"), ref.SHA)
}

func TestRepoBrowserReadBlobRejectsTraversalAndLargeFiles(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	mgr, repo, work := setupRepoBrowserTestRepo(t)

	largePath := filepath.Join(work, "large.txt")
	require.NoError(os.WriteFile(largePath, []byte(string(make([]byte, RepoBrowserBlobSizeLimit+1))), 0o644))
	commitTestRun(t, work, "git", "add", ".")
	commitTestRun(t, work, "git", "commit", "-m", "large file")
	commitTestRun(t, work, "git", "push", "origin", "main")
	require.NoError(mgr.EnsureClone(t.Context(), repo.Host, repo.Owner, repo.Name, repo.RemoteURL))
	ref := repoBrowserMainRef(t, mgr, repo)

	_, err := mgr.ReadRepoBrowserBlob(t.Context(), repo, ref, "../secret.txt")
	require.ErrorIs(err, ErrUnsafePath)

	blob, err := mgr.ReadRepoBrowserBlob(t.Context(), repo, ref, "large.txt")
	require.NoError(err)
	assert.True(blob.TooLarge)
	assert.Equal(int64(RepoBrowserBlobSizeLimit+1), blob.Size)
	assert.Empty(blob.Content)
}

func TestRepoBrowserLastChangedBatchCapsPaths(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	mgr, repo, _ := setupRepoBrowserTestRepo(t)
	ref := repoBrowserMainRef(t, mgr, repo)
	paths := make([]string, RepoBrowserLastChangedBatchMax+1)
	for i := range paths {
		paths[i] = "README.md"
	}

	_, err := mgr.RepoBrowserLastChanged(t.Context(), repo, ref, paths)

	require.Error(err)
	assert.ErrorIs(err, ErrTooManyPaths)
}

func TestRepoBrowserFileHistoryIsBoundedAtSelectedSHA(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	mgr, repo, work := setupRepoBrowserTestRepo(t)

	require.NoError(os.WriteFile(filepath.Join(work, "README.md"), []byte("two\n"), 0o644))
	commitTestRun(t, work, "git", "add", ".")
	commitTestRun(t, work, "git", "commit", "-m", "readme two")
	selectedSHA := gitSHA(t, work, "HEAD")
	require.NoError(os.WriteFile(filepath.Join(work, "README.md"), []byte("three\n"), 0o644))
	commitTestRun(t, work, "git", "add", ".")
	commitTestRun(t, work, "git", "commit", "-m", "readme three")
	commitTestRun(t, work, "git", "push", "origin", "main")
	require.NoError(mgr.EnsureClone(t.Context(), repo.Host, repo.Owner, repo.Name, repo.RemoteURL))

	history, err := mgr.RepoBrowserFileHistory(
		t.Context(),
		repo,
		RepoBrowserRef{Type: RepoBrowserRefCommit, SHA: selectedSHA},
		"README.md",
	)
	require.NoError(err)
	require.NotEmpty(history)
	assert.Equal(selectedSHA, history[0].SHA)
	assert.Equal("readme two", history[0].Subject)
	for _, commit := range history {
		assert.NotEqual("readme three", commit.Subject)
	}
}

func TestRepoBrowserCommitDetailRequiresSelectedFileHistory(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	mgr, repo, work := setupRepoBrowserTestRepo(t)

	require.NoError(os.WriteFile(filepath.Join(work, "other.txt"), []byte("other\n"), 0o644))
	commitTestRun(t, work, "git", "add", ".")
	commitTestRun(t, work, "git", "commit", "-m", "other file")
	otherSHA := gitSHA(t, work, "HEAD")
	commitTestRun(t, work, "git", "push", "origin", "main")
	require.NoError(mgr.EnsureClone(t.Context(), repo.Host, repo.Owner, repo.Name, repo.RemoteURL))
	ref := repoBrowserMainRef(t, mgr, repo)

	_, err := mgr.RepoBrowserCommitDetail(t.Context(), repo, ref, "README.md", otherSHA)
	require.ErrorIs(err, ErrCommitOutOfScope)

	commit, err := mgr.RepoBrowserCommitDetail(t.Context(), repo, ref, "other.txt", otherSHA)
	require.NoError(err)
	assert.Equal(otherSHA, commit.SHA)
	assert.Equal("other file", commit.Subject)
}

func TestRepoBrowserMarkdownAssetRejectsUnsafeAndOversizedPaths(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	mgr, repo, work := setupRepoBrowserTestRepo(t)

	require.NoError(os.WriteFile(filepath.Join(work, "image.svg"), []byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`), 0o644))
	require.NoError(os.WriteFile(filepath.Join(work, "page.html"), []byte(`<script>alert(1)</script>`), 0o644))
	require.NoError(os.WriteFile(filepath.Join(work, "script.js"), []byte(`alert(1)`), 0o644))
	require.NoError(os.WriteFile(filepath.Join(work, "image.png"), []byte{0x89, 0x50, 0x4e, 0x47}, 0o644))
	require.NoError(os.WriteFile(filepath.Join(work, "huge.png"), []byte(string(make([]byte, RepoBrowserBlobSizeLimit+1))), 0o644))
	commitTestRun(t, work, "git", "add", ".")
	commitTestRun(t, work, "git", "commit", "-m", "assets")
	commitTestRun(t, work, "git", "push", "origin", "main")
	require.NoError(mgr.EnsureClone(t.Context(), repo.Host, repo.Owner, repo.Name, repo.RemoteURL))
	ref := repoBrowserMainRef(t, mgr, repo)

	_, err := mgr.ReadRepoBrowserAsset(t.Context(), repo, ref, "/etc/passwd")
	require.ErrorIs(err, ErrUnsafePath)

	for _, path := range []string{"image.svg", "page.html", "script.js"} {
		_, err = mgr.ReadRepoBrowserAsset(t.Context(), repo, ref, path)
		require.ErrorIs(err, ErrUnsupportedAsset, path)
	}

	asset, err := mgr.ReadRepoBrowserAsset(t.Context(), repo, ref, "image.png")
	require.NoError(err)
	assert.Equal("image/png", asset.MediaType)

	_, err = mgr.ReadRepoBrowserAsset(t.Context(), repo, ref, "huge.png")
	assert.True(errors.Is(err, ErrTooLarge) || errors.Is(err, ErrTooLargeAsset))
}

func setupRepoBrowserTestRepo(t *testing.T) (*Manager, RepoBrowserRepoRef, string) {
	t.Helper()
	dir := t.TempDir()
	remote := filepath.Join(dir, "remote.git")
	commitTestRun(t, dir, "git", "init", "--bare", "--initial-branch=main", remote)

	work := filepath.Join(dir, "work")
	commitTestRun(t, dir, "git", "clone", remote, work)
	commitTestRun(t, work, "git", "config", "user.email", "alice@example.com")
	commitTestRun(t, work, "git", "config", "user.name", "Alice")
	require.NoError(t, os.MkdirAll(filepath.Join(work, ".github", "workflows"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(work, "src"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(work, ".github", "workflows", "ci.yml"), []byte("name: ci\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(work, ".gitignore"), []byte("tmp\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(work, "README.md"), []byte("# Widgets\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(work, "src", "main.go"), []byte("package main\n"), 0o644))
	commitTestRun(t, work, "git", "add", ".")
	commitTestRun(t, work, "git", "commit", "-m", "initial")
	commitTestRun(t, work, "git", "push", "origin", "main")

	mgr := New(filepath.Join(dir, "clones"), nil)
	repo := RepoBrowserRepoRef{
		Host:      "github.com",
		Owner:     "acme",
		Name:      "widgets",
		RepoPath:  "acme/widgets",
		RemoteURL: remote,
	}
	require.NoError(t, mgr.EnsureClone(t.Context(), repo.Host, repo.Owner, repo.Name, repo.RemoteURL))
	return mgr, repo, work
}

func repoBrowserMainRef(t *testing.T, mgr *Manager, repo RepoBrowserRepoRef) RepoBrowserRef {
	t.Helper()
	_, ref, err := mgr.ResolveDefaultBranch(t.Context(), repo.Host, repo.Owner, repo.Name, "main")
	require.NoError(t, err)
	return RepoBrowserRef{Type: RepoBrowserRefBranch, Name: "main", SHA: ref}
}
