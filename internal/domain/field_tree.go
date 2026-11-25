package domain

import (
	"cmp"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// The field trace engine both languages share: it runs the two walks, folds their
// keys into source lines and turns those into steps. A fieldLang adapter answers
// what depends on the language: Go over SSA (field_walk.go), Java and Kotlin over
// tree-sitter syntax (jvm_field_walk.go).
type fieldLang interface {
	// taint takes one way into k on w, from the key from; at is the adapter's
	// record of where it happened. It records k with w.add the first time.
	taint(w *fwalk, k, from tkey, how flow, at any)
	// follow taints every key k flows to on w (its sources, backward).
	follow(w *fwalk, k tkey)
	// place is where k shows in w's tree.
	place(w *fwalk, k tkey) fplace
}

// ftrace is one field trace: its adapter, its two walks and what it skipped.
type ftrace struct {
	lang      fieldLang
	fwd, back *fwalk
	warnings  []string
}

func newFtrace(lang fieldLang) ftrace {
	return ftrace{lang: lang, fwd: newFwalk(false), back: newFwalk(true)}
}

type hit struct {
	from tkey
	how  flow
	at   any // the adapter's: token.Pos for Go, jat for the JVM
}

// fwalk is the worklist of one direction.
type fwalk struct {
	back  bool
	hits  map[tkey]hit
	order []tkey // taint order; a key's parent always comes first
	queue []tkey
}

func newFwalk(back bool) *fwalk { return &fwalk{back: back, hits: map[tkey]hit{}} }

// add records k the first time it is reached.
func (w *fwalk) add(k, from tkey, how flow, at any) {
	if _, ok := w.hits[k]; !ok {
		w.hits[k] = hit{from, how, at}
		w.order = append(w.order, k)
		w.queue = append(w.queue, k)
	}
}

// seed starts both walks: fk forward from fat, bk backward from bat. A seed read
// from a field (req.GetResultId(), x.F) is that field's value: every other read
// of the field belongs to the lifecycle too.
func (e *ftrace) seed(fk, bk, read tkey, fat, bat any) {
	e.lang.taint(e.fwd, fk, nil, fSeed, fat)
	e.lang.taint(e.back, bk, nil, fSeed, bat)
	if read != nil {
		e.lang.taint(e.fwd, read, fk, fRead, bat)
	}
}

// run empties both worklists. Cancelling ctx stops it.
func (e *ftrace) run(ctx context.Context) error {
	for _, w := range []*fwalk{e.back, e.fwd} {
		for len(w.queue) > 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
			k := w.queue[0]
			w.queue = w.queue[1:]
			e.lang.follow(w, k)
		}
	}
	return nil
}

type srcLine struct {
	file string // absolute; "" for none
	line int
}

// fplace is where a key shows in a tree: a field, or a source line in a function.
type fplace struct {
	field  string // pkg.Type.Field; "" for a line
	pkg    string // a field's package name, shown before Type.Field
	at     srcLine
	fn     any // the function of the line: equal lines of two functions stay apart
	fnName string
	typ    string
	via    srcLine   // call site, for parameters
	over   []srcLine // stores overwriting a tainted field with an unrelated value
	join   bool      // no line of its own (SSA plumbing): joins the line its value came from
	dead   bool      // only describes the value (x == null): dropped unless it leads somewhere
}

// fnode is one step of a tree: a source line in a function, or a field.
type fnode struct {
	fplace
	how  flow
	kids []*fnode
}

// tree folds a walk's keys into source lines: keys on the same line, or joining
// one, share its node.
func (e *ftrace) tree(w *fwalk) []*fnode {
	places := make(map[tkey]fplace, len(w.order))
	next := map[tkey][]tkey{}
	for _, k := range w.order {
		places[k] = e.lang.place(w, k)
		next[w.hits[k].from] = append(next[w.hits[k].from], k)
	}
	live := map[tkey]bool{}
	for _, k := range slices.Backward(w.order) { // kids come after their parent
		live[k] = !places[k].dead || slices.ContainsFunc(next[k], func(n tkey) bool { return live[n] })
	}
	type lineKey struct {
		fn any
		at srcLine
	}
	at := map[tkey]*fnode{}
	lines := map[lineKey]*fnode{}
	fields := map[string]*fnode{}
	var roots []*fnode
	for _, k := range w.order {
		if !live[k] {
			continue
		}
		h, p := w.hits[k], places[k]
		parent := at[h.from]
		if p.join && parent != nil {
			at[k] = parent
			continue
		}
		var n *fnode
		created := false
		if p.field != "" {
			if n = fields[p.field]; n == nil {
				n = &fnode{fplace: p, how: h.how}
				fields[p.field], created = n, true
			}
		} else {
			lk := lineKey{p.fn, p.at}
			if n = lines[lk]; n == nil {
				n = &fnode{fplace: p, how: h.how}
				lines[lk], created = n, true
			}
		}
		if n.typ == "" {
			n.typ = p.typ
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

// result turns the finished walks into the two trees.
func (e *ftrace) result(p *printer) *FieldTrace {
	r := &FieldTrace{Warnings: e.warnings}
	for _, n := range e.tree(e.back) {
		r.Origin = append(r.Origin, p.step(n))
	}
	for _, n := range e.tree(e.fwd) {
		r.Uses = append(r.Uses, p.step(n))
	}
	return r
}

// printer turns a finished trace into Steps: paths relative to the projects,
// source lines read from disk.
type printer struct {
	seedProject project   // files here show without a project prefix
	projects    []project // innermost first
	src         map[string][]string
}

func newPrinter(seedProject project, projects []project) *printer {
	projects = slices.Clone(projects)
	slices.SortFunc(projects, func(a, b project) int { return cmp.Compare(len(b.dir), len(a.dir)) })
	return &printer{seedProject: seedProject, projects: projects, src: map[string][]string{}}
}

// step converts an fnode; kids are ordered by location, overwrites come last.
func (p *printer) step(n *fnode) *FieldStep {
	s := &FieldStep{Kind: n.how.String(), Type: n.typ}
	if n.field != "" {
		name := n.field
		if i := strings.LastIndex(name[:max(0, strings.LastIndex(name, "."))], "."); i >= 0 {
			name = name[i+1:] // Type.Field
		}
		if n.pkg != "" {
			name = n.pkg + "." + name
		}
		s.Kind, s.Name = "field", name
	} else {
		s.Loc, s.Code, s.Fn = p.where(n.at), p.text(n.at.file, n.at.line), n.fnName
		s.Before, s.After = p.text(n.at.file, n.at.line-1), p.text(n.at.file, n.at.line+1) // one line of context each side
	}
	if n.via.file != "" {
		s.Via = p.where(n.via)
	}
	kids := slices.Clone(n.kids)
	slices.SortStableFunc(kids, func(a, b *fnode) int {
		return cmp.Or(cmp.Compare(p.rel(a.at.file), p.rel(b.at.file)), cmp.Compare(a.at.line, b.at.line))
	})
	for _, k := range kids {
		s.Kids = append(s.Kids, p.step(k))
	}
	for _, o := range n.over {
		s.Kids = append(s.Kids, &FieldStep{Kind: "overwritten", Loc: p.where(o), Code: p.text(o.file, o.line)})
	}
	return s
}

func (p *printer) rel(file string) string {
	for _, pr := range append([]project{p.seedProject}, p.projects...) {
		if r, err := filepath.Rel(pr.dir, file); err == nil && !strings.HasPrefix(r, "..") {
			if pr == p.seedProject {
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

func (p *printer) where(l srcLine) string { return p.rel(l.file) + ":" + strconv.Itoa(l.line) }
