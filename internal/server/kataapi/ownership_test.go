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
	for _, name := range []string{
		"kata_proxy.go",
		"kata_proxy_routes.go",
		"kata_routes.go",
		"kata_task_detail.go",
		"kata_workspace.go",
	} {
		_, err := filepath.Glob(filepath.Join("..", name))
		require.NoError(t, err)
		assert.NoFileExists(t, filepath.Join("..", name),
			"Kata daemon API production remains in the root server package")
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
