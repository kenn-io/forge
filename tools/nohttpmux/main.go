package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/wesm/middleman/internal/procutil"
)

const diagnosticMessage = "non-Huma HTTP route registration is not allowed; register API routes through the Huma route layer"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		args = []string{"."}
	}

	files, err := goFiles(args)
	if err != nil {
		fmt.Fprintf(stderr, "nohttpmux: %v\n", err)
		return 1
	}

	var diagnostics []Diagnostic
	for _, file := range files {
		fileDiagnostics, err := checkFile(file)
		if err != nil {
			fmt.Fprintf(stderr, "nohttpmux: %v\n", err)
			return 1
		}
		diagnostics = append(diagnostics, fileDiagnostics...)
	}

	slices.SortFunc(diagnostics, Diagnostic.Compare)

	for _, diagnostic := range diagnostics {
		fmt.Fprintf(
			stdout, "%s:%d:%d: %s\n",
			diagnostic.Path, diagnostic.Line, diagnostic.Column, diagnostic.Message,
		)
	}
	if len(diagnostics) > 0 {
		return 1
	}
	return 0
}

type Diagnostic struct {
	Path    string
	Line    int
	Column  int
	Message string
}

func (d Diagnostic) Compare(other Diagnostic) int {
	if d.Path != other.Path {
		return strings.Compare(d.Path, other.Path)
	}
	if d.Line != other.Line {
		return d.Line - other.Line
	}
	return d.Column - other.Column
}

func checkFile(path string) ([]Diagnostic, error) {
	if strings.HasSuffix(path, "_test.go") {
		return nil, nil
	}

	src, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return checkSource(path, string(src))
}

func checkSource(path, src string) ([]Diagnostic, error) {
	if strings.HasSuffix(path, "_test.go") {
		return nil, nil
	}

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, 0)
	if err != nil {
		return nil, err
	}

	httpImports := httpImportNames(file)
	if len(httpImports) == 0 {
		return nil, nil
	}

	var diagnostics []Diagnostic
	report := func(pos token.Pos) {
		position := fset.Position(pos)
		diagnostics = append(diagnostics, Diagnostic{
			Path:    path,
			Line:    position.Line,
			Column:  position.Column,
			Message: diagnosticMessage,
		})
	}

	ast.Inspect(file, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			return true
		}

		muxVars := collectServeMuxVars(fn.Body, httpImports)
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if disallowedRegistration(path, call, httpImports, muxVars) {
				report(call.Pos())
			}
			return true
		})
		return false
	})

	return diagnostics, nil
}

func httpImportNames(file *ast.File) map[string]struct{} {
	names := make(map[string]struct{})
	for _, spec := range file.Imports {
		if strings.Trim(spec.Path.Value, `"`) != "net/http" {
			continue
		}
		name := "http"
		if spec.Name != nil {
			name = spec.Name.Name
		}
		names[name] = struct{}{}
	}
	return names
}

func collectServeMuxVars(body *ast.BlockStmt, httpImports map[string]struct{}) map[string]struct{} {
	vars := make(map[string]struct{})
	ast.Inspect(body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.AssignStmt:
			for i, rhs := range node.Rhs {
				if i >= len(node.Lhs) || !isHTTPNewServeMuxCall(rhs, httpImports) {
					continue
				}
				if ident, ok := node.Lhs[i].(*ast.Ident); ok {
					vars[ident.Name] = struct{}{}
				}
			}
		case *ast.ValueSpec:
			for i, value := range node.Values {
				if i >= len(node.Names) || !isHTTPNewServeMuxCall(value, httpImports) {
					continue
				}
				vars[node.Names[i].Name] = struct{}{}
			}
		}
		return true
	})
	return vars
}

func disallowedRegistration(
	path string,
	call *ast.CallExpr,
	httpImports map[string]struct{},
	muxVars map[string]struct{},
) bool {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || selector.Sel == nil || !isRegistrationMethod(selector.Sel.Name) {
		return false
	}

	if allowedRegistration(path, call) {
		return false
	}

	if ident, ok := selector.X.(*ast.Ident); ok {
		if _, imported := httpImports[ident.Name]; imported {
			return true
		}
		if _, tracked := muxVars[ident.Name]; tracked {
			return true
		}
		if isServerApplicationPath(path) && strings.Contains(strings.ToLower(ident.Name), "mux") {
			return true
		}
	}

	return isHTTPDefaultServeMux(selector.X, httpImports)
}

func isRegistrationMethod(name string) bool {
	return name == "Handle" || name == "HandleFunc"
}

func isHTTPNewServeMuxCall(expr ast.Expr, httpImports map[string]struct{}) bool {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return false
	}
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || selector.Sel == nil || selector.Sel.Name != "NewServeMux" {
		return false
	}
	ident, ok := selector.X.(*ast.Ident)
	if !ok {
		return false
	}
	_, ok = httpImports[ident.Name]
	return ok
}

func isHTTPDefaultServeMux(expr ast.Expr, httpImports map[string]struct{}) bool {
	selector, ok := expr.(*ast.SelectorExpr)
	if !ok || selector.Sel == nil || selector.Sel.Name != "DefaultServeMux" {
		return false
	}
	ident, ok := selector.X.(*ast.Ident)
	if !ok {
		return false
	}
	_, ok = httpImports[ident.Name]
	return ok
}

func allowedRegistration(path string, call *ast.CallExpr) bool {
	if !pathHasSuffix(path, "internal/server/server.go") {
		return false
	}
	if len(call.Args) == 0 {
		return false
	}

	switch routePattern(call.Args[0]) {
	case "/", "/healthz", "/livez":
		return true
	case "basePath":
		return true
	default:
		return false
	}
}

func routePattern(expr ast.Expr) string {
	switch node := expr.(type) {
	case *ast.BasicLit:
		if node.Kind != token.STRING {
			return ""
		}
		return strings.Trim(node.Value, `"`)
	case *ast.Ident:
		return node.Name
	default:
		return ""
	}
}

func isServerApplicationPath(path string) bool {
	slashPath := filepath.ToSlash(path)
	return strings.Contains(slashPath, "/internal/server/") ||
		strings.HasPrefix(slashPath, "internal/server/") ||
		strings.Contains(slashPath, "/cmd/middleman/") ||
		strings.HasPrefix(slashPath, "cmd/middleman/")
}

func pathHasSuffix(path, suffix string) bool {
	slashPath := filepath.ToSlash(path)
	slashSuffix := filepath.ToSlash(suffix)
	return slashPath == slashSuffix || strings.HasSuffix(slashPath, "/"+slashSuffix)
}

func goFiles(args []string) ([]string, error) {
	var files []string
	for _, arg := range args {
		if strings.HasSuffix(arg, ".go") {
			files = append(files, arg)
			continue
		}
		pkgFiles, err := packageGoFiles(arg)
		if err != nil {
			return nil, err
		}
		files = append(files, pkgFiles...)
	}
	return files, nil
}

type listedPackage struct {
	Dir      string
	GoFiles  []string
	CgoFiles []string
	Error    *listedPackageError
}

type listedPackageError struct {
	Err string
}

func packageGoFiles(pattern string) ([]string, error) {
	cmd := procutil.Command("go", "list", "-json", pattern)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, errors.New(msg)
	}

	dec := json.NewDecoder(bytes.NewReader(out))
	var files []string
	for dec.More() {
		var pkg listedPackage
		if err := dec.Decode(&pkg); err != nil {
			return nil, err
		}
		if pkg.Error != nil {
			return nil, errors.New(pkg.Error.Err)
		}
		for _, name := range append(pkg.GoFiles, pkg.CgoFiles...) {
			files = append(files, filepath.Join(pkg.Dir, name))
		}
	}
	return files, nil
}
