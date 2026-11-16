package gitclone

import (
	"path"
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
	Code      LineCounts `json:"code" doc:"Lines in source code, build and dependency manifests, configuration, and every other file not counted below."`
	Docs      LineCounts `json:"docs" doc:"Lines in prose files: Markdown, reStructuredText, AsciiDoc, and plain text other than build and dependency files such as requirements.txt."`
	Data      LineCounts `json:"data" doc:"Lines in data files: JSON, JSON Lines, CSV, TSV, SVG, and test snapshots, other than manifests and configuration such as package.json and tsconfig.json."`
	Generated LineCounts `json:"generated" doc:"Lines in generated files and lockfiles, as the diff view marks them."`
}

var docsExtensions = map[string]bool{
	".adoc": true, ".markdown": true, ".md": true, ".mdx": true, ".rst": true, ".txt": true,
}

var dataExtensions = map[string]bool{
	".csv": true, ".json": true, ".jsonl": true, ".ndjson": true, ".snap": true, ".svg": true, ".tsv": true,
}

// codeNames are build, dependency, and configuration files that docs or
// data extensions would otherwise claim.
var codeNames = []string{
	".eslintrc.json", "CMakeLists.txt", "app.json", "biome.json", "composer.json",
	"constraints*.txt", "deno.json", "jsconfig*.json", "package.json",
	"renovate.json", "requirements*.txt", "tsconfig*.json", "vercel.json",
}

func isCodeName(name string) bool {
	for _, pattern := range codeNames {
		if matched, _ := path.Match(pattern, name); matched {
			return true
		}
	}
	return false
}

// CountLineKinds totals files' changed lines by kind. A file the diff view
// marks generated is generated whatever its name.
func CountLineKinds(files []DiffFile) LineKinds {
	var kinds LineKinds
	for _, file := range files {
		counts := &kinds.Code
		extension := strings.ToLower(filepath.Ext(file.Path))
		switch {
		case file.IsGenerated:
			counts = &kinds.Generated
		case isCodeName(path.Base(file.Path)):
			counts = &kinds.Code
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
