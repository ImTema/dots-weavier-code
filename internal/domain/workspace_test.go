package domain

import (
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// A workspace is a name with registered folders. Indexing a project outside every
// folder of the workspace registers its parent folder there, creating the
// workspace; a project folder has one index, shared by every workspace that
// contains it; an index whose folder is gone is invalid in every such workspace.
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
	write("other/b/go.mod", "module b\n\ngo 1.22\n")
	write("other/b/b.go", "package b\n\nfunc B() {}\n")
	write("x/c/go.mod", "module c\n\ngo 1.22\n")
	write("y/c/go.mod", "module c\n\ngo 1.22\n")
	svc := filepath.Join(ws, "svc")
	noop := func(string, string, error) {}

	if err := IndexProject(filepath.Join(ws, "svc"), "", noop); err == nil {
		t.Error("indexing a folder without a manifest: want an error")
	}
	if _, err := TracePath("", "a::A", "a::A", 1, false); err == nil {
		t.Error("tracing in a workspace that does not exist: want an error")
	}
	var indexed []string
	if err := IndexProject(filepath.Join(ws, "svc/a"), "", func(p, _ string, err error) {
		if err != nil {
			t.Errorf("index %s: %v", p, err)
		}
		indexed = append(indexed, p)
	}); err != nil {
		t.Fatal(err)
	}
	slices.Sort(indexed)
	if !slices.Equal(indexed, []string{"a", "a/api"}) {
		t.Errorf("indexed %v, want the project and its module", indexed)
	}
	if all, _ := Workspaces(); !slices.Equal(all, []string{DefaultWorkspace}) {
		t.Errorf("workspaces %v, want only the default one", all)
	}
	if fs, _ := folders(DefaultWorkspace); !slices.Equal(fs, []string{svc}) {
		t.Errorf("default folders %v, want the project's parent", fs)
	}

	// A custom workspace is created on first use and reused after. Its folder
	// contains svc, so it names the same index svc/a.
	names, err := InitWorkspace(ws, "team")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(names, "svc/a") {
		t.Errorf("init detected %v, want svc/a among them", names)
	}
	if err := IndexProject(filepath.Join(ws, "svc/a"), "team", noop); err != nil {
		t.Fatal(err)
	}
	if fs, _ := folders("team"); !slices.Equal(fs, []string{ws}) {
		t.Errorf("team folders %v, want the reused %s only", fs, ws)
	}
	if all, _ := Workspaces(); !slices.Equal(all, []string{DefaultWorkspace, "team"}) {
		t.Errorf("workspaces %v", all)
	}
	for name, want := range map[string]string{DefaultWorkspace: "a", "team": "svc/a"} {
		fs, _ := folders(name)
		g, loaded, err := loadIndexes(fs)
		if err != nil {
			t.Fatal(err)
		}
		if !loaded[want] || len(loaded) != 2 {
			t.Errorf("%s loads %v, want %s and its module", name, loaded, want)
		}
		for _, n := range g.Nodes {
			if n.Name == "A" && n.Project != want {
				t.Errorf("%s names A's project %q, want %q", name, n.Project, want)
			}
		}
	}
	if files, _ := filepath.Glob(filepath.Join(storesDir(), "*.db")); len(files) != 2 {
		t.Errorf("index files %v, want one per project folder", files)
	}

	// One workspace holds projects indexed from several folders.
	if err := IndexProject(filepath.Join(ws, "other/b"), "", noop); err != nil {
		t.Fatal(err)
	}
	if fs, _ := folders(DefaultWorkspace); !slices.Equal(fs, []string{filepath.Join(ws, "other"), svc}) {
		t.Errorf("default folders %v, want other and svc", fs)
	}
	if _, loaded, _ := loadIndexes([]string{filepath.Join(ws, "other"), svc}); !loaded["a"] || !loaded["b"] {
		t.Errorf("default loads %v, want a and b", loaded)
	}

	// Two folders of one workspace may not produce one project name.
	if err := IndexProject(filepath.Join(ws, "x/c"), "clash", noop); err != nil {
		t.Fatal(err)
	}
	if err := IndexProject(filepath.Join(ws, "y/c"), "clash", noop); err == nil || !strings.Contains(err.Error(), `two projects named "c"`) {
		t.Errorf("clashing names: got %v", err)
	}

	// An index whose folder is gone is invalid in every workspace that contains it.
	if err := save(&stored{Project: "gone", Dir: filepath.Join(svc, "gone"), Built: time.Now(),
		Graph: graph{ids: map[string]int32{}, Impls: map[string][]int32{}}}); err != nil {
		t.Fatal(err)
	}
	outer, err := LoadOverview("team")
	if err != nil {
		t.Fatal(err)
	}
	if inv, _ := outer.Invalid(); !slices.Equal(inv, []string{"svc/gone"}) {
		t.Errorf("team invalid %v, want [svc/gone]", inv)
	}
	src, err := LoadOverview("")
	if err != nil {
		t.Fatal(err)
	}
	if inv, _ := src.Invalid(); !slices.Equal(inv, []string{"gone"}) {
		t.Errorf("default invalid %v, want [gone]", inv)
	}
	if dir, err := src.FolderOf("b:b.go:3"); err != nil || dir != filepath.Join(ws, "other") {
		t.Errorf("FolderOf(b) = %s, %v", dir, err)
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

// Folders registered before workspaces had names move into the default workspace.
func TestRegistryMigratesFolderWorkspaces(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := os.MkdirAll(dotweavierDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dotweavierDir(), "dotweavier.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE workspaces (id INTEGER PRIMARY KEY, dir TEXT NOT NULL UNIQUE, registered TEXT NOT NULL);
		INSERT INTO workspaces (dir, registered) VALUES ('/w/one', 'x'), ('/w/two', 'y')`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if all, err := Workspaces(); err != nil || !slices.Equal(all, []string{DefaultWorkspace}) {
		t.Errorf("workspaces %v, %v", all, err)
	}
	if fs, _ := folders(DefaultWorkspace); !slices.Equal(fs, []string{"/w/one", "/w/two"}) {
		t.Errorf("default folders %v", fs)
	}
}
