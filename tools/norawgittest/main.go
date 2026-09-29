// Command norawgittest rejects test code that runs Git outside the isolated
// test fixtures. Raw Git in a test inherits the developer's configuration,
// hooks, and enclosing checkout, so a mistaken fixture can rewrite the
// repository the tests run from. Tests must use internal/testutil/gitfixture
// or the runners from internal/testutil/gitsafe.
package main

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"go.kenn.io/forge/internal/procutil"
)

const (
	rawGitMessage = "tests must not run git directly; use internal/testutil/gitfixture " +
		"(Run, NewRepository) or gitsafe.Runner(), which isolate Git from the developer's " +
		"config and checkout (context/testing-basics.md)"
	realGitMessage = "tests must not read GIT_SAFE_REAL_GIT; it bypasses the host Git " +
		"guard. Use gitsafe.Runner() or internal/testutil/gitfixture"
	runnerMessage = "tests must not build a gitcmd.Runner; use gitsafe.Runner(), " +
		"gitsafe.MutableRunner(t) when the test changes global Git config, or " +
		"gitsafe.UserConfigRunner(t, home, xdg) when it tests default config discovery"
	isolatedMainMessage = "test packages that use gitsafe or gitfixture must call " +
		"gitsafe.RunIsolatedMain from TestMain; without it the runners inherit the " +
		"developer's HOME and Git config"
)

const (
	gitsafeImport    = "go.kenn.io/forge/internal/testutil/gitsafe"
	gitfixtureImport = "go.kenn.io/forge/internal/testutil/gitfixture"
)

const (
	execImport     = "os/exec"
	procutilImport = "go.kenn.io/forge/internal/procutil"
	gitcmdImport   = "go.kenn.io/kit/git/cmd"
)

// Packages that implement the isolation itself.
var allowedDirs = []string{
	"internal/testutil/gitsafe",
	"internal/testutil/gitfixture",
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		args = []string{"."}
	}

	files, err := goFiles(args)
	if err != nil {
		fmt.Fprintf(stderr, "norawgittest: %v\n", err)
		return 1
	}

	sources := make(map[string]string, len(files))
	for _, file := range files {
		src, err := os.ReadFile(file)
		if err != nil {
			fmt.Fprintf(stderr, "norawgittest: %v\n", err)
			return 1
		}
		sources[file] = string(src)
	}
	diagnostics, err := checkSources(sources)
	if err != nil {
		fmt.Fprintf(stderr, "norawgittest: %v\n", err)
		return 1
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

// checkSources reports raw Git use per file, then requires every test
// package that uses the Git fixtures to install their isolation in TestMain.
func checkSources(sources map[string]string) ([]Diagnostic, error) {
	type testPackage struct {
		fixtureUse   *Diagnostic
		isolatedMain bool
	}
	packages := map[string]*testPackage{}
	var diagnostics []Diagnostic
	for filePath, src := range sources {
		fileDiagnostics, facts, err := checkFile(filePath, src)
		if err != nil {
			return nil, err
		}
		diagnostics = append(diagnostics, fileDiagnostics...)
		if !strings.HasSuffix(filePath, "_test.go") || !inScope(filePath) {
			continue
		}
		dir := path.Dir(filepath.ToSlash(filePath))
		pkg := packages[dir]
		if pkg == nil {
			pkg = &testPackage{}
			packages[dir] = pkg
		}
		pkg.isolatedMain = pkg.isolatedMain || facts.callsRunIsolatedMain
		if facts.fixtureImport != nil &&
			(pkg.fixtureUse == nil || facts.fixtureImport.Compare(*pkg.fixtureUse) < 0) {
			pkg.fixtureUse = facts.fixtureImport
		}
	}
	for _, pkg := range packages {
		if pkg.fixtureUse != nil && !pkg.isolatedMain {
			diagnostics = append(diagnostics, *pkg.fixtureUse)
		}
	}
	return diagnostics, nil
}

// checkSource reports raw Git use in a single file.
func checkSource(path, src string) ([]Diagnostic, error) {
	diagnostics, _, err := checkFile(path, src)
	return diagnostics, err
}

type fileFacts struct {
	fixtureImport        *Diagnostic
	callsRunIsolatedMain bool
}

// checkFile reports raw Git use in test code. path decides scope: test
// files anywhere, and every file of an internal/testutil helper package.
func checkFile(path, src string) ([]Diagnostic, fileFacts, error) {
	var facts fileFacts
	if !inScope(path) {
		return nil, facts, nil
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, 0)
	if err != nil {
		return nil, facts, err
	}

	imports := importNames(file)
	constants := stringConstants(file)

	var diagnostics []Diagnostic
	report := func(pos token.Pos, message string) {
		position := fset.Position(pos)
		diagnostics = append(diagnostics, Diagnostic{
			Path: path, Line: position.Line, Column: position.Column, Message: message,
		})
	}

	for _, spec := range file.Imports {
		importPath, err := strconv.Unquote(spec.Path.Value)
		if err == nil && (importPath == gitsafeImport || importPath == gitfixtureImport) {
			position := fset.Position(spec.Pos())
			facts.fixtureImport = &Diagnostic{
				Path: path, Line: position.Line, Column: position.Column,
				Message: isolatedMainMessage,
			}
			break
		}
	}

	ast.Inspect(file, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.CallExpr:
			if programArg, ok := commandProgramArg(node, imports); ok &&
				isGitProgram(programArg, constants) {
				report(node.Pos(), rawGitMessage)
			}
			if isImportedFunc(node.Fun, imports[gitcmdImport], "New") {
				report(node.Pos(), runnerMessage)
			}
			if isImportedFunc(node.Fun, imports[gitsafeImport], "RunIsolatedMain") {
				facts.callsRunIsolatedMain = true
			}
		case *ast.BasicLit:
			if node.Kind == token.STRING {
				if value, err := strconv.Unquote(node.Value); err == nil && value == "GIT_SAFE_REAL_GIT" {
					report(node.Pos(), realGitMessage)
				}
			}
		case *ast.CompositeLit:
			if isImportedType(node.Type, imports[gitcmdImport], "Runner") {
				report(node.Pos(), runnerMessage)
			}
		case *ast.ValueSpec:
			if node.Type != nil && len(node.Values) == 0 &&
				isImportedType(node.Type, imports[gitcmdImport], "Runner") {
				report(node.Pos(), runnerMessage)
			}
		}
		return true
	})
	return diagnostics, facts, nil
}

func isImportedFunc(expr ast.Expr, pkgNames []string, funcName string) bool {
	selector, ok := expr.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != funcName {
		return false
	}
	pkg, ok := selector.X.(*ast.Ident)
	return ok && slices.Contains(pkgNames, pkg.Name)
}

func inScope(filePath string) bool {
	slashPath := filepath.ToSlash(filePath)
	dir := path.Dir(slashPath)
	for _, allowed := range allowedDirs {
		if dir == allowed || strings.HasSuffix(dir, "/"+allowed) {
			return false
		}
	}
	if strings.HasSuffix(slashPath, "_test.go") {
		return true
	}
	return dir == "internal/testutil" || strings.HasPrefix(dir, "internal/testutil/") ||
		strings.Contains(dir, "/internal/testutil")
}

// importNames maps an import path to the local names it is visible under.
func importNames(file *ast.File) map[string][]string {
	names := map[string][]string{}
	for _, spec := range file.Imports {
		importPath, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			continue
		}
		name := path.Base(importPath)
		if importPath == gitcmdImport {
			name = "cmd"
		}
		if spec.Name != nil {
			name = spec.Name.Name
		}
		if name == "_" || name == "." {
			continue
		}
		names[importPath] = append(names[importPath], name)
	}
	return names
}

// stringConstants records file-level constants and variables initialized
// to a single string literal, so `const gitBin = "git"` is still caught.
func stringConstants(file *ast.File) map[string]string {
	values := map[string]string{}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || (gen.Tok != token.CONST && gen.Tok != token.VAR) {
			continue
		}
		for _, spec := range gen.Specs {
			valueSpec, ok := spec.(*ast.ValueSpec)
			if !ok || len(valueSpec.Names) != len(valueSpec.Values) {
				continue
			}
			for i, name := range valueSpec.Names {
				if value, ok := stringLiteral(valueSpec.Values[i]); ok {
					values[name.Name] = value
				}
			}
		}
	}
	return values
}

// commandProgramArg returns the program argument of an exec or procutil
// command constructor.
func commandProgramArg(call *ast.CallExpr, imports map[string][]string) (ast.Expr, bool) {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return nil, false
	}
	pkg, ok := selector.X.(*ast.Ident)
	if !ok {
		return nil, false
	}
	if !slices.Contains(imports[execImport], pkg.Name) &&
		!slices.Contains(imports[procutilImport], pkg.Name) {
		return nil, false
	}
	index := -1
	switch selector.Sel.Name {
	case "Command":
		index = 0
	case "CommandContext":
		index = 1
	}
	if index < 0 || len(call.Args) <= index {
		return nil, false
	}
	return call.Args[index], true
}

func isGitProgram(expr ast.Expr, constants map[string]string) bool {
	value, ok := stringLiteral(expr)
	if !ok {
		ident, isIdent := expr.(*ast.Ident)
		if !isIdent {
			return false
		}
		value, ok = constants[ident.Name]
		if !ok {
			return false
		}
	}
	base := path.Base(filepath.ToSlash(value))
	return base == "git" || base == "git.exe"
}

func stringLiteral(expr ast.Expr) (string, bool) {
	lit, ok := expr.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	value, err := strconv.Unquote(lit.Value)
	return value, err == nil
}

func isImportedType(expr ast.Expr, pkgNames []string, typeName string) bool {
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	selector, ok := expr.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != typeName {
		return false
	}
	pkg, ok := selector.X.(*ast.Ident)
	return ok && slices.Contains(pkgNames, pkg.Name)
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
	Dir            string
	GoFiles        []string
	CgoFiles       []string
	TestGoFiles    []string
	XTestGoFiles   []string
	IgnoredGoFiles []string
	Error          *listedPackageError
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

	wd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	dec := jsontext.NewDecoder(bytes.NewReader(out))
	var files []string
	for dec.PeekKind() != 0 {
		var pkg listedPackage
		if err := json.UnmarshalDecode(dec, &pkg); err != nil {
			return nil, err
		}
		if pkg.Error != nil {
			return nil, errors.New(pkg.Error.Err)
		}
		// The isolation rule also covers tests and helpers excluded by build tags
		// or the current platform.
		names := slices.Concat(pkg.GoFiles, pkg.CgoFiles, pkg.TestGoFiles, pkg.XTestGoFiles, pkg.IgnoredGoFiles)
		for _, name := range names {
			full := filepath.Join(pkg.Dir, name)
			// Report module-relative paths so scope matching and output
			// do not depend on where the checkout lives.
			if rel, err := filepath.Rel(wd, full); err == nil && !strings.HasPrefix(rel, "..") {
				full = rel
			}
			files = append(files, full)
		}
	}
	return files, nil
}
