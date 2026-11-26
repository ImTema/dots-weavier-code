package domain

import (
	"context"
	"database/sql"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// isolate gives the test its own HOME, keeping Go's caches so packages does not re-download every module.
func isolate(t *testing.T) {
	t.Helper()
	out, err := exec.Command("go", "env", "GOPATH", "GOMODCACHE", "GOCACHE").Output()
	require.NoError(t, err)
	for i, v := range strings.Fields(string(out)) {
		t.Setenv([]string{"GOPATH", "GOMODCACHE", "GOCACHE"}[i], v)
	}
	t.Setenv("HOME", t.TempDir())
}

// goProject writes a one-file Go project named name under dir and returns its folder.
func goProject(t *testing.T, dir, name string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	require.NoError(t, os.MkdirAll(p, 0o755))
	for f, body := range map[string]string{
		"go.mod":     "module " + name + "\n\ngo 1.22\n",
		name + ".go": "package " + name + "\n\nfunc A() {}\n",
	} {
		require.NoError(t, os.WriteFile(filepath.Join(p, f), []byte(body), 0o644))
	}
	return p
}

// stderrOf runs f and returns what it wrote to os.Stderr.
func stderrOf(t *testing.T, f func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	require.NoError(t, err)
	old := os.Stderr
	os.Stderr = w
	done := make(chan string)
	go func() { b, _ := io.ReadAll(r); done <- string(b) }()
	f()
	os.Stderr = old
	w.Close()
	return <-done
}

// hasPrefix reports whether one of ss starts with prefix.
func hasPrefix(ss []string, prefix string) bool {
	for _, s := range ss {
		if strings.HasPrefix(s, prefix) {
			return true
		}
	}
	return false
}

func TestCancelledFieldTraceStops(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := TraceField(ctx, FieldQuery{Dir: "testdata"}, "fieldcases:main.go:26")
	assert.ErrorIs(t, err, context.Canceled)
}

func TestCancelledJVMFieldTraceStops(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := TraceField(ctx, FieldQuery{Dir: "testdata"}, "jvmcases/java:src/main/java/cases/Cases.java:9")
	assert.ErrorIs(t, err, context.Canceled)
}

func TestCancelledIndexWritesNoIndex(t *testing.T) {
	isolate(t)
	dir := goProject(t, t.TempDir(), "a")
	_, err := InitWorkspace(filepath.Dir(dir), "")
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	assert.ErrorIs(t, Index(ctx, IndexOptions{}), context.Canceled)
	_, ok := indexBuilt(dir)
	assert.False(t, ok, "a cancelled index wrote an index")
}

// A module whose only file is build-tagged out (a tools.go module) loads no packages; that is an empty index, not an error.
func TestBuildTaggedOnlyModuleIndexesEmpty(t *testing.T) {
	isolate(t)
	dir := filepath.Join(t.TempDir(), "tools")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	for f, body := range map[string]string{
		"go.mod":   "module tools\n\ngo 1.22\n",
		"tools.go": "//go:build tools\n\npackage tools\n",
	} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, f), []byte(body), 0o644))
	}
	_, err := InitWorkspace(filepath.Dir(dir), "")
	require.NoError(t, err)
	var errs []error
	require.NoError(t, Index(t.Context(), IndexOptions{Progress: func(r IndexResult) {
		if r.Err != nil {
			errs = append(errs, r.Err)
		}
	}}))
	assert.Empty(t, errs)
	_, ok := indexBuilt(dir)
	assert.True(t, ok, "no index written, so sync would retry it forever")
}

// A field trace reports what it skipped in its result and prints nothing.
func TestFieldTraceReturnsWarnings(t *testing.T) {
	isolate(t) // no index: the seed has no call paths
	var r *FieldRun
	var err error
	out := stderrOf(t, func() {
		r, err = TraceField(t.Context(), FieldQuery{Dir: "testdata"}, "fieldcases:main.go:26")
	})
	require.NoError(t, err)
	assert.Empty(t, out, "domain wrote to stderr")
	assert.True(t, hasPrefix(r.Warnings, "call paths skipped: seed is not in the index (run: dotweavier index "), "warnings %q", r.Warnings)
	assert.True(t, hasPrefix(r.Warnings, "loaded fieldcases: pkgs="), "warnings %q", r.Warnings)
}

// Projects importing a reached shared package past MaxProjects are named in a warning instead of loaded.
func TestFieldTraceWarnsAboutProjectsNotLoaded(t *testing.T) {
	isolate(t)
	r, err := TraceField(t.Context(), FieldQuery{Dir: "../../samples-monorepo/golang", MaxProjects: 1}, "orders:internal/pricer/pricer.go:23")
	require.NoError(t, err)
	assert.True(t, hasPrefix(r.Warnings, "not loaded (--max-projects 1): "), "warnings %q", r.Warnings)
}

// An index that cannot be read is skipped with a warning; the trace runs over the rest.
func TestPathTraceWarnsAboutUnreadableIndex(t *testing.T) {
	isolate(t)
	ws := t.TempDir()
	goProject(t, ws, "a")
	b := goProject(t, ws, "b")
	_, err := InitWorkspace(ws, "")
	require.NoError(t, err)
	require.NoError(t, Index(t.Context(), IndexOptions{}))
	db, err := sql.Open("sqlite", mustStorePath(t, b))
	require.NoError(t, err)
	_, err = db.Exec(`DROP TABLE nodes`) // meta still names b's folder
	require.NoError(t, err)
	db.Close()
	r, err := TracePath(PathQuery{A: "a::A", B: "a::A", MaxPaths: 1})
	require.NoError(t, err)
	if assert.Len(t, r.Warnings, 1) {
		assert.True(t, strings.HasPrefix(r.Warnings[0], "skip index "+mustStorePath(t, b)+": "), "warnings %q, want b's index skipped", r.Warnings)
	}
	assert.Equal(t, 1, r.Indexes, "want a's index only")
}

// Without a home folder dotweavier fails instead of writing .dotweavier into the folder it runs in.
func TestNoHomeFolderIsAnError(t *testing.T) {
	isolate(t)
	t.Setenv("HOME", "")
	dir := goProject(t, t.TempDir(), "a")
	t.Chdir(t.TempDir())
	assert.Error(t, IndexProject(t.Context(), dir, "", nil), "indexing without a home folder")
	assert.NoDirExists(t, ".dotweavier", "wrote .dotweavier into the current folder")
}
