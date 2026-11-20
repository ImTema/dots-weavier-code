package domain

import "golang.org/x/tools/go/ssa"

// STUB: commit 3d17ac9 calls pubsubEnds and uses topicKey/typeKey, but the
// pubsub.go that defines them was never committed. This no-op only lets the
// package build: no pubsub hops are extracted. Replace it with the real file.
const (
	topicKey = "topic "
	typeKey  = "type "
)

func pubsubEnds(g *graph, fns map[*ssa.Function]bool, nodeOf func(*ssa.Function) int32, keep func(*ssa.Function) bool) {
}
