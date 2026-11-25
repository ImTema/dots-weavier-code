package domain

import (
	"go/token"
	"go/types"
	"iter"
	"maps"
	"slices"

	"golang.org/x/tools/go/ssa"
)

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

// first is an instruction's first operand: X of a conversion or an index, Iter of a Next.
func first(in any) ssa.Value { return *in.(ssa.Instruction).Operands(nil)[0] }

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
	*fwalk
	t       *tracker
	entered map[*ssa.Function][]ssa.CallInstruction
	free    map[*ssa.Function]bool
	pend    map[*ssa.Function]map[int]ssa.Value // ways out: forward results, backward params
}

func (t *tracker) newWalk(w *fwalk) *walk {
	return &walk{fwalk: w, t: t, entered: map[*ssa.Function][]ssa.CallInstruction{},
		free: map[*ssa.Function]bool{}, pend: map[*ssa.Function]map[int]ssa.Value{}}
}

func (t *tracker) taint(w *fwalk, k, from tkey, how flow, at any) {
	t.walks[w].taint(k, from, how, at.(token.Pos))
}

func (t *tracker) follow(w *fwalk, k tkey) { t.walks[w].follow(k) }

func (w *walk) taint(k, from tkey, how flow, pos token.Pos) {
	if v, ok := k.(ssa.Value); ok && !useful(v.Type()) {
		return
	}
	w.add(k, from, how, pos)
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

func (w *walk) follow(k tkey) {
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

// requeueFields revisits reached fields after a project was added: it has new uses.
func requeueFields(w *fwalk) {
	for _, k := range w.order {
		if f, ok := k.(fieldKey); ok {
			w.queue = append(w.queue, f)
		}
	}
}

// place shows a field as pkg.Type.Field and a value at its line; a parameter at
// its declaration, the call site goes to via.
func (t *tracker) place(w *fwalk, k tkey) fplace {
	if f, ok := k.(fieldKey); ok {
		v := t.fields[f]
		p := fplace{field: string(f), typ: types.TypeString(v.Type(), qual)}
		if v.Pkg() != nil {
			p.pkg = v.Pkg().Name()
		}
		if !w.back {
			p.over = t.overwrites(f)
		}
		return p
	}
	v, at := k.(ssa.Value), w.hits[k].at.(token.Pos)
	pos := at
	if p, ok := v.(*ssa.Parameter); ok && p.Pos().IsValid() {
		pos = p.Pos()
	}
	if !pos.IsValid() {
		pos = v.Pos()
	}
	fn := fnOf(v)
	// SSA plumbing and method wrappers have no line of their own.
	p := fplace{at: t.line(pos), fn: fn, join: !pos.IsValid() || fn != nil && fn.Synthetic != ""}
	if fn != nil {
		p.fnName = display(fn)
	}
	if _, ok := v.(*ssa.Parameter); ok && at != pos {
		p.via = t.line(at)
	}
	if typ := v.Type(); useful(typ) {
		if _, tuple := typ.(*types.Tuple); !tuple {
			switch v.(type) {
			case *ssa.Alloc, *ssa.Global:
				typ = typ.Underlying().(*types.Pointer).Elem()
			}
			p.typ = types.TypeString(typ, qual)
		}
	}
	return p
}

func (t *tracker) line(pos token.Pos) srcLine {
	ps := t.fset.Position(pos)
	return srcLine{ps.Filename, ps.Line}
}

func qual(pkg *types.Package) string { return pkg.Name() }

// overwrites lists stores into f of values in neither tree: places where the
// tracked value is replaced, e.g. `if app.ResultID == nil { app.ResultID = NewResultID() }`.
func (t *tracker) overwrites(f fieldKey) []srcLine {
	var out []srcLine
	for st := range t.stores(f) {
		_, used := t.fwd.hits[st.Val]
		if _, origin := t.back.hits[st.Val]; !used && !origin {
			out = append(out, t.line(st.Pos()))
		}
	}
	return out
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
	for st := range w.t.stores(f) {
		w.taint(st.Val, f, fStore, st.Pos())
	}
}

// stores yields the stores into field f.
func (t *tracker) stores(f fieldKey) iter.Seq[*ssa.Store] {
	return func(yield func(*ssa.Store) bool) {
		for _, u := range t.fieldUses[f] {
			if fa, ok := u.(*ssa.FieldAddr); ok {
				for _, r := range *fa.Referrers() {
					if st, ok := r.(*ssa.Store); ok && st.Addr == fa && !yield(st) {
						return
					}
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
		case *ssa.Index, *ssa.IndexAddr, *ssa.Lookup:
			if first(in) == v { // not the index
				w.taint(in.(ssa.Value), v, fContainer, pos)
			}
		case *ssa.FieldAddr:
			if _, _, ok := w.t.fieldOf(in.X.Type(), in.Field); !ok {
				w.taint(in, v, fContainer, pos)
			}
		case *ssa.Field:
			if _, _, ok := w.t.fieldOf(in.X.Type(), in.Field); !ok {
				w.taint(in, v, fContainer, pos)
			}
		case *ssa.Range, *ssa.Next:
			w.taint(in.(ssa.Value), v, fContainer, pos)
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
	case *ssa.ChangeType, *ssa.Convert, *ssa.ChangeInterface, *ssa.MakeInterface,
		*ssa.SliceToArrayPointer, *ssa.MultiConvert, *ssa.TypeAssert:
		w.taint(first(v), v, fConvert, token.NoPos)
	case *ssa.Phi:
		for _, e := range v.Edges {
			w.taint(e, v, fAssign, token.NoPos)
		}
	case *ssa.BinOp:
		w.taint(v.X, v, fTransform, token.NoPos)
		w.taint(v.Y, v, fTransform, token.NoPos)
	case *ssa.Slice:
		w.taint(v.X, v, fAssign, token.NoPos)
	case *ssa.Index, *ssa.IndexAddr, *ssa.Lookup, *ssa.Range, *ssa.Next:
		w.taint(first(v), v, fContainer, token.NoPos)
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
