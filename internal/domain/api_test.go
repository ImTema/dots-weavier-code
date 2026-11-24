package domain

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// isolate gives the test its own HOME (so its own ~/.dotweavier) while keeping
// Go's caches, or packages re-downloads every module into a temp dir.
func isolate(t *testing.T) {
	t.Helper()
	out, err := exec.Command("go", "env", "GOPATH", "GOMODCACHE", "GOCACHE").Output()
	if err != nil {
		t.Fatal(err)
	}
	for i, v := range strings.Fields(string(out)) {
		t.Setenv([]string{"GOPATH", "GOMODCACHE", "GOCACHE"}[i], v)
	}
	t.Setenv("HOME", t.TempDir())
}

// goProject writes a one-file Go project named name under dir and returns its folder.
func goProject(t *testing.T, dir, name string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	for f, body := range map[string]string{
		"go.mod":     "module " + name + "\n\ngo 1.22\n",
		name + ".go": "package " + name + "\n\nfunc A() {}\n",
	} {
		if err := os.WriteFile(filepath.Join(p, f), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

// stderrOf runs f and returns what it wrote to os.Stderr.
func stderrOf(t *testing.T, f func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	done := make(chan string)
	go func() { b, _ := io.ReadAll(r); done <- string(b) }()
	f()
	os.Stderr = old
	w.Close()
	return <-done
}

func TestCancelledFieldTraceStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := TraceField(ctx, FieldQuery{Dir: "testdata"}, "fieldcases:main.go:26")
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestCancelledIndexWritesNoIndex(t *testing.T) {
	isolate(t)
	dir := goProject(t, t.TempDir(), "a")
	if _, err := InitWorkspace(filepath.Dir(dir), ""); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Index(ctx, IndexOptions{}); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if _, ok := indexBuilt(dir); ok {
		t.Error("a cancelled index wrote an index")
	}
}

// A field trace reports what it skipped in its result and prints nothing.
func TestFieldTraceReturnsWarnings(t *testing.T) {
	isolate(t) // no index: the seed has no call paths
	var r *FieldTrace
	out := stderrOf(t, func() {
		var err error
		if r, err = TraceField(context.Background(), FieldQuery{Dir: "testdata"}, "fieldcases:main.go:26"); err != nil {
			t.Fatal(err)
		}
	})
	if out != "" {
		t.Errorf("domain wrote to stderr: %q", out)
	}
	if !slices.ContainsFunc(r.Warnings, func(w string) bool {
		return strings.HasPrefix(w, "call paths skipped: seed is not in the index (run: dotweavier index ")
	}) {
		t.Errorf("warnings %q, want call paths skipped", r.Warnings)
	}
	if !slices.ContainsFunc(r.Warnings, func(w string) bool { return strings.HasPrefix(w, "loaded fieldcases: pkgs=") }) {
		t.Errorf("warnings %q, want the project's load stats", r.Warnings)
	}
}

// A field reached in a shared package would load the projects importing it; past
// MaxProjects they are named in a warning instead.
func TestFieldTraceWarnsAboutProjectsNotLoaded(t *testing.T) {
	isolate(t)
	r, err := TraceField(context.Background(), FieldQuery{Dir: "../../samples/golang", MaxProjects: 1}, "orders:internal/pricer/pricer.go:23")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(r.Warnings, func(w string) bool { return strings.HasPrefix(w, "not loaded (--max-projects 1): ") }) {
		t.Errorf("warnings %q, want a project not loaded", r.Warnings)
	}
}

// An index that cannot be read is skipped with a warning; the trace runs over the rest.
func TestPathTraceWarnsAboutUnreadableIndex(t *testing.T) {
	isolate(t)
	ws := t.TempDir()
	goProject(t, ws, "a")
	b := goProject(t, ws, "b")
	if _, err := InitWorkspace(ws, ""); err != nil {
		t.Fatal(err)
	}
	if err := Index(context.Background(), IndexOptions{}); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", mustStorePath(t, b))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TABLE nodes`); err != nil { // meta still names b's folder
		t.Fatal(err)
	}
	db.Close()
	r, err := TracePath(PathQuery{A: "a::A", B: "a::A", MaxPaths: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Warnings) != 1 || !strings.HasPrefix(r.Warnings[0], "skip index "+mustStorePath(t, b)+": ") {
		t.Errorf("warnings %q, want b's index skipped", r.Warnings)
	}
	if r.Indexes != 1 {
		t.Errorf("trace ran over %d indexes, want a's only", r.Indexes)
	}
}

// Without a home folder dotweavier has nowhere to keep state: it fails instead of
// writing .dotweavier into the folder it runs in.
func TestNoHomeFolderIsAnError(t *testing.T) {
	isolate(t)
	t.Setenv("HOME", "")
	dir := goProject(t, t.TempDir(), "a")
	t.Chdir(t.TempDir())
	if err := IndexProject(context.Background(), dir, "", nil); err == nil {
		t.Error("indexing without a home folder: want an error")
	}
	if _, err := os.Stat(".dotweavier"); err == nil {
		t.Error("wrote .dotweavier into the current folder")
	}
}
