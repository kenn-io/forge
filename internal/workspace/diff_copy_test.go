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
	repo.Write(t, "source.txt", "one\ntwo\nthree\n")
	repo.Write(t, "old.txt", "rename me\n")
	repo.Stage(t, ".")
	gitfixture.Run(t, repo.Dir, "commit", "-m", "copy sources")
	repo.Write(t, "copy.txt", "seed\n")
	repo.Write(t, "modified-copy.txt", "one\ntwo\nthree\n")
	repo.Write(t, "source.txt", "one\ntwo\nthree\nfour\n")
	gitfixture.Run(t, repo.Dir, "mv", "old.txt", "renamed.txt")
	repo.Stage(t, ".")

	want := []gitclone.DiffFile{
		{Path: "copy.txt", OldPath: "copy.txt", Status: "added", Additions: 1, Hunks: []gitclone.Hunk{}},
		{Path: "modified-copy.txt", OldPath: "source.txt", Status: "copied", Hunks: []gitclone.Hunk{}},
		{Path: "renamed.txt", OldPath: "old.txt", Status: "renamed", Hunks: []gitclone.Hunk{}},
		{Path: "source.txt", OldPath: "source.txt", Status: "modified", Additions: 1, Hunks: []gitclone.Hunk{}},
	}
	for _, hideWhitespace := range []bool{false, true} {
		files, ok, err := WorktreeDiffFiles(t.Context(), repo.Dir, WorktreeDiffBaseHead, hideWhitespace)
		require.NoError(t, err)
		require.True(t, ok)
		assert.Equal(want, files)
		result, ok, err := WorktreeDiff(t.Context(), repo.Dir, WorktreeDiffBaseHead, hideWhitespace)
		require.NoError(t, err)
		require.True(t, ok)
		require.Len(t, result.Files, 4)
		assert.Contains(result.Files[0].Patch, "+seed\n")
		require.Len(t, result.Files[0].Hunks, 1)
		assert.Equal([]gitclone.Line{{Type: "add", Content: "seed", NewNum: 1}}, result.Files[0].Hunks[0].Lines)
		for i := range result.Files {
			result.Files[i].Patch = ""
			result.Files[i].Hunks = []gitclone.Hunk{}
		}
		assert.Equal(want, result.Files)
		file, ok, err := WorktreeFileDiff(t.Context(), repo.Dir, WorktreeDiffBaseHead, hideWhitespace, "copy.txt")
		require.NoError(t, err)
		require.True(t, ok)
		require.Len(t, file.Files, 1)
		assert.Equal("added", file.Files[0].Status)
		assert.Equal(1, file.Files[0].Additions)
		assert.Contains(file.Files[0].Patch, "+seed\n")
	}
	added, removed, ok, err := WorktreeDiffTotals(t.Context(), repo.Dir, "")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(2, added)
	assert.Zero(removed)
}
