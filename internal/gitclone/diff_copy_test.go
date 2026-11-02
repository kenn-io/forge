package gitclone

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/testutil/gitfixture"
)

func TestDiffCopyDetection(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	repo := gitfixture.NewRepository(t, false)
	repo.Write(t, "source.txt", "one\ntwo\nthree\n")
	repo.Write(t, "old.txt", "rename me\n")
	repo.Stage(t, ".")
	gitfixture.Run(t, repo.Dir, "commit", "-m", "copy sources")
	base := gitfixture.SHA(t, repo.Dir, "HEAD")
	repo.Write(t, "copy.txt", "seed\n")
	repo.Write(t, "modified-copy.txt", "one\ntwo\nthree\n")
	repo.Write(t, "source.txt", "one\ntwo\nthree\nfour\n")
	gitfixture.Run(t, repo.Dir, "mv", "old.txt", "renamed.txt")
	repo.Stage(t, ".")
	gitfixture.Run(t, repo.Dir, "commit", "-m", "copies and rename")
	head := gitfixture.SHA(t, repo.Dir, "HEAD")
	mgr := New(t.TempDir(), nil)
	t.Cleanup(mgr.Wait)
	require.NoError(t, mgr.EnsureClone(t.Context(), "github", "example.com", "test", "repo", repo.Dir))

	want := []DiffFile{
		{Path: "copy.txt", OldPath: "copy.txt", Status: "added", Additions: 1, Hunks: []Hunk{}},
		{Path: "modified-copy.txt", OldPath: "source.txt", Status: "copied", Hunks: []Hunk{}},
		{Path: "renamed.txt", OldPath: "old.txt", Status: "renamed", Hunks: []Hunk{}},
		{Path: "source.txt", OldPath: "source.txt", Status: "modified", Additions: 1, Hunks: []Hunk{}},
	}
	files, err := mgr.DiffFiles(t.Context(), "github", "example.com", "test", "repo", base, head)
	require.NoError(t, err)
	assert.Equal(want, files)
	for _, hideWhitespace := range []bool{false, true} {
		result, err := mgr.Diff(t.Context(), "github", "example.com", "test", "repo", base, head, hideWhitespace)
		require.NoError(t, err)
		require.Len(t, result.Files, 4)
		assert.Contains(result.Files[0].Patch, "+seed\n")
		require.Len(t, result.Files[0].Hunks, 1)
		assert.Equal([]Line{{Type: "add", Content: "seed", NewNum: 1}}, result.Files[0].Hunks[0].Lines)
		for i := range result.Files {
			result.Files[i].Patch = ""
			result.Files[i].Hunks = []Hunk{}
		}
		assert.Equal(want, result.Files)
	}
}
