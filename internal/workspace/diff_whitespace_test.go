package workspace

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gitcmd "go.kenn.io/kit/git/cmd"
	"go.kenn.io/middleman/internal/gitclone"
)

func TestGitWhitespaceDigestMatchesFileSemantics(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name  string
		left  string
		right string
		want  bool
	}{
		{name: "indentation", left: "first\n\tvalue\n", right: "first\n  value\n", want: true},
		{name: "blank record", left: "first\nsecond\n", right: "first\n \nsecond\n", want: false},
		{name: "final newline", left: "first\n", right: "first", want: false},
		{name: "substantive", left: "old\n", right: "new\n", want: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			left, err := gitWhitespaceDigest(bytes.NewBufferString(tt.left), int64(len(tt.left)))
			require.NoError(t, err)
			right, err := gitWhitespaceDigest(bytes.NewBufferString(tt.right), int64(len(tt.right)))
			require.NoError(t, err)
			assert.Equal(t, tt.want, left == right)
		})
	}
}

func TestGitWhitespaceRecordEqual(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name  string
		left  string
		right string
		want  bool
	}{
		{name: "indentation", left: "\treturn value", right: "  return value", want: true},
		{name: "vertical tab and form feed", left: "a\vb\fc", right: "abc", want: true},
		{name: "carriage return", left: "value\r", right: "value", want: true},
		{name: "substantive", left: "return old", right: "return new", want: false},
		{name: "non ascii space", left: "a\u00a0b", right: "ab", want: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, gitWhitespaceRecordEqual(tt.left, tt.right))
		})
	}
}

func TestClassifyWhitespaceOnly(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)

	whitespaceHunk := gitclone.Hunk{Lines: []gitclone.Line{
		{Type: "context", Content: "func example() {"},
		{Type: "delete", Content: "\treturn"},
		{Type: "add", Content: "  return"},
		{Type: "context", Content: "}"},
	}}
	substantiveHunk := gitclone.Hunk{Lines: []gitclone.Line{
		{Type: "delete", Content: "old"},
		{Type: "add", Content: "new"},
	}}
	blankLineInsertion := gitclone.Hunk{Lines: []gitclone.Line{
		{Type: "context", Content: "first"},
		{Type: "add", Content: "  "},
		{Type: "context", Content: "second"},
	}}

	files := []gitclone.DiffFile{
		{Path: "whitespace.go", Status: "modified", Hunks: []gitclone.Hunk{whitespaceHunk}},
		{Path: "mixed.go", Status: "modified", Hunks: []gitclone.Hunk{whitespaceHunk, substantiveHunk}},
		{Path: "blank.go", Status: "modified", Hunks: []gitclone.Hunk{blankLineInsertion}},
		{Path: "binary.dat", Status: "modified", IsBinary: true, Hunks: []gitclone.Hunk{whitespaceHunk}},
		{Path: "renamed.go", Status: "renamed", Hunks: []gitclone.Hunk{whitespaceHunk}},
		{Path: "mode.go", Status: "modified", Hunks: []gitclone.Hunk{whitespaceHunk}},
	}

	count := classifyWhitespaceOnly(files, map[string]bool{"mode.go": true})

	assert.Equal(1, count)
	assert.True(files[0].IsWhitespaceOnly)
	assert.False(files[1].IsWhitespaceOnly)
	assert.False(files[2].IsWhitespaceOnly)
	assert.False(files[3].IsWhitespaceOnly)
	assert.False(files[4].IsWhitespaceOnly)
	assert.False(files[5].IsWhitespaceOnly)
}

func TestClassifyWhitespaceOnlyMatchesGit(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name string
		old  string
		new  string
	}{
		{name: "indentation", old: "first\n\tvalue\nlast\n", new: "first\n  value\nlast\n"},
		{name: "crlf", old: "first\r\nsecond\r\n", new: "first\nsecond\n"},
		{name: "blank line insertion", old: "first\nsecond\n", new: "first\n \nsecond\n"},
		{name: "missing final newline", old: "first\nsecond\n", new: "first\nsecond"},
		{name: "repeated lines", old: "same\n value\nsame\n", new: "same\n\tvalue\nsame\n"},
		{
			name: "distant repeated lines",
			old:  "start\n value\nsame\none\ntwo\nthree\nfour\nfive\nsix\nseven\nsame\nvalue\nend\n",
			new:  "start\nvalue\nsame\none\ntwo\nthree\nfour\nfive\nsix\nseven\nsame\n value\nend\n",
		},
		{name: "mixed edit", old: "first\n old\nlast\n", new: "first\n new\nlast\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require := require.New(t)
			assert := assert.New(t)
			work := t.TempDir()
			path := filepath.Join(work, "fixture.txt")

			runWorkspaceTestGit(t, work, "init", "--initial-branch=main")
			runWorkspaceTestGit(t, work, "config", "user.email", "t@test.com")
			runWorkspaceTestGit(t, work, "config", "user.name", "Test")
			require.NoError(os.WriteFile(path, []byte(tt.old), 0o644))
			runWorkspaceTestGit(t, work, "add", "fixture.txt")
			runWorkspaceTestGit(t, work, "commit", "-m", "fixture")
			require.NoError(os.WriteFile(path, []byte(tt.new), 0o644))
			_, _, gitErr := gitcmd.New().Run(
				t.Context(), work, nil,
				"diff", "--quiet", "-w", "HEAD", "--", "fixture.txt",
			)
			diff, ok, err := WorktreeDiff(
				t.Context(), work, WorktreeDiffBaseHead, false,
			)
			require.NoError(err)
			require.True(ok)
			require.Len(diff.Files, 1)
			assert.Equal(gitErr == nil, diff.Files[0].IsWhitespaceOnly)
		})
	}
}

func TestClassifyWorkspaceWhitespaceOnlyChecksWholeFileAcrossHunks(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	assert := assert.New(t)
	work := t.TempDir()
	path := filepath.Join(work, "fixture.txt")
	oldContent := "first\n value\nmiddle\nlast\n"
	newContent := "first\n\tvalue\nmiddle\nlast\n"
	runWorkspaceTestGit(t, work, "init", "--initial-branch=main")
	runWorkspaceTestGit(t, work, "config", "user.email", "t@test.com")
	runWorkspaceTestGit(t, work, "config", "user.name", "Test")
	require.NoError(os.WriteFile(path, []byte(oldContent), 0o644))
	runWorkspaceTestGit(t, work, "add", "fixture.txt")
	runWorkspaceTestGit(t, work, "commit", "-m", "fixture")
	require.NoError(os.WriteFile(path, []byte(newContent), 0o644))
	files := []gitclone.DiffFile{{
		Path: "fixture.txt", Status: "modified",
		Hunks: []gitclone.Hunk{
			{OldCount: 1, Lines: []gitclone.Line{{Type: "delete", Content: " value"}}},
			{NewCount: 1, Lines: []gitclone.Line{{Type: "add", Content: "\tvalue"}}},
		},
	}}

	count, err := classifyWorkspaceWhitespaceOnly(
		t.Context(), work, "HEAD", "", true, files, nil,
	)
	require.NoError(err)
	_, _, gitErr := gitcmd.New().Run(
		t.Context(), work, nil, "diff", "--quiet", "-w", "HEAD", "--", "fixture.txt",
	)

	require.NoError(gitErr)
	assert.Equal(1, count)
	assert.True(files[0].IsWhitespaceOnly)
}

func TestWorktreeDiffDoesNotClassifyModeChangeAsWhitespaceOnly(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	work := setupDivergenceWorktree(t)
	path := filepath.Join(work, "f.txt")

	require.NoError(os.WriteFile(path, []byte("f1  \n"), 0o755))
	require.NoError(os.Chmod(path, 0o755))
	diff, ok, err := WorktreeDiff(t.Context(), work, WorktreeDiffBaseHead, false)

	require.NoError(err)
	require.True(ok)
	require.Len(diff.Files, 1)
	assert.False(t, diff.Files[0].IsWhitespaceOnly)
	assert.Equal(t, 0, diff.WhitespaceOnlyCount)
}
