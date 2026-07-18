package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/gitclone"
)

func TestResolveDiffSnapshotSpec(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	assert := assert.New(t)
	work := setupDivergenceWorktree(t)

	resolved, ok, err := ResolveDiffSnapshotSpec(t.Context(), DiffSnapshotSpec{
		WorktreePath: work,
		Base:         WorktreeDiffBaseHead,
	})

	require.NoError(err)
	require.True(ok)
	assert.Equal(WorktreeDiffBaseHead, resolved.Base)
	assert.Equal("HEAD", resolved.BaseRef)
	assert.Len(resolved.BaseOID, 40)
	assert.Len(resolved.HeadOID, 40)
	assert.True(resolved.IncludeUntracked)
	assert.Equal(filepath.Clean(work), resolved.WorktreePath)
}

func TestFingerprintDiffSnapshotDetectsDirtyContentWithoutSizeChange(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	work := setupDivergenceWorktree(t)
	path := filepath.Join(work, "f.txt")

	require.NoError(os.WriteFile(path, []byte("a1\n"), 0o644))
	resolved, ok, err := ResolveDiffSnapshotSpec(t.Context(), DiffSnapshotSpec{
		WorktreePath: work,
		Base:         WorktreeDiffBaseHead,
	})
	require.NoError(err)
	require.True(ok)
	first, err := FingerprintDiffSnapshot(t.Context(), resolved)
	require.NoError(err)

	require.NoError(os.WriteFile(path, []byte("b1\n"), 0o644))
	second, err := FingerprintDiffSnapshot(t.Context(), resolved)
	require.NoError(err)
	assert.NotEqual(t, first, second)
}

func TestDiffContentDigestHonorsCancellation(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "large.txt")
	require.NoError(t, os.WriteFile(path, make([]byte, 1<<20), 0o600))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, _, err := diffContentDigest(ctx, path)

	require.ErrorIs(t, err, context.Canceled)
}

func TestFingerprintDiffSnapshotDetectsUntrackedContentChange(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	work := setupDivergenceWorktree(t)
	path := filepath.Join(work, "new.txt")

	require.NoError(os.WriteFile(path, []byte("a1\n"), 0o644))
	resolved, ok, err := ResolveDiffSnapshotSpec(t.Context(), DiffSnapshotSpec{
		WorktreePath: work,
		Base:         WorktreeDiffBaseHead,
	})
	require.NoError(err)
	require.True(ok)
	first, err := FingerprintDiffSnapshot(t.Context(), resolved)
	require.NoError(err)

	require.NoError(os.WriteFile(path, []byte("b1\n"), 0o644))
	second, err := FingerprintDiffSnapshot(t.Context(), resolved)
	require.NoError(err)
	assert.NotEqual(t, first, second)
}

func TestFingerprintDiffSnapshotRejectsIntermediateSymlink(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	work := t.TempDir()
	outside := t.TempDir()

	runWorkspaceTestGit(t, work, "init", "--initial-branch=main")
	runWorkspaceTestGit(t, work, "config", "user.email", "t@test.com")
	runWorkspaceTestGit(t, work, "config", "user.name", "Test")
	require.NoError(os.MkdirAll(filepath.Join(work, "nested"), 0o755))
	require.NoError(os.WriteFile(filepath.Join(work, "nested", "tracked.txt"), []byte("inside\n"), 0o644))
	runWorkspaceTestGit(t, work, "add", "nested/tracked.txt")
	runWorkspaceTestGit(t, work, "commit", "-m", "fixture")

	require.NoError(os.WriteFile(filepath.Join(outside, "tracked.txt"), []byte("outside\n"), 0o600))
	require.NoError(os.RemoveAll(filepath.Join(work, "nested")))
	require.NoError(os.Symlink(outside, filepath.Join(work, "nested")))

	resolved, ok, err := ResolveDiffSnapshotSpec(t.Context(), DiffSnapshotSpec{
		WorktreePath: work,
		Base:         WorktreeDiffBaseHead,
	})
	require.NoError(err)
	require.True(ok)

	_, err = FingerprintDiffSnapshot(t.Context(), resolved)
	require.Error(err)
}

func TestFingerprintDiffSnapshotDetectsRepositoryAttributeChange(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	work := setupDivergenceWorktree(t)
	require.NoError(os.WriteFile(filepath.Join(work, "f.txt"), []byte("dirty\n"), 0o644))

	resolved, ok, err := ResolveDiffSnapshotSpec(t.Context(), DiffSnapshotSpec{
		WorktreePath: work,
		Base:         WorktreeDiffBaseHead,
	})
	require.NoError(err)
	require.True(ok)
	first, err := FingerprintDiffSnapshot(t.Context(), resolved)
	require.NoError(err)

	gitDir := strings.TrimSpace(string(runWorkspaceTestGit(t, work, "rev-parse", "--git-dir")))
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(work, gitDir)
	}
	require.NoError(os.MkdirAll(filepath.Join(gitDir, "info"), 0o755))
	require.NoError(os.WriteFile(
		filepath.Join(gitDir, "info", "attributes"),
		[]byte("*.txt linguist-generated\n"),
		0o644,
	))
	second, err := FingerprintDiffSnapshot(t.Context(), resolved)
	require.NoError(err)
	assert.NotEqual(t, first, second)
}

func TestFingerprintDiffSnapshotRangeIgnoresWorktree(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	work := setupDivergenceWorktree(t)
	from := strings.TrimSpace(string(runWorkspaceTestGit(t, work, "rev-parse", "HEAD^")))
	to := strings.TrimSpace(string(runWorkspaceTestGit(t, work, "rev-parse", "HEAD")))

	resolved, ok, err := ResolveDiffSnapshotSpec(t.Context(), DiffSnapshotSpec{
		WorktreePath: work,
		FromSHA:      from,
		ToSHA:        to,
	})
	require.NoError(err)
	require.True(ok)
	first, err := FingerprintDiffSnapshot(t.Context(), resolved)
	require.NoError(err)

	require.NoError(os.WriteFile(filepath.Join(work, "unrelated.txt"), []byte("dirty\n"), 0o644))
	second, err := FingerprintDiffSnapshot(t.Context(), resolved)
	require.NoError(err)
	assert.Equal(t, first, second)
}

func TestFingerprintDiffSnapshotRangeDetectsRepositoryAttributeChange(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	assert := assert.New(t)
	work := setupDivergenceWorktree(t)
	from := strings.TrimSpace(string(runWorkspaceTestGit(t, work, "rev-parse", "HEAD^")))
	to := strings.TrimSpace(string(runWorkspaceTestGit(t, work, "rev-parse", "HEAD")))

	resolved, ok, err := ResolveDiffSnapshotSpec(t.Context(), DiffSnapshotSpec{
		WorktreePath: work,
		FromSHA:      from,
		ToSHA:        to,
	})
	require.NoError(err)
	require.True(ok)
	first, err := FingerprintDiffSnapshot(t.Context(), resolved)
	require.NoError(err)

	gitDir := strings.TrimSpace(string(runWorkspaceTestGit(t, work, "rev-parse", "--git-dir")))
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(work, gitDir)
	}
	require.NoError(os.MkdirAll(filepath.Join(gitDir, "info"), 0o755))
	require.NoError(os.WriteFile(filepath.Join(gitDir, "info", "attributes"), []byte("*.txt linguist-generated\n"), 0o644))
	second, err := FingerprintDiffSnapshot(t.Context(), resolved)
	require.NoError(err)
	assert.NotEqual(first, second)
}

func TestPrepareDiffSnapshotRangeUsesCommittedAttributes(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	work := setupDivergenceWorktree(t)
	from := strings.TrimSpace(string(runWorkspaceTestGit(t, work, "rev-parse", "HEAD^")))
	to := strings.TrimSpace(string(runWorkspaceTestGit(t, work, "rev-parse", "HEAD")))
	require.NoError(os.WriteFile(
		filepath.Join(work, ".gitattributes"),
		[]byte("*.txt linguist-generated\n"),
		0o644,
	))

	resolved, ok, err := ResolveDiffSnapshotSpec(t.Context(), DiffSnapshotSpec{
		WorktreePath: work,
		FromSHA:      from,
		ToSHA:        to,
	})
	require.NoError(err)
	require.True(ok)
	diff, err := PrepareDiffSnapshot(t.Context(), resolved)
	require.NoError(err)
	require.Len(diff.Files, 1)
	assert.False(t, diff.Files[0].IsGenerated)
}

func TestReadDiffSnapshotFileRejectsIntermediateSymlink(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	worktree := t.TempDir()
	outside := t.TempDir()
	require.NoError(os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret\n"), 0o600))
	require.NoError(os.Symlink(outside, filepath.Join(worktree, "nested")))
	resolved := ResolvedDiffSnapshotSpec{
		DiffSnapshotSpec: DiffSnapshotSpec{WorktreePath: worktree},
		IncludeUntracked: true,
	}

	content, err := ReadDiffSnapshotFile(
		t.Context(), resolved, gitclone.DiffFile{Path: "nested/secret.txt", Status: "modified"}, "new", 1024,
	)

	require.Error(err)
	assert.Nil(t, content)
}

func TestPrepareDiffSnapshotUsesResolvedInputs(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	assert := assert.New(t)
	work := setupDivergenceWorktree(t)
	require.NoError(os.WriteFile(filepath.Join(work, "f.txt"), []byte("f1  \n"), 0o644))

	resolved, ok, err := ResolveDiffSnapshotSpec(t.Context(), DiffSnapshotSpec{
		WorktreePath: work,
		Base:         WorktreeDiffBaseHead,
	})
	require.NoError(err)
	require.True(ok)
	diff, err := PrepareDiffSnapshot(t.Context(), resolved)
	require.NoError(err)
	require.Len(diff.Files, 1)
	assert.Equal("f.txt", diff.Files[0].Path)
	assert.True(diff.Files[0].IsWhitespaceOnly)
	assert.Equal(1, diff.WhitespaceOnlyCount)
}
