// Throwaway prototype of `dotweavier trace` (ticket .scratch/dotweavier-mvp/issues/11).
// No core/extractor split, no schema; field tracing lives in field.go.
//
//	dotweavier index [--workspace DIR] [project ...]
//	dotweavier trace path -A <addr> -B <addr> [--max-paths 10] [--include-tests]
//	dotweavier trace field [project:]rel/path.go:LINE [--depth N] [--include-tests] [--format text|ui]
//
// Addresses: project::Type.Method, project:rel/path.go:Type.Method, project:rel/path.go:LINE.
package main

import (
	"cmp"
	"container/heap"
	_ "embed"
	"encoding/gob"
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/tools/go/callgraph/cha"
	"golang.org/x/tools/go/callgraph/vta"
	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"
)

const maxDepth = 25

type kind uint8

const (
	direct  kind = iota // static call, always sure
	sure                // interface hop confirmed by VTA
	guess               // interface hop linked by CHA only (or a closure assumed to run)
	grpcHop             // client call joined to a server handler on the full method name
)

func (k kind) String() string {
	return [...]string{"call", "iface:sure", "iface:guess", "grpc"}[k]
}

type edge struct {
	To     int32
	Kind   kind
	Method string // gRPC full method name, for grpcHop
}

type node struct {
	Project, File, Name string
	Line, End           int  // declaration span of top-level functions, for path:LINE addresses
	Test                bool // _test.go or mock/fake package
}

func (n node) String() string {
	if n.File == "" {
		return n.Name
	}
	return n.Project + ":" + n.File + ":" + n.Name
}

type grpcCall struct {
	From   int32
	Method string
	Ext    string // external node name used when no indexed project serves the method
}

type graph struct {
	Nodes []node
	ids   map[string]int32
	Out   [][]edge
	Calls []grpcCall
	Impls map[string][]int32 // full method name -> handler nodes
}

func (g *graph) add(id string, n node) int32 {
	if i, ok := g.ids[id]; ok {
		return i
	}
	i := int32(len(g.Nodes))
	g.ids[id] = i
	g.Nodes = append(g.Nodes, n)
	g.Out = append(g.Out, nil)
	return i
}

// link keeps one edge per (from, to): the most certain kind wins.
func (g *graph) link(from, to int32, k kind, method string) {
	for i, e := range g.Out[from] {
		if e.To == to {
			if k < e.Kind {
				g.Out[from][i] = edge{to, k, method}
			}
			return
		}
	}
	g.Out[from] = append(g.Out[from], edge{to, k, method})
}

func (g *graph) external(name string) int32 {
	return g.add("external|"+name, node{Name: "external: " + name})
}

type project struct{ Name, dir string }

func discover(ws string) ([]project, error) {
	var ps []project
	err := filepath.WalkDir(ws, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && p != ws && (strings.HasPrefix(d.Name(), ".") || d.Name() == "vendor" || d.Name() == "testdata" || d.Name() == "node_modules") {
			return filepath.SkipDir
		}
		if !d.IsDir() && d.Name() == "go.mod" {
			dir := filepath.Dir(p)
			rel, _ := filepath.Rel(ws, dir)
			ps = append(ps, project{filepath.ToSlash(rel), dir})
		}
		return nil
	})
	return ps, err
}

var (
	fullMethodRe = regexp.MustCompile(`^/[A-Za-z_][\w.]*/\w+$`)
	registerRe   = regexp.MustCompile(`^Register\w+Server$`)
)

func constIn(pkg *types.Package, name string) string {
	if pkg == nil {
		return ""
	}
	if c, ok := pkg.Scope().Lookup(name).(*types.Const); ok && c.Val().Kind() == constant.String {
		return constant.StringVal(c.Val())
	}
	return ""
}

// clientMethod recognises a call on a generated gRPC client interface (XClient)
// by the X_Method_FullMethodName const next to it; works on type info alone.
func clientMethod(c *ssa.CallCommon) string {
	n, ok := types.Unalias(c.Value.Type()).(*types.Named)
	if !ok || !strings.HasSuffix(n.Obj().Name(), "Client") {
		return ""
	}
	svc := strings.TrimSuffix(n.Obj().Name(), "Client")
	return constIn(n.Obj().Pkg(), svc+"_"+c.Method.Name()+"_FullMethodName")
}

func display(f *ssa.Function) string {
	if p := f.Parent(); p != nil {
		top := p
		for top.Parent() != nil {
			top = top.Parent()
		}
		name := f.Name()
		return display(top) + name[strings.Index(name, "$"):]
	}
	if recv := f.Signature.Recv(); recv != nil {
		t := recv.Type()
		if p, ok := t.(*types.Pointer); ok {
			t = p.Elem()
		}
		if n, ok := types.Unalias(t).(*types.Named); ok {
			return n.Obj().Name() + "." + f.Name()
		}
	}
	return f.Name()
}

type loadStats struct {
	pkgs, pkgErrs, funcs, illTyped, stubbed, buildPanics int
	load, analyze                                        time.Duration
}

// extract loads one project and adds its functions, call edges, gRPC client calls
// and gRPC handlers to g. Only the project's own packages get bodies; dependencies
// come from export data (type info only), which is the analysis boundary of ticket 10.
func extract(g *graph, p project, tests bool) (st loadStats, err error) {
	prog, own, err := loadSSA(p, tests, &st, nil)
	if err != nil {
		return st, err
	}
	t1 := time.Now()
	isTest := func(f *ssa.Function) bool { return isTestFunc(prog, f) }
	canon := func(f *ssa.Function) *ssa.Function {
		if o := f.Origin(); o != nil {
			return o
		}
		return f
	}
	keep := func(f *ssa.Function) bool {
		return f != nil && f.Pkg != nil && own[f.Pkg.Pkg] && f.Synthetic == "" && f.Pos().IsValid() && (tests || !isTest(f))
	}
	nodeOf := func(f *ssa.Function) int32 {
		f = canon(f)
		id := p.Name + "|" + f.String()
		if i, ok := g.ids[id]; ok {
			return i
		}
		pos := prog.Fset.Position(f.Pos())
		rel, _ := filepath.Rel(p.dir, pos.Filename)
		n := node{Project: p.Name, File: filepath.ToSlash(rel), Name: display(f), Line: pos.Line, Test: isTest(f)}
		if f.Parent() == nil && f.Syntax() != nil {
			n.Line = prog.Fset.Position(f.Syntax().Pos()).Line
			n.End = prog.Fset.Position(f.Syntax().End()).Line
		}
		return g.add(id, n)
	}

	fns := map[*ssa.Function]bool{}
	for f := range ssautil.AllFunctions(prog) {
		if keep(f) {
			fns[f] = true
			nodeOf(f)
		}
	}
	st.funcs = len(fns)

	chaG := cha.CallGraph(prog)
	vtaG := vta.CallGraph(fns, chaG)
	chaG.DeleteSyntheticNodes()
	vtaG.DeleteSyntheticNodes()
	confirmed := map[ssa.CallInstruction]map[*ssa.Function]bool{}
	for _, n := range vtaG.Nodes {
		for _, e := range n.Out {
			if e.Site != nil {
				if confirmed[e.Site] == nil {
					confirmed[e.Site] = map[*ssa.Function]bool{}
				}
				confirmed[e.Site][e.Callee.Func] = true
			}
		}
	}

	linked := map[ssa.CallInstruction]bool{} // invoke sites with at least one in-project target
	for f, n := range chaG.Nodes {
		if !keep(f) {
			continue
		}
		from := nodeOf(f)
		for _, e := range n.Out {
			callee := e.Callee.Func
			if e.Site == nil || !keep(callee) {
				continue
			}
			c := e.Site.Common()
			switch {
			case c.StaticCallee() != nil:
				g.link(from, nodeOf(callee), direct, "")
			case confirmed[e.Site][callee]:
				g.link(from, nodeOf(callee), sure, "")
				linked[e.Site] = true
			case c.IsInvoke():
				g.link(from, nodeOf(callee), guess, "")
				linked[e.Site] = true
				// Function-value calls take VTA targets only: CHA links every
				// same-signature function there, which is pure noise.
			}
		}
	}

	handlersOf := func(t types.Type, iface *types.Named) {
		it, _ := iface.Underlying().(*types.Interface)
		if it == nil {
			return
		}
		svc := strings.TrimSuffix(iface.Obj().Name(), "Server")
		mset := prog.MethodSets.MethodSet(t)
		for m := range it.Methods() {
			fm := constIn(iface.Obj().Pkg(), svc+"_"+m.Name()+"_FullMethodName")
			sel := mset.Lookup(m.Pkg(), m.Name())
			if fm == "" || sel == nil {
				continue
			}
			if h := prog.MethodValue(sel); keep(h) && !slices.Contains(g.Impls[fm], nodeOf(h)) {
				g.Impls[fm] = append(g.Impls[fm], nodeOf(h))
			}
		}
	}

	for f := range fns {
		from := nodeOf(f)
		for _, b := range f.Blocks {
			for _, in := range b.Instrs {
				if mc, ok := in.(*ssa.MakeClosure); ok {
					// A closure passed to a dependency (errgroup.Go, retry.Do, ...) is
					// called outside the boundary; assume it runs.
					if fn := mc.Fn.(*ssa.Function); keep(fn) {
						g.link(from, nodeOf(fn), guess, "")
					}
					continue
				}
				call, ok := in.(ssa.CallInstruction)
				if !ok {
					continue
				}
				c := call.Common()
				name := ""
				if c.IsInvoke() {
					name = c.Method.Name()
					if fm := clientMethod(c); fm != "" {
						iface := types.TypeString(c.Value.Type(), func(p *types.Package) string { return p.Name() })
						g.Calls = append(g.Calls, grpcCall{from, fm, iface + "." + name})
						continue
					}
					if !linked[call] {
						iface := types.TypeString(c.Value.Type(), func(p *types.Package) string { return p.Name() })
						g.link(from, g.external(iface+"."+name), guess, "")
					}
				} else if sc := c.StaticCallee(); sc != nil {
					name = sc.Name()
				}
				switch {
				case name == "Invoke" || name == "NewStream":
					// Generated client code inside the project itself.
					for _, a := range c.Args {
						if k, ok := a.(*ssa.Const); ok && k.Value != nil && k.Value.Kind() == constant.String {
							if s := constant.StringVal(k.Value); fullMethodRe.MatchString(s) {
								g.Calls = append(g.Calls, grpcCall{from, s, "grpc " + s})
							}
						}
					}
				case registerRe.MatchString(name) && len(c.Args) == 2:
					iface, ok := types.Unalias(c.Signature().Params().At(1).Type()).(*types.Named)
					if !ok {
						continue
					}
					if mi, ok := c.Args[1].(*ssa.MakeInterface); ok {
						handlersOf(mi.X.Type(), iface)
						continue
					}
					// Registered value is already an interface: take every own implementer.
					it := iface.Underlying().(*types.Interface)
					for pk := range own {
						for _, nm := range pk.Scope().Names() {
							if tn, ok := pk.Scope().Lookup(nm).(*types.TypeName); ok && !tn.IsAlias() {
								if pt := types.NewPointer(tn.Type()); types.Implements(pt, it) {
									handlersOf(pt, iface)
								}
							}
						}
					}
				}
			}
		}
	}
	st.analyze = time.Since(t1)
	return st, nil
}

// loadSSA loads one project and builds SSA for its own packages. Only they get
// bodies; dependencies come from export data (type info only), which is the
// analysis boundary of ticket 10.
//
// fset may be shared between loads so positions from several projects stay distinct;
// nil gives the load its own.
func loadSSA(p project, tests bool, st *loadStats, fset *token.FileSet) (*ssa.Program, map[*types.Package]bool, error) {
	t0 := time.Now()
	cfg := &packages.Config{
		Fset:  fset,
		Mode:  packages.LoadSyntax,
		Dir:   p.dir,
		Tests: tests,
		Env:   append(os.Environ(), "GOFLAGS=-mod=readonly", "GOWORK=off"),
	}
	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		return nil, nil, err
	}
	for _, pk := range pkgs {
		st.pkgErrs += len(pk.Errors)
	}
	st.pkgs = len(pkgs)
	st.load = time.Since(t0)

	// Like ssautil.Packages, but ill-typed packages are kept: messy targets often
	// have a broken import or two, and dropping the package loses every path through it.
	if len(pkgs) == 0 {
		return nil, nil, fmt.Errorf("no packages")
	}
	prog := ssa.NewProgram(pkgs[0].Fset, ssa.InstantiateGenerics)
	root := map[*packages.Package]bool{}
	for _, pk := range pkgs {
		root[pk] = true
	}
	own := map[*types.Package]bool{}
	var ownSSA []*ssa.Package
	packages.Visit(pkgs, nil, func(pk *packages.Package) {
		if pk.Types == nil {
			return
		}
		if !root[pk] {
			prog.CreatePackage(pk.Types, nil, nil, true)
			return
		}
		if pk.IllTyped {
			st.illTyped++
			st.stubbed += stubBroken(pk)
		}
		own[pk.Types] = true
		ownSSA = append(ownSSA, prog.CreatePackage(pk.Types, pk.Syntax, pk.TypesInfo, true))
	})
	for _, sp := range ownSSA {
		for _, imp := range sp.Pkg.Imports() {
			if prog.Package(imp) == nil { // unresolvable import: go/types made a fake package
				prog.CreatePackage(imp, nil, nil, true)
			}
		}
	}
	for _, sp := range ownSSA {
		func() {
			defer func() {
				if r := recover(); r != nil {
					st.buildPanics++
					fmt.Fprintf(os.Stderr, "  ssa build panic in %s: %v\n", sp.Pkg.Path(), r)
				}
			}()
			sp.Build()
		}()
	}
	return prog, own, nil
}

// isTestFunc reports test code: _test.go files and mock/fake packages.
func isTestFunc(prog *ssa.Program, f *ssa.Function) bool {
	for f.Parent() != nil {
		f = f.Parent()
	}
	path := strings.ToLower(f.Pkg.Pkg.Path())
	return strings.HasSuffix(prog.Fset.Position(f.Pos()).Filename, "_test.go") ||
		strings.Contains(path, "mock") || strings.Contains(path, "fake") ||
		slices.ContainsFunc(strings.Split(path, "/"), func(seg string) bool { // test/, testutil, sessionviewtest
			return strings.HasPrefix(seg, "test") || strings.HasSuffix(seg, "test") && seg != "latest"
		})
}

// stubBroken empties, in memory only, the bodies of functions and drops the
// package-level initializers that use an invalid type (e.g. from an unresolvable
// import), because the SSA builder panics on them. Stubbed functions stay as
// nodes but lose their outgoing calls.
func stubBroken(pk *packages.Package) (n int) {
	invalid := func(root ast.Node) (bad bool) {
		ast.Inspect(root, func(x ast.Node) bool {
			if e, ok := x.(ast.Expr); ok && !bad {
				if tv, ok := pk.TypesInfo.Types[e]; ok && (tv.Type == nil || tv.Type == types.Typ[types.Invalid]) {
					bad = true
				}
			}
			if id, ok := x.(*ast.Ident); ok {
				if pn, ok := pk.TypesInfo.Uses[id].(*types.PkgName); ok && pn.Imported().Scope().Len() == 0 {
					bad = true // selector on an import go/types could not resolve
				}
			}
			return !bad
		})
		return bad
	}
	for _, f := range pk.Syntax {
		for _, d := range f.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok && fd.Body != nil && invalid(fd.Body) {
				fd.Body = &ast.BlockStmt{Lbrace: fd.Body.Lbrace, Rbrace: fd.Body.Rbrace}
				n++
			}
		}
	}
	pk.TypesInfo.InitOrder = slices.DeleteFunc(pk.TypesInfo.InitOrder, func(in *types.Initializer) bool {
		if invalid(in.Rhs) {
			n++
			return true
		}
		return false
	})
	return n
}

// join turns gRPC client calls into grpc hops to every indexed handler of the method.
func (g *graph) join() {
	for _, c := range g.Calls {
		hs := g.Impls[c.Method]
		if len(hs) == 0 {
			g.link(c.From, g.external(c.Ext+" ["+c.Method+"]"), guess, "")
		}
		for _, h := range hs {
			g.link(c.From, h, grpcHop, c.Method)
		}
	}
}

type address struct {
	Project, File, Name string
	Line                int
}

func parseAddr(s string) (address, error) {
	if proj, name, ok := strings.Cut(s, "::"); ok {
		return address{Project: proj, Name: name}, nil
	}
	proj, rest, ok := strings.Cut(s, ":")
	if !ok {
		return address{}, fmt.Errorf("bad address %q", s)
	}
	file, tail, ok := strings.Cut(rest, ".go:")
	if !ok {
		return address{}, fmt.Errorf("bad address %q", s)
	}
	a := address{Project: proj, File: file + ".go"}
	if n, err := strconv.Atoi(tail); err == nil {
		a.Line = n
	} else {
		a.Name = tail
	}
	return a, nil
}

func (g *graph) resolve(a address, tests bool) (int32, error) {
	var hits []int32
	for i, n := range g.Nodes {
		if n.Project != a.Project || (a.File != "" && n.File != a.File) || (n.Test && !tests) {
			continue
		}
		if a.Line > 0 && n.End > 0 && n.Line <= a.Line && a.Line <= n.End ||
			a.Name != "" && (n.Name == a.Name || strings.HasSuffix(n.Name, "."+a.Name)) {
			hits = append(hits, int32(i))
		}
	}
	// path:LINE resolves to the innermost enclosing top-level function.
	slices.SortFunc(hits, func(x, y int32) int {
		return cmp.Compare(g.Nodes[x].End-g.Nodes[x].Line, g.Nodes[y].End-g.Nodes[y].Line)
	})
	switch {
	case len(hits) == 0:
		return 0, fmt.Errorf("no node for %+v", a)
	case len(hits) > 1 && a.Line == 0:
		var s []string
		for _, h := range hits {
			s = append(s, g.Nodes[h].String())
		}
		return 0, fmt.Errorf("ambiguous %+v: %s", a, strings.Join(s, ", "))
	}
	return hits[0], nil
}

type cost struct{ guesses, hops int }

func (c cost) less(d cost) bool {
	return c.guesses < d.guesses || c.guesses == d.guesses && c.hops < d.hops
}

func (k kind) cost() cost {
	if k == guess {
		return cost{1, 1}
	}
	return cost{0, 1}
}

type state struct {
	n      int32
	c, f   cost
	in     edge // edge that entered n
	parent *state
	depth  int
}

type pq []*state

func (q pq) Len() int           { return len(q) }
func (q pq) Less(i, j int) bool { return q[i].f.less(q[j].f) }
func (q pq) Swap(i, j int)      { q[i], q[j] = q[j], q[i] }
func (q *pq) Push(x any)        { *q = append(*q, x.(*state)) }
func (q *pq) Pop() any          { o := *q; x := o[len(o)-1]; *q = o[:len(o)-1]; return x }

const popLimit = 5_000_000

// trace returns up to k simple paths a->b, fewest guesses first then fewest hops.
// It is A* over simple paths; the heuristic is the exact (guesses, hops) distance
// to b on the reverse graph, so paths come out in rank order and search never
// enters nodes that cannot reach b.
func (g *graph) trace(a, b int32, k int) (paths []*state, exhausted bool) {
	rev := make([][]edge, len(g.Nodes))
	for from, es := range g.Out {
		for _, e := range es {
			rev[e.To] = append(rev[e.To], edge{int32(from), e.Kind, ""})
		}
	}
	h := map[int32]cost{b: {}}
	q := &pq{{n: b}}
	for q.Len() > 0 {
		s := heap.Pop(q).(*state)
		if s.c != h[s.n] {
			continue
		}
		for _, e := range rev[s.n] {
			c := cost{s.c.guesses + e.Kind.cost().guesses, s.c.hops + 1}
			if old, ok := h[e.To]; !ok || c.less(old) {
				h[e.To] = c
				heap.Push(q, &state{n: e.To, c: c, f: c})
			}
		}
	}
	if _, ok := h[a]; !ok {
		return nil, true
	}
	q = &pq{{n: a, f: h[a]}}
	for pops := 0; q.Len() > 0; pops++ {
		if pops == popLimit {
			return paths, false
		}
		s := heap.Pop(q).(*state)
		if s.n == b {
			if paths = append(paths, s); len(paths) == k {
				return paths, false
			}
			continue
		}
		if s.depth == maxDepth {
			continue
		}
	next:
		for _, e := range g.Out[s.n] {
			hv, ok := h[e.To]
			if !ok {
				continue
			}
			for p := s; p != nil; p = p.parent {
				if p.n == e.To {
					continue next
				}
			}
			ec := e.Kind.cost()
			c := cost{s.c.guesses + ec.guesses, s.c.hops + 1}
			heap.Push(q, &state{n: e.To, c: c, f: cost{c.guesses + hv.guesses, c.hops + hv.hops}, in: e, parent: s, depth: s.depth + 1})
		}
	}
	return paths, true
}

func (g *graph) format(s *state) string {
	var chain []*state
	for p := s; p != nil; p = p.parent {
		chain = append(chain, p)
	}
	slices.Reverse(chain)
	var b strings.Builder
	sureN, ifaceN := 0, 0
	for i, p := range chain {
		if i > 0 {
			label := p.in.Kind.String()
			if p.in.Kind == grpcHop {
				label += " " + p.in.Method
			}
			fmt.Fprintf(&b, " -%s-> ", label)
			switch p.in.Kind {
			case sure:
				sureN++
				ifaceN++
			case guess:
				ifaceN++
			}
		}
		b.WriteString(g.Nodes[p.n].String())
	}
	if ifaceN > 0 {
		fmt.Fprintf(&b, "  (%d/%d sure)", sureN, ifaceN)
	}
	return b.String()
}

// stored is one project's index: its own nodes, edges, gRPC client calls and
// handlers. Projects are joined on gRPC method names only at trace time, so
// re-indexing one project never touches another's file.
type stored struct {
	Project, Dir string
	Built        time.Time
	Graph        graph
}

// shortcut: gob files instead of per-project SQLite (ADR 0004); fine for a prototype.
func storeDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".dotweavier", "proto")
}

func storePath(project string) string {
	return filepath.Join(storeDir(), strings.ReplaceAll(project, "/", "__")+".gob")
}

func save(s *stored) error {
	if err := os.MkdirAll(storeDir(), 0o755); err != nil {
		return err
	}
	tmp := storePath(s.Project) + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if err := gob.NewEncoder(f).Encode(s); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, storePath(s.Project)) // never leave a half-written index
}

func load(path string) (*stored, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var s stored
	return &s, gob.NewDecoder(f).Decode(&s)
}

// merge appends another project's graph, shifting its node numbers.
func (g *graph) merge(o *graph) {
	base := int32(len(g.Nodes))
	g.Nodes = append(g.Nodes, o.Nodes...)
	for _, es := range o.Out {
		shifted := make([]edge, len(es))
		for i, e := range es {
			e.To += base
			shifted[i] = e
		}
		g.Out = append(g.Out, shifted)
	}
	for _, c := range o.Calls {
		c.From += base
		g.Calls = append(g.Calls, c)
	}
	for m, hs := range o.Impls {
		for _, h := range hs {
			g.Impls[m] = append(g.Impls[m], h+base)
		}
	}
}

// dropTests removes every edge into a test node, so traces never pass through one.
func (g *graph) dropTests() {
	for from, es := range g.Out {
		g.Out[from] = slices.DeleteFunc(es, func(e edge) bool { return g.Nodes[e.To].Test })
	}
}

//go:embed trace-path.html
var uiTemplate string

type uiNode struct {
	Name    string `json:"name"`
	Project string `json:"project"`
	File    string `json:"file"`
	Line    int    `json:"line"`
}

type uiEdge struct {
	S      int    `json:"s"`
	T      int    `json:"t"`
	Kind   string `json:"kind"`
	Method string `json:"method,omitempty"`
}

type uiPath struct {
	Nodes []int    `json:"nodes"`
	Edges []int    `json:"edges"`
	Sure  int      `json:"sure"`
	Iface int      `json:"iface"`
	GRPC  []string `json:"grpc"`
}

// ui renders the union of all paths as one self-contained HTML page: a node or
// edge shared by several paths appears once and lists every path through it.
func (g *graph) ui(paths []*state, a, b string) ([]byte, error) {
	data := struct {
		A        string   `json:"a"`
		B        string   `json:"b"`
		Projects []string `json:"projects"`
		Nodes    []uiNode `json:"nodes"`
		Edges    []uiEdge `json:"edges"`
		Paths    []uiPath `json:"paths"`
	}{A: a, B: b, Nodes: []uiNode{}, Edges: []uiEdge{}, Paths: []uiPath{}}
	nodeIdx := map[int32]int{}
	edgeIdx := map[[2]int]int{}
	nodeOf := func(n int32) int {
		if i, ok := nodeIdx[n]; ok {
			return i
		}
		gn := g.Nodes[n]
		if gn.Project != "" && !slices.Contains(data.Projects, gn.Project) {
			data.Projects = append(data.Projects, gn.Project)
		}
		nodeIdx[n] = len(data.Nodes)
		data.Nodes = append(data.Nodes, uiNode{gn.Name, gn.Project, gn.File, gn.Line})
		return nodeIdx[n]
	}
	for _, s := range paths {
		var chain []*state
		for p := s; p != nil; p = p.parent {
			chain = append(chain, p)
		}
		slices.Reverse(chain)
		up := uiPath{GRPC: []string{}}
		for i, st := range chain {
			to := nodeOf(st.n)
			up.Nodes = append(up.Nodes, to)
			if i == 0 {
				continue
			}
			key := [2]int{up.Nodes[i-1], to}
			if _, ok := edgeIdx[key]; !ok {
				edgeIdx[key] = len(data.Edges)
				data.Edges = append(data.Edges, uiEdge{key[0], key[1], st.in.Kind.String(), st.in.Method})
			}
			up.Edges = append(up.Edges, edgeIdx[key])
			switch st.in.Kind {
			case sure:
				up.Sure++
				up.Iface++
			case guess:
				up.Iface++
			case grpcHop:
				up.GRPC = append(up.GRPC, st.in.Method)
			}
		}
		data.Paths = append(data.Paths, up)
	}
	js, err := json.Marshal(data) // escapes <, > and &, so it is safe inside <script>
	if err != nil {
		return nil, err
	}
	return []byte(strings.Replace(uiTemplate, "/*DATA*/", string(js), 1)), nil
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  dotweavier index [--workspace DIR] [project ...]   (no projects: every go.mod under the workspace)
  dotweavier trace path -A <addr> -B <addr> [--max-paths N] [--include-tests] [--format text|ui]
  dotweavier trace field [project:]rel/path.go:LINE [--workspace DIR] [--depth N] [--include-tests] [--max-projects N] [--format text|ui]`)
	os.Exit(2)
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	start := time.Now()
	switch os.Args[1] {
	case "index":
		runIndex(os.Args[2:])
	case "trace":
		if len(os.Args) < 3 {
			usage()
		}
		switch os.Args[2] {
		case "path":
			runTrace(os.Args[3:])
		case "field":
			runTraceField(os.Args[3:])
		default:
			usage()
		}
	default:
		usage()
	}
	var ru syscall.Rusage
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &ru)
	fmt.Fprintf(os.Stderr, "wall=%s maxRSS=%dMB\n", time.Since(start).Round(time.Millisecond), ru.Maxrss>>20) // darwin: bytes
}

// shortcut: hard-coded proving ground; the real CLI gets it from `dotweavier init`.
func defaultWorkspace() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "GolandProjects/scoring-workspace")
}

func runIndex(args []string) {
	fl := flag.NewFlagSet("index", flag.ExitOnError)
	ws := fl.String("workspace", defaultWorkspace(), "workspace root")
	_ = fl.Parse(args)
	all, err := discover(*ws)
	must(err)
	want := map[string]bool{}
	for _, p := range fl.Args() {
		want[p] = true
	}
	filter := len(want) > 0
	for _, p := range all {
		if filter && !want[p.Name] {
			continue
		}
		delete(want, p.Name)
		g := &graph{ids: map[string]int32{}, Impls: map[string][]int32{}}
		// Tests are always indexed and flagged; trace drops them unless --include-tests.
		st, err := extract(g, p, true)
		if err != nil {
			fmt.Fprintf(os.Stderr, "index %s: %v\n", p.Name, err)
			continue
		}
		must(save(&stored{Project: p.Name, Dir: p.dir, Built: time.Now(), Graph: *g}))
		fmt.Fprintf(os.Stderr, "indexed %-28s pkgs=%d pkgErrors=%d illTyped=%d stubbedFuncs=%d ssaPanics=%d funcs=%d load=%s analyze=%s\n",
			p.Name, st.pkgs, st.pkgErrs, st.illTyped, st.stubbed, st.buildPanics, st.funcs, st.load.Round(time.Millisecond), st.analyze.Round(time.Millisecond))
	}
	for p := range want {
		must(fmt.Errorf("project %q not found under %s", p, *ws))
	}
}

func runTrace(args []string) {
	fl := flag.NewFlagSet("trace", flag.ExitOnError)
	aFlag := fl.String("A", "", "start symbol address")
	bFlag := fl.String("B", "", "end symbol address")
	maxPaths := fl.Int("max-paths", 10, "number of paths to print")
	tests := fl.Bool("include-tests", false, "include test nodes (_test.go, mock/fake packages)")
	format := fl.String("format", "text", "text (one line per path) or ui (galaxy view written to ~/.dotweavier/ui/trace-path.html)")
	_ = fl.Parse(args)

	addrA, err := parseAddr(*aFlag)
	must(err)
	addrB, err := parseAddr(*bFlag)
	must(err)
	files, err := filepath.Glob(filepath.Join(storeDir(), "*.gob"))
	must(err)
	g := &graph{ids: map[string]int32{}, Impls: map[string][]int32{}}
	loaded := map[string]bool{}
	for _, f := range files {
		s, err := load(f)
		if err != nil {
			fmt.Fprintf(os.Stderr, "skip index %s: %v\n", f, err)
			continue
		}
		loaded[s.Project] = true
		g.merge(&s.Graph)
	}
	for _, p := range []string{addrA.Project, addrB.Project} {
		if !loaded[p] {
			must(fmt.Errorf("project %q is not indexed; run: dotweavier index %s", p, p))
		}
	}
	if !*tests {
		g.dropTests()
	}
	g.join()
	nEdges, nGRPC := 0, 0
	for _, es := range g.Out {
		nEdges += len(es)
		for _, e := range es {
			if e.Kind == grpcHop {
				nGRPC++
			}
		}
	}
	fmt.Fprintf(os.Stderr, "loaded %d indexes: nodes=%d edges=%d grpcHops=%d grpcClientCalls=%d servedMethods=%d\n",
		len(loaded), len(g.Nodes), nEdges, nGRPC, len(g.Calls), len(g.Impls))

	a, err := g.resolve(addrA, *tests)
	must(err)
	b, err := g.resolve(addrB, *tests)
	must(err)
	fmt.Fprintf(os.Stderr, "A = %s\nB = %s\n", g.Nodes[a], g.Nodes[b])
	paths, exhausted := g.trace(a, b, *maxPaths)
	switch *format {
	case "ui":
		html, err := g.ui(paths, *aFlag, *bFlag)
		must(err)
		out := writeUI("trace-path.html", html)
		fmt.Printf("%d paths -> file://%s\n", len(paths), out)
	default:
		if len(paths) == 0 {
			fmt.Println("no path")
		}
		for i, p := range paths {
			fmt.Printf("%d. %s\n", i+1, g.format(p))
		}
	}
	if !exhausted && len(paths) < *maxPaths {
		fmt.Fprintf(os.Stderr, "search stopped at %d expansions; more paths may exist\n", popLimit)
	}
}

// writeUI writes a page under ~/.dotweavier/ui and returns its path.
func writeUI(name string, html []byte) string {
	out := filepath.Join(filepath.Dir(storeDir()), "ui", name)
	must(os.MkdirAll(filepath.Dir(out), 0o755))
	must(os.WriteFile(out, html, 0o644))
	return out
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "dotweavier:", err)
		os.Exit(1)
	}
}
