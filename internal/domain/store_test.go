package domain

import (
	"path/filepath"
	"reflect"
	"testing"
	"time"
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
	if err := save(in); err != nil {
		t.Fatal(err)
	}
	if err := save(in); err != nil { // re-index overwrites
		t.Fatal(err)
	}
	path := mustStorePath(t, "/x")
	out, err := load(path)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != "x.db" {
		t.Errorf("path %s", path)
	}
	out.Graph.ids, in.Graph.ids = nil, nil
	if !reflect.DeepEqual(in, out) {
		t.Errorf("round trip mismatch:\n in %+v\nout %+v", in, out)
	}
}

func mustStorePath(t *testing.T, dir string) string {
	t.Helper()
	p, err := storePath(dir)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func mustStoresDir(t *testing.T) string {
	t.Helper()
	d, err := storesDir()
	if err != nil {
		t.Fatal(err)
	}
	return d
}
