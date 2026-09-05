package gitclone

import (
	"bytes"
	"go.kenn.io/forge/landedwork"
)

// GeneratedAttributeInput returns NUL-delimited paths for git check-attr.
func GeneratedAttributeInput(files []DiffFile) []byte {
	var input bytes.Buffer
	seen := make(map[string]bool, len(files))
	for _, file := range files {
		path := file.Path
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		input.WriteString(path)
		input.WriteByte(0)
	}
	return input.Bytes()
}

// ParseLinguistGeneratedAttributes parses `git check-attr -z` triples.
func ParseLinguistGeneratedAttributes(out []byte) map[string]bool {
	parts := bytes.Split(out, []byte{0})
	generated := make(map[string]bool)
	for i := 0; i+2 < len(parts); i += 3 {
		path := string(parts[i])
		attr := string(parts[i+1])
		value := string(parts[i+2])
		if path == "" || attr != "linguist-generated" {
			continue
		}
		if value == "unspecified" {
			continue
		}
		generated[path] = value == "set" || value == "true"
	}
	return generated
}

// MarkGeneratedFiles applies Linguist metadata and local generated heuristics.
func MarkGeneratedFiles(files []DiffFile, linguistGenerated map[string]bool) {
	for i := range files {
		if generated, ok := linguistGenerated[files[i].Path]; ok {
			files[i].IsGenerated = generated
			continue
		}
		files[i].IsGenerated = landedwork.IsGeneratedPath(files[i].Path)
	}
}
