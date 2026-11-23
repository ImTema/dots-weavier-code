// Path tracing: resolving addresses and ranking call paths between two nodes.
package domain

import (
	"cmp"
	"container/heap"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

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

const (
	maxDepth = 25
	popLimit = 5_000_000
)

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
