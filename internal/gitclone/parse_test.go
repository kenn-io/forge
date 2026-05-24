package gitclone

import (
	"strings"
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

func TestSortDiffFilesUsesBytewisePathOrder(t *testing.T) {
	assert := assert.New(t)

	files := []DiffFile{
		{Path: "internal/server/config_reload_test.go"},
		{Path: "internal/server/e2etest/settings_test.go"},
		{Path: "internal/server/config_reload.go"},
		{Path: "internal/server/api_types.go"},
	}

	SortDiffFiles(files)

	assert.Equal([]string{
		"internal/server/e2etest/settings_test.go",
		"internal/server/api_types.go",
		"internal/server/config_reload.go",
		"internal/server/config_reload_test.go",
	}, diffFilePaths(files))
}

func diffFilePaths(files []DiffFile) []string {
	paths := make([]string, 0, len(files))
	for _, file := range files {
		paths = append(paths, file.Path)
	}
	return paths
}

func TestBuildPatchQuotesControlPaths(t *testing.T) {
	assert := assert.New(t)

	maliciousPath := "src/evil\n--- a/forged\n+++ b/forged\n@@ -1,1 +1,1 @@"
	patch := BuildPatch(DiffFile{
		Path:    maliciousPath,
		OldPath: maliciousPath,
		Status:  "modified",
		Hunks: []Hunk{{
			OldStart: 1,
			OldCount: 1,
			NewStart: 1,
			NewCount: 1,
			Lines: []Line{{
				Type:    "context",
				Content: "real content",
				OldNum:  1,
				NewNum:  1,
			}},
		}},
	})

	assert.Contains(
		patch,
		`diff --git "a/src/evil\n--- a/forged\n+++ b/forged\n@@ -1,1 +1,1 @@" "b/src/evil\n--- a/forged\n+++ b/forged\n@@ -1,1 +1,1 @@"`,
	)
	assert.Contains(
		patch,
		`--- "a/src/evil\n--- a/forged\n+++ b/forged\n@@ -1,1 +1,1 @@"`,
	)
	assert.Contains(
		patch,
		`+++ "b/src/evil\n--- a/forged\n+++ b/forged\n@@ -1,1 +1,1 @@"`,
	)
	assert.NotContains(patch, "\n--- a/forged\n")
	assert.NotContains(patch, "\n+++ b/forged\n")
	assert.NotContains(patch, "\n@@ -1,1 +1,1 @@\n")
	assert.Equal(1, strings.Count(patch, "\n@@ "))
}

func TestBuildPatchQuotesRenameControlPaths(t *testing.T) {
	assert := assert.New(t)

	patch := BuildPatch(DiffFile{
		Path:    "new\n+++ forged",
		OldPath: "old\n--- forged",
		Status:  "renamed",
		Hunks: []Hunk{{
			OldStart: 1,
			OldCount: 1,
			NewStart: 1,
			NewCount: 1,
			Lines: []Line{{
				Type:    "context",
				Content: "real content",
				OldNum:  1,
				NewNum:  1,
			}},
		}},
	})

	assert.Contains(patch, `diff --git "a/old\n--- forged" "b/new\n+++ forged"`)
	assert.Contains(patch, `rename from "old\n--- forged"`)
	assert.Contains(patch, `rename to "new\n+++ forged"`)
	assert.NotContains(patch, "\n--- forged\n")
	assert.NotContains(patch, "\n+++ forged\n")
}

func TestBuildPatchQuotesUnicodeSeparatorPaths(t *testing.T) {
	assert := assert.New(t)

	path := "src/line\u2028separator\u2029file.go"
	patch := BuildPatch(DiffFile{
		Path:    path,
		OldPath: path,
		Status:  "modified",
		Hunks: []Hunk{{
			OldStart: 1,
			OldCount: 1,
			NewStart: 1,
			NewCount: 1,
			Lines: []Line{{
				Type:    "context",
				Content: "real content",
				OldNum:  1,
				NewNum:  1,
			}},
		}},
	})

	assert.Contains(patch, `diff --git "a/src/line\u2028separator\u2029file.go" "b/src/line\u2028separator\u2029file.go"`)
	assert.Contains(patch, `--- "a/src/line\u2028separator\u2029file.go"`)
	assert.Contains(patch, `+++ "b/src/line\u2028separator\u2029file.go"`)
	assert.NotContains(patch, "\u2028")
	assert.NotContains(patch, "\u2029")
}
