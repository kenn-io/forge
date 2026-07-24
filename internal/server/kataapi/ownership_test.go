package kataapi

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKataProductionIsOwnedByKataAPI(t *testing.T) {
	rootFiles, err := filepath.Glob(filepath.Join("..", "kata_*.go"))
	require.NoError(t, err)
	for _, path := range rootFiles {
		if !strings.HasSuffix(path, "_test.go") {
			assert.Fail(t, "Kata production file remains in the root server package", path)
		}
	}
}

func TestKataAPIDoesNotImportRootServer(t *testing.T) {
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)

	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, parseErr := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		require.NoError(t, parseErr)
		for _, imported := range file.Imports {
			assert.NotEqual(t, `"go.kenn.io/middleman/internal/server"`, imported.Path.Value, path)
		}
	}
}
