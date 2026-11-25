package domain

import (
	"cmp"
	"go/token"
	"go/types"
	"slices"
	"strings"

	"golang.org/x/tools/go/callgraph/cha"
	"golang.org/x/tools/go/callgraph/vta"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"
)

// tracker holds the loaded projects; the two walks share it.
type tracker struct {
	fset       *token.FileSet
	tests      bool
	modules    []string // module paths of the loaded projects: their structs' fields are shared locations
	own        map[*types.Package]bool
	fields     map[fieldKey]*types.Var  // for the field's type and package name
	fieldUses  map[fieldKey][]ssa.Value // *ssa.FieldAddr, *ssa.Field and getter *ssa.Call
	globalUses map[*ssa.Global][]ssa.Instruction
	closures   map[*ssa.Function][]*ssa.MakeClosure
	callees    map[ssa.CallInstruction][]*ssa.Function
	callers    map[*ssa.Function][]ssa.CallInstruction
	fwd, back  *walk
	seedOK     func(ssa.Value) bool // nil: every value on the line is a seed
	warnings   []string             // projects skipped or not loaded, load stats
}

func newTracker(fset *token.FileSet, tests bool, modules []string) *tracker {
	t := &tracker{
		fset: fset, tests: tests, modules: modules, own: map[*types.Package]bool{},
		fields: map[fieldKey]*types.Var{}, fieldUses: map[fieldKey][]ssa.Value{},
		globalUses: map[*ssa.Global][]ssa.Instruction{}, closures: map[*ssa.Function][]*ssa.MakeClosure{},
		callees: map[ssa.CallInstruction][]*ssa.Function{}, callers: map[*ssa.Function][]ssa.CallInstruction{},
	}
	t.fwd, t.back = t.newWalk(false), t.newWalk(true)
	return t
}

// keep reports project functions with bodies, including synthetic wrappers of project methods.
func (t *tracker) keep(f *ssa.Function) bool {
	if f == nil || len(f.Blocks) == 0 {
		return false
	}
	if f.Pkg == nil {
		o, ok := f.Object().(*types.Func)
		return ok && f.Synthetic != "" && t.own[o.Pkg()]
	}
	return t.own[f.Pkg.Pkg] && (t.tests || !isTestFunc(f.Prog, f))
}

// add indexes one loaded project: field uses, global uses, closures and its call graph.
func (t *tracker) add(prog *ssa.Program, own map[*types.Package]bool) {
	for pk := range own {
		t.own[pk] = true
	}
	fns := map[*ssa.Function]bool{}
	for f := range ssautil.AllFunctions(prog) {
		if t.keep(f) {
			fns[f] = true
		}
	}
	use := func(k fieldKey, v *types.Var, u ssa.Value) {
		if t.fields[k] == nil {
			t.fields[k] = v
		}
		t.fieldUses[k] = append(t.fieldUses[k], u)
	}
	for f := range fns {
		for _, b := range f.Blocks {
			for _, in := range b.Instrs {
				switch in := in.(type) {
				case *ssa.FieldAddr:
					if k, v, ok := t.fieldOf(in.X.Type(), in.Field); ok {
						use(k, v, in)
					}
				case *ssa.Field:
					if k, v, ok := t.fieldOf(in.X.Type(), in.Field); ok {
						use(k, v, in)
					}
				case *ssa.MakeClosure:
					fn := in.Fn.(*ssa.Function)
					t.closures[fn] = append(t.closures[fn], in)
				case *ssa.Call:
					if k, v, ok := t.getter(&in.Call); ok {
						use(k, v, in)
					}
				}
				for _, op := range in.Operands(nil) {
					if g, ok := (*op).(*ssa.Global); ok { // globals have no Referrers
						t.globalUses[g] = append(t.globalUses[g], in)
					}
				}
			}
		}
	}
	// VTA targets, or every CHA target where VTA resolves none, as trace path's iface:guess (ADR 0005).
	chaG := cha.CallGraph(prog)
	link := func(site ssa.CallInstruction, f *ssa.Function) {
		if t.keep(f) && !slices.Contains(t.callees[site], f) {
			t.callees[site] = append(t.callees[site], f)
			t.callers[f] = append(t.callers[f], site)
		}
	}
	for f, n := range vta.CallGraph(fns, chaG).Nodes {
		if fns[f] {
			for _, e := range n.Out {
				if e.Site != nil {
					link(e.Site, e.Callee.Func)
				}
			}
		}
	}
	for f, n := range chaG.Nodes {
		if !fns[f] {
			continue
		}
		for _, e := range n.Out {
			if e.Site != nil && e.Site.Common().IsInvoke() && len(t.callees[e.Site]) == 0 {
				link(e.Site, e.Callee.Func)
			}
		}
	}
	// The lists above were filled in map order; sort them so runs are reproducible.
	byPos := t.byPos
	for _, us := range t.fieldUses {
		slices.SortFunc(us, func(a, b ssa.Value) int { return byPos(a.Pos(), b.Pos()) })
	}
	for _, ss := range t.callers {
		slices.SortFunc(ss, func(a, b ssa.CallInstruction) int { return byPos(a.Pos(), b.Pos()) })
	}
	for _, fs := range t.callees {
		slices.SortFunc(fs, func(a, b *ssa.Function) int {
			return cmp.Or(byPos(a.Pos(), b.Pos()), cmp.Compare(a.String(), b.String()))
		})
	}
	for _, ms := range t.closures {
		slices.SortFunc(ms, func(a, b *ssa.MakeClosure) int { return byPos(a.Pos(), b.Pos()) })
	}
}

// readOf reports the shared field a value is read from, if any.
func (t *tracker) readOf(v ssa.Value) (fieldKey, bool) {
	switch v := v.(type) {
	case *ssa.Call:
		k, _, ok := t.getter(&v.Call)
		return k, ok
	case *ssa.UnOp:
		if fa, ok := v.X.(*ssa.FieldAddr); ok && v.Op == token.MUL {
			k, _, ok := t.fieldOf(fa.X.Type(), fa.Field)
			return k, ok
		}
	case *ssa.Field:
		k, _, ok := t.fieldOf(v.X.Type(), v.Field)
		return k, ok
	}
	return "", false
}

// byPos orders by file and offset, which stays stable whatever order projects load into the shared FileSet.
func (t *tracker) byPos(a, b token.Pos) int {
	pa, pb := t.fset.Position(a), t.fset.Position(b)
	return cmp.Or(cmp.Compare(pa.Filename, pb.Filename), cmp.Compare(pa.Offset, pb.Offset))
}

// getter recognises a protobuf getter x.GetF() outside the project as a read of x.F.
func (t *tracker) getter(c *ssa.CallCommon) (fieldKey, *types.Var, bool) {
	f := c.StaticCallee()
	if f == nil || t.keep(f) || !strings.HasPrefix(f.Name(), "Get") || f.Signature.Recv() == nil || len(c.Args) != 1 {
		return "", nil, false
	}
	recv := f.Signature.Recv().Type()
	if p, ok := recv.Underlying().(*types.Pointer); ok {
		recv = p.Elem()
	}
	st, ok := recv.Underlying().(*types.Struct)
	if !ok {
		return "", nil, false
	}
	for i := range st.NumFields() {
		if st.Field(i).Name() == f.Name()[3:] {
			return t.fieldOf(recv, i)
		}
	}
	return "", nil, false
}

// fieldOf names the field selected from a struct or pointer to struct; ok only for a shared location (ADR 0009).
func (t *tracker) fieldOf(typ types.Type, i int) (k fieldKey, v *types.Var, ok bool) {
	if p, ok := typ.Underlying().(*types.Pointer); ok {
		typ = p.Elem()
	}
	v = typ.Underlying().(*types.Struct).Field(i).Origin()
	n, named := types.Unalias(typ).(*types.Named)
	if !named || n.Obj().Pkg() == nil {
		return "", v, false
	}
	o := n.Origin().Obj()
	path := o.Pkg().Path()
	ok = slices.ContainsFunc(t.modules, func(m string) bool { return path == m || strings.HasPrefix(path, m+"/") })
	return fieldKey(path + "." + o.Name() + "." + v.Name()), v, ok
}
