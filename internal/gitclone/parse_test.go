package gitclone

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseRawZ(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	// git diff --raw -z output: status\0path\0 (NUL-delimited)
	// M for modified, A for added, D for deleted
	// Renames: R100\0oldpath\0newpath\0
	raw := ":100644 100644 abc def M\x00src/main.go\x00" +
		":000000 100644 000 abc A\x00src/new.go\x00" +
		":100644 000000 abc 000 D\x00src/old.go\x00" +
		":100644 100644 abc def R100\x00src/before.go\x00src/after.go\x00"

	files := ParseRawZ([]byte(raw))
	require.Len(files, 4)

	assert.Equal("src/main.go", files[0].Path)
	assert.Equal("modified", files[0].Status)

	assert.Equal("src/new.go", files[1].Path)
	assert.Equal("added", files[1].Status)

	assert.Equal("src/old.go", files[2].Path)
	assert.Equal("deleted", files[2].Status)

	assert.Equal("src/after.go", files[3].Path)
	assert.Equal("src/before.go", files[3].OldPath)
	assert.Equal("renamed", files[3].Status)
}

func TestParsePatch(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	patch := `diff --git a/src/main.go b/src/main.go
index abc..def 100644
--- a/src/main.go
+++ b/src/main.go
@@ -10,6 +10,8 @@ func main() {
 	fmt.Println("hello")
 	fmt.Println("world")
+	fmt.Println("new line 1")
+	fmt.Println("new line 2")
 	fmt.Println("end")
-	fmt.Println("removed")
 }
`

	// Provide pre-populated file metadata from --raw -z.
	rawFiles := []DiffFile{
		{Path: "src/main.go", OldPath: "src/main.go", Status: "modified"},
	}

	files := ParsePatch([]byte(patch), rawFiles)
	require.Len(files, 1)

	f := files[0]
	assert.Equal("src/main.go", f.Path)
	assert.Equal(2, f.Additions)
	assert.Equal(1, f.Deletions)
	assert.False(f.IsBinary)
	assert.Contains(f.Patch, "diff --git a/src/main.go b/src/main.go\n")
	assert.Contains(f.Patch, "@@ -10,6 +10,8 @@ func main() {\n")
	assert.Contains(f.Patch, "+\tfmt.Println(\"new line 1\")\n")

	require.Len(f.Hunks, 1)
	h := f.Hunks[0]
	assert.Equal(10, h.OldStart)
	assert.Equal(6, h.OldCount)
	assert.Equal(10, h.NewStart)
	assert.Equal(8, h.NewCount)
	assert.Equal("func main() {", h.Section)

	// Check line types.
	types := make([]string, len(h.Lines))
	for i, l := range h.Lines {
		types[i] = l.Type
	}
	assert.Equal([]string{
		"context", "context", "add", "add", "context", "delete", "context",
	}, types)
}

func TestParsePatchNoNewline(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	patch := `diff --git a/file.txt b/file.txt
index abc..def 100644
--- a/file.txt
+++ b/file.txt
@@ -1,2 +1,2 @@
 line1
-line2
\ No newline at end of file
+line2-modified
\ No newline at end of file
`
	rawFiles := []DiffFile{
		{Path: "file.txt", OldPath: "file.txt", Status: "modified"},
	}
	files := ParsePatch([]byte(patch), rawFiles)
	require.Len(files, 1)
	require.Len(files[0].Hunks, 1)

	lines := files[0].Hunks[0].Lines
	require.Len(lines, 3) // context + delete + add

	assert.True(lines[1].NoNewline, "deleted line should have no_newline")
	assert.True(lines[2].NoNewline, "added line should have no_newline")
	assert.Contains(files[0].Patch, "\\ No newline at end of file\n")
}

func TestTreeData(t *testing.T) {
	assert := assert.New(t)

	files := []DiffFile{
		{Path: "src/changed.go", Status: "modified"},
		{Path: "src/new.go", Status: "added"},
		{Path: "src/copy.go", Status: "copied"},
	}

	assert.Equal([]string{"src/changed.go", "src/new.go", "src/copy.go"}, TreePaths(files))
	assert.Equal([]TreeStatus{
		{Path: "src/changed.go", Status: "modified"},
		{Path: "src/new.go", Status: "added"},
		{Path: "src/copy.go", Status: "renamed"},
	}, TreeGitStatus(files))
}
