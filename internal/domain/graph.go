// Indexing, storage and path tracing. Throwaway prototype (ticket .scratch/dotweavier-mvp/issues/11).
package domain

import (
	"cmp"
	"container/heap"
	"database/sql"
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
	"time"

	"golang.org/x/tools/go/callgraph/cha"
	"golang.org/x/tools/go/callgraph/vta"
	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"
	_ "modernc.org/sqlite"
)

const maxDepth = 25

type kind uint8

const (
	direct      kind = iota // static call, always sure
	sure                    // interface hop confirmed by VTA
	guess                   // interface hop linked by CHA only (or a closure assumed to run)
	grpcHop                 // client call joined to a server handler on the full method name
	pubsubHop               // publish joined to a subscriber handler on the topic
	pubsubGuess             // publish joined to a subscriber on the proto message type only
	httpHop                 // HTTP request joined to the handler of its method and path (http.go)
)

// link reports whether k joins two ends by a contract (method, route, topic)
// rather than by a call, which is what makes a hop between projects.
func (k kind) link() bool { return k >= grpcHop }

func (k kind) String() string {
	return [...]string{"call", "iface:sure", "iface:guess", "grpc", "pubsub", "pubsub:guess", "http"}[k]
}

type edge struct {
	To     int32
	Kind   kind
	Method string // gRPC full method name, or pubsub topic/type key (pubsub.go)
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

type project struct{ Name, dir, lang string }

// manifests recognise a project folder by its build file (GLOSSARY: Project),
// in order of precedence when a folder has several.
var manifests = []struct{ file, lang string }{
	{"go.mod", "go"},
	{"pom.xml", "jvm"}, {"build.gradle.kts", "jvm"}, {"build.gradle", "jvm"},
	{"settings.gradle.kts", "jvm"}, {"settings.gradle", "jvm"},
}

// langOf is the language of the project rooted at dir, or "" if dir is none.
func langOf(dir string) string {
	for _, m := range manifests {
		if _, err := os.Stat(filepath.Join(dir, m.file)); err == nil {
			return m.lang
		}
	}
	return ""
}

func discover(ws string) ([]project, error) {
	// Absolute, because loaded packages report absolute file names: a relative project
	// dir makes filepath.Rel fail and leaves nodes without a file (path:LINE never resolves).
	ws, err := filepath.Abs(ws)
	if err != nil {
		return nil, err
	}
	var ps []project
	seen := map[string]bool{}
	err = filepath.WalkDir(ws, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && p != ws && (strings.HasPrefix(d.Name(), ".") || d.Name() == "vendor" || d.Name() == "testdata" || d.Name() == "node_modules") {
			return filepath.SkipDir
		}
		if dir := filepath.Dir(p); !d.IsDir() && !seen[dir] && slices.ContainsFunc(manifests, func(m struct{ file, lang string }) bool { return m.file == d.Name() }) {
			seen[dir] = true
			rel, _ := filepath.Rel(ws, dir)
			ps = append(ps, project{filepath.ToSlash(rel), dir, langOf(dir)})
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
	if p.lang == "jvm" {
		return extractJVM(g, p)
	}
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
					httpServes(g, prog, c, nodeOf, keep)
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
	pubsubEnds(g, fns, nodeOf, keep)
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
	// ssa.CreatePackage dereferences Defs[name] for every declared name; go/types
	// leaves it nil for some broken decls (e.g. redeclarations), so drop those.
	undefined := func(id *ast.Ident) bool { return pk.TypesInfo.Defs[id] == nil }
	for _, f := range pk.Syntax {
		f.Decls = slices.DeleteFunc(f.Decls, func(d ast.Decl) bool {
			switch d := d.(type) {
			case *ast.FuncDecl:
				if undefined(d.Name) {
					n++
					return true
				}
			case *ast.GenDecl:
				d.Specs = slices.DeleteFunc(d.Specs, func(s ast.Spec) bool {
					bad := false
					switch s := s.(type) {
					case *ast.ValueSpec:
						bad = slices.ContainsFunc(s.Names, undefined)
					case *ast.TypeSpec:
						bad = undefined(s.Name)
					}
					if bad {
						n++
					}
					return bad
				})
			}
			return false
		})
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

// join turns gRPC client calls and pubsub publishes into hops to every indexed
// handler of the method, topic or message type.
func (g *graph) join() {
	for _, c := range g.Calls {
		k := grpcHop
		switch {
		case strings.HasPrefix(c.Method, topicKey):
			k = pubsubHop
		case strings.HasPrefix(c.Method, typeKey):
			k = pubsubGuess
		case strings.HasPrefix(c.Method, httpKey):
			k = httpHop
		}
		hs := g.Impls[c.Method]
		if k == httpHop { // a route without a method serves them all
			_, path, _ := strings.Cut(strings.TrimPrefix(c.Method, httpKey), " ")
			hs = append(slices.Clip(hs), g.Impls[httpKey+"* "+path]...)
		}
		if len(hs) == 0 && k != pubsubGuess { // an unmatched type says nothing: no external node
			g.link(c.From, g.external(c.Ext+" ["+c.Method+"]"), guess, "")
		}
		method := c.Method
		if k == httpHop {
			method = strings.TrimPrefix(method, httpKey) // the kind already says http
		}
		for _, h := range hs {
			if h != c.From {
				g.link(c.From, h, k, method)
			}
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
	i := strings.LastIndex(rest, ":")
	if i < 0 {
		return address{}, fmt.Errorf("bad address %q", s)
	}
	file, tail := rest[:i], rest[i+1:]
	a := address{Project: proj, File: file}
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
	if k == guess || k == pubsubGuess {
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

// stored is one project's index: its own nodes, edges, gRPC client calls and
// handlers. Projects are joined on gRPC method names only at trace time, so
// re-indexing one project never touches another's file.
type stored struct {
	Project, Dir string
	Built        time.Time
	Graph        graph
}

// One SQLite file per project (ADR 0004). trace still loads whole graphs into
// memory; the tables just make them queryable without dotweavier.
func storeDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".dotweavier", "proto")
}

func storePath(project string) string {
	return filepath.Join(storeDir(), strings.ReplaceAll(project, "/", "__")+".db")
}

const schema = `
CREATE TABLE meta (project TEXT NOT NULL, dir TEXT NOT NULL, built TEXT NOT NULL);
CREATE TABLE nodes (id INTEGER PRIMARY KEY, project TEXT NOT NULL, file TEXT NOT NULL, name TEXT NOT NULL,
	line INTEGER NOT NULL, end_line INTEGER NOT NULL, test INTEGER NOT NULL);
CREATE TABLE edges (src INTEGER NOT NULL REFERENCES nodes(id), dst INTEGER NOT NULL REFERENCES nodes(id),
	kind INTEGER NOT NULL, method TEXT NOT NULL);
CREATE TABLE grpc_calls (src INTEGER NOT NULL REFERENCES nodes(id), method TEXT NOT NULL, ext TEXT NOT NULL);
CREATE TABLE impls (method TEXT NOT NULL, node INTEGER NOT NULL REFERENCES nodes(id));
CREATE INDEX edges_src ON edges(src);
CREATE INDEX nodes_name ON nodes(name);
`

// save writes a fresh database next to the old one and renames it over, so a
// crash never leaves a half-written index.
func save(s *stored) error {
	if err := os.MkdirAll(storeDir(), 0o755); err != nil {
		return err
	}
	tmp := storePath(s.Project) + ".tmp"
	os.Remove(tmp)
	if err := writeDB(tmp, s); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, storePath(s.Project))
}

func writeDB(path string, s *stored) error {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := db.Exec(schema); err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	exec := func(q string, rows func(*sql.Stmt) error) error {
		st, err := tx.Prepare(q)
		if err != nil {
			return err
		}
		defer st.Close()
		return rows(st)
	}
	g := &s.Graph
	steps := []struct {
		q    string
		rows func(*sql.Stmt) error
	}{
		{`INSERT INTO meta VALUES (?, ?, ?)`, func(st *sql.Stmt) error {
			_, err := st.Exec(s.Project, s.Dir, s.Built.Format(time.RFC3339Nano))
			return err
		}},
		{`INSERT INTO nodes VALUES (?, ?, ?, ?, ?, ?, ?)`, func(st *sql.Stmt) error {
			for i, n := range g.Nodes {
				if _, err := st.Exec(i, n.Project, n.File, n.Name, n.Line, n.End, n.Test); err != nil {
					return err
				}
			}
			return nil
		}},
		{`INSERT INTO edges VALUES (?, ?, ?, ?)`, func(st *sql.Stmt) error {
			for from, es := range g.Out {
				for _, e := range es {
					if _, err := st.Exec(from, e.To, e.Kind, e.Method); err != nil {
						return err
					}
				}
			}
			return nil
		}},
		{`INSERT INTO grpc_calls VALUES (?, ?, ?)`, func(st *sql.Stmt) error {
			for _, c := range g.Calls {
				if _, err := st.Exec(c.From, c.Method, c.Ext); err != nil {
					return err
				}
			}
			return nil
		}},
		{`INSERT INTO impls VALUES (?, ?)`, func(st *sql.Stmt) error {
			for m, hs := range g.Impls {
				for _, h := range hs {
					if _, err := st.Exec(m, h); err != nil {
						return err
					}
				}
			}
			return nil
		}},
	}
	for _, s := range steps {
		if err := exec(s.q, s.rows); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func load(path string) (*stored, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	s := &stored{Graph: graph{ids: map[string]int32{}, Impls: map[string][]int32{}}}
	g := &s.Graph
	var built string
	if err := db.QueryRow(`SELECT project, dir, built FROM meta`).Scan(&s.Project, &s.Dir, &built); err != nil {
		return nil, err
	}
	if s.Built, err = time.Parse(time.RFC3339Nano, built); err != nil {
		return nil, err
	}
	// rowid order keeps node numbers and edge order exactly as indexed.
	each := func(q string, scan func(*sql.Rows) error) error {
		rows, err := db.Query(q)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			if err := scan(rows); err != nil {
				return err
			}
		}
		return rows.Err()
	}
	if err := each(`SELECT project, file, name, line, end_line, test FROM nodes ORDER BY id`, func(r *sql.Rows) error {
		var n node
		err := r.Scan(&n.Project, &n.File, &n.Name, &n.Line, &n.End, &n.Test)
		g.Nodes = append(g.Nodes, n)
		return err
	}); err != nil {
		return nil, err
	}
	g.Out = make([][]edge, len(g.Nodes))
	if err := each(`SELECT src, dst, kind, method FROM edges ORDER BY rowid`, func(r *sql.Rows) error {
		var from int32
		var e edge
		if err := r.Scan(&from, &e.To, &e.Kind, &e.Method); err != nil {
			return err
		}
		g.Out[from] = append(g.Out[from], e)
		return nil
	}); err != nil {
		return nil, err
	}
	if err := each(`SELECT src, method, ext FROM grpc_calls ORDER BY rowid`, func(r *sql.Rows) error {
		var c grpcCall
		err := r.Scan(&c.From, &c.Method, &c.Ext)
		g.Calls = append(g.Calls, c)
		return err
	}); err != nil {
		return nil, err
	}
	if err := each(`SELECT method, node FROM impls ORDER BY rowid`, func(r *sql.Rows) error {
		var m string
		var h int32
		err := r.Scan(&m, &h)
		g.Impls[m] = append(g.Impls[m], h)
		return err
	}); err != nil {
		return nil, err
	}
	return s, nil
}

// loadIndexes merges every project's store into one graph; unreadable stores are skipped.
func loadIndexes() (*graph, map[string]bool, error) {
	files, err := filepath.Glob(filepath.Join(storeDir(), "*.db"))
	if err != nil {
		return nil, nil, err
	}
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
	return g, loaded, nil
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
