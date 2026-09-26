package main

import (
	"fmt"
	"go/ast"
	"go/constant"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const diagnosticMessage = "wait on the in-process signal or synctest, or add a reviewed allowance in tools/timingbudgetcheck"

var allowedBudgets = map[budgetKey]budgetAllowance{
	{
		path:      "internal/github/sync_test.go",
		function:  "TestTerminalStatusPublicationKeepsRunSlotUntilOrdered",
		assertion: "github.com/stretchr/testify/require.Never",
		budget:    100 * time.Millisecond,
	}: {
		count:  1,
		reason: "Real syncer over an openTestDB SQLite fixture. The test holds syncer.statusMu across terminal publication, and mutex waits are not durably blocking in a synctest bubble.",
	},
	{
		path:      "internal/server/api_test.go",
		function:  "TestAPIEnqueuePRSyncQueuesOneRerun",
		assertion: "github.com/stretchr/testify/assert.Never",
		budget:    100 * time.Millisecond,
	}: {
		count:  1,
		reason: "Rerun enqueued through the generated HTTP client against a SQLite-backed server. Retained by #1110.",
	},
	{
		path:      "internal/server/federation_events_test.go",
		function:  "TestNodeStreamsHubEventsWithNodeLocalCursorIDs",
		assertion: "github.com/stretchr/testify/assert.Never",
		budget:    50 * time.Millisecond,
	}: {
		count:  1,
		reason: "Hub events over an httptest TLS federation stream to a disabled spoke. Retained by #1112.",
	},
	{
		path:      "internal/server/repobrowserapi/handler_test.go",
		function:  "TestRepoBrowserStartupRefreshHonorsDisabledBackgroundMonitors",
		assertion: "github.com/stretchr/testify/assert.Never",
		budget:    250 * time.Millisecond,
	}: {
		count:  1,
		reason: "Real Git clone ref resolution after an upstream push, with background monitors disabled.",
	},
	{
		path:      "internal/server/runtime_launch_rollback_test.go",
		function:  "TestCommandSameKeyPersistenceOwnership",
		assertion: "github.com/stretchr/testify/require.Never",
		budget:    150 * time.Millisecond,
	}: {
		count:  1,
		reason: "SQLite writer-pool WaitCount while a fake-tmux subprocess launch on a PTY holds rollback authority.",
	},
	{
		path:      "internal/server/runtime_launch_rollback_test.go",
		function:  "TestProjectWorktreeShellPersistenceOwnership",
		assertion: "github.com/stretchr/testify/require.Never",
		budget:    150 * time.Millisecond,
	}: {
		count:  1,
		reason: "Same owner, for the project worktree shell route.",
	},
	{
		path:      "internal/workspace/localruntime/command_session_test.go",
		function:  "TestEnsureCommandSessionAndPersistSerializesSameKeyOwnership",
		assertion: "github.com/stretchr/testify/require.Never",
		budget:    100 * time.Millisecond,
	}: {
		count:  1,
		reason: "Fake-tmux subprocess on a PTY. The follower must not reach persistence while the creator holds the keyed start lock.",
	},
}

var testifyNames = map[string]struct{}{
	"Eventually":       {},
	"EventuallyWithT":  {},
	"Eventuallyf":      {},
	"EventuallyWithTf": {},
	"Never":            {},
	"Neverf":           {},
}

type budgetKey struct {
	path      string
	function  string
	assertion string
	budget    time.Duration
}

type budgetAllowance struct {
	count  int
	reason string
}

type diagnostic struct {
	path      string
	line      int
	column    int
	assertion string
	budget    time.Duration
}

type stubPackages struct {
	time    *types.Package
	assert  *types.Package
	require *types.Package
}

type stubImporter struct {
	packages map[string]*types.Package
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) > 1 {
		fmt.Fprintln(stderr, "usage: timingbudgetcheck [directory]")
		return 1
	}

	root := "."
	if len(args) == 1 {
		root = args[0]
	}
	rootInfo, err := os.Stat(root)
	if err != nil {
		fmt.Fprintf(stderr, "timingbudgetcheck: %v\n", err)
		return 1
	}
	if !rootInfo.IsDir() {
		fmt.Fprintf(stderr, "timingbudgetcheck: %s is not a directory\n", root)
		return 1
	}

	absRoot, err := filepath.Abs(root)
	if err != nil {
		fmt.Fprintf(stderr, "timingbudgetcheck: %v\n", err)
		return 1
	}
	stubs, err := buildStubs()
	if err != nil {
		fmt.Fprintf(stderr, "timingbudgetcheck: %v\n", err)
		return 1
	}

	seen := make(map[budgetKey]int)
	var diagnostics []diagnostic
	walkErr := filepath.WalkDir(absRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if filepath.Clean(path) != filepath.Clean(absRoot) && skippedDirectory(entry.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), "_test.go") {
			return nil
		}

		src, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}

		moduleRoot := findModuleRoot(filepath.Dir(path), absRoot)
		relPath, err := filepath.Rel(moduleRoot, path)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		fileDiagnostics := checkFile(
			fset,
			file,
			filepath.ToSlash(relPath),
			stubs,
			seen,
		)
		diagnostics = append(diagnostics, fileDiagnostics...)
		return nil
	})
	if walkErr != nil {
		fmt.Fprintf(stderr, "timingbudgetcheck: %v\n", walkErr)
		return 1
	}

	for _, item := range diagnostics {
		fmt.Fprintf(
			stdout,
			"%s:%d:%d: %s budget %s is below 1s; %s\n",
			item.path,
			item.line,
			item.column,
			item.assertion,
			item.budget.String(),
			diagnosticMessage,
		)
	}
	if len(diagnostics) > 0 {
		return 1
	}
	return 0
}

func skippedDirectory(name string) bool {
	return name == "vendor" || name == "node_modules" || name == "testdata" ||
		strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")
}

func findModuleRoot(start, fallback string) string {
	dir := filepath.Clean(start)
	for {
		if info, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil && !info.IsDir() {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return fallback
		}
		dir = parent
	}
}

func checkFile(
	fset *token.FileSet,
	file *ast.File,
	path string,
	stubs *stubPackages,
	seen map[budgetKey]int,
) []diagnostic {
	info := &types.Info{
		Types:      make(map[ast.Expr]types.TypeAndValue),
		Uses:       make(map[*ast.Ident]types.Object),
		Selections: make(map[*ast.SelectorExpr]*types.Selection),
	}
	config := types.Config{
		Importer: &stubImporter{packages: map[string]*types.Package{
			"time":                                stubs.time,
			"github.com/stretchr/testify/assert":  stubs.assert,
			"github.com/stretchr/testify/require": stubs.require,
		}},
		Error:       func(error) {},
		FakeImportC: true,
	}
	_, _ = config.Check(path, fset, []*ast.File{file}, info)

	var diagnostics []diagnostic
	checkDecl := func(decl ast.Node, function string) {
		ast.Inspect(decl, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			obj, signature := resolvedCallee(call.Fun, info)
			if obj == nil || signature == nil || !isTestifyFunction(obj) {
				return true
			}
			if _, ok := testifyNames[obj.Name()]; !ok {
				return true
			}
			waitFor := waitForIndex(signature)
			if waitFor < 0 || waitFor >= len(call.Args) {
				return true
			}
			budget, ok := literalBudget(call.Args[waitFor], info, stubs)
			if !ok || budget >= time.Second {
				return true
			}

			assertion := obj.Pkg().Path() + "." + obj.Name()
			key := budgetKey{
				path:      path,
				function:  function,
				assertion: assertion,
				budget:    budget,
			}
			seen[key]++
			if allowance, ok := allowedBudgets[key]; ok && seen[key] <= allowance.count {
				return true
			}

			position := fset.Position(call.Pos())
			diagnostics = append(diagnostics, diagnostic{
				path:      path,
				line:      position.Line,
				column:    position.Column,
				assertion: assertion,
				budget:    budget,
			})
			return true
		})
	}

	for _, decl := range file.Decls {
		if function, ok := decl.(*ast.FuncDecl); ok {
			if function.Body != nil {
				checkDecl(function.Body, function.Name.Name)
			}
			continue
		}
		checkDecl(decl, "")
	}
	return diagnostics
}

func isTestifyFunction(obj *types.Func) bool {
	if obj.Pkg() == nil {
		return false
	}
	path := obj.Pkg().Path()
	return path == "github.com/stretchr/testify/assert" ||
		path == "github.com/stretchr/testify/require"
}

func resolvedCallee(expr ast.Expr, info *types.Info) (*types.Func, *types.Signature) {
	expr = unparen(expr)
	var object types.Object
	switch node := expr.(type) {
	case *ast.Ident:
		object = info.Uses[node]
	case *ast.SelectorExpr:
		if selection := info.Selections[node]; selection != nil {
			object = selection.Obj()
		} else {
			object = info.Uses[node.Sel]
		}
	}
	function, ok := object.(*types.Func)
	if !ok {
		return nil, nil
	}
	signature, _ := info.TypeOf(expr).(*types.Signature)
	if signature == nil {
		signature, _ = function.Type().(*types.Signature)
	}
	return function, signature
}

func waitForIndex(signature *types.Signature) int {
	if params := signature.Params(); params != nil {
		for i := range params.Len() {
			if params.At(i).Name() == "waitFor" {
				return i
			}
		}
	}
	return -1
}

func literalBudget(expr ast.Expr, info *types.Info, stubs *stubPackages) (time.Duration, bool) {
	if !literalExpression(expr, info, stubs) {
		return 0, false
	}
	typeAndValue, ok := info.Types[expr]
	if !ok || typeAndValue.Value == nil {
		typeAndValue, ok = info.Types[unparen(expr)]
	}
	if !ok || typeAndValue.Value == nil {
		return 0, false
	}
	integer := constant.ToInt(typeAndValue.Value)
	if integer == nil {
		return 0, false
	}
	value, ok := constant.Int64Val(integer)
	if !ok {
		return 0, false
	}
	return time.Duration(value), true
}

func literalExpression(expr ast.Expr, info *types.Info, stubs *stubPackages) bool {
	switch node := expr.(type) {
	case *ast.BasicLit:
		return node.Kind == token.INT || node.Kind == token.FLOAT
	case *ast.ParenExpr:
		return literalExpression(node.X, info, stubs)
	case *ast.UnaryExpr:
		switch node.Op {
		case token.ADD, token.SUB, token.XOR:
			return literalExpression(node.X, info, stubs)
		default:
			return false
		}
	case *ast.BinaryExpr:
		if !literalBinaryOperator(node.Op) {
			return false
		}
		return literalExpression(node.X, info, stubs) && literalExpression(node.Y, info, stubs)
	case *ast.SelectorExpr:
		object, ok := info.Uses[node.Sel].(*types.Const)
		return ok && object.Pkg() == stubs.time
	case *ast.CallExpr:
		return len(node.Args) == 1 && !node.Ellipsis.IsValid() &&
			isDurationConversion(node.Fun, info, stubs) &&
			literalExpression(node.Args[0], info, stubs)
	default:
		return false
	}
}

func literalBinaryOperator(op token.Token) bool {
	switch op {
	case token.ADD, token.SUB, token.MUL, token.QUO, token.REM,
		token.SHL, token.SHR, token.AND, token.OR, token.XOR, token.AND_NOT:
		return true
	default:
		return false
	}
}

func isDurationConversion(expr ast.Expr, info *types.Info, stubs *stubPackages) bool {
	expr = unparen(expr)
	selector, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	object, ok := info.Uses[selector.Sel].(*types.TypeName)
	return ok && object.Pkg() == stubs.time && object.Name() == "Duration"
}

func unparen(expr ast.Expr) ast.Expr {
	for {
		paren, ok := expr.(*ast.ParenExpr)
		if !ok {
			return expr
		}
		expr = paren.X
	}
}

func (i *stubImporter) Import(path string) (*types.Package, error) {
	if pkg, ok := i.packages[path]; ok {
		return pkg, nil
	}
	return nil, fmt.Errorf("unsupported import %q", path)
}

func buildStubs() (*stubPackages, error) {
	importer := &stubImporter{packages: make(map[string]*types.Package)}
	timePackage, err := checkStubPackage("time", timeStubSource, importer)
	if err != nil {
		return nil, err
	}
	importer.packages["time"] = timePackage

	assertPackage, err := checkStubPackage(
		"github.com/stretchr/testify/assert",
		assertStubSource,
		importer,
	)
	if err != nil {
		return nil, err
	}
	importer.packages["github.com/stretchr/testify/assert"] = assertPackage

	requirePackage, err := checkStubPackage(
		"github.com/stretchr/testify/require",
		requireStubSource,
		importer,
	)
	if err != nil {
		return nil, err
	}

	return &stubPackages{
		time:    timePackage,
		assert:  assertPackage,
		require: requirePackage,
	}, nil
}

func checkStubPackage(path, source string, importer types.Importer) (*types.Package, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path+".go", source, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	config := types.Config{Importer: importer}
	return config.Check(path, fset, []*ast.File{file}, nil)
}

const timeStubSource = `package time

type Duration int64

const (
	Nanosecond  Duration = 1
	Microsecond          = 1000 * Nanosecond
	Millisecond          = 1000 * Microsecond
	Second               = 1000 * Millisecond
	Minute               = 60 * Second
	Hour                 = 60 * Minute
)
`

const assertStubSource = `package assert

import "time"

type TestingT interface{}
type CollectT struct{}
type Assertions struct{}

func New(t TestingT) *Assertions { return &Assertions{} }

func Eventually(t TestingT, condition func() bool, waitFor time.Duration, tick time.Duration, msgAndArgs ...interface{}) bool {
	return false
}
func EventuallyWithT(t TestingT, condition func(collect *CollectT), waitFor time.Duration, tick time.Duration, msgAndArgs ...interface{}) bool {
	return false
}
func Eventuallyf(t TestingT, condition func() bool, waitFor time.Duration, tick time.Duration, msg string, args ...interface{}) bool {
	return false
}
func EventuallyWithTf(t TestingT, condition func(collect *CollectT), waitFor time.Duration, tick time.Duration, msg string, args ...interface{}) bool {
	return false
}
func Never(t TestingT, condition func() bool, waitFor time.Duration, tick time.Duration, msgAndArgs ...interface{}) bool {
	return false
}
func Neverf(t TestingT, condition func() bool, waitFor time.Duration, tick time.Duration, msg string, args ...interface{}) bool {
	return false
}

func (a *Assertions) Eventually(condition func() bool, waitFor time.Duration, tick time.Duration, msgAndArgs ...interface{}) bool {
	return false
}
func (a *Assertions) EventuallyWithT(condition func(collect *CollectT), waitFor time.Duration, tick time.Duration, msgAndArgs ...interface{}) bool {
	return false
}
func (a *Assertions) Eventuallyf(condition func() bool, waitFor time.Duration, tick time.Duration, msg string, args ...interface{}) bool {
	return false
}
func (a *Assertions) EventuallyWithTf(condition func(collect *CollectT), waitFor time.Duration, tick time.Duration, msg string, args ...interface{}) bool {
	return false
}
func (a *Assertions) Never(condition func() bool, waitFor time.Duration, tick time.Duration, msgAndArgs ...interface{}) bool {
	return false
}
func (a *Assertions) Neverf(condition func() bool, waitFor time.Duration, tick time.Duration, msg string, args ...interface{}) bool {
	return false
}
`

const requireStubSource = `package require

import (
	"time"
	assert "github.com/stretchr/testify/assert"
)

type TestingT interface{}
type Assertions struct{}

func New(t TestingT) *Assertions { return &Assertions{} }

func Eventually(t TestingT, condition func() bool, waitFor time.Duration, tick time.Duration, msgAndArgs ...interface{}) {}
func EventuallyWithT(t TestingT, condition func(collect *assert.CollectT), waitFor time.Duration, tick time.Duration, msgAndArgs ...interface{}) {}
func Eventuallyf(t TestingT, condition func() bool, waitFor time.Duration, tick time.Duration, msg string, args ...interface{}) {}
func EventuallyWithTf(t TestingT, condition func(collect *assert.CollectT), waitFor time.Duration, tick time.Duration, msg string, args ...interface{}) {}
func Never(t TestingT, condition func() bool, waitFor time.Duration, tick time.Duration, msgAndArgs ...interface{}) {}
func Neverf(t TestingT, condition func() bool, waitFor time.Duration, tick time.Duration, msg string, args ...interface{}) {}

func (a *Assertions) Eventually(condition func() bool, waitFor time.Duration, tick time.Duration, msgAndArgs ...interface{}) {}
func (a *Assertions) EventuallyWithT(condition func(collect *assert.CollectT), waitFor time.Duration, tick time.Duration, msgAndArgs ...interface{}) {}
func (a *Assertions) Eventuallyf(condition func() bool, waitFor time.Duration, tick time.Duration, msg string, args ...interface{}) {}
func (a *Assertions) EventuallyWithTf(condition func(collect *assert.CollectT), waitFor time.Duration, tick time.Duration, msg string, args ...interface{}) {}
func (a *Assertions) Never(condition func() bool, waitFor time.Duration, tick time.Duration, msgAndArgs ...interface{}) {}
func (a *Assertions) Neverf(condition func() bool, waitFor time.Duration, tick time.Duration, msg string, args ...interface{}) {}
`
