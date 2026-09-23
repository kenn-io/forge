package main

import (
	"bytes"
	"encoding/json/v2"
	"errors"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"golang.org/x/tools/go/ast/astutil"
	"golang.org/x/tools/imports"
)

const handlersType = "Handlers"

type edit struct {
	start, end int
	text       string
}

func mustRead(path string) []byte {
	b, err := os.ReadFile(path)
	if err != nil {
		panic(err)
	}
	return b
}

func (s *splitter) pkgPath(p string) string {
	if p == rootPkg {
		return s.pkg.PkgPath
	}
	return s.pkg.PkgPath + "/" + p
}

func declStart(d ast.Decl) token.Pos {
	switch d := d.(type) {
	case *ast.FuncDecl:
		if d.Doc != nil {
			return d.Doc.Pos()
		}
	case *ast.GenDecl:
		if d.Doc != nil {
			return d.Doc.Pos()
		}
	}
	return d.Pos()
}

// render returns the source of d as it should read in package dest, and the
// split packages it now qualifies.
func (s *splitter) render(d *decl, dest string) (string, map[string]bool) {
	info := s.pkg.TypesInfo
	f := d.file
	off := func(p token.Pos) int { return f.tf.Offset(p) }
	var edits []edit
	covered := map[*ast.Ident]bool{}
	used := map[string]bool{}
	scope := s.pkg.Types.Scope()

	parents := map[ast.Node]ast.Node{}
	var stack []ast.Node
	ast.Inspect(d.node, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return false
		}
		if len(stack) > 0 {
			parents[n] = stack[len(stack)-1]
		}
		stack = append(stack, n)
		return true
	})

	var recv types.Object
	fd, isFunc := d.node.(*ast.FuncDecl)
	movedHub := d.isHub && dest != rootPkg
	if movedHub && isFunc {
		recv = receiverObj(info, fd)
	}

	// Selector-level rewrites.
	ast.Inspect(d.node, func(n ast.Node) bool {
		se, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		obj := info.Uses[se.Sel]
		if obj == nil {
			return true
		}
		key := s.keyOf(obj)
		if recv != nil {
			if id, ok := se.X.(*ast.Ident); ok && info.Uses[id] == recv {
				r := id.Name
				switch {
				case s.isHubField(key):
					name := exported(obj.Name())
					text := r + "." + name
					span := ast.Node(se)
					if s.pointer[obj.Name()] {
						parentSel, _ := parents[se].(*ast.SelectorExpr)
						_, isPtr := obj.Type().Underlying().(*types.Pointer)
						switch u, _ := parents[se].(*ast.UnaryExpr); {
						case u != nil && u.Op == token.AND:
							span = u // &s.f is the shared pointer itself
						case parentSel != nil && parentSel.X == se && !isPtr:
							// s.f.x and s.f.m() auto-dereference the pointer.
						default:
							text = "(*" + r + "." + name + ")"
						}
					}
					edits = append(edits, edit{off(span.Pos()), off(span.End()), text})
					covered[id], covered[se.Sel] = true, true
				case s.isHubMethod(key):
					name := obj.Name()
					if s.pkgOf(key) != dest {
						name = exported(name)
					} else if n, ok := s.rename[key]; ok {
						name = n
					}
					edits = append(edits, edit{off(se.Sel.Pos()), off(se.Sel.End()), name})
					covered[se.Sel] = true
				}
				return true
			}
		}
		if dest == rootPkg && s.isHubMethod(key) && s.pkgOf(key) != rootPkg {
			p := s.pkgOf(key)
			name := obj.Name()
			if n, ok := s.rename[key]; ok {
				name = n
			}
			edits = append(edits, edit{off(se.Sel.Pos()), off(se.Sel.End()), p + "." + name})
			covered[se.Sel] = true
		}
		return true
	})

	// Positional literals of struct types that now live in another package
	// get field names (vet rejects unkeyed literals of foreign types).
	ast.Inspect(d.node, func(n ast.Node) bool {
		cl, ok := n.(*ast.CompositeLit)
		if !ok || len(cl.Elts) == 0 {
			return true
		}
		if _, keyed := cl.Elts[0].(*ast.KeyValueExpr); keyed {
			return true
		}
		named, ok := info.TypeOf(cl).(*types.Named)
		if !ok || named.Obj().Pkg() == nil || named.Obj().Pkg().Path() != s.pkg.PkgPath {
			return true
		}
		st, ok := named.Underlying().(*types.Struct)
		if !ok || s.pkgOf("type:"+named.Obj().Name()) == dest || st.NumFields() != len(cl.Elts) {
			return true
		}
		for i, el := range cl.Elts {
			name := st.Field(i).Name()
			if n, ok := s.rename["field:"+named.Obj().Name()+"."+name]; ok {
				name = n
			}
			edits = append(edits, edit{off(el.Pos()), off(el.Pos()), name + ": "})
		}
		return true
	})

	// Identifier-level renames and qualification.
	ast.Inspect(d.node, func(n ast.Node) bool {
		id, ok := n.(*ast.Ident)
		if !ok || covered[id] {
			return true
		}
		obj := info.Uses[id]
		isDef := false
		if obj == nil {
			obj = info.Defs[id]
			isDef = true
		}
		if obj == nil || obj.Pkg() == nil || obj.Pkg().Path() != s.pkg.PkgPath {
			return true
		}
		key := s.keyOf(obj)
		if key == "" {
			return true
		}
		text := id.Name
		if movedHub && key == s.hubType() && !isDef {
			text = handlersType
		} else {
			if n, ok := s.rename[key]; ok {
				text = n
			}
			isTestDecl := s.isTestKey(key)
			if !isDef && obj.Parent() == scope && !isTestDecl && s.pkgOf(key) != dest {
				q := s.pkgOf(key)
				if q == rootPkg {
					text = s.pkg.Name + "." + text
				} else {
					text = q + "." + text
				}
				used[q] = true
			}
		}
		if text != id.Name {
			edits = append(edits, edit{off(id.Pos()), off(id.End()), text})
		}
		return true
	})

	if dest == rootPkg {
		edits = append(edits, s.rootEdits(d, off)...)
	}

	start, end := off(declStart(d.node)), off(d.node.End())
	sort.SliceStable(edits, func(i, j int) bool {
		if edits[i].start != edits[j].start {
			return edits[i].start < edits[j].start
		}
		return edits[i].end-edits[i].start < edits[j].end-edits[j].start // insertions first
	})
	var b strings.Builder
	prev := start
	for _, e := range edits {
		if e.start < prev {
			continue // nested in an earlier replacement
		}
		b.Write(f.src[prev:e.start])
		b.WriteString(e.text)
		prev = e.end
	}
	b.Write(f.src[prev:end])
	return b.String(), used
}

// rootEdits wires handlers into hub values built in the root: right after
// newServer allocates the hub, and around every other hub literal.
func (s *splitter) rootEdits(d *decl, off func(token.Pos) int) []edit {
	info := s.pkg.TypesInfo
	var edits []edit
	isHubLit := func(e ast.Expr) bool {
		u, ok := e.(*ast.UnaryExpr)
		if !ok || u.Op != token.AND {
			return false
		}
		cl, ok := u.X.(*ast.CompositeLit)
		if !ok {
			return false
		}
		t := info.TypeOf(cl)
		if n, ok := t.(*types.Named); ok && n.Obj().Name() == s.cfg.Hub && n.Obj().Pkg().Path() == s.pkg.PkgPath {
			return true
		}
		return false
	}
	if d.key == "func:newServer" {
		ast.Inspect(d.node, func(n ast.Node) bool {
			as, ok := n.(*ast.AssignStmt)
			if ok && len(as.Rhs) == 1 && isHubLit(as.Rhs[0]) {
				name := as.Lhs[0].(*ast.Ident).Name
				edits = append(edits, edit{off(as.End()), off(as.End()), "\n\t" + name + ".wireHandlers()"})
			}
			return true
		})
		return edits
	}
	if d.key == s.hubType() {
		gd := d.node.(*ast.GenDecl)
		st := gd.Specs[0].(*ast.TypeSpec).Type.(*ast.StructType)
		var fields strings.Builder
		fields.WriteString("\n\t// Handlers for the packages split out of this one; see wireHandlers.\n")
		for _, p := range s.handlerPackages() {
			fmt.Fprintf(&fields, "\t%s *%s.%s\n", p, p, handlersType)
		}
		c := off(st.Fields.Closing)
		edits = append(edits, edit{c, c, fields.String()})
		return edits
	}
	ast.Inspect(d.node, func(n ast.Node) bool {
		if e, ok := n.(ast.Expr); ok && isHubLit(e) {
			edits = append(edits, edit{off(e.Pos()), off(e.Pos()), "wiredServer("}, edit{off(e.End()), off(e.End()), ")"})
		}
		return true
	})
	return edits
}

func (s *splitter) handlerPackages() []string {
	seen := map[string]bool{}
	for _, f := range s.files {
		for _, d := range f.decls {
			if d.isHub && !d.test {
				if p := s.pkgOf(d.key); p != rootPkg {
					seen[p] = true
				}
			}
		}
	}
	var out []string
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func (s *splitter) write() error {
	dir := filepath.Dir(s.files[0].path)
	for _, p := range s.handlerPackages() {
		if s.pkg.Types.Scope().Lookup(p) != nil {
			return fmt.Errorf("package name %s collides with a declaration in the root package", p)
		}
	}
	type outFile struct {
		build  string
		parts  []string
		used   map[string]bool
		source *srcFile
	}
	dests := map[string]map[string]*outFile{} // pkg -> basename -> file
	for _, f := range s.files {
		for _, d := range f.decls {
			for p := range d.homes {
				if p == rootPkg {
					continue
				}
				if dests[p] == nil {
					dests[p] = map[string]*outFile{}
				}
				base := filepath.Base(f.path)
				of := dests[p][base]
				if of == nil {
					of = &outFile{build: f.build, used: map[string]bool{}, source: f}
					dests[p][base] = of
				}
				text, used := s.render(d, p)
				of.parts = append(of.parts, text)
				for q := range used {
					of.used[q] = true
				}
			}
		}
	}
	for p, files := range dests {
		pdir := filepath.Join(dir, p)
		if entries, err := os.ReadDir(pdir); err == nil && len(entries) > 0 {
			return fmt.Errorf("destination %s already exists", pdir)
		}
		if err := os.MkdirAll(pdir, 0o755); err != nil {
			return err
		}
		for base, of := range files {
			var b strings.Builder
			if of.build != "" {
				b.WriteString(of.build + "\n\n")
			}
			fmt.Fprintf(&b, "package %s\n\n", p)
			b.WriteString(importBlock(of.source))
			for _, part := range of.parts {
				b.WriteString("\n" + part + "\n")
			}
			if err := s.writeGo(filepath.Join(pdir, base), []byte(b.String()), s.paths(of.used)); err != nil {
				return err
			}
		}
		if hs := s.handlersFile(p); hs != "" {
			if err := s.writeGo(filepath.Join(pdir, "handlers.go"), []byte(hs), nil); err != nil {
				return err
			}
		}
	}
	// Rewrite the root files in place.
	for _, f := range s.files {
		var b bytes.Buffer
		prev := 0
		used := map[string]bool{}
		kept := 0
		for _, d := range f.decls {
			start, end := f.tf.Offset(declStart(d.node)), f.tf.Offset(d.node.End())
			b.Write(f.src[prev:start])
			if d.homes[rootPkg] {
				text, u := s.render(d, rootPkg)
				b.WriteString(text)
				for q := range u {
					used[q] = true
				}
				kept++
			} else {
				for end < len(f.src) && f.src[end] == '\n' {
					end++
				}
			}
			prev = end
		}
		b.Write(f.src[prev:])
		if kept == 0 {
			if err := os.Remove(f.path); err != nil {
				return err
			}
			continue
		}
		if err := s.writeGo(f.path, b.Bytes(), s.paths(used)); err != nil {
			return err
		}
	}
	if err := s.writeGo(filepath.Join(dir, "handler_wiring.go"), []byte(s.wiringFile()), s.paths(setOf(s.handlerPackages()))); err != nil {
		return err
	}
	return s.rewriteExternal()
}

func setOf(xs []string) map[string]bool {
	out := map[string]bool{}
	for _, x := range xs {
		out[x] = true
	}
	return out
}

func (s *splitter) paths(pkgs map[string]bool) []string {
	var out []string
	for p := range pkgs {
		if p != rootPkg {
			out = append(out, s.pkgPath(p))
		} else {
			out = append(out, s.pkg.PkgPath)
		}
	}
	sort.Strings(out)
	return out
}

func importBlock(f *srcFile) string {
	if len(f.syntax.Imports) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("import (\n")
	for _, imp := range f.syntax.Imports {
		b.WriteString("\t" + string(f.src[f.tf.Offset(imp.Pos()):f.tf.Offset(imp.End())]) + "\n")
	}
	b.WriteString(")\n")
	return b.String()
}

// writeGo adds imports, formats, and drops unused imports.
func (s *splitter) writeGo(path string, src []byte, add []string) error {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		_ = os.WriteFile(path+".broken", src, 0o644)
		return fmt.Errorf("parse %s: %w", path, err)
	}
	for _, p := range add {
		astutil.AddImport(fset, file, p)
	}
	var buf bytes.Buffer
	if err := format.Node(&buf, fset, file); err != nil {
		return fmt.Errorf("format %s: %w", path, err)
	}
	out, err := imports.Process(path, buf.Bytes(), &imports.Options{Comments: true, TabIndent: true, TabWidth: 8})
	if err != nil {
		_ = os.WriteFile(path+".broken", buf.Bytes(), 0o644)
		return fmt.Errorf("imports %s: %w", path, err)
	}
	return os.WriteFile(path, out, 0o644)
}

var srcMarker = regexp.MustCompile(`SRCPKG\.(\w+)`)

// typeString prints t for use in package p, qualifying source-package
// types by their final package.
func (s *splitter) typeString(t types.Type, p string, imps map[string]string) string {
	str := types.TypeString(t, func(pkg *types.Package) string {
		if pkg.Path() == s.pkg.PkgPath {
			return "SRCPKG"
		}
		name := s.importName(pkg)
		imps[pkg.Path()] = name
		return name
	})
	return srcMarker.ReplaceAllStringFunc(str, func(m string) string {
		name := srcMarker.FindStringSubmatch(m)[1]
		key := "type:" + name
		if n, ok := s.rename[key]; ok {
			name = n
		}
		q := s.pkgOf(key)
		if q == p {
			return name
		}
		imps[s.pkgPath(q)] = q
		if q == rootPkg {
			return s.pkg.Name + "." + name
		}
		return q + "." + name
	})
}

// importName reuses the root package's alias for an import when it has one.
func (s *splitter) importName(pkg *types.Package) string {
	for _, f := range s.files {
		for _, imp := range f.syntax.Imports {
			if strings.Trim(imp.Path.Value, `"`) == pkg.Path() && imp.Name != nil {
				return imp.Name.Name
			}
		}
	}
	return pkg.Name()
}

func (s *splitter) handlersFile(p string) string {
	fields := s.hubFields[p]
	calls := s.hubCalls[p]
	if len(fields) == 0 && len(calls) == 0 && !s.hasHubMethods(p) {
		return ""
	}
	hub := s.pkg.Types.Scope().Lookup(s.cfg.Hub).(*types.TypeName)
	st := hub.Type().Underlying().(*types.Struct)
	byName := map[string]*types.Var{}
	for fld := range st.Fields() {
		byName[fld.Name()] = fld
	}
	imps := map[string]string{}
	var body strings.Builder
	for _, f := range sortedKeys(fields) {
		t := s.typeString(byName[f].Type(), p, imps)
		if s.pointer[f] {
			t = "*" + t
		}
		fmt.Fprintf(&body, "\t%s %s\n", exported(f), t)
	}
	for _, m := range sortedKeys(calls) {
		obj := s.methodObj(m)
		sig := obj.Type().(*types.Signature)
		fmt.Fprintf(&body, "\t%s %s\n", exported(keyName(m)), s.typeString(types.NewSignatureType(nil, nil, nil, sig.Params(), sig.Results(), sig.Variadic()), p, imps))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "package %s\n\n", p)
	if len(imps) > 0 {
		b.WriteString("import (\n")
		for _, path := range sortedKeys(setOfKeys(imps)) {
			fmt.Fprintf(&b, "\t%s %q\n", imps[path], path)
		}
		b.WriteString(")\n\n")
	}
	fmt.Fprintf(&b, "// %s holds the server state and cross-package calls this package's\n// handlers use. The server package builds it in wireHandlers and shares\n// mutable server fields by pointer.\ntype %s struct {\n%s}\n", handlersType, handlersType, body.String())
	return b.String()
}

func (s *splitter) hasHubMethods(p string) bool {
	return slices.Contains(s.handlerPackages(), p)
}

func (s *splitter) methodObj(key string) *types.Func {
	name := keyName(key)
	hub := s.pkg.Types.Scope().Lookup(s.cfg.Hub).(*types.TypeName)
	obj, _, _ := types.LookupFieldOrMethod(types.NewPointer(hub.Type()), true, s.pkg.Types, name)
	return obj.(*types.Func)
}

func sortedKeys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func setOfKeys(m map[string]string) map[string]bool {
	out := map[string]bool{}
	for k := range m {
		out[k] = true
	}
	return out
}

func (s *splitter) wiringFile() string {
	var b strings.Builder
	fmt.Fprintf(&b, "package %s\n\n", s.pkg.Name)
	b.WriteString("// wireHandlers builds the Handlers of every package split out of this one.\n")
	b.WriteString("// Mutable fields are shared by pointer so reloads and locks stay global.\n")
	b.WriteString("func (s *Server) wireHandlers() {\n")
	pkgs := s.handlerPackages()
	for _, p := range pkgs {
		fmt.Fprintf(&b, "\ts.%s = &%s.%s{\n", p, p, handlersType)
		for _, f := range sortedKeys(s.hubFields[p]) {
			if s.pointer[f] {
				fmt.Fprintf(&b, "\t\t%s: &s.%s,\n", exported(f), f)
			} else {
				fmt.Fprintf(&b, "\t\t%s: s.%s,\n", exported(f), f)
			}
		}
		b.WriteString("\t}\n")
	}
	for _, p := range pkgs {
		for _, m := range sortedKeys(s.hubCalls[p]) {
			q := s.pkgOf(m)
			if q == rootPkg {
				name := keyName(m)
				if n, ok := s.rename[m]; ok {
					name = n
				}
				fmt.Fprintf(&b, "\ts.%s.%s = s.%s\n", p, exported(keyName(m)), name)
			} else {
				name := keyName(m)
				if n, ok := s.rename[m]; ok {
					name = n
				}
				fmt.Fprintf(&b, "\ts.%s.%s = s.%s.%s\n", p, exported(keyName(m)), q, name)
			}
		}
	}
	b.WriteString("}\n\n")
	b.WriteString("// wiredServer wires a hub built outside newServer, such as in tests.\n")
	b.WriteString("func wiredServer(s *Server) *Server {\n\ts.wireHandlers()\n\treturn s\n}\n")
	return b.String()
}

// rewriteExternal requalifies references from other packages to API that
// moved out of the source package.
func (s *splitter) rewriteExternal() error {
	done := map[string]bool{}
	for _, f := range s.files {
		done[f.path] = true
	}
	var errs []error
	for _, p := range s.all {
		if p.PkgPath == s.pkg.PkgPath {
			continue
		}
		for _, file := range p.Syntax {
			path := p.Fset.Position(file.Pos()).Filename
			if done[path] || !strings.HasSuffix(path, ".go") {
				continue
			}
			done[path] = true
			tf := p.Fset.File(file.Pos())
			src := mustRead(path)
			var edits []edit
			used := map[string]bool{}
			ast.Inspect(file, func(n ast.Node) bool {
				se, ok := n.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				x, ok := se.X.(*ast.Ident)
				if !ok {
					return true
				}
				pn, ok := p.TypesInfo.Uses[x].(*types.PkgName)
				if !ok || pn.Imported().Path() != s.pkg.PkgPath {
					return true
				}
				obj := p.TypesInfo.Uses[se.Sel]
				if obj == nil {
					return true
				}
				key := s.keyOf(obj)
				q := s.pkgOf(key)
				if q == rootPkg {
					return true
				}
				edits = append(edits, edit{tf.Offset(se.Pos()), tf.Offset(se.End()), q + "." + obj.Name()})
				used[q] = true
				return true
			})
			if len(edits) == 0 {
				continue
			}
			sort.Slice(edits, func(i, j int) bool { return edits[i].start < edits[j].start })
			var b bytes.Buffer
			prev := 0
			for _, e := range edits {
				b.Write(src[prev:e.start])
				b.WriteString(e.text)
				prev = e.end
			}
			b.Write(src[prev:])
			if err := s.writeGo(path, b.Bytes(), s.paths(used)); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// move records where a declaration went, for updating documentation that
// cites "path::Symbol" locations.
type move struct {
	OldFile   string `json:"old_file"`
	OldSymbol string `json:"old_symbol"`
	NewFile   string `json:"new_file"`
	NewSymbol string `json:"new_symbol"`
}

func (s *splitter) writeMoves(path string) error {
	var out []move
	for _, f := range s.files {
		base := filepath.Base(f.path)
		for _, d := range f.decls {
			if d.key == "" {
				continue
			}
			keys := []string{d.key}
			if gd, ok := d.node.(*ast.GenDecl); ok {
				keys = groupKeys(gd)
			}
			for _, key := range keys {
				p := s.pkgOf(key)
				if d.test {
					p = rootPkg
					if !d.homes[rootPkg] {
						for h := range d.homes {
							p = h
						}
					}
				}
				oldSym := strings.SplitN(key, ":", 2)[1]
				newSym := oldSym
				if n, ok := s.rename[key]; ok {
					if i := strings.LastIndex(newSym, "."); i >= 0 {
						newSym = newSym[:i+1] + n
					} else {
						newSym = n
					}
				}
				if s.isHubMethod(key) && p != rootPkg {
					name := keyName(key)
					if n, ok := s.rename[key]; ok {
						name = n
					}
					newSym = handlersType + "." + name
				}
				newFile := base
				if p != rootPkg {
					newFile = p + "/" + base
				}
				out = append(out, move{OldFile: base, OldSymbol: oldSym, NewFile: newFile, NewSymbol: newSym})
			}
		}
	}
	data, err := json.Marshal(out)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
