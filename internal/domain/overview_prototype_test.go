// PROTOTYPE, throwaway (branch prototype/overview).
package domain

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestChangedSince(t *testing.T) {
	dir := t.TempDir()
	old := time.Now().Add(-time.Hour)
	write := func(rel string, mtime time.Time) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	setDir := func(rel string, mtime time.Time) {
		if err := os.Chtimes(filepath.Join(dir, rel), mtime, mtime); err != nil {
			t.Fatal(err)
		}
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

	if changedSince(dir, built) {
		t.Fatal("unchanged project reported as changed")
	}
	write("pkg/a.go", time.Now())
	if !changedSince(dir, built) {
		t.Fatal("edited .go file not detected")
	}
	write("pkg/a.go", old)
	setDir("pkg", time.Now()) // a file was added or removed in pkg
	if !changedSince(dir, built) {
		t.Fatal("directory change not detected")
	}
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
	if total != 6 {
		t.Fatalf("total = %d, want 6 (test node excluded)", total)
	}
	var got []string
	for i, k := range keep {
		if k {
			got = append(got, g.Nodes[i].Name)
		}
	}
	// client and server cross projects; then hub has the most edges.
	if want := []string{"hub", "client", "server"}; !slices.Equal(got, want) {
		t.Fatalf("kept %v, want %v", got, want)
	}
}
