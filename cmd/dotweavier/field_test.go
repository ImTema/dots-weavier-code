package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dotweavier/internal/domain"
)

func TestTraceFieldLifecycle(t *testing.T) {
	ws, err := filepath.Abs("testdata")
	if err != nil {
		t.Fatal(err)
	}
	r, err := domain.TraceField(context.Background(), domain.FieldQuery{Dir: ws, MaxProjects: 1}, "fieldproj:main.go:16") // func convert(raw string)
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	lifecycle(&b, r, 0)
	got := b.String()
	want := []string{
		"└─ [arg] main.go:44  req, _ := convert(\"42\")  : string",
		"[seed] main.go:16  func convert(raw string) (*Request, error) {  : string  in convert",
		"[transform] main.go:19  n, err := strconv.Atoi(raw)  : int",
		"[field] main.Request.ID : *main.ID",
		"[read] main.go:30  return Loan{ID: *r.ID}",
		"[read] main.go:35  res.ID = keep(l.ID)",
		"[field] main.Loan.ID : main.ID",
		"[arg] main.go:39  func keep(id ID) ID { return id }",
		"[field] main.Result.ID : main.ID",
		"[overwritten] main.go:47  req.ID = &fresh",
	}
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q in\n%s", w, got)
		}
	}
	// keep is entered only from process, so its return must not reach unrelated().
	if strings.Contains(got, "main.go:41") {
		t.Errorf("return leaked to an unrelated caller:\n%s", got)
	}
}

// The domain returns warnings; the CLI prints them to stderr with the old text.
func TestTraceFieldPrintsWarnings(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // no index: call paths are skipped
	ws, err := filepath.Abs("testdata")
	if err != nil {
		t.Fatal(err)
	}
	// Separate pipes, each read meanwhile: a full pipe would block the trace.
	capture := func() (*os.File, chan []byte) {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan []byte)
		go func() { b, _ := io.ReadAll(r); done <- b }()
		return w, done
	}
	errW, errC := capture()
	outW, outC := capture()
	stderr, stdout := os.Stderr, os.Stdout
	os.Stderr, os.Stdout = errW, outW
	runTraceField(context.Background(), []string{"fieldproj:main.go:16", "--project", ws, "--max-projects", "1"})
	os.Stderr, os.Stdout = stderr, stdout
	errW.Close()
	outW.Close()
	out := <-errC
	if tree := <-outC; !strings.Contains(string(tree), "[seed] main.go:16") {
		t.Errorf("stdout has no tree:\n%s", tree)
	}
	for _, want := range []string{"loaded fieldproj: pkgs=", "call paths skipped: seed is not in the index (run: dotweavier index "} {
		if !strings.Contains(string(out), "\n"+want) && !strings.HasPrefix(string(out), want) {
			t.Errorf("missing line %q in\n%s", want, out)
		}
	}
}
