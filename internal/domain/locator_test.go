package domain

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The locator turns every address form into a folder, a project, a file and a canonical address, scoped to one workspace.
func TestLocator(t *testing.T) {
	isolate(t)
	root := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(root, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(body), 0o644))
	}
	write("svc/a/go.mod", "module a\n")
	write("svc/a/a.go", "package a\n\nfunc A() {}\n")
	write("svc/a/api/go.mod", "module a/api\n")
	write("svc/a/api/api.go", "package api\n")
	write("svc/j/pom.xml", "<project/>\n")
	write("svc/j/src/J.java", "class J {}\n")
	write("other/b/go.mod", "module b\n")
	write("other/b/b.go", "package b\n")
	write("other/b2/go.mod", "module b2\n")
	write("other/b2/b.go", "package b2\n")
	write("loose/l/go.mod", "module l\n")
	write("loose/l/l.go", "package l\n")
	svc, other, loose := filepath.Join(root, "svc"), filepath.Join(root, "other"), filepath.Join(root, "loose")
	require.NoError(t, register(DefaultWorkspace, svc))
	require.NoError(t, register(DefaultWorkspace, other))
	require.NoError(t, register("team", root)) // contains svc, which default also registers
	require.NoError(t, register("team", svc))  // nested folders of one workspace

	for _, c := range []struct {
		name, ws, dir, addr string
		folder, project     string
		file                string // relative to root; "" for project::Name
		canon               string
	}{
		{"project::Name", "", "", "a::A", svc, "a", "", "a::A"},
		{"project:file:Name", "", "", "a:a.go:A", svc, "a", "svc/a/a.go", "a:a.go:A"},
		{"project:file:LINE", "", "", "a/api:api.go:1", svc, "a/api", "svc/a/api/api.go", "a/api:api.go:1"},
		{"bare path:LINE", "", "", "api/api.go:1", svc, "a", "svc/a/api/api.go", "a:api/api.go:1"},
		{"bare path in a nested module", "", "", "api.go:1", svc, "a/api", "svc/a/api/api.go", "a/api:api.go:1"},
		{"jvm file", "", "", "src/J.java:1", svc, "j", "svc/j/src/J.java", "j:src/J.java:1"},
		{"absolute path", "", "", filepath.Join(svc, "a/a.go") + ":3", svc, "a", "svc/a/a.go", "a:a.go:3"},
		{"dir first, then renamed to the registered folder", "", filepath.Join(svc, "a"), "a.go:3", svc, "a", "svc/a/a.go", "a:a.go:3"},
		{"dir outside every folder", "", loose, "l:l.go:1", loose, "l", "loose/l/l.go", "l:l.go:1"},
		{"dir without the file falls back to the workspace", "", loose, "a.go:3", svc, "a", "svc/a/a.go", "a:a.go:3"},
		{"other workspace names the same project from its deepest folder", "team", "", "a:a.go:3", svc, "a", "svc/a/a.go", "a:a.go:3"},
		{"a name from a shallower folder is accepted and made canonical", "team", "", "svc/a:a.go:3", svc, "a", "svc/a/a.go", "a:a.go:3"},
		{"team sees other through its root folder", "team", "", "other/b:b.go:1", root, "other/b", "other/b/b.go", "other/b:b.go:1"},
	} {
		t.Run(c.name, func(t *testing.T) {
			l := locator{ws: c.ws}
			sp, err := l.locate(c.dir, c.addr)
			require.NoError(t, err)
			assert.Equal(t, c.folder, sp.Folder)
			assert.Equal(t, c.project, sp.Project.Name)
			if c.file == "" {
				assert.Empty(t, sp.File)
			} else {
				assert.Equal(t, filepath.Join(root, c.file), sp.File)
			}
			assert.Equal(t, c.canon, sp.Addr.String())
			assert.Contains(t, sp.Projects, sp.Project, "the project is one of the folder's projects")
			for _, p := range sp.Projects {
				assert.True(t, within(p.dir, sp.Folder), "%s is under %s", p.dir, sp.Folder)
			}
		})
	}

	t.Run("projects are of the file's language", func(t *testing.T) {
		l := locator{}
		sp, err := l.locate("", "j:src/J.java:1")
		require.NoError(t, err)
		for _, p := range sp.Projects {
			assert.Equal(t, "jvm", p.lang, p.Name)
		}
		sp, err = l.locate("", "a::A")
		require.NoError(t, err)
		assert.Len(t, sp.Projects, 3, "no file: every project of the folder")
	})

	for _, c := range []struct{ name, ws, dir, addr, err string }{
		{"bad address", "", "", "a::", "bad address"},
		{"no project has the file", "", "", "nope.go:3", "no project has nope.go:3 in workspace default"},
		{"in several projects", "", "", "b.go:1", "b.go is in several projects (b, b2)"},
		{"unknown project", "", "", "zz::A", "no project has zz::A"},
		{"a project of another workspace only", "", "", "l:l.go:1", "no project has l:l.go:1 in workspace default"},
		{"default does not name a project from team's root", "", "", "svc/a:a.go:3", "no project has svc/a:a.go:3"},
		{"workspace without folders", "nobody", "", "a::A", "no project has a::A"},
		{"missing dir", "", filepath.Join(root, "gone"), "a.go:3", "gone"},
	} {
		t.Run(c.name, func(t *testing.T) {
			l := locator{ws: c.ws}
			_, err := l.locate(c.dir, c.addr)
			assert.ErrorContains(t, err, c.err)
		})
	}

	t.Run("in resolves in exactly one folder, without renaming", func(t *testing.T) {
		var l locator // the zero value works
		sp, err := l.in(root, "svc/a:a.go:3")
		require.NoError(t, err)
		assert.Equal(t, root, sp.Folder)
		assert.Equal(t, "svc/a:a.go:3", sp.Addr.String())
		_, err = l.in(root, "a:a.go:3")
		assert.ErrorContains(t, err, "no project has a:a.go")
	})

	t.Run("a legacy run finds its folder in any workspace", func(t *testing.T) {
		dir, err := legacyRunFolder("nope.go:1")
		assert.ErrorContains(t, err, "no project has nope.go:1 in any workspace")
		assert.Empty(t, dir)
		dir, err = legacyRunFolder("other/b:b.go:1")
		require.NoError(t, err)
		assert.Equal(t, root, dir, "only team names other/b")
	})
}
