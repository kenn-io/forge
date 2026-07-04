package gitclone

import (
	"encoding/json"
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
	assert.Equal("100644", files[0].OldMode)
	assert.Equal("100644", files[0].NewMode)

	assert.Equal("src/new.go", files[1].Path)
	assert.Equal("added", files[1].Status)
	assert.Equal("000000", files[1].OldMode)
	assert.Equal("100644", files[1].NewMode)

	assert.Equal("src/old.go", files[2].Path)
	assert.Equal("deleted", files[2].Status)
	assert.Equal("100644", files[2].OldMode)
	assert.Equal("000000", files[2].NewMode)

	assert.Equal("src/after.go", files[3].Path)
	assert.Equal("src/before.go", files[3].OldPath)
	assert.Equal("renamed", files[3].Status)
	assert.Equal("100644", files[3].OldMode)
	assert.Equal("100644", files[3].NewMode)
}

func TestDiffFileModesStayOutOfJSON(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	raw := ":100644 100755 abc def M\x00scripts/run.sh\x00"
	files := ParseRawZ([]byte(raw))
	require.Len(files, 1)
	assert.Equal("100644", files[0].OldMode)
	assert.Equal("100755", files[0].NewMode)

	data, err := json.Marshal(files[0])
	require.NoError(err)
	assert.NotContains(string(data), "OldMode")
	assert.NotContains(string(data), "NewMode")
	assert.NotContains(string(data), "old_mode")
	assert.NotContains(string(data), "new_mode")
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

func TestParsePatchMatchesPatchEntriesByPath(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	patch := `diff --git a/docs/guide.md b/docs/guide.md
deleted file mode 120000
index 1111111..0000000
--- a/docs/guide.md
+++ /dev/null
@@ -1 +0,0 @@
-README.md
diff --git a/docs/guide.md b/docs/guide.md
new file mode 100644
index 0000000..2222222
--- /dev/null
+++ b/docs/guide.md
@@ -0,0 +1,2 @@
+# Example Guide
+Use generic fixture data.
diff --git a/src/report.go b/src/report.go
index 3333333..4444444 100644
--- a/src/report.go
+++ b/src/report.go
@@ -1,3 +1,2 @@
 package src
-func oldReport() {}
-func removedReport() {}
+func newReport() {}
`

	rawFiles := []DiffFile{
		{Path: "docs/guide.md", OldPath: "docs/guide.md", Status: "modified"},
		{Path: "src/report.go", OldPath: "src/report.go", Status: "modified"},
	}

	files := ParsePatch([]byte(patch), rawFiles)
	require.Len(files, 2)

	assert.Equal("docs/guide.md", files[0].Path)
	assert.Equal(2, files[0].Additions)
	assert.Equal(1, files[0].Deletions)
	assert.Contains(files[0].Patch, "+# Example Guide\n")

	assert.Equal("src/report.go", files[1].Path)
	assert.Equal(1, files[1].Additions)
	assert.Equal(2, files[1].Deletions)
	assert.Contains(files[1].Patch, "+func newReport() {}\n")
	assert.NotContains(files[1].Patch, "# Example Guide")
	assert.NotContains(files[1].Patch, "Use generic fixture data.")
}

func TestParsePatchKeepsModifiedSourcePatchSeparateFromCopy(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	patch := `diff --git a/src/source.txt b/src/source.txt
index 1111111..2222222 100644
--- a/src/source.txt
+++ b/src/source.txt
@@ -1,2 +1,2 @@
-base line
+changed line
 shared line
diff --git a/src/source.txt b/src/copied.txt
similarity index 100%
copy from src/source.txt
copy to src/copied.txt
`

	rawFiles := []DiffFile{
		{Path: "src/source.txt", OldPath: "src/source.txt", Status: "modified"},
		{Path: "src/copied.txt", OldPath: "src/source.txt", Status: "copied"},
	}

	files := ParsePatch([]byte(patch), rawFiles)
	require.Len(files, 2)

	source := files[0]
	assert.Equal("src/source.txt", source.Path)
	assert.Equal(1, source.Additions)
	assert.Equal(1, source.Deletions)
	assert.Contains(source.Patch, "diff --git a/src/source.txt b/src/source.txt\n")
	assert.Contains(source.Patch, "+changed line\n")
	assert.NotContains(source.Patch, "copy to src/copied.txt")

	copied := files[1]
	assert.Equal("src/copied.txt", copied.Path)
	assert.Zero(copied.Additions)
	assert.Zero(copied.Deletions)
	assert.Contains(copied.Patch, "diff --git a/src/source.txt b/src/copied.txt\n")
	assert.Contains(copied.Patch, "copy from src/source.txt\n")
	assert.Contains(copied.Patch, "copy to src/copied.txt\n")
	assert.NotContains(copied.Patch, "--- a/src/source.txt")
	assert.NotContains(copied.Patch, "+++ b/src/copied.txt")
	assert.Empty(copied.Hunks)
}

func TestParsePatchReturnsEmptySliceWithoutRawMetadata(t *testing.T) {
	assert := assert.New(t)

	patch := `diff --git a/src/example.go b/src/example.go
index 1111111..2222222 100644
--- a/src/example.go
+++ b/src/example.go
@@ -1 +1 @@
-old
+new
`

	files := ParsePatch([]byte(patch), nil)

	assert.NotNil(files)
	assert.Empty(files)
}

func TestParsePatchSynthesizesUnmatchedPatchEntriesWhenRawMetadataIsPartial(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	patch := `diff --git a/src/kept.go b/src/kept.go
index 1111111..2222222 100644
--- a/src/kept.go
+++ b/src/kept.go
@@ -1 +1 @@
-old
+new
diff --git a/src/missing.go b/src/missing.go
new file mode 100644
index 0000000..3333333
--- /dev/null
+++ b/src/missing.go
@@ -0,0 +1 @@
+missing
`
	rawFiles := []DiffFile{
		{Path: "src/kept.go", OldPath: "src/kept.go", Status: "modified"},
	}

	files := ParsePatch([]byte(patch), rawFiles)

	require.Len(files, 2)
	kept := files[0]
	assert.Equal("src/kept.go", kept.Path)
	assert.Equal(1, kept.Additions)
	assert.Equal(1, kept.Deletions)

	missing := files[1]
	assert.Equal("src/missing.go", missing.Path)
	assert.Equal("added", missing.Status)
	assert.Equal(1, missing.Additions)
	assert.Zero(missing.Deletions)
	assert.Contains(missing.Patch, "diff --git a/src/missing.go b/src/missing.go\n")
	assert.Contains(missing.Patch, "+missing\n")
	require.Len(missing.Hunks, 1)
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

func TestBuildPatchCompletesHunklessExtendedHeaderSections(t *testing.T) {
	assert := assert.New(t)

	tests := []struct {
		name     string
		file     DiffFile
		contains []string
		absent   []string
	}{
		{
			name: "rename only",
			file: DiffFile{
				Path: "src/new.go", OldPath: "src/old.go", Status: "renamed",
			},
			contains: []string{
				"diff --git a/src/old.go b/src/new.go\n",
				"rename from src/old.go\n",
				"rename to src/new.go\n",
			},
			absent: []string{"--- ", "+++ ", "@@ "},
		},
		{
			name: "copy only",
			file: DiffFile{
				Path: "src/copied.go", OldPath: "src/source.go", Status: "copied",
			},
			contains: []string{
				"diff --git a/src/source.go b/src/copied.go\n",
				"copy from src/source.go\n",
				"copy to src/copied.go\n",
			},
			absent: []string{"--- ", "+++ ", "@@ "},
		},
		{
			name: "mode only",
			file: DiffFile{
				Path: "scripts/run.sh", OldPath: "scripts/run.sh", Status: "modified",
				OldMode: "100644", NewMode: "100755",
			},
			contains: []string{
				"diff --git a/scripts/run.sh b/scripts/run.sh\n",
				"old mode 100644\n",
				"new mode 100755\n",
			},
			absent: []string{"--- ", "+++ ", "@@ "},
		},
		{
			name: "binary",
			file: DiffFile{
				Path: "assets/logo.png", OldPath: "assets/logo.png", Status: "modified", IsBinary: true,
			},
			contains: []string{
				"diff --git a/assets/logo.png b/assets/logo.png\n",
				"Binary files a/assets/logo.png and b/assets/logo.png differ\n",
			},
			absent: []string{"--- ", "+++ ", "@@ "},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			patch := BuildPatch(tt.file)
			for _, want := range tt.contains {
				assert.Contains(patch, want)
			}
			for _, absent := range tt.absent {
				assert.NotContains(patch, absent)
			}
			assert.True(strings.HasSuffix(patch, "\n"))
		})
	}
}

func TestBuildPatchUsesRealModesForContentChanges(t *testing.T) {
	assert := assert.New(t)

	patch := BuildPatch(DiffFile{
		Path: "scripts/run.sh", OldPath: "scripts/run.sh", Status: "modified",
		OldMode: "100644", NewMode: "100755",
		Hunks: []Hunk{{
			OldStart: 1, OldCount: 1, NewStart: 1, NewCount: 1,
			Lines: []Line{
				{Type: "delete", Content: "echo old", OldNum: 1},
				{Type: "add", Content: "echo new", NewNum: 1},
			},
		}},
	})

	assert.Contains(patch, "old mode 100644\n")
	assert.Contains(patch, "new mode 100755\n")
	assert.Contains(patch, "--- a/scripts/run.sh\n")
	assert.Contains(patch, "+++ b/scripts/run.sh\n")
	assert.Contains(patch, "-echo old\n")
	assert.Contains(patch, "+echo new\n")
}

func TestBuildPatchUsesRawModesForAddsAndDeletes(t *testing.T) {
	assert := assert.New(t)

	added := BuildPatch(DiffFile{
		Path: "scripts/install.sh", Status: "added", OldMode: "000000", NewMode: "100755",
		Hunks: []Hunk{{
			OldStart: 0, OldCount: 0, NewStart: 1, NewCount: 1,
			Lines: []Line{{Type: "add", Content: "#!/bin/sh", NewNum: 1}},
		}},
	})
	assert.Contains(added, "new file mode 100755\n")
	assert.Contains(added, "--- /dev/null\n")
	assert.Contains(added, "+++ b/scripts/install.sh\n")

	deleted := BuildPatch(DiffFile{
		Path: "scripts/remove.sh", OldPath: "scripts/remove.sh", Status: "deleted",
		OldMode: "100755", NewMode: "000000",
		Hunks: []Hunk{{
			OldStart: 1, OldCount: 1, NewStart: 0, NewCount: 0,
			Lines: []Line{{Type: "delete", Content: "#!/bin/sh", OldNum: 1}},
		}},
	})
	assert.Contains(deleted, "deleted file mode 100755\n")
	assert.Contains(deleted, "--- a/scripts/remove.sh\n")
	assert.Contains(deleted, "+++ /dev/null\n")
}
