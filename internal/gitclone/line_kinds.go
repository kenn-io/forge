package gitclone

import (
	"path/filepath"
	"strings"
)

// LineCounts are the lines a diff adds and deletes.
type LineCounts struct {
	Additions int `json:"additions"`
	Deletions int `json:"deletions"`
}

// LineKinds are a diff's changed lines by the kind of file they are in, so
// a reader can tell how much of a change is code.
type LineKinds struct {
	Code      LineCounts `json:"code" doc:"Lines in source code, configuration, and every other file not counted below."`
	Docs      LineCounts `json:"docs" doc:"Lines in prose files: Markdown, reStructuredText, AsciiDoc, and plain text."`
	Data      LineCounts `json:"data" doc:"Lines in data files: JSON, JSON Lines, CSV, TSV, SVG, and test snapshots."`
	Generated LineCounts `json:"generated" doc:"Lines in generated files and lockfiles, as the diff view marks them."`
}

var docsExtensions = map[string]bool{
	".adoc": true, ".markdown": true, ".md": true, ".mdx": true, ".rst": true, ".txt": true,
}

var dataExtensions = map[string]bool{
	".csv": true, ".json": true, ".jsonl": true, ".ndjson": true, ".snap": true, ".svg": true, ".tsv": true,
}

// CountLineKinds totals files' changed lines by kind. A file the diff view
// marks generated is generated whatever its extension.
func CountLineKinds(files []DiffFile) LineKinds {
	var kinds LineKinds
	for _, file := range files {
		counts := &kinds.Code
		extension := strings.ToLower(filepath.Ext(file.Path))
		switch {
		case file.IsGenerated:
			counts = &kinds.Generated
		case docsExtensions[extension]:
			counts = &kinds.Docs
		case dataExtensions[extension]:
			counts = &kinds.Data
		}
		counts.Additions += file.Additions
		counts.Deletions += file.Deletions
	}
	return kinds
}
