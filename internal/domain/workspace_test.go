package domain

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// Indexing a project outside every workspace registers its parent folder; a
// project folder has one index, shared by nested workspaces under their own
// names; an index whose folder is gone is invalid in every workspace containing it.
func TestWorkspaceRegistryAndInvalid(t *testing.T) {
	out, err := exec.Command("go", "env", "GOPATH", "GOMODCACHE", "GOCACHE").Output()
	if err != nil {
		t.Fatal(err)
	}
	for i, v := range strings.Fields(string(out)) {
		t.Setenv([]string{"GOPATH", "GOMODCACHE", "GOCACHE"}[i], v)
	}
	t.Setenv("HOME", t.TempDir())
	ws := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(ws, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("svc/a/go.mod", "module a\n\ngo 1.22\n")
	write("svc/a/a.go", "package a\n\nfunc A() {}\n")
	write("svc/a/api/go.mod", "module a/api\n\ngo 1.22\n")
	write("svc/a/api/api.go", "package api\n\nfunc B() {}\n")

	if _, err := IndexProject(filepath.Join(ws, "svc"), func(string, string, error) {}); err == nil {
		t.Error("indexing a folder without a manifest: want an error")
	}
	var indexed []string
	got, err := IndexProject(filepath.Join(ws, "svc/a"), func(p, _ string, err error) {
		if err != nil {
			t.Errorf("index %s: %v", p, err)
		}
		indexed = append(indexed, p)
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(ws, "svc"); got != want {
		t.Errorf("workspace %s, want the parent %s", got, want)
	}
	slices.Sort(indexed)
	if !slices.Equal(indexed, []string{"a", "a/api"}) {
		t.Errorf("indexed %v, want the project and its module", indexed)
	}

	// Workspaces nest. The outer one sees the same index under its own name for the project.
	if _, _, err := InitWorkspace(ws); err != nil {
		t.Fatal(err)
	}
	svc := filepath.Join(ws, "svc")
	if all, _ := Workspaces(); !slices.Equal(all, []string{ws, svc}) {
		t.Errorf("workspaces %v", all)
	}
	if w, _ := workspaceFor(filepath.Join(ws, "svc/a")); w != svc {
		t.Errorf("workspaceFor = %s, want the deepest", w)
	}
	for w, want := range map[string]string{svc: "a", ws: "svc/a"} {
		g, loaded, err := loadIndexes(w)
		if err != nil {
			t.Fatal(err)
		}
		if !loaded[want] || len(loaded) != 2 {
			t.Errorf("%s loads %v, want %s and its module", w, loaded, want)
		}
		for _, n := range g.Nodes {
			if n.Name == "A" && n.Project != want {
				t.Errorf("%s names A's project %q, want %q", w, n.Project, want)
			}
		}
	}
	if files, _ := filepath.Glob(filepath.Join(storesDir(), "*.db")); len(files) != 2 {
		t.Errorf("index files %v, want one per project folder", files)
	}

	// An index whose folder is gone is invalid in every workspace that contains it.
	if err := save(&stored{Project: "gone", Dir: filepath.Join(svc, "gone"), Built: time.Now(),
		Graph: graph{ids: map[string]int32{}, Impls: map[string][]int32{}}}); err != nil {
		t.Fatal(err)
	}
	outer, err := LoadOverview(ws)
	if err != nil {
		t.Fatal(err)
	}
	if inv, _ := outer.Invalid(); !slices.Equal(inv, []string{"svc/gone"}) {
		t.Errorf("outer invalid %v, want [svc/gone]", inv)
	}
	src, err := LoadOverview(svc)
	if err != nil {
		t.Fatal(err)
	}
	if inv, _ := src.Invalid(); !slices.Equal(inv, []string{"gone"}) {
		t.Errorf("invalid %v, want [gone]", inv)
	}
	if removed, err := src.RemoveInvalid(); err != nil || !slices.Equal(removed, []string{"gone"}) {
		t.Errorf("removed %v, %v", removed, err)
	}
	if _, err := os.Stat(storePath(filepath.Join(svc, "gone"))); err == nil {
		t.Error("gone's index still exists")
	}
	if _, ok := indexBuilt(filepath.Join(svc, "a")); !ok {
		t.Error("removing invalid indexes removed a valid one")
	}
}
