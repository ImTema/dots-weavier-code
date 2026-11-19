// `gofacon trace field`: the full lifecycle of one value, as two trees.
//
//   - origin: where the value comes from, back to literals, parsing, other projects'
//     gRPC clients, ...; each line flows into the one above it.
//   - lifecycle: every local it is copied into, every conversion and transformation,
//     every struct field it is stored in and every place that field is read back;
//     each line flows into the ones below it.
//
// The analysis is field-based taint propagation over SSA, run live (no index). A
// struct field is one location for all objects of the type, keyed by package path,
// type and field name, so a protobuf field written by a gRPC client in one project is
// the same location its server reads in another. Projects that import a reached
// field's package are loaded until nothing new is reached. That over-approximates by
// design: the trees show where the value may come from and go, not where it must.
package main

import (
	"cmp"
	_ "embed"
	"encoding/json"
	"flag"
	"fmt"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/tools/go/callgraph/cha"
	"golang.org/x/tools/go/callgraph/vta"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"
)

type flow uint8

const (
	fSeed      flow = iota
	fAssign         // copied: local variable, phi, load, store to a local or global
	fConvert        // type conversion, same value
	fTransform      // derived: result of a call outside the project, arithmetic, concatenation
	fArg            // passed as an argument into a parameter
	fReturn         // returned to a caller
	fStore          // stored into a struct field
	fRead           // read from a struct field (or its protobuf getter)
	fContainer      // put into or taken out of a slice, array, map or channel
	fCapture        // captured by a closure
	fCallback       // guessed: handed by a dependency (lo.Map, ...) to a project callback
)

func (f flow) String() string {
	return [...]string{"seed", "assign", "convert", "transform", "arg", "return", "store", "read", "container", "capture", "callback?"}[f]
}

// A taint key is an ssa.Value or a fieldKey (one location for every object).
type tkey any

type fieldKey string // pkgpath.Type.Field

func (k fieldKey) pkg() string {
	s := string(k)
	s = s[:strings.LastIndex(s, ".")] // drop the field
	return s[:max(0, strings.LastIndex(s, "."))]
}

type hit struct {
	from tkey
	how  flow
	pos  token.Pos // where the flow happened
}

// tracker holds the loaded projects; the two walks share it.
type tracker struct {
	fset       *token.FileSet
	tests      bool
	modules    []string // workspace module paths: their structs' fields are shared locations
	own        map[*types.Package]bool
	fields     map[fieldKey]*types.Var  // for the field's type and package name
	fieldUses  map[fieldKey][]ssa.Value // *ssa.FieldAddr, *ssa.Field and getter *ssa.Call
	globalUses map[*ssa.Global][]ssa.Instruction
	closures   map[*ssa.Function][]*ssa.MakeClosure
	callees    map[ssa.CallInstruction][]*ssa.Function
	callers    map[*ssa.Function][]ssa.CallInstruction
	fwd, back  *walk
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

// keep reports project functions with bodies, including synthetic wrappers of
// project methods (a value-receiver method called through a pointer or interface).
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
	// VTA targets; an interface call VTA cannot resolve (the implementation is wired
	// where VTA does not see it) takes every CHA target, as trace path's iface:guess.
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
		slices.SortFunc(fs, func(a, b *ssa.Function) int { return cmp.Or(byPos(a.Pos(), b.Pos()), cmp.Compare(a.String(), b.String())) })
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

// byPos orders by file and offset, which stays stable whatever order projects
// load into the shared FileSet.
func (t *tracker) byPos(a, b token.Pos) int {
	pa, pb := t.fset.Position(a), t.fset.Position(b)
	return cmp.Or(cmp.Compare(pa.Filename, pb.Filename), cmp.Compare(pa.Offset, pb.Offset))
}

// getter recognises x.GetF() outside the project on a struct with field F (protobuf
// getters): it reads x.F, which is how a gRPC server reads what a client wrote.
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

// fieldOf names the field selected from a struct or pointer to struct. Only named
// structs declared in the workspace are shared locations (ok); a field of a library
// struct (uuid.NullUUID.UUID, ...) or an anonymous one stays part of its object,
// otherwise every UUID in a project would meet in uuid.NullUUID.UUID.
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

func fnOf(k tkey) *ssa.Function {
	switch v := k.(type) {
	case ssa.Instruction:
		return v.Parent()
	case *ssa.Parameter:
		return v.Parent()
	case *ssa.FreeVar:
		return v.Parent()
	}
	return nil
}

// useful drops values that only describe the datum (bools, errors) and contexts:
// log.WithFields(ctx, "id", id) would otherwise drag every ctx downstream along.
func useful(t types.Type) bool {
	if p, ok := t.Underlying().(*types.Pointer); ok { // a local or captured variable
		t = p.Elem()
	}
	if b, ok := t.Underlying().(*types.Basic); ok && b.Info()&types.IsBoolean != 0 {
		return false
	}
	if n, ok := types.Unalias(t).(*types.Named); ok && n.Obj().Pkg() != nil && n.Obj().Pkg().Path() == "context" {
		return false
	}
	return !types.Identical(t, types.Universe.Lookup("error").Type())
}

// root walks back through pointer conversions to the variable a store writes to.
func root(v ssa.Value) ssa.Value {
	for {
		c, ok := v.(*ssa.ChangeType)
		if !ok {
			return v
		}
		v = c.X
	}
}

func argsOf(in ssa.CallInstruction) []ssa.Value {
	c := in.Common()
	if c.IsInvoke() {
		return append([]ssa.Value{c.Value}, c.Args...) // the receiver is the callee's first param
	}
	return c.Args
}

func (t *tracker) referrers(v ssa.Value) []ssa.Instruction {
	if g, ok := v.(*ssa.Global); ok {
		return t.globalUses[g]
	}
	if rs := v.Referrers(); rs != nil {
		return *rs
	}
	return nil
}

// walk is one direction of propagation. Calls are matched one level deep: a function
// entered from a call site (forward through a parameter, backward through a return)
// leaves only through that site, unless it was reached on its own (seed, field,
// capture), which makes it free to leave through every call site.
type walk struct {
	t       *tracker
	back    bool
	hits    map[tkey]hit
	order   []tkey // taint order; a key's parent always comes first
	queue   []tkey
	entered map[*ssa.Function][]ssa.CallInstruction
	free    map[*ssa.Function]bool
	pend    map[*ssa.Function]map[int]ssa.Value // ways out: forward results, backward params
}

func (t *tracker) newWalk(back bool) *walk {
	return &walk{t: t, back: back, hits: map[tkey]hit{}, entered: map[*ssa.Function][]ssa.CallInstruction{},
		free: map[*ssa.Function]bool{}, pend: map[*ssa.Function]map[int]ssa.Value{}}
}

func (w *walk) taint(k, from tkey, how flow, pos token.Pos) {
	if v, ok := k.(ssa.Value); ok && !useful(v.Type()) {
		return
	}
	if _, ok := w.hits[k]; !ok {
		w.hits[k] = hit{from, how, pos}
		w.order = append(w.order, k)
		w.queue = append(w.queue, k)
	}
	// Checked on every way in, not just the first, so the result does not depend
	// on the order the walk happens to visit things.
	enter, exit := fArg, fReturn
	if w.back {
		enter, exit = fReturn, fArg
	}
	if f := fnOf(k); f != nil && f != fnOf(from) && how != enter && (how != exit || w.free[fnOf(from)]) {
		w.markFree(f)
	}
}

func (w *walk) markFree(f *ssa.Function) {
	if w.free[f] {
		return
	}
	w.free[f] = true
	for _, i := range slices.Sorted(maps.Keys(w.pend[f])) {
		w.leave(f, i, w.pend[f][i])
	}
}

// out records way i out of f and takes it.
func (w *walk) out(f *ssa.Function, i int, v ssa.Value) {
	if w.pend[f] == nil {
		w.pend[f] = map[int]ssa.Value{}
	}
	if _, ok := w.pend[f][i]; !ok {
		w.pend[f][i] = v
		w.leave(f, i, v)
	}
}

func (w *walk) leave(f *ssa.Function, i int, v ssa.Value) {
	sites := w.entered[f]
	if w.free[f] {
		sites = w.t.callers[f]
	}
	for _, s := range sites {
		w.leaveAt(s, i, v)
	}
}

func (w *walk) leaveAt(s ssa.CallInstruction, i int, v ssa.Value) {
	if !w.back {
		w.result(s, i, v, fReturn)
	} else if args := argsOf(s); i < len(args) {
		w.taint(args[i], v, fArg, s.Pos())
	}
}

// enter marks f as entered from s; ways out found earlier are taken through s too.
// shortcut: they may belong to another parameter (forward) or result (backward);
// per-parameter summaries if it gets noisy.
func (w *walk) enter(f *ssa.Function, s ssa.CallInstruction) {
	if slices.Contains(w.entered[f], s) {
		return
	}
	w.entered[f] = append(w.entered[f], s)
	for _, i := range slices.Sorted(maps.Keys(w.pend[f])) {
		w.leaveAt(s, i, w.pend[f][i])
	}
}

func (w *walk) run() {
	for len(w.queue) > 0 {
		k := w.queue[0]
		w.queue = w.queue[1:]
		switch k := k.(type) {
		case fieldKey:
			if w.back {
				w.fieldBack(k)
			} else {
				w.field(k)
			}
		case ssa.Value:
			if w.back {
				w.valueBack(k)
			} else {
				w.value(k)
			}
		}
	}
}

// requeueFields revisits reached fields after a project was added: it has new uses.
func (w *walk) requeueFields() {
	for _, k := range w.order {
		if f, ok := k.(fieldKey); ok {
			w.queue = append(w.queue, f)
		}
	}
}

// field taints every read of a tainted struct field.
func (w *walk) field(f fieldKey) {
	for _, u := range w.t.fieldUses[f] {
		if fa, ok := u.(*ssa.FieldAddr); ok {
			for _, r := range *fa.Referrers() {
				switch r := r.(type) {
				case *ssa.UnOp:
					w.taint(r, f, fRead, fa.Pos())
				case ssa.CallInstruction: // &x.F passed on, e.g. a pointer-receiver method
					w.taint(fa, f, fRead, fa.Pos())
				}
			}
			continue
		}
		w.taint(u, f, fRead, u.Pos())
	}
}

// fieldBack taints every value stored into a field.
func (w *walk) fieldBack(f fieldKey) {
	for _, u := range w.t.fieldUses[f] {
		if fa, ok := u.(*ssa.FieldAddr); ok {
			for _, r := range *fa.Referrers() {
				if st, ok := r.(*ssa.Store); ok && st.Addr == fa {
					w.taint(st.Val, f, fStore, st.Pos())
				}
			}
		}
	}
}

func (w *walk) value(v ssa.Value) {
	for _, in := range w.t.referrers(v) {
		pos := in.Pos()
		switch in := in.(type) {
		case *ssa.Store:
			if in.Val != v {
				continue // a write through a tainted pointer overwrites it
			}
			switch a := in.Addr.(type) {
			case *ssa.FieldAddr:
				if k, _, ok := w.t.fieldOf(a.X.Type(), a.Field); ok {
					w.taint(k, v, fStore, pos)
				} else {
					w.taint(root(a.X), v, fContainer, pos) // the object carries it
				}
			case *ssa.IndexAddr:
				w.taint(root(a.X), v, fContainer, pos)
			default:
				w.taint(root(a), v, fAssign, pos)
			}
		case *ssa.UnOp:
			switch in.Op {
			case token.MUL:
				w.taint(in, v, fAssign, pos)
			case token.ARROW:
				w.taint(in, v, fContainer, pos)
			default:
				w.taint(in, v, fTransform, pos)
			}
		case *ssa.ChangeType, *ssa.Convert, *ssa.ChangeInterface, *ssa.MakeInterface,
			*ssa.SliceToArrayPointer, *ssa.MultiConvert, *ssa.TypeAssert:
			w.taint(in.(ssa.Value), v, fConvert, pos)
		case *ssa.Phi, *ssa.Slice, *ssa.Extract:
			w.taint(in.(ssa.Value), v, fAssign, pos)
		case *ssa.Index:
			if in.X == v {
				w.taint(in, v, fContainer, pos)
			}
		case *ssa.IndexAddr:
			if in.X == v {
				w.taint(in, v, fContainer, pos)
			}
		case *ssa.Lookup:
			if in.X == v {
				w.taint(in, v, fContainer, pos)
			}
		case *ssa.FieldAddr:
			if _, _, ok := w.t.fieldOf(in.X.Type(), in.Field); !ok {
				w.taint(in, v, fContainer, pos)
			}
		case *ssa.Field:
			if _, _, ok := w.t.fieldOf(in.X.Type(), in.Field); !ok {
				w.taint(in, v, fContainer, pos)
			}
		case *ssa.Range:
			w.taint(in, v, fContainer, pos)
		case *ssa.Next:
			w.taint(in, v, fContainer, pos)
		case *ssa.MapUpdate:
			if in.Key == v || in.Value == v {
				w.taint(root(in.Map), v, fContainer, pos)
			}
		case *ssa.Send:
			if in.X == v {
				w.taint(root(in.Chan), v, fContainer, pos)
			}
		case *ssa.BinOp:
			w.taint(in, v, fTransform, pos)
		case *ssa.MakeClosure:
			fn := in.Fn.(*ssa.Function)
			for i, b := range in.Bindings {
				if b == v && i < len(fn.FreeVars) {
					w.taint(fn.FreeVars[i], v, fCapture, pos)
				}
			}
		case *ssa.Return:
			for i, r := range in.Results {
				if r == v {
					w.out(in.Parent(), i, v)
				}
			}
		case ssa.CallInstruction:
			w.call(in, v)
		}
	}
}

func (w *walk) call(in ssa.CallInstruction, v ssa.Value) {
	c := in.Common()
	if !c.IsInvoke() && c.Value == v {
		return // calling a tainted function value moves no data
	}
	args := argsOf(in)
	if fs := w.t.callees[in]; len(fs) > 0 {
		for _, f := range fs {
			for i, a := range args {
				if a == v && i < len(f.Params) {
					w.taint(f.Params[i], v, fArg, in.Pos())
				}
			}
			w.enter(f, in)
		}
		return
	}
	// Outside the project: assume the result is derived from the arguments.
	if b, ok := c.Value.(*ssa.Builtin); ok {
		switch b.Name() {
		case "append":
			w.result(in, -1, v, fContainer)
		case "copy":
			if args[1] == v {
				w.taint(root(args[0]), v, fContainer, in.Pos())
			}
		}
		return
	}
	w.result(in, -1, v, fTransform)
	w.callbacks(in, args, v)
}

// result taints result i of a call, or every result when i < 0.
func (w *walk) result(in ssa.CallInstruction, i int, from ssa.Value, how flow) {
	call, ok := in.(*ssa.Call)
	if !ok {
		return // go and defer drop results
	}
	if _, tuple := call.Type().(*types.Tuple); !tuple {
		if i <= 0 {
			w.taint(call, from, how, call.Pos())
		}
		return
	}
	for _, r := range *call.Referrers() {
		if ex, ok := r.(*ssa.Extract); ok && (ex.Index == i || i < 0) {
			w.taint(ex, from, how, call.Pos())
		}
	}
}

// callbacks guesses that a dependency hands the tainted value (or its elements, as
// lo.Map does) to a project function passed in the same call.
func (w *walk) callbacks(in ssa.CallInstruction, args []ssa.Value, v ssa.Value) {
	vt := v.Type()
	var elem types.Type
	switch u := vt.Underlying().(type) {
	case *types.Slice:
		elem = u.Elem()
	case *types.Array:
		elem = u.Elem()
	case *types.Map:
		elem = u.Elem()
	case *types.Chan:
		elem = u.Elem()
	}
	for _, a := range args {
		var fn *ssa.Function
		switch a := a.(type) {
		case *ssa.MakeClosure:
			fn = a.Fn.(*ssa.Function)
		case *ssa.Function:
			fn = a
		}
		if !w.t.keep(fn) {
			continue
		}
		for _, p := range fn.Params {
			if types.Identical(p.Type(), vt) || elem != nil && types.Identical(p.Type(), elem) {
				w.taint(p, v, fCallback, in.Pos())
			}
		}
	}
}

// valueBack taints the values v is computed from.
func (w *walk) valueBack(v ssa.Value) {
	switch v := v.(type) {
	case *ssa.Parameter:
		f := v.Parent()
		if i := slices.Index(f.Params, v); i >= 0 {
			w.out(f, i, v)
		}
	case *ssa.FreeVar:
		f := v.Parent()
		i := slices.Index(f.FreeVars, v)
		for _, mc := range w.t.closures[f] {
			w.taint(mc.Bindings[i], v, fCapture, mc.Pos())
		}
	case *ssa.Global, *ssa.Alloc, *ssa.MakeMap, *ssa.MakeSlice, *ssa.MakeChan:
		w.storesTo(v)
	case *ssa.UnOp:
		switch {
		case v.Op != token.MUL:
			w.taint(v.X, v, fTransform, token.NoPos)
		default:
			w.taint(root(v.X), v, fAssign, token.NoPos)
		}
	case *ssa.FieldAddr:
		w.readBack(v, v.X, v.Field)
	case *ssa.Field:
		w.readBack(v, v.X, v.Field)
	case *ssa.ChangeType:
		w.taint(v.X, v, fConvert, token.NoPos)
	case *ssa.Convert:
		w.taint(v.X, v, fConvert, token.NoPos)
	case *ssa.ChangeInterface:
		w.taint(v.X, v, fConvert, token.NoPos)
	case *ssa.MakeInterface:
		w.taint(v.X, v, fConvert, token.NoPos)
	case *ssa.SliceToArrayPointer:
		w.taint(v.X, v, fConvert, token.NoPos)
	case *ssa.MultiConvert:
		w.taint(v.X, v, fConvert, token.NoPos)
	case *ssa.TypeAssert:
		w.taint(v.X, v, fConvert, token.NoPos)
	case *ssa.Phi:
		for _, e := range v.Edges {
			w.taint(e, v, fAssign, token.NoPos)
		}
	case *ssa.BinOp:
		w.taint(v.X, v, fTransform, token.NoPos)
		w.taint(v.Y, v, fTransform, token.NoPos)
	case *ssa.Slice:
		w.taint(v.X, v, fAssign, token.NoPos)
	case *ssa.Index:
		w.taint(v.X, v, fContainer, token.NoPos)
	case *ssa.IndexAddr:
		w.taint(v.X, v, fContainer, token.NoPos)
	case *ssa.Lookup:
		w.taint(v.X, v, fContainer, token.NoPos)
	case *ssa.Range:
		w.taint(v.X, v, fContainer, token.NoPos)
	case *ssa.Next:
		w.taint(v.Iter, v, fContainer, token.NoPos)
	case *ssa.Extract:
		if call, ok := v.Tuple.(*ssa.Call); ok {
			w.callBack(call, v.Index, v)
		} else {
			w.taint(v.Tuple, v, fAssign, token.NoPos)
		}
	case *ssa.Call:
		w.callBack(v, 0, v)
	}
}

// readBack goes from a field read to the field's stores, or for a library struct to
// its object.
func (w *walk) readBack(v, x ssa.Value, i int) {
	if k, _, ok := w.t.fieldOf(x.Type(), i); ok {
		w.taint(k, v, fRead, v.Pos())
	} else {
		w.taint(root(x), v, fContainer, token.NoPos)
	}
}

// storesTo taints everything written into a variable, global or container.
func (w *walk) storesTo(v ssa.Value) {
	for _, in := range w.t.referrers(v) {
		switch in := in.(type) {
		case *ssa.Store:
			if in.Addr == v {
				w.taint(in.Val, v, fAssign, in.Pos())
			}
		case *ssa.ChangeType: // an alias of the variable: (*T)(&x)
			w.storesTo(in)
		case *ssa.IndexAddr, *ssa.FieldAddr: // literal elements, library struct fields
			if fa, ok := in.(*ssa.FieldAddr); ok {
				if _, _, shared := w.t.fieldOf(fa.X.Type(), fa.Field); shared {
					continue
				}
			}
			for _, r := range *in.(ssa.Value).Referrers() {
				if st, ok := r.(*ssa.Store); ok && st.Addr == in.(ssa.Value) {
					w.taint(st.Val, v, fContainer, st.Pos())
				}
			}
		case *ssa.MapUpdate:
			w.taint(in.Value, v, fContainer, in.Pos())
		case *ssa.Send:
			w.taint(in.X, v, fContainer, in.Pos())
		}
	}
}

// callBack taints what result i of a call is computed from.
func (w *walk) callBack(call *ssa.Call, i int, from ssa.Value) {
	if fs := w.t.callees[call]; len(fs) > 0 {
		for _, f := range fs {
			for _, b := range f.Blocks {
				if r, ok := b.Instrs[len(b.Instrs)-1].(*ssa.Return); ok && i < len(r.Results) {
					w.taint(r.Results[i], from, fReturn, r.Pos())
				}
			}
			w.enter(f, call)
		}
		return
	}
	if k, _, ok := w.t.getter(&call.Call); ok {
		w.taint(k, from, fRead, call.Pos())
		return
	}
	if b, ok := call.Call.Value.(*ssa.Builtin); ok {
		if b.Name() == "append" {
			for _, a := range call.Call.Args {
				w.taint(a, from, fContainer, token.NoPos)
			}
		}
		return
	}
	// A call outside the project derives its result from its receiver (id.String())
	// or its one data argument (uuid.Parse(s)). With several inputs the call itself is
	// the origin: geouuid.NewV8WithCountry(createdAt, country) makes a new ID.
	args := argsOf(call)
	if sc := call.Call.StaticCallee(); call.Call.IsInvoke() || sc != nil && sc.Signature.Recv() != nil {
		args = args[:min(1, len(args))]
	}
	var data []ssa.Value
	for _, a := range args {
		switch a.(type) {
		case *ssa.Function, *ssa.Const:
			continue
		}
		if useful(a.Type()) {
			data = append(data, a)
		}
	}
	if len(data) == 1 {
		w.taint(data[0], from, fTransform, token.NoPos)
	}
}

// seeds returns the values first computed on file:line: value-producing instructions
// whose operands come from elsewhere (so `x := a.Get(); x != ""` seeds the call only),
// and parameters declared there. Addresses and constants are skipped.
func (t *tracker) seeds(prog *ssa.Program, file string, line int) []ssa.Value {
	on := func(p token.Pos) bool {
		if !p.IsValid() {
			return false
		}
		ps := t.fset.Position(p)
		return ps.Line == line && ps.Filename == file
	}
	cand := map[ssa.Value]bool{}
	var order []ssa.Value
	for f := range ssautil.AllFunctions(prog) {
		if !t.keep(f) || t.fset.Position(f.Pos()).Filename != file {
			continue
		}
		for _, p := range f.Params {
			if on(p.Pos()) {
				cand[p] = true
				order = append(order, p)
			}
		}
		for _, b := range f.Blocks {
			for _, in := range b.Instrs {
				v, ok := in.(ssa.Value)
				if !ok || !on(in.Pos()) || !useful(v.Type()) {
					continue
				}
				switch v.(type) {
				case *ssa.Alloc, *ssa.FieldAddr, *ssa.IndexAddr:
					continue
				}
				cand[v] = true
				order = append(order, v)
			}
		}
	}
	slices.SortStableFunc(order, func(a, b ssa.Value) int { return t.byPos(a.Pos(), b.Pos()) })
	var out []ssa.Value
	for _, v := range order {
		derived := false
		if in, ok := v.(ssa.Instruction); ok {
			for _, op := range in.Operands(nil) {
				derived = derived || cand[*op]
			}
		}
		if !derived {
			out = append(out, v)
		}
	}
	return out
}

// tnode is one step of a tree: a source line in a function, or a field.
type tnode struct {
	file  string // absolute
	line  int
	fn    *ssa.Function
	field fieldKey
	how   flow
	typ   types.Type
	via   token.Pos // call site, for parameters
	kids  []*tnode
	over  []token.Pos // stores overwriting a tainted field with an unrelated value
}

// tree folds a walk's keys into source lines: keys on the same line (or without a
// position, like SSA plumbing) join the line their value came from.
func (w *walk) tree() []*tnode {
	at := map[tkey]*tnode{}
	type lineKey struct {
		fn   *ssa.Function
		file string
		line int
	}
	lines := map[lineKey]*tnode{}
	fields := map[fieldKey]*tnode{}
	var roots []*tnode
	for _, k := range w.order {
		h := w.hits[k]
		parent := at[h.from]
		var n *tnode
		created := false
		if f, ok := k.(fieldKey); ok {
			n = fields[f]
			if n == nil {
				n = &tnode{field: f, how: h.how, typ: w.t.fields[f].Type()}
				if !w.back {
					n.over = w.t.overwrites(f)
				}
				fields[f] = n
				created = true
			}
		} else {
			v := k.(ssa.Value)
			pos := h.pos
			if p, ok := v.(*ssa.Parameter); ok && p.Pos().IsValid() {
				pos = p.Pos() // show a parameter at its declaration, the call site goes to via
			}
			if !pos.IsValid() {
				pos = v.Pos()
			}
			if (!pos.IsValid() || fnOf(v) != nil && fnOf(v).Synthetic != "") && parent != nil {
				at[k] = parent // SSA plumbing and method wrappers have no line of their own
				continue
			}
			ps := w.t.fset.Position(pos)
			lk := lineKey{fnOf(v), ps.Filename, ps.Line}
			n = lines[lk]
			if n == nil {
				n = &tnode{file: ps.Filename, line: ps.Line, fn: fnOf(v), how: h.how}
				if _, ok := v.(*ssa.Parameter); ok && h.pos != pos {
					n.via = h.pos
				}
				lines[lk] = n
				created = true
			}
			if typ := v.Type(); n.typ == nil && useful(typ) {
				if _, tuple := typ.(*types.Tuple); !tuple {
					switch v.(type) {
					case *ssa.Alloc, *ssa.Global:
						typ = typ.Underlying().(*types.Pointer).Elem()
					}
					n.typ = typ
				}
			}
		}
		at[k] = n
		switch {
		case !created: // reached again: the tree keeps the first way in
		case parent == nil:
			roots = append(roots, n)
		default:
			parent.kids = append(parent.kids, n)
		}
	}
	return roots
}

// overwrites lists stores into f of values in neither tree: places where the
// tracked value is replaced, e.g. `if app.ResultID == nil { app.ResultID = NewResultID() }`.
func (t *tracker) overwrites(f fieldKey) []token.Pos {
	var out []token.Pos
	for _, u := range t.fieldUses[f] {
		fa, ok := u.(*ssa.FieldAddr)
		if !ok {
			continue
		}
		for _, r := range *fa.Referrers() {
			if st, ok := r.(*ssa.Store); ok && st.Addr == fa {
				_, used := t.fwd.hits[st.Val]
				if _, origin := t.back.hits[st.Val]; !used && !origin {
					out = append(out, st.Pos())
				}
			}
		}
	}
	return out
}

type printer struct {
	w        io.Writer
	t        *tracker
	home     project   // files here print without a project prefix
	projects []project // innermost first
	depth    int
	src      map[string][]string
	steps    int
}

func (p *printer) rel(file string) string {
	for _, pr := range append([]project{p.home}, p.projects...) {
		if r, err := filepath.Rel(pr.dir, file); err == nil && !strings.HasPrefix(r, "..") {
			if pr == p.home {
				return filepath.ToSlash(r)
			}
			return pr.Name + ":" + filepath.ToSlash(r)
		}
	}
	return file
}

func (p *printer) text(file string, line int) string {
	ls, ok := p.src[file]
	if !ok {
		b, _ := os.ReadFile(file)
		ls = strings.Split(string(b), "\n")
		p.src[file] = ls
	}
	if line < 1 || line > len(ls) {
		return ""
	}
	return strings.TrimSpace(ls[line-1])
}

func (p *printer) where(pos token.Pos) string {
	ps := p.t.fset.Position(pos)
	return p.rel(ps.Filename) + ":" + strconv.Itoa(ps.Line)
}

func qual(pkg *types.Package) string { return pkg.Name() }

// fieldName shows a field as pkg.Type.Field.
func (p *printer) fieldName(k fieldKey) string {
	s := string(k)
	name := s[strings.LastIndex(s[:strings.LastIndex(s, ".")], ".")+1:] // Type.Field
	if v := p.t.fields[k]; v.Pkg() != nil {
		name = v.Pkg().Name() + "." + name
	}
	return name
}

func (p *printer) label(n, parent *tnode) string {
	if n.field != "" {
		return fmt.Sprintf("[field] %s : %s", p.fieldName(n.field), types.TypeString(n.typ, qual))
	}
	s := fmt.Sprintf("[%s] %s:%d  %s", n.how, p.rel(n.file), n.line, p.text(n.file, n.line))
	if n.typ != nil {
		s += "  : " + types.TypeString(n.typ, qual)
	}
	if n.via.IsValid() {
		s += "  <- called at " + p.where(n.via)
	}
	if n.fn != nil && (parent == nil || parent.fn != n.fn) {
		s += "  in " + display(n.fn)
	}
	return s
}

func (p *printer) print(n, parent *tnode, prefix string, last bool, depth int) {
	p.steps++
	branch, next := "├─ ", "│  "
	if last {
		branch, next = "└─ ", "   "
	}
	if parent == nil {
		branch, next = "", ""
	}
	fmt.Fprintln(p.w, prefix+branch+p.label(n, parent))
	if p.depth > 0 && depth >= p.depth {
		if len(n.kids) > 0 {
			fmt.Fprintf(p.w, "%s%s… %d more steps below (raise --depth)\n", prefix, next, count(n)-1)
		}
		return
	}
	kids := slices.Clone(n.kids)
	slices.SortStableFunc(kids, func(a, b *tnode) int {
		return cmp.Or(cmp.Compare(p.rel(a.file), p.rel(b.file)), cmp.Compare(a.line, b.line))
	})
	for i, k := range kids {
		p.print(k, n, prefix+next, i == len(kids)-1 && len(n.over) == 0, depth+1)
	}
	for i, o := range n.over {
		b := "├─ "
		if i == len(n.over)-1 {
			b = "└─ "
		}
		ps := p.t.fset.Position(o)
		fmt.Fprintf(p.w, "%s%s[overwritten] %s  %s\n", prefix+next, b, p.where(o), p.text(ps.Filename, ps.Line))
	}
}

func count(n *tnode) int {
	c := 1
	for _, k := range n.kids {
		c += count(k)
	}
	return c
}

// lifecycle prints both trees of a finished trace.
func (p *printer) lifecycle() {
	fmt.Fprintln(p.w, "== origin: each line flows into the one above it ==")
	for _, r := range p.t.back.tree() {
		p.print(r, nil, "", true, 0)
	}
	fmt.Fprintln(p.w, "\n== lifecycle: each line flows into the ones below it ==")
	for _, r := range p.t.fwd.tree() {
		p.print(r, nil, "", true, 0)
	}
}

//go:embed trace-field.html
var fieldUITemplate string

// uiStep is one tnode for the HTML view; overwrites become "overwritten" kids.
type uiStep struct {
	Kind string    `json:"kind"`
	Name string    `json:"name,omitempty"` // field
	Loc  string    `json:"loc,omitempty"`
	Code string    `json:"code,omitempty"`
	Type string    `json:"type,omitempty"`
	Fn   string    `json:"fn,omitempty"`
	Via  string    `json:"via,omitempty"`
	Kids []*uiStep `json:"kids,omitempty"`
}

func (p *printer) uiStep(n *tnode) *uiStep {
	s := &uiStep{Kind: n.how.String()}
	if n.field != "" {
		s.Kind, s.Name = "field", p.fieldName(n.field)
	} else {
		s.Loc, s.Code = p.rel(n.file)+":"+strconv.Itoa(n.line), p.text(n.file, n.line)
		if n.fn != nil {
			s.Fn = display(n.fn)
		}
	}
	if n.typ != nil {
		s.Type = types.TypeString(n.typ, qual)
	}
	if n.via.IsValid() {
		s.Via = p.where(n.via)
	}
	for _, k := range n.kids {
		s.Kids = append(s.Kids, p.uiStep(k))
	}
	for _, o := range n.over {
		ps := p.t.fset.Position(o)
		s.Kids = append(s.Kids, &uiStep{Kind: "overwritten", Loc: p.where(o), Code: p.text(ps.Filename, ps.Line)})
	}
	return s
}

// ui renders both trees as one page: origin grows left of the seed, uses right.
func (p *printer) ui(addr string) ([]byte, error) {
	data := struct {
		Addr   string    `json:"addr"`
		Open   int       `json:"open,omitempty"` // levels open at first; the page defaults to 6
		Origin []*uiStep `json:"origin"`
		Uses   []*uiStep `json:"uses"`
	}{Addr: addr, Open: p.depth}
	for _, r := range p.t.back.tree() {
		data.Origin = append(data.Origin, p.uiStep(r))
	}
	for _, r := range p.t.fwd.tree() {
		data.Uses = append(data.Uses, p.uiStep(r))
	}
	js, err := json.Marshal(data) // escapes <, > and &, so it is safe inside <script>
	if err != nil {
		return nil, err
	}
	return []byte(strings.Replace(fieldUITemplate, "/*DATA*/", string(js), 1)), nil
}

// resolveFile finds the project and absolute path of [project:]rel/path.go.
func resolveFile(projects []project, s string) (project, string, error) {
	name, file, ok := strings.Cut(s, ":")
	if !ok {
		name, file = "", s
	}
	var hits []project
	for _, p := range projects {
		if name != "" && p.Name != name {
			continue
		}
		if _, err := os.Stat(filepath.Join(p.dir, file)); err == nil {
			hits = append(hits, p)
		}
	}
	if abs, err := filepath.Abs(file); name == "" && len(hits) == 0 && err == nil {
		if _, err := os.Stat(abs); err == nil { // absolute or cwd-relative: innermost project wins
			for _, p := range projects {
				if strings.HasPrefix(abs, p.dir+string(filepath.Separator)) {
					hits = append(hits, p)
				}
			}
			slices.SortFunc(hits, func(a, b project) int { return cmp.Compare(len(b.dir), len(a.dir)) })
			if len(hits) > 0 {
				return hits[0], abs, nil
			}
		}
	}
	switch len(hits) {
	case 0:
		return project{}, "", fmt.Errorf("no project has %s", s)
	case 1:
		return hits[0], filepath.Join(hits[0].dir, file), nil
	}
	var names []string
	for _, h := range hits {
		names = append(names, h.Name)
	}
	return project{}, "", fmt.Errorf("%s is in several projects (%s); prefix it with project:", s, strings.Join(names, ", "))
}

// importers maps each import path to the projects whose non-test code imports it.
// Nested projects are scanned on their own, not as part of the outer one.
func importers(projects []project) map[string][]project {
	roots := map[string]bool{}
	for _, p := range projects {
		roots[p.dir] = true
	}
	out := map[string][]project{}
	fset := token.NewFileSet()
	for _, p := range projects {
		seen := map[string]bool{}
		_ = filepath.WalkDir(p.dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if path != p.dir && (roots[path] || strings.HasPrefix(d.Name(), ".") || d.Name() == "vendor" || d.Name() == "testdata") {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
			if err != nil {
				return nil
			}
			for _, im := range f.Imports {
				if ip, err := strconv.Unquote(im.Path.Value); err == nil && !seen[ip] {
					seen[ip] = true
					out[ip] = append(out[ip], p)
				}
			}
			return nil
		})
	}
	return out
}

// traceField runs both walks from file:line in home, loading further projects that
// import a reached field's package, up to maxProjects in total.
func traceField(projects []project, home project, file string, line int, tests bool, maxProjects int) (*tracker, error) {
	var modules []string
	moduleOf := map[string]string{} // project name -> module path
	for _, p := range projects {
		if b, err := os.ReadFile(filepath.Join(p.dir, "go.mod")); err == nil {
			for l := range strings.Lines(string(b)) {
				if m, ok := strings.CutPrefix(strings.TrimSpace(l), "module "); ok {
					m = strings.Trim(strings.TrimSpace(m), `"`)
					modules = append(modules, m)
					moduleOf[p.Name] = m
					break
				}
			}
		}
	}
	t := newTracker(token.NewFileSet(), tests, modules)
	loaded := map[string]bool{home.Name: true}
	// Loads run one after another: concurrent loads measured slower (12.1s vs 11.4s),
	// go list contends on the go command's locks and is mostly system time.
	loadAll := func(ps []project) []*ssa.Program {
		progs := make([]*ssa.Program, len(ps))
		for i, p := range ps {
			var st loadStats
			prog, own, err := loadSSA(p, tests, &st, t.fset)
			if err != nil {
				fmt.Fprintf(os.Stderr, "skip project %s: %v\n", p.Name, err)
				continue
			}
			t0 := time.Now()
			t.add(prog, own)
			progs[i] = prog
			fmt.Fprintf(os.Stderr, "loaded %s: pkgs=%d load=%s analyze=%s\n", p.Name, st.pkgs, st.load.Round(time.Millisecond), time.Since(t0).Round(time.Millisecond))
		}
		return progs
	}
	// The workspace import scan only matters once fields are reached; run it meanwhile.
	impsC := make(chan map[string][]project, 1)
	go func() { impsC <- importers(projects) }()
	prog := loadAll([]project{home})[0]
	if prog == nil {
		return nil, fmt.Errorf("project %s did not load", home.Name)
	}
	seeds := t.seeds(prog, file, line)
	if len(seeds) == 0 {
		return nil, fmt.Errorf("no value computed at %s:%d in project %s", file, line, home.Name)
	}
	for _, s := range seeds {
		t.fwd.taint(s, nil, fSeed, s.Pos())
		t.back.taint(s, nil, fSeed, s.Pos())
		// A seed read from a field (req.GetResultId(), x.F) is that field's value:
		// every other read of the field belongs to the lifecycle too.
		if k, ok := t.readOf(s); ok {
			t.fwd.taint(k, s, fRead, s.Pos())
		}
	}

	// Fields of packages shared with other projects (protobuf messages, SDKs) are
	// followed into the projects that import them, until no new project is reached.
	var imps map[string][]project
	asked := map[string]bool{}
	for {
		t.back.run()
		t.fwd.run()
		var next []project
		for _, w := range []*walk{t.back, t.fwd} {
			for _, k := range w.order {
				f, ok := k.(fieldKey)
				if !ok || asked[f.pkg()] {
					continue
				}
				asked[f.pkg()] = true
				if imps == nil {
					imps = <-impsC
				}
				for _, p := range imps[f.pkg()] {
					// The module declaring the package (generated protobuf, SDK types)
					// only defines it; its getters are already read as fields.
					m := moduleOf[p.Name]
					if m != "" && (f.pkg() == m || strings.HasPrefix(f.pkg(), m+"/")) {
						continue
					}
					if !loaded[p.Name] && !slices.Contains(next, p) {
						next = append(next, p)
					}
				}
			}
		}
		var batch []project
		for _, p := range next {
			if len(loaded) >= maxProjects {
				fmt.Fprintf(os.Stderr, "not loaded (--max-projects %d): %s\n", maxProjects, p.Name)
				continue
			}
			loaded[p.Name] = true
			batch = append(batch, p)
		}
		if !slices.ContainsFunc(loadAll(batch), func(p *ssa.Program) bool { return p != nil }) {
			return t, nil
		}
		t.back.requeueFields()
		t.fwd.requeueFields()
	}
}

func runTraceField(args []string) {
	fl := flag.NewFlagSet("trace field", flag.ExitOnError)
	ws := fl.String("workspace", defaultWorkspace(), "workspace root")
	depth := fl.Int("depth", 0, "text: print at most N levels of each tree (0: all); ui: levels open at first (0: 6)")
	tests := fl.Bool("include-tests", false, "include test code (_test.go, mock/fake packages)")
	maxProjects := fl.Int("max-projects", 10, "load at most N projects when following fields shared across projects")
	format := fl.String("format", "text", "text (two trees) or ui (tree view written to ~/.gofacon/ui/trace-field.html)")
	// Accept the address before or after the flags.
	var pos []string
	for len(args) > 0 {
		_ = fl.Parse(args)
		if fl.NArg() == 0 {
			break
		}
		pos = append(pos, fl.Arg(0))
		args = fl.Args()[1:]
	}
	if len(pos) != 1 {
		usage()
	}
	addr := pos[0]
	i := strings.LastIndex(addr, ":")
	line, err := strconv.Atoi(addr[i+1:])
	if i < 0 || err != nil || !strings.HasSuffix(addr[:i], ".go") {
		must(fmt.Errorf("bad address %q, want [project:]rel/path.go:LINE", addr))
	}
	projects, err := discover(*ws)
	must(err)
	home, file, err := resolveFile(projects, addr[:i])
	must(err)
	t, err := traceField(projects, home, file, line, *tests, *maxProjects)
	must(err)
	slices.SortFunc(projects, func(a, b project) int { return cmp.Compare(len(b.dir), len(a.dir)) })
	pr := &printer{w: os.Stdout, t: t, home: home, projects: projects, depth: *depth, src: map[string][]string{}}
	switch *format {
	case "ui":
		html, err := pr.ui(addr)
		must(err)
		fmt.Printf("lifecycle of %s -> file://%s\n", addr, writeUI("trace-field.html", html))
	default:
		pr.lifecycle()
		fmt.Fprintf(os.Stderr, "%d steps\n", pr.steps)
	}
}
