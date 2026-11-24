package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	assert.True(t, exhausted)
	assert.Equal(t, []string{
		"p:f.go:A -call-> p:f.go:Z -grpc /s.S/M-> p:f.go:B",
		"p:f.go:A -call-> p:f.go:X -iface:sure-> p:f.go:Y -call-> p:f.go:B  (1/1 sure)",
		"p:f.go:A -iface:guess-> p:f.go:B  (0/1 sure)",
	}, pathStrings(g, paths))
}

func pathStrings(g *graph, paths []*state) []string {
	var out []string
	for _, p := range paths {
		out = append(out, g.path(p).String())
	}
	return out
}

func TestParseAddr(t *testing.T) {
	for in, want := range map[string]address{
		"billing::Service.proxyPolicy":           {Project: "billing", Name: "Service.proxyPolicy"},
		"billing:internal/a/b.go:Server.Approve": {Project: "billing", File: "internal/a/b.go", Name: "Server.Approve"},
		"billing-system:service/x.go:243":        {Project: "billing-system", File: "service/x.go", Line: 243},
	} {
		got, err := parseAddr(in)
		require.NoError(t, err, in)
		assert.Equal(t, want, got, in)
	}
	_, err := parseAddr("nope")
	assert.ErrorContains(t, err, "bad address")
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
	assert.Equal(t, []string{
		"a:f.go:Pub -pubsub topic t-> b:f.go:Run$1 -call-> b:f.go:Handle",
		"a:f.go:Pub -pubsub:guess type M-> b:f.go:Handle",
	}, pathStrings(g, paths))
	assert.Len(t, g.Out[pub], 2, "unmatched type N adds no external node")
}
