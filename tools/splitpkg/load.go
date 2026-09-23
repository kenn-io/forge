package main

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"
)

const rootPkg = ""

// decl is one top-level declaration of the source package (production or
// in-package test), keyed like tools/pkggraph ("func:x", "method:T.m", ...).
type decl struct {
	key    string
	node   ast.Decl
	file   *srcFile
	test   bool
	unit   string // plan unit this declaration belongs to
	homes  map[string]bool
	isHub  bool // method of the hub type
	isTest bool // Test/Benchmark/Fuzz/Example function
	setup  bool // TestMain or init in a test file
}

type srcFile struct {
	path   string
	syntax *ast.File
	tf     *token.File
	src    []byte
	test   bool
	decls  []*decl
	build  string // //go:build line, if any
}

type splitter struct {
	cfg    config
	plan   map[string]string // unit -> label
	fset   *token.FileSet
	pkg    *packages.Package // internal test variant of the source package
	all    []*packages.Package
	files  []*srcFile
	decls  map[string]*decl // key -> decl (first one for duplicated keys)
	byNode map[ast.Decl]*decl
	objKey map[types.Object]string
	// placement results
	prodPkg   map[string]string          // key -> final package ("" = root)
	rename    map[string]string          // key -> new exported name
	hubFields map[string]map[string]bool // package -> hub fields used
	hubCalls  map[string]map[string]bool // package -> hub methods called but living elsewhere
	pointer   map[string]bool            // hub field -> shared by pointer
	imports   map[string]map[string]bool // package -> packages it imports (within the split)
	notes     []string
}

func load(cfg config, pl planFile) (*splitter, error) {
	lc := &packages.Config{Mode: packages.LoadAllSyntax, Tests: true}
	pkgs, err := packages.Load(lc, "./...")
	if err != nil {
		return nil, err
	}
	root, err := packages.Load(&packages.Config{Mode: packages.NeedName}, cfg.Source)
	if err != nil || len(root) != 1 {
		return nil, fmt.Errorf("resolve %s: %v", cfg.Source, err)
	}
	path := root[0].PkgPath
	s := &splitter{cfg: cfg, all: pkgs, decls: map[string]*decl{}, byNode: map[ast.Decl]*decl{}, objKey: map[types.Object]string{}}
	s.plan = map[string]string{}
	for u, label := range pl.Packages {
		if label == "root" {
			continue
		}
		name, ok := cfg.Names[label]
		if !ok {
			return nil, fmt.Errorf("plan label %q has no package name in config", label)
		}
		s.plan[u] = name
	}
	for _, p := range pkgs {
		if p.PkgPath == path && strings.HasSuffix(p.ID, ".test]") && p.Name == root[0].Name {
			s.pkg = p
		}
	}
	if s.pkg == nil {
		return nil, fmt.Errorf("no internal test variant of %s", path)
	}
	for _, p := range pkgs {
		if len(p.Errors) > 0 {
			return nil, fmt.Errorf("load %s: %v", p.ID, p.Errors[0])
		}
	}
	s.fset = s.pkg.Fset
	s.indexObjects()
	for _, f := range s.pkg.Syntax {
		s.addFile(f)
	}
	return s, nil
}

// keyOf names an object of the source package the way pkggraph does, so
// objects from different package variants (test and non-test) match.
func (s *splitter) keyOf(obj types.Object) string {
	if k, ok := s.objKey[obj]; ok {
		return k
	}
	if o, ok := obj.(*types.Func); ok {
		if sig, ok := o.Type().(*types.Signature); ok && sig.Recv() != nil {
			recv := recvTypeName(sig.Recv().Type())
			if recv == "?" {
				return "" // method of an anonymous interface
			}
			return "method:" + recv + "." + o.Name()
		}
		return "func:" + o.Name()
	}
	if v, ok := obj.(*types.Var); ok && v.IsField() {
		return "" // struct fields resolve through objKey
	}
	// Only package-level names; locals share names across the package.
	if obj.Pkg() == nil || obj.Parent() != obj.Pkg().Scope() {
		return ""
	}
	switch o := obj.(type) {
	case *types.TypeName:
		return "type:" + o.Name()
	case *types.Const:
		return "const:" + o.Name()
	case *types.Var:
		return "var:" + o.Name()
	}
	return ""
}

func recvTypeName(t types.Type) string {
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	if n, ok := t.(*types.Named); ok {
		return n.Obj().Name()
	}
	return "?"
}

// indexObjects maps struct fields of source-package types (in every loaded
// variant of the package) to "field:T.f" keys.
func (s *splitter) indexObjects() {
	for _, p := range s.all {
		if p.PkgPath != s.pkg.PkgPath || p.Types == nil {
			continue
		}
		scope := p.Types.Scope()
		for _, name := range scope.Names() {
			tn, ok := scope.Lookup(name).(*types.TypeName)
			if !ok {
				continue
			}
			if st, ok := tn.Type().Underlying().(*types.Struct); ok {
				for f := range st.Fields() {
					s.objKey[f] = "field:" + name + "." + f.Name()
				}
			}
		}
	}
}

func (s *splitter) addFile(f *ast.File) {
	path := s.fset.Position(f.Pos()).Filename
	src := mustRead(path)
	sf := &srcFile{path: path, syntax: f, tf: s.fset.File(f.Pos()), src: src, test: strings.HasSuffix(path, "_test.go")}
	for _, cg := range f.Comments {
		if cg.Pos() > f.Package {
			break
		}
		for _, c := range cg.List {
			if strings.HasPrefix(c.Text, "//go:build") {
				sf.build = c.Text
			}
		}
	}
	info := s.pkg.TypesInfo
	for _, d := range f.Decls {
		key := ""
		switch d := d.(type) {
		case *ast.FuncDecl:
			if d.Name.Name == "init" {
				key = fmt.Sprintf("func:init@%s:%d", filepath.Base(path), s.fset.Position(d.Pos()).Line)
			} else if obj := info.Defs[d.Name]; obj != nil {
				key = s.keyOf(obj)
			}
		case *ast.GenDecl:
			if d.Tok == token.IMPORT {
				continue
			}
			key = genDeclKey(d)
		}
		dd := &decl{key: key, node: d, file: sf, test: sf.test, homes: map[string]bool{}}
		if fd, ok := d.(*ast.FuncDecl); ok {
			if fd.Recv != nil && recvName(fd) == s.cfg.Hub {
				dd.isHub = true
			}
			if fd.Recv == nil {
				switch n := fd.Name.Name; {
				case n == "TestMain" || n == "init":
					dd.setup = sf.test
				case isTestFunc(n):
					dd.isTest = sf.test
				}
			}
		}
		dd.unit = s.unitOf(dd)
		sf.decls = append(sf.decls, dd)
		s.byNode[d] = dd
		keys := []string{key}
		if gd, ok := d.(*ast.GenDecl); ok {
			keys = groupKeys(gd)
		}
		for _, k := range keys {
			if _, dup := s.decls[k]; !dup && k != "" {
				s.decls[k] = dd
			}
		}
	}
	s.files = append(s.files, sf)
	sort.Slice(s.files, func(i, j int) bool { return s.files[i].path < s.files[j].path })
}

func genDeclKey(d *ast.GenDecl) string {
	for _, sp := range d.Specs {
		switch sp := sp.(type) {
		case *ast.TypeSpec:
			return "type:" + sp.Name.Name
		case *ast.ValueSpec:
			kind := "var"
			if d.Tok == token.CONST {
				kind = "const"
			}
			for _, n := range sp.Names {
				if n.Name != "_" {
					return kind + ":" + n.Name
				}
			}
		}
	}
	return ""
}

// unitOf maps a declaration to its partition unit: methods of non-hub types
// belong to their type.
func (s *splitter) unitOf(d *decl) string {
	if fd, ok := d.node.(*ast.FuncDecl); ok && fd.Recv != nil && !d.isHub {
		return "type:" + recvName(fd)
	}
	return d.key
}

func recvName(fd *ast.FuncDecl) string {
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
			return "?"
		}
	}
}

func isTestFunc(name string) bool {
	for _, prefix := range []string{"Test", "Benchmark", "Fuzz", "Example"} {
		if rest, ok := strings.CutPrefix(name, prefix); ok && (rest == "" || rest[0] < 'a' || rest[0] > 'z') {
			return true
		}
	}
	return false
}
