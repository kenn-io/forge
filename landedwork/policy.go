package landedwork

import (
	"bytes"
	"errors"
	"path"
	"slices"
	"strings"
)

const CodePolicy = "forge-code/1"

var codeSuffixes = strings.Fields(`.go .rs .py .pyi .js .jsx .mjs .cjs .ts .tsx .mts .cts .java .kt .kts .c .h .cc .cpp .cxx .hh .hpp .cs .fs .fsx .swift .m .mm .rb .php .sh .bash .zsh .fish .ps1 .sql .proto .r .jl .lua .pl .pm .ex .exs .erl .hrl .hs .scala .sc .clj .cljs .cljc .dart .vue .svelte .html .htm .css .scss .sass .less .cmake .nix .tf .hcl .ml .mli .zig .gradle .groovy`)

// ClassifyCodePath applies forge-code/1. Paths remain Git bytes; only suffix
// classification folds ASCII case. An explicit generated attribute overrides
// the filename heuristic, never the vendor or code-inclusion policy.
func ClassifyCodePath(raw []byte, generated *bool) string {
	name := string(raw)
	for component := range strings.SplitSeq(name, "/") {
		if component == "vendor" || component == "node_modules" || component == "third_party" {
			return "vendor"
		}
	}
	isGenerated := IsGeneratedPath(name)
	if generated != nil {
		isGenerated = *generated
	}
	if isGenerated {
		return "generated"
	}
	switch path.Base(name) {
	case "Makefile", "GNUmakefile", "Dockerfile", "CMakeLists.txt":
		return "included"
	}
	suffix := []byte(path.Ext(name))
	for i, b := range suffix {
		if b >= 'A' && b <= 'Z' {
			suffix[i] = b + ('a' - 'A')
		}
	}
	if slices.Contains(codeSuffixes, string(suffix)) {
		return "included"
	}
	return "not_code"
}

// ParseGeneratedAttributes validates check-attr -z triples against the exact
// requested path set. Missing, duplicate, foreign and malformed triples fail;
// unspecified is observed successfully but leaves the filename heuristic active.
func ParseGeneratedAttributes(paths []string, output []byte) (map[string]bool, error) {
	invalid := errors.New("generated_attributes_unavailable")
	expected := make(map[string]bool, len(paths))
	for _, name := range paths {
		if name == "" || strings.ContainsRune(name, 0) {
			return nil, invalid
		}
		expected[name] = false
	}
	attributes := make(map[string]bool)
	if len(expected) == 0 && len(output) == 0 {
		return attributes, nil
	}
	parts := bytes.Split(output, []byte{0})
	if len(parts)%3 != 1 || len(parts[len(parts)-1]) != 0 {
		return nil, invalid
	}
	for i := 0; i < len(parts)-1; i += 3 {
		name := string(parts[i])
		seen, ok := expected[name]
		if !ok || seen || string(parts[i+1]) != "linguist-generated" {
			return nil, invalid
		}
		expected[name] = true
		switch string(parts[i+2]) {
		case "set", "true":
			attributes[name] = true
		case "unset", "false":
			attributes[name] = false
		case "unspecified":
		default:
			return nil, invalid
		}
	}
	for _, seen := range expected {
		if !seen {
			return nil, invalid
		}
	}
	return attributes, nil
}
