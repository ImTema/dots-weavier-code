package domain

import (
	"strings"
	"testing"
)

func TestTraceRanksFewerGuessesFirst(t *testing.T) {
	g := &graph{ids: map[string]int32{}, Impls: map[string][]int32{}}
	n := func(name string) int32 { return g.add(name, node{Project: "p", File: "f.go", Name: name}) }
	a, x, y, z, b := n("A"), n("X"), n("Y"), n("Z"), n("B")
	g.link(a, b, guess, "")  // 1 hop, 1 guess
	g.link(a, x, direct, "") // 3 hops, 0 guesses
	g.link(x, y, sure, "")
	g.link(y, b, direct, "")
	g.link(a, z, direct, "") // 2 hops, 0 guesses
	g.link(z, b, grpcHop, "/s.S/M")
	g.link(b, a, direct, "") // cycle must not produce non-simple paths

	paths, exhausted := g.trace(a, b, 10)
	if !exhausted || len(paths) != 3 {
		t.Fatalf("got %d paths, exhausted=%v", len(paths), exhausted)
	}
	want := []string{
		"p:f.go:A -call-> p:f.go:Z -grpc /s.S/M-> p:f.go:B",
		"p:f.go:A -call-> p:f.go:X -iface:sure-> p:f.go:Y -call-> p:f.go:B  (1/1 sure)",
		"p:f.go:A -iface:guess-> p:f.go:B  (0/1 sure)",
	}
	for i, p := range paths {
		if got := g.path(p).String(); got != want[i] {
			t.Errorf("path %d:\n got %s\nwant %s", i, got, want[i])
		}
	}
}

func TestParseAddr(t *testing.T) {
	for in, want := range map[string]address{
		"billing::Service.proxyPolicy":           {Project: "billing", Name: "Service.proxyPolicy"},
		"billing:internal/a/b.go:Server.Approve": {Project: "billing", File: "internal/a/b.go", Name: "Server.Approve"},
		"billing-system:service/x.go:243":        {Project: "billing-system", File: "service/x.go", Line: 243},
	} {
		if got, err := parseAddr(in); err != nil || got != want {
			t.Errorf("parseAddr(%q) = %+v, %v; want %+v", in, got, err, want)
		}
	}
	if _, err := parseAddr("nope"); err == nil || !strings.Contains(err.Error(), "bad address") {
		t.Errorf("want bad address error, got %v", err)
	}
}

func TestJoinPubsub(t *testing.T) {
	g := &graph{ids: map[string]int32{}, Impls: map[string][]int32{}}
	n := func(p, name string) int32 { return g.add(p+name, node{Project: p, File: "f.go", Name: name}) }
	pub, h, dec := n("a", "Pub"), n("b", "Run$1"), n("b", "Handle")
	g.link(h, dec, direct, "")
	g.Calls = []grpcCall{{pub, topicKey + "t", "pubsub"}, {pub, typeKey + "M", "pubsub"}, {pub, typeKey + "N", "pubsub"}}
	g.Impls[topicKey+"t"] = []int32{h}
	g.Impls[typeKey+"M"] = []int32{dec}
	g.join()

	paths, _ := g.trace(pub, dec, 10)
	want := []string{
		"a:f.go:Pub -pubsub topic t-> b:f.go:Run$1 -call-> b:f.go:Handle",
		"a:f.go:Pub -pubsub:guess type M-> b:f.go:Handle",
	}
	if len(paths) != len(want) {
		t.Fatalf("got %d paths, want %d", len(paths), len(want))
	}
	for i, p := range paths {
		if got := g.path(p).String(); got != want[i] {
			t.Errorf("path %d:\n got %s\nwant %s", i, got, want[i])
		}
	}
	if len(g.Out[pub]) != 2 { // unmatched type N adds no external node
		t.Errorf("edges from Pub = %v", g.Out[pub])
	}
}
