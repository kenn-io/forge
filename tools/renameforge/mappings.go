package main

import (
	"go/scanner"
	"go/token"
	"path/filepath"
	"regexp"
	"strings"
)

type Rule struct {
	Old string
	New string
}

var contentRules = []Rule{
	{Old: "go.kenn.io/middleman", New: "go.kenn.io/forge"},
	{Old: "@middleman/github-app-ui", New: "@kenn-forge/github-app-ui"},
	{Old: "@middleman/ui", New: "@kenn-forge/ui"},
	{Old: "middleman-github-app", New: "kenn-forge-github-app"},
	{Old: "middleman-openapi", New: "kenn-forge-openapi"},
	{Old: "middle*", New: "kenn-*"},
	{Old: "X-Middleman-", New: "X-Kenn-Forge-"},
	{Old: "X-Kenn Forge-", New: "X-Kenn-Forge-"},
	{Old: "XMiddleman", New: "XKennForge"},
	{Old: "XForgeKataDaemon", New: "XKennForgeKataDaemon"},
	{Old: "XForgeRuntimeSessionKey", New: "XKennForgeRuntimeSessionKey"},
	{Old: "cb190ac507b2b0a4", New: "5e73279a62064378"},
	{Old: "forge.lock", New: "kenn-forge.lock"},
	{Old: "forge.run.json", New: "kenn-forge.run.json"},
	{Old: ".forge.run.json.tmp", New: ".kenn-forge.run.json.tmp"},
	{Old: "kenn-kenn-forge", New: "kenn-forge"},
	{Old: ".config/kenn-forge", New: ".kenn/forge"},
	{Old: "MIDDLEMAN", New: "KENN_FORGE"},
	{Old: "__middleman", New: "__kenn_forge"},
	{Old: "middleman_", New: "forge_"},
	{Old: `name = "https://github.com/wesm/forge.git"`, New: `name = "https://github.com/wesm/kenn-forge.git"`},
}

var (
	upperIdentifier = regexp.MustCompile(`Middleman([A-Z][A-Za-z0-9_]*)`)
	lowerIdentifier = regexp.MustCompile(`middleman([A-Z][A-Za-z0-9_]*)`)
	productName     = regexp.MustCompile(`\bMiddleman\b`)
)

func rewritePath(path string) string {
	return strings.ReplaceAll(path, "middleman", "kenn-forge")
}

func rewriteContent(path, value string) string {
	if strings.HasPrefix(filepath.ToSlash(path), "tools/renameforge/") {
		legacyImport := `"go.kenn.io/` + "middleman/internal/procutil" + `"`
		return strings.ReplaceAll(
			value,
			legacyImport,
			`"go.kenn.io/forge/internal/procutil"`,
		)
	}
	if isAllowlistedPath(path) {
		return value
	}
	for _, rule := range contentRules {
		value = strings.ReplaceAll(value, rule.Old, rule.New)
	}
	value = rewriteGoIdentifiers(path, value)
	value = rewriteJavaScriptIdentifiers(path, value)
	value = upperIdentifier.ReplaceAllString(value, "Forge$1")
	value = lowerIdentifier.ReplaceAllString(value, "forge$1")
	value = productName.ReplaceAllString(value, "Kenn Forge")
	return strings.ReplaceAll(value, "middleman", "kenn-forge")
}

func rewriteJavaScriptIdentifiers(path, value string) string {
	switch filepath.Ext(path) {
	case ".js", ".jsx", ".mjs", ".cjs", ".ts", ".tsx":
	default:
		return value
	}

	var builder strings.Builder
	for index := 0; index < len(value); {
		if value[index] == '\'' || value[index] == '"' || value[index] == '`' {
			next := quotedEnd(value, index, value[index])
			builder.WriteString(value[index:next])
			index = next
			continue
		}
		if strings.HasPrefix(value[index:], "//") {
			next := strings.IndexByte(value[index:], '\n')
			if next < 0 {
				next = len(value)
			} else {
				next += index
			}
			builder.WriteString(value[index:next])
			index = next
			continue
		}
		if strings.HasPrefix(value[index:], "/*") {
			next := strings.Index(value[index+2:], "*/")
			if next < 0 {
				next = len(value)
			} else {
				next += index + 4
			}
			builder.WriteString(value[index:next])
			index = next
			continue
		}
		if value[index] == '/' && javascriptRegexCanStart(value, index) {
			next := javascriptRegexEnd(value, index)
			builder.WriteString(value[index:next])
			index = next
			continue
		}
		if strings.HasPrefix(value[index:], "kenn-forge") &&
			(index == 0 || !isJavaScriptIdentifierPart(value[index-1])) &&
			(index+len("kenn-forge") == len(value) || !isJavaScriptIdentifierPart(value[index+len("kenn-forge")])) {
			builder.WriteString("forge")
			index += len("kenn-forge")
			continue
		}
		if isJavaScriptIdentifierStart(value[index]) {
			next := index + 1
			for next < len(value) && isJavaScriptIdentifierPart(value[next]) {
				next++
			}
			identifier := strings.ReplaceAll(value[index:next], "Middleman", "Forge")
			identifier = strings.ReplaceAll(identifier, "middleman", "forge")
			builder.WriteString(identifier)
			index = next
			continue
		}
		builder.WriteByte(value[index])
		index++
	}
	return builder.String()
}

func quotedEnd(value string, start int, quote byte) int {
	for index := start + 1; index < len(value); index++ {
		if value[index] == '\\' {
			index++
			continue
		}
		if value[index] == quote {
			return index + 1
		}
	}
	return len(value)
}

func javascriptRegexCanStart(value string, index int) bool {
	for index--; index >= 0; index-- {
		switch value[index] {
		case ' ', '\t', '\r', '\n':
			continue
		case '=', '(', ':', ',', '!', '[', '{', ';', '?':
			return true
		default:
			return false
		}
	}
	return true
}

func javascriptRegexEnd(value string, start int) int {
	inClass := false
	for index := start + 1; index < len(value); index++ {
		switch value[index] {
		case '\\':
			index++
		case '[':
			inClass = true
		case ']':
			inClass = false
		case '/':
			if !inClass {
				index++
				for index < len(value) && isJavaScriptIdentifierPart(value[index]) {
					index++
				}
				return index
			}
		case '\n', '\r':
			return index
		}
	}
	return len(value)
}

func isJavaScriptIdentifierStart(char byte) bool {
	return char == '_' || char == '$' || char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z'
}

func isJavaScriptIdentifierPart(char byte) bool {
	return isJavaScriptIdentifierStart(char) || char >= '0' && char <= '9'
}

type sourceReplacement struct {
	start int
	end   int
	value string
}

type sourceToken struct {
	start int
	end   int
	token token.Token
	lit   string
}

func rewriteGoIdentifiers(path, value string) string {
	if filepath.Ext(path) != ".go" {
		return value
	}

	fileSet := token.NewFileSet()
	file := fileSet.AddFile(path, -1, len(value))
	var lexer scanner.Scanner
	lexer.Init(file, []byte(value), nil, scanner.ScanComments)
	var tokens []sourceToken
	for {
		position, tok, literal := lexer.Scan()
		if tok == token.EOF {
			break
		}
		start := file.Offset(position)
		if literal == "" {
			literal = tok.String()
		}
		tokens = append(tokens, sourceToken{
			start: start,
			end:   start + len(literal),
			token: tok,
			lit:   literal,
		})
	}

	var replacements []sourceReplacement
	for index := 0; index < len(tokens); index++ {
		current := tokens[index]
		if index+2 < len(tokens) &&
			current.token == token.IDENT && current.lit == "kenn" &&
			tokens[index+1].token == token.SUB &&
			tokens[index+2].token == token.IDENT && tokens[index+2].lit == "forge" &&
			current.end == tokens[index+1].start && tokens[index+1].end == tokens[index+2].start {
			replacements = append(replacements, sourceReplacement{
				start: current.start,
				end:   tokens[index+2].end,
				value: "forge",
			})
			index += 2
			continue
		}
		if current.token != token.IDENT {
			continue
		}
		replacement := strings.ReplaceAll(current.lit, "Middleman", "Forge")
		replacement = strings.ReplaceAll(replacement, "middleman", "forge")
		if replacement != current.lit {
			replacements = append(replacements, sourceReplacement{
				start: current.start,
				end:   current.end,
				value: replacement,
			})
		}
	}
	if len(replacements) == 0 {
		return value
	}

	var builder strings.Builder
	last := 0
	for _, replacement := range replacements {
		builder.WriteString(value[last:replacement.start])
		builder.WriteString(replacement.value)
		last = replacement.end
	}
	builder.WriteString(value[last:])
	return builder.String()
}

func isAllowlistedPath(path string) bool {
	path = filepath.ToSlash(path)
	if strings.HasPrefix(path, "internal/db/migrations/") {
		return true
	}
	if path == "internal/db/migrations.go" || path == "internal/db/db_test.go" {
		return true
	}
	if path == "internal/config/legacy_migration.go" || path == "internal/config/legacy_migration_test.go" {
		return true
	}
	return strings.HasPrefix(path, "docs/superpowers/specs/") ||
		strings.HasPrefix(path, "docs/superpowers/plans/")
}
