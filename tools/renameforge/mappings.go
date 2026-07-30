package main

import (
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
	{Old: "MIDDLEMAN", New: "KENN_FORGE"},
	{Old: "__middleman", New: "__kenn_forge"},
	{Old: "middleman_", New: "forge_"},
}

var (
	upperIdentifier = regexp.MustCompile(`\bMiddleman([A-Z][A-Za-z0-9_]*)`)
	lowerIdentifier = regexp.MustCompile(`\bmiddleman([A-Z][A-Za-z0-9_]*)`)
	packageAlias    = regexp.MustCompile(`\bmiddleman(\.[A-Za-z_])`)
	productName     = regexp.MustCompile(`\bMiddleman\b`)
)

func rewritePath(path string) string {
	return strings.ReplaceAll(path, "middleman", "kenn-forge")
}

func rewriteContent(path, value string) string {
	if isAllowlistedPath(path) {
		return value
	}
	for _, rule := range contentRules {
		value = strings.ReplaceAll(value, rule.Old, rule.New)
	}
	value = upperIdentifier.ReplaceAllString(value, "Forge$1")
	value = lowerIdentifier.ReplaceAllString(value, "forge$1")
	value = packageAlias.ReplaceAllString(value, "forge$1")
	value = productName.ReplaceAllString(value, "Kenn Forge")
	return strings.ReplaceAll(value, "middleman", "kenn-forge")
}

func isAllowlistedPath(path string) bool {
	path = filepath.ToSlash(path)
	if strings.HasPrefix(path, "internal/db/migrations/") {
		return true
	}
	if strings.HasPrefix(path, "tools/renameforge/") {
		return true
	}
	return strings.HasPrefix(path, "docs/superpowers/specs/") ||
		strings.HasPrefix(path, "docs/superpowers/plans/")
}
