package domain

import (
	"cmp"
	"slices"
	"strconv"
	"strings"
)

// jnode is one step of a tree: a source line in a function, or a field.
type jnode struct {
	fn       *jvmFunc
	line     int
	field    jfield
	how      flow
	typ, via string
	kids     []*jnode
}

func (w *jwalk) tree() []*jnode {
	at := map[any]*jnode{}
	type lineKey struct {
		fn   *jvmFunc
		file *jvmFile
		line int
	}
	lines := map[lineKey]*jnode{}
	fields := map[jfield]*jnode{}
	var roots []*jnode
	// A read that leads nowhere (x == null, x.isBlank()) only describes the value, as Go's bool drop.
	next := map[any][]any{}
	for _, k := range w.order {
		next[w.hits[k].from] = append(next[w.hits[k].from], k)
	}
	live := map[any]bool{}
	for _, k := range slices.Backward(w.order) { // kids come after their parent
		h := w.hits[k]
		_, expr := k.(jexpr)
		live[k] = !expr || h.how != fAssign || h.from == nil || slices.ContainsFunc(next[k], func(n any) bool { return live[n] })
	}
	for _, k := range w.order {
		h := w.hits[k]
		if !live[k] {
			continue
		}
		parent := at[h.from]
		var n *jnode
		created := false
		if f, ok := k.(jfield); ok {
			if n = fields[f]; n == nil {
				n = &jnode{field: f, how: h.how, typ: w.t.ftype[f]}
				fields[f], created = n, true
			}
		} else {
			lk := lineKey{h.fn, h.fn.file, line(h.at)}
			if n = lines[lk]; n == nil {
				n = &jnode{fn: h.fn, line: lk.line, how: h.how, via: h.via}
				lines[lk], created = n, true
			}
			if v, ok := k.(jvar); ok && n.typ == "" && v.name != "" {
				n.typ = v.fn.vars[v.name]
			}
		}
		at[k] = n
		switch {
		case !created:
		case parent == nil:
			roots = append(roots, n)
		default:
			parent.kids = append(parent.kids, n)
		}
	}
	return roots
}

func (t *jtrace) step(n *jnode) *FieldStep {
	s := &FieldStep{Kind: n.how.String(), Type: n.typ, Via: n.via}
	if n.field != "" {
		name := string(n.field)
		if i := strings.LastIndex(name[:max(0, strings.LastIndex(name, "."))], "."); i >= 0 {
			name = name[i+1:] // Type.field
		}
		s.Kind, s.Name = "field", name
	} else {
		ls := strings.Split(string(n.fn.file.src), "\n")
		text := func(i int) string {
			if i < 1 || i > len(ls) {
				return ""
			}
			return strings.TrimSpace(ls[i-1])
		}
		s.Loc, s.Code, s.Before, s.After = n.fn.file.rel+":"+strconv.Itoa(n.line), text(n.line), text(n.line-1), text(n.line+1)
		if n.fn.id >= 0 {
			s.Fn = t.jp.g.Nodes[n.fn.id].Name
		} else if n.fn.owner != nil {
			s.Fn = n.fn.owner.name
		}
	}
	kids := slices.Clone(n.kids)
	rel := func(n *jnode) string {
		if n.fn == nil {
			return "" // a field
		}
		return n.fn.file.rel
	}
	slices.SortStableFunc(kids, func(a, b *jnode) int { return cmp.Or(cmp.Compare(rel(a), rel(b)), cmp.Compare(a.line, b.line)) })
	for _, k := range kids {
		s.Kids = append(s.Kids, t.step(k))
	}
	return s
}
