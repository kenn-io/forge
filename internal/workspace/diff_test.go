package workspace

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWorktreeDiffFilesAgainstHead(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	work := setupDivergenceWorktree(t)

	require.NoError(os.WriteFile(
		filepath.Join(work, "f.txt"), []byte("dirty\n"), 0o644,
	))
	require.NoError(os.WriteFile(
		filepath.Join(work, "dirty-test.txt"), []byte("test\n"), 0o644,
	))

	files, ok, err := WorktreeDiffFiles(
		t.Context(), work, WorktreeDiffBaseHead, false,
	)
	require.NoError(err)
	require.True(ok)
	require.Len(files, 2)
	assert.Equal("dirty-test.txt", files[0].Path)
	assert.Equal("added", files[0].Status)
	assert.Equal("f.txt", files[1].Path)
	assert.Equal("modified", files[1].Status)
	assert.Equal(1, files[1].Additions)
	assert.Equal(1, files[1].Deletions)
}

func TestWorktreeDiffFilesHidesWhitespaceOnlyChanges(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	work := setupDivergenceWorktree(t)

	require.NoError(os.WriteFile(
		filepath.Join(work, "f.txt"), []byte("f1  \n"), 0o644,
	))
	require.NoError(os.WriteFile(
		filepath.Join(work, "dirty-test.txt"), []byte("test\n"), 0o644,
	))

	files, ok, err := WorktreeDiffFiles(
		t.Context(), work, WorktreeDiffBaseHead, true,
	)
	require.NoError(err)
	require.True(ok)
	require.Len(files, 1)
	assert.Equal("dirty-test.txt", files[0].Path)
}

func TestWorktreeDiffFilesHidesWhitespaceOnlyUntrackedFiles(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	work := setupDivergenceWorktree(t)

	require.NoError(os.WriteFile(
		filepath.Join(work, "dirty-test.txt"), []byte("test\n"), 0o644,
	))
	require.NoError(os.WriteFile(
		filepath.Join(work, "z-blank.txt"), []byte(" \t\n"), 0o644,
	))
	require.NoError(os.WriteFile(
		filepath.Join(work, "z-empty.txt"), nil, 0o644,
	))

	files, ok, err := WorktreeDiffFiles(
		t.Context(), work, WorktreeDiffBaseHead, true,
	)
	require.NoError(err)
	require.True(ok)
	require.Len(files, 2)
	assert.Equal("dirty-test.txt", files[0].Path)
	assert.Equal("z-empty.txt", files[1].Path)
}

func TestWorktreeDiffFilesMarksGeneratedFiles(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	work := setupDivergenceWorktree(t)

	require.NoError(os.WriteFile(
		filepath.Join(work, ".gitattributes"),
		[]byte("dist/** linguist-generated\n"), 0o644,
	))
	require.NoError(os.MkdirAll(filepath.Join(work, "dist"), 0o755))
	require.NoError(os.WriteFile(
		filepath.Join(work, "dist", "api.ts"), []byte("export const api = 1;\n"), 0o644,
	))
	require.NoError(os.WriteFile(
		filepath.Join(work, "src.ts"), []byte("export const src = 1;\n"), 0o644,
	))

	files, ok, err := WorktreeDiffFiles(
		t.Context(), work, WorktreeDiffBaseHead, false,
	)
	require.NoError(err)
	require.True(ok)
	require.Len(files, 3)

	generated := map[string]bool{}
	for _, file := range files {
		generated[file.Path] = file.IsGenerated
	}
	assert.False(generated[".gitattributes"])
	assert.True(generated["dist/api.ts"])
	assert.False(generated["src.ts"])
}

func TestWorktreeFileDiffAgainstHeadScopesPatchToOnePath(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	work := setupDivergenceWorktree(t)

	require.NoError(os.WriteFile(
		filepath.Join(work, "f.txt"), []byte("dirty\n"), 0o644,
	))
	require.NoError(os.WriteFile(
		filepath.Join(work, "dirty-test.txt"), []byte("test\n"), 0o644,
	))

	diff, ok, err := WorktreeFileDiff(
		t.Context(), work, WorktreeDiffBaseHead, false, "f.txt",
	)
	require.NoError(err)
	require.True(ok)
	require.NotNil(diff)
	require.Len(diff.Files, 1)

	file := diff.Files[0]
	assert.Equal("f.txt", file.Path)
	assert.Equal("modified", file.Status)
	assert.Equal(1, file.Additions)
	assert.Equal(1, file.Deletions)
	require.Len(file.Hunks, 1)
	assert.NotEmpty(file.Hunks[0].Lines)
}

func TestWorktreeDiffAgainstPushedBranchIncludesLocalCommitsAndDirtyChanges(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	work := setupDivergenceWorktree(t)

	require.NoError(os.WriteFile(
		filepath.Join(work, "committed.go"), []byte("package committed\n"), 0o644,
	))
	runWorkspaceTestGit(t, work, "add", ".")
	runWorkspaceTestGit(t, work, "commit", "-m", "local commit")
	require.NoError(os.WriteFile(
		filepath.Join(work, "dirty.go"), []byte("package dirty\n"), 0o644,
	))

	diff, ok, err := WorktreeDiff(
		t.Context(), work, WorktreeDiffBasePushed, false,
	)
	require.NoError(err)
	require.True(ok)
	require.NotNil(diff)

	paths := make([]string, 0, len(diff.Files))
	for _, file := range diff.Files {
		paths = append(paths, file.Path)
	}
	assert.Contains(paths, "committed.go")
	assert.Contains(paths, "dirty.go")
	assert.Equal(0, diff.WhitespaceOnlyCount)
}

func TestWorktreeDiffWhitespaceOnlyCountBetweenUsesRangeRefs(t *testing.T) {
	require := require.New(t)
	work := setupDivergenceWorktree(t)
	baseSHA := strings.TrimSpace(
		string(runWorkspaceTestGit(t, work, "rev-parse", "HEAD")),
	)

	require.NoError(os.WriteFile(
		filepath.Join(work, "f.txt"), []byte("f1  \n"), 0o644,
	))
	runWorkspaceTestGit(t, work, "add", ".")
	runWorkspaceTestGit(t, work, "commit", "-m", "whitespace change")
	require.NoError(os.WriteFile(
		filepath.Join(work, "base.txt"), []byte("base changed\n"), 0o644,
	))
	runWorkspaceTestGit(t, work, "add", ".")
	runWorkspaceTestGit(t, work, "commit", "-m", "content change")
	headSHA := strings.TrimSpace(
		string(runWorkspaceTestGit(t, work, "rev-parse", "HEAD")),
	)

	count, ok, err := WorktreeDiffWhitespaceOnlyCountBetween(
		t.Context(), work, baseSHA, headSHA,
	)
	require.NoError(err)
	require.True(ok)
	Assert.New(t).Equal(1, count)
}

func TestWorktreeDiffAgainstMergeTargetUsesMergeBase(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	work := setupDivergenceWorktree(t)

	other := filepath.Join(filepath.Dir(work), "other")
	remote := filepath.Join(filepath.Dir(work), "remote.git")
	runWorkspaceTestGit(t, filepath.Dir(work), "clone", remote, other)
	runWorkspaceTestGit(t, other, "config", "user.email", "o@test.com")
	runWorkspaceTestGit(t, other, "config", "user.name", "Other")
	require.NoError(os.WriteFile(
		filepath.Join(other, "target-only.txt"), []byte("target\n"), 0o644,
	))
	runWorkspaceTestGit(t, other, "add", ".")
	runWorkspaceTestGit(t, other, "commit", "-m", "target branch advance")
	runWorkspaceTestGit(t, other, "push", "origin", "main")
	runWorkspaceTestGit(t, work, "fetch", "origin", "main")

	require.NoError(os.WriteFile(
		filepath.Join(work, "committed.go"), []byte("package committed\n"), 0o644,
	))
	runWorkspaceTestGit(t, work, "add", ".")
	runWorkspaceTestGit(t, work, "commit", "-m", "local commit")
	require.NoError(os.WriteFile(
		filepath.Join(work, "dirty.go"), []byte("package dirty\n"), 0o644,
	))

	diff, ok, err := WorktreeDiffAgainstMergeTarget(
		t.Context(), work, "main", false,
	)
	require.NoError(err)
	require.True(ok)
	require.NotNil(diff)

	paths := make([]string, 0, len(diff.Files))
	for _, file := range diff.Files {
		paths = append(paths, file.Path)
	}
	assert.Contains(paths, "f.txt")
	assert.Contains(paths, "committed.go")
	assert.Contains(paths, "dirty.go")
	assert.NotContains(paths, "target-only.txt")
}

func TestWorktreeDiffAgainstPushedBranchWithoutTrackingBranch(t *testing.T) {
	require := require.New(t)
	root := t.TempDir()
	work := filepath.Join(root, "work")
	runWorkspaceTestGit(t, root, "init", "--initial-branch=main", work)
	runWorkspaceTestGit(t, work, "config", "user.email", "t@test.com")
	runWorkspaceTestGit(t, work, "config", "user.name", "Test")
	require.NoError(os.WriteFile(
		filepath.Join(work, "x.txt"), []byte("x\n"), 0o644,
	))
	runWorkspaceTestGit(t, work, "add", ".")
	runWorkspaceTestGit(t, work, "commit", "-m", "init")

	diff, ok, err := WorktreeDiff(
		t.Context(), work, WorktreeDiffBasePushed, false,
	)
	require.NoError(err)
	require.False(ok)
	require.Nil(diff)
}

func TestWorktreeDiffRendersUntrackedSymlinkTarget(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	root := t.TempDir()
	work := filepath.Join(root, "work")
	secret := filepath.Join(root, "secret.txt")
	runWorkspaceTestGit(t, root, "init", "--initial-branch=main", work)
	runWorkspaceTestGit(t, work, "config", "user.email", "t@test.com")
	runWorkspaceTestGit(t, work, "config", "user.name", "Test")
	require.NoError(os.WriteFile(
		filepath.Join(work, "tracked.txt"), []byte("tracked\n"), 0o644,
	))
	runWorkspaceTestGit(t, work, "add", ".")
	runWorkspaceTestGit(t, work, "commit", "-m", "init")
	require.NoError(os.WriteFile(secret, []byte("do not expose\n"), 0o644))
	requireSymlink(t, secret, filepath.Join(work, "secret-link"))

	diff, ok, err := WorktreeDiff(
		t.Context(), work, WorktreeDiffBaseHead, false,
	)
	require.NoError(err)
	require.True(ok)
	require.NotNil(diff)
	require.Len(diff.Files, 1)
	require.Len(diff.Files[0].Hunks, 1)

	file := diff.Files[0]
	assert.Equal("secret-link", file.Path)
	assert.Equal("added", file.Status)
	assert.Equal(1, file.Additions)
	assert.False(file.IsBinary)
	require.Len(file.Hunks[0].Lines, 1)
	assert.Equal(secret, file.Hunks[0].Lines[0].Content)
	assert.True(file.Hunks[0].Lines[0].NoNewline)
	assert.NotContains(file.Hunks[0].Lines[0].Content, "do not expose")
}

func TestOpenRegularUntrackedFileRejectsSymlinks(t *testing.T) {
	require := require.New(t)
	root := t.TempDir()
	secret := filepath.Join(root, "secret.txt")
	link := filepath.Join(root, "secret-link")
	require.NoError(os.WriteFile(secret, []byte("do not expose\n"), 0o644))
	requireSymlink(t, secret, link)

	file, _, err := openRegularUntrackedFile(link)
	require.Error(err)
	require.Nil(file)
}

func requireSymlink(t *testing.T, oldname string, newname string) {
	t.Helper()
	err := os.Symlink(oldname, newname)
	if err != nil && strings.Contains(
		err.Error(),
		"A required privilege is not held by the client",
	) {
		t.Skipf("symlink privilege unavailable: %v", err)
	}
	require.NoError(t, err)
}

func TestWorktreeDiffMarksLargeUntrackedFileBinary(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	work := setupDivergenceWorktree(t)
	require.NoError(os.WriteFile(
		filepath.Join(work, "large.txt"),
		bytes.Repeat([]byte("x"), maxUntrackedTextFileBytes+1),
		0o644,
	))

	diff, ok, err := WorktreeDiff(
		t.Context(), work, WorktreeDiffBaseHead, false,
	)
	require.NoError(err)
	require.True(ok)
	require.NotNil(diff)
	require.Len(diff.Files, 1)

	file := diff.Files[0]
	assert.Equal("large.txt", file.Path)
	assert.True(file.IsBinary)
	assert.Zero(file.Additions)
	assert.Empty(file.Hunks)
}
