package domain

import (
	"cmp"
	"container/heap"
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// address is a symbol address (GLOSSARY.md), parsed: project::Name,
// project:file:Name, or [project:]file[:LINE]. File is relative to the project.
type address struct {
	Project, File, Name string
	Line                int
}

// parseAddr parses any form of a symbol address. A two-part address is
// project:file unless its last part is a line. Which parts a caller needs is
// for the caller to check.
func parseAddr(s string) (address, error) {
	if proj, name, ok := strings.Cut(s, "::"); ok {
		if proj == "" || name == "" || strings.Contains(name, ":") {
			return address{}, fmt.Errorf("bad address %q", s)
		}
		return address{Project: proj, Name: name}, nil
	}
	parts := strings.Split(s, ":")
	if len(parts) > 3 || slices.Contains(parts, "") {
		return address{}, fmt.Errorf("bad address %q", s)
	}
	var a address
	if n, err := strconv.Atoi(parts[len(parts)-1]); err == nil && len(parts) > 1 {
		a.Line, parts = n, parts[:len(parts)-1]
	} else if len(parts) == 3 {
		a.Name, parts = parts[2], parts[:2]
	}
	if len(parts) == 2 {
		a.Project, parts = parts[0], parts[1:]
	}
	if len(parts) != 1 || a.Line < 0 {
		return address{}, fmt.Errorf("bad address %q", s)
	}
	a.File = parts[0]
	return a, nil
}

// String is the address as parseAddr reads it back.
func (a address) String() string {
	if a.File == "" {
		return a.Project + "::" + a.Name
	}
	s := a.loc()
	if a.Line > 0 {
		s += ":" + strconv.Itoa(a.Line)
	} else if a.Name != "" {
		s += ":" + a.Name
	}
	return s
}

// loc is the file part: [project:]file.
func (a address) loc() string {
	if a.Project == "" {
		return a.File
	}
	return a.Project + ":" + a.File
}

// fieldAddr parses the address of a field trace's seed, [project:]file.{go,java,kt}:LINE,
// and returns it with the file's language.
func fieldAddr(s string) (address, string, error) {
	a, err := parseAddr(s)
	lang := langOfExt(a.File)
	if err != nil || a.Line == 0 || lang == "" {
		return address{}, "", fmt.Errorf("bad address %q, want [project:]rel/path.{go,java,kt}:LINE", s)
	}
	return a, lang, nil
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

const (
	maxDepth = 25
	popLimit = 5_000_000
)

// trace returns up to k simple paths a->b in rank order, by A* with the exact (guesses, hops) distance to b as heuristic.
// Cancelling ctx stops the search early, returning the paths found so far, not exhausted.
func (g *graph) trace(ctx context.Context, a, b int32, k int) (paths []*state, exhausted bool) {
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
		if pops == popLimit || pops%4096 == 0 && ctx.Err() != nil {
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
