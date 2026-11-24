package domain

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestGraphGolden pins graph.go end to end over samples/: what extract stores per
// project, the hops join adds between projects, and the paths trace ranks. Nodes
// are written by name, not number, so the files survive reordering. Run with
// -update after an intended change.
func TestGraphGolden(t *testing.T) {
	// The store lives under HOME; keep Go's caches where they are, or packages
	// re-downloads every module into a temp dir it then cannot delete.
	out, err := exec.Command("go", "env", "GOPATH", "GOMODCACHE", "GOCACHE").Output()
	if err != nil {
		t.Fatal(err)
	}
	for i, v := range strings.Fields(string(out)) {
		t.Setenv([]string{"GOPATH", "GOMODCACHE", "GOCACHE"}[i], v)
	}
	t.Setenv("HOME", t.TempDir())
	if err := Index("../../samples", nil, func(p, _ string, err error) {
		if err != nil {
			t.Errorf("index %s: %v", p, err)
		}
	}); err != nil {
		t.Fatal(err)
	}
	ws, _ := filepath.Abs("../../samples")
	files, err := filepath.Glob(filepath.Join(storesDir(), "*.db"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no stores: %v", err)
	}
	for _, f := range files {
		s, err := load(f)
		if err != nil {
			t.Fatal(err)
		}
		golden(t, strings.ReplaceAll(s.Project, "/", "_")+".txt", dumpGraph(&s.Graph, false))
	}

	g, _, err := loadIndexes(ws)
	if err != nil {
		t.Fatal(err)
	}
	g.join()
	golden(t, "joined.txt", dumpGraph(g, true))

	var b strings.Builder
	for _, c := range []struct {
		a, b  string
		tests bool
	}{
		{"golang/orders::Handler.placeOrder", "golang/pricing::TierRule.Discount", false},
		{"golang/orders::Service.Place", "golang/orders::SMS.Notify", false},
		{"golang/orders::Service.Place", "golang/orders::Client.Reserve", false},
		{"golang/pricing::Server.Quote", "golang/pricing::Client.Rate", false},
		{"golang/orders::Service.Place", "golang/orders::Publisher.OrderPlaced", false},
		{"golang/pricing::Subscriber.Run", "golang/pricing::Ledger.Record", false},
		{"golang/orders::Service.Place", "golang/pricing::Subscriber.Handle", false},
		{"golang/orders::Service.Place", "java/inventory::StockLedger.reserve", false},
		{"kotlin/shipping::ShippingRates.quote", "golang/orders::Handler.getCustomer", false},
		{"golang/orders::Handler.placeOrder", "golang/pricing::TierRule.Discount", true},
		{"golang/orders:internal/pricer/pricer.go:23", "golang/pricing::TierRule.Discount", false},
	} {
		fmt.Fprintf(&b, "== %s -> %s tests=%v\n", c.a, c.b, c.tests)
		r, err := TracePath(ws, c.a, c.b, 10, c.tests)
		if err != nil {
			fmt.Fprintf(&b, "error: %v\n\n", err)
			continue
		}
		fmt.Fprintf(&b, "A=%s:%d B=%s:%d exhausted=%v\n", r.A, r.A.Line, r.B, r.B.Line, r.Exhausted)
		for _, p := range r.Paths {
			b.WriteString(p.String() + "\n")
		}
		b.WriteString("\n")
	}
	golden(t, "traces.txt", b.String())
}

// dumpGraph writes g as sorted lines; hopsOnly keeps just the edges join adds.
func dumpGraph(g *graph, hopsOnly bool) string {
	var ls []string
	for i, es := range g.Out {
		if !hopsOnly {
			n := g.Nodes[i]
			ls = append(ls, fmt.Sprintf("node %s %d-%d test=%v", n, n.Line, n.End, n.Test))
		}
		for _, e := range es {
			if !hopsOnly || e.Kind.link() {
				ls = append(ls, fmt.Sprintf("edge %s -%s %s-> %s", g.Nodes[i], e.Kind, e.Method, g.Nodes[e.To]))
			}
		}
	}
	if !hopsOnly {
		for _, c := range g.Calls {
			ls = append(ls, fmt.Sprintf("call %s %s ext=%s", g.Nodes[c.From], c.Method, c.Ext))
		}
		for m, hs := range g.Impls {
			for _, h := range hs {
				ls = append(ls, fmt.Sprintf("impl %s <- %s", m, g.Nodes[h]))
			}
		}
	}
	slices.Sort(ls)
	return strings.Join(ls, "\n") + "\n"
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	file := filepath.Join("testdata", "graph_golden", name)
	if *update {
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal([]byte(got), want) {
		t.Errorf("%s changed; diff it against %s.got (go test -run TestGraphGolden -update rewrites it)", file, file)
		_ = os.WriteFile(file+".got", []byte(got), 0o644)
	}
}
