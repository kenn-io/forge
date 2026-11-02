package workspace

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/gitclone"
	"go.kenn.io/forge/internal/testutil/gitfixture"
)

func TestWorktreeDiffCopyDetection(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	repo := gitfixture.NewRepository(t, false)
	repo.Write(t, "keep.txt", "keep\nthis\nunchanged\n")
	repo.Stage(t, ".")
	gitfixture.Run(t, repo.Dir, "commit", "-m", "copy sources")
	repo.Write(t, "copy.txt", "keep\nthis\nunchanged\n")
	repo.Stage(t, ".")

	want := []gitclone.DiffFile{
		{Path: "copy.txt", OldPath: "copy.txt", Status: "added", Additions: 3, Hunks: []gitclone.Hunk{}},
	}
	for _, hideWhitespace := range []bool{false, true} {
		files, ok, err := WorktreeDiffFiles(t.Context(), repo.Dir, WorktreeDiffBaseHead, hideWhitespace)
		require.NoError(t, err)
		require.True(t, ok)
		assert.Equal(want, files)
		result, ok, err := WorktreeDiff(t.Context(), repo.Dir, WorktreeDiffBaseHead, hideWhitespace)
		require.NoError(t, err)
		require.True(t, ok)
		require.Len(t, result.Files, 1)
		assert.Contains(result.Files[0].Patch, "+keep\n+this\n+unchanged\n")
		for i := range result.Files {
			result.Files[i].Patch = ""
			result.Files[i].Hunks = []gitclone.Hunk{}
		}
		assert.Equal(want, result.Files)
	}
	added, removed, ok, err := WorktreeDiffTotals(t.Context(), repo.Dir, "")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(3, added)
	assert.Zero(removed)
}
