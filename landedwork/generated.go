package landedwork

import (
	"path"
	"strings"
)

var generatedBasenames = map[string]bool{
	"bun.lock":            true,
	"bun.lockb":           true,
	"cargo.lock":          true,
	"composer.lock":       true,
	"deno.lock":           true,
	"flake.lock":          true,
	"gemfile.lock":        true,
	"go.sum":              true,
	"gradle.lockfile":     true,
	"mix.lock":            true,
	"npm-shrinkwrap.json": true,
	"package-lock.json":   true,
	"pipfile.lock":        true,
	"pnpm-lock.yaml":      true,
	"poetry.lock":         true,
	"pubspec.lock":        true,
	".terraform.lock.hcl": true,
	"terraform.lock.hcl":  true,
	"uv.lock":             true,
	"yarn.lock":           true,
}

var generatedSuffixes = []string{
	".lock",
	".lock.json",
	".lock.yaml",
	".lock.yml",
}

// IsGeneratedPath recognizes generated artifacts that are useful even without
// repository-specific Linguist attributes.
func IsGeneratedPath(name string) bool {
	value := []byte(path.Base(name))
	for i, ch := range value {
		if ch >= 'A' && ch <= 'Z' {
			value[i] = ch + ('a' - 'A')
		}
	}
	base := string(value)
	if generatedBasenames[base] {
		return true
	}
	for _, suffix := range generatedSuffixes {
		if strings.HasSuffix(base, suffix) {
			return true
		}
	}
	return false
}
