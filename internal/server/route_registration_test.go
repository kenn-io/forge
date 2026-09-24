package server

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAPIRoutesUseHumaRegistration(t *testing.T) {
	require := require.New(t)

	paths, err := filepath.Glob("*.go")
	require.NoError(err)

	var matches []string
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		source, err := os.ReadFile(path)
		require.NoError(err)

		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, source, 0)
		require.NoError(err)

		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok || len(call.Args) == 0 {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if selector.Sel.Name != "Handle" &&
				selector.Sel.Name != "HandleFunc" {
				return true
			}
			ident, ok := selector.X.(*ast.Ident)
			if !ok || ident.Name != "mux" {
				return true
			}
			lit, ok := call.Args[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			route, err := strconv.Unquote(lit.Value)
			require.NoError(err)
			if strings.Contains(route, "/api/") {
				matches = append(matches, path+":"+route)
			}
			return true
		})
	}
	require.Empty(
		matches,
		"raw API routes should be registered through Huma: "+
			strings.Join(matches, ", "),
	)
}
