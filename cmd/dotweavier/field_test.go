package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"dotweavier/internal/domain"
)

func TestTraceFieldLifecycle(t *testing.T) {
	ws, err := filepath.Abs("testdata")
	require.NoError(t, err)
	r, err := domain.TraceField(t.Context(), domain.FieldQuery{Dir: ws, MaxProjects: 1}, "fieldproj:main.go:16") // func convert(raw string)
	require.NoError(t, err)
	var b bytes.Buffer
	lifecycle(&b, r, 0, "")
	got := b.String()
	for _, w := range []string{
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
	} {
		assert.Contains(t, got, w)
	}
	// keep is entered only from process, so its return must not reach unrelated().
	assert.NotContains(t, got, "main.go:41", "return leaked to an unrelated caller")
}

// The domain returns warnings; the CLI prints them to stderr with the old text.
// --only origin leaves the lifecycle out of stdout.
func TestTraceFieldPrintsWarnings(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // no index: call paths are skipped
	ws, err := filepath.Abs("testdata")
	require.NoError(t, err)
	// Separate pipes, each read meanwhile: a full pipe would block the trace.
	capture := func() (*os.File, chan []byte) {
		r, w, err := os.Pipe()
		require.NoError(t, err)
		done := make(chan []byte)
		go func() { b, _ := io.ReadAll(r); done <- b }()
		return w, done
	}
	errW, errC := capture()
	outW, outC := capture()
	stderr, stdout := os.Stderr, os.Stdout
	os.Stderr, os.Stdout = errW, outW
	runTraceField(t.Context(), []string{"fieldproj:main.go:16", "--project", ws, "--max-projects", "1", "--only", "origin"})
	os.Stderr, os.Stdout = stderr, stdout
	errW.Close()
	outW.Close()
	out := "\n" + string(<-errC)
	stdoutText := string(<-outC)
	assert.Contains(t, stdoutText, "[seed] main.go:16", "stdout has no tree")
	assert.NotContains(t, stdoutText, "== lifecycle", "--only origin printed the lifecycle")
	for _, want := range []string{"loaded fieldproj: pkgs=", "call paths skipped: seed is not in the index (run: dotweavier index "} {
		assert.Contains(t, out, "\n"+want, "missing line")
	}
}

// --only prints one tree and leaves the other's heading out; a depth cut names
// the tree's direction.
func TestLifecycleOnlyAndDepth(t *testing.T) {
	chain := func() []*domain.FieldStep { // three steps deep
		return []*domain.FieldStep{{Kind: "seed", Kids: []*domain.FieldStep{{Kind: "arg", Kids: []*domain.FieldStep{{Kind: "arg"}}}}}}
	}
	for _, c := range []struct {
		only       string
		want, miss []string
	}{
		{"", []string{"== origin", "== lifecycle", "… +1 more steps up (raise --depth)", "… +1 more steps down (raise --depth)"}, nil},
		{"origin", []string{"== origin", "… +1 more steps up (raise --depth)"}, []string{"== lifecycle", "steps down"}},
		{"uses", []string{"== lifecycle", "… +1 more steps down (raise --depth)"}, []string{"== origin", "steps up"}},
	} {
		var b bytes.Buffer
		lifecycle(&b, &domain.FieldTrace{Origin: chain(), Uses: chain()}, 1, c.only)
		got := b.String()
		for _, w := range c.want {
			assert.Contains(t, got, w, "only=%q", c.only)
		}
		for _, w := range c.miss {
			assert.NotContains(t, got, w, "only=%q", c.only)
		}
	}
}

// A saved run reads back whole, so following from the page continues it with
// the options it ran with; an export leaves out the local folder.
func TestSavedFieldRunRoundTrips(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	r := &domain.FieldTrace{Addr: "golang/orders:a.go:3", Dir: "/work/samples", IncludeTests: true, MaxProjects: 3,
		Warnings: []string{"loaded golang/orders"},
		Uses:     []*domain.FieldStep{{Kind: "seed", Loc: "a.go:3", Hops: []domain.Hop{{Kind: "grpc", Addr: "golang/pricing:s.go:9"}}}}}
	name, err := saveFieldRun(r, 4)
	require.NoError(t, err)
	got, open, err := loadFieldRun(name)
	require.NoError(t, err)
	assert.Equal(t, r, got)
	assert.Equal(t, 4, open)
	page, err := exportField(r, 4)
	require.NoError(t, err)
	assert.NotContains(t, string(page), r.Dir)
	assert.Contains(t, string(page), r.Addr)
}
