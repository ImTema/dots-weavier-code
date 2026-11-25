package domain

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Indexing outside a workspace's folders registers the parent folder, and one index per project folder is shared by every workspace.
func TestWorkspaceRegistryAndInvalid(t *testing.T) {
	isolate(t)
	ws := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(ws, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(body), 0o644))
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
	ctx := t.Context()
	noop := func(IndexResult) {}

	assert.Error(t, IndexProject(ctx, filepath.Join(ws, "svc"), "", noop), "indexing a folder without a manifest")
	_, err := TracePath(PathQuery{A: "a::A", B: "a::A", MaxPaths: 1})
	assert.Error(t, err, "tracing in a workspace that does not exist")
	var indexed []string
	require.NoError(t, IndexProject(ctx, filepath.Join(ws, "svc/a"), "", func(r IndexResult) {
		assert.NoError(t, r.Err, "index %s", r.Project)
		indexed = append(indexed, r.Project)
	}))
	assert.ElementsMatch(t, []string{"a", "a/api"}, indexed, "want the project and its module")
	all, _ := Workspaces()
	assert.Equal(t, []string{DefaultWorkspace}, all)
	fs, _ := folders(DefaultWorkspace)
	assert.Equal(t, []string{svc}, fs, "want the project's parent")

	// A custom workspace is created on first use; its folder contains svc, so it names the same index svc/a.
	names, err := InitWorkspace(ws, "team")
	require.NoError(t, err)
	assert.Contains(t, names, "svc/a")
	require.NoError(t, IndexProject(ctx, filepath.Join(ws, "svc/a"), "team", noop))
	fs, _ = folders("team")
	assert.Equal(t, []string{ws}, fs, "want the reused folder only")
	all, _ = Workspaces()
	assert.Equal(t, []string{DefaultWorkspace, "team"}, all)
	for name, want := range map[string]string{DefaultWorkspace: "a", "team": "svc/a"} {
		fs, _ := folders(name)
		g, loaded, _, err := loadIndexes(fs, nil)
		require.NoError(t, err)
		assert.True(t, loaded[want], "%s loads %v, want %s", name, loaded, want)
		assert.Len(t, loaded, 2, "%s: want %s and its module", name, want)
		for _, n := range g.Nodes {
			if n.Name == "A" {
				assert.Equal(t, want, n.Project, "%s names A's project", name)
			}
		}
	}
	files, _ := filepath.Glob(filepath.Join(mustStoresDir(t), "*.db"))
	assert.Len(t, files, 2, "want one index per project folder")

	// One workspace holds projects indexed from several folders.
	require.NoError(t, IndexProject(ctx, filepath.Join(ws, "other/b"), "", noop))
	fs, _ = folders(DefaultWorkspace)
	assert.Equal(t, []string{filepath.Join(ws, "other"), svc}, fs)
	_, loaded, _, _ := loadIndexes([]string{filepath.Join(ws, "other"), svc}, nil)
	assert.True(t, loaded["a"] && loaded["b"], "default loads %v, want a and b", loaded)

	// A field address resolves from any folder through the registered folders;
	// the innermost registered folder wins.
	// The run traces the registered folder holding the project, named as there.
	elsewhere := t.TempDir()
	dir, addr, err := runFolder(elsewhere, "b.go:3")
	assert.NoError(t, err)
	assert.Equal(t, filepath.Join(ws, "other"), dir)
	assert.Equal(t, "b:b.go:3", addr)
	dir, _, err = runFolder(elsewhere, "api/api.go:3")
	assert.NoError(t, err)
	assert.Equal(t, svc, dir, "want svc, inside the team folder")
	dir, addr, err = runFolder(svc, "a.go:3")
	assert.NoError(t, err)
	assert.Equal(t, svc, dir, "the given folder has the file")
	assert.Equal(t, "a:a.go:3", addr)
	dir, addr, err = runFolder(filepath.Join(svc, "a"), "a.go:3")
	assert.NoError(t, err)
	assert.Equal(t, svc, dir, "inside a project: want the registered folder holding it")
	assert.Equal(t, "a:a.go:3", addr)
	_, _, err = runFolder(elsewhere, "nope.go:3")
	assert.ErrorContains(t, err, "no project has nope.go")

	// Two folders of one workspace may not produce one project name.
	require.NoError(t, IndexProject(ctx, filepath.Join(ws, "x/c"), "clash", noop))
	assert.ErrorContains(t, IndexProject(ctx, filepath.Join(ws, "y/c"), "clash", noop), `two projects named "c"`)

	// An index whose folder is gone is invalid in every workspace that contains it.
	require.NoError(t, save(&stored{Project: "gone", Dir: filepath.Join(svc, "gone"), Built: time.Now(),
		Graph: graph{ids: map[string]int32{}, Impls: map[string][]int32{}}}))
	outer, err := LoadOverview("team")
	require.NoError(t, err)
	inv, _ := outer.Invalid()
	assert.Equal(t, []string{"svc/gone"}, inv)
	src, err := LoadOverview("")
	require.NoError(t, err)
	inv, _ = src.Invalid()
	assert.Equal(t, []string{"gone"}, inv)
	dir, err = src.FolderOf("b:b.go:3")
	assert.NoError(t, err)
	assert.Equal(t, filepath.Join(ws, "other"), dir)
	dir, err = src.FolderOf("b.go:3")
	assert.NoError(t, err, "an unprefixed address resolves by its file")
	assert.Equal(t, filepath.Join(ws, "other"), dir)
	name, err := WorkspaceOf(filepath.Join(svc, "a"))
	assert.NoError(t, err)
	assert.Equal(t, DefaultWorkspace, name)
	name, _ = WorkspaceOf(elsewhere)
	assert.Empty(t, name)
	removed, err := src.RemoveInvalid()
	assert.NoError(t, err)
	assert.Equal(t, []string{"gone"}, removed)
	assert.NoFileExists(t, mustStorePath(t, filepath.Join(svc, "gone")))
	_, ok := indexBuilt(filepath.Join(svc, "a"))
	assert.True(t, ok, "removing invalid indexes removed a valid one")
}

// Folders registered before workspaces had names move into the default workspace.
func TestRegistryMigratesFolderWorkspaces(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := filepath.Join(os.Getenv("HOME"), ".dotweavier")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	db, err := sql.Open("sqlite", filepath.Join(dir, "dotweavier.db"))
	require.NoError(t, err)
	_, err = db.Exec(`CREATE TABLE workspaces (id INTEGER PRIMARY KEY, dir TEXT NOT NULL UNIQUE, registered TEXT NOT NULL);
		INSERT INTO workspaces (dir, registered) VALUES ('/w/one', 'x'), ('/w/two', 'y')`)
	require.NoError(t, err)
	db.Close()
	all, err := Workspaces()
	assert.NoError(t, err)
	assert.Equal(t, []string{DefaultWorkspace}, all)
	fs, _ := folders(DefaultWorkspace)
	assert.Equal(t, []string{"/w/one", "/w/two"}, fs)
}
