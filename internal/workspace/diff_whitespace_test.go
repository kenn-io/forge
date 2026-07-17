package workspace

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gitcmd "go.kenn.io/kit/git/cmd"
	"go.kenn.io/middleman/internal/gitclone"
)

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
	}

	count := classifyWhitespaceOnly(files)

	assert.Equal(1, count)
	assert.True(files[0].IsWhitespaceOnly)
	assert.False(files[1].IsWhitespaceOnly)
	assert.False(files[2].IsWhitespaceOnly)
	assert.False(files[3].IsWhitespaceOnly)
	assert.False(files[4].IsWhitespaceOnly)
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
