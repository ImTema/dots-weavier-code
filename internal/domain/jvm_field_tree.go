package domain

import "path/filepath"

// place shows a field as Type.field and every other key at the line it flowed at.
// A read that leads nowhere (x == null, x.isBlank()) only describes the value, as Go's bool drop.
func (t *jtrace) place(w *fwalk, k tkey) fplace {
	h := w.hits[k]
	if f, ok := k.(jfield); ok {
		return fplace{field: string(f), typ: t.ftype[f]}
	}
	at := h.at.(jat)
	_, expr := k.(jexpr)
	p := fplace{at: t.line(jref{at.fn, at.n}), fn: at.fn, dead: expr && h.how == fAssign && h.from != nil}
	if at.via.fn != nil {
		p.via = t.line(at.via)
	}
	if v, ok := k.(jvar); ok && v.name != "" {
		p.typ = v.fn.vars[v.name]
	}
	if at.fn.id >= 0 {
		p.fnName = t.jp.g.Nodes[at.fn.id].Name
	} else if at.fn.owner != nil {
		p.fnName = at.fn.owner.name
	}
	return p
}

func (t *jtrace) line(r jref) srcLine {
	return srcLine{filepath.Join(t.jp.p.dir, filepath.FromSlash(r.fn.file.rel)), line(r.n)}
}
