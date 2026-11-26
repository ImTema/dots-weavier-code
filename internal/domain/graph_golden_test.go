package domain

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGraphGolden pins what extract stores, join adds and trace ranks over samples-monorepo/, nodes by name; -update rewrites it.
func TestGraphGolden(t *testing.T) {
	isolate(t)
	_, err := InitWorkspace("../../samples-monorepo", "")
	require.NoError(t, err)
	require.NoError(t, Index(t.Context(), IndexOptions{Progress: func(r IndexResult) {
		assert.NoError(t, r.Err, "index %s", r.Project)
	}}))
	ws, err := filepath.Abs("../../samples-monorepo")
	require.NoError(t, err)
	files, err := filepath.Glob(filepath.Join(mustStoresDir(t), "*.db"))
	require.NoError(t, err)
	require.NotEmpty(t, files, "no stores")
	for _, f := range files {
		s, err := load(f)
		require.NoError(t, err)
		golden(t, strings.ReplaceAll(s.Project, "/", "_")+".txt", dumpGraph(&s.Graph, false))
	}

	g, _, _, err := loadIndexes([]string{ws}, nil)
	require.NoError(t, err)
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
		r, err := TracePath(t.Context(), PathQuery{A: c.a, B: c.b, MaxPaths: 10, IncludeTests: c.tests})
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

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = TracePath(ctx, PathQuery{A: "golang/orders::Service.Place", B: "golang/orders::SMS.Notify", MaxPaths: 10})
	require.ErrorIs(t, err, context.Canceled)
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
		require.NoError(t, os.MkdirAll(filepath.Dir(file), 0o755))
		require.NoError(t, os.WriteFile(file, []byte(got), 0o644))
		return
	}
	want, err := os.ReadFile(file)
	require.NoError(t, err)
	if !assert.Equal(t, string(want), got, "%s changed; diff it against %s.got (go test -run TestGraphGolden -update rewrites it)", file, file) {
		_ = os.WriteFile(file+".got", []byte(got), 0o644)
	}
}
