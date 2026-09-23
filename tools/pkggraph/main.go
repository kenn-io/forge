// Command pkggraph writes the declaration-level dependency graph of one Go
// package, including its in-package tests, as JSON.
//
// Nodes are package-level functions, methods, types, struct fields, vars,
// and consts. Edges point from the declaration whose source mentions a
// name to the declaration that defines it, weighted by mention count. The
// graph feeds partitioning analysis for splitting a large package into
// smaller ones.
//
// Usage:
//
//	go run ./tools/pkggraph -pkg ./internal/server -out /tmp/server-graph.json
package main

import (
	"encoding/json/v2"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"
)

type node struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"` // func, method, type, field, var, const
	Name     string `json:"name"`
	Recv     string `json:"recv,omitempty"`
	File     string `json:"file"`
	Line     int    `json:"line"`
	Lines    int    `json:"lines"`
	Test     bool   `json:"test"`
	Exported bool   `json:"exported"`
}

type edge struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Weight int    `json:"weight"`
}

type graph struct {
	Package string  `json:"package"`
	Nodes   []*node `json:"nodes"`
	Edges   []edge  `json:"edges"`
}

func main() {
	pkg := flag.String("pkg", "", "package pattern to graph, for example ./internal/server")
	out := flag.String("out", "", "output JSON path")
	flag.Parse()
	if err := run(*pkg, *out); err != nil {
		fmt.Fprintln(os.Stderr, "pkggraph:", err)
		os.Exit(1)
	}
}

func run(pattern, out string) error {
	if pattern == "" || out == "" {
		return errors.New("-pkg and -out are required")
	}
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedSyntax |
			packages.NeedTypes | packages.NeedTypesInfo,
		Tests: true,
	}
	pkgs, err := packages.Load(cfg, pattern)
	if err != nil {
		return err
	}
	var p *packages.Package
	for _, c := range pkgs {
		if strings.HasSuffix(c.ID, ".test]") && !strings.HasSuffix(c.Name, "_test") && len(c.Syntax) > 0 {
			p = c
		}
	}
	if p == nil {
		return fmt.Errorf("no internal test variant for %s", pattern)
	}
	if len(p.Errors) > 0 {
		return fmt.Errorf("load: %v", p.Errors[0])
	}
	g := build(p)
	data, err := json.Marshal(g)
	if err != nil {
		return err
	}
	return os.WriteFile(out, data, 0o644)
}

type builder struct {
	p      *packages.Package
	nodes  map[string]*node
	objIDs map[types.Object]string
	edges  map[[2]string]int
}

func build(p *packages.Package) *graph {
	b := &builder{p: p, nodes: map[string]*node{}, objIDs: map[types.Object]string{}, edges: map[[2]string]int{}}
	scope := p.Types.Scope()
	// Struct fields of package types become their own nodes so field access
	// on a hub type (for example a server struct) is visible as a dependency.
	for _, name := range scope.Names() {
		tn, ok := scope.Lookup(name).(*types.TypeName)
		if !ok {
			continue
		}
		if st, ok := tn.Type().Underlying().(*types.Struct); ok {
			for f := range st.Fields() {
				b.objIDs[f] = "field:" + name + "." + f.Name()
			}
		}
	}
	for _, f := range p.Syntax {
		for _, d := range f.Decls {
			b.declare(d)
		}
	}
	for _, f := range p.Syntax {
		for _, d := range f.Decls {
			b.connect(d)
		}
	}
	g := &graph{Package: p.PkgPath}
	for _, n := range b.nodes {
		g.Nodes = append(g.Nodes, n)
	}
	sort.Slice(g.Nodes, func(i, j int) bool { return g.Nodes[i].ID < g.Nodes[j].ID })
	for k, w := range b.edges {
		g.Edges = append(g.Edges, edge{From: k[0], To: k[1], Weight: w})
	}
	sort.Slice(g.Edges, func(i, j int) bool {
		if g.Edges[i].From != g.Edges[j].From {
			return g.Edges[i].From < g.Edges[j].From
		}
		return g.Edges[i].To < g.Edges[j].To
	})
	return g
}

func (b *builder) add(id, kind, name, recv string, from, to token.Pos, obj types.Object) {
	start, end := b.p.Fset.Position(from), b.p.Fset.Position(to)
	file := filepath.Base(start.Filename)
	b.nodes[id] = &node{
		ID: id, Kind: kind, Name: name, Recv: recv, File: file,
		Line: start.Line, Lines: end.Line - start.Line + 1,
		Test: strings.HasSuffix(file, "_test.go"), Exported: ast.IsExported(name),
	}
	if obj != nil {
		b.objIDs[obj] = id
	}
}

func (b *builder) declare(d ast.Decl) {
	info := b.p.TypesInfo
	switch d := d.(type) {
	case *ast.FuncDecl:
		obj := info.Defs[d.Name]
		if d.Recv == nil {
			id := "func:" + d.Name.Name
			if d.Name.Name == "init" {
				id = fmt.Sprintf("func:init@%s:%d", filepath.Base(b.p.Fset.Position(d.Pos()).Filename), b.p.Fset.Position(d.Pos()).Line)
			}
			b.add(id, "func", d.Name.Name, "", d.Pos(), d.End(), obj)
			return
		}
		recv := recvName(d)
		b.add("method:"+recv+"."+d.Name.Name, "method", d.Name.Name, recv, d.Pos(), d.End(), obj)
	case *ast.GenDecl:
		for _, s := range d.Specs {
			switch s := s.(type) {
			case *ast.TypeSpec:
				b.add("type:"+s.Name.Name, "type", s.Name.Name, "", s.Pos(), s.End(), info.Defs[s.Name])
				if st, ok := s.Type.(*ast.StructType); ok {
					for _, fl := range st.Fields.List {
						for _, n := range fl.Names {
							b.add("field:"+s.Name.Name+"."+n.Name, "field", n.Name, s.Name.Name, fl.Pos(), fl.End(), info.Defs[n])
						}
						if len(fl.Names) == 0 {
							name := embeddedName(fl.Type)
							b.add("field:"+s.Name.Name+"."+name, "field", name, s.Name.Name, fl.Pos(), fl.End(), nil)
						}
					}
				}
			case *ast.ValueSpec:
				kind := "var"
				if d.Tok == token.CONST {
					kind = "const"
				}
				for _, n := range s.Names {
					if n.Name == "_" {
						continue
					}
					b.add(kind+":"+n.Name, kind, n.Name, "", s.Pos(), s.End(), info.Defs[n])
				}
			}
		}
	}
}

// connect records edges from each declaration (or struct field) to what its
// source mentions in the same package.
func (b *builder) connect(d ast.Decl) {
	info := b.p.TypesInfo
	walk := func(from string, n ast.Node) {
		if from == "" || n == nil {
			return
		}
		ast.Inspect(n, func(n ast.Node) bool {
			id, ok := n.(*ast.Ident)
			if !ok {
				return true
			}
			obj := info.Uses[id]
			if obj == nil || obj.Pkg() != b.p.Types {
				return true
			}
			to, ok := b.objIDs[obj]
			if !ok || to == from {
				return true
			}
			b.edges[[2]string{from, to}]++
			return true
		})
	}
	switch d := d.(type) {
	case *ast.FuncDecl:
		from := b.objIDs[info.Defs[d.Name]]
		if d.Name.Name == "init" {
			from = fmt.Sprintf("func:init@%s:%d", filepath.Base(b.p.Fset.Position(d.Pos()).Filename), b.p.Fset.Position(d.Pos()).Line)
		}
		if d.Recv != nil {
			recv := recvName(d)
			// A method belongs to its receiver type.
			b.edges[[2]string{from, "type:" + recv}]++
			walk(from, d.Recv)
		}
		walk(from, d.Type)
		if d.Body != nil {
			walk(from, d.Body)
		}
	case *ast.GenDecl:
		for _, s := range d.Specs {
			switch s := s.(type) {
			case *ast.TypeSpec:
				from := "type:" + s.Name.Name
				if st, ok := s.Type.(*ast.StructType); ok {
					for _, fl := range st.Fields.List {
						for _, n := range fl.Names {
							fid := "field:" + s.Name.Name + "." + n.Name
							b.edges[[2]string{fid, from}]++
							walk(fid, fl.Type)
						}
						if len(fl.Names) == 0 {
							fid := "field:" + s.Name.Name + "." + embeddedName(fl.Type)
							b.edges[[2]string{fid, from}]++
							walk(fid, fl.Type)
						}
					}
					continue
				}
				walk(from, s.Type)
				if s.TypeParams != nil {
					walk(from, s.TypeParams)
				}
			case *ast.ValueSpec:
				for _, n := range s.Names {
					if n.Name == "_" {
						continue
					}
					from := b.objIDs[info.Defs[n]]
					if s.Type != nil {
						walk(from, s.Type)
					}
					for _, v := range s.Values {
						walk(from, v)
					}
				}
			}
		}
	}
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

func embeddedName(expr ast.Expr) string {
	for {
		switch t := expr.(type) {
		case *ast.StarExpr:
			expr = t.X
		case *ast.SelectorExpr:
			return t.Sel.Name
		case *ast.IndexExpr:
			expr = t.X
		case *ast.Ident:
			return t.Name
		default:
			return "?"
		}
	}
}
