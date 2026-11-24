package domain

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStoreRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	g := graph{ids: map[string]int32{}, Impls: map[string][]int32{}}
	a := g.add("a", node{Project: "p", File: "a.go", Name: "A", Line: 3, End: 9})
	b := g.add("b", node{Project: "p", File: "b_test.go", Name: "B", Test: true})
	c := g.add("c", node{Project: "p", File: "c.go", Name: "C"})
	g.link(a, c, direct, "")
	g.link(a, b, grpcHop, "/s.S/M")
	g.Calls = []grpcCall{{From: a, Method: "/s.S/M", Ext: "ext"}}
	g.Impls["/s.S/M"] = []int32{b, c}
	in := &stored{Project: "org/p", Dir: "/x", Built: time.Now().Round(0), Graph: g}
	require.NoError(t, save(in))
	require.NoError(t, save(in)) // re-index overwrites
	path := mustStorePath(t, "/x")
	out, err := load(path)
	require.NoError(t, err)
	assert.Equal(t, "x.db", filepath.Base(path))
	out.Graph.ids, in.Graph.ids = nil, nil
	assert.Equal(t, in, out)
}

func mustStorePath(t *testing.T, dir string) string {
	t.Helper()
	p, err := storePath(dir)
	require.NoError(t, err)
	return p
}

func mustStoresDir(t *testing.T) string {
	t.Helper()
	d, err := storesDir()
	require.NoError(t, err)
	return d
}
