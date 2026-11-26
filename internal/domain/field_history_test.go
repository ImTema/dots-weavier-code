package domain

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A kept run reads back whole, with the options it ran with, by name or as the newest.
func TestKeptFieldRunRoundTrips(t *testing.T) {
	isolate(t)
	r, err := TraceField(t.Context(), FieldQuery{Dir: "testdata", IncludeTests: true, MaxProjects: 3, Only: "uses", Keep: true}, "fieldcases:main.go:26")
	require.NoError(t, err)
	require.NotEmpty(t, r.Name)
	assert.NotEmpty(t, r.Origin, "a run keeps the tree Only leaves out")
	got, err := LoadFieldRun(r.Name)
	require.NoError(t, err)
	assert.Equal(t, r, got)
	newest, err := LoadFieldRun("")
	require.NoError(t, err)
	assert.Equal(t, r, newest)
	runs, err := FieldRuns()
	require.NoError(t, err)
	require.Len(t, runs, 1)
	assert.Equal(t, r.Name, runs[0].Name)
	assert.Equal(t, r.Addr, runs[0].Addr)

	_, _, err = FollowFieldRun(t.Context(), FollowQuery{Run: r.Name, Step: "0.x"})
	assert.ErrorContains(t, err, `bad step "0.x"`)
	_, _, err = FollowFieldRun(t.Context(), FollowQuery{Run: "../x"})
	assert.ErrorContains(t, err, "bad run")
	_, err = TraceField(t.Context(), FieldQuery{Dir: "testdata", Only: "both"}, "fieldcases:main.go:26")
	assert.ErrorContains(t, err, `only "both"`)
}

// A run saved as a window.TRACE script by an older dotweavier still lists and
// loads; one saved with --only origin had its lifecycle dropped, and loads as Only origin.
func TestLegacyFieldRunLoads(t *testing.T) {
	isolate(t)
	dir := filepath.Join(os.Getenv("HOME"), ".dotweavier", "trace-field-history")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	old := `window.TRACE = {"addr":"golang/orders:a.go:3","open":4,"origin":[{"kind":"seed","loc":"a.go:3"}],"uses":null,"dir":"/work/samples","maxProjects":3};` + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "20250101-120000_golang_orders_a.go_3.js"), []byte(old), 0o644))
	runs, err := FieldRuns()
	require.NoError(t, err)
	require.Len(t, runs, 1)
	assert.Equal(t, "golang/orders:a.go:3", runs[0].Addr)
	r, err := LoadFieldRun(runs[0].Name)
	require.NoError(t, err)
	assert.Equal(t, "origin", r.Only)
	assert.Equal(t, "/work/samples", r.Dir)
	assert.Equal(t, 3, r.MaxProjects)
	assert.Equal(t, "a.go:3", r.Origin[0].Loc)
}

// Following one hop of a kept run, then every hop, each keeps a new run that
// reads back with the hop followed, the options kept, and no handler traced twice.
func TestFollowFieldRun(t *testing.T) {
	isolate(t)
	_, err := InitWorkspace("../../samples-monorepo", "")
	require.NoError(t, err)
	require.NoError(t, Index(t.Context(), IndexOptions{Names: []string{"golang/orders", "golang/pricing"}}))
	r, err := TraceField(t.Context(), FieldQuery{Dir: "../../samples-monorepo", MaxProjects: 2, Keep: true}, "golang/orders:internal/pricer/pricer.go:23")
	require.NoError(t, err)

	path := hopPath(r.Uses, nil)
	require.NotNil(t, path, "no hop in %v", hopsIn(r.Uses))
	step := strings.Trim(strings.Join(strings.Fields(fmt.Sprint(path)), "."), "[]")
	one, _, err := FollowFieldRun(t.Context(), FollowQuery{Run: r.Name, Step: step})
	require.NoError(t, err)
	got, err := LoadFieldRun(one.Name)
	require.NoError(t, err)
	assert.Equal(t, 2, got.MaxProjects)
	assert.Equal(t, r.Dir, got.Dir)
	s, err := stepAt(got.Uses, path)
	require.NoError(t, err)
	assert.True(t, s.Hops[0].Followed, "hop at %s not followed", step)

	all, _, err := FollowFieldRun(t.Context(), FollowQuery{Run: one.Name, Depth: 2})
	require.NoError(t, err)
	for addr, n := range tracedHandlers(all.Uses) {
		assert.Equal(t, 1, n, "handler %s traced %d times", addr, n)
	}
}

// A run traced with --only origin still follows its lifecycle hops.
func TestFollowRunTracedWithOnlyOrigin(t *testing.T) {
	isolate(t)
	_, err := InitWorkspace("../../samples-monorepo", "")
	require.NoError(t, err)
	require.NoError(t, Index(t.Context(), IndexOptions{Names: []string{"golang/orders", "golang/pricing"}}))
	r, err := TraceField(t.Context(), FieldQuery{Dir: "../../samples-monorepo", MaxProjects: 2, Only: "origin", Keep: true}, "golang/orders:internal/pricer/pricer.go:23")
	require.NoError(t, err)
	next, _, err := FollowFieldRun(t.Context(), FollowQuery{Run: r.Name})
	require.NoError(t, err)
	got, err := LoadFieldRun(next.Name)
	require.NoError(t, err)
	assert.Equal(t, "origin", got.Only)
	assert.NotEmpty(t, followedIn(got.Uses), "follow kept a run with no hop followed")
}

// hopPath is the path of the first step under ss with an unfollowed first hop, or nil.
func hopPath(ss []*FieldStep, path []int) []int {
	for i, s := range ss {
		p := append(append([]int(nil), path...), i)
		if len(s.Hops) > 0 && !s.Hops[0].Followed {
			return p
		}
		if q := hopPath(s.Kids, p); q != nil {
			return q
		}
	}
	return nil
}

// followedIn lists the address of every followed hop under ss.
func followedIn(ss []*FieldStep) []string {
	var out []string
	for _, s := range ss {
		for _, h := range s.Hops {
			if h.Followed {
				out = append(out, h.Addr)
			}
		}
		out = append(out, followedIn(s.Kids)...)
	}
	return out
}

// tracedHandlers counts, per handler address, the grafted hop steps under ss that carry a trace.
func tracedHandlers(ss []*FieldStep) map[string]int {
	n := map[string]int{}
	var walk func(ss []*FieldStep)
	walk = func(ss []*FieldStep) {
		for _, s := range ss {
			for _, h := range s.Hops {
				for _, k := range s.Kids {
					if h.Followed && k.Loc == h.Addr && k.Kind == h.Kind && len(k.Kids) > 0 {
						n[h.Addr]++
					}
				}
			}
			walk(s.Kids)
		}
	}
	walk(ss)
	return n
}

// A handler followed by one FollowFieldRun is not traced again by the next,
// though other steps reach it: here three steps call GET /customers/{}.
func TestFollowDoesNotRetraceHandler(t *testing.T) {
	isolate(t)
	_, err := InitWorkspace("../../samples-monorepo", "")
	require.NoError(t, err)
	require.NoError(t, Index(t.Context(), IndexOptions{Names: []string{"kotlin/shipping", "golang/orders"}}))
	r, err := TraceField(t.Context(), FieldQuery{Dir: "../../samples-monorepo", Keep: true}, "kotlin/shipping:src/main/kotlin/com/example/shipping/customers/CustomerDirectory.kt:26")
	require.NoError(t, err)
	path := hopPath(r.Uses, nil)
	require.NotNil(t, path, "no hop in %v", hopsIn(r.Uses))
	step := strings.Trim(strings.Join(strings.Fields(fmt.Sprint(path)), "."), "[]")
	one, _, err := FollowFieldRun(t.Context(), FollowQuery{Run: r.Name, Step: step})
	require.NoError(t, err)
	all, _, err := FollowFieldRun(t.Context(), FollowQuery{Run: one.Name})
	require.NoError(t, err)
	assert.Len(t, followedIn(all.Uses), 3, "every step calling the handler is marked")
	for addr, n := range tracedHandlers(all.Uses) {
		assert.Equal(t, 1, n, "handler %s traced %d times", addr, n)
	}
}
