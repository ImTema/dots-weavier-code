package domain

import (
	"fmt"
	"path/filepath"
	"strings"
)

// attach gives every field step of r the best call path, over the run's graph,
// between the seed's function and the step's function: the path trace that
// reflects how the value got there. The value can flow either way along calls
// (an argument goes down, a return comes up), so it tries seed -> step first and
// then step -> seed. Steps in the seed's function, struct fields and steps in
// unindexed projects get no path. Lifecycle steps also get their hops; origin
// steps do not, as only lifecycle hops are followed.
// ponytail: one trace per distinct function, each rebuilding the reverse graph;
// share one reverse graph if large trees get slow.
func (run *fieldRun) attach(r *FieldTrace, seedAt address) {
	// Project names in the trace are relative to the run's folder, and so are the indexes' names.
	warn := func(format string, args ...any) { r.Warnings = append(r.Warnings, fmt.Sprintf(format, args...)) }
	g, err := run.graph(r)
	if err != nil {
		warn("call paths skipped: %v", err)
		return
	}
	seed, err := g.resolve(seedAt, false)
	if err != nil {
		warn("call paths skipped: seed is not in the index (run: dotweavier index %s): %v", filepath.Join(run.q.Dir, seedAt.Project), err)
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
	var walk func(ss []*FieldStep, hops bool)
	walk = func(ss []*FieldStep, hops bool) {
		for _, s := range ss {
			if a, ok := stepAddr(s.Loc, seedAt.Project); ok {
				if n, err := g.resolve(a, false); err == nil {
					if hops {
						s.Hops = hopsOf(g, n)
					}
					if n != seed {
						s.Call = best(n)
					}
				}
			}
			walk(s.Kids, hops)
		}
	}
	walk(r.Origin, false)
	walk(r.Uses, true)
}

// stepAddr turns a step location, [project:]rel/path.go:LINE, into a graph address.
func stepAddr(loc, seedProject string) (address, bool) {
	i := strings.LastIndex(loc, ":")
	if i < 0 {
		return address{}, false
	}
	var line int
	if _, err := fmt.Sscan(loc[i+1:], &line); err != nil {
		return address{}, false
	}
	proj, file := seedProject, loc[:i]
	if p, f, ok := strings.Cut(file, ":"); ok {
		proj, file = p, f
	}
	return address{Project: proj, File: file, Line: line}, true
}
