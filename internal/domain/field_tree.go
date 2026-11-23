package domain

import (
	"go/token"
	"go/types"

	"golang.org/x/tools/go/ssa"
)

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
	for st := range t.stores(f) {
		_, used := t.fwd.hits[st.Val]
		if _, origin := t.back.hits[st.Val]; !used && !origin {
			out = append(out, st.Pos())
		}
	}
	return out
}
