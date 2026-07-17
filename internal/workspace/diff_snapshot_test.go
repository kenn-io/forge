package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
