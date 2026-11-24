// Indexing: finding projects under a workspace and extracting their call graphs.
package domain

import (
	"context"
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
	"strings"
	"time"

	"golang.org/x/tools/go/callgraph/cha"
	"golang.org/x/tools/go/callgraph/vta"
	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"
)

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
	warnings                                             []string // one line each, e.g. an SSA build panic
}

// extract loads one project and adds its functions, call edges, gRPC client calls
// and gRPC handlers to g. Only the project's own packages get bodies; dependencies
// come from export data (type info only), which is the analysis boundary of ticket 10.
func extract(ctx context.Context, g *graph, p project, tests bool) (st loadStats, err error) {
	if p.lang == "jvm" {
		return extractJVM(g, p)
	}
	prog, own, err := loadSSA(ctx, p, tests, &st, nil)
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
			n := nodeOf(f)
			if key := libFunc(f); key != "" && !isTest(f) {
				g.Impls[key] = append(g.Impls[key], n)
			}
		}
	}
	st.funcs = len(fns)

	chaG := cha.CallGraph(prog)
	vtaG := vta.CallGraph(fns, chaG)
	// Before the synthetic nodes go: a dependency's functions come from export
	// data and count as synthetic, so deleting them drops these targets.
	// An interface call VTA resolves to a dependency's type (an engine built by
	// another project) is a library call; CHA-only targets are not: small
	// interfaces like io.Writer match half of every dependency.
	type libTarget struct {
		site ssa.CallInstruction
		from *ssa.Function
		key  string
	}
	var libTargets []libTarget
	for f, n := range vtaG.Nodes {
		if !keep(f) {
			continue
		}
		for _, e := range n.Out {
			if e.Site == nil || !e.Site.Common().IsInvoke() || keep(e.Callee.Func) {
				continue
			}
			if key := libFunc(e.Callee.Func); key != "" && !own[e.Callee.Func.Object().Pkg()] {
				libTargets = append(libTargets, libTarget{e.Site, f, key})
			}
		}
	}
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

	type libSite struct {
		from int32
		key  string
	}
	libCalled := map[libSite]bool{} // one library call per caller and callee
	libCall := func(from int32, key string) {
		if !libCalled[libSite{from, key}] {
			libCalled[libSite{from, key}] = true
			g.Calls = append(g.Calls, grpcCall{from, key, ""})
		}
	}

	linked := map[ssa.CallInstruction]bool{} // invoke sites with at least one in-project or library target
	for _, t := range libTargets {
		libCall(nodeOf(t.from), t.key)
		linked[t.site] = true
	}
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
					if key := libFunc(sc); key != "" && !own[sc.Object().Pkg()] {
						libCall(from, key)
					}
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
func loadSSA(ctx context.Context, p project, tests bool, st *loadStats, fset *token.FileSet) (*ssa.Program, map[*types.Package]bool, error) {
	t0 := time.Now()
	cfg := &packages.Config{
		Context: ctx,
		Fset:    fset,
		Mode:    packages.LoadSyntax,
		Dir:     p.dir,
		Tests:   tests,
		Env:     append(os.Environ(), "GOFLAGS=-mod=readonly", "GOWORK=off"),
	}
	pkgs, err := packages.Load(cfg, "./...")
	if err == nil {
		err = ctx.Err()
	}
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
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		func() {
			defer func() {
				if r := recover(); r != nil {
					st.buildPanics++
					st.warnings = append(st.warnings, fmt.Sprintf("  ssa build panic in %s: %v", sp.Pkg.Path(), r))
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
