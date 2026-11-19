package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func TestTraceFieldLifecycle(t *testing.T) {
	dir, err := filepath.Abs("testdata/fieldproj")
	if err != nil {
		t.Fatal(err)
	}
	p := project{Name: "fieldproj", dir: dir}
	tr, err := traceField([]project{p}, p, filepath.Join(dir, "main.go"), 16, false, 1) // func convert(raw string)
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	(&printer{w: &b, t: tr, home: p, src: map[string][]string{}}).lifecycle()
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
