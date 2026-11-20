package domain

import (
	"fmt"
	"os"
	"strings"
)

// attachCalls gives every field step the best call path, over the indexed graph,
// between the seed's function and the step's function: the path trace that
// reflects how the value got there. The value can flow either way along calls
// (an argument goes down, a return comes up), so it tries seed -> step first and
// then step -> seed. Steps in the seed's function, struct fields and steps in
// unindexed projects get no path.
// ponytail: one trace per distinct function, each rebuilding the reverse graph;
// share one reverse graph if large trees get slow.
func attachCalls(r *FieldTrace, home, seedFile string, seedLine int) {
	g, err := mergedGraph()
	if err != nil {
		fmt.Fprintln(os.Stderr, "call paths skipped:", err)
		return
	}
	seed, err := g.resolve(address{Project: home, File: seedFile, Line: seedLine}, false)
	if err != nil {
		fmt.Fprintf(os.Stderr, "call paths skipped: seed is not in the index (run: dotweavier index %s): %v\n", home, err)
		return
	}
	cache := map[int32]*Path{}
	best := func(n int32) *Path {
		if p, ok := cache[n]; ok {
			return p
		}
		var p *Path
		for _, ab := range [][2]int32{{seed, n}, {n, seed}} {
			if s, _ := g.trace(ab[0], ab[1], 1); len(s) > 0 {
				x := g.path(s[0])
				p = &x
				break
			}
		}
		cache[n] = p
		return p
	}
	var walk func(ss []*FieldStep)
	walk = func(ss []*FieldStep) {
		for _, s := range ss {
			if a, ok := stepAddr(s.Loc, home); ok {
				if n, err := g.resolve(a, false); err == nil && n != seed {
					s.Call = best(n)
				}
			}
			walk(s.Kids)
		}
	}
	walk(r.Origin)
	walk(r.Uses)
}

// stepAddr turns a step location, [project:]rel/path.go:LINE, into a graph address.
func stepAddr(loc, home string) (address, bool) {
	i := strings.LastIndex(loc, ":")
	if i < 0 {
		return address{}, false
	}
	var line int
	if _, err := fmt.Sscan(loc[i+1:], &line); err != nil {
		return address{}, false
	}
	proj, file := home, loc[:i]
	if p, f, ok := strings.Cut(file, ":"); ok {
		proj, file = p, f
	}
	return address{Project: proj, File: file, Line: line}, true
}
