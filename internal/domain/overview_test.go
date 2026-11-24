package domain

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChangedSince(t *testing.T) {
	dir := t.TempDir()
	old := time.Now().Add(-time.Hour)
	write := func(rel string, mtime time.Time) {
		p := filepath.Join(dir, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, nil, 0o644))
		require.NoError(t, os.Chtimes(p, mtime, mtime))
	}
	setDir := func(rel string, mtime time.Time) {
		require.NoError(t, os.Chtimes(filepath.Join(dir, rel), mtime, mtime))
	}
	write("go.mod", old)
	write("pkg/a.go", old)
	write("README.md", time.Now()) // not Go source
	write("nested/go.mod", time.Now())
	write("nested/b.go", time.Now()) // another project
	write("vendor/x/c.go", time.Now())
	for _, d := range []string{".", "pkg", "nested", "vendor", "vendor/x"} {
		setDir(d, old)
	}
	built := old.Add(time.Minute)

	assert.False(t, changedSince(dir, built), "unchanged project reported as changed")
	write("pkg/a.go", time.Now())
	assert.True(t, changedSince(dir, built), "edited .go file not detected")
	write("pkg/a.go", old)
	setDir("pkg", time.Now()) // a file was added or removed in pkg
	assert.True(t, changedSince(dir, built), "directory change not detected")
}

func TestSelectNodesRanksCrossProjectThenDegree(t *testing.T) {
	g := &graph{ids: map[string]int32{}}
	for _, n := range []node{
		{Project: "a", Name: "lonely"}, // 0: no edges
		{Project: "a", Name: "hub"},    // 1: 3 edges inside a
		{Project: "a", Name: "x"},      // 2
		{Project: "a", Name: "y"},      // 3
		{Project: "a", Name: "client"}, // 4: 1 edge, to project b
		{Project: "b", Name: "server"}, // 5: 1 edge, from project a
		{Project: "a", Name: "test", Test: true},
	} {
		g.add(n.Project+n.Name, n)
	}
	g.link(1, 2, direct, "")
	g.link(1, 3, direct, "")
	g.link(2, 1, direct, "")
	g.link(4, 5, grpcHop, "/s/M")
	keep, total := selectNodes(g, map[string]bool{}, 3)
	assert.Equal(t, 6, total, "test node excluded")
	var got []string
	for i, k := range keep {
		if k {
			got = append(got, g.Nodes[i].Name)
		}
	}
	// client and server cross projects; then hub has the most edges.
	assert.Equal(t, []string{"hub", "client", "server"}, got)
}

func TestWeavedPaths(t *testing.T) {
	// Boundary 0, 3, 4. 0 reaches 3 by two call paths; 4 only through 3; 6 is a dead end.
	calls := []int32{0, 1, 1, 2, 2, 3, 0, 5, 5, 3, 3, 4, 0, 6}
	boundary := []bool{0: true, 3: true, 4: true, 7: false}
	assert.Equal(t, []int32{0, 3, 3, 4}, weavedPaths(boundary, calls))
}
