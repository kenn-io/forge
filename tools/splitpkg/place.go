package main

import (
	"errors"
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// visitRefs calls fn for every identifier in n that uses an object of the
// source package, with the object's key.
func (s *splitter) visitRefs(n ast.Node, fn func(id *ast.Ident, obj types.Object, key string)) {
	info := s.pkg.TypesInfo
	ast.Inspect(n, func(n ast.Node) bool {
		id, ok := n.(*ast.Ident)
		if !ok {
			return true
		}
		obj := info.Uses[id]
		if obj == nil || obj.Pkg() == nil || obj.Pkg().Path() != s.pkg.PkgPath {
			return true
		}
		if key := s.keyOf(obj); key != "" {
			fn(id, obj, key)
		}
		return true
	})
}

// isTestKey reports whether key names a test-file declaration, including
// fields and methods of test-file types.
func (s *splitter) isTestKey(key string) bool {
	if d := s.decls[key]; d != nil {
		return d.test
	}
	if kind, rest, _ := strings.Cut(key, ":"); kind == "method" || kind == "field" {
		if d := s.decls["type:"+strings.SplitN(rest, ".", 2)[0]]; d != nil {
			return d.test
		}
	}
	return false
}

func (s *splitter) hubType() string { return "type:" + s.cfg.Hub }

func (s *splitter) isHubField(key string) bool {
	return strings.HasPrefix(key, "field:"+s.cfg.Hub+".")
}

func (s *splitter) isHubMethod(key string) bool {
	return strings.HasPrefix(key, "method:"+s.cfg.Hub+".")
}

// pkgOf is the final package of a production key.
func (s *splitter) pkgOf(key string) string {
	if p, ok := s.prodPkg[key]; ok {
		return p
	}
	// Interface methods and promoted members follow their type.
	if kind, rest, _ := strings.Cut(key, ":"); kind == "method" || kind == "field" {
		if p, ok := s.prodPkg["type:"+strings.SplitN(rest, ".", 2)[0]]; ok {
			return p
		}
	}
	return rootPkg
}

func (s *splitter) place() error {
	s.prodPkg = map[string]string{}
	for _, f := range s.files {
		for _, d := range f.decls {
			if d.test || d.key == "" {
				continue
			}
			p := s.plan[d.unit]
			if d.unit == s.hubType() {
				p = rootPkg
			}
			// Every name in a grouped declaration moves with the group.
			names := []string{d.key}
			if gd, ok := d.node.(*ast.GenDecl); ok {
				names = groupKeys(gd)
				for _, k := range names {
					if q, ok := s.plan[k]; ok && p == rootPkg {
						p = q
					}
				}
			}
			for _, k := range names {
				s.prodPkg[k] = p
			}
			d.homes[p] = true
		}
	}
	// Non-hub struct fields and methods follow their type; keys for fields.
	for _, key := range s.objKey {
		if rest, ok := strings.CutPrefix(key, "field:"); ok {
			s.prodPkg[key] = s.pkgOf("type:" + strings.SplitN(rest, ".", 2)[0])
		}
	}
	s.pinMisusedReceivers()
	s.analyzeHub()
	if err := s.repairImports(); err != nil {
		return err
	}
	s.placeTests()
	if err := s.buildImports(true); err != nil {
		return err
	}
	return s.computeRenames()
}

// pinMisusedReceivers keeps hub methods in the root when they use the
// receiver as a value (passing, returning, comparing it) rather than only to
// reach fields and methods.
func (s *splitter) pinMisusedReceivers() {
	info := s.pkg.TypesInfo
	for _, f := range s.files {
		for _, d := range f.decls {
			if !d.isHub || d.test || s.pkgOf(d.key) == rootPkg {
				continue
			}
			fd := d.node.(*ast.FuncDecl)
			recv := receiverObj(info, fd)
			if recv == nil {
				continue
			}
			selX := map[*ast.Ident]bool{}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				if se, ok := n.(*ast.SelectorExpr); ok {
					if id, ok := se.X.(*ast.Ident); ok {
						selX[id] = true
					}
				}
				return true
			})
			misuse := false
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				if id, ok := n.(*ast.Ident); ok && info.Uses[id] == recv && !selX[id] {
					misuse = true
				}
				return true
			})
			if misuse {
				s.notes = append(s.notes, "kept in root (receiver used as a value): "+d.key)
				s.prodPkg[d.key] = rootPkg
				d.homes = map[string]bool{rootPkg: true}
			}
		}
	}
}

func groupKeys(gd *ast.GenDecl) []string {
	var out []string
	for _, sp := range gd.Specs {
		switch sp := sp.(type) {
		case *ast.TypeSpec:
			out = append(out, "type:"+sp.Name.Name)
		case *ast.ValueSpec:
			kind := "var"
			if gd.Tok == token.CONST {
				kind = "const"
			}
			for _, n := range sp.Names {
				if n.Name != "_" {
					out = append(out, kind+":"+n.Name)
				}
			}
		}
	}
	return out
}

func receiverObj(info *types.Info, fd *ast.FuncDecl) types.Object {
	if fd.Recv == nil || len(fd.Recv.List) == 0 || len(fd.Recv.List[0].Names) == 0 {
		return nil
	}
	return info.Defs[fd.Recv.List[0].Names[0]]
}

// analyzeHub records which hub fields and foreign hub methods each package's
// moved hub methods reach through the receiver, and how each field is shared.
func (s *splitter) analyzeHub() {
	info := s.pkg.TypesInfo
	s.hubFields = map[string]map[string]bool{}
	s.hubCalls = map[string]map[string]bool{}
	for _, f := range s.files {
		for _, d := range f.decls {
			p := s.pkgOf(d.key)
			if !d.isHub || d.test || p == rootPkg {
				continue
			}
			fd := d.node.(*ast.FuncDecl)
			recv := receiverObj(info, fd)
			if recv == nil {
				continue
			}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				se, ok := n.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if id, ok := se.X.(*ast.Ident); !ok || info.Uses[id] != recv {
					return true
				}
				obj := info.Uses[se.Sel]
				if obj == nil {
					return true
				}
				key := s.keyOf(obj)
				switch {
				case s.isHubField(key):
					add(s.hubFields, p, strings.SplitN(key, ".", 2)[1])
				case s.isHubMethod(key) && s.pkgOf(key) != p:
					add(s.hubCalls, p, key)
				}
				return true
			})
		}
	}
	// A hub field is copied only when it has a reference type and nothing
	// assigns it after the hub's composite literal; otherwise every package
	// shares the hub's own field through a pointer.
	s.pointer = map[string]bool{}
	hub := s.pkg.Types.Scope().Lookup(s.cfg.Hub).(*types.TypeName)
	st := hub.Type().Underlying().(*types.Struct)
	for fld := range st.Fields() {
		if !isReferenceType(fld.Type()) {
			s.pointer[fld.Name()] = true
		}
	}
	for _, f := range s.files {
		ast.Inspect(f.syntax, func(n ast.Node) bool {
			var targets []ast.Expr
			switch x := n.(type) {
			case *ast.AssignStmt:
				targets = x.Lhs
			case *ast.IncDecStmt:
				targets = []ast.Expr{x.X}
			case *ast.UnaryExpr:
				if x.Op == token.AND {
					targets = []ast.Expr{x.X}
				}
			}
			for _, t := range targets {
				if se, ok := t.(*ast.SelectorExpr); ok {
					if obj := info.Uses[se.Sel]; obj != nil && s.isHubField(s.keyOf(obj)) {
						s.pointer[obj.Name()] = true
					}
				}
			}
			return true
		})
	}
}

func isReferenceType(t types.Type) bool {
	switch t.Underlying().(type) {
	case *types.Pointer, *types.Map, *types.Chan, *types.Signature, *types.Interface, *types.Slice:
		return true
	}
	return false
}

func add(m map[string]map[string]bool, k, v string) {
	if m[k] == nil {
		m[k] = map[string]bool{}
	}
	m[k][v] = true
}

// hubFieldTypeKeys lists source-package keys a hub field's type mentions.
func (s *splitter) hubFieldTypeKeys() map[string][]string {
	out := map[string][]string{}
	hub := s.decls[s.hubType()]
	if hub == nil {
		return out
	}
	gd := hub.node.(*ast.GenDecl)
	st := gd.Specs[0].(*ast.TypeSpec).Type.(*ast.StructType)
	for _, fl := range st.Fields.List {
		var keys []string
		s.visitRefs(fl.Type, func(_ *ast.Ident, _ types.Object, key string) { keys = append(keys, key) })
		for _, n := range fl.Names {
			out[n.Name] = keys
		}
	}
	return out
}

// importsOf returns the packages a declaration placed in pkg needs to import.
func (s *splitter) importsOf(d *decl, pkg string) map[string]bool {
	out := map[string]bool{}
	info := s.pkg.TypesInfo
	var recv types.Object
	if fd, ok := d.node.(*ast.FuncDecl); ok && d.isHub && pkg != rootPkg {
		recv = receiverObj(info, fd)
	}
	viaRecv := map[*ast.Ident]bool{}
	if recv != nil {
		ast.Inspect(d.node, func(n ast.Node) bool {
			if se, ok := n.(*ast.SelectorExpr); ok {
				if id, ok := se.X.(*ast.Ident); ok && info.Uses[id] == recv {
					viaRecv[se.Sel] = true
				}
			}
			return true
		})
	}
	s.visitRefs(d.node, func(id *ast.Ident, _ types.Object, key string) {
		if viaRecv[id] {
			return // wired through Handlers
		}
		if d.isHub && pkg != rootPkg && key == s.hubType() {
			return // the receiver type becomes Handlers
		}
		if s.isTestKey(key) {
			return // test helpers are copied alongside
		}
		if q := s.pkgOf(key); q != pkg {
			out[q] = true
		}
	})
	return out
}

// repairImports resolves cycles the plan's coarser graph could not see.
// A package that would import the root gives its offending declarations to
// the root. In a cycle between packages, the edge backed by fewer
// declarations is broken by moving its targets (and what they need from
// their package) into the package that uses them.
func (s *splitter) repairImports() error {
	for range 200 {
		err := s.buildImports(false)
		if err == nil {
			return nil
		}
		var rv *rootViolation
		var cv *cycleViolation
		switch {
		case errors.As(err, &rv):
			for _, key := range rv.decls {
				if d := s.decls[key]; d != nil {
					s.moveUnit(d.unit, rootPkg, "imports the root")
				}
			}
		case errors.As(err, &cv):
			best, bestKeys := -1, []string(nil)
			for i := 0; i+1 < len(cv.cycle); i++ {
				keys := s.edgeTargets(cv.cycle[i], cv.cycle[i+1])
				if best < 0 || len(keys) < len(bestKeys) {
					best, bestKeys = i, keys
				}
			}
			from, to := cv.cycle[best], cv.cycle[best+1]
			for _, key := range s.sameClosure(bestKeys, to) {
				s.moveUnit(s.unitOfKey(key), from, "breaks cycle "+display(from)+" <-> "+display(to))
			}
		default:
			return err
		}
		s.analyzeHub()
	}
	return errors.New("import repair did not converge")
}

type rootViolation struct {
	pkg   string
	decls []string
}

func (e *rootViolation) Error() string {
	return "package " + e.pkg + " would import the root package: " + strings.Join(e.decls, ", ")
}

type cycleViolation struct{ cycle []string }

func (e *cycleViolation) Error() string { return "import cycle: " + strings.Join(e.cycle, " -> ") }

// edgeTargets lists keys in package to that declarations in from use.
func (s *splitter) edgeTargets(from, to string) []string {
	seen := map[string]bool{}
	for _, f := range s.files {
		for _, d := range f.decls {
			if d.test || !d.homes[from] {
				continue
			}
			for key := range s.refsFrom(d, from) {
				if s.pkgOf(key) == to {
					seen[key] = true
				}
			}
		}
	}
	return sortedKeys(seen)
}

// refsFrom lists the keys a declaration placed in pkg imports from other
// packages (the references importsOf counts).
func (s *splitter) refsFrom(d *decl, pkg string) map[string]bool {
	out := map[string]bool{}
	info := s.pkg.TypesInfo
	var recv types.Object
	if fd, ok := d.node.(*ast.FuncDecl); ok && d.isHub && pkg != rootPkg {
		recv = receiverObj(info, fd)
	}
	viaRecv := map[*ast.Ident]bool{}
	if recv != nil {
		ast.Inspect(d.node, func(n ast.Node) bool {
			if se, ok := n.(*ast.SelectorExpr); ok {
				if id, ok := se.X.(*ast.Ident); ok && info.Uses[id] == recv {
					viaRecv[se.Sel] = true
				}
			}
			return true
		})
	}
	s.visitRefs(d.node, func(id *ast.Ident, _ types.Object, key string) {
		if viaRecv[id] || (d.isHub && pkg != rootPkg && key == s.hubType()) {
			return
		}
		if s.isTestKey(key) {
			return
		}
		if s.pkgOf(key) != pkg {
			out[key] = true
		}
	})
	return out
}

// sameClosure extends keys with everything they reference inside pkg.
func (s *splitter) sameClosure(keys []string, pkg string) []string {
	seen := map[string]bool{}
	stack := append([]string{}, keys...)
	for len(stack) > 0 {
		k := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if seen[k] {
			continue
		}
		seen[k] = true
		for _, d := range s.unitDecls(s.unitOfKey(k)) {
			s.visitRefs(d.node, func(_ *ast.Ident, _ types.Object, key string) {
				if s.pkgOf(key) == pkg && !seen[key] && !s.isTestKey(key) && !s.isHubMethod(key) {
					stack = append(stack, key)
				}
			})
		}
	}
	return sortedKeys(seen)
}

func (s *splitter) unitOfKey(key string) string {
	if d := s.decls[key]; d != nil {
		return d.unit
	}
	if kind, rest, _ := strings.Cut(key, ":"); kind == "method" || kind == "field" {
		return "type:" + strings.SplitN(rest, ".", 2)[0]
	}
	return key
}

func (s *splitter) unitDecls(unit string) []*decl {
	var out []*decl
	for _, f := range s.files {
		for _, d := range f.decls {
			if !d.test && d.unit == unit {
				out = append(out, d)
			}
		}
	}
	return out
}

func (s *splitter) moveUnit(unit, to, why string) {
	for _, d := range s.unitDecls(unit) {
		if d.unit == s.hubType() {
			continue
		}
		keys := []string{d.key}
		if gd, ok := d.node.(*ast.GenDecl); ok {
			keys = groupKeys(gd)
		}
		for _, k := range keys {
			s.prodPkg[k] = to
		}
		d.homes = map[string]bool{to: true}
	}
	for _, key := range s.objKey {
		if rest, ok := strings.CutPrefix(key, "field:"); ok && "type:"+strings.SplitN(rest, ".", 2)[0] == unit {
			s.prodPkg[key] = to
		}
	}
	s.notes = append(s.notes, fmt.Sprintf("moved %s to %s (%s)", unit, display(to), why))
}

func (s *splitter) buildImports(withTests bool) error {
	s.imports = map[string]map[string]bool{}
	edge := func(from, to string) {
		if from != to {
			add(s.imports, from, to)
		}
	}
	for _, f := range s.files {
		for _, d := range f.decls {
			if d.test && !withTests {
				continue
			}
			for p := range d.homes {
				for q := range s.importsOf(d, p) {
					edge(p, q)
				}
			}
		}
	}
	fieldTypes := s.hubFieldTypeKeys()
	for p, fields := range s.hubFields {
		for f := range fields {
			for _, k := range fieldTypes[f] {
				edge(p, s.pkgOf(k))
			}
		}
	}
	for p, calls := range s.hubCalls {
		for m := range calls {
			d := s.decls[m]
			if d == nil {
				continue
			}
			fd := d.node.(*ast.FuncDecl)
			s.visitRefs(fd.Type, func(_ *ast.Ident, _ types.Object, key string) { edge(p, s.pkgOf(key)) })
		}
	}
	for p := range s.imports {
		if p != rootPkg && s.imports[p][rootPkg] {
			var offenders []string
			for _, f := range s.files {
				for _, d := range f.decls {
					if !d.test && d.homes[p] && s.importsOf(d, p)[rootPkg] {
						offenders = append(offenders, d.key)
					}
				}
			}
			if withTests || len(offenders) == 0 {
				return fmt.Errorf("package %s would import the root package: %s", p, s.whyImports(p, rootPkg))
			}
			return &rootViolation{pkg: p, decls: offenders}
		}
	}
	if cyc := findCycle(s.imports); cyc != nil {
		var why []string
		for i := 0; i+1 < len(cyc); i++ {
			why = append(why, fmt.Sprintf("%s -> %s: %s", display(cyc[i]), display(cyc[i+1]), s.whyImports(cyc[i], cyc[i+1])))
		}
		if !withTests {
			return &cycleViolation{cycle: cyc}
		}
		return fmt.Errorf("import cycle: %s\n  %s", strings.Join(cyc, " -> "), strings.Join(why, "\n  "))
	}
	return nil
}

func (s *splitter) whyImports(from, to string) string {
	var out []string
	for _, f := range s.files {
		for _, d := range f.decls {
			if !d.homes[from] {
				continue
			}
			if s.importsOf(d, from)[to] {
				var via []string
				s.visitRefs(d.node, func(_ *ast.Ident, _ types.Object, key string) {
					if s.pkgOf(key) == to && !s.isTestKey(key) {
						via = append(via, key)
					}
				})
				out = append(out, d.key+"{"+strings.Join(uniqueSorted(via), " ")+"}")
			}
		}
	}
	sort.Strings(out)
	if len(out) > 8 {
		out = append(out[:8], "...")
	}
	return strings.Join(out, ", ")
}

func findCycle(g map[string]map[string]bool) []string {
	state := map[string]int{}
	var stack []string
	var cyc []string
	var dfs func(u string) bool
	dfs = func(u string) bool {
		state[u] = 1
		stack = append(stack, u)
		for v := range g[u] {
			if state[v] == 1 {
				for i, x := range stack {
					if x == v {
						cyc = append(append([]string{}, stack[i:]...), v)
					}
				}
				return true
			}
			if state[v] == 0 && dfs(v) {
				return true
			}
		}
		stack = stack[:len(stack)-1]
		state[u] = 2
		return false
	}
	var nodes []string
	for u := range g {
		nodes = append(nodes, u)
	}
	sort.Strings(nodes)
	for _, u := range nodes {
		if state[u] == 0 && dfs(u) {
			return cyc
		}
	}
	return nil
}

func (s *splitter) reach(p string) map[string]bool {
	seen := map[string]bool{p: true}
	stack := []string{p}
	for len(stack) > 0 {
		u := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for v := range s.imports[u] {
			if !seen[v] {
				seen[v] = true
				stack = append(stack, v)
			}
		}
	}
	return seen
}

var testNamePattern = regexp.MustCompile(`\b(Test|Benchmark|Fuzz|Example)[A-Z_]\w*`)

// testDeps returns the test declarations d uses directly, including tests
// named in string literals (helper-process re-exec targets).
func (s *splitter) testDeps(d *decl) []*decl {
	var out []*decl
	seen := map[*decl]bool{}
	addDep := func(t *decl) {
		if t != nil && t.test && t != d && !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	s.visitRefs(d.node, func(_ *ast.Ident, _ types.Object, key string) {
		if t := s.decls[key]; t != nil && t.test {
			addDep(t)
		}
		if strings.HasPrefix(key, "method:") || strings.HasPrefix(key, "field:") {
			typ := "type:" + strings.SplitN(strings.SplitN(key, ":", 2)[1], ".", 2)[0]
			if t := s.decls[typ]; t != nil && t.test {
				addDep(t)
			}
		}
	})
	ast.Inspect(d.node, func(n ast.Node) bool {
		if bl, ok := n.(*ast.BasicLit); ok && bl.Kind == token.STRING {
			for _, m := range testNamePattern.FindAllString(bl.Value, -1) {
				addDep(s.decls["func:"+m])
			}
		}
		return true
	})
	// Methods declared on a test type travel with it.
	if gd, ok := d.node.(*ast.GenDecl); ok && gd.Tok == token.TYPE {
		for _, f := range s.files {
			for _, m := range f.decls {
				if fd, ok := m.node.(*ast.FuncDecl); ok && fd.Recv != nil && m.test && "type:"+recvName(fd) == d.key {
					addDep(m)
				}
			}
		}
	}
	return out
}

func (s *splitter) testClosure(roots []*decl) map[*decl]bool {
	seen := map[*decl]bool{}
	stack := append([]*decl{}, roots...)
	for len(stack) > 0 {
		d := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if seen[d] {
			continue
		}
		seen[d] = true
		stack = append(stack, s.testDeps(d)...)
	}
	return seen
}

// placeTests assigns every in-package test to the deepest package that can
// see everything it (and its helpers) touch, preferring the package whose
// code it references most; helpers and TestMain are copied along.
func (s *splitter) placeTests() {
	var setups, tests []*decl
	for _, f := range s.files {
		for _, d := range f.decls {
			switch {
			case d.setup:
				setups = append(setups, d)
			case d.isTest:
				tests = append(tests, d)
			}
		}
	}
	setupClosure := s.testClosure(setups)
	setupNeeds := map[string]bool{}
	setupPortable := true
	for d := range setupClosure {
		if d.file.build != "" {
			setupPortable = false
		}
		s.visitRefs(d.node, func(_ *ast.Ident, _ types.Object, key string) {
			if !s.isTestKey(key) {
				setupNeeds[s.pkgOf(key)] = true
			}
		})
	}
	var pkgs []string
	for p := range s.imports {
		pkgs = append(pkgs, p)
	}
	for _, p := range s.prodPkg {
		pkgs = append(pkgs, p)
	}
	pkgs = uniqueSorted(pkgs)
	moved := map[string]bool{}
	for _, t := range tests {
		closure := s.testClosure([]*decl{t})
		weight := map[string]int{}
		portable := setupPortable
		for d := range closure {
			if d.file.build != "" || strings.Contains(string(d.file.src[d.file.tf.Offset(d.node.Pos()):d.file.tf.Offset(d.node.End())]), "testdata") {
				portable = false
			}
			s.visitRefs(d.node, func(_ *ast.Ident, _ types.Object, key string) {
				if s.isTestKey(key) {
					return
				}
				weight[s.pkgOf(key)]++
			})
		}
		home := rootPkg
		if portable && weight[rootPkg] == 0 {
			best := -1
			for _, p := range pkgs {
				if p == rootPkg {
					continue
				}
				r := s.reach(p)
				ok := true
				for q := range weight {
					if !r[q] {
						ok = false
					}
				}
				for q := range setupNeeds {
					if !r[q] {
						ok = false
					}
				}
				if ok && weight[p] > best {
					best, home = weight[p], p
				}
			}
		}
		for d := range closure {
			d.homes[home] = true
		}
		if home != rootPkg {
			moved[home] = true
		}
	}
	for p := range moved {
		for d := range setupClosure {
			d.homes[p] = true
		}
	}
	// Tests that did not move keep their helpers and setup in the root.
	var stay []*decl
	for _, f := range s.files {
		for _, d := range f.decls {
			if !d.test {
				continue
			}
			if (d.isTest && d.homes[rootPkg]) || d.setup || len(d.homes) == 0 {
				stay = append(stay, d)
			}
		}
	}
	for d := range s.testClosure(stay) {
		d.homes[rootPkg] = true
	}
	for _, f := range s.files {
		for _, d := range f.decls {
			if d.test && len(d.homes) == 0 {
				d.homes[rootPkg] = true
			}
		}
	}
}

func uniqueSorted(in []string) []string {
	sort.Strings(in)
	out := in[:0]
	for i, v := range in {
		if i == 0 || v != in[i-1] {
			out = append(out, v)
		}
	}
	return out
}

// computeRenames exports every source-package identifier referenced across
// the new package boundaries, keeping interface methods and their
// implementations consistent.
func (s *splitter) computeRenames() error {
	s.rename = map[string]string{}
	need := map[string]bool{}
	for _, f := range s.files {
		for _, d := range f.decls {
			for p := range d.homes {
				s.visitRefs(d.node, func(_ *ast.Ident, obj types.Object, key string) {
					if !obj.Exported() && s.crosses(d, p, key) {
						need[key] = true
					}
				})
			}
		}
	}
	for p, calls := range s.hubCalls {
		for m := range calls {
			if s.pkgOf(m) != rootPkg {
				need[m] = true
			}
			// Types in a wired call's signature are named from package p.
			if d := s.decls[m]; d != nil {
				s.visitRefs(d.node.(*ast.FuncDecl).Type, func(_ *ast.Ident, obj types.Object, key string) {
					if !obj.Exported() && s.pkgOf(key) != p {
						need[key] = true
					}
				})
			}
		}
	}
	// Hub field types are named from each Handlers package that holds them.
	fieldTypes := s.hubFieldTypeKeys()
	for p, fields := range s.hubFields {
		for f := range fields {
			for _, k := range fieldTypes[f] {
				if s.pkgOf(k) != p && !ast.IsExported(keyName(k)) {
					need[k] = true
				}
			}
		}
	}
	// Moved hub methods that root code or other packages call are exported.
	for _, f := range s.files {
		for _, d := range f.decls {
			for p := range d.homes {
				s.visitRefs(d.node, func(_ *ast.Ident, _ types.Object, key string) {
					if s.isHubMethod(key) && s.pkgOf(key) != p && s.pkgOf(key) != rootPkg {
						need[key] = true
					}
				})
			}
		}
	}
	for key := range s.linkedMethods(need) {
		need[key] = true
	}
	taken := map[string]bool{}
	for key := range s.prodPkg {
		taken[s.pkgOf(key)+"|"+scopeName(key)] = true
	}
	// Method sets include promoted methods (for example from an embedded
	// context), which have no declaration of their own here.
	scope := s.pkg.Types.Scope()
	for _, n := range scope.Names() {
		tn, ok := scope.Lookup(n).(*types.TypeName)
		if !ok {
			continue
		}
		p := s.pkgOf("type:" + n)
		for _, t := range []types.Type{tn.Type(), types.NewPointer(tn.Type())} {
			for sel := range types.NewMethodSet(t).Methods() {
				taken[p+"|"+n+"."+sel.Obj().Name()] = true
			}
		}
	}
	var clashes []string
	for key := range need {
		name := keyName(key)
		exp := exported(name)
		if exp == name {
			continue
		}
		slot := s.pkgOf(key) + "|" + scopeNameWith(key, exp)
		if taken[slot] && strings.HasPrefix(key, "field:") {
			// A field clashing with a method of its type keeps a suffix.
			suffix := "Value"
			if v := s.fieldVar(key); v != nil {
				if _, ok := v.Type().Underlying().(*types.Signature); ok {
					suffix = "Func"
				}
			}
			exp += suffix
			slot = s.pkgOf(key) + "|" + scopeNameWith(key, exp)
		}
		if taken[slot] {
			clashes = append(clashes, key+" -> "+exp)
			continue
		}
		taken[slot] = true
		s.rename[key] = exp
	}
	if len(clashes) > 0 {
		sort.Strings(clashes)
		return errors.New("export renames collide with existing names:\n  " + strings.Join(clashes, "\n  "))
	}
	return nil
}

// crosses reports whether a reference from d (placed in pkg) to key crosses
// a package boundary.
func (s *splitter) crosses(d *decl, pkg, key string) bool {
	if t := s.decls[key]; t != nil && t.test {
		return false
	}
	if s.isHubField(key) {
		return false // hub fields keep their names in the root; Handlers fields are generated
	}
	return s.pkgOf(key) != pkg
}

// linkedMethods returns method keys that must be renamed together with the
// requested ones because an interface in the package ties them.
func (s *splitter) linkedMethods(need map[string]bool) map[string]bool {
	out := map[string]bool{}
	scope := s.pkg.Types.Scope()
	var named []*types.TypeName
	var ifaces []*types.TypeName
	for _, n := range scope.Names() {
		tn, ok := scope.Lookup(n).(*types.TypeName)
		if !ok {
			continue
		}
		if _, ok := tn.Type().Underlying().(*types.Interface); ok {
			ifaces = append(ifaces, tn)
		} else {
			named = append(named, tn)
		}
	}
	for _, it := range ifaces {
		iface := it.Type().Underlying().(*types.Interface)
		for m := range iface.Methods() {
			if m.Exported() {
				continue
			}
			group := []string{"method:" + it.Name() + "." + m.Name()}
			for _, tn := range named {
				if types.Implements(types.NewPointer(tn.Type()), iface) || types.Implements(tn.Type(), iface) {
					group = append(group, "method:"+tn.Name()+"."+m.Name())
				}
			}
			hit := false
			for _, k := range group {
				if need[k] {
					hit = true
				}
			}
			if hit {
				for _, k := range group {
					out[k] = true
				}
			}
		}
	}
	return out
}

func (s *splitter) fieldVar(key string) *types.Var {
	for obj, k := range s.objKey {
		if k == key {
			if v, ok := obj.(*types.Var); ok {
				return v
			}
		}
	}
	return nil
}

func keyName(key string) string {
	rest := strings.SplitN(key, ":", 2)[1]
	if i := strings.LastIndex(rest, "."); i >= 0 {
		return rest[i+1:]
	}
	return rest
}

// scopeName identifies the namespace a key's name lives in: package scope
// for top-level names, the receiver type for methods and fields.
func scopeName(key string) string { return scopeNameWith(key, keyName(key)) }

func scopeNameWith(key, name string) string {
	kind, rest, _ := strings.Cut(key, ":")
	if kind == "method" || kind == "field" {
		return strings.SplitN(rest, ".", 2)[0] + "." + name
	}
	return name
}

func exported(name string) string {
	r := []rune(name)
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

func (s *splitter) report() {
	lines := map[string]int{}
	tests := map[string]int{}
	for _, f := range s.files {
		for _, d := range f.decls {
			n := s.fset.Position(d.node.End()).Line - s.fset.Position(d.node.Pos()).Line + 1
			for p := range d.homes {
				if d.test {
					tests[p] += n
				} else {
					lines[p] += n
				}
			}
		}
	}
	var pkgs []string
	for p := range lines {
		pkgs = append(pkgs, p)
	}
	for p := range tests {
		pkgs = append(pkgs, p)
	}
	pkgs = uniqueSorted(pkgs)
	fmt.Printf("%-18s %6s %6s %7s %6s\n", "package", "prod", "tests", "fields", "calls")
	for _, p := range pkgs {
		name := p
		if name == rootPkg {
			name = "(root)"
		}
		fmt.Printf("%-18s %6d %6d %7d %6d\n", name, lines[p], tests[p], len(s.hubFields[p]), len(s.hubCalls[p]))
	}
	fmt.Printf("exported renames: %d\n", len(s.rename))
	for _, n := range s.notes {
		fmt.Println("note:", n)
	}
}

func (s *splitter) checkImports() error {
	var order []string
	for p, deps := range s.imports {
		for q := range deps {
			order = append(order, fmt.Sprintf("%s -> %s", display(p), display(q)))
		}
	}
	sort.Strings(order)
	fmt.Printf("package imports within the split: %d edges\n", len(order))
	return nil
}

func display(p string) string {
	if p == rootPkg {
		return "(root)"
	}
	return p
}
