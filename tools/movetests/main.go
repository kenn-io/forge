// Command movetests moves black-box tests out of a package's internal test
// variant into separate test-only packages.
//
// Large in-package test suites compile and type-check as one unit on one
// core. Moving tests that only use a package's exported API into sibling
// packages lets them build and lint in parallel, and keeps test-only edits
// from recompiling the whole suite.
//
// Usage:
//
//	go run ./tools/movetests -config tools/movetests/server.json [-list] [-apply]
//
// The config names the source package and ordered rules mapping test names
// (and optionally source file names) to destination packages; the first
// matching rule wins and unmatched tests stay. For every matched test the
// tool follows the package-level test declarations it uses (helpers, types
// with their methods, variables) and tests named in string literals
// (helper-process re-exec targets). A test moves only if nothing it reaches
// uses an unexported identifier of the production package or lives in a
// platform-specific file. TestMain and moveable init functions are copied
// into every destination so process setup is unchanged. Declarations still
// needed in the source package are copied rather than moved. Exported
// production identifiers are qualified with the package name. Without
// -apply the tool only prints the plan.
package main

import (
	"encoding/json/v2"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/build/constraint"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/imports"
)

type rule struct {
	Match  string `json:"match"`
	File   string `json:"file"`
	Dest   string `json:"dest"`
	re     *regexp.Regexp
	fileRe *regexp.Regexp
}

type config struct {
	Source string `json:"source"`
	Rules  []rule `json:"rules"`
	// Keep lists tests that must stay in the source package even though
	// they only use exported identifiers, for example because they rely on
	// in-package test setup that a destination cannot reproduce.
	Keep []string `json:"keep"`
}

// piece is one movable chunk of source text: a whole declaration, or a
// single spec split out of a grouped var/type declaration.
type piece struct {
	node    ast.Node
	group   *ast.GenDecl // set when node is a spec inside a parenthesized group
	keyword string       // "var" or "type" for split specs
	file    string
}

func (pc *piece) start() token.Pos {
	switch n := pc.node.(type) {
	case *ast.FuncDecl:
		if n.Doc != nil {
			return n.Doc.Pos()
		}
	case *ast.GenDecl:
		if n.Doc != nil {
			return n.Doc.Pos()
		}
	case *ast.ValueSpec:
		if n.Doc != nil {
			return n.Doc.Pos()
		}
	case *ast.TypeSpec:
		if n.Doc != nil {
			return n.Doc.Pos()
		}
	}
	return pc.node.Pos()
}

// unit is the set of pieces that must move together: a function, a spec, a
// const group, or a type with its methods.
type unit struct {
	name     string
	pieces   []*piece
	deps     map[*unit]bool
	blockers map[string]bool
	qualify  []token.Pos
	isTest   bool
	isSetup  bool
	platform bool
}

type span struct {
	start, end token.Pos
	unit       *unit
}

type planner struct {
	cfg        config
	fset       *token.FileSet
	pkg        *packages.Package
	units      []*unit
	spans      []span
	byName     map[string]*unit
	fileSyntax map[string]*ast.File
}

func main() {
	cfgPath := flag.String("config", "", "path to the JSON move plan")
	apply := flag.Bool("apply", false, "write the moved files instead of printing the plan")
	list := flag.Bool("list", false, "list each moved test with its source file and line count")
	flag.Parse()
	if err := run(*cfgPath, *apply, *list); err != nil {
		fmt.Fprintln(os.Stderr, "movetests:", err)
		os.Exit(1)
	}
}

func run(cfgPath string, apply, list bool) error {
	if cfgPath == "" {
		return errors.New("-config is required")
	}
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		return err
	}
	var cfg config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return fmt.Errorf("parse %s: %w", cfgPath, err)
	}
	for i := range cfg.Rules {
		if cfg.Rules[i].re, err = regexp.Compile(cfg.Rules[i].Match); err != nil {
			return fmt.Errorf("rule %d match: %w", i, err)
		}
		if cfg.Rules[i].fileRe, err = regexp.Compile(cfg.Rules[i].File); err != nil {
			return fmt.Errorf("rule %d file: %w", i, err)
		}
	}
	p, err := load(cfg)
	if err != nil {
		return err
	}
	p.index()
	pl := p.plan()
	p.report(pl)
	if list {
		p.list(pl)
	}
	if !apply {
		return nil
	}
	return p.apply(pl)
}

func load(cfg config) (*planner, error) {
	lc := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedSyntax |
			packages.NeedTypes | packages.NeedTypesInfo | packages.NeedImports,
		Tests: true,
	}
	pkgs, err := packages.Load(lc, cfg.Source)
	if err != nil {
		return nil, err
	}
	var target *packages.Package
	for _, c := range pkgs {
		if strings.HasSuffix(c.ID, ".test]") && !strings.HasSuffix(c.Name, "_test") && len(c.Syntax) > 0 {
			target = c
		}
	}
	if target == nil {
		return nil, fmt.Errorf("no internal test variant found for %s", cfg.Source)
	}
	if len(target.Errors) > 0 {
		return nil, fmt.Errorf("load %s: %v", target.ID, target.Errors[0])
	}
	p := &planner{cfg: cfg, fset: target.Fset, pkg: target, byName: map[string]*unit{}, fileSyntax: map[string]*ast.File{}}
	for _, f := range target.Syntax {
		p.fileSyntax[target.Fset.Position(f.Pos()).Filename] = f
	}
	return p, nil
}

func hasPlatformConstraint(path string, f *ast.File) bool {
	for _, cg := range f.Comments {
		if cg.Pos() > f.Package {
			break
		}
		for _, c := range cg.List {
			if constraint.IsGoBuild(c.Text) || constraint.IsPlusBuild(c.Text) {
				return true
			}
		}
	}
	base := strings.TrimSuffix(filepath.Base(path), "_test.go")
	for _, goos := range []string{"unix", "linux", "darwin", "windows", "freebsd", "openbsd", "netbsd"} {
		if strings.HasSuffix(base, "_"+goos) {
			return true
		}
	}
	return false
}

func (p *planner) newUnit(name string) *unit {
	u := &unit{name: name, deps: map[*unit]bool{}, blockers: map[string]bool{}}
	p.units = append(p.units, u)
	return u
}

// splittable reports whether a grouped declaration can be split per spec.
// Const groups stay whole because iota and implicit repetition depend on
// spec order.
func splittable(d *ast.GenDecl) bool {
	return d.Lparen.IsValid() && (d.Tok == token.VAR || d.Tok == token.TYPE)
}

// index builds units and their dependency edges from the test files.
func (p *planner) index() {
	typeUnits := map[string]*unit{}
	type method struct {
		decl *ast.FuncDecl
		file string
	}
	var methods []method
	paths := make([]string, 0, len(p.fileSyntax))
	for path := range p.fileSyntax {
		if strings.HasSuffix(path, "_test.go") {
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)
	for _, path := range paths {
		f := p.fileSyntax[path]
		platform := hasPlatformConstraint(path, f)
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *ast.GenDecl:
				if d.Tok == token.IMPORT {
					continue
				}
				if splittable(d) {
					for _, s := range d.Specs {
						u := p.newUnit(specName(s))
						u.pieces = append(u.pieces, &piece{node: s, group: d, keyword: d.Tok.String(), file: path})
						u.platform = platform
						p.registerSpec(u, s, typeUnits)
					}
					continue
				}
				u := p.newUnit("")
				u.pieces = append(u.pieces, &piece{node: d, file: path})
				u.platform = platform
				for _, s := range d.Specs {
					if u.name == "" {
						u.name = specName(s)
					}
					p.registerSpec(u, s, typeUnits)
				}
			case *ast.FuncDecl:
				if d.Recv != nil {
					methods = append(methods, method{d, path})
					continue
				}
				u := p.newUnit(d.Name.Name)
				u.pieces = append(u.pieces, &piece{node: d, file: path})
				u.platform = platform
				switch name := d.Name.Name; {
				case name == "TestMain" || name == "init":
					u.isSetup = true
				case isTestFunc(name):
					u.isTest = true
				}
				if d.Name.Name != "init" {
					p.byName[d.Name.Name] = u
				}
			}
		}
	}
	for _, m := range methods {
		recv := receiverTypeName(m.decl)
		u := typeUnits[recv]
		if u == nil {
			u = p.newUnit(recv + "." + m.decl.Name.Name)
			u.blockers["method on production type "+recv] = true
		}
		u.pieces = append(u.pieces, &piece{node: m.decl, file: m.file})
		u.platform = u.platform || hasPlatformConstraint(m.file, p.fileSyntax[m.file])
	}
	for _, u := range p.units {
		for _, pc := range u.pieces {
			p.spans = append(p.spans, span{pc.start(), pc.node.End(), u})
		}
	}
	sort.Slice(p.spans, func(i, j int) bool { return p.spans[i].start < p.spans[j].start })
	for _, u := range p.units {
		for _, pc := range u.pieces {
			p.scan(u, pc.node)
		}
	}
}

func (p *planner) registerSpec(u *unit, s ast.Spec, typeUnits map[string]*unit) {
	switch s := s.(type) {
	case *ast.TypeSpec:
		typeUnits[s.Name.Name] = u
		p.byName[s.Name.Name] = u
	case *ast.ValueSpec:
		for _, n := range s.Names {
			p.byName[n.Name] = u
		}
	}
}

func specName(s ast.Spec) string {
	switch s := s.(type) {
	case *ast.TypeSpec:
		return s.Name.Name
	case *ast.ValueSpec:
		return s.Names[0].Name
	}
	return ""
}

// scan records the dependencies, blockers, and identifiers to qualify for
// one piece of a unit.
func (p *planner) scan(u *unit, node ast.Node) {
	info := p.pkg.TypesInfo
	scope := p.pkg.Types.Scope()
	ast.Inspect(node, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.BasicLit:
			if x.Kind != token.STRING {
				return true
			}
			for _, m := range testNamePattern.FindAllString(x.Value, -1) {
				if t := p.byName[m]; t != nil && t != u {
					u.deps[t] = true
				}
			}
			if strings.Contains(x.Value, "testdata") {
				u.blockers["relative testdata path"] = true
			}
		case *ast.Ident:
			obj := info.Uses[x]
			if obj == nil || obj.Pkg() != p.pkg.Types {
				return true
			}
			if strings.HasSuffix(p.fset.Position(obj.Pos()).Filename, "_test.go") {
				if t := p.unitAt(obj.Pos()); t != nil && t != u {
					u.deps[t] = true
				}
				return true
			}
			if !obj.Exported() {
				u.blockers[obj.Name()] = true
				return true
			}
			if obj.Parent() == scope {
				u.qualify = append(u.qualify, x.Pos())
			}
		}
		return true
	})
}

var testNamePattern = regexp.MustCompile(`\b(Test|Benchmark|Fuzz|Example)[A-Z_]\w*`)

func isTestFunc(name string) bool {
	for _, prefix := range []string{"Test", "Benchmark", "Fuzz", "Example"} {
		if rest, ok := strings.CutPrefix(name, prefix); ok && (rest == "" || rest[0] < 'a' || rest[0] > 'z') {
			return true
		}
	}
	return false
}

func receiverTypeName(fd *ast.FuncDecl) string {
	expr := fd.Recv.List[0].Type
	for {
		switch t := expr.(type) {
		case *ast.StarExpr:
			expr = t.X
		case *ast.IndexExpr:
			expr = t.X
		case *ast.IndexListExpr:
			expr = t.X
		case *ast.ParenExpr:
			expr = t.X
		case *ast.Ident:
			return t.Name
		default:
			return ""
		}
	}
}

func (p *planner) unitAt(pos token.Pos) *unit {
	i := sort.Search(len(p.spans), func(i int) bool { return p.spans[i].start > pos }) - 1
	for ; i >= 0; i-- {
		if s := p.spans[i]; pos >= s.start && pos < s.end {
			return s.unit
		}
		if pos-p.spans[i].start > 1<<20 {
			break
		}
	}
	return nil
}

func closure(roots []*unit) map[*unit]bool {
	seen := map[*unit]bool{}
	var walk func(u *unit)
	walk = func(u *unit) {
		if seen[u] {
			return
		}
		seen[u] = true
		for d := range u.deps {
			walk(d)
		}
	}
	for _, r := range roots {
		walk(r)
	}
	return seen
}

func blockersOf(set map[*unit]bool) []string {
	var out []string
	for u := range set {
		for b := range u.blockers {
			out = append(out, b)
		}
		if u.platform {
			out = append(out, "platform-specific file")
		}
	}
	sort.Strings(out)
	return compact(out)
}

func compact(s []string) []string {
	out := s[:0]
	for i, v := range s {
		if i == 0 || v != s[i-1] {
			out = append(out, v)
		}
	}
	return out
}

func (p *planner) lines(u *unit) int {
	n := 0
	for _, pc := range u.pieces {
		n += p.fset.Position(pc.node.End()).Line - p.fset.Position(pc.start()).Line + 1
	}
	return n
}

type destPlan struct {
	dest  string
	roots []*unit
	units map[*unit]bool
	lines int
}

type plan struct {
	dests      []*destPlan
	keep       map[*unit]bool
	blocked    map[string][]string
	setupNotes []string
}

func (p *planner) plan() plan {
	keepNames := map[string]bool{}
	for _, k := range p.cfg.Keep {
		keepNames[k] = true
	}
	pl := plan{blocked: map[string][]string{}}
	var setup []*unit
	for _, u := range p.units {
		if !u.isSetup {
			continue
		}
		if b := blockersOf(closure([]*unit{u})); len(b) > 0 {
			pl.setupNotes = append(pl.setupNotes, fmt.Sprintf("%s in %s not copied (%s)", u.name, filepath.Base(u.pieces[0].file), strings.Join(b, ", ")))
			continue
		}
		setup = append(setup, u)
	}
	destByName := map[string]*destPlan{}
	for _, r := range p.cfg.Rules {
		if destByName[r.Dest] == nil {
			d := &destPlan{dest: r.Dest}
			destByName[r.Dest] = d
			pl.dests = append(pl.dests, d)
		}
	}
	var tests []*unit
	for _, u := range p.units {
		if u.isTest {
			tests = append(tests, u)
		}
	}
	sort.Slice(tests, func(i, j int) bool { return tests[i].name < tests[j].name })
	moved := map[*unit]bool{}
	for _, t := range tests {
		if keepNames[t.name] {
			continue
		}
		var d *destPlan
		for _, r := range p.cfg.Rules {
			if r.re.MatchString(t.name) && r.fileRe.MatchString(filepath.Base(t.pieces[0].file)) {
				d = destByName[r.Dest]
				break
			}
		}
		if d == nil {
			continue
		}
		if b := blockersOf(closure([]*unit{t})); len(b) > 0 {
			pl.blocked[t.name] = b
			continue
		}
		d.roots = append(d.roots, t)
		moved[t] = true
	}
	inDest := map[*unit]bool{}
	for _, d := range pl.dests {
		if len(d.roots) == 0 {
			continue
		}
		d.units = closure(append(append([]*unit{}, d.roots...), setup...))
		for u := range d.units {
			inDest[u] = true
			d.lines += p.lines(u)
		}
	}
	// The source keeps every test that did not move, its setup, whatever
	// excluded-platform files reference, and anything no destination took,
	// together with everything those need.
	var stay []*unit
	for _, u := range p.units {
		if (u.isTest && !moved[u]) || u.isSetup || !inDest[u] {
			stay = append(stay, u)
		}
	}
	for _, name := range p.namesUsedByIgnoredFiles() {
		if u := p.byName[name]; u != nil {
			stay = append(stay, u)
		}
	}
	pl.keep = closure(stay)
	return pl
}

// namesUsedByIgnoredFiles returns identifiers referenced by test files that
// the current build configuration excludes, so declarations they need stay.
func (p *planner) namesUsedByIgnoredFiles() []string {
	var names []string
	for _, path := range p.pkg.IgnoredFiles {
		if !strings.HasSuffix(path, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			continue
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok {
				names = append(names, id.Name)
			}
			return true
		})
	}
	return names
}

func (p *planner) report(pl plan) {
	total := 0
	for _, d := range pl.dests {
		total += len(d.roots)
		fmt.Printf("%-48s %4d tests  %6d lines\n", d.dest, len(d.roots), d.lines)
	}
	fmt.Printf("moved tests: %d\n", total)
	names := make([]string, 0, len(pl.blocked))
	for n := range pl.blocked {
		names = append(names, n)
	}
	sort.Strings(names)
	fmt.Printf("matched but blocked: %d\n", len(names))
	for _, n := range names {
		fmt.Printf("  %s: %s\n", n, strings.Join(pl.blocked[n], ", "))
	}
	for _, s := range pl.setupNotes {
		fmt.Println("setup:", s)
	}
}

func (p *planner) list(pl plan) {
	for _, d := range pl.dests {
		for _, t := range d.roots {
			fmt.Printf("%s\t%s\t%s\t%d\n", d.dest, filepath.Base(t.pieces[0].file), t.name, p.lines(t))
		}
	}
}

func (p *planner) apply(pl plan) error {
	for _, d := range pl.dests {
		if len(d.roots) == 0 {
			continue
		}
		if entries, err := os.ReadDir(d.dest); err == nil && len(entries) > 0 {
			return fmt.Errorf("destination %s already exists and is not empty", d.dest)
		}
		if err := os.MkdirAll(d.dest, 0o755); err != nil {
			return err
		}
		byFile := map[string][]*piece{}
		for u := range d.units {
			for _, pc := range u.pieces {
				byFile[pc.file] = append(byFile[pc.file], pc)
			}
		}
		for src, pieces := range byFile {
			out, err := p.renderFile(src, pieces, filepath.Base(d.dest))
			if err != nil {
				return fmt.Errorf("%s: %w", d.dest, err)
			}
			if err := os.WriteFile(filepath.Join(d.dest, filepath.Base(src)), out, 0o644); err != nil {
				return err
			}
		}
	}
	return p.pruneSources(pl)
}

// renderFile writes pieces from src into a new file for the destination
// package, qualifying exported production identifiers.
func (p *planner) renderFile(src string, pieces []*piece, destPkg string) ([]byte, error) {
	content, err := os.ReadFile(src)
	if err != nil {
		return nil, err
	}
	f := p.fileSyntax[src]
	tf := p.fset.File(f.Pos())
	sort.Slice(pieces, func(i, j int) bool { return pieces[i].node.Pos() < pieces[j].node.Pos() })

	pkgName := p.pkg.Name
	qualifier := pkgName
	for _, pc := range pieces {
		ast.Inspect(pc.node, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok && id.Name == pkgName && p.pkg.TypesInfo.Defs[id] != nil {
				qualifier = "forge" + pkgName
			}
			return true
		})
	}

	var b strings.Builder
	for _, cg := range f.Comments {
		if cg.Pos() > f.Package {
			break
		}
		for _, c := range cg.List {
			if constraint.IsGoBuild(c.Text) {
				b.WriteString(c.Text + "\n\n")
			}
		}
	}
	fmt.Fprintf(&b, "package %s\n\nimport (\n", destPkg)
	if qualifier == pkgName {
		fmt.Fprintf(&b, "\t%q\n", p.pkg.PkgPath)
	} else {
		fmt.Fprintf(&b, "\t%s %q\n", qualifier, p.pkg.PkgPath)
	}
	for _, imp := range f.Imports {
		b.WriteString("\t" + string(content[tf.Offset(imp.Pos()):tf.Offset(imp.End())]) + "\n")
	}
	b.WriteString(")\n")
	for _, pc := range pieces {
		u := p.unitAt(pc.start())
		start, end := tf.Offset(pc.start()), tf.Offset(pc.node.End())
		var inserts []int
		for _, q := range u.qualify {
			if q >= pc.node.Pos() && q < pc.node.End() {
				inserts = append(inserts, tf.Offset(q))
			}
		}
		sort.Ints(inserts)
		b.WriteString("\n")
		if pc.group != nil {
			if doc := pc.group.Doc; doc != nil && len(pc.group.Specs) == 1 {
				b.Write(content[tf.Offset(doc.Pos()):tf.Offset(doc.End())])
				b.WriteString("\n")
			}
			b.WriteString(pc.keyword + " ")
		}
		prev := start
		for _, off := range inserts {
			b.Write(content[prev:off])
			b.WriteString(qualifier + ".")
			prev = off
		}
		b.Write(content[prev:end])
		b.WriteString("\n")
	}
	return imports.Process(filepath.Join(filepath.Dir(src), "moved", filepath.Base(src)), []byte(b.String()), &imports.Options{Comments: true, TabIndent: true, TabWidth: 8})
}

// pruneSources removes pieces that moved and have no remaining user in the
// source package, then drops unused imports and emptied files.
func (p *planner) pruneSources(pl plan) error {
	remove := map[string]map[*piece]bool{}
	for _, d := range pl.dests {
		for u := range d.units {
			if pl.keep[u] {
				continue
			}
			for _, pc := range u.pieces {
				if remove[pc.file] == nil {
					remove[pc.file] = map[*piece]bool{}
				}
				remove[pc.file][pc] = true
			}
		}
	}
	for src, set := range remove {
		content, err := os.ReadFile(src)
		if err != nil {
			return err
		}
		tf := p.fset.File(p.fileSyntax[src].Pos())
		// Remove a whole group once all of its specs are gone.
		groupLeft := map[*ast.GenDecl]int{}
		for pc := range set {
			if pc.group != nil {
				groupLeft[pc.group] = len(pc.group.Specs)
			}
		}
		for pc := range set {
			if pc.group != nil {
				groupLeft[pc.group]--
			}
		}
		type cut struct{ start, end int }
		var cuts []cut
		for pc := range set {
			node, start := pc.node, pc.start()
			if pc.group != nil && groupLeft[pc.group] == 0 {
				node, start = pc.group, pc.group.Pos()
				if pc.group.Doc != nil {
					start = pc.group.Doc.Pos()
				}
			}
			cuts = append(cuts, cut{tf.Offset(start), tf.Offset(node.End())})
		}
		sort.Slice(cuts, func(i, j int) bool { return cuts[i].start > cuts[j].start })
		out := content
		last := len(out) + 1
		for _, c := range cuts {
			if c.start >= last {
				continue // already removed as part of a whole group
			}
			end := c.end
			for end < len(out) && out[end] == '\n' {
				end++
			}
			out = append(append([]byte{}, out[:c.start]...), out[end:]...)
			last = c.start
		}
		formatted, err := imports.Process(src, out, &imports.Options{Comments: true, TabIndent: true, TabWidth: 8})
		if err != nil {
			return fmt.Errorf("%s: %w", src, err)
		}
		if onlyImports(formatted) {
			if err := os.Remove(src); err != nil {
				return err
			}
			continue
		}
		if err := os.WriteFile(src, formatted, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func onlyImports(src []byte) bool {
	f, err := parser.ParseFile(token.NewFileSet(), "", src, parser.ParseComments)
	if err != nil {
		return false
	}
	for _, d := range f.Decls {
		if g, ok := d.(*ast.GenDecl); !ok || g.Tok != token.IMPORT {
			return false
		}
	}
	return true
}
